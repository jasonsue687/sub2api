package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	strictFlowPassthrough = iota
	strictFlowStop
	strictFlowRetryGroup
	strictFlowSameAccount
)

const (
	strictSessionErrorType              = "session_binding_error"
	strictSessionErrorIDRequired        = "strict_session_id_required"
	strictSessionErrorStoreUnavailable  = "strict_session_store_unavailable"
	strictSessionErrorFallbackRequired  = "strict_session_fallback_required"
	strictSessionErrorThirdPartyFailed  = "strict_session_third_party_failed"
	strictSessionErrorStreamInterrupted = "strict_session_stream_interrupted"
	strictSessionErrorHeader            = "X-Sub2API-Error-Code"
)

// strictSessionRuntime 是 Claude Messages 请求上的严格绑定状态。
// Active 为 false 时，后续选号和故障转移保持官方行为。
// InFallbackGroup 为 true 时，选号改走兜底分组自己的调度，但主绑定不变。
type strictSessionRuntime struct {
	Active             bool
	InFallbackGroup    bool
	TriedThirdParty    bool
	TriedFallbackGroup bool
	Plan               *service.StrictSessionPlan
	Config             config.GatewayStrictSessionBindingConfig
	OriginGroupID      int64
	activateFallback   func() error
}

func (rt *strictSessionRuntime) locksAccount() bool {
	return rt != nil && rt.Active && !rt.InFallbackGroup
}

func (h *GatewayHandler) strictSessionEnabled(c *gin.Context) bool {
	return h.effectiveStrictBinding(c).Enabled
}

func (h *GatewayHandler) effectiveStrictBinding(c *gin.Context) config.GatewayStrictSessionBindingConfig {
	if h == nil {
		return config.GatewayStrictSessionBindingConfig{}
	}
	if h.gatewayService != nil {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		return h.gatewayService.EffectiveStrictSessionBinding(ctx)
	}
	if h.cfg != nil {
		return h.cfg.Gateway.StrictSessionBinding
	}
	return config.GatewayStrictSessionBindingConfig{}
}

// prepareStrictClaudeMessages 只在非 Gemini 的 /v1/messages 路径调用。
// 缺少稳定会话 ID 或存储读失败时直接拒绝，不用消息摘要顶替。
func (h *GatewayHandler) prepareStrictClaudeMessages(c *gin.Context, apiKey *service.APIKey, requestPlatform, metadataUserID string) (*strictSessionRuntime, error) {
	bindingCfg := h.effectiveStrictBinding(c)
	originGroupID := int64(0)
	if apiKey != nil && apiKey.GroupID != nil {
		originGroupID = *apiKey.GroupID
	}
	if !bindingCfg.Enabled || apiKey == nil {
		return &strictSessionRuntime{Config: bindingCfg, OriginGroupID: originGroupID}, nil
	}
	if requestPlatform == service.PlatformGemini {
		return &strictSessionRuntime{Config: bindingCfg, OriginGroupID: originGroupID}, nil
	}
	input := service.StrictSessionIdentityInput{
		APIKeyID:            apiKey.ID,
		GroupID:             apiKey.GroupID,
		RequestPlatform:     requestPlatform,
		MetadataUserID:      metadataUserID,
		ClaudeCodeSessionID: service.ClaudeCodeSessionIDFromHeader(c),
		SessionHeaderValue:  c.GetHeader(bindingCfg.SessionHeaderOrDefault()),
	}
	plan, err := h.gatewayService.PrepareStrictSession(c.Request.Context(), input)
	if err != nil {
		return nil, err
	}
	if plan == nil || !plan.Active {
		return &strictSessionRuntime{Config: bindingCfg, OriginGroupID: originGroupID}, nil
	}
	return &strictSessionRuntime{Active: true, Plan: plan, Config: bindingCfg, OriginGroupID: originGroupID}, nil
}

