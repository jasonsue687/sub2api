package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var warmupTitleInstruction = regexp.MustCompile(`(?i)^(?:please\s+)?(?:generate|write|create|provide)\b[^\n<]{0,180}\btitle\b`)
var warmupTitleWordCount = regexp.MustCompile(`(?i)\b2\s*[-–—]\s*5\s+words?\b`)

const desktopTitleSystem = "You write short session titles. Reply with only the tagged fields the prompt asks for."

// isDesktopTitleWarmup recognizes the desktop's direct one-shot request, not
// its CLI fallback. The fixed system identifies the task; user text may vary.
func isDesktopTitleWarmup(body []byte, model string, maxTokens int) bool {
	if !isHaikuModel(model) || maxTokens != 200 {
		return false
	}
	var req struct {
		service.WarmupRequest
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) != 1 || req.Messages[0].Role != "user" || len(req.Tools) != 0 {
		return false
	}
	if choice := strings.TrimSpace(string(req.ToolChoice)); choice != "" && choice != "null" {
		return false
	}
	system, ok := desktopTitleText(req.System)
	if !ok || strings.Join(strings.Fields(system), " ") != desktopTitleSystem {
		return false
	}
	_, ok = desktopTitleText(req.Messages[0].Content)
	return ok
}

// desktopTitleText accepts nonempty text strings or explicitly typed text blocks.
// Keep the stricter shape checks local so legacy warmup recognition is unchanged.
func desktopTitleText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, strings.TrimSpace(text) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", false
		}
		parts = append(parts, block.Text)
	}
	text = strings.Join(parts, "\n")
	return text, strings.TrimSpace(text) != ""
}

func parseWarmupText(body []byte) (system, text string, singleUser bool) {
	var req service.WarmupRequest
	if json.Unmarshal(body, &req) != nil || len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		return "", "", false
	}
	system, systemOK := service.WarmupText(req.System)
	text, textOK := service.WarmupText(req.Messages[0].Content)
	return strings.TrimSpace(system), strings.TrimSpace(text), textOK && (systemOK || len(req.System) == 0)
}

func isXMLTitleWarmup(body []byte) bool {
	system, text, ok := parseWarmupText(body)
	if !ok {
		return false
	}
	// Only the instructions outside the description identify this template.
	// Quoted examples inside ordinary conversation text cannot enable the bypass.
	lower := strings.ToLower(text)
	start, end := strings.Index(lower, "<description>"), strings.LastIndex(lower, "</description>")
	if start < 0 || end <= start || strings.Count(lower, "<description>") != 1 {
		return false
	}
	instructions := lower[:start] + lower[end+len("</description>"):]
	system = strings.ToLower(system)
	return warmupTitleInstruction.MatchString(text) && warmupTitleWordCount.MatchString(instructions) &&
		strings.Contains(instructions, "<title>") && strings.Contains(instructions, "</title>") &&
		(strings.Contains(system, "title") || strings.Contains(system, "helpful") || system == "")
}

