//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type strictChannelAccountRepo struct {
	AccountRepository
	account *Account
}

func (r strictChannelAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestStrictBoundSessionHonorsChannelModelRestrictions(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream, BillingModelSourceResponse} {
		t.Run(source, func(t *testing.T) {
			gid := int64(10)
			account := healthyStrictAccount(1, gid)
			channel := Channel{
				ID: 1, Status: StatusActive, GroupIDs: []int64{gid}, RestrictModels: true,
				BillingModelSource: source,
				ModelPricing:       []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"claude-sonnet-4"}}},
			}
			store := presetStore(t, "bound", account.ID)
			svc := &GatewayService{
				cfg:                &config.Config{RunMode: config.RunModeStandard},
				accountRepo:        strictChannelAccountRepo{account: account},
				channelService:     newTestChannelService(makeStandardRepo(channel, map[int64]string{gid: PlatformAnthropic})),
				strictSessionStore: store,
			}
			ctx := context.Background()
			plan := &StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: PlatformAnthropic}
			selected, err := svc.SelectStrictSessionAccount(ctx, plan, &gid, "bound", "claude-opus-4", nil, "", 0)
			require.Nil(t, selected)
			var unavailable *StrictSessionAccountUnavailableError
			require.ErrorAs(t, err, &unavailable)
			require.Equal(t, "channel_model_restricted", unavailable.Reason)
			require.Equal(t, account.ID, unavailable.AccountID)
			binding, err := store.Get(ctx, "bound")
			require.NoError(t, err)
			require.Equal(t, account.ID, binding.AccountID)
			// A supported model on the same session remains usable after the rejection.
			selected, err = svc.SelectStrictSessionAccount(ctx, plan, &gid, "bound", "claude-sonnet-4", nil, "", 0)
			require.NoError(t, err)
			require.Equal(t, account.ID, selected.Account.ID)
			releaseSelection(selected)
		})
	}
}

func TestStrictBoundSessionChecksMappedBillingModel(t *testing.T) {
	for _, source := range []string{BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			gid := int64(10)
			account := healthyStrictAccount(1, gid)
			channel := Channel{
				ID: 1, Status: StatusActive, GroupIDs: []int64{gid}, RestrictModels: true,
				BillingModelSource: source,
				ModelPricing:       []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{"allowed-alias"}}},
			}
			if source == BillingModelSourceChannelMapped {
				channel.ModelMapping = map[string]map[string]string{PlatformAnthropic: {"allowed-alias": "restricted-target"}}
			} else {
				account.Credentials = map[string]any{"model_mapping": map[string]any{"allowed-alias": "restricted-target"}}
			}
			svc := &GatewayService{
				cfg:                &config.Config{RunMode: config.RunModeStandard},
				accountRepo:        strictChannelAccountRepo{account: account},
				channelService:     newTestChannelService(makeStandardRepo(channel, map[int64]string{gid: PlatformAnthropic})),
				strictSessionStore: presetStore(t, "bound", account.ID),
			}
			selected, err := svc.SelectStrictSessionAccount(context.Background(), &StrictSessionPlan{Active: true, BindingKey: "bound", RequestPlatform: PlatformAnthropic}, &gid, "bound", "allowed-alias", nil, "", 0)
			require.Nil(t, selected)
			var unavailable *StrictSessionAccountUnavailableError
			require.ErrorAs(t, err, &unavailable)
			require.Equal(t, "channel_model_restricted", unavailable.Reason)
		})
	}
}
