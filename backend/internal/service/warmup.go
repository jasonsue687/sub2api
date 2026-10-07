package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WarmupConfig is stored in the existing account credentials. Old switches remain Mock.
// The key is deliberately a top-level sensitive credential for redaction and merge handling.
type WarmupConfig struct {
	Mode           string
	Protocol       string
	BaseURL        string
	APIKey         string
	Model          string
	TimeoutSeconds int
}

func WarmupConfigFromCredentials(credentials map[string]any) WarmupConfig {
	a := &Account{Credentials: credentials}
	cfg := WarmupConfig{
		Mode: a.GetCredential("warmup_mode"), Protocol: a.GetCredential("warmup_protocol"),
		BaseURL: strings.TrimSpace(a.GetCredential("warmup_base_url")),
		APIKey:  a.GetCredential("warmup_api_key"),
		Model:   strings.TrimSpace(a.GetCredential("warmup_model")), TimeoutSeconds: 30,
	}
	if cfg.Mode == "" {
		cfg.Mode = "mock"
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "openai"
	}
	if v, exists := credentials["warmup_timeout_seconds"]; exists {
		data, _ := json.Marshal(v)
		if json.Unmarshal(data, &cfg.TimeoutSeconds) != nil {
			cfg.TimeoutSeconds = 0
		}
	}
	return cfg
}

