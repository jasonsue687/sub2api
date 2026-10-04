/** Admin view of strict Claude Messages session binding. The API key is never returned. */
export interface StrictSessionBindingAdminSettings {
  strict_session_binding_source: "database" | "config" | string;
  strict_session_binding_enabled: boolean;
  strict_session_session_header: string;
  strict_session_same_account_retry_limit: number;
  strict_session_fallback_order: "group_first" | "third_party_first" | string;
  strict_session_fallback_group_id: number;
  strict_session_third_party_enabled: boolean;
  strict_session_third_party_base_url: string;
  strict_session_third_party_api_key_configured: boolean;
  strict_session_third_party_timeout_seconds: number;
}

/** Fields written by the settings page. An empty API key keeps the stored secret. */
export interface StrictSessionBindingUpdate {
  strict_session_binding_enabled?: boolean;
  strict_session_session_header?: string;
  strict_session_same_account_retry_limit?: number;
  strict_session_fallback_order?: string;
  strict_session_fallback_group_id?: number;
  strict_session_third_party_enabled?: boolean;
  strict_session_third_party_base_url?: string;
  strict_session_third_party_api_key?: string;
  strict_session_third_party_timeout_seconds?: number;
}
