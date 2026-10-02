package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPlanStrictRecoveryOrderAndOriginSkip(t *testing.T) {
	groupFirst := config.GatewayStrictSessionBindingConfig{
		FallbackOrder:   config.StrictFallbackOrderGroupFirst,
		FallbackGroupID: 20,
		ThirdParty: config.GatewayStrictThirdPartyConfig{
			Enabled: true,
			BaseURL: "https://relay.example",
			APIKey:  "secret",
		},
	}
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryGroup, GroupID: 20}, PlanStrictRecovery(groupFirst, StrictRecoveryState{OriginGroupID: 10}))
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryThirdParty}, PlanStrictRecovery(groupFirst, StrictRecoveryState{OriginGroupID: 10, TriedFallbackGroup: true}))
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryReject}, PlanStrictRecovery(groupFirst, StrictRecoveryState{OriginGroupID: 10, TriedFallbackGroup: true, TriedThirdParty: true}))
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryThirdParty}, PlanStrictRecovery(groupFirst, StrictRecoveryState{OriginGroupID: 20}))

	thirdFirst := groupFirst
	thirdFirst.FallbackOrder = config.StrictFallbackOrderThirdPartyFirst
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryThirdParty}, PlanStrictRecovery(thirdFirst, StrictRecoveryState{OriginGroupID: 10}))
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryGroup, GroupID: 20}, PlanStrictRecovery(thirdFirst, StrictRecoveryState{OriginGroupID: 10, TriedThirdParty: true}))

	neither := config.GatewayStrictSessionBindingConfig{FallbackOrder: config.StrictFallbackOrderGroupFirst}
	require.Equal(t, StrictRecoveryPlan{Kind: StrictRecoveryReject}, PlanStrictRecovery(neither, StrictRecoveryState{OriginGroupID: 10}))
}
