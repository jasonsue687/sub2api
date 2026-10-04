package admin

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// StrictSessionBindingSettingsRequest 随整页设置一起提交。
type StrictSessionBindingSettingsRequest struct {
	StrictSessionBindingEnabled        bool   `json:"strict_session_binding_enabled"`
	StrictSessionSessionHeader         string `json:"strict_session_session_header"`
	StrictSessionSameAccountRetryLimit int    `json:"strict_session_same_account_retry_limit"`
}

func (h *SettingHandler) prepareStrictSessionBindingSave(ctx context.Context, previous *service.SystemSettings, req UpdateSettingsRequest, sent map[string]json.RawMessage, omitted service.OmittedSettingKeys) (*config.GatewayStrictSessionBindingConfig, error) {
	if !strictSessionPayloadTouched(sent) {
		return nil, nil
	}
	resolved, err := h.settingService.ResolveStrictSessionBindingSave(ctx, previous, strictSessionBindingPatch(req, sent))
	if err != nil {
		return nil, err
	}
	service.KeepStrictSessionBindingKeys(omitted)
	return &resolved, nil
}

func appendStrictSessionBindingAudit(changed []string, before, after *service.SystemSettings) []string {
	if before == nil || !after.HasStrictSessionBindingSettings() {
		return changed
	}
	if before.StrictSessionBindingEnabled != after.StrictSessionBindingEnabled {
		changed = append(changed, "strict_session_binding_enabled")
	}
	if before.StrictSessionSessionHeader != after.StrictSessionSessionHeader {
		changed = append(changed, "strict_session_session_header")
	}
	if before.StrictSessionSameAccountRetryLimit != after.StrictSessionSameAccountRetryLimit {
		changed = append(changed, "strict_session_same_account_retry_limit")
	}
	return changed
}

func strictSessionPayloadTouched(sent map[string]json.RawMessage) bool {
	for _, name := range []string{
		"strict_session_binding_enabled",
		"strict_session_session_header",
		"strict_session_same_account_retry_limit",
	} {
		if _, ok := sent[name]; ok {
			return true
		}
	}
	return false
}

func strictSessionBindingPatch(req UpdateSettingsRequest, sent map[string]json.RawMessage) service.StrictSessionBindingPatch {
	patch := service.StrictSessionBindingPatch{}
	if _, ok := sent["strict_session_binding_enabled"]; ok {
		enabled := req.StrictSessionBindingEnabled
		patch.Enabled = &enabled
	}
	if _, ok := sent["strict_session_session_header"]; ok {
		value := req.StrictSessionSessionHeader
		patch.SessionHeader = &value
	}
	if _, ok := sent["strict_session_same_account_retry_limit"]; ok {
		value := req.StrictSessionSameAccountRetryLimit
		patch.SameAccountRetryLimit = &value
	}
	return patch
}

func applyStrictSessionBindingDTO(payload *dto.SystemSettings, view service.StrictSessionBindingAdminView) {
	if payload == nil {
		return
	}
	payload.StrictSessionBindingEnabled = view.Enabled
	payload.StrictSessionSessionHeader = view.SessionHeader
	payload.StrictSessionSameAccountRetryLimit = view.SameAccountRetryLimit
}