func (h *GatewayHandler) selectMessageAccount(
	c *gin.Context,
	rt *strictSessionRuntime,
	groupID *int64,
	sessionKey, requestedModel string,
	excluded map[int64]struct{},
	metadataUserID string,
	userID int64,
) (*service.AccountSelectionResult, error) {
	if rt != nil && rt.locksAccount() {
		return h.gatewayService.SelectStrictSessionAccount(
			c.Request.Context(),
			rt.Plan,
			groupID,
			sessionKey,
			requestedModel,
			excluded,
			metadataUserID,
			userID,
		)
	}
	return h.gatewayService.SelectAccountWithLoadAwareness(
		c.Request.Context(),
		groupID,
		sessionKey,
		requestedModel,
		excluded,
		metadataUserID,
		userID,
	)
}

func (h *GatewayHandler) writeStrictSessionSetupError(c *gin.Context, reqLog *zap.Logger, err error, streamStarted bool) {
	switch {
	case errors.Is(err, service.ErrStrictSessionIDRequired):
		h.respondStrictSessionError(c, http.StatusBadRequest, strictSessionErrorIDRequired, "stable_session_id_required", streamStarted)
	default:
		if reqLog != nil {
			reqLog.Warn("strict_session.store_failed", zap.Error(err))
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStoreUnavailable, "store_unavailable", streamStarted)
	}
}

// handleStrictSelectFailure 处理严格选号的存储故障和原账号不可承接。
// strictFlowPassthrough 表示这不是严格绑定错误，调用方继续走官方选号失败路径。
func (h *GatewayHandler) handleStrictSelectFailure(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, err error, body []byte, streamStarted bool, writerSizeAtEntry int) int {
	if rt == nil || !rt.Active || err == nil {
		return strictFlowPassthrough
	}
	var fallback *service.StrictSessionFallbackError
	if errors.As(err, &fallback) && rt.locksAccount() {
		return h.dispatchStrictFallback(c, rt, reqLog, fallback.AccountID, fallback.Reason, true, true, body, streamStarted, writerSizeAtEntry)
	}
	if errors.Is(err, service.ErrStrictSessionStore) && rt.locksAccount() {
		if reqLog != nil {
			reqLog.Warn("strict_session.store_failed", zap.String("session_fp", rt.fingerprint()), zap.Error(err))
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStoreUnavailable, "store_unavailable", streamStarted)
		return strictFlowStop
	}
	if rt.InFallbackGroup {
		return h.dispatchStrictFallback(c, rt, reqLog, 0, "fallback_group_unavailable", true, true, body, streamStarted, writerSizeAtEntry)
	}
	return strictFlowPassthrough
}

func (h *GatewayHandler) dispatchStrictFailover(
	c *gin.Context,
	fs *FailoverState,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	account *service.Account,
	failoverErr *service.UpstreamFailoverError,
	body []byte,
	platform string,
	streamStarted bool,
	writerSizeAtEntry int,
) int {
	if strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, accountIDOf(account), true)
		return strictFlowStop
	}
	if failoverClientGone(c) {
		return strictFlowStop
	}
	action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, platform, account.GetPoolModeRetryCount(), failoverErr)
	switch action {
	case FailoverContinue:
		return strictFlowPassthrough
	case FailoverCanceled:
		failoverClientGone(c)
		return strictFlowStop
	default:
		decision := service.DecideStrictFallback(service.StrictFallbackInput{
			ResponseWritten:   strictResponseStarted(c, streamStarted, writerSizeAtEntry),
			ClientCanceled:    c.Request != nil && c.Request.Context().Err() != nil,
			AccountSide:       false,
			RetryableUpstream: failoverErr != nil && failoverErr.ShouldRetryNextAccount(),
			ThirdPartyEnabled: strictRecoveryAvailable(rt),
		})
		if decision == service.StrictFallbackPassthrough {
			h.handleFailoverExhausted(c, fs.LastFailoverErr, platform, streamStarted)
			return strictFlowStop
		}
		return h.dispatchStrictFallback(c, rt, reqLog, account.ID, "upstream_exhausted", false, failoverErr != nil && failoverErr.ShouldRetryNextAccount(), body, streamStarted, writerSizeAtEntry)
	}
}

