package admin

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func strictSessionPayloadTouched(sent map[string]json.RawMessage) bool {
	for _, name := range []string{
		"strict_session_binding_enabled",
		"strict_session_end_user_header",
		"strict_session_end_user_header_trusted",
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
	if _, ok := sent["strict_session_end_user_header"]; ok {
		value := req.StrictSessionEndUserHeader
		patch.EndUserHeader = &value
	}
	if _, ok := sent["strict_session_end_user_header_trusted"]; ok {
		value := req.StrictSessionEndUserHeaderTrusted
		patch.EndUserHeaderTrusted = &value
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
	payload.StrictSessionEndUserHeader = view.EndUserHeader
	payload.StrictSessionEndUserHeaderTrusted = view.EndUserHeaderTrusted
	payload.StrictSessionSessionHeader = view.SessionHeader
	payload.StrictSessionSameAccountRetryLimit = view.SameAccountRetryLimit
	payload.StrictSessionFallbackOrder = view.FallbackOrder
	payload.StrictSessionFallbackGroupID = view.FallbackGroupID
	payload.StrictSessionThirdPartyEnabled = view.ThirdPartyEnabled
	payload.StrictSessionThirdPartyBaseURL = view.ThirdPartyBaseURL
	payload.StrictSessionThirdPartyAPIKeyConfigured = view.ThirdPartyKeyConfigured
	payload.StrictSessionThirdPartyTimeoutSeconds = view.ThirdPartyTimeoutSeconds
}
