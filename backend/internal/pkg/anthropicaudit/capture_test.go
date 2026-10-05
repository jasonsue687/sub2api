package anthropicaudit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

func auditRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ENABLED", "true")
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "11")
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header["user-agent"] = []string{"claude-cli/2.1.283 (external, cli)"}
	req.Header["authorization"] = []string{"Bearer secret-token"}
	req.Header.Set("Cookie", "private-cookie")
	return WithOrigin(req, "gateway", nil, true)
}

const sampleBody = `{"model":"claude-sonnet-4-5","stream":false,"max_tokens":2048,"temperature":0,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.283.abc; cc_entrypoint=local-agent;"},{"type":"text","text":"private-system"}],"messages":[{"role":"user","content":"private-conversation"}],"tools":[{"name":"private-tool","input_schema":{"private-schema":true}}],"metadata":{"user_id":"{\"device_id\":\"private-device\",\"account_uuid\":\"private-account\",\"session_id\":\"private-session\"}"},"thinking":{"type":"enabled","budget_tokens":1024}}`

func TestAnthropicCapturePrivacyAndNoMutation(t *testing.T) {
	req := auditRequest(t, sampleBody)
	before := req.Header.Clone()
	originalBody := req.Body
	snapshot := Begin(req, 11)
	if snapshot == nil || snapshot.Consistency != "mismatch" || !reflect.DeepEqual(snapshot.Issues, []string{"entrypoint_mismatch"}) {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if !reflect.DeepEqual(req.Header, before) || req.Body != originalBody {
		t.Fatal("capture mutated outbound request")
	}
	actual, _ := io.ReadAll(req.Body)
	if string(actual) != sampleBody {
		t.Fatal("capture consumed or changed body")
	}
	raw, _ := json.Marshal(snapshot)
	for _, secret := range []string{"secret-token", "private-cookie", "private-system", "private-conversation", "private-tool", "private-schema", "private-device", "private-account", "private-session", "authorization"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("retained forbidden value %q", secret)
		}
	}
	if snapshot.Parameters["stream"] != false || snapshot.Parameters["temperature"] != float64(0) || snapshot.Parameters["tools_count"] != 1 || snapshot.DeviceHash == "" {
		t.Fatalf("lost zero values/counts/hashes: %s", raw)
	}
}
func TestAnthropicConsistencyUnknownAndMatched(t *testing.T) {
	tests := []struct{ name, body, ua, state string }{
		{"matching", strings.ReplaceAll(sampleBody, "local-agent", "cli"), "claude-cli/2.1.283 (external, cli)", "matched"},
		{"version mismatch", strings.ReplaceAll(sampleBody, "local-agent", "cli"), "claude-cli/2.1.282 (external, cli)", "mismatch"},
		{"missing billing", `{"model":"claude-sonnet-4-5"}`, "claude-cli/2.1.283 (external, cli)", "unknown"},
		{"unknown ua", sampleBody, "custom-client/1", "unknown"},
		{"invalid json", `{broken`, "claude-cli/2.1.283 (external, cli)", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := auditRequest(t, tt.body)
			req.Header["user-agent"] = []string{tt.ua}
			s := Begin(req, 11)
			if s.Consistency != tt.state {
				t.Fatalf("got %s", s.Consistency)
			}
		})
	}
}
func TestAnthropicScopeAndOverrides(t *testing.T) {
	req := auditRequest(t, sampleBody)
	if Begin(req, 12) != nil {
		t.Fatal("captured unselected account")
	}
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "")
	if Begin(req, 12) == nil {
		t.Fatal("empty selection must enable all eligible accounts")
	}
	t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", "11")
	req.URL.Host = "example.org"
	if Begin(req, 11) != nil {
		t.Fatal("captured another provider")
	}
	req.URL.Host = "api.anthropic.com"
	if Begin(WithOrigin(req, "gateway", nil, false), 11) != nil {
		t.Fatal("captured bearer API-key account")
	}
	delete(req.Header, "authorization")
	req.Header.Set("X-Api-Key", "private-key")
	if Begin(req, 11) != nil {
		t.Fatal("captured api key request")
	}
}
func TestAnthropicBoundedBodyAndMissingGetBody(t *testing.T) {
	req := auditRequest(t, sampleBody)
	req.GetBody = nil
	s := Begin(req, 11)
	if s.BodyState != "unavailable" || s.Consistency != "unknown" {
		t.Fatal("missing body treated as known")
	}
	req = auditRequest(t, strings.Repeat("x", MaxBodyBytes+1))
	req.ContentLength = -1
	s = Begin(req, 11)
	if s.BodyState != "too_large" || s.ParameterSignature != "" {
		t.Fatal("oversized body not bounded")
	}
	req = auditRequest(t, `{"model":"claude","stream":null,"max_tokens":null}`)
	s = Begin(req, 11)
	if _, ok := s.Parameters["stream"]; ok {
		t.Fatal("null became false")
	}
	if _, ok := s.Parameters["max_tokens"]; ok {
		t.Fatal("null became zero")
	}
}
func TestAnthropicIdentityExcludesSessionGenerationAndFingerprintSuffix(t *testing.T) {
	a := Begin(auditRequest(t, sampleBody), 11)
	body := strings.NewReplacer("private-session", "another-session", "2048", "4096", ".abc;", ".def;").Replace(sampleBody)
	b := Begin(auditRequest(t, body), 11)
	if a.IdentitySignature != b.IdentitySignature {
		t.Fatal("session/generation/fingerprint suffix changes split identity")
	}
	if a.ParameterSignature == b.ParameterSignature || a.SessionHash == b.SessionHash || a.AttemptID == b.AttemptID {
		t.Fatal("parameter/session/attempt distinctions lost")
	}
}

