// Package anthropicaudit records allowlisted metadata at the outbound HTTP boundary.
// It never retains request/response bodies, credentials, or raw identity identifiers.
package anthropicaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const Component = "audit.anthropic_outbound"
const MaxBodyBytes = 8 << 20

var uaPattern = regexp.MustCompile(`^claude-cli/(\d+\.\d+\.\d+) \(external, ([a-zA-Z0-9_-]+)\)$`)
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+`)
var tokenPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,120}$`)
var oldIdentityPattern = regexp.MustCompile(`^user_(.+)_account_(.*)_session_(.+)$`)

// EnabledForAccount fails closed: deployment must explicitly enable collection
// and name the account IDs. An empty/invalid list never enables every account.
func EnabledForAccount(accountID int64) bool {
	if accountID <= 0 || os.Getenv("SUB2API_ANTHROPIC_AUDIT_ENABLED") != "true" {
		return false
	}
	for _, raw := range strings.Split(os.Getenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS"), ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id == accountID {
			return true
		}
	}
	return false
}

type contextKey struct{}
type Origin struct {
	Subscription bool   `json:"subscription"`
	Source       string `json:"source"`
	Mimic        *bool  `json:"mimic,omitempty"`
}

func WithOrigin(req *http.Request, source string, mimic *bool, subscription bool) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), contextKey{}, Origin{Source: source, Mimic: mimic, Subscription: subscription}))
}

type Snapshot struct {
	Origin             Origin            `json:"origin"`
	Schema             int               `json:"schema"`
	AttemptID          string            `json:"attempt_id"`
	StartedAt          time.Time         `json:"started_at"`
	Endpoint           string            `json:"endpoint"`
	Headers            map[string]string `json:"headers"`
	Parameters         map[string]any    `json:"parameters"`
	BodyState          string            `json:"body_state"`
	CCVersion          string            `json:"cc_version,omitempty"`
	CCEntrypoint       string            `json:"cc_entrypoint,omitempty"`
	DeviceHash         string            `json:"device_hash,omitempty"`
	AccountHash        string            `json:"account_hash,omitempty"`
	SessionHash        string            `json:"session_hash,omitempty"`
	IdentitySignature  string            `json:"identity_signature"`
	ParameterSignature string            `json:"parameter_signature,omitempty"`
	Consistency        string            `json:"consistency"`
	Issues             []string          `json:"issues"`
	Status             int               `json:"status"`
	ErrorClass         string            `json:"error_class,omitempty"`
	UpstreamRequestID  string            `json:"upstream_request_id,omitempty"`
	HeadersMS          int64             `json:"headers_ms"`
	accountID          int64
	requestID          string
	clientRequestID    string
	started            time.Time
}

// Header also handles manually inserted non-canonical keys used by the gateway.
func Header(h http.Header, key string) string {
	var values []string
	var keys []string
	for k := range h {
		if strings.EqualFold(k, key) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		values = append(values, h[k]...)
	}
	return strings.Join(values, ", ")
}

func Begin(req *http.Request, accountID int64) *Snapshot {
	if !EnabledForAccount(accountID) || req == nil || req.URL == nil || req.Method != http.MethodPost ||
		!strings.EqualFold(req.URL.Hostname(), "api.anthropic.com") ||
		(req.URL.Path != "/v1/messages" && req.URL.Path != "/v1/messages/count_tokens") ||
		!strings.HasPrefix(strings.ToLower(Header(req.Header, "Authorization")), "bearer ") {
		return nil
	}
	origin, _ := req.Context().Value(contextKey{}).(Origin)
	if !origin.Subscription {
		return nil
	}
	started := time.Now()
	s := &Snapshot{Schema: 1, AttemptID: uuid.NewString(), StartedAt: started.UTC(), Endpoint: req.URL.Path,
		Headers: map[string]string{}, Parameters: map[string]any{}, BodyState: "unavailable", Consistency: "unknown", Issues: []string{}, accountID: accountID, started: started}
	s.Origin = origin
	if s.Origin.Source == "" {
		s.Origin.Source = "unspecified"
	}
	s.requestID, _ = req.Context().Value(ctxkey.RequestID).(string)
	s.clientRequestID, _ = req.Context().Value(ctxkey.ClientRequestID).(string)
	for _, key := range []string{"user-agent", "anthropic-version", "anthropic-beta", "x-app", "x-stainless-lang", "x-stainless-package-version", "x-stainless-os", "x-stainless-arch", "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-retry-count", "x-stainless-timeout"} {
		if value := Header(req.Header, key); value != "" {
			s.Headers[key] = bounded(value, 1024)
		}
	}
	s.readBody(req)
	s.checkConsistency()
	// Session changes and generation parameters are deliberately excluded from identity.
	identity := map[string]any{"headers": s.Headers, "cc_version": s.CCVersion, "cc_entrypoint": s.CCEntrypoint, "device": s.DeviceHash, "account": s.AccountHash}
	identityHeaders := make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		if k != "x-stainless-retry-count" && k != "x-stainless-timeout" {
			identityHeaders[k] = v
		}
	}
	identity["headers"] = identityHeaders
	identity["cc_version"] = versionPattern.FindString(s.CCVersion)
	s.IdentitySignature = hashJSON(identity)
	if s.BodyState == "parsed" {
		s.ParameterSignature = hashJSON(s.Parameters)
	}
	return s
}

