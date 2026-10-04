package dto

// StrictSessionBindingSettings 是管理接口里的严格会话绑定视图。
type StrictSessionBindingSettings struct {
	StrictSessionBindingEnabled        bool   `json:"strict_session_binding_enabled"`
	StrictSessionSessionHeader         string `json:"strict_session_session_header"`
	StrictSessionSameAccountRetryLimit int    `json:"strict_session_same_account_retry_limit"`
}