func strictRecoveryAvailable(rt *strictSessionRuntime) bool {
	if rt == nil {
		return false
	}
	plan := service.PlanStrictRecovery(rt.Config, service.StrictRecoveryState{
		TriedThirdParty:    rt.TriedThirdParty,
		TriedFallbackGroup: rt.TriedFallbackGroup,
		OriginGroupID:      rt.OriginGroupID,
	})
	return plan.Kind != service.StrictRecoveryReject
}

func (h *GatewayHandler) dispatchStrictFallback(
	c *gin.Context,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	accountID int64,
	reason string,
	accountSide bool,
	retryableUpstream bool,
	body []byte,
	streamStarted bool,
	writerSizeAtEntry int,
) int {
	responseStarted := strictResponseStarted(c, streamStarted, writerSizeAtEntry)
	if !responseStarted && c.Request != nil && c.Request.Context().Err() != nil {
		failoverClientGone(c)
		return strictFlowStop
	}
	cfg := h.recoveryConfig(c, rt)
	decision := service.DecideStrictFallback(service.StrictFallbackInput{
		ResponseWritten:   responseStarted,
		ClientCanceled:    c.Request != nil && c.Request.Context().Err() != nil,
		AccountSide:       accountSide,
		RetryableUpstream: retryableUpstream,
		ThirdPartyEnabled: service.StrictThirdPartyConfigured(cfg.ThirdParty) || cfg.FallbackGroupID > 0,
	})
	fingerprint := ""
	if rt != nil {
		fingerprint = rt.fingerprint()
	}
	switch decision {
	case service.StrictFallbackStreamInterrupted:
		service.LogStrictSession(accountID, fingerprint, reason, "stream_interrupted")
		h.respondStrictStreamInterrupted(c, rt, reqLog, accountID, true)
		return strictFlowStop
	case service.StrictFallbackPassthrough:
		if c.Request != nil && c.Request.Context().Err() != nil {
			failoverClientGone(c)
			return strictFlowStop
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorFallbackRequired, reason, streamStarted)
		return strictFlowStop
	default:
		return h.runStrictRecovery(c, rt, reqLog, accountID, reason, fingerprint, body, streamStarted, writerSizeAtEntry, responseStarted)
	}
}

func (h *GatewayHandler) recoveryConfig(c *gin.Context, rt *strictSessionRuntime) config.GatewayStrictSessionBindingConfig {
	if rt != nil && (rt.Active || rt.Config.Enabled || rt.Config.FallbackGroupID > 0 || rt.Config.ThirdParty.Enabled) {
		return rt.Config
	}
	return h.effectiveStrictBinding(c)
}

