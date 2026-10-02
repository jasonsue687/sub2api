package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const strictTestSessionID = "c72554f2-1234-5678-abcd-123456789abc"

func strictTestConfig(enabled bool) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.StrictSessionBinding.Enabled = enabled
	return cfg
}

func TestStickySessionTTLRemainsOneHour(t *testing.T) {
	require.Equal(t, time.Hour, stickySessionTTL)
}

func TestStrictPlanInactiveWhenDisabled(t *testing.T) {
	plan, err := ResolveStrictSessionPlan(nil, StrictSessionIdentityInput{APIKeyID: 1, SessionHeaderValue: "sess"})
	require.NoError(t, err)
	require.False(t, plan.Active)

	plan, err = ResolveStrictSessionPlan(strictTestConfig(false), StrictSessionIdentityInput{
		APIKeyID:           1,
		SessionHeaderValue: "sess",
	})
	require.NoError(t, err)
	require.False(t, plan.Active)
}

func TestStrictModeRejectsMissingStableSessionID(t *testing.T) {
	_, err := ResolveStrictSessionPlan(strictTestConfig(true), StrictSessionIdentityInput{
		APIKeyID:       7,
		MetadataUserID: "not-a-session-and-not-a-digest",
	})
	require.ErrorIs(t, err, ErrStrictSessionIDRequired)
}

func TestStrictIdentityIsolation(t *testing.T) {
	cfg := strictTestConfig(true)
	base := StrictSessionIdentityInput{APIKeyID: 10, MetadataUserID: strictMetadata(strictTestSessionID)}
	first, err := ResolveStrictSessionPlan(cfg, base)
	require.NoError(t, err)

	otherSession, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID:       10,
		MetadataUserID: strictMetadata("11111111-2222-3333-4444-555555555555"),
	})
	require.NoError(t, err)
	require.NotEqual(t, first.BindingKey, otherSession.BindingKey)

	otherTenant, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID:       11,
		MetadataUserID: strictMetadata(strictTestSessionID),
	})
	require.NoError(t, err)
	require.NotEqual(t, first.BindingKey, otherTenant.BindingKey)

	groupA := int64(1)
	groupB := int64(2)
	withGroupA, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID: 10, GroupID: &groupA, MetadataUserID: strictMetadata(strictTestSessionID),
	})
	require.NoError(t, err)
	withGroupB, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID: 10, GroupID: &groupB, MetadataUserID: strictMetadata(strictTestSessionID),
	})
	require.NoError(t, err)
	require.Equal(t, withGroupA.BindingKey, withGroupB.BindingKey, "group changes must not mint a new binding key")
	require.NotContains(t, first.BindingKey, strictTestSessionID)
	require.NotContains(t, first.SessionFingerprint, strictTestSessionID)
	require.NotEqual(t, first.SessionID, first.SessionFingerprint)
}

func TestStrictEndUserHeaderSplitsSharedAPIKey(t *testing.T) {
	cfg := strictTestConfig(true)
	cfg.Gateway.StrictSessionBinding.EndUserHeader = "X-End-User-Id"
	_, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID: 1, SessionHeaderValue: "shared-session",
	})
	require.ErrorIs(t, err, ErrStrictEndUserRequired)

	alice, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID: 1, SessionHeaderValue: "shared-session", EndUserHeaderValue: "alice",
	})
	require.NoError(t, err)
	bob, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{
		APIKeyID: 1, SessionHeaderValue: "shared-session", EndUserHeaderValue: "bob",
	})
	require.NoError(t, err)
	require.NotEqual(t, alice.BindingKey, bob.BindingKey)
	require.NotContains(t, alice.BindingKey, "alice")
	require.NotContains(t, alice.EndUserFingerprint, "alice")
}

