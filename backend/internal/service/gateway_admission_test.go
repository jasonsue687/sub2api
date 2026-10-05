//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayPrivacyAdmissionAcrossSelectionPaths(t *testing.T) {
	for _, loadAware := range []bool{false, true} {
		for _, platform := range []string{PlatformAnthropic, PlatformAntigravity} {
			for _, sticky := range []bool{false, true} {
				for _, routed := range []bool{false, true} {
					if routed && platform == PlatformAntigravity {
						continue // Model routing only applies to Anthropic/OpenAI targets.
					}
					t.Run(fmt.Sprintf("load=%t/platform=%s/sticky=%t/routed=%t", loadAware, platform, sticky, routed), func(t *testing.T) {
						group := &Group{ID: 1, Platform: platform, Status: StatusActive, Hydrated: true, RequirePrivacySet: true}
						blocked := healthyStrictAccount(1, group.ID)
						blocked.Platform, blocked.Type, blocked.Concurrency = PlatformAntigravity, AccountTypeOAuth, 1
						blocked.Extra = map[string]any{"mixed_scheduling": true}
						allowed := *blocked
						allowed.ID, allowed.Priority = 2, 1
						allowed.Extra = map[string]any{"mixed_scheduling": true, "privacy_mode": AntigravityPrivacySet}
						if routed {
							group.ModelRoutingEnabled = true
							group.ModelRouting = map[string][]int64{"claude-sonnet-4-5": {blocked.ID, allowed.ID}}
						}
						cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}
						if sticky {
							cache.sessionBindings["session"] = blocked.ID
						}
						slots := &mockConcurrencyCache{}
						svc := &GatewayService{
							cfg: testConfig(), cache: cache,
							accountRepo:        &mockAccountRepoForPlatform{accounts: []Account{*blocked, allowed}, accountsByID: map[int64]*Account{blocked.ID: blocked, allowed.ID: &allowed}},
							groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}},
							concurrencyService: NewConcurrencyService(slots),
							strictSessionStore: presetStore(t, "bound", blocked.ID),
						}
						svc.cfg.Gateway.Scheduling.LoadBatchEnabled = loadAware
						ctx := context.WithValue(context.Background(), ctxkey.Group, group)
						// Exercise forced single-platform admission as well as mixed scheduling.
						if platform == PlatformAntigravity {
							ctx = context.WithValue(ctx, ctxkey.ForcePlatform, platform)
						}
						selected, err := svc.SelectAccountWithLoadAwareness(ctx, &group.ID, "session", "claude-sonnet-4-5", nil, "", 0)
						require.NoError(t, err)
						assert.Equal(t, allowed.ID, selected.Account.ID, "ordinary selection must skip the ineligible account")
						releaseSelection(selected)
						acquired := slots.acquireAccountCalls
						plan := &StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: platform, ForcePlatform: platform == PlatformAntigravity}
						selected, err = svc.SelectStrictSessionAccount(ctx, plan, &group.ID, "session", "claude-sonnet-4-5", nil, "", 0)
						require.Nil(t, selected)
						var unavailable *StrictSessionAccountUnavailableError
						require.ErrorAs(t, err, &unavailable)
						assert.Equal(t, "privacy_not_set", unavailable.Reason)
						assert.Equal(t, acquired, slots.acquireAccountCalls, "reject before acquiring a slot")
						binding, err := svc.strictSessionStore.Get(ctx, "bound")
						require.NoError(t, err)
						assert.Equal(t, blocked.ID, binding.AccountID, "strict selection must not switch to the valid alternative")
						blocked.Extra["privacy_mode"] = AntigravityPrivacySet
						selected, err = svc.SelectStrictSessionAccount(ctx, plan, &group.ID, "session", "claude-sonnet-4-5", nil, "", 0)
						require.NoError(t, err)
						assert.Equal(t, blocked.ID, selected.Account.ID)
						releaseSelection(selected)
					})
				}
			}
		}
	}
}

