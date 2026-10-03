package anthropicaudit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

func TestAttemptSequenceAcrossRetries(t *testing.T) {
	ctx := WithTracker(context.Background(), "/v1/messages")
	NoteCaller(ctx, InboundCaller{UserID: 7, APIKeyID: 12, APIKeyName: "研发共享", Username: "dev-alice", Endpoint: "/v1/messages"})

	seq, reason, switches := takeAttempt(ctx, 11)
	if seq != 1 || reason != ReasonInitial || switches != 0 {
		t.Fatalf("initial %+v %s %d", seq, reason, switches)
	}
	MarkRetry(ctx, ReasonUpstreamRetry)
	seq, reason, switches = takeAttempt(ctx, 11)
	if seq != 2 || reason != ReasonUpstreamRetry || switches != 0 {
		t.Fatalf("upstream retry %d %s %d", seq, reason, switches)
	}
	MarkRetry(ctx, ReasonSignatureRectify)
	seq, reason, _ = takeAttempt(ctx, 11)
	if seq != 3 || reason != ReasonSignatureRectify {
		t.Fatalf("signature %d %s", seq, reason)
	}
	MarkRetry(ctx, ReasonBudgetRectify)
	seq, reason, _ = takeAttempt(ctx, 11)
	if seq != 4 || reason != ReasonBudgetRectify {
		t.Fatalf("budget %d %s", seq, reason)
	}
	MarkRetry(ctx, ReasonSameAccountRetry)
	seq, reason, switches = takeAttempt(ctx, 11)
	if seq != 5 || reason != ReasonSameAccountRetry || switches != 0 {
		t.Fatalf("same account %d %s %d", seq, reason, switches)
	}
	MarkRetry(ctx, ReasonAccountSwitch)
	seq, reason, switches = takeAttempt(ctx, 22)
	if seq != 6 || reason != ReasonAccountSwitch || switches != 1 {
		t.Fatalf("switch %d %s %d", seq, reason, switches)
	}
	attempts, account, caller := trackerFrom(ctx).snapshot()
	if attempts != 6 || account != 22 || caller.APIKeyID != 12 || caller.Username != "dev-alice" {
		t.Fatalf("snapshot %d %d %+v", attempts, account, caller)
	}
}

func TestInboundCopiesBodyBeforeRewriteAndLinksAttempts(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "")
	rec := make(chan Record, 4)
	StartCapturePersist(func(_ context.Context, record Record) error {
		rec <- record
		return nil
	})
	body := []byte(`{"model":"claude-sonnet-4-5","system":"raw prompt","messages":[{"role":"user","content":"hello"}]}`)
	req, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(context.WithValue(context.WithValue(req.Context(), ctxkey.ClientRequestID, "crid-1"), ctxkey.RequestID, "req-1"))
	req.Header.Set("X-Api-Key", "sk-secret")
	req.Header.Set("User-Agent", "claude-cli/2.0.14")
	req, done := CaptureInbound(req, body, InboundCaller{UserID: 3, APIKeyID: 9, APIKeyName: "key", Username: "alice", Endpoint: "/v1/messages"})
	if done == nil {
		t.Fatal("expected capture")
	}
	copy(body, []byte(`{"model":"rewritten-model","system":"changed!!","messages":[{"role":"user","content":"nope!!"}]}`))
	upstream, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream = upstream.WithContext(req.Context())
	wire := []byte(`{"model":"claude-sonnet-4-5-20250929","system":"raw prompt","max_tokens":10}`)
	upstream.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wire)), nil }
	upstream.ContentLength = int64(len(wire))
	upstream.Header.Set("Authorization", "Bearer sk-ant-secret")
	draft := PrepareOutbound(upstream, 11, true)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"request-id": []string{"req_up_1"}}}
	draft.Finish(resp, nil)
	done(200)

	var inbound, outbound Record
	deadline := time.After(2 * time.Second)
	for inbound.Direction == "" || outbound.Direction == "" {
		select {
		case record := <-rec:
			if record.Direction == DirectionInbound {
				inbound = record
			} else {
				outbound = record
			}
		case <-deadline:
			t.Fatal("timed out waiting for records")
		}
	}
	if strings.Contains(string(inbound.Body), "raw prompt") || strings.Contains(string(inbound.Body), "rewritten-model") || strings.Contains(string(inbound.Body), "sk-secret") {
		t.Fatalf("inbound leaked or saw rewrite: %s", inbound.Body)
	}
	if gjson.GetBytes(inbound.Body, "model").String() != "claude-sonnet-4-5" {
		t.Fatalf("inbound model %s", inbound.Body)
	}
	if gjson.GetBytes(outbound.Body, "model").String() != "claude-sonnet-4-5-20250929" {
		t.Fatalf("outbound model %s", outbound.Body)
	}
	if strings.Contains(string(outbound.Headers), "sk-ant-secret") {
		t.Fatalf("auth leaked %s", outbound.Headers)
	}
	if inbound.ClientRequestID != "crid-1" || outbound.ClientRequestID != "crid-1" || outbound.AttemptSeq != 1 || outbound.RetryReason != ReasonInitial {
		t.Fatalf("link %+v %+v", inbound, outbound)
	}
	if inbound.AttemptCount != 1 || inbound.AccountID != 11 || inbound.Status != 200 || outbound.UpstreamRequestID != "req_up_1" {
		t.Fatalf("result inbound=%+v outbound=%+v", inbound, outbound)
	}
	if inbound.APIKeyID != 9 || outbound.Username != "alice" {
		t.Fatalf("caller not copied")
	}
}