func TestBindingSurvivesAgeAndDoesNotRewrite(t *testing.T) {
	store := NewMemoryStrictSessionBindingStore()
	ctx := context.Background()
	created, err := store.Create(ctx, &StrictSessionBinding{
		BindingKey: "k", SessionFingerprint: "fp", AccountID: 1, Protocol: StrictSessionProtocol, APIKeyID: 3,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), created.AccountID)

	again, err := store.Create(ctx, &StrictSessionBinding{
		BindingKey: "k", SessionFingerprint: "fp", AccountID: 2, Protocol: StrictSessionProtocol, APIKeyID: 3,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), again.AccountID)

	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, int64(1), got.AccountID)
	require.True(t, time.Since(got.CreatedAt) > 24*time.Hour)
}

func TestStoreReadAndWriteFailuresDoNotLookLikeANewSession(t *testing.T) {
	ctx := context.Background()
	readFail := NewMemoryStrictSessionBindingStore()
	readFail.GetErr = errors.New("db read down")
	_, err := (&StrictSessionSelector{Store: readFail, OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
		t.Fatal("read failure must not select another account")
		return nil, nil
	}}).Select(ctx, &StrictSessionPlan{Active: true, BindingKey: "k", Protocol: StrictSessionProtocol}, nil)
	require.ErrorIs(t, err, ErrStrictSessionStore)
	require.NotErrorIs(t, err, ErrStrictBindingNotFound)

	writeFail := NewMemoryStrictSessionBindingStore()
	writeFail.CreateErr = errors.New("db write down")
	released := false
	_, err = (&StrictSessionSelector{
		Store: writeFail,
		OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Account: &Account{ID: 9}, Acquired: true, ReleaseFunc: func() { released = true }}, nil
		},
	}).Select(ctx, &StrictSessionPlan{Active: true, BindingKey: "k", Protocol: StrictSessionProtocol, APIKeyID: 1}, nil)
	require.ErrorIs(t, err, ErrStrictSessionStore)
	require.True(t, released)
	_, getErr := writeFail.Get(ctx, "k")
	require.ErrorIs(t, getErr, ErrStrictBindingNotFound)
}

func TestCachedStoreSurvivesCacheMissAndCacheError(t *testing.T) {
	ctx := context.Background()
	db := NewMemoryStrictSessionBindingStore()
	cache := &fakeStrictCache{}
	store := NewCachedStrictSessionBindingStore(db, cache)
	_, err := store.Create(ctx, &StrictSessionBinding{BindingKey: "persist", AccountID: 4, SessionFingerprint: "abcd", Protocol: StrictSessionProtocol, APIKeyID: 1})
	require.NoError(t, err)

	cache.cleared = true
	got, err := store.Get(ctx, "persist")
	require.NoError(t, err)
	require.Equal(t, int64(4), got.AccountID)

	cache.err = errors.New("redis down")
	cache.cleared = false
	got, err = store.Get(ctx, "persist")
	require.NoError(t, err)
	require.Equal(t, int64(4), got.AccountID)

	db.GetErr = errors.New("db down")
	cache.err = errors.New("redis down")
	cache.accountID = 0
	_, err = store.Get(ctx, "persist")
	require.ErrorIs(t, err, db.GetErr)
	require.NotErrorIs(t, err, ErrStrictBindingNotFound)
}

