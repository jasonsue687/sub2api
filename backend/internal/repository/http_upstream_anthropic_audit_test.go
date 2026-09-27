package repository

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicaudit"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

type outboundAuditSink struct{ events []*logger.LogEvent }

func (s *outboundAuditSink) WriteLogEvent(e *logger.LogEvent) {
	if e.Component == anthropicaudit.Component {
		s.events = append(s.events, e)
	}
}
func TestAnthropicTransportAttemptsAndTLSDelegation(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "11")
	sink := &outboundAuditSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	body := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"unchanged payload"}]}`
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		actual, _ := io.ReadAll(r.Body)
		if string(actual) != body {
			t.Error("transport body changed")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("authentication changed")
		}
		w.Header().Set("request-id", "synthetic-upstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	client := NewHTTPUpstream(nil)
	for i := 0; i < 2; i++ {
		// HTTP through a local fake proxy: never makes a network request to Anthropic.
		req, err := http.NewRequest(http.MethodPost, "http://api.anthropic.com/v1/messages", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer synthetic-test-token")
		req = anthropicaudit.WithOrigin(req, "gateway", nil, true)
		resp, err := client.DoWithTLS(req, proxy.URL, 11, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if calls != 2 || len(sink.events) != 2 {
		t.Fatalf("calls=%d audit events=%d; TLS delegation must not double count", calls, len(sink.events))
	}
	first, ok := sink.events[0].Fields["audit"].(*anthropicaudit.Snapshot)
	if !ok {
		t.Fatal("first audit snapshot missing")
	}
	second, ok := sink.events[1].Fields["audit"].(*anthropicaudit.Snapshot)
	if !ok {
		t.Fatal("second audit snapshot missing")
	}
	if first.AttemptID == second.AttemptID || first.Status != 200 || first.UpstreamRequestID != "synthetic-upstream" {
		t.Fatal("incorrect attempt/result metadata")
	}
}
