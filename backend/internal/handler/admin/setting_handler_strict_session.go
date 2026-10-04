package admin

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// StrictSessionBindingSettingsRequest 随整页设置一起提交。空的第三方密钥表示保留已有密钥。
type StrictSessionBindingSettingsRequest struct {
	StrictSessionBindingEnabled           bool   `json:"strict_session_binding_enabled"`
	StrictSessionSessionHeader            string `json:"strict_session_session_header"`
	StrictSessionSameAccountRetryLimit    int    `json:"strict_session_same_account_retry_limit"`
	StrictSessionFallbackOrder            string `json:"strict_session_fallback_order"`
	StrictSessionFallbackGroupID          int64  `json:"strict_session_fallback_group_id"`
	StrictSessionThirdPartyEnabled        bool   `json:"strict_session_third_party_enabled"`
	StrictSessionThirdPartyBaseURL        string `json:"strict_session_third_party_base_url"`
	StrictSessionThirdPartyAPIKey         string `json:"strict_session_third_party_api_key"`
	StrictSessionThirdPartyTimeoutSeconds int    `json:"strict_session_third_party_timeout_seconds"`
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
	if before == nil || after == nil || !after.StrictSessionBindingOverride {
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
	if before.StrictSessionFallbackOrder != after.StrictSessionFallbackOrder {
		changed = append(changed, "strict_session_fallback_order")
	}
	if before.StrictSessionFallbackGroupID != after.StrictSessionFallbackGroupID {
		changed = append(changed, "strict_session_fallback_group_id")
	}
	if before.StrictSessionThirdPartyEnabled != after.StrictSessionThirdPartyEnabled {
		changed = append(changed, "strict_session_third_party_enabled")
	}
	if before.StrictSessionThirdPartyBaseURL != after.StrictSessionThirdPartyBaseURL {
		changed = append(changed, "strict_session_third_party_base_url")
	}
	if before.StrictSessionThirdPartyAPIKey != after.StrictSessionThirdPartyAPIKey {
		changed = append(changed, "strict_session_third_party_api_key")
	}
	if before.StrictSessionThirdPartyTimeoutSeconds != after.StrictSessionThirdPartyTimeoutSeconds {
		changed = append(changed, "strict_session_third_party_timeout_seconds")
	}
	return changed
}

func strictSessionPayloadTouched(sent map[string]json.RawMessage) bool {
	for _, name := range []string{
		"strict_session_binding_enabled",
		"strict_session_session_header",
		"strict_session_same_account_retry_limit",
		"strict_session_fallback_order",
		"strict_session_fallback_group_id",
		"strict_session_third_party_enabled",
		"strict_session_third_party_base_url",
		"strict_session_third_party_api_key",
		"strict_session_third_party_timeout_seconds",
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
	if _, ok := sent["strict_session_fallback_order"]; ok {
		value := req.StrictSessionFallbackOrder
		patch.FallbackOrder = &value
	}
	if _, ok := sent["strict_session_fallback_group_id"]; ok {
		value := req.StrictSessionFallbackGroupID
		patch.FallbackGroupID = &value
	}
	if _, ok := sent["strict_session_third_party_enabled"]; ok {
		value := req.StrictSessionThirdPartyEnabled
		patch.ThirdPartyEnabled = &value
	}
	if _, ok := sent["strict_session_third_party_base_url"]; ok {
		value := req.StrictSessionThirdPartyBaseURL
		patch.ThirdPartyBaseURL = &value
	}
	if _, ok := sent["strict_session_third_party_api_key"]; ok {
		value := req.StrictSessionThirdPartyAPIKey
		patch.ThirdPartyAPIKey = &value
	}
	if _, ok := sent["strict_session_third_party_timeout_seconds"]; ok {
		value := req.StrictSessionThirdPartyTimeoutSeconds
		patch.ThirdPartyTimeoutSeconds = &value
	}
	return patch
}

func applyStrictSessionBindingDTO(payload *dto.SystemSettings, view service.StrictSessionBindingAdminView) {
	if payload == nil {
		return
	}
	payload.StrictSessionBindingSource = view.Source
	payload.StrictSessionBindingEnabled = view.Enabled
	payload.StrictSessionSessionHeader = view.SessionHeader
	payload.StrictSessionSameAccountRetryLimit = view.SameAccountRetryLimit
	payload.StrictSessionFallbackOrder = view.FallbackOrder
	payload.StrictSessionFallbackGroupID = view.FallbackGroupID
	payload.StrictSessionThirdPartyEnabled = view.ThirdPartyEnabled
	payload.StrictSessionThirdPartyBaseURL = view.ThirdPartyBaseURL
	payload.StrictSessionThirdPartyAPIKeyConfigured = view.ThirdPartyKeyConfigured
	payload.StrictSessionThirdPartyTimeoutSeconds = view.ThirdPartyTimeoutSeconds
}