func TestConcurrentFirstAssignmentProducesOneBinding(t *testing.T) {
	ctx := context.Background()
	const n = 16
	store := &strictRaceStore{
		inner:   NewMemoryStrictSessionBindingStore(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var seq atomic.Int64
	var releases atomic.Int64
	var officialCalls atomic.Int64
	plan := &StrictSessionPlan{Active: true, BindingKey: "race", SessionFingerprint: "fp", Protocol: StrictSessionProtocol, APIKeyID: 1}
	selector := StrictSessionSelector{
		Store: store,
		OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
			officialCalls.Add(1)
			id := seq.Add(1)
			return &AccountSelectionResult{Account: &Account{ID: id}, Acquired: true, ReleaseFunc: func() { releases.Add(1) }}, nil
		},
		LoadAccount: func(_ context.Context, id int64) (*Account, error) {
			return healthyStrictAccount(id, 1), nil
		},
		BlockReason: func(context.Context, *Account) (string, bool) { return "", false },
		Acquire: func(_ context.Context, account *Account) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: func() {}}, nil
		},
	}

	results := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			selected, err := selector.Select(ctx, plan, nil)
			errs[i] = err
			if err == nil && selected != nil && selected.Account != nil {
				results[i] = selected.Account.ID
			}
		}(i)
	}

	for seen := 0; seen < n; seen++ {
		select {
		case <-store.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent first writes")
		}
	}
	close(store.release)
	wg.Wait()
	for i, selectErr := range errs {
		require.NoError(t, selectErr, "request %d", i)
	}

	winner, err := store.inner.Get(ctx, "race")
	require.NoError(t, err)
	for _, id := range results {
		require.Equal(t, winner.AccountID, id)
	}
	require.Equal(t, int64(n-1), releases.Load())
	require.Equal(t, int64(n), officialCalls.Load())
}

// strictRaceStore 让每个首写都停在提交前，直到测试放行，从而覆盖唯一约束冲突路径。
type strictRaceStore struct {
	inner   *MemoryStrictSessionBindingStore
	entered chan struct{}
	release chan struct{}
}

func (s *strictRaceStore) Get(ctx context.Context, bindingKey string) (*StrictSessionBinding, error) {
	return s.inner.Get(ctx, bindingKey)
}

func (s *strictRaceStore) Create(ctx context.Context, binding *StrictSessionBinding) (*StrictSessionBinding, error) {
	s.entered <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.Create(ctx, binding)
}

func TestBoundSessionDoesNotSelectAnotherAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1)
	store := NewMemoryStrictSessionBindingStore()
	_, err := store.Create(ctx, &StrictSessionBinding{BindingKey: "bound", AccountID: 1, SessionFingerprint: "fp", Protocol: StrictSessionProtocol, APIKeyID: 1, GroupID: &groupID})
	require.NoError(t, err)

	svc := &GatewayService{}
	account := healthyStrictAccount(1, groupID)
	selected, err := (&StrictSessionSelector{
		Store: store,
		OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
			t.Fatal("bound session must not call the official selector")
			return nil, nil
		},
		LoadAccount: func(context.Context, int64) (*Account, error) { return account, nil },
		BlockReason: func(ctx context.Context, got *Account) (string, bool) {
			return svc.strictAccountBlockReason(ctx, got, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
		},
		Acquire: func(_ context.Context, got *Account) (*AccountSelectionResult, error) {
			require.Equal(t, int64(1), got.ID)
			return &AccountSelectionResult{Account: got, Acquired: true, ReleaseFunc: func() {}}, nil
		},
	}).Select(ctx, &StrictSessionPlan{Active: true, BindingKey: "bound", SessionFingerprint: "fp"}, &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.Account.ID)
}

