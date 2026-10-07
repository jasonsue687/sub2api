//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagesStrictSessionCapacityAcrossIdentitySources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, source := range []struct {
		name          string
		metadata      string
		headers       http.Header
		sessionHeader string
	}{
		{name: "session-only metadata", metadata: `{"session_id":"` + strictHTTPSessionID + `"}`},
		{name: "legacy metadata", metadata: "user_" + strictHTTPDeviceID + "_account__session_" + strictHTTPSessionID},
		{name: "Claude Code header", headers: http.Header{"X-Claude-Code-Session-Id": {strictHTTPSessionID}}},
		{name: "default header", headers: http.Header{"X-Session-Id": {strictHTTPSessionID}}},
		{name: "configured header", headers: http.Header{"X-Test-Session": {strictHTTPSessionID}}, sessionHeader: "X-Test-Session"},
	} {
		for _, sourceFirst := range []bool{false, true} {
			name := source.name + "/full metadata first"
			if sourceFirst {
				name = source.name + "/alternative first"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				sessions := &strictCapacitySessionCache{active: make(map[int64]map[string]struct{})}
				slots := &strictCapacityConcurrencyCache{}
				group := strictHTTPGroup(2301)
				account := strictHTTPAccount(1, group.ID, "bound")
				account.Platform = service.PlatformAnthropic
				account.Extra["max_sessions"] = 1
				cfg := &strictMessagesTestConfig{
					Config:      config.Config{RunMode: config.RunModeStandard},
					sessions:    sessions,
					concurrency: service.NewConcurrencyService(slots),
				}
				cfg.binding.Enabled = true
				cfg.binding.SessionHeader = source.sessionHeader
				cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
				store := service.NewMemoryStrictSessionBindingStore()
				repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{account.ID: account}}
				h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, repo, store)
				t.Cleanup(cleanup)
				h.concurrencyHelper = NewConcurrencyHelper(cfg.concurrency, SSEPingFormatClaude, 0)

				// Suggestion mode is handled locally after account admission; no upstream is contacted.
				// Start with either representation, switch, then switch back.
				for i := 0; i < 3; i++ {
					metadata, headers := strictHTTPMetadata(), http.Header(nil)
					if (i%2 == 0) == sourceFirst {
						metadata, headers = source.metadata, source.headers
					}
					rec, selected := postStrictMessagesWithAPIKey(t, h, group, group.ID, nil, metadata, 3101, headers)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Equal(t, account.ID, selected)
					count, err := sessions.GetActiveSessionCount(ctx, account.ID)
					require.NoError(t, err)
					assert.Equal(t, 1, count, "the same session must occupy only one slot")
					assert.Equal(t, account.ID, requireStrictStoreAccount(t, store, cfg, group.ID))
				}

				// A different session bound to this account must still hit the limit.
				otherMetadata := `{"session_id":"11111111-2222-3333-4444-555555555555"}`
				plan, err := service.ResolveStrictSessionPlan(cfg.binding, service.StrictSessionIdentityInput{MetadataUserID: otherMetadata})
				require.NoError(t, err)
				_, err = store.Create(ctx, &service.StrictSessionBinding{BindingKey: plan.BindingKey, AccountID: account.ID})
				require.NoError(t, err)
				denied, selected := postStrictMessages(t, h, group, group.ID, nil, otherMetadata)
				assert.Equal(t, http.StatusServiceUnavailable, denied.Code, denied.Body.String())
				assert.Contains(t, denied.Body.String(), `"reason":"session_capacity"`)
				assert.Zero(t, selected)

				// A rejected duplicate must preserve the shared capacity member,
				// even when identity came from an alternative source.
				slots.full = true
				account.Credentials["intercept_warmup_requests"] = false
				failed, _ := postStrictMessagesWithAPIKey(t, h, group, group.ID, nil, source.metadata, 3101, source.headers)
				assert.Equal(t, http.StatusServiceUnavailable, failed.Code, failed.Body.String())
				assert.Contains(t, failed.Body.String(), `"reason":"concurrency_exhausted"`)
				count, err := sessions.GetActiveSessionCount(ctx, account.ID)
				require.NoError(t, err)
				assert.Equal(t, 1, count, "failed duplicate admission must not remove a shared session")
				slots.full = false
				account.Credentials["intercept_warmup_requests"] = true
				stillDenied, _ := postStrictMessages(t, h, group, group.ID, nil, otherMetadata)
				assert.Equal(t, http.StatusServiceUnavailable, stillDenied.Code)
				assert.Contains(t, stillDenied.Body.String(), `"reason":"session_capacity"`)
				// Simulate the existing idle timeout expiring before a new session.
				delete(sessions.active, account.ID)
				recovered, selected := postStrictMessages(t, h, group, group.ID, nil, otherMetadata)
				assert.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
				assert.Equal(t, account.ID, selected)
			})
		}
	}
}

