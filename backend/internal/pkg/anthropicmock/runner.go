package anthropicmock

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
)

// ErrMockNotGuaranteed means a one-click run was refused because mock
// interception could not be proven before any sample was replayed.
var ErrMockNotGuaranteed = errors.New("anthropic mock intercept is not guaranteed; refusing to run")

// ErrRunBusy means another one-click run is already in progress.
var ErrRunBusy = errors.New("an anthropic mock test run is already in progress")

// ErrStoreUnavailable means the corpus store is not ready.
var ErrStoreUnavailable = errors.New("anthropic mock store is not available")

// RunOptions controls one in-process corpus replay.
type RunOptions struct {
	// APIKey is applied only to this run. It is never written to the corpus.
	APIKey string
	Limit  int
}

// RunItem is the outcome of replaying one sample.
type RunItem struct {
	SampleID int64  `json:"sample_id"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Error    string `json:"error,omitempty"`
}

// RunReport is the summary returned to the admin page.
type RunReport struct {
	Total          int       `json:"total"`
	Completed      int       `json:"completed"`
	Failed         int       `json:"failed"`
	MockGuaranteed bool      `json:"mock_guaranteed"`
	Results        []RunItem `json:"results"`
}

// Status is the admin snapshot of the switch and capture queues.
type Status struct {
	Enabled         bool   `json:"enabled"`
	Source          string `json:"source"`
	HookArmed       bool   `json:"hook_armed"`
	ReplayReady     bool   `json:"replay_ready"`
	StoreReady      bool   `json:"store_ready"`
	InboundDropped  uint64 `json:"inbound_dropped"`
	OutboundDropped uint64 `json:"outbound_dropped"`
	InboundQueued   int    `json:"inbound_queued"`
	OutboundQueued  int    `json:"outbound_queued"`
	SampleCount     int64  `json:"sample_count"`
}

// CurrentStatus reads the in-memory switch and, when a store is configured, the sample count.
func CurrentStatus(ctx context.Context) (Status, error) {
	snap := Status{
		Enabled:         enabled.Load(),
		Source:          settingSource(),
		HookArmed:       hookArmed.Load(),
		ReplayReady:     replayHandler() != nil,
		InboundDropped:  inboundDropped.Load(),
		OutboundDropped: outboundDropped.Load(),
		InboundQueued:   len(inboundQ),
		OutboundQueued:  len(outboundQ),
	}
	st := currentStore()
	snap.StoreReady = st != nil
	if st == nil {
		return snap, nil
	}
	n, err := st.CountSamples(ctx)
	if err != nil {
		return snap, err
	}
	snap.SampleCount = n
	return snap, nil
}

// SetEnabled persists the admin switch and updates this process immediately.
func SetEnabled(ctx context.Context, on bool) error {
	st := currentStore()
	if st == nil {
		return ErrStoreUnavailable
	}
	value := "false"
	if on {
		value = "true"
	}
	if err := st.SetSetting(ctx, settingKey, value); err != nil {
		return err
	}
	enabled.Store(on)
	source.Store("db")
	return nil
}

// Run replays corpus samples through the in-process server.
// It refuses to start unless the outbound hook is armed and a synthetic
// Anthropic request is intercepted. While the run is active every Anthropic
// host is mocked, including a replay whose force flag was dropped.
func Run(ctx context.Context, opts RunOptions) (*RunReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !hookArmed.Load() || replayHandler() == nil || currentStore() == nil {
		return nil, ErrMockNotGuaranteed
	}
	if !runMu.TryLock() {
		return nil, ErrRunBusy
	}
	defer runMu.Unlock()

	testRuns.Add(1)
	defer testRuns.Add(-1)

	if err := proveIntercept(); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	if limit > maxRunLimit {
		limit = maxRunLimit
	}
	samples, _, err := currentStore().ListSamples(ctx, 0, limit)
	if err != nil {
		return nil, err
	}
	report := &RunReport{Total: len(samples), MockGuaranteed: true, Results: []RunItem{}}
	handler := replayHandler()
	for _, sample := range samples {
		if ctx.Err() != nil {
			break
		}
		item := replayOne(handler, sample, opts.APIKey)
		report.Results = append(report.Results, item)
		if item.Error != "" {
			report.Failed++
			continue
		}
		report.Completed++
	}
	return report, nil
}

func proveIntercept() error {
	probe, err := http.NewRequestWithContext(withSilent(context.Background()), http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		return ErrMockNotGuaranteed
	}
	resp, ok := Intercept(probe)
	if !ok || resp == nil {
		return ErrMockNotGuaranteed
	}
	_ = resp.Body.Close()
	other, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		return ErrMockNotGuaranteed
	}
	if _, intercepted := Intercept(other); intercepted {
		return ErrMockNotGuaranteed
	}
	return nil
}

func replayOne(h http.Handler, sample Sample, apiKey string) (item RunItem) {
	item = RunItem{SampleID: sample.ID, Method: sample.Method, Path: sample.Path}
	defer func() {
		if rec := recover(); rec != nil {
			item.Error = "panic"
			item.Status = 0
		}
	}()
	method := sample.Method
	if method == "" {
		method = http.MethodPost
	}
	path := sample.Path
	if path == "" {
		path = "/"
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(sample.Body))
	req = req.WithContext(WithSkipCapture(WithForce(req.Context())))
	if sample.ContentType != "" {
		req.Header.Set("Content-Type", sample.ContentType)
	}
	for key, value := range sample.Headers {
		if sensitiveHeader(key) {
			continue
		}
		req.Header.Set(key, value)
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	item.Status = rec.Code
	if rec.Code >= 500 {
		item.Error = "upstream handler failed"
	}
	return item
}