type captureSink struct{ events []*logger.LogEvent }

func (s *captureSink) WriteLogEvent(e *logger.LogEvent) { s.events = append(s.events, e) }
func TestAnthropicFinishRecordsEachAttemptAndSanitizesErrors(t *testing.T) {
	sink := &captureSink{}
	logger.SetSink(sink)
	defer logger.SetSink(nil)
	a := Begin(auditRequest(t, sampleBody), 11)
	a.Finish(&http.Response{StatusCode: 200, Header: http.Header{"request-id": []string{"upstream-1"}}}, nil)
	b := Begin(auditRequest(t, sampleBody), 11)
	b.Finish(nil, errors.New("https://secret-user:secret-password@proxy.invalid"))
	if len(sink.events) != 2 || a.UpstreamRequestID != "upstream-1" || b.ErrorClass != "transport_error" {
		t.Fatal("attempt metadata missing")
	}
	raw, _ := json.Marshal(sink.events)
	if strings.Contains(string(raw), "secret-password") {
		t.Fatal("raw transport error leaked")
	}
	var absent *Snapshot
	absent.Finish(nil, nil)
}

func TestAnthropicDefaultCollectionAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, ids string
		accountID          int64
		want               bool
	}{
		{"defaults include existing account", "", "", 11, true},
		{"defaults include new account", "", "", 9999, true},
		{"explicit all", "true", "*", 12, true},
		{"global disable", "false", "", 11, false},
		{"invalid switch fails closed", "invalid", "", 11, false},
		{"explicit selected account", "true", "11, 12", 12, true},
		{"explicit unselected account", "true", "11", 12, false},
		{"invalid selection fails closed", "", "invalid", 11, false},
		{"invalid account", "", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := auditRequest(t, sampleBody)
			t.Setenv("SUB2API_ANTHROPIC_AUDIT_ENABLED", tc.enabled)
			t.Setenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS", tc.ids)
			if got := Begin(req, tc.accountID) != nil; got != tc.want {
				t.Fatalf("collection = %v, want %v", got, tc.want)
			}
		})
	}
}