func TestBoundAccountUnavailableReasonsDoNotReselect(t *testing.T) {
	groupID := int64(8)
	svc := &GatewayService{}
	cases := []struct {
		name   string
		mutate func(*Account)
		reason string
	}{
		{name: "rate limited", mutate: func(a *Account) { until := time.Now().Add(time.Minute); a.RateLimitResetAt = &until }, reason: "rate_limited"},
		{name: "disabled", mutate: func(a *Account) { a.Status = StatusDisabled }, reason: "disabled"},
		{name: "schedulable off", mutate: func(a *Account) { a.Schedulable = false }, reason: "disabled"},
		{name: "removed from pool", mutate: func(a *Account) { a.AccountGroups = nil }, reason: "removed_from_pool"},
		{name: "model unsupported", mutate: func(a *Account) {
			a.Type = AccountTypeAPIKey
			a.Credentials = map[string]any{"model_mapping": map[string]any{"claude-haiku-4-5": "claude-haiku-4-5"}}
		}, reason: "model_unsupported"},
		{name: "quota exceeded", mutate: func(a *Account) {
			a.Type = AccountTypeAPIKey
			a.Extra = map[string]any{"quota_limit": 1.0, "quota_used": 2.0}
		}, reason: "quota_exceeded"},
		{name: "authorization invalid", mutate: func(a *Account) {
			past := time.Now().Add(-time.Hour)
			a.AutoPauseOnExpired = true
			a.ExpiresAt = &past
		}, reason: "authorization_invalid"},
		{name: "overloaded", mutate: func(a *Account) { until := time.Now().Add(time.Minute); a.OverloadUntil = &until }, reason: "overloaded"},
		{name: "temporarily unschedulable", mutate: func(a *Account) { until := time.Now().Add(time.Minute); a.TempUnschedulableUntil = &until }, reason: "temporarily_unschedulable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := healthyStrictAccount(1, groupID)
			tc.mutate(account)
			reason, blocked := svc.strictAccountBlockReason(context.Background(), account, &groupID, "claude-opus-4-5", PlatformAnthropic, false, "sess")
			require.True(t, blocked)
			require.Equal(t, tc.reason, reason)
		})
	}

	t.Run("deleted account", func(t *testing.T) {
		_, err := (&StrictSessionSelector{
			Store: presetStore(t, "gone", 42),
			OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
				t.Fatal("deleted binding must not select another subscription account")
				return nil, nil
			},
			LoadAccount: func(context.Context, int64) (*Account, error) { return nil, ErrAccountNotFound },
		}).Select(context.Background(), &StrictSessionPlan{BindingKey: "gone", SessionFingerprint: "fp"}, &groupID)
		var fallback *StrictSessionFallbackError
		require.ErrorAs(t, err, &fallback)
		require.Equal(t, "account_deleted", fallback.Reason)
		require.Equal(t, int64(42), fallback.AccountID)
	})

	t.Run("session capacity", func(t *testing.T) {
		account := healthyStrictAccount(3, groupID)
		account.Type = AccountTypeOAuth
		account.Extra = map[string]any{"max_sessions": 1}
		svc := &GatewayService{sessionLimitCache: stubSessionLimitCache{reject: true}}
		reason, blocked := svc.strictAccountBlockReason(context.Background(), account, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
		require.True(t, blocked)
		require.Equal(t, "session_capacity", reason)
	})

	t.Run("window cost", func(t *testing.T) {
		account := healthyStrictAccount(3, groupID)
		account.Type = AccountTypeOAuth
		account.Extra = map[string]any{"window_cost_limit": 1.0, "window_cost_sticky_reserve": 1.0}
		svc := &GatewayService{sessionLimitCache: stubSessionLimitCache{windowHit: true, windowCost: 10}}
		reason, blocked := svc.strictAccountBlockReason(context.Background(), account, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
		require.True(t, blocked)
		require.Equal(t, "window_cost_exhausted", reason)
	})

	t.Run("rpm", func(t *testing.T) {
		account := healthyStrictAccount(3, groupID)
		account.Type = AccountTypeOAuth
		account.Extra = map[string]any{"base_rpm": 1}
		svc := &GatewayService{rpmCache: stubRPMCache{count: 100}}
		reason, blocked := svc.strictAccountBlockReason(context.Background(), account, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
		require.True(t, blocked)
		require.Equal(t, "rpm_exceeded", reason)
	})
}

func TestAccountRecoveryReturnsToOriginalBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1)
	store := presetStore(t, "recover", 5)
	account := healthyStrictAccount(5, groupID)
	until := time.Now().Add(time.Minute)
	account.RateLimitResetAt = &until
	svc := &GatewayService{}
	selector := StrictSessionSelector{
		Store: store,
		OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
			t.Fatal("recovery must not assign a new subscription account")
			return nil, nil
		},
		LoadAccount: func(context.Context, int64) (*Account, error) { return account, nil },
		BlockReason: func(ctx context.Context, got *Account) (string, bool) {
			return svc.strictAccountBlockReason(ctx, got, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
		},
		Acquire: func(_ context.Context, got *Account) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Account: got, Acquired: true, ReleaseFunc: func() {}}, nil
		},
	}
	_, err := selector.Select(ctx, &StrictSessionPlan{BindingKey: "recover"}, &groupID)
	var fallback *StrictSessionFallbackError
	require.ErrorAs(t, err, &fallback)
	require.Equal(t, "rate_limited", fallback.Reason)

	account.RateLimitResetAt = nil
	selected, err := selector.Select(ctx, &StrictSessionPlan{BindingKey: "recover"}, &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(5), selected.Account.ID)
	got, err := store.Get(ctx, "recover")
	require.NoError(t, err)
	require.Equal(t, int64(5), got.AccountID)
}

