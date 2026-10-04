//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type strictProfitSessionCache struct {
	stubSessionLimitCache
	registered int
}

func (c *strictProfitSessionCache) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	c.registered++
	return true, nil
}

func TestStrictBoundSessionProfitVetoPreservesBindingUntilRecovery(t *testing.T) {
	for _, invalidRate := range []bool{false, true} {
		name := "over threshold"
		if invalidRate {
			name = "missing rate"
		}
		t.Run(name, func(t *testing.T) {
			group := gatewayProfitTestGroup(301, PlatformAnthropic)
			account := gatewayProfitTestAccount(1, PlatformAnthropic, 0.8, group.ID)
			account.Type = AccountTypeOAuth
			account.Extra = map[string]any{"max_sessions": float64(2)}
			if invalidRate {
				account.RateMultiplier = nil
			}
			store := presetStore(t, "bound", account.ID)
			sessions := &strictProfitSessionCache{}
			svc := &GatewayService{
				// Only GetByID is implemented: any attempt to select alternatives fails.
				accountRepo:        strictChannelAccountRepo{account: &account},
				strictSessionStore: store,
				sessionLimitCache:  sessions,
			}
			plan := &StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: PlatformAnthropic}
			ctx := gatewayProfitTestContext(group)
			selected, err := svc.SelectStrictSessionAccount(ctx, plan, &group.ID, "bound", "", nil, "", 0)
			require.Nil(t, selected)
			var unavailable *StrictSessionAccountUnavailableError
			require.ErrorAs(t, err, &unavailable)
			assert.Equal(t, "profit_control", unavailable.Reason)
			assert.Equal(t, account.ID, unavailable.AccountID)
			assert.Zero(t, sessions.registered, "reject before occupying a session slot")
			binding, err := store.Get(ctx, "bound")
			require.NoError(t, err)
			assert.Equal(t, account.ID, binding.AccountID)

			rate := 0.2
			account.RateMultiplier = &rate
			selected, err = svc.SelectStrictSessionAccount(ctx, plan, &group.ID, "bound", "", nil, "", 0)
			require.NoError(t, err)
			require.NotNil(t, selected)
			t.Cleanup(func() { releaseSelection(selected) })
			assert.Equal(t, account.ID, selected.Account.ID)
			assert.True(t, selected.ProfitGateActive())
			assert.Equal(t, 1, sessions.registered)
		})
	}
}

func TestStrictBoundSessionWithoutProfitControlKeepsAccount(t *testing.T) {
	group := gatewayProfitTestGroup(302, PlatformAnthropic)
	group.ProfitControlEnabled = false
	account := gatewayProfitTestAccount(1, PlatformAnthropic, 0.8, group.ID)
	svc := &GatewayService{
		accountRepo:        strictChannelAccountRepo{account: &account},
		strictSessionStore: presetStore(t, "bound", account.ID),
	}
	selected, err := svc.SelectStrictSessionAccount(gatewayProfitTestContext(group),
		&StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: PlatformAnthropic},
		&group.ID, "bound", "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, selected)
	defer releaseSelection(selected)
	assert.Equal(t, account.ID, selected.Account.ID)
	assert.False(t, selected.ProfitGateActive())
}

// Simulate another request committing its binding after our initial cache miss.
type strictProfitWinnerStore struct {
	*MemoryStrictSessionBindingStore
	winnerID int64
}

func (s *strictProfitWinnerStore) Create(ctx context.Context, binding *StrictSessionBinding) (*StrictSessionBinding, error) {
	winner := *binding
	winner.AccountID = s.winnerID
	return s.MemoryStrictSessionBindingStore.Create(ctx, &winner)
}

func TestStrictSessionConcurrentWinnerHonorsProfitControl(t *testing.T) {
	group := gatewayProfitTestGroup(303, PlatformAnthropic)
	cheap := gatewayProfitTestAccount(1, PlatformAnthropic, 0.2, group.ID)
	winner := gatewayProfitTestAccount(2, PlatformAnthropic, 0.8, group.ID)
	store := &strictProfitWinnerStore{MemoryStrictSessionBindingStore: NewMemoryStrictSessionBindingStore(), winnerID: winner.ID}
	svc := &GatewayService{
		accountRepo: &mockAccountRepoForPlatform{
			accounts:     []Account{cheap},
			accountsByID: map[int64]*Account{cheap.ID: &cheap, winner.ID: &winner},
		},
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                testConfig(),
		strictSessionStore: store,
	}
	ctx := gatewayProfitTestContext(group)
	selected, err := svc.SelectStrictSessionAccount(ctx,
		&StrictSessionPlan{Active: true, BindingKey: "race", RequestPlatform: PlatformAnthropic},
		&group.ID, "race", "", nil, "", 0)
	require.Nil(t, selected)
	var unavailable *StrictSessionAccountUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, "profit_control", unavailable.Reason)
	assert.Equal(t, winner.ID, unavailable.AccountID)
	binding, err := store.Get(ctx, "race")
	require.NoError(t, err)
	assert.Equal(t, winner.ID, binding.AccountID)
}