// handleEarlyWarmup runs after user concurrency, security and billing admission,
// before strict identity resolution and account/session selection.
func (h *GatewayHandler) handleEarlyWarmup(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription, body []byte, model string, stream, streamStarted bool, pricingAt time.Time) bool {
	account, err := h.gatewayService.ResolveWarmupAccount(c.Request.Context(), apiKey.GroupID, model)
	if err != nil {
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "warmup_configuration_error", "Cannot resolve warmup configuration", streamStarted)
		return true
	}
	if account == nil {
		return false
	}
	if err := service.ValidateWarmupCredentials(account.Credentials); err != nil {
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "warmup_configuration_error", "Invalid warmup configuration", streamStarted)
		return true
	}
	cfg := service.WarmupConfigFromCredentials(account.Credentials)
	result := &service.WarmupResult{Text: "New Conversation", StopReason: "end_turn"}
	var source service.WarmupRequest
	_ = json.Unmarshal(body, &source)
	desktopTitle := isDesktopTitleWarmup(body, model, source.MaxTokens)
	if desktopTitle {
		result.Text = "<title>New Conversation</title>"
	}
	system, _ := service.WarmupText(source.System)
	if strings.Contains(system, "nalyze if this message indicates a new conversation topic. If it does, extract a 2-3 word title") {
		result.Text = `{"isNewTopic":true,"title":"New Conversation"}`
	}
	if cfg.Mode == "forward" {
		result, err = h.gatewayService.ForwardWarmup(c.Request.Context(), cfg, body)
		if err != nil {
			status, code := http.StatusBadGateway, "warmup_upstream_error"
			if errors.Is(err, context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, "warmup_upstream_timeout"
			}
			if c.Request.Context().Err() != nil {
				return true
			}
			h.handleStreamingAwareError(c, status, code, err.Error(), streamStarted)
			return true
		}
		// Attribute the log to the configuration owner, with the actual external
		// model and endpoint. Do not apply that subscription account's cache/quotas.
		billingAccount := &service.Account{ID: account.ID, Platform: account.Platform, Type: account.Type}
		forwarded := &service.ForwardResult{
			ExternalWarmup: true,
			RequestID:      generateRealisticMsgID(),
			Model:          cfg.Model, UpstreamModel: cfg.Model,
			Usage: result.Usage, Stream: stream, Duration: result.Duration,
		}
		upstreamEndpoint := "/v1/chat/completions"
		if cfg.Protocol == "anthropic" {
			upstreamEndpoint = "/v1/messages"
		}
		input := &service.RecordUsageInput{
			Result: forwarded, APIKey: apiKey, User: apiKey.User, Account: billingAccount,
			Subscription: subscription, PricingAt: pricingAt,
			InboundEndpoint: "/v1/messages/warmup", UpstreamEndpoint: upstreamEndpoint,
			APIKeyService: h.apiKeyService, QuotaPlatform: service.QuotaPlatform(c.Request.Context(), apiKey),
			ChannelUsageFields: service.ChannelUsageFields{OriginalModel: model},
		}
		h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, input); err != nil {
				logger.L().Error("gateway.warmup_usage_record_failed", zap.Int64("account_id", billingAccount.ID), zap.Error(err))
			}
		})
	}
	// Desktop instructions specify their tagged fields. Preserve the forwarded
	// result, including optional branch tags, rather than wrapping all of it.
	if !desktopTitle && isXMLTitleWarmup(body) {
		title := strings.TrimSpace(result.Text)
		title = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(title, "<title>"), "</title>"))
		// Always return one XML title, escaping any upstream markup.
		title = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(title)
		result.Text = "<title>" + title + "</title>"
	}
	sendWarmupResult(c, model, stream, result)
	return true
}

func sendWarmupResult(c *gin.Context, model string, stream bool, result *service.WarmupResult) {
	usage := gin.H{"input_tokens": result.Usage.InputTokens, "output_tokens": result.Usage.OutputTokens, "cache_read_input_tokens": result.Usage.CacheReadInputTokens, "cache_creation_input_tokens": result.Usage.CacheCreationInputTokens}
	message := gin.H{"id": generateRealisticMsgID(), "type": "message", "role": "assistant", "model": model, "content": []gin.H{{"type": "text", "text": result.Text}}, "stop_reason": result.StopReason, "stop_sequence": nil, "usage": usage}
	if !stream {
		c.JSON(http.StatusOK, message)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	message["content"] = []any{}
	message["stop_reason"] = nil
	usage["output_tokens"] = 0
	events := []gin.H{
		{"type": "message_start", "message": message},
		{"type": "content_block_start", "index": 0, "content_block": gin.H{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 0, "delta": gin.H{"type": "text_delta", "text": result.Text}},
		{"type": "content_block_stop", "index": 0},
		{"type": "message_delta", "delta": gin.H{"stop_reason": result.StopReason, "stop_sequence": nil}, "usage": gin.H{"output_tokens": result.Usage.OutputTokens}},
		{"type": "message_stop"},
	}
	for _, event := range events {
		if c.Request != nil && c.Request.Context().Err() != nil {
			return
		}
		data, _ := json.Marshal(event)
		eventType, ok := event["type"].(string)
		if !ok {
			return
		}
		if _, err := c.Writer.WriteString("event: " + eventType + "\ndata: " + string(data) + "\n\n"); err != nil {
			return
		}
		c.Writer.Flush()
	}
}
