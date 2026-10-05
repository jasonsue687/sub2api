import type {
  StrictSessionBindingAdminSettings,
  StrictSessionBindingUpdate,
} from "@/api/admin/strictSessionBinding";

export type StrictSessionBindingForm = StrictSessionBindingAdminSettings;

export const strictSessionBindingFormDefaults: StrictSessionBindingForm = {
  strict_session_binding_enabled: true,
  strict_session_session_header: "X-Session-Id",
  strict_session_same_account_retry_limit: -1,
};


export function strictSessionBindingUpdatePayload(form: StrictSessionBindingForm): StrictSessionBindingUpdate {
  return {
    strict_session_binding_enabled: form.strict_session_binding_enabled,
    strict_session_session_header: form.strict_session_session_header,
    strict_session_same_account_retry_limit: form.strict_session_same_account_retry_limit,
  };
}