func (h *GatewayHandler) runStrictRecovery(
	c *gin.Context,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	accountID int64,
	reason, fingerprint string,
	body []byte,
	streamStarted bool,
	writerSizeAtEntry int,
	responseStarted bool,
) int {
	cfg := h.recoveryConfig(c, rt)
	state := service.StrictRecoveryState{OriginGroupID: cfgOrigin(rt)}
	if rt != nil {
		state.TriedThirdParty = rt.TriedThirdParty
		state.TriedFallbackGroup = rt.TriedFallbackGroup
		state.OriginGroupID = rt.OriginGroupID
	}
	plan := service.PlanStrictRecovery(cfg, state)
	switch plan.Kind {
	case service.StrictRecoveryGroup:
		if rt != nil {
			rt.TriedFallbackGroup = true
		}
		if rt == nil || rt.activateFallback == nil {
			return h.runStrictRecovery(c, rt, reqLog, accountID, reason, fingerprint, body, streamStarted, writerSizeAtEntry, responseStarted)
		}
		if err := rt.activateFallback(); err != nil {
			if reqLog != nil {
				reqLog.Warn("strict_session.fallback_group_failed",
					zap.Int64("account_id", accountID),
					zap.String("session_fp", fingerprint),
					zap.Int64("fallback_group_id", plan.GroupID),
					zap.Error(err),
				)
			}
			return h.runStrictRecovery(c, rt, reqLog, accountID, reason, fingerprint, body, streamStarted, writerSizeAtEntry, responseStarted)
		}
		service.LogStrictSession(accountID, fingerprint, reason, "fallback_group")
		if reqLog != nil {
			reqLog.Info("strict_session.fallback_group",
				zap.Int64("account_id", accountID),
				zap.String("session_fp", fingerprint),
				zap.String("reason", reason),
				zap.Int64("fallback_group_id", plan.GroupID),
				zap.String("path", "fallback_group"),
			)
		}
		return strictFlowRetryGroup
	case service.StrictRecoveryThirdParty:
		if rt != nil {
			rt.TriedThirdParty = true
		}
		service.LogStrictSession(accountID, fingerprint, reason, "third_party")
		err := h.forwardStrictThirdParty(c, cfg, body)
		if err == nil {
			if reqLog != nil {
				reqLog.Info("strict_session.third_party_completed",
					zap.Int64("account_id", accountID),
					zap.String("session_fp", fingerprint),
					zap.String("reason", reason),
					zap.String("path", "third_party"),
				)
			}
			return strictFlowStop
		}
		if strictResponseStarted(c, streamStarted, writerSizeAtEntry) && c.Writer.Size() != writerSizeAtEntry {
			if reqLog != nil {
				reqLog.Warn("strict_session.third_party_partial",
					zap.Int64("account_id", accountID),
					zap.String("session_fp", fingerprint),
					zap.Error(err),
				)
			}
			return strictFlowStop
		}
		if reqLog != nil {
			reqLog.Warn("strict_session.third_party_failed",
				zap.Int64("account_id", accountID),
				zap.String("session_fp", fingerprint),
				zap.String("reason", reason),
				zap.Error(err),
			)
		}
		next := service.PlanStrictRecovery(cfg, service.StrictRecoveryState{
			TriedThirdParty:    true,
			TriedFallbackGroup: rt != nil && rt.TriedFallbackGroup,
			OriginGroupID:      state.OriginGroupID,
		})
		if next.Kind != service.StrictRecoveryReject {
			return h.runStrictRecovery(c, rt, reqLog, accountID, reason, fingerprint, body, streamStarted, writerSizeAtEntry, responseStarted)
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorThirdPartyFailed, "third_party_failed", streamStarted || responseStarted)
		return strictFlowStop
	default:
		service.LogStrictSession(accountID, fingerprint, reason, "error")
		if reqLog != nil {
			reqLog.Info("strict_session.fallback_required",
				zap.Int64("account_id", accountID),
				zap.String("session_fp", fingerprint),
				zap.String("reason", reason),
				zap.String("path", "error"),
			)
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorFallbackRequired, reason, streamStarted || responseStarted)
		return strictFlowStop
	}
}

func cfgOrigin(rt *strictSessionRuntime) int64 {
	if rt == nil {
		return 0
	}
	return rt.OriginGroupID
}

func (h *GatewayHandler) forwardStrictThirdParty(c *gin.Context, cfg config.GatewayStrictSessionBindingConfig, body []byte) error {
	if h == nil || h.gatewayService == nil {
		return errors.New("strict third party is not configured")
	}
	if service.StrictThirdPartyConfigured(cfg.ThirdParty) {
		return h.gatewayService.ForwardStrictThirdPartyConfig(c.Request.Context(), cfg.ThirdParty, c.Request.Header, body, c.Writer)
	}
	return h.gatewayService.ForwardStrictThirdParty(c.Request.Context(), c.Request.Header, body, c.Writer)
}

func strictResponseStarted(c *gin.Context, streamStarted bool, writerSizeAtEntry int) bool {
	if streamStarted {
		return true
	}
	if c == nil || c.Writer == nil {
		return false
	}
	if c.Writer.Written() {
		return true
	}
	return c.Writer.Size() != writerSizeAtEntry
}

func (h *GatewayHandler) respondStrictStreamInterrupted(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, accountID int64, streamStarted bool) {
	fingerprint := ""
	if rt != nil {
		fingerprint = rt.fingerprint()
	}
	if reqLog != nil {
		reqLog.Warn("strict_session.stream_interrupted",
			zap.Int64("account_id", accountID),
			zap.String("session_fp", fingerprint),
			zap.String("path", "stream_interrupted"),
		)
	}
	h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStreamInterrupted, "stream_interrupted", true)
}

