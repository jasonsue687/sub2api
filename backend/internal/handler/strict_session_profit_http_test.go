//go:build unit

package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagesStrictProfitVetoAndOrdinaryReselection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, strict := range []bool{false, true} {
		name := "ordinary selection"
		if strict {
			name = "strict binding"
		}
		t.Run(name, func(t *testing.T) {
			group := strictHTTPGroup(2201)
			group.RateMultiplier = 0.5
			group.ProfitControlEnabled = true
			cheapRate, expensiveRate := 0.2, 0.8
			bound := strictHTTPAccount(1, group.ID, "bound")
			bound.RateMultiplier = &cheapRate
			other := strictHTTPAccount(2, group.ID, "other")
			other.RateMultiplier = &cheapRate
			cache := &fakeSchedulerCache{accounts: []*service.Account{bound}}
			repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{bound.ID: bound, other.ID: other}}
			store := service.NewMemoryStrictSessionBindingStore()
			cfg := &strictMessagesTestConfig{Config: config.Config{RunMode: config.RunModeStandard}}
			cfg.binding.Enabled = strict
			h, cleanup := newStrictMessagesHandlerWithCache(t, cfg, group, cache, repo, store)
			t.Cleanup(cleanup)
			first, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			require.Equal(t, bound.ID, selected)

			bound.RateMultiplier = &expensiveRate
			cache.accounts = []*service.Account{bound, other}
			second, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
			if strict {
				assert.Equal(t, http.StatusServiceUnavailable, second.Code, second.Body.String())
				assert.Zero(t, selected)
				assert.Contains(t, second.Body.String(), `"code":"strict_session_account_unavailable"`)
				assert.Contains(t, second.Body.String(), `"reason":"profit_control"`)
				assert.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, group.ID))
			} else {
				assert.Equal(t, http.StatusOK, second.Code, second.Body.String())
				assert.Equal(t, other.ID, selected, "ordinary selection must still use another eligible account")
			}

			bound.RateMultiplier = &cheapRate
			recovered, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
			assert.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
			assert.Equal(t, bound.ID, selected)
		})
	}
}

type strictProfitHTTPConcurrencyCache struct {
	fakeConcurrencyCache
	waitFirst bool
	attempts  int
	releases  int
	waiting   int
	onAcquire func()
}

func (c *strictProfitHTTPConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.attempts++
	if c.waitFirst && c.attempts == 1 {
		return false, nil
	}
	c.onAcquire()
	return true, nil
}

func (c *strictProfitHTTPConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases++
	return nil
}

func (c *strictProfitHTTPConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	c.waiting++
	return true, nil
}

func (c *strictProfitHTTPConcurrencyCache) DecrementAccountWaitCount(context.Context, int64) error {
	c.waiting--
	return nil
}

type strictProfitHTTPSessionCache struct {
	service.SessionLimitCache
	registered int
	released   int
}

func (c *strictProfitHTTPSessionCache) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	c.registered++
	return true, nil
}

func (c *strictProfitHTTPSessionCache) UnregisterSession(context.Context, int64, string) error {
	c.released++
	return nil
}

func TestMessagesStrictProfitRecheckReleasesSlots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, wait := range []bool{false, true} {
		name := "immediate slot"
		if wait {
			name = "wait for slot"
		}
		t.Run(name, func(t *testing.T) {
			group := strictHTTPGroup(2202)
			group.RateMultiplier = 0.5
			group.ProfitControlEnabled = true
			cheapRate, expensiveRate := 0.2, 0.8
			bound := strictHTTPAccount(1, group.ID, "bound")
			bound.Platform = service.PlatformAnthropic
			bound.Credentials = nil // Reach the post-slot check instead of intercepting warmup.
			bound.Extra = map[string]any{"max_sessions": float64(2)}
			bound.RateMultiplier = &cheapRate
			latest := *bound
			latest.RateMultiplier = &expensiveRate
			latest.UpdatedAt = bound.UpdatedAt.Add(time.Second)
			cache := &fakeSchedulerCache{accounts: []*service.Account{bound}}
			slots := &strictProfitHTTPConcurrencyCache{waitFirst: wait, onAcquire: func() {
				cache.accounts = []*service.Account{&latest}
			}}
			sessions := &strictProfitHTTPSessionCache{}
			cfg := &strictMessagesTestConfig{
				Config:      config.Config{RunMode: config.RunModeStandard},
				concurrency: service.NewConcurrencyService(slots),
				sessions:    sessions,
			}
			cfg.binding.Enabled = true
			cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
			cfg.Gateway.Scheduling.StickySessionWaitTimeout = time.Second
			store := service.NewMemoryStrictSessionBindingStore()
			plan, err := service.ResolveStrictSessionPlan(cfg.binding, service.StrictSessionIdentityInput{MetadataUserID: strictHTTPMetadata()})
			require.NoError(t, err)
			_, err = store.Create(context.Background(), &service.StrictSessionBinding{BindingKey: plan.BindingKey, AccountID: bound.ID})
			require.NoError(t, err)
			repo := &strictMessagesAccountRepo{byID: map[int64]*service.Account{bound.ID: bound}}
			h, cleanup := newStrictMessagesHandlerWithCache(t, cfg, group, cache, repo, store)
			t.Cleanup(cleanup)
			h.concurrencyHelper = NewConcurrencyHelper(cfg.concurrency, SSEPingFormatClaude, 0)

			rec, selected := postStrictMessages(t, h, group, group.ID, nil, strictHTTPMetadata())
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"reason":"profit_control"`)
			assert.Equal(t, bound.ID, selected)
			assert.Equal(t, bound.ID, requireStrictStoreAccount(t, store, cfg, group.ID))
			assert.Equal(t, 1, slots.releases)
			assert.Zero(t, slots.waiting)
			assert.Equal(t, 1, sessions.registered)
			assert.Zero(t, sessions.released, "a profit veto must not delete a shared session member")
			wantAttempts := 1
			if wait {
				wantAttempts = 2
			}
			assert.Equal(t, wantAttempts, slots.attempts, "a veto must not reselect or retry")
		})
	}
}
