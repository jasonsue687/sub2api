package repository

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

func TestMockSwitchOffStillReachesLocalUpstream(t *testing.T) {
	anthropicmock.ResetForTest()
	t.Cleanup(anthropicmock.ResetForTest)
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)
	req := anthropicMockProbe(t)
	resp, err := NewHTTPUpstream(nil).Do(req, proxy.URL, 11, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestOneClickReplayCannotReachUpstream(t *testing.T) {
	anthropicmock.ResetForTest()
	t.Cleanup(anthropicmock.ResetForTest)
	anthropicmock.SetEnabledForTest(false)
	sink := &outboundAuditSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		// Use a fresh context so a dropped force flag cannot be the only protection.
		upstreamReq, err := http.NewRequest(http.MethodPost, "http://api.anthropic.com/v1/messages", bytes.NewReader(body))
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		upstreamReq.Header.Set("Authorization", "Bearer synthetic-test-token")
		upstreamReq = anthropicaudit.WithOrigin(upstreamReq, "gateway", nil, true)
		resp, err := NewHTTPUpstream(nil).Do(upstreamReq, proxy.URL, 11, 1)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		payload, _ := io.ReadAll(resp.Body)
		c.Data(resp.StatusCode, "application/json", payload)
	})
	mem := anthropicmock.NewMemoryStore()
	_, _ = mem.InsertSample(context.Background(), anthropicmock.Sample{
		Method: http.MethodPost, Path: "/v1/messages", ContentType: "application/json", ClientLabel: "claude-cli/test",
		Body: []byte(`{"model":"claude-sonnet-4-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`),
	})
	anthropicmock.SetStore(mem)
	anthropicmock.ArmOutboundHook()
	anthropicmock.SetReplayHandler(engine)
	report, err := anthropicmock.Run(context.Background(), anthropicmock.RunOptions{Limit: 5, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
	if report.Completed != 1 {
		t.Fatalf("%+v", report)
	}
	if len(sink.events) != 1 {
		t.Fatalf("audit events=%d", len(sink.events))
	}
	snap, ok := sink.events[0].Fields["audit"].(*anthropicaudit.Snapshot)
	if !ok || !snap.Mock || snap.MockReason == "" {
		t.Fatalf("audit not marked mock: %+v", sink.events[0].Fields["audit"])
	}
}

func TestRunRefusalLeavesRealUpstreamOpen(t *testing.T) {
	anthropicmock.ResetForTest()
	t.Cleanup(anthropicmock.ResetForTest)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	anthropicmock.SetReplayHandler(engine)
	anthropicmock.SetStore(anthropicmock.NewMemoryStore())
	anthropicmock.ArmOutboundHook()
	anthropicmock.DisableInterceptForTest(true)
	_, err := anthropicmock.Run(context.Background(), anthropicmock.RunOptions{})
	if !errors.Is(err, anthropicmock.ErrMockNotGuaranteed) {
		t.Fatal(err)
	}
	anthropicmock.DisableInterceptForTest(false)
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)
	resp, err := NewHTTPUpstream(nil).Do(anthropicMockProbe(t), proxy.URL, 11, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls != 1 {
		t.Fatalf("guard leaked, calls=%d", calls)
	}
}

func anthropicMockProbe(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer synthetic-test-token")
	return anthropicaudit.WithOrigin(req, "gateway", nil, true)
}
