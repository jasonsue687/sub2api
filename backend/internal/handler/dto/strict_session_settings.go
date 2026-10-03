package dto

// StrictSessionBindingSettings 是管理接口里的严格会话绑定视图。明文密钥不会出现在响应里。
type StrictSessionBindingSettings struct {
	StrictSessionBindingSource              string `json:"strict_session_binding_source"`
	StrictSessionBindingEnabled             bool   `json:"strict_session_binding_enabled"`
	StrictSessionEndUserHeader              string `json:"strict_session_end_user_header"`
	StrictSessionEndUserHeaderTrusted       bool   `json:"strict_session_end_user_header_trusted"`
	StrictSessionSessionHeader              string `json:"strict_session_session_header"`
	StrictSessionSameAccountRetryLimit      int    `json:"strict_session_same_account_retry_limit"`
	StrictSessionFallbackOrder              string `json:"strict_session_fallback_order"`
	StrictSessionFallbackGroupID            int64  `json:"strict_session_fallback_group_id"`
	StrictSessionThirdPartyEnabled          bool   `json:"strict_session_third_party_enabled"`
	StrictSessionThirdPartyBaseURL          string `json:"strict_session_third_party_base_url"`
	StrictSessionThirdPartyAPIKeyConfigured bool   `json:"strict_session_third_party_api_key_configured"`
	StrictSessionThirdPartyTimeoutSeconds   int    `json:"strict_session_third_party_timeout_seconds"`
}
