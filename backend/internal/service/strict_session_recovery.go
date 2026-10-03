package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type strictFallbackGroupContextKey struct{}

// WithStrictFallbackGroup 把本次选号限制在兜底分组内。
// simple 模式平时忽略分组；这个标记让兜底转发仍然只看该分组的账号。
func WithStrictFallbackGroup(ctx context.Context, groupID int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, strictFallbackGroupContextKey{}, groupID)
}

// StrictFallbackGroupFromContext 返回本次请求强制使用的兜底分组。
func StrictFallbackGroupFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	id, ok := ctx.Value(strictFallbackGroupContextKey{}).(int64)
	return id, ok && id > 0
}

func strictFallbackSchedulingScope(ctx context.Context, groupID *int64) (*int64, bool) {
	forced, ok := StrictFallbackGroupFromContext(ctx)
	if !ok {
		return groupID, false
	}
	return &forced, true
}

func overrideStrictFallbackGroup(ctx context.Context, groupID *int64) *int64 {
	scoped, _ := strictFallbackSchedulingScope(ctx, groupID)
	return scoped
}

func withStrictFallbackBucketGroup(ctx context.Context, bucket SchedulerBucket) SchedulerBucket {
	if forced, ok := StrictFallbackGroupFromContext(ctx); ok {
		bucket.GroupID = forced
	}
	return bucket
}

func strictFallbackSimpleGroupID(ctx context.Context, groupID int64) int64 {
	if forced, ok := StrictFallbackGroupFromContext(ctx); ok {
		return forced
	}
	return groupID
}

const (
	// StrictRecoveryThirdParty 把请求发到独立中转，不改绑定。
	StrictRecoveryThirdParty = "third_party"
	// StrictRecoveryGroup 把请求交给兜底分组自己的调度，不改绑定。
	StrictRecoveryGroup = "fallback_group"
	// StrictRecoveryReject 两个目标都不可用，直接拒绝。
	StrictRecoveryReject = "reject"
)

// StrictRecoveryPlan 是原账号不能承接、且响应尚未写出时的下一步。
type StrictRecoveryPlan struct {
	Kind    string
	GroupID int64
}

// StrictRecoveryState 记录本请求已经尝试过的回退目标。
type StrictRecoveryState struct {
	TriedThirdParty    bool
	TriedFallbackGroup bool
	OriginGroupID      int64
}

// PlanStrictRecovery 按 fallback_order 选择下一个回退目标。
// 兜底分组与原分组相同时直接跳过，避免回到原分组的订阅账号池。
func PlanStrictRecovery(cfg config.GatewayStrictSessionBindingConfig, state StrictRecoveryState) StrictRecoveryPlan {
	order := []string{StrictRecoveryGroup, StrictRecoveryThirdParty}
	if cfg.FallbackOrder == config.StrictFallbackOrderThirdPartyFirst {
		order = []string{StrictRecoveryThirdParty, StrictRecoveryGroup}
	}
	for _, kind := range order {
		switch kind {
		case StrictRecoveryGroup:
			if state.TriedFallbackGroup || cfg.FallbackGroupID <= 0 || cfg.FallbackGroupID == state.OriginGroupID {
				continue
			}
			return StrictRecoveryPlan{Kind: StrictRecoveryGroup, GroupID: cfg.FallbackGroupID}
		case StrictRecoveryThirdParty:
			if state.TriedThirdParty || !StrictThirdPartyConfigured(cfg.ThirdParty) {
				continue
			}
			return StrictRecoveryPlan{Kind: StrictRecoveryThirdParty}
		}
	}
	return StrictRecoveryPlan{Kind: StrictRecoveryReject}
}

// StrictThirdPartyConfigured 报告独立中转是否具备地址和密钥。
func StrictThirdPartyConfigured(cfg config.GatewayStrictThirdPartyConfig) bool {
	return cfg.Enabled && strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.APIKey) != ""
}
