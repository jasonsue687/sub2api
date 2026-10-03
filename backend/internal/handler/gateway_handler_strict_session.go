package handler

import (
	"context"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// strictMessagesWriterSize 记下进入 Messages 时的写出量。
// 严格模式用它判断响应是否已经开始，避免把另一个回退目标拼到已写出的流上。
func (h *GatewayHandler) strictMessagesWriterSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return 0
	}
	return c.Writer.Size()
}

// writeStrictStickySessionHash 在严格模式开启时写脱敏日志。
// 返回 false 时调用方继续写官方的会话 hash 日志。
func (h *GatewayHandler) writeStrictStickySessionHash(c *gin.Context, reqLog *zap.Logger, platform, sessionHash, metadataUserID string) bool {
	if reqLog == nil || !h.strictSessionEnabled(c) || platform == service.PlatformGemini {
		return false
	}
	reqLog.Info("sticky.session_hash_generated",
		zap.Bool("strict_session", true),
		zap.Bool("has_session_hash", sessionHash != ""),
	)
	return true
}

func (h *GatewayHandler) strictLoggedSessionKey(c *gin.Context, platform, sessionKey string) string {
	if h.strictSessionEnabled(c) && platform != service.PlatformGemini {
		return "redacted"
	}
	return sessionKey
}

func strictSelectingSessionKey(rt *strictSessionRuntime, sessionKey string) string {
	if rt != nil && rt.Active {
		return rt.fingerprint()
	}
	return sessionKey
}

// beginStrictClaudeMessages 准备非 Gemini Messages 的严格绑定。
// 出错时已经写好响应，调用方直接 return。
func (h *GatewayHandler) beginStrictClaudeMessages(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	platform, metadataUserID string,
	sessionKey *string,
	hasBoundSession *bool,
	streamStarted bool,
) (*strictSessionRuntime, error) {
	rt, err := h.prepareStrictClaudeMessages(c, apiKey, platform, metadataUserID)
	if err != nil {
		h.writeStrictSessionSetupError(c, reqLog, err, streamStarted)
		return nil, err
	}
	if rt != nil && rt.Plan != nil {
		if _, forced := middleware2.GetForcePlatformFromContext(c); forced {
			rt.Plan.ForcePlatform = true
		}
	}
	if rt != nil && rt.Active && rt.Plan != nil && sessionKey != nil && *sessionKey != rt.Plan.SessionID {
		*sessionKey = rt.Plan.BindingKey
		groupID := int64(0)
		if apiKey != nil {
			groupID = derefGroupID(apiKey.GroupID)
		}
		cleared := service.WithPrefetchedStickySession(c.Request.Context(), 0, groupID, h.metadataBridgeEnabled())
		c.Request = c.Request.WithContext(cleared)
	}
	if rt != nil && rt.Active {
		if hasBoundSession != nil {
			*hasBoundSession = rt.boundAccountID() > 0
		}
		if reqLog != nil {
			reqLog.Info("strict_session.enabled",
				zap.String("session_fp", rt.fingerprint()),
				zap.Int64("bound_account_id", rt.boundAccountID()),
				zap.Bool("bound", rt.boundAccountID() > 0),
			)
		}
	}
	return rt, nil
}

func (h *GatewayHandler) bindStrictFallbackActivator(
	c *gin.Context,
	rt *strictSessionRuntime,
	currentAPIKey **service.APIKey,
	currentSubscription **service.UserSubscription,
	sessionKey *string,
	hasBoundSession *bool,
	sessionBoundAccountID *int64,
	fallbackUsed *bool,
	sessionSlotAccounts map[int64]*service.Account,
) {
	if rt == nil {
		return
	}
	rt.activateFallback = func() error {
		return h.activateStrictFallbackGroup(c, rt, currentAPIKey, currentSubscription, sessionKey, hasBoundSession, sessionBoundAccountID, fallbackUsed, sessionSlotAccounts)
	}
}

func (h *GatewayHandler) applyStrictBindingLimit(fs *FailoverState, rt *strictSessionRuntime) {
	if rt.locksAccount() && rt.Plan != nil {
		fs.EnableStrictBinding(rt.Plan.SameAccountRetryLimit)
	}
}

