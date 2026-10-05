package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	strictFlowPassthrough = iota
	strictFlowStop
	strictFlowSameAccount
)

const (
	strictSessionErrorType               = "session_binding_error"
	strictSessionErrorIDRequired         = "strict_session_id_required"
	strictSessionErrorConfigUnavailable  = "strict_session_config_unavailable"
	strictSessionErrorStoreUnavailable   = "strict_session_store_unavailable"
	strictSessionErrorAccountUnavailable = "strict_session_account_unavailable"
	strictSessionErrorStreamInterrupted  = "strict_session_stream_interrupted"
	strictSessionErrorHeader             = "X-Sub2API-Error-Code"
)

// strictSessionRuntime 是 Claude Messages 请求上的严格绑定状态。
// Active 为 false 时，后续选号和故障转移保持官方行为。
type strictSessionRuntime struct {
	Active bool
	Plan   *service.StrictSessionPlan
}

func (rt *strictSessionRuntime) locksAccount() bool {
	return rt != nil && rt.Active
}

func (h *GatewayHandler) strictSessionEnabled(c *gin.Context) bool {
	return h.effectiveStrictBinding(c).Enabled
}

// Logging helpers only inspect the request snapshot and never reload it.
func (h *GatewayHandler) effectiveStrictBinding(c *gin.Context) config.GatewayStrictSessionBindingConfig {
	if c == nil || c.Request == nil {
		return config.GatewayStrictSessionBindingConfig{}
	}
	cfg, _ := service.StrictSessionBindingConfigFromContext(c.Request.Context())
	return cfg
}

func (h *GatewayHandler) snapshotStrictBinding(c *gin.Context) (config.GatewayStrictSessionBindingConfig, error) {
	cfg, err := h.gatewayService.EffectiveStrictSessionBinding(c.Request.Context())
	if err != nil {
		return config.GatewayStrictSessionBindingConfig{}, err
	}
	c.Request = c.Request.WithContext(service.WithStrictSessionBindingConfig(c.Request.Context(), cfg))
	return cfg, nil
}

// prepareStrictClaudeMessages 只在非 Gemini 的 /v1/messages 路径调用。
// 缺少稳定会话 ID 或存储读失败时直接拒绝，不用消息摘要顶替。
func (h *GatewayHandler) prepareStrictClaudeMessages(c *gin.Context, apiKey *service.APIKey, requestPlatform, metadataUserID string) (*strictSessionRuntime, error) {
	if requestPlatform == service.PlatformGemini {
		return &strictSessionRuntime{}, nil
	}
	bindingCfg, err := h.snapshotStrictBinding(c)
	if err != nil {
		return nil, err
	}
	if !bindingCfg.Enabled || apiKey == nil {
		return &strictSessionRuntime{}, nil
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
	case errors.Is(err, service.ErrStrictSessionConfigUnavailable):
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorConfigUnavailable, "config_unavailable", streamStarted)
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
func (h *GatewayHandler) handleStrictSelectFailure(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, err error, streamStarted bool, writerSizeAtEntry int) int {
	if !rt.locksAccount() || err == nil {
		return strictFlowPassthrough
	}
	var unavailable *service.StrictSessionAccountUnavailableError
	if errors.As(err, &unavailable) {
		return h.rejectStrictAccount(c, rt, reqLog, unavailable.AccountID, unavailable.Reason, streamStarted, writerSizeAtEntry)
	}
	if errors.Is(err, service.ErrStrictSessionStore) {
		if reqLog != nil {
			reqLog.Warn("strict_session.store_failed", zap.String("session_fp", rt.fingerprint()), zap.Error(err))
		}
		h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorStoreUnavailable, "store_unavailable", strictResponseStarted(c, streamStarted, writerSizeAtEntry))
		return strictFlowStop
	}
	return strictFlowPassthrough
}

// rejectStrictAccount preserves the binding and delegates routing to the caller.
// It never selects another account, changes groups, or forwards to another relay.
func (h *GatewayHandler) rejectStrictAccount(c *gin.Context, rt *strictSessionRuntime, reqLog *zap.Logger, accountID int64, reason string, streamStarted bool, writerSizeAtEntry int) int {
	if failoverClientGone(c) {
		return strictFlowStop
	}
	if strictResponseStarted(c, streamStarted, writerSizeAtEntry) {
		h.respondStrictStreamInterrupted(c, rt, reqLog, accountID, true)
		return strictFlowStop
	}
	service.LogStrictSession(accountID, rt.fingerprint(), reason, "error")
	h.respondStrictSessionError(c, http.StatusServiceUnavailable, strictSessionErrorAccountUnavailable, reason, false)
	return strictFlowStop
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
	h.handleStreamingAwareErrorWithCode(c, status, strictSessionErrorType, code, message, streamStarted, reason)
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
