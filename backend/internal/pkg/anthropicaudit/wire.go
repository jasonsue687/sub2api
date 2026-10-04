package anthropicaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
	// CaptureQueueCapacity bounds in-flight capture jobs. Overflow is dropped.
	CaptureQueueCapacity = 512
)

// Record is one persisted inbound request or outbound attempt.
// Bodies and headers are filtered by the worker before persistence.
type Record struct {
	Direction          string          `json:"direction"`
	ClientRequestID    string          `json:"client_request_id"`
	RequestID          string          `json:"request_id"`
	AccountID          int64           `json:"account_id,omitempty"`
	UserID             int64           `json:"user_id,omitempty"`
	APIKeyID           int64           `json:"api_key_id,omitempty"`
	APIKeyName         string          `json:"api_key_name,omitempty"`
	Username           string          `json:"username,omitempty"`
	Endpoint           string          `json:"endpoint"`
	ClientPath         string          `json:"client_path,omitempty"`
	Model              string          `json:"model,omitempty"`
	Stream             *bool           `json:"stream,omitempty"`
	AttemptSeq         int             `json:"attempt_seq,omitempty"`
	RetryReason        string          `json:"retry_reason,omitempty"`
	AccountSwitchCount int             `json:"account_switch_count,omitempty"`
	AttemptCount       int             `json:"attempt_count,omitempty"`
	Status             int             `json:"status"`
	ErrorClass         string          `json:"error_class,omitempty"`
	UpstreamRequestID  string          `json:"upstream_request_id,omitempty"`
	DurationMS         int64           `json:"duration_ms"`
	HeadersMS          int64           `json:"headers_ms,omitempty"`
	Headers            json.RawMessage `json:"headers,omitempty"`
	Body               json.RawMessage `json:"body,omitempty"`
	BodyState          string          `json:"body_state"`
	Summary            json.RawMessage `json:"summary,omitempty"`
	Consistency        string          `json:"consistency,omitempty"`
	Truncated          bool            `json:"truncated,omitempty"`
	OriginalBytes      int             `json:"original_bytes,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
}

type captureJob struct {
	record  Record
	body    []byte
	headers http.Header
}

type persistFunc func(context.Context, Record) error

type persistBox struct{ fn persistFunc }

var (
	captureQueue = make(chan captureJob, CaptureQueueCapacity)
	persister    atomic.Value
	dropped      atomic.Int64
	failed       atomic.Int64
	written      atomic.Int64
)

func init() {
	go func() {
		for job := range captureQueue {
			rec := finishJob(job)
			box, _ := persister.Load().(*persistBox)
			if box == nil || box.fn == nil {
				dropped.Add(1)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := box.fn(ctx, rec)
			cancel()
			if err != nil {
				failed.Add(1)
				continue
			}
			written.Add(1)
		}
	}()
}

// StartCapturePersist registers the database writer. The queue is already running.
func StartCapturePersist(fn func(context.Context, Record) error) {
	if fn == nil {
		return
	}
	persister.Store(&persistBox{fn: fn})
}

// CaptureStats reports queue loss since process start.
type CaptureStats struct {
	QueueCapacity    int   `json:"queue_capacity"`
	QueueDepth       int   `json:"queue_depth"`
	DroppedCount     int64 `json:"dropped_count"`
	WriteFailedCount int64 `json:"write_failed_count"`
	WrittenCount     int64 `json:"written_count"`
}

func Stats() CaptureStats {
	return CaptureStats{
		QueueCapacity:    cap(captureQueue),
		QueueDepth:       len(captureQueue),
		DroppedCount:     dropped.Load(),
		WriteFailedCount: failed.Load(),
		WrittenCount:     written.Load(),
	}
}

// InboundEnabled defaults to on. An explicit false stops inbound capture only.
func InboundEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED"))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

// inboundAccountAllowed keeps pre-selection failures and whitelisted accounts.
// The outbound master switch is not consulted; this switch is independent.
func inboundAccountAllowed(accountID int64) bool {
	if accountID <= 0 {
		return true
	}
	selection := strings.TrimSpace(os.Getenv("SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS"))
	if selection == "" || selection == "*" {
		return true
	}
	for _, raw := range strings.Split(selection, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id == accountID {
			return true
		}
	}
	return false
}

// CaptureInbound copies the client body before any rewrite. done is nil when
// inbound capture is disabled. done must run when the handler returns.
func CaptureInbound(req *http.Request, body []byte, caller InboundCaller) (*http.Request, func(status int)) {
	if !InboundEnabled() || req == nil {
		return req, nil
	}
	path := caller.Endpoint
	if path == "" && req.URL != nil {
		path = req.URL.Path
	}
	ctx := WithTracker(req.Context(), path)
	NoteCaller(ctx, caller)
	req = req.WithContext(ctx)
	started := time.Now()
	job := captureJob{
		record: Record{
			Direction:       DirectionInbound,
			ClientRequestID: correlationID(ctx, ctxkey.ClientRequestID),
			RequestID:       correlationID(ctx, ctxkey.RequestID),
			UserID:          caller.UserID,
			APIKeyID:        caller.APIKeyID,
			APIKeyName:      caller.APIKeyName,
			Username:        caller.Username,
			Endpoint:        path,
			ClientPath:      path,
			CreatedAt:       started.UTC(),
		},
		headers: req.Header.Clone(),
	}
	if len(body) > MaxBodyBytes {
		job.record.BodyState = "too_large"
		job.record.OriginalBytes = len(body)
	} else if len(body) > 0 {
		job.body = bytes.Clone(body)
	}
	return req, func(status int) {
		tracker := trackerFrom(ctx)
		if tracker == nil {
			return
		}
		attempts, accountID, snap := tracker.snapshot()
		if !inboundAccountAllowed(accountID) {
			return
		}
		job.record.Status = status
		job.record.DurationMS = time.Since(started).Milliseconds()
		job.record.AttemptCount = attempts
		job.record.AccountID = accountID
		job.record.ErrorClass = clientErrorClass(status)
		if snap.UserID > 0 {
			job.record.UserID = snap.UserID
		}
		if snap.APIKeyID > 0 {
			job.record.APIKeyID = snap.APIKeyID
		}
		if snap.APIKeyName != "" {
			job.record.APIKeyName = snap.APIKeyName
		}
		if snap.Username != "" {
			job.record.Username = snap.Username
		}
		enqueue(job)
	}
}

// PrepareOutbound copies the wire request when the existing outbound audit
// accepted it. Finish is safe on a nil draft.
func PrepareOutbound(req *http.Request, accountID int64, enabled bool) *outboundDraft {
	if req == nil {
		return nil
	}
	// Inbound account filtering and attempt counts must not depend on whether
	// the outbound audit accepts this account or destination.
	seq, reason, switches := takeAttempt(req.Context(), accountID)
	if !enabled {
		return nil
	}
	draft := &outboundDraft{accountID: accountID, started: time.Now(), headers: cloneHeader(req.Header)}
	if req.URL != nil {
		draft.endpoint = req.URL.Path
	}
	draft.seq = seq
	draft.reason = reason
	draft.switches = switches
	draft.ctx = req.Context()
	body, state, original := copyWireBody(req)
	draft.body = body
	draft.bodyState = state
	draft.original = original
	return draft
}

type outboundDraft struct {
	accountID int64
	started   time.Time
	headers   http.Header
	endpoint  string
	body      []byte
	bodyState string
	original  int
	seq       int
	reason    string
	switches  int
	ctx       context.Context
}

func (d *outboundDraft) Finish(resp *http.Response, err error) {
	if d == nil {
		return
	}
	caller := InboundCaller{}
	clientPath := ""
	if tracker := trackerFrom(d.ctx); tracker != nil {
		_, _, caller = tracker.snapshot()
		clientPath = caller.Endpoint
	}
	if caller.UserID == 0 {
		if userID, ok := d.ctx.Value(ctxkey.UserID).(int64); ok {
			caller.UserID = userID
		}
	}
	rec := Record{
		Direction:          DirectionOutbound,
		ClientRequestID:    correlationID(d.ctx, ctxkey.ClientRequestID),
		RequestID:          correlationID(d.ctx, ctxkey.RequestID),
		AccountID:          d.accountID,
		UserID:             caller.UserID,
		APIKeyID:           caller.APIKeyID,
		APIKeyName:         caller.APIKeyName,
		Username:           caller.Username,
		Endpoint:           d.endpoint,
		ClientPath:         clientPath,
		AttemptSeq:         d.seq,
		RetryReason:        d.reason,
		AccountSwitchCount: d.switches,
		DurationMS:         time.Since(d.started).Milliseconds(),
		HeadersMS:          time.Since(d.started).Milliseconds(),
		BodyState:          d.bodyState,
		OriginalBytes:      d.original,
		CreatedAt:          d.started.UTC(),
	}
	if resp != nil {
		rec.Status = resp.StatusCode
		rec.UpstreamRequestID = bounded(Header(resp.Header, "request-id"), 160)
		if rec.UpstreamRequestID == "" {
			rec.UpstreamRequestID = bounded(Header(resp.Header, "x-request-id"), 160)
		}
	}
	rec.ErrorClass = upstreamErrorClass(resp, err)
	enqueue(captureJob{record: rec, body: d.body, headers: d.headers})
}

func takeAttempt(ctx context.Context, accountID int64) (int, string, int) {
	if tracker := trackerFrom(ctx); tracker != nil {
		return tracker.take(accountID)
	}
	return 1, ReasonInitial, 0
}

func enqueue(job captureJob) {
	if len(captureQueue) >= cap(captureQueue) {
		dropped.Add(1)
		return
	}
	select {
	case captureQueue <- job:
	default:
		dropped.Add(1)
	}
}

func finishJob(job captureJob) Record {
	rec := job.record
	var body, headers []byte
	var state string
	var truncated, headerTruncated bool
	var original int
	if rec.Direction == DirectionInbound && rec.BodyState == "" {
		body, state, truncated, original = CaptureInboundBody(job.body)
	} else {
		body, state, truncated, original = redactOrKeep(job.body, rec.BodyState, rec.OriginalBytes)
	}
	if rec.Direction == DirectionInbound {
		headers, headerTruncated = CaptureInboundHeaders(job.headers)
	} else {
		headers, headerTruncated = RedactHeaders(job.headers)
	}
	rec.Headers = headers
	rec.Body = body
	if state != "" {
		rec.BodyState = state
	}
	if original > 0 {
		rec.OriginalBytes = original
	}
	rec.Truncated = truncated || headerTruncated
	if len(job.body) > 0 && state == "parsed" {
		summary := summarizeWire(job.headers, job.body, rec.Direction == DirectionInbound)
		rec.Summary = summary.raw
		rec.Consistency = summary.consistency
		if rec.Model == "" {
			rec.Model = summary.model
		}
		if summary.stream != nil && rec.Stream == nil {
			rec.Stream = summary.stream
		}
	}
	if rec.Model == "" && len(job.body) > 0 {
		rec.Model = gjson.GetBytes(job.body, "model").String()
	}
	if rec.Stream == nil && len(job.body) > 0 && gjson.GetBytes(job.body, "stream").Exists() {
		value := gjson.GetBytes(job.body, "stream").Bool()
		rec.Stream = &value
	}
	return rec
}

func redactOrKeep(body []byte, state string, original int) ([]byte, string, bool, int) {
	if state == "too_large" || state == "unavailable" || state == "invalid_json" {
		return nil, state, false, original
	}
	if len(body) == 0 {
		if state == "" {
			state = "unavailable"
		}
		return nil, state, false, original
	}
	stored, next, truncated, size := RedactBody(body)
	return stored, next, truncated, size
}

type wireSummary struct {
	raw         json.RawMessage
	consistency string
	model       string
	stream      *bool
}

func summarizeWire(headers http.Header, body []byte, inbound bool) wireSummary {
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		return wireSummary{}
	}
	req.Header = headers.Clone()
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
	snap := &Snapshot{
		Headers: map[string]string{}, Parameters: map[string]any{},
		BodyState: "unavailable", Issues: []string{}, Consistency: "unknown",
	}
	for _, key := range []string{"user-agent", "anthropic-version", "anthropic-beta", "x-app", "x-stainless-lang", "x-stainless-package-version", "x-stainless-os", "x-stainless-arch", "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-retry-count", "x-stainless-timeout"} {
		if value := Header(req.Header, key); value != "" {
			snap.Headers[key] = bounded(value, 1024)
		}
	}
	snap.readBody(req)
	if inbound {
		delete(snap.Parameters, "messages_count")
		delete(snap.Parameters, "tools_count")
		delete(snap.Parameters, "tool_choice.type")
	}
	snap.checkConsistency()
	identityHeaders := make(map[string]string, len(snap.Headers))
	for key, value := range snap.Headers {
		if key != "x-stainless-retry-count" && key != "x-stainless-timeout" {
			identityHeaders[key] = value
		}
	}
	identity := map[string]any{
		"headers": identityHeaders, "cc_version": versionPattern.FindString(snap.CCVersion),
		"cc_entrypoint": snap.CCEntrypoint, "device": snap.DeviceHash, "account": snap.AccountHash,
	}
	snap.IdentitySignature = hashJSON(identity)
	if snap.BodyState == "parsed" {
		snap.ParameterSignature = hashJSON(snap.Parameters)
	}
	payload := map[string]any{
		"consistency": snap.Consistency, "issues": snap.Issues,
		"cc_version": snap.CCVersion, "cc_entrypoint": snap.CCEntrypoint,
		"device_hash": snap.DeviceHash, "account_hash": snap.AccountHash, "session_hash": snap.SessionHash,
		"identity_signature": snap.IdentitySignature, "parameter_signature": snap.ParameterSignature,
		"parameters": snap.Parameters, "body_state": snap.BodyState,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = nil
	}
	var stream *bool
	if value, ok := snap.Parameters["stream"].(bool); ok {
		stream = &value
	}
	model, _ := snap.Parameters["model"].(string)
	return wireSummary{raw: raw, consistency: snap.Consistency, model: model, stream: stream}
}

func copyWireBody(req *http.Request) ([]byte, string, int) {
	if req.ContentLength > MaxBodyBytes {
		return nil, "too_large", int(req.ContentLength)
	}
	if req.GetBody == nil {
		return nil, "unavailable", 0
	}
	reader, err := req.GetBody()
	if err != nil {
		return nil, "unavailable", 0
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, MaxBodyBytes+1))
	if err != nil {
		return nil, "unavailable", 0
	}
	if len(data) > MaxBodyBytes {
		return nil, "too_large", len(data)
	}
	if !json.Valid(data) {
		return nil, "invalid_json", len(data)
	}
	return data, "", len(data)
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h.Clone()
}

func correlationID(ctx context.Context, key ctxkey.Key) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(key).(string)
	return bounded(strings.TrimSpace(value), 128)
}

func clientErrorClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusBadRequest:
		return "invalid_request"
	case status == http.StatusServiceUnavailable || status == 529:
		return "unavailable"
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return ""
	}
}

func upstreamErrorClass(resp *http.Response, err error) string {
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return "timeout"
		}
		return "transport_error"
	}
	if resp != nil && resp.StatusCode >= 400 {
		return clientErrorClass(resp.StatusCode)
	}
	return ""
}