func (s *Snapshot) readBody(req *http.Request) {
	if req.GetBody == nil {
		return
	}
	if req.ContentLength > MaxBodyBytes {
		s.BodyState = "too_large"
		return
	}
	body, err := req.GetBody()
	if err != nil {
		return
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, MaxBodyBytes+1))
	if err != nil {
		return
	}
	if len(data) > MaxBodyBytes {
		s.BodyState = "too_large"
		return
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		s.BodyState = "invalid_json"
		return
	}
	s.BodyState = "parsed"
	for _, key := range []string{"model", "service_tier"} {
		var v string
		if json.Unmarshal(root[key], &v) == nil && tokenPattern.MatchString(v) {
			s.Parameters[key] = v
		}
	}
	var stream bool
	if raw, ok := root["stream"]; ok && string(raw) != "null" && json.Unmarshal(raw, &stream) == nil {
		s.Parameters["stream"] = stream
	}
	for _, key := range []string{"max_tokens", "temperature", "top_p", "top_k"} {
		var v float64
		if raw, ok := root[key]; ok && string(raw) != "null" && json.Unmarshal(raw, &v) == nil {
			s.Parameters[key] = v
		}
	}
	for _, key := range []string{"messages", "tools", "stop_sequences"} {
		var list []json.RawMessage
		if raw, ok := root[key]; ok && string(raw) != "null" && json.Unmarshal(raw, &list) == nil {
			s.Parameters[key+"_count"] = len(list)
		}
	}
	for _, pair := range [][2]string{{"thinking", "type"}, {"output_config", "effort"}, {"tool_choice", "type"}} {
		var obj map[string]json.RawMessage
		var v string
		if json.Unmarshal(root[pair[0]], &obj) == nil && json.Unmarshal(obj[pair[1]], &v) == nil && tokenPattern.MatchString(v) {
			s.Parameters[pair[0]+"."+pair[1]] = v
		}
	}
	var thinking struct {
		Budget *float64 `json:"budget_tokens"`
	}
	if json.Unmarshal(root["thinking"], &thinking) == nil && thinking.Budget != nil {
		s.Parameters["thinking.budget_tokens"] = *thinking.Budget
	}
	var system []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(root["system"], &system) != nil {
		var v string
		if json.Unmarshal(root["system"], &v) == nil {
			system = append(system, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{"text", v})
		}
	}
	blocks := 0
	for _, block := range system {
		if block.Type != "text" || !strings.HasPrefix(block.Text, "x-anthropic-billing-header:") {
			continue
		}
		blocks++
		for _, field := range strings.Split(strings.TrimPrefix(block.Text, "x-anthropic-billing-header:"), ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(field), "=")
			if !ok || !tokenPattern.MatchString(v) {
				continue
			}
			switch k {
			case "cc_version":
				s.CCVersion = v
			case "cc_entrypoint":
				s.CCEntrypoint = v
			}
		}
	}
	if blocks > 1 {
		s.Issues = append(s.Issues, "multiple_billing_blocks")
	}
	var metadata struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(root["metadata"], &metadata) == nil {
		var id struct {
			Device  string `json:"device_id"`
			Account string `json:"account_uuid"`
			Session string `json:"session_id"`
		}
		if json.Unmarshal([]byte(metadata.UserID), &id) != nil {
			if m := oldIdentityPattern.FindStringSubmatch(metadata.UserID); len(m) == 4 {
				id.Device, id.Account, id.Session = m[1], m[2], m[3]
			}
		}
		s.DeviceHash = hashID(id.Device)
		s.AccountHash = hashID(id.Account)
		s.SessionHash = hashID(id.Session)
	}
}

func (s *Snapshot) checkConsistency() {
	ua := uaPattern.FindStringSubmatch(s.Headers["user-agent"])
	version := versionPattern.FindString(s.CCVersion)
	if len(ua) == 3 {
		if s.CCEntrypoint != "" && ua[2] != s.CCEntrypoint {
			s.Issues = append(s.Issues, "entrypoint_mismatch")
		}
		if version != "" && ua[1] != version {
			s.Issues = append(s.Issues, "version_mismatch")
		}
	}
	if len(s.Issues) > 0 {
		s.Consistency = "mismatch"
	} else if s.BodyState == "parsed" && len(ua) == 3 && version != "" && s.CCEntrypoint != "" {
		s.Consistency = "matched"
	}
}

// Finish records the response-header boundary, never stream completion. The
// existing bounded asynchronous Ops sink owns persistence and loss counters.
func (s *Snapshot) Finish(resp *http.Response, err error) {
	if s == nil {
		return
	}
	s.HeadersMS = time.Since(s.started).Milliseconds()
	if resp != nil {
		s.Status = resp.StatusCode
		s.UpstreamRequestID = bounded(Header(resp.Header, "request-id"), 160)
		if s.UpstreamRequestID == "" {
			s.UpstreamRequestID = bounded(Header(resp.Header, "x-request-id"), 160)
		}
	}
	if err != nil {
		s.ErrorClass = "transport_error"
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			s.ErrorClass = "timeout"
		}
	}
	model, _ := s.Parameters["model"].(string)
	logger.WriteSinkEvent("info", Component, "Anthropic outbound attempt", map[string]any{
		"account_id": s.accountID, "platform": "anthropic", "model": model, "request_id": bounded(s.requestID, 128), "client_request_id": bounded(s.clientRequestID, 128), "audit": s,
	})
}
func hashID(v string) string {
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
func hashJSON(v any) string { b, _ := json.Marshal(v); return hashID(string(b)) }
func bounded(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
