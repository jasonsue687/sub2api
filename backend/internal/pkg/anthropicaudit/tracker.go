package anthropicaudit

import (
	"context"
	"sync"
)

const (
	ReasonInitial          = "initial"
	ReasonAccountSwitch    = "account_switch"
	ReasonSameAccountRetry = "same_account_retry"
	ReasonUpstreamRetry    = "upstream_retry"
	ReasonSignatureRectify = "signature_rectify"
	ReasonBudgetRectify    = "budget_rectify"
)

type trackerKey struct{}

// Tracker counts outbound attempts for one inbound request.
type Tracker struct {
	mu           sync.Mutex
	seq          int
	reason       string
	switches     int
	finalAccount int64
	clientPath   string
	userID       int64
	apiKeyID     int64
	apiKeyName   string
	username     string
}

// InboundCaller is the authenticated client, when known.
type InboundCaller struct {
	UserID     int64
	APIKeyID   int64
	APIKeyName string
	Username   string
	Endpoint   string
}

// WithTracker attaches an attempt counter. A tracker already on ctx is kept.
func WithTracker(ctx context.Context, clientPath string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing := trackerFrom(ctx); existing != nil {
		existing.notePath(clientPath)
		return ctx
	}
	return context.WithValue(ctx, trackerKey{}, &Tracker{clientPath: clientPath})
}

func trackerFrom(ctx context.Context) *Tracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(trackerKey{}).(*Tracker)
	return tracker
}

// MarkRetry sets the reason consumed by the next outbound capture.
// account_switch also increments the switch counter.
func MarkRetry(ctx context.Context, reason string) {
	tracker := trackerFrom(ctx)
	if tracker == nil || reason == "" {
		return
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.reason = reason
	if reason == ReasonAccountSwitch {
		tracker.switches++
	}
}

// NoteCaller records who sent the inbound request. Empty fields are ignored.
func NoteCaller(ctx context.Context, caller InboundCaller) {
	tracker := trackerFrom(ctx)
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if caller.UserID > 0 {
		tracker.userID = caller.UserID
	}
	if caller.APIKeyID > 0 {
		tracker.apiKeyID = caller.APIKeyID
	}
	if caller.APIKeyName != "" {
		tracker.apiKeyName = caller.APIKeyName
	}
	if caller.Username != "" {
		tracker.username = caller.Username
	}
	if caller.Endpoint != "" {
		tracker.clientPath = caller.Endpoint
	}
}

func (t *Tracker) notePath(path string) {
	if t == nil || path == "" {
		return
	}
	t.mu.Lock()
	if t.clientPath == "" {
		t.clientPath = path
	}
	t.mu.Unlock()
}

func (t *Tracker) take(accountID int64) (seq int, reason string, switches int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	reason = t.reason
	if reason == "" {
		reason = ReasonInitial
	}
	t.reason = ""
	if accountID > 0 {
		t.finalAccount = accountID
	}
	return t.seq, reason, t.switches
}

func (t *Tracker) snapshot() (attempts int, accountID int64, caller InboundCaller) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seq, t.finalAccount, InboundCaller{
		UserID: t.userID, APIKeyID: t.apiKeyID, APIKeyName: t.apiKeyName,
		Username: t.username, Endpoint: t.clientPath,
	}
}
