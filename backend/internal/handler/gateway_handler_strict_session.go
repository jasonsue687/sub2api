package handler

import (
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
		// Capacity registration, refresh and release must use the same ID for
		// every identity source. Keep the raw ID used by existing metadata sessions;
		// BindingKey is only the identity of the permanent database binding.
		*sessionKey = rt.Plan.SessionID
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

func (h *GatewayHandler) applyStrictBindingLimit(fs *FailoverState, rt *strictSessionRuntime) {
	if rt.locksAccount() && rt.Plan != nil {
		fs.EnableStrictBinding(rt.Plan.SameAccountRetryLimit)
	}
}

func (h *GatewayHandler) strictSelectFailureHandled(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, err error, streamStarted bool, writerSizeAtEntry int) bool {
	return h.handleStrictSelectFailure(c, rt, reqLog, err, streamStarted, writerSizeAtEntry) == strictFlowStop
}

func (h *GatewayHandler) strictGiveUpAccount(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, account *service.Account, reason string, streamStarted bool, writerSizeAtEntry int) bool {
	if !rt.locksAccount() || account == nil {
		return false
	}
	h.rejectStrictAccount(c, rt, reqLog, account.ID, reason, streamStarted, writerSizeAtEntry)
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
	streamStarted bool,
	writerSizeAtEntry int,
) int {
	if rt == nil || !rt.Active || account == nil {
		return strictFlowPassthrough
	}
	if strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, account.ID, true)
		return strictFlowStop
	}
	action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr)
	switch action {
	case FailoverContinue:
		return strictFlowSameAccount
	case FailoverExhausted:
		// Deterministic request errors keep their original status/body. Capacity,
		// authorization and retryable upstream failures use the routing contract.
		if failoverErr != nil && !failoverErr.ShouldRetryNextAccount() {
			h.handleFailoverExhausted(c, failoverErr, account.Platform, streamStarted)
			return strictFlowStop
		}
		return h.rejectStrictAccount(c, rt, reqLog, account.ID, "upstream_exhausted", streamStarted, writerSizeAtEntry)
	case FailoverCanceled:
		failoverClientGone(c)
		return strictFlowStop
	default:
		return strictFlowStop
	}
}
