/** Admin view of strict Claude Messages session binding. */
export interface StrictSessionBindingAdminSettings {
  strict_session_binding_enabled: boolean;
  strict_session_session_header: string;
  strict_session_same_account_retry_limit: number;
}

/** Fields written by the settings page. */
export interface StrictSessionBindingUpdate {
  strict_session_binding_enabled?: boolean;
  strict_session_session_header?: string;
  strict_session_same_account_retry_limit?: number;
}