type strictCapacitySessionCache struct {
	service.SessionLimitCache
	active map[int64]map[string]struct{}
}

func (c *strictCapacitySessionCache) RegisterSession(_ context.Context, accountID int64, sessionID string, maxSessions int, _ time.Duration) (bool, error) {
	if c.active[accountID] == nil {
		c.active[accountID] = make(map[string]struct{})
	}
	if _, exists := c.active[accountID][sessionID]; exists {
		return true, nil
	}
	if len(c.active[accountID]) >= maxSessions {
		return false, nil
	}
	c.active[accountID][sessionID] = struct{}{}
	return true, nil
}

func (c *strictCapacitySessionCache) UnregisterSession(_ context.Context, accountID int64, sessionID string) error {
	delete(c.active[accountID], sessionID)
	return nil
}

func (c *strictCapacitySessionCache) GetActiveSessionCount(_ context.Context, accountID int64) (int, error) {
	return len(c.active[accountID]), nil
}

type strictCapacityConcurrencyCache struct {
	fakeConcurrencyCache
	full bool
}

func (c *strictCapacityConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return !c.full, nil
}

func (c *strictCapacityConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return !c.full, nil
}

func TestMessagesStrictForwardFailurePreservesCapacity(t *testing.T) {
	group := strictHTTPGroup(2302)
	account := strictHTTPAccount(1, group.ID, "bound")
	account.Platform = service.PlatformAnthropic
	account.Credentials = map[string]any{"intercept_warmup_requests": false}
	account.Extra["max_sessions"] = 1
	sessions := &strictCapacitySessionCache{active: map[int64]map[string]struct{}{account.ID: {strictHTTPSessionID: {}}}}
	slots := &strictProfitHTTPConcurrencyCache{onAcquire: func() {}}
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}, sessions: sessions, concurrency: service.NewConcurrencyService(slots)}
	cfg.binding.Enabled = true
	store := service.NewMemoryStrictSessionBindingStore()
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, &strictMessagesAccountRepo{byID: map[int64]*service.Account{account.ID: account}}, store)
	t.Cleanup(cleanup)
	// Missing credentials fail locally before any upstream transport is called.
	rec, _ := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"reason":"upstream_failed"`)
	count, err := sessions.GetActiveSessionCount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 1, slots.releases)
}

func TestStrictRetryAndExhaustionPreserveCapacity(t *testing.T) {
	group := strictHTTPGroup(2303)
	account := strictHTTPAccount(1, group.ID, "bound")
	account.Platform = service.PlatformAnthropic
	account.Extra["max_sessions"] = 1
	sessions := &strictCapacitySessionCache{active: map[int64]map[string]struct{}{account.ID: {strictHTTPSessionID: {}}}}
	cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}, sessions: sessions}
	h, cleanup := newStrictMessagesHandler(t, cfg, group, []*service.Account{account}, &strictMessagesAccountRepo{byID: map[int64]*service.Account{account.ID: account}}, nil)
	t.Cleanup(cleanup)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	fs := NewFailoverState(10, true)
	fs.EnableStrictBinding(1)
	rt := &strictSessionRuntime{Active: true}
	upstreamErr := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, RetryableOnSameAccount: true, SameAccountRetryDelay: time.Millisecond}
	for _, want := range []int{strictFlowSameAccount, strictFlowStop} {
		require.Equal(t, want, h.handleStrictUpstreamFailover(c, fs, rt, nil, account, upstreamErr, false, c.Writer.Size()))
		count, err := sessions.GetActiveSessionCount(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, 1, count)
	}
}