func (h *GatewayHandler) strictSelectFailureHandled(
	c *gin.Context,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	err error,
	body []byte,
	streamStarted bool,
	writerSizeAtEntry int,
	retry *bool,
) bool {
	action := h.handleStrictSelectFailure(c, rt, reqLog, err, body, streamStarted, writerSizeAtEntry)
	if action == strictFlowRetryGroup {
		if retry != nil {
			*retry = true
		}
		return true
	}
	return action == strictFlowStop
}

func (h *GatewayHandler) strictGiveUpAccount(
	c *gin.Context,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	account *service.Account,
	sessionKey, reason string,
	releaseSession bool,
	includeFallbackGroup bool,
	body []byte,
	streamStarted bool,
	writerSizeAtEntry int,
	retry *bool,
) bool {
	if rt == nil || account == nil {
		return false
	}
	if !rt.locksAccount() && !(includeFallbackGroup && rt.InFallbackGroup) {
		return false
	}
	if releaseSession {
		h.releaseStrictBoundSession(account, sessionKey)
	}
	if h.dispatchStrictFallback(c, rt, reqLog, account.ID, reason, true, true, body, streamStarted, writerSizeAtEntry) == strictFlowRetryGroup && retry != nil {
		*retry = true
	}
	return true
}

func (h *GatewayHandler) strictPromptTooLongStops(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, account *service.Account, promptTooLongErr *service.PromptTooLongError) bool {
	if rt == nil || !rt.Active || promptTooLongErr == nil || account == nil {
		return false
	}
	if reqLog != nil {
		reqLog.Warn("strict_session.skip_group_fallback",
			zap.Int64("account_id", account.ID),
			zap.String("session_fp", rt.fingerprint()),
		)
	}
	_ = h.antigravityGatewayService.WriteMappedClaudeError(c, account, promptTooLongErr.StatusCode, promptTooLongErr.RequestID, promptTooLongErr.Body)
	return true
}

// handleStrictUpstreamFailover 在严格模式开启时接管上游 failover。
// 关闭时返回 strictFlowPassthrough，调用方继续走官方分支。
func (h *GatewayHandler) handleStrictUpstreamFailover(
	c *gin.Context,
	fs *FailoverState,
	rt *strictSessionRuntime,
	reqLog *zap.Logger,
	account *service.Account,
	failoverErr *service.UpstreamFailoverError,
	body []byte,
	sessionKey string,
	streamStarted bool,
	writerSizeAtEntry int,
	writerSizeBeforeForward int,
	sessionSlotAccounts map[int64]*service.Account,
) int {
	if rt == nil || !rt.Active || account == nil {
		return strictFlowPassthrough
	}
	if strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, account.ID, true)
		return strictFlowStop
	}
	if c.Writer.Size() != writerSizeBeforeForward {
		h.handleFailoverExhausted(c, failoverErr, account.Platform, true)
		return strictFlowStop
	}
	if rt.locksAccount() {
		action := h.dispatchStrictFailover(c, fs, rt, reqLog, account, failoverErr, body, account.Platform, streamStarted, writerSizeAtEntry)
		if action == strictFlowPassthrough {
			h.gatewayService.ReleaseAccountSession(context.Background(), account, sessionKey)
			delete(sessionSlotAccounts, account.ID)
			return strictFlowSameAccount
		}
		return action
	}
	if rt.InFallbackGroup && strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, account.ID, true)
		return strictFlowStop
	}
	action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr)
	switch action {
	case FailoverContinue:
		h.gatewayService.ReleaseAccountSession(context.Background(), account, sessionKey)
		delete(sessionSlotAccounts, account.ID)
		return strictFlowSameAccount
	case FailoverExhausted:
		if rt.InFallbackGroup {
			if h.dispatchStrictFallback(c, rt, reqLog, account.ID, "upstream_exhausted", false, failoverErr != nil && failoverErr.ShouldRetryNextAccount(), body, streamStarted, writerSizeAtEntry) == strictFlowRetryGroup {
				return strictFlowRetryGroup
			}
			return strictFlowStop
		}
		h.handleFailoverExhausted(c, fs.LastFailoverErr, account.Platform, streamStarted)
		return strictFlowStop
	case FailoverCanceled:
		failoverClientGone(c)
		return strictFlowStop
	default:
		return strictFlowStop
	}
}
