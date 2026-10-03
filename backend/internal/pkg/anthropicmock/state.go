// Package anthropicmock intercepts outbound Anthropic calls, records them, and
// collects inbound gateway bodies for later mock replay.
//
// The global switch defaults to off. While it is off and no test run is active,
// Intercept returns immediately and callers proceed with the real transport.
package anthropicmock

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
)

const (
	settingKey        = "anthropic_mock_enabled"
	inboundQueueSize  = 256
	outboundQueueSize = 128
	maxInboundBody    = 256 << 10
	maxOutboundBody   = 1 << 20
	maxSeenKeys       = 20000
	defaultRunLimit   = 20
	maxRunLimit       = 50
	headerMock        = "X-Asterflow-Anthropic-Mock" // must match anthropicaudit.MockHeader
	mockReasonForced  = "forced"
	mockReasonTestRun = "test_run"
	mockReasonSwitch  = "switch"
)

type forceKey struct{}
type skipCaptureKey struct{}
type silentKey struct{}

var (
	enabled           atomic.Bool
	hookArmed         atomic.Bool
	interceptDisabled atomic.Bool
	testRuns          atomic.Int32
	inboundDropped    atomic.Uint64
	outboundDropped   atomic.Uint64
	inboundFailed     atomic.Uint64
	outboundFailed    atomic.Uint64
	seenN             atomic.Int64
	source            atomic.Value // string: "config" or "db"
	replay            atomic.Pointer[replayBox]
	runMu             sync.Mutex
	storeMu           sync.RWMutex
	store             Store
	seen              sync.Map
	startOnce         sync.Once
	inboundQ          chan inboundJob
	outboundQ         chan outboundRecord
)

type replayBox struct {
	h http.Handler
}

func init() {
	inboundQ = make(chan inboundJob, inboundQueueSize)
	outboundQ = make(chan outboundRecord, outboundQueueSize)
	source.Store("config")
}

// ArmOutboundHook marks the real outbound HTTP boundary as wired to Intercept.
// One-click runs refuse to start until this has been called.
func ArmOutboundHook() { hookArmed.Store(true) }

// HookArmed reports whether the outbound boundary has installed Intercept.
func HookArmed() bool { return hookArmed.Load() }

// Enabled reports the global mock switch. It is safe for the request hot path.
func Enabled() bool { return enabled.Load() }

// WithForce marks this request, and any upstream request derived from its
// context, as required to use mock. Callers cannot set this from an inbound
// header; only in-process test replay applies it.
func WithForce(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, forceKey{}, true)
}

// Forced reports whether ctx requires Anthropic mock interception.
func Forced(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(forceKey{}).(bool)
	return on
}

// WithSkipCapture tells the inbound collector to ignore this request.
func WithSkipCapture(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipCaptureKey{}, true)
}

func skipCapture(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(skipCaptureKey{}).(bool)
	return on
}

func withSilent(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, silentKey{}, true)
}

func silent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(silentKey{}).(bool)
	return on
}

// SetReplayHandler installs the in-process HTTP handler used by one-click replay.
func SetReplayHandler(h http.Handler) {
	if h == nil {
		replay.Store(nil)
		return
	}
	replay.Store(&replayBox{h: h})
}

func replayHandler() http.Handler {
	box := replay.Load()
	if box == nil || box.h == nil {
		return nil
	}
	return box.h
}

// SetStore replaces the persistence backend. A nil store disables writes.
func SetStore(s Store) {
	storeMu.Lock()
	store = s
	storeMu.Unlock()
}

func currentStore() Store {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return store
}

// CurrentStore returns the configured persistence backend, or nil before startup.
func CurrentStore() Store { return currentStore() }

// SetEnabledForTest flips only the in-memory switch.
func SetEnabledForTest(on bool) { enabled.Store(on) }

// DisableInterceptForTest forces Intercept to refuse so callers can prove a
// test run stops before any request is issued.
func DisableInterceptForTest(on bool) { interceptDisabled.Store(on) }

// ResetForTest restores process-local switches. It does not stop background workers.
func ResetForTest() {
	interceptDisabled.Store(false)
	hookArmed.Store(false)
	enabled.Store(false)
	testRuns.Store(0)
	inboundDropped.Store(0)
	outboundDropped.Store(0)
	inboundFailed.Store(0)
	outboundFailed.Store(0)
	source.Store("config")
	replay.Store(nil)
	SetStore(nil)
	seen.Range(func(key, _ any) bool {
		seen.Delete(key)
		return true
	})
	seenN.Store(0)
	drainQueue(inboundQ)
	drainQueue(outboundQ)
}

func drainQueue[T any](ch chan T) {
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func settingSource() string {
	v, _ := source.Load().(string)
	if v == "" {
		return "config"
	}
	return v
}
