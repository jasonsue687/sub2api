package anthropicmock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

var streamTruePattern = regexp.MustCompile(`"stream"\s*:\s*true`)
var modelPattern = regexp.MustCompile(`"model"\s*:\s*"([^"\\]{1,120})"`)

// IsAnthropicHost reports whether host is an Anthropic API hostname.
func IsAnthropicHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	switch {
	case host == "api.anthropic.com" || host == "anthropic.com":
		return true
	case strings.HasSuffix(host, ".anthropic.com"):
		return true
	default:
		return false
	}
}

// Reason returns why req must be mocked, or empty when it must be sent upstream.
// It has no side effects and is safe to call from the audit filter.
func Reason(req *http.Request) string {
	if interceptDisabled.Load() || req == nil || req.URL == nil {
		return ""
	}
	forced := Forced(req.Context())
	if !forced && testRuns.Load() == 0 && !enabled.Load() {
		return ""
	}
	if !IsAnthropicHost(req.URL.Hostname()) {
		return ""
	}
	switch {
	case forced:
		return mockReasonForced
	case testRuns.Load() > 0:
		return mockReasonTestRun
	case enabled.Load():
		return mockReasonSwitch
	default:
		return ""
	}
}

// Intercept returns a mock response when req would otherwise be sent to Anthropic.
// The second result is false when the caller must use the real transport.
func Intercept(req *http.Request) (*http.Response, bool) {
	reason := Reason(req)
	if reason == "" {
		return nil, false
	}
	if !silent(req.Context()) {
		recordOutbound(req, reason)
	}
	return mockResponse(req, reason), true
}

// WrapRoundTripper returns a transport that mocks Anthropic and delegates the rest.
func WrapRoundTripper(base http.RoundTripper) http.RoundTripper {
	if existing, ok := base.(*guardTransport); ok {
		return existing
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &guardTransport{base: base}
}

type guardTransport struct {
	base http.RoundTripper
}

func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if resp, ok := Intercept(req); ok {
		return resp, nil
	}
	if g == nil || g.base == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return g.base.RoundTrip(req)
}

func mockResponse(req *http.Request, reason string) *http.Response {
	body, contentType := buildMockBody(req)
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	header.Set(headerMock, reason)
	header.Set("Request-Id", "mock_"+reason)
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
}

func buildMockBody(req *http.Request) ([]byte, string) {
	path := ""
	if req != nil && req.URL != nil {
		path = req.URL.Path
	}
	peeked, _ := peekBody(req, maxOutboundBody)
	if strings.Contains(path, "count_tokens") {
		return []byte(`{"input_tokens":1}`), "application/json"
	}
	model := "claude-mock"
	if match := modelPattern.FindSubmatch(peeked); len(match) == 2 {
		model = string(match[1])
	}
	if streamTruePattern.Match(peeked) && !strings.Contains(path, "count_tokens") {
		return []byte(streamBody(model)), "text/event-stream"
	}
	payload := map[string]any{
		"id":            "msg_mock_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]string{{"type": "text", "text": "[mock] anthropic upstream intercepted"}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 1, "output_tokens": 1},
	}
	if !strings.Contains(path, "/messages") {
		payload = map[string]any{"id": "mock", "type": "mock", "mock": true, "model": model}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"type":"mock","mock":true}`)
	}
	return encoded, "application/json"
}

func streamBody(model string) string {
	id := "msg_mock_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	start := `{"type":"message_start","message":{"id":"` + id + `","type":"message","role":"assistant","model":"` + model + `","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`
	return strings.Join([]string{
		"event: message_start",
		"data: " + start,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[mock] anthropic upstream intercepted"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		"",
	}, "\n")
}

func peekBody(req *http.Request, limit int64) ([]byte, bool) {
	if req == nil || req.GetBody == nil || limit <= 0 {
		return nil, false
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	defer func() { _ = rc.Close() }()
	buf, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, false
	}
	if int64(len(buf)) > limit {
		return buf[:limit], true
	}
	return buf, false
}

func recordOutbound(req *http.Request, reason string) {
	body, truncated := peekBody(req, maxOutboundBody)
	redacted := RedactBody(contentTypeOf(req), body)
	if len(redacted) > maxOutboundBody {
		redacted = redacted[:maxOutboundBody]
		truncated = true
	}
	rec := outboundRecord{
		AccountID:     accountID(req),
		Method:        "",
		URL:           safeURL(req),
		Headers:       redactHeaders(req),
		Body:          append([]byte(nil), redacted...),
		BodyTruncated: truncated,
		MockReason:    reason,
		StatusCode:    http.StatusOK,
		CreatedAt:     time.Now().UTC(),
	}
	if req != nil {
		rec.Method = req.Method
	}
	select {
	case outboundQ <- rec:
	default:
		outboundDropped.Add(1)
	}
}

func accountID(req *http.Request) int64 {
	if req == nil {
		return 0
	}
	id, _ := req.Context().Value(ctxkey.AccountID).(int64)
	return id
}

func contentTypeOf(req *http.Request) string {
	if req == nil {
		return ""
	}
	return req.Header.Get("Content-Type")
}