func TestThirdPartyResultDoesNotChangeBinding(t *testing.T) {
	store := presetStore(t, "tp", 6)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "relay-secret", r.Header.Get("x-api-key"))
		require.Empty(t, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		require.Contains(t, string(body), "hello")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := strictTestConfig(true)
	cfg.Gateway.StrictSessionBinding.ThirdParty.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL
	cfg.Gateway.StrictSessionBinding.ThirdParty.APIKey = "relay-secret"
	svc := &GatewayService{cfg: cfg}
	recorder := httptest.NewRecorder()
	err := svc.ForwardStrictThirdParty(context.Background(), http.Header{"Authorization": []string{"Bearer client"}}, []byte(`{"messages":[{"content":"hello"}]}`), recorder)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"ok":true`)

	got, err := store.Get(context.Background(), "tp")
	require.NoError(t, err)
	require.Equal(t, int64(6), got.AccountID)

	upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	})
	err = svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{"messages":[]}`), httptest.NewRecorder())
	var statusErr *StrictThirdPartyStatusError
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
	got, err = store.Get(context.Background(), "tp")
	require.NoError(t, err)
	require.Equal(t, int64(6), got.AccountID)
}

func TestThirdPartyPartialBodyIsNotRetried(t *testing.T) {
	cfg := strictTestConfig(true)
	cfg.Gateway.StrictSessionBinding.ThirdParty.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = "https://relay.example"
	cfg.Gateway.StrictSessionBinding.ThirdParty.APIKey = "relay-secret"
	svc := &GatewayService{cfg: cfg, strictThirdPartyHTTP: &http.Client{Transport: strictRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(&errAfterReader{data: []byte("data: partial\n\n")}),
			Request:    r,
		}, nil
	})}}
	recorder := httptest.NewRecorder()
	err := svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{"stream":true}`), recorder)
	var statusErr *StrictThirdPartyStatusError
	require.ErrorAs(t, err, &statusErr)
	require.True(t, statusErr.WroteBody)
	require.Contains(t, recorder.Body.String(), "data: partial")
	require.NotContains(t, recorder.Body.String(), "relay-secret")
}

func TestDecideStrictFallback(t *testing.T) {
	require.Equal(t, StrictFallbackStreamInterrupted, DecideStrictFallback(StrictFallbackInput{ResponseWritten: true, RetryableUpstream: true, ThirdPartyEnabled: true}))
	require.Equal(t, StrictFallbackPassthrough, DecideStrictFallback(StrictFallbackInput{ClientCanceled: true}))
	require.Equal(t, StrictFallbackPassthrough, DecideStrictFallback(StrictFallbackInput{RetryableUpstream: false}))
	require.Equal(t, StrictFallbackThirdParty, DecideStrictFallback(StrictFallbackInput{AccountSide: true, ThirdPartyEnabled: true}))
	require.Equal(t, StrictFallbackError, DecideStrictFallback(StrictFallbackInput{AccountSide: true, ThirdPartyEnabled: false}))
	require.Equal(t, StrictFallbackThirdParty, DecideStrictFallback(StrictFallbackInput{RetryableUpstream: true, ThirdPartyEnabled: true}))
	require.Equal(t, StrictFallbackError, DecideStrictFallback(StrictFallbackInput{RetryableUpstream: true, ThirdPartyEnabled: false}))
}

func TestBindingSurvivesDisableAndReenable(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStrictSessionBindingStore()
	cfg := strictTestConfig(true)
	svc := &GatewayService{cfg: cfg, strictSessionStore: store}
	in := StrictSessionIdentityInput{APIKeyID: 4, SessionHeaderValue: "persist-session"}
	plan, err := svc.PrepareStrictSession(ctx, in)
	require.NoError(t, err)
	require.True(t, plan.Active)
	require.Zero(t, plan.BoundAccountID)

	selected, err := (&StrictSessionSelector{
		Store: store,
		OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Account: healthyStrictAccount(8, 1), Acquired: true, ReleaseFunc: func() {}}, nil
		},
		LoadAccount: func(_ context.Context, id int64) (*Account, error) { return healthyStrictAccount(id, 1), nil },
		Acquire: func(_ context.Context, account *Account) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: func() {}}, nil
		},
	}).Select(ctx, plan, nil)
	require.NoError(t, err)
	require.Equal(t, int64(8), selected.Account.ID)

	cfg.Gateway.StrictSessionBinding.Enabled = false
	disabled, err := svc.PrepareStrictSession(ctx, in)
	require.NoError(t, err)
	require.False(t, disabled.Active)
	stored, err := store.Get(ctx, plan.BindingKey)
	require.NoError(t, err)
	require.Equal(t, int64(8), stored.AccountID)

	cfg.Gateway.StrictSessionBinding.Enabled = true
	again, err := svc.PrepareStrictSession(ctx, in)
	require.NoError(t, err)
	require.True(t, again.Active)
	require.Equal(t, int64(8), again.BoundAccountID)
	require.Equal(t, plan.BindingKey, again.BindingKey)
}

func TestSimpleModeDoesNotPermanentlyRemoveForeignGroup(t *testing.T) {
	groupID := int64(1)
	account := healthyStrictAccount(3, 99)
	simple := &GatewayService{cfg: &config.Config{RunMode: config.RunModeSimple}}
	reason, blocked := simple.strictAccountBlockReason(context.Background(), account, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
	require.False(t, blocked, reason)

	standard := &GatewayService{cfg: &config.Config{RunMode: config.RunModeStandard}}
	reason, blocked = standard.strictAccountBlockReason(context.Background(), account, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
	require.True(t, blocked)
	require.Equal(t, "removed_from_pool", reason)
}

func TestStrictPlatformMatchesOfficialSamePlatformFilter(t *testing.T) {
	svc := &GatewayService{}
	groupID := int64(1)
	anthropic := healthyStrictAccount(2, groupID)
	reason, blocked := svc.strictAccountBlockReason(context.Background(), anthropic, &groupID, "claude-sonnet-4-5", PlatformAntigravity, true, "sess")
	require.True(t, blocked)
	require.Equal(t, "platform_mismatch", reason)

	mixed := healthyStrictAccount(4, groupID)
	mixed.Platform = PlatformAntigravity
	mixed.Extra = map[string]any{"mixed_scheduling": true}
	reason, blocked = svc.strictAccountBlockReason(context.Background(), mixed, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
	require.False(t, blocked, reason)

	plain := healthyStrictAccount(5, groupID)
	plain.Platform = PlatformAntigravity
	reason, blocked = svc.strictAccountBlockReason(context.Background(), plain, &groupID, "claude-sonnet-4-5", PlatformAnthropic, false, "sess")
	require.True(t, blocked)
	require.Equal(t, "platform_mismatch", reason)
}

func TestGenerateSessionHashRedactsIdentityWhenStrict(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sessionID := strictTestSessionID
	deviceID := "d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677"
	metadata := strictMetadata(sessionID)
	require.Contains(t, metadata, sessionID)
	require.Contains(t, metadata, deviceID)

	svc := &GatewayService{cfg: strictTestConfig(true)}
	require.Equal(t, sessionID, svc.GenerateSessionHash(&ParsedRequest{MetadataUserID: metadata}))
	logged := buf.String()
	require.NotContains(t, logged, sessionID)
	require.NotContains(t, logged, deviceID)
	require.NotContains(t, logged, metadata)
	require.Contains(t, logged, "session_fp")
}

func TestThirdPartyPassthrough4xxRejectsRedirectAndFlushes(t *testing.T) {
	var redirected atomic.Int32
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = w.Write([]byte("stolen"))
	}))
	t.Cleanup(secret.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/redirect/v1/messages"):
			http.Redirect(w, r, secret.URL, http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/client/v1/messages"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"invalid_request_error","message":"model not supported"}`))
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: one\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: two\n\n"))
			flusher.Flush()
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := strictTestConfig(true)
	cfg.Gateway.StrictSessionBinding.ThirdParty.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.APIKey = "relay-secret"
	cfg.Gateway.StrictSessionBinding.ThirdParty.TimeoutSeconds = 2
	svc := &GatewayService{cfg: cfg}

	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL + "/client"
	recorder := httptest.NewRecorder()
	err := svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{"model":"claude"}`), recorder)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "model not supported")
	require.NotContains(t, recorder.Body.String(), "relay-secret")

	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL + "/redirect"
	err = svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{}`), httptest.NewRecorder())
	require.Error(t, err)
	require.Zero(t, redirected.Load())

	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL + "/stream"
	streamRecorder := httptest.NewRecorder()
	err = svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{"stream":true}`), streamRecorder)
	require.NoError(t, err)
	require.True(t, streamRecorder.Flushed)
	require.Contains(t, streamRecorder.Body.String(), "data: two")
}

func TestThirdPartyHeaderTimeoutDoesNotCutActiveStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/hang-header/v1/messages") {
			time.Sleep(1500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < 4; i++ {
			_, _ = w.Write([]byte("data: chunk\n\n"))
			flusher.Flush()
			time.Sleep(400 * time.Millisecond)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := strictTestConfig(true)
	cfg.Gateway.StrictSessionBinding.ThirdParty.Enabled = true
	cfg.Gateway.StrictSessionBinding.ThirdParty.APIKey = "relay-secret"
	cfg.Gateway.StrictSessionBinding.ThirdParty.TimeoutSeconds = 1
	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL + "/hang-header"
	svc := &GatewayService{cfg: cfg}
	err := svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{}`), httptest.NewRecorder())
	require.Error(t, err)

	cfg.Gateway.StrictSessionBinding.ThirdParty.BaseURL = upstream.URL + "/slow-body"
	recorder := httptest.NewRecorder()
	err = svc.ForwardStrictThirdParty(context.Background(), nil, []byte(`{"stream":true}`), recorder)
	require.NoError(t, err)
	require.Equal(t, 4, bytes.Count(recorder.Body.Bytes(), []byte("data: chunk")))
}

