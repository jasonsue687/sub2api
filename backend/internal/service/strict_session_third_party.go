package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const strictThirdPartyErrorBodyLimit = 4 << 10

// StrictThirdPartyStatusError 是独立中转返回的失败。它不是订阅账号池的切换信号。
type StrictThirdPartyStatusError struct {
	StatusCode int
	WroteBody  bool
}

func (e *StrictThirdPartyStatusError) Error() string {
	if e == nil {
		return "strict third party failed"
	}
	if e.WroteBody {
		return fmt.Sprintf("strict third party wrote a partial body before status %d", e.StatusCode)
	}
	return fmt.Sprintf("strict third party status %d", e.StatusCode)
}

// StrictFallbackAction 决定原账号失败后的唯一出口。
type StrictFallbackAction int

const (
	// StrictFallbackPassthrough 把客户端取消、参数错误留给原有错误路径，不转发第三方。
	StrictFallbackPassthrough StrictFallbackAction = iota
	// StrictFallbackThirdParty 把请求发到独立中转，不回到订阅账号池。
	StrictFallbackThirdParty
	// StrictFallbackError 返回可识别错误，供外层 New API 转到指定第三方。
	StrictFallbackError
	// StrictFallbackStreamInterrupted 表示流式内容已经写出，禁止拼接或重放。
	StrictFallbackStreamInterrupted
)

// StrictFallbackInput 是故障出口决策的全部输入。
type StrictFallbackInput struct {
	ResponseWritten   bool
	ClientCanceled    bool
	AccountSide       bool
	RetryableUpstream bool
	ThirdPartyEnabled bool
}

// DecideStrictFallback 把「可回退故障」和「不能重放的故障」分开。
// 任何分支都不会选择另一个订阅账号。
func DecideStrictFallback(in StrictFallbackInput) StrictFallbackAction {
	if in.ResponseWritten {
		return StrictFallbackStreamInterrupted
	}
	if in.ClientCanceled {
		return StrictFallbackPassthrough
	}
	if !in.AccountSide && !in.RetryableUpstream {
		return StrictFallbackPassthrough
	}
	if in.ThirdPartyEnabled {
		return StrictFallbackThirdParty
	}
	return StrictFallbackError
}

func (s *GatewayService) SetStrictThirdPartyHTTPClient(client *http.Client) {
	if s == nil {
		return
	}
	s.strictThirdPartyHTTP = client
}

func (s *GatewayService) strictThirdPartyConfig() (config.GatewayStrictThirdPartyConfig, bool) {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.StrictSessionBinding.Enabled {
		return config.GatewayStrictThirdPartyConfig{}, false
	}
	cfg := s.cfg.Gateway.StrictSessionBinding.ThirdParty
	if !cfg.Enabled || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return cfg, false
	}
	return cfg, true
}

// StrictThirdPartyEnabled 报告进程内独立中转是否可用。
func (s *GatewayService) StrictThirdPartyEnabled() bool {
	_, ok := s.strictThirdPartyConfig()
	return ok
}

func (s *GatewayService) strictThirdPartyHTTPClient() *http.Client {
	if s != nil && s.strictThirdPartyHTTP != nil {
		return s.strictThirdPartyHTTP
	}
	timeout := time.Duration(0)
	if s != nil && s.cfg != nil && s.cfg.Gateway.StrictSessionBinding.ThirdParty.TimeoutSeconds > 0 {
		timeout = time.Duration(s.cfg.Gateway.StrictSessionBinding.ThirdParty.TimeoutSeconds) * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// ForwardStrictThirdParty 把原始 Claude Messages 请求发到独立中转。
// 不使用订阅账号凭据，也不改写会话绑定。
func (s *GatewayService) ForwardStrictThirdParty(ctx context.Context, inbound http.Header, body []byte, w http.ResponseWriter) error {
	cfg, ok := s.strictThirdPartyConfig()
	if !ok {
		return fmt.Errorf("%w: third party is not configured", ErrStrictSessionStore)
	}
	endpoint, err := strictThirdPartyMessagesURL(cfg.BaseURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.APIKey)
	if version := strings.TrimSpace(inbound.Get("anthropic-version")); version != "" {
		req.Header.Set("anthropic-version", version)
	} else {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if beta := strings.TrimSpace(inbound.Get("anthropic-beta")); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	if accept := strings.TrimSpace(inbound.Get("Accept")); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := s.strictThirdPartyHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, strictThirdPartyErrorBodyLimit))
		return &StrictThirdPartyStatusError{StatusCode: resp.StatusCode}
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && w != nil {
		w.Header().Set("Content-Type", contentType)
	}
	if w == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return &StrictThirdPartyStatusError{StatusCode: resp.StatusCode, WroteBody: true}
	}
	return nil
}

func strictThirdPartyMessagesURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u == nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("%w: invalid third party base url", ErrStrictSessionStore)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: third party base url must not embed credentials", ErrStrictSessionStore)
	}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/messages"):
		u.Path = path
	case strings.HasSuffix(path, "/v1"):
		u.Path = path + "/messages"
	default:
		u.Path = path + "/v1/messages"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