func ValidateWarmupCredentials(credentials map[string]any) error {
	cfg := WarmupConfigFromCredentials(credentials)
	invalid := func(message string) error {
		return infraerrors.New(http.StatusBadRequest, "INVALID_WARMUP_CONFIG", message)
	}
	if cfg.Mode != "mock" && cfg.Mode != "forward" {
		return invalid("Warmup mode must be mock or forward")
	}
	if cfg.Protocol != "openai" && cfg.Protocol != "anthropic" {
		return invalid("Warmup protocol must be openai or anthropic")
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 120 {
		return invalid("Warmup timeout must be between 1 and 120 seconds")
	}
	enabled, _ := credentials["intercept_warmup_requests"].(bool)
	if !enabled || cfg.Mode != "forward" {
		return nil
	}
	if cfg.Model == "" || len(cfg.Model) > 200 || strings.ContainsAny(cfg.Model, "\r\n") {
		return invalid("Warmup upstream model is required (maximum 200 characters)")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return invalid("Warmup API key is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid("Warmup Base URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	return nil
}

// ResolveWarmupAccount only reads configuration: no selection, sticky binding, RPM,
// account concurrency or session-capacity registration. A narrowly classified
// desktop title may read its own group's auxiliary configuration even when the
// subscription pool requires Claude Code. This does not establish client identity
// or allow ordinary account scheduling. Other warmups retain client restrictions.
// Enabled configurations use ascending priority, then ascending ID.
func (s *GatewayService) ResolveWarmupAccount(ctx context.Context, groupID *int64, model string, desktopTitle bool) (*Account, error) {
	var group *Group
	var err error
	scopedGroupID := groupID
	if desktopTitle {
		if groupID != nil {
			group, err = s.resolveGroupByID(ctx, *groupID)
		}
	} else {
		group, scopedGroupID, err = s.checkClaudeCodeRestriction(ctx, groupID)
	}
	if err != nil {
		return nil, err
	}
	ctx = s.withGroupContext(ctx, group)
	if s.checkChannelPricingRestriction(ctx, scopedGroupID, model) {
		return nil, ErrNoAvailableAccounts
	}
	platform, forced, err := s.resolvePlatform(ctx, scopedGroupID, group, model)
	if err != nil {
		return nil, err
	}
	accounts, useMixed, err := s.listSchedulableAccounts(ctx, scopedGroupID, platform, forced)
	if err != nil {
		return nil, err
	}
	var selected *Account
	for i := range accounts {
		// Scheduler lists contain metadata only and intentionally omit warmup
		// settings and secrets. Read the full account before testing the switch,
		// then apply admission checks to that same hydrated configuration.
		a, err := s.hydrateSelectedAccount(ctx, &accounts[i])
		if err != nil {
			return nil, err
		}
		if !a.IsInterceptWarmupEnabled() || a.Status != StatusActive || !a.Schedulable || !s.isAccountInGroup(a, scopedGroupID) || !s.isModelSupportedByAccountWithContext(ctx, a, model) || s.isStickyAccountUpstreamRestricted(ctx, scopedGroupID, a, model) {
			continue
		}
		if !s.isAccountAllowedForPlatform(a, platform, useMixed) {
			continue
		}
		if selected == nil || a.Priority < selected.Priority || (a.Priority == selected.Priority && a.ID < selected.ID) {
			selected = a
		}
	}
	return selected, nil
}

// WarmupText accepts only text; tool/image content must never become a title request.
func WarmupText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, block := range blocks {
		if block.Type != "text" && block.Type != "" {
			return "", false
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n"), true
}

type WarmupRequest struct {
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	MaxTokens int `json:"max_tokens"`
}

type WarmupResult struct {
	Text       string
	StopReason string
	Usage      ClaudeUsage
	Duration   time.Duration
}

var warmupHTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (cfg WarmupConfig) Endpoint() string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	if cfg.Protocol == "anthropic" {
		return base + "/messages"
	}
	return base + "/chat/completions"
}

// ForwardWarmup applies the same outbound URL policy as ordinary gateway requests.
func (s *GatewayService) ForwardWarmup(ctx context.Context, cfg WarmupConfig, body []byte) (*WarmupResult, error) {
	if s.cfg == nil {
		return nil, errors.New("warmup outbound policy is unavailable")
	}
	base, err := s.validateUpstreamBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("warmup endpoint is not allowed by the outbound URL policy")
	}
	cfg.BaseURL = base
	return forwardWarmup(ctx, cfg, body)
}

// ForwardWarmup buffers a small non-streaming upstream result, then the handler
// emits either Messages JSON or Messages SSE. Credentials/metadata/session headers
// from the original request are never forwarded. No retries or fallback exist.
func forwardWarmup(ctx context.Context, cfg WarmupConfig, body []byte) (*WarmupResult, error) {
	var source WarmupRequest
	if err := json.Unmarshal(body, &source); err != nil {
		return nil, errors.New("invalid warmup request")
	}
	system, ok := WarmupText(source.System)
	if !ok && len(source.System) > 0 {
		return nil, errors.New("unsupported warmup system content")
	}
	messages := make([]map[string]string, 0, len(source.Messages)+1)
	if cfg.Protocol == "openai" && system != "" {
		messages = append(messages, map[string]string{"role": "system", "content": system})
	}
	for _, message := range source.Messages {
		text, ok := WarmupText(message.Content)
		if !ok {
			return nil, errors.New("unsupported warmup message content")
		}
		messages = append(messages, map[string]string{"role": message.Role, "content": text})
	}
	maxTokens := source.MaxTokens
	if maxTokens <= 0 || maxTokens > 1024 {
		maxTokens = 1024
	}
	payload := map[string]any{"model": cfg.Model, "messages": messages, "stream": false, "max_tokens": maxTokens}
	if cfg.Protocol == "anthropic" && system != "" {
		payload["system"] = system
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("cannot encode warmup request")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint(), bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid warmup endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.Protocol == "anthropic" {
		request.Header.Set("x-api-key", cfg.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	start := time.Now()
	response, err := warmupHTTPClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("warmup upstream connection failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("warmup upstream returned HTTP %d", response.StatusCode)
	}
	const maxResponse = 1 << 20
	data, err = io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot read warmup upstream response")
	}
	if len(data) > maxResponse {
		return nil, errors.New("warmup upstream response too large")
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Choices    []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			InputTokens          *int `json:"input_tokens"`
			OutputTokens         *int `json:"output_tokens"`
			PromptTokens         *int `json:"prompt_tokens"`
			CompletionTokens     *int `json:"completion_tokens"`
			PromptCacheHit       int  `json:"prompt_cache_hit_tokens"`
			CacheRead            int  `json:"cache_read_input_tokens"`
			CacheCreation        int  `json:"cache_creation_input_tokens"`
			CacheCreationDetails struct {
				FiveMinute int `json:"ephemeral_5m_input_tokens"`
				OneHour    int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			PromptDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &decoded) != nil || decoded.Usage == nil {
		return nil, errors.New("invalid warmup upstream response or missing usage")
	}
	result := &WarmupResult{StopReason: "end_turn", Duration: time.Since(start)}
	usage := decoded.Usage
	if cfg.Protocol == "anthropic" {
		if usage.InputTokens == nil || usage.OutputTokens == nil {
			return nil, errors.New("missing warmup upstream token usage")
		}
		for _, block := range decoded.Content {
			if block.Type == "text" {
				result.Text += block.Text
			}
		}
		if decoded.StopReason == "max_tokens" {
			result.StopReason = "max_tokens"
		}
		result.Usage = ClaudeUsage{InputTokens: *usage.InputTokens, OutputTokens: *usage.OutputTokens, CacheReadInputTokens: usage.CacheRead, CacheCreationInputTokens: usage.CacheCreation, CacheCreation5mTokens: usage.CacheCreationDetails.FiveMinute, CacheCreation1hTokens: usage.CacheCreationDetails.OneHour}
	} else {
		if usage.PromptTokens == nil || usage.CompletionTokens == nil {
			return nil, errors.New("missing warmup upstream token usage")
		}
		if len(decoded.Choices) != 1 {
			return nil, errors.New("invalid warmup upstream choices")
		}
		result.Text = decoded.Choices[0].Message.Content
		if decoded.Choices[0].FinishReason == "length" {
			result.StopReason = "max_tokens"
		}
		cached := usage.PromptDetails.CachedTokens
		if cached == 0 {
			cached = usage.PromptCacheHit
		}
		result.Usage = ClaudeUsage{InputTokens: *usage.PromptTokens - cached, OutputTokens: *usage.CompletionTokens, CacheReadInputTokens: cached}
	}
	if strings.TrimSpace(result.Text) == "" || result.Usage.InputTokens < 0 || result.Usage.OutputTokens < 0 || result.Usage.CacheReadInputTokens < 0 || result.Usage.CacheCreationInputTokens < 0 || result.Usage.CacheCreation5mTokens < 0 || result.Usage.CacheCreation1hTokens < 0 {
		return nil, errors.New("invalid warmup upstream text or usage")
	}
	return result, nil
}