func TestEndUserHeaderRequiresExplicitTrust(t *testing.T) {
	cfg := config.GatewayStrictSessionBindingConfig{Enabled: true, EndUserHeader: "X-End-User"}
	require.Error(t, cfg.NormalizeAndValidate())
	cfg.EndUserHeaderTrusted = true
	require.NoError(t, cfg.NormalizeAndValidate())
}

func TestStrictBoundWaitStaysOnSameAccount(t *testing.T) {
	require.True(t, strictBoundWaitAllowed(false, 0, 3))
	require.False(t, strictBoundWaitAllowed(false, 3, 3))
	require.False(t, strictBoundWaitAllowed(true, 0, 3))
}

func TestIndependentSessionsCanUseDifferentAccounts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStrictSessionBindingStore()
	cfg := strictTestConfig(true)
	sessionX, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{APIKeyID: 1, SessionHeaderValue: "session-x"})
	require.NoError(t, err)
	sessionY, err := ResolveStrictSessionPlan(cfg, StrictSessionIdentityInput{APIKeyID: 1, SessionHeaderValue: "session-y"})
	require.NoError(t, err)

	pick := map[string]int64{sessionX.BindingKey: 1, sessionY.BindingKey: 2}
	newSelector := func(plan *StrictSessionPlan) StrictSessionSelector {
		return StrictSessionSelector{
			Store: store,
			OfficialSelect: func(context.Context) (*AccountSelectionResult, error) {
				return &AccountSelectionResult{Account: &Account{ID: pick[plan.BindingKey]}, ReleaseFunc: func() {}}, nil
			},
			LoadAccount: func(_ context.Context, id int64) (*Account, error) { return &Account{ID: id}, nil },
			Acquire: func(_ context.Context, account *Account) (*AccountSelectionResult, error) {
				return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: func() {}}, nil
			},
		}
	}
	gotX, err := newSelector(sessionX).Select(ctx, sessionX, nil)
	require.NoError(t, err)
	gotY, err := newSelector(sessionY).Select(ctx, sessionY, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), gotX.Account.ID)
	require.Equal(t, int64(2), gotY.Account.ID)

	again, err := newSelector(sessionX).Select(ctx, sessionX, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), again.Account.ID)
}

