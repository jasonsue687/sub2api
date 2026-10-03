package anthropicmock

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSwitchOffLeavesNonForcedRequestsAlone(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Intercept(req); ok {
		t.Fatal("switch off must not intercept")
	}
	if Reason(req) != "" {
		t.Fatal("reason must be empty while the switch is off")
	}
}

func TestSwitchOnInterceptsAnthropicOnly(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	SetEnabledForTest(true)
	anthropic := mustReq(t, "https://api.anthropic.com/v1/messages")
	other := mustReq(t, "https://api.openai.com/v1/responses")
	resp, ok := Intercept(anthropic)
	if !ok || resp.StatusCode != 200 {
		t.Fatal("expected mock response")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "mock") {
		t.Fatalf("body %s", body)
	}
	if resp.Header.Get(headerMock) != mockReasonSwitch {
		t.Fatal("missing mock mark")
	}
	if _, ok := Intercept(other); ok {
		t.Fatal("non-anthropic host was intercepted")
	}
}

func TestForceAndStreamMock(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(WithForce(req.Context()))
	resp, ok := Intercept(req)
	if !ok {
		t.Fatal("forced request was not intercepted")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "message_start") {
		t.Fatalf("stream body %s", body)
	}
}

func TestWrapRoundTripperDoesNotCallBaseWhenMocked(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	SetEnabledForTest(true)
	called := 0
	base := RoundTripFunc(func(*http.Request) (*http.Response, error) {
		called++
		return nil, io.EOF
	})
	rt := WrapRoundTripper(base)
	req := mustReq(t, "https://api.anthropic.com/v1/messages")
	resp, err := rt.RoundTrip(req)
	if err != nil || resp == nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if called != 0 {
		t.Fatal("base transport was called")
	}
	other := mustReq(t, "https://example.com/v1")
	_, _ = rt.RoundTrip(other)
	if called != 1 {
		t.Fatalf("base calls=%d", called)
	}
}

func TestEnqueueDoesNotBlockWhenFull(t *testing.T) {
	ResetForTest()
	t.Cleanup(func() {
		for {
			select {
			case <-inboundQ:
			default:
				ResetForTest()
				return
			}
		}
	})
	start := time.Now()
	for i := 0; i < cap(inboundQ)+32; i++ {
		ObserveInbound(mustBodyReq(t, "/v1/messages", `{"model":"claude","n":`+strings.Repeat("1", 8)+`}`))
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("enqueue blocked for %s", time.Since(start))
	}
	if inboundDropped.Load() == 0 {
		t.Fatal("expected overflow drops")
	}
}

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mustReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, raw, strings.NewReader(`{"model":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func mustBodyReq(t *testing.T, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://gateway.local"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/test")
	req.Header.Set("Authorization", "Bearer sk-ant-secret")
	return req
}