func TestInboundDisabledAndWhitelist(t *testing.T) {
	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "false")
	req, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, done := CaptureInbound(req, []byte(`{"model":"m"}`), InboundCaller{Endpoint: "/v1/messages"})
	if done != nil {
		t.Fatal("disabled inbound still captured")
	}

	t.Setenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "11")
	got := make(chan Record, 4)
	StartCapturePersist(func(_ context.Context, record Record) error {
		if record.ClientRequestID == "deny" || record.ClientRequestID == "allow" {
			got <- record
		}
		return nil
	})
	deny := req.Clone(context.WithValue(WithTracker(context.Background(), "/v1/messages"), ctxkey.ClientRequestID, "deny"))
	deny, done = CaptureInbound(deny, []byte(`{"model":"m"}`), InboundCaller{Endpoint: "/v1/messages"})
	if done == nil {
		t.Fatal("expected done")
	}
	takeAttempt(deny.Context(), 99)
	done(400)
	select {
	case record := <-got:
		t.Fatalf("non-whitelisted account persisted: %+v", record)
	case <-time.After(150 * time.Millisecond):
	}

	allowReq := req.Clone(context.WithValue(WithTracker(context.Background(), "/v1/messages"), ctxkey.ClientRequestID, "allow"))
	_, done = CaptureInbound(allowReq, []byte(`{"model":"m"}`), InboundCaller{Endpoint: "/v1/messages"})
	if done == nil {
		t.Fatal("expected done")
	}
	done(503)
	select {
	case record := <-got:
		if record.Status != 503 || record.AccountID != 0 || record.AttemptCount != 0 {
			t.Fatalf("pre-selection failure: %+v", record)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("whitelisted pre-selection failure was dropped")
	}
}

func TestOutboundMatchesWireBody(t *testing.T) {
	wire := []byte(`{"model":"claude-haiku","messages":[{"role":"user","content":"secret"}]}`)
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wire)), nil }
	req.ContentLength = int64(len(wire))
	req.Header.Set("Anthropic-Beta", "a,b")
	draft := PrepareOutbound(req, 4, true)
	if draft == nil || !bytes.Equal(draft.body, wire) {
		t.Fatal("wire body mismatch")
	}
	if PrepareOutbound(req, 4, false) != nil {
		t.Fatal("disabled outbound captured")
	}
	req.ContentLength = MaxBodyBytes + 1
	draft = PrepareOutbound(req, 4, true)
	if draft.bodyState != "too_large" || draft.body != nil {
		t.Fatalf("too large %+v", draft)
	}
}

func TestQueueDropsInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	StartCapturePersist(func(context.Context, Record) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	enqueue(captureJob{record: Record{Direction: DirectionOutbound, ClientRequestID: "block"}})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	before := dropped.Load()
	for i := 0; i < CaptureQueueCapacity+32; i++ {
		enqueue(captureJob{record: Record{Direction: DirectionOutbound, ClientRequestID: "fill"}})
	}
	if dropped.Load() <= before {
		t.Fatal("expected overflow drops")
	}
	close(release)
}
