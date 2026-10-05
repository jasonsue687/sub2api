//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type strictSharedSessionCache struct {
	stubSessionLimitCache
	active map[string]bool
}

func (s *strictSharedSessionCache) RegisterSession(_ context.Context, _ int64, id string, max int, _ time.Duration) (bool, error) {
	if s.active[id] {
		return true, nil
	}
	if len(s.active) >= max {
		return false, nil
	}
	s.active[id] = true
	return true, nil
}
func (s *strictSharedSessionCache) UnregisterSession(_ context.Context, _ int64, id string) error {
	delete(s.active, id)
	return nil
}
func TestStrictRejectedRequestPreservesInflightSession(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	account := healthyStrictAccount(1, group.ID)
	account.Concurrency = 2
	account.Type = AccountTypeOAuth
	account.Extra = map[string]any{"max_sessions": 1}
	cache := &strictSharedSessionCache{active: map[string]bool{}}
	slots := &mockConcurrencyCache{acquireResults: map[int64]bool{account.ID: true}, waitCounts: map[int64]int{account.ID: 100}}
	svc := &GatewayService{cfg: testConfig(), strictSessionStore: presetStore(t, "bound", account.ID), sessionLimitCache: cache, concurrencyService: NewConcurrencyService(slots), accountRepo: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}}}
	plan := &StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: PlatformAnthropic}
	first, err := svc.SelectStrictSessionAccount(context.Background(), plan, &group.ID, "inflight-S", "claude-sonnet-4-5", nil, "", 0)
	require.NoError(t, err)
	require.True(t, first.Acquired)
	defer releaseSelection(first)
	require.True(t, cache.active["inflight-S"])
	// First selection remains acquired and has not completed. A duplicate request
	// of S cannot acquire another slot and its account waiting queue is already full.
	slots.acquireResults[account.ID] = false
	second, err := svc.SelectStrictSessionAccount(context.Background(), plan, &group.ID, "inflight-S", "claude-sonnet-4-5", nil, "", 0)
	require.Error(t, err)
	require.Nil(t, second)
	require.True(t, cache.active["inflight-S"], "rejecting duplicate S must not remove S while its first request is still in flight")
}

func TestStrictBindingWriteFailurePreservesCapacityAndReleasesSlot(t *testing.T) {
	for _, race := range []bool{false, true} {
		name := "create failure"
		if race {
			name = "concurrent loser"
		}
		t.Run(name, func(t *testing.T) {
			group := &Group{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
			candidate := healthyStrictAccount(1, group.ID)
			candidate.Concurrency = 2
			candidate.Type = AccountTypeOAuth
			candidate.Extra = map[string]any{"max_sessions": 1}
			winner := healthyStrictAccount(2, group.ID)
			winner.Schedulable = false
			memory := NewMemoryStrictSessionBindingStore()
			var store StrictSessionBindingStore = memory
			if race {
				store = &strictProfitWinnerStore{MemoryStrictSessionBindingStore: memory, winnerID: winner.ID}
			} else {
				memory.CreateErr = errors.New("write failed")
			}
			// Another request may already share the member before this request fails.
			cache := &strictSharedSessionCache{active: map[string]bool{"shared": true}}
			slots := &admissionConcurrencyCache{}
			svc := &GatewayService{cfg: testConfig(), strictSessionStore: store, sessionLimitCache: cache, concurrencyService: NewConcurrencyService(slots),
				accountRepo: &mockAccountRepoForPlatform{accounts: []Account{*candidate}, accountsByID: map[int64]*Account{candidate.ID: candidate, winner.ID: winner}},
				groupRepo:   &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}}}
			selected, err := svc.SelectStrictSessionAccount(context.Background(), &StrictSessionPlan{Active: true, BindingKey: "new", RequestPlatform: PlatformAnthropic}, &group.ID, "shared", "claude-sonnet-4-5", nil, "", 0)
			require.Error(t, err)
			require.Nil(t, selected)
			require.Equal(t, 1, slots.released)
			require.True(t, cache.active["shared"])
		})
	}
}

type strictCancelAfterAcquireCache struct {
	mockConcurrencyCache
	cancel   context.CancelFunc
	releases int
}

func (c *strictCancelAfterAcquireCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.cancel()
	return true, nil
}
func (c *strictCancelAfterAcquireCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases++
	return nil
}
func TestStrictHydrationFailureReleasesAcquiredSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slots := &strictCancelAfterAcquireCache{cancel: cancel}
	svc := &GatewayService{cfg: testConfig(), concurrencyService: NewConcurrencyService(slots), schedulerSnapshot: &SchedulerSnapshotService{}}
	account := healthyStrictAccount(1, 1)
	account.Concurrency = 1
	selected, err := svc.acquireStrictBoundAccount(ctx, account)
	require.Error(t, err)
	require.Nil(t, selected)
	require.Equal(t, 1, slots.releases, "client cancellation after Redis slot acquisition must release this request slot when hydration fails")
}
