package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	strictSessionErrorType              = "session_binding_error"
	strictSessionErrorIDRequired        = "strict_session_id_required"
	strictSessionErrorEndUserRequired   = "strict_session_end_user_required"
	strictSessionErrorStoreUnavailable  = "strict_session_store_unavailable"
	strictSessionErrorFallbackRequired  = "strict_session_fallback_required"
	strictSessionErrorThirdPartyFailed  = "strict_session_third_party_failed"
	strictSessionErrorStreamInterrupted = "strict_session_stream_interrupted"
	strictSessionErrorHeader            = "X-Sub2API-Error-Code"
)

// strictSessionRuntime 是 Claude Messages 请求上的严格绑定状态。
// Active 为 false 时，后续选号和故障转移保持官方行为。
type strictSessionRuntime struct {
	Active bool
	Plan   *service.StrictSessionPlan
}

func (h *GatewayHandler) strictSessionEnabled() bool {
	return h != nil && h.cfg != nil && h.cfg.Gateway.StrictSessionBinding.Enabled
}

// prepareStrictClaudeMessages 只在非 Gemini 的 /v1/messages 路径调用。
// 缺少稳定会话 ID 或存储读失败时直接拒绝，不用消息摘要顶替。
func (h *GatewayHandler) prepareStrictClaudeMessages(c *gin.Context, apiKey *service.APIKey, requestPlatform, metadataUserID string) (*strictSessionRuntime, error) {
	if !h.strictSessionEnabled() || apiKey == nil {
		return &strictSessionRuntime{}, nil
	}
	if requestPlatform == service.PlatformGemini {
		return &strictSessionRuntime{}, nil
	}
	bindingCfg := h.cfg.Gateway.StrictSessionBinding
	input := service.StrictSessionIdentityInput{
		APIKeyID:            apiKey.ID,
		GroupID:             apiKey.GroupID,
		RequestPlatform:     requestPlatform,
		MetadataUserID:      metadataUserID,
		ClaudeCodeSessionID: service.ClaudeCodeSessionIDFromHeader(c),
		SessionHeaderValue:  c.GetHeader(bindingCfg.SessionHeaderOrDefault()),
		EndUserHeaderValue:  c.GetHeader(bindingCfg.EndUserHeader),
	}
	plan, err := h.gatewayService.PrepareStrictSession(c.Request.Context(), input)
	if err != nil {
		return nil, err
	}
	if plan == nil || !plan.Active {
		return &strictSessionRuntime{}, nil
	}
	return &strictSessionRuntime{Active: true, Plan: plan}, nil
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
	if rt != nil && rt.Active {
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
	case errors.Is(err, service.ErrStrictEndUserRequired):
		h.respondStrictSessionError(c, http.StatusBadRequest, strictSessionErrorEndUserRequired, "end_user_required", streamStarted)
	default:
		if reqLog != nil {
			reqLog.Warn("strict_session.store_failed", zap.Error(err))
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStoreUnavailable, "store_unavailable", streamStarted)
	}
}

// handleStrictSelectFailure 处理严格选号的存储故障和原账号不可承接。
// 返回 false 表示这不是严格绑定错误，调用方继续走官方选号失败路径。
func (h *GatewayHandler) handleStrictSelectFailure(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, err error, body []byte, streamStarted bool, writerSizeAtEntry int) bool {
	if rt == nil || !rt.Active || err == nil {
		return false
	}
	var fallback *service.StrictSessionFallbackError
	if errors.As(err, &fallback) {
		h.dispatchStrictFallback(c, rt, reqLog, fallback.AccountID, fallback.Reason, true, true, body, streamStarted, writerSizeAtEntry)
		return true
	}
	if errors.Is(err, service.ErrStrictSessionStore) {
		if reqLog != nil {
			reqLog.Warn("strict_session.store_failed", zap.String("session_fp", rt.fingerprint()), zap.Error(err))
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStoreUnavailable, "store_unavailable", streamStarted)
		return true
	}
	return false
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
) bool {
	if strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, accountIDOf(account), true)
		return true
	}
	if failoverClientGone(c) {
		return true
	}
	action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, platform, account.GetPoolModeRetryCount(), failoverErr)
	switch action {
	case FailoverContinue:
		return false
	case FailoverCanceled:
		failoverClientGone(c)
		return true
	default:
		decision := service.DecideStrictFallback(service.StrictFallbackInput{
			ResponseWritten:   strictResponseStarted(c, streamStarted, writerSizeAtEntry),
			ClientCanceled:    c.Request != nil && c.Request.Context().Err() != nil,
			AccountSide:       false,
			RetryableUpstream: failoverErr != nil && failoverErr.ShouldRetryNextAccount(),
			ThirdPartyEnabled: h.gatewayService.StrictThirdPartyEnabled(),
		})
		if decision == service.StrictFallbackPassthrough {
			h.handleFailoverExhausted(c, fs.LastFailoverErr, platform, streamStarted)
			return true
		}
		h.dispatchStrictFallback(c, rt, reqLog, account.ID, "upstream_exhausted", false, failoverErr != nil && failoverErr.ShouldRetryNextAccount(), body, streamStarted, writerSizeAtEntry)
		return true
	}
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
) {
	responseStarted := strictResponseStarted(c, streamStarted, writerSizeAtEntry)
	if !responseStarted && c.Request != nil && c.Request.Context().Err() != nil {
		failoverClientGone(c)
		return
	}
	decision := service.DecideStrictFallback(service.StrictFallbackInput{
		ResponseWritten:   responseStarted,
		ClientCanceled:    c.Request != nil && c.Request.Context().Err() != nil,
		AccountSide:       accountSide,
		RetryableUpstream: retryableUpstream,
		ThirdPartyEnabled: h.gatewayService != nil && h.gatewayService.StrictThirdPartyEnabled(),
	})
	fingerprint := ""
	if rt != nil {
		fingerprint = rt.fingerprint()
	}
	switch decision {
	case service.StrictFallbackStreamInterrupted:
		service.LogStrictSession(accountID, fingerprint, reason, "stream_interrupted")
		h.respondStrictStreamInterrupted(c, rt, reqLog, accountID, true)
	case service.StrictFallbackThirdParty:
		service.LogStrictSession(accountID, fingerprint, reason, "third_party")
		err := h.gatewayService.ForwardStrictThirdParty(c.Request.Context(), c.Request.Header, body, c.Writer)
		if err == nil {
			if reqLog != nil {
				reqLog.Info("strict_session.third_party_completed",
					zap.Int64("account_id", accountID),
					zap.String("session_fp", fingerprint),
					zap.String("reason", reason),
					zap.String("path", "third_party"),
				)
			}
			return
		}
		if strictResponseStarted(c, streamStarted, writerSizeAtEntry) && c.Writer.Size() != writerSizeAtEntry {
			if reqLog != nil {
				reqLog.Warn("strict_session.third_party_partial",
					zap.Int64("account_id", accountID),
					zap.String("session_fp", fingerprint),
					zap.Error(err),
				)
			}
			return
		}
		if reqLog != nil {
			reqLog.Warn("strict_session.third_party_failed",
				zap.Int64("account_id", accountID),
				zap.String("session_fp", fingerprint),
				zap.String("reason", reason),
				zap.Error(err),
			)
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorThirdPartyFailed, "third_party_failed", streamStarted || responseStarted)
	case service.StrictFallbackPassthrough:
		if c.Request != nil && c.Request.Context().Err() != nil {
			failoverClientGone(c)
			return
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorFallbackRequired, reason, streamStarted)
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
	}
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
