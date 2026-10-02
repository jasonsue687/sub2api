package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	strictThirdPartyDefaultHeaderTimeout = 60 * time.Second
	strictThirdPartyDefaultStallTimeout  = 5 * time.Minute
	strictThirdPartyDialTimeout          = 10 * time.Second
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

func strictThirdPartyTimeouts(timeoutSeconds int) (header, stall time.Duration) {
	if timeoutSeconds > 0 {
		d := time.Duration(timeoutSeconds) * time.Second
		return d, d
	}
	return strictThirdPartyDefaultHeaderTimeout, strictThirdPartyDefaultStallTimeout
}

func (s *GatewayService) strictThirdPartyHTTPClient() *http.Client {
	if s != nil && s.strictThirdPartyHTTP != nil {
		client := s.strictThirdPartyHTTP
		if client.CheckRedirect == nil {
			clone := *client
			clone.CheckRedirect = refuseStrictThirdPartyRedirect
			return &clone
		}
		return client
	}
	headerTimeout, _ := strictThirdPartyTimeouts(0)
	if s != nil && s.cfg != nil {
		headerTimeout, _ = strictThirdPartyTimeouts(s.cfg.Gateway.StrictSessionBinding.ThirdParty.TimeoutSeconds)
	}
	return &http.Client{
		// Timeout 为 0：不按整段响应体计时，避免截断仍在输出的长流。
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout: strictThirdPartyDialTimeout,
			}).DialContext,
			TLSHandshakeTimeout:   strictThirdPartyDialTimeout,
			ResponseHeaderTimeout: headerTimeout,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: refuseStrictThirdPartyRedirect,
	}
}

func refuseStrictThirdPartyRedirect(_ *http.Request, _ []*http.Request) error {
	return errors.New("strict third party redirects are not allowed")
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

	_, stall := strictThirdPartyTimeouts(cfg.TimeoutSeconds)
	resp, err := s.strictThirdPartyHTTPClient().Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, strictThirdPartyErrorBodyLimit))
			_ = resp.Body.Close()
		}
		return err
	}
	upstreamBody := &strictIdleReader{body: resp.Body, idle: stall}
	defer func() { _ = upstreamBody.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(upstreamBody, strictThirdPartyErrorBodyLimit))
		return fmt.Errorf("%w: redirect status %d", ErrStrictSessionStore, resp.StatusCode)
	}
	// 4xx 原样交给客户端。5xx 仍视为中转失败，不把订阅池错误和第三方错误拼在一起。
	if resp.StatusCode >= http.StatusInternalServerError {
		_, _ = io.Copy(io.Discard, io.LimitReader(upstreamBody, strictThirdPartyErrorBodyLimit))
		return &StrictThirdPartyStatusError{StatusCode: resp.StatusCode}
	}
	if w == nil {
		_, _ = io.Copy(io.Discard, upstreamBody)
		return nil
	}
	copyStrictThirdPartyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if err := copyStrictThirdPartyBody(w, upstreamBody); err != nil {
		return &StrictThirdPartyStatusError{StatusCode: resp.StatusCode, WroteBody: true}
	}
	return nil
}

func copyStrictThirdPartyHeaders(dst, src http.Header) {
	if contentType := src.Get("Content-Type"); contentType != "" {
		dst.Set("Content-Type", contentType)
	}
	if requestID := src.Get("request-id"); requestID != "" {
		dst.Set("request-id", requestID)
	}
}

func copyStrictThirdPartyBody(w http.ResponseWriter, r io.Reader) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// strictIdleReader 在两次读取之间空闲过久时关闭底层 body，避免长流被总时长截断，也不无限挂起。
type strictIdleReader struct {
	body   io.ReadCloser
	idle   time.Duration
	mu     sync.Mutex
	timer  *time.Timer
	closed atomic.Bool
}

func (r *strictIdleReader) Read(p []byte) (int, error) {
	if r.closed.Load() {
		return 0, context.DeadlineExceeded
	}
	r.bump()
	n, err := r.body.Read(p)
	if n > 0 {
		r.bump()
	}
	if r.closed.Load() && err == nil {
		return n, context.DeadlineExceeded
	}
	return n, err
}

func (r *strictIdleReader) bump() {
	if r.idle <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.idle, func() {
		r.closed.Store(true)
		_ = r.body.Close()
	})
}

func (r *strictIdleReader) Close() error {
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	r.closed.Store(true)
	return r.body.Close()
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