func (h *GatewayHandler) respondStrictSessionError(c *gin.Context, status int, code, reason string, streamStarted bool) {
	if c.Writer != nil && !c.Writer.Written() {
		c.Header(strictSessionErrorHeader, code)
	}
	message := "Strict session binding blocked subscription-account reassignment (" + reason + ")"
	h.handleStreamingAwareErrorWithCode(c, status, strictSessionErrorType, code, message, streamStarted)
}

func (h *GatewayHandler) releaseStrictBoundSession(account *service.Account, sessionKey string) {
	if h == nil || h.gatewayService == nil || account == nil {
		return
	}
	h.gatewayService.ReleaseAccountSession(context.Background(), account, sessionKey)
}

func (rt *strictSessionRuntime) fingerprint() string {
	if rt == nil || rt.Plan == nil {
		return ""
	}
	return rt.Plan.SessionFingerprint
}

func (rt *strictSessionRuntime) boundAccountID() int64 {
	if rt == nil || rt.Plan == nil {
		return 0
	}
	return rt.Plan.BoundAccountID
}

func accountIDOf(account *service.Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

// activateStrictFallbackGroup 把本次请求交给兜底分组，不读写主绑定。
func (h *GatewayHandler) activateStrictFallbackGroup(
	c *gin.Context,
	rt *strictSessionRuntime,
	currentAPIKey **service.APIKey,
	currentSubscription **service.UserSubscription,
	sessionKey *string,
	hasBoundSession *bool,
	sessionBoundAccountID *int64,
	fallbackUsed *bool,
	sessionSlotAccounts map[int64]*service.Account,
) error {
	if rt == nil || rt.Plan == nil || currentAPIKey == nil || *currentAPIKey == nil {
		return errors.New("strict fallback group is not available")
	}
	groupID := rt.Config.FallbackGroupID
	if groupID <= 0 || groupID == rt.OriginGroupID {
		return errors.New("strict fallback group matches the origin group")
	}
	group, err := h.gatewayService.ResolveGroupByID(c.Request.Context(), groupID)
	if err != nil || group == nil || !group.IsActive() {
		return errors.New("strict fallback group is not active")
	}
	switch group.Platform {
	case service.PlatformAnthropic, service.PlatformAntigravity:
	default:
		return errors.New("strict fallback group cannot serve claude messages")
	}
	fallbackKey := cloneAPIKeyWithGroup(*currentAPIKey, group)
	if h.billingCacheService != nil {
		if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), fallbackKey.User, fallbackKey, group, nil, service.PlatformFromAPIKey(fallbackKey)); err != nil {
			return err
		}
	}
	releaseKey := ""
	if sessionKey != nil {
		releaseKey = *sessionKey
	}
	for id, acc := range sessionSlotAccounts {
		h.gatewayService.ReleaseAccountSession(context.Background(), acc, releaseKey)
		delete(sessionSlotAccounts, id)
	}
	ctx := c.Request.Context()
	ctx = context.WithValue(ctx, ctxkey.ForcePlatform, "")
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = service.WithStrictFallbackGroup(ctx, group.ID)
	stickyID := int64(0)
	if h.gatewayService != nil {
		stickyID, _ = h.gatewayService.GetCachedSessionAccountID(ctx, &group.ID, rt.Plan.BindingKey)
	}
	ctx = service.WithPrefetchedStickySession(ctx, stickyID, group.ID, h.metadataBridgeEnabled())
	c.Request = c.Request.WithContext(ctx)
	*currentAPIKey = fallbackKey
	if currentSubscription != nil {
		*currentSubscription = nil
	}
	if sessionKey != nil {
		*sessionKey = rt.Plan.BindingKey
	}
	if sessionBoundAccountID != nil {
		*sessionBoundAccountID = stickyID
	}
	if hasBoundSession != nil {
		*hasBoundSession = stickyID > 0
	}
	if fallbackUsed != nil {
		*fallbackUsed = true
	}
	rt.InFallbackGroup = true
	return nil
}