func strictMetadata(sessionID string) string {
	return `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"","session_id":"` + sessionID + `"}`
}

func healthyStrictAccount(id, groupID int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		AccountGroups: []AccountGroup{{
			AccountID: id,
			GroupID:   groupID,
		}},
	}
}

func presetStore(t *testing.T, key string, accountID int64) *MemoryStrictSessionBindingStore {
	t.Helper()
	store := NewMemoryStrictSessionBindingStore()
	_, err := store.Create(context.Background(), &StrictSessionBinding{
		BindingKey: key, AccountID: accountID, SessionFingerprint: "fp", Protocol: StrictSessionProtocol, APIKeyID: 1,
	})
	require.NoError(t, err)
	return store
}

type fakeStrictCache struct {
	accountID int64
	cleared   bool
	err       error
}

func (f *fakeStrictCache) Get(context.Context, string) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	if f.cleared || f.accountID <= 0 {
		return 0, false, nil
	}
	return f.accountID, true, nil
}

func (f *fakeStrictCache) Set(_ context.Context, _ string, accountID int64) error {
	if f.err != nil {
		return f.err
	}
	f.accountID = accountID
	f.cleared = false
	return nil
}

type stubSessionLimitCache struct {
	reject     bool
	windowHit  bool
	windowCost float64
}