func TestStrictClientAdmissionBeforeAssignmentAndBoundReuse(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for _, forced := range []bool{false, true} {
			t.Run(fmt.Sprintf("bound=%t/forced=%t", bound, forced), func(t *testing.T) {
				fallbackID := int64(2)
				group := &Group{ID: 1, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true, ClaudeCodeOnly: true, FallbackGroupID: &fallbackID}
				account := healthyStrictAccount(1, group.ID)
				account.Platform, account.Type = PlatformAntigravity, AccountTypeOAuth
				store := NewMemoryStrictSessionBindingStore()
				if bound {
					store = presetStore(t, "session", account.ID)
				}
				slots := &mockConcurrencyCache{}
				repo := &mockAccountRepoForPlatform{accounts: []Account{*account}, accountsByID: map[int64]*Account{account.ID: account}}
				svc := &GatewayService{cfg: testConfig(), accountRepo: repo, strictSessionStore: store,
					groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}}, concurrencyService: NewConcurrencyService(slots)}
				ctx := context.Background()
				if forced {
					ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAntigravity)
				}
				plan := &StrictSessionPlan{Active: true, BindingKey: "session", RequestPlatform: PlatformAntigravity, ForcePlatform: forced}
				selected, err := svc.SelectStrictSessionAccount(ctx, plan, &group.ID, "session", "claude-sonnet-4-5", nil, "", 0)
				if forced {
					require.NoError(t, err, "preserve the existing forced-platform exemption")
					assert.Equal(t, account.ID, selected.Account.ID)
					releaseSelection(selected)
					return
				}
				require.Nil(t, selected)
				var unavailable *StrictSessionAccountUnavailableError
				require.ErrorAs(t, err, &unavailable)
				assert.Equal(t, "claude_code_only", unavailable.Reason)
				assert.Zero(t, slots.acquireAccountCalls)
				assert.Zero(t, repo.getByIDCalls)
				binding, err := store.Get(ctx, "session")
				if bound {
					require.NoError(t, err)
					assert.Equal(t, account.ID, binding.AccountID)
				} else {
					assert.ErrorIs(t, err, ErrStrictBindingNotFound)
				}
			})
		}
	}
}

type admissionConcurrencyCache struct {
	mockConcurrencyCache
	released int
}

func (c *admissionConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.released++
	return nil
}

func TestStrictConcurrentWinnerUsesAccountAdmission(t *testing.T) {
	for _, reason := range []string{"privacy_not_set", "rate_limited", "credits_available"} {
		t.Run(reason, func(t *testing.T) {
			group := &Group{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, RequirePrivacySet: true}
			candidate := healthyStrictAccount(1, group.ID)
			candidate.Concurrency = 1
			winner := healthyStrictAccount(2, group.ID)
			winner.Concurrency = 1
			winner.Platform, winner.Type = PlatformAntigravity, AccountTypeOAuth
			winner.Extra = map[string]any{"mixed_scheduling": true}
			if reason != "privacy_not_set" {
				winner.Extra["privacy_mode"] = AntigravityPrivacySet
				winner.Extra["allow_overages"] = reason == "credits_available"
				winner.Extra["model_rate_limits"] = map[string]any{"claude-sonnet-4-5": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
			}
			store := &strictProfitWinnerStore{MemoryStrictSessionBindingStore: NewMemoryStrictSessionBindingStore(), winnerID: winner.ID}
			slots := &admissionConcurrencyCache{}
			svc := &GatewayService{cfg: testConfig(), strictSessionStore: store, concurrencyService: NewConcurrencyService(slots),
				accountRepo: &mockAccountRepoForPlatform{accounts: []Account{*candidate}, accountsByID: map[int64]*Account{candidate.ID: candidate, winner.ID: winner}},
				groupRepo:   &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}}}
			selected, err := svc.SelectStrictSessionAccount(context.Background(), &StrictSessionPlan{Active: true, BindingKey: "race", RequestPlatform: PlatformAnthropic}, &group.ID, "race", "claude-sonnet-4-5", nil, "", 0)
			assert.Equal(t, 1, slots.released, "release the losing candidate's slot before checking the winner")
			if reason == "credits_available" {
				require.NoError(t, err)
				assert.Equal(t, winner.ID, selected.Account.ID)
				releaseSelection(selected)
			} else {
				require.Nil(t, selected)
				var unavailable *StrictSessionAccountUnavailableError
				require.ErrorAs(t, err, &unavailable)
				assert.Equal(t, reason, unavailable.Reason)
				assert.Equal(t, 1, slots.acquireAccountCalls, "do not acquire a slot for a rejected winner")
			}
			binding, err := store.Get(context.Background(), "race")
			require.NoError(t, err)
			assert.Equal(t, winner.ID, binding.AccountID)
		})
	}
}
