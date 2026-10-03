import type {
  StrictSessionBindingAdminSettings,
  StrictSessionBindingUpdate,
} from "@/api/admin/strictSessionBinding";

export type StrictSessionBindingForm = StrictSessionBindingAdminSettings & {
  strict_session_third_party_api_key: string;
};

export const strictSessionBindingFormDefaults: StrictSessionBindingForm = {
  strict_session_binding_source: "config",
  strict_session_binding_enabled: false,
  strict_session_end_user_header: "",
  strict_session_end_user_header_trusted: false,
  strict_session_session_header: "X-Session-Id",
  strict_session_same_account_retry_limit: -1,
  strict_session_fallback_order: "group_first",
  strict_session_fallback_group_id: 0,
  strict_session_third_party_enabled: false,
  strict_session_third_party_base_url: "",
  strict_session_third_party_api_key: "",
  strict_session_third_party_api_key_configured: false,
  strict_session_third_party_timeout_seconds: 0,
};

export function clearStrictSessionBindingSecret(form: { strict_session_third_party_api_key: string }) {
  form.strict_session_third_party_api_key = "";
}

export function strictSessionBindingUpdatePayload(form: StrictSessionBindingForm): StrictSessionBindingUpdate {
  return {
    strict_session_binding_enabled: form.strict_session_binding_enabled,
    strict_session_end_user_header: form.strict_session_end_user_header,
    strict_session_end_user_header_trusted: form.strict_session_end_user_header_trusted,
    strict_session_session_header: form.strict_session_session_header,
    strict_session_same_account_retry_limit: form.strict_session_same_account_retry_limit,
    strict_session_fallback_order: form.strict_session_fallback_order,
    strict_session_fallback_group_id: Number(form.strict_session_fallback_group_id) || 0,
    strict_session_third_party_enabled: form.strict_session_third_party_enabled,
    strict_session_third_party_base_url: form.strict_session_third_party_base_url,
    strict_session_third_party_api_key: form.strict_session_third_party_api_key,
    strict_session_third_party_timeout_seconds: Number(form.strict_session_third_party_timeout_seconds) || 0,
  };
}