func (s stubSessionLimitCache) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	return !s.reject, nil
}
func (stubSessionLimitCache) RefreshSession(context.Context, int64, string, time.Duration) error {
	return nil
}
func (stubSessionLimitCache) UnregisterSession(context.Context, int64, string) error { return nil }
func (stubSessionLimitCache) GetActiveSessionCount(context.Context, int64) (int, error) {
	return 0, nil
}
func (stubSessionLimitCache) GetActiveSessionCountBatch(context.Context, []int64, map[int64]time.Duration) (map[int64]int, error) {
	return nil, nil
}
func (stubSessionLimitCache) IsSessionActive(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (s stubSessionLimitCache) GetWindowCost(context.Context, int64) (float64, bool, error) {
	return s.windowCost, s.windowHit, nil
}
func (stubSessionLimitCache) SetWindowCost(context.Context, int64, float64) error { return nil }
func (stubSessionLimitCache) GetWindowCostBatch(context.Context, []int64) (map[int64]float64, error) {
	return nil, nil
}

type stubRPMCache struct{ count int }

func (s stubRPMCache) IncrementRPM(context.Context, int64) (int, error) { return s.count, nil }
func (s stubRPMCache) GetRPM(context.Context, int64) (int, error)       { return s.count, nil }
func (stubRPMCache) GetRPMBatch(context.Context, []int64) (map[int64]int, error) {
	return nil, nil
}

type strictRoundTripFunc func(*http.Request) (*http.Response, error)

func (f strictRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errAfterReader struct {
	data []byte
	n    int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.n >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data[r.n:])
	r.n += n
	if r.n >= len(r.data) {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}
