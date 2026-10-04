<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t("admin.settings.strictSession.title") }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t("admin.settings.strictSession.description") }}
      </p>
    </div>
    <div class="space-y-5 p-6">
      <div class="flex items-center justify-between">
        <div>
          <label class="font-medium text-gray-900 dark:text-white">{{
            t("admin.settings.strictSession.enabled")
          }}</label>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.enabledHint") }}
          </p>
        </div>
        <Toggle :model-value="form.strict_session_binding_enabled" @update:model-value="updateField('strict_session_binding_enabled', $event)" />
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{
          form.strict_session_binding_source === "database"
            ? t("admin.settings.strictSession.sourceDatabase")
            : t("admin.settings.strictSession.sourceConfig")
        }}
      </p>
      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.fallbackOrder") }}
          </label>
          <select :value="form.strict_session_fallback_order" @change="updateField('strict_session_fallback_order', ($event.target as HTMLSelectElement).value)" class="input">
            <option value="group_first">{{ t("admin.settings.strictSession.orderGroupFirst") }}</option>
            <option value="third_party_first">{{ t("admin.settings.strictSession.orderThirdPartyFirst") }}</option>
          </select>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.fallbackOrderHint") }}
          </p>
        </div>
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.fallbackGroup") }}
          </label>
          <select :value="form.strict_session_fallback_group_id" @change="updateField('strict_session_fallback_group_id', Number(($event.target as HTMLSelectElement).value))" class="input">
            <option :value="0">{{ t("admin.settings.strictSession.fallbackGroupNone") }}</option>
            <option
              v-for="group in groups"
              :key="group.id"
              :value="group.id"
            >
              {{ group.name }} (#{{ group.id }}, {{ group.platform }})
            </option>
          </select>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.fallbackGroupHint") }}
          </p>
        </div>
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.retryLimit") }}
          </label>
          <input :value="form.strict_session_same_account_retry_limit" @input="updateField('strict_session_same_account_retry_limit', Number(($event.target as HTMLInputElement).value))" type="number" min="-1" class="input" />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.retryLimitHint") }}
          </p>
        </div>
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.sessionHeader") }}
          </label>
          <input :value="form.strict_session_session_header" @input="updateField('strict_session_session_header', ($event.target as HTMLInputElement).value)" type="text" class="input" />
        </div>
      </div>
      <div class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700">
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.strictSession.thirdPartyEnabled")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.strictSession.thirdPartyHint") }}
            </p>
          </div>
          <Toggle :model-value="form.strict_session_third_party_enabled" @update:model-value="updateField('strict_session_third_party_enabled', $event)" />
        </div>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyURL") }}
            </label>
            <input :value="form.strict_session_third_party_base_url" @input="updateField('strict_session_third_party_base_url', ($event.target as HTMLInputElement).value)" type="url" class="input" placeholder="https://relay.example" />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyTimeout") }}
            </label>
            <input :value="form.strict_session_third_party_timeout_seconds" @input="updateField('strict_session_third_party_timeout_seconds', Number(($event.target as HTMLInputElement).value))" type="number" min="0" class="input" />
          </div>
          <div class="md:col-span-2">
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyKey") }}
            </label>
            <input
              :value="form.strict_session_third_party_api_key" @input="updateField('strict_session_third_party_api_key', ($event.target as HTMLInputElement).value)"
              type="password"
              autocomplete="new-password"
              class="input"
              :placeholder="form.strict_session_third_party_api_key_configured ? t('admin.settings.strictSession.thirdPartyKeyConfigured') : ''"
            />
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.strictSession.thirdPartyKeyHint") }}
            </p>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { AdminGroup } from "@/types";
import Toggle from "@/components/common/Toggle.vue";
import type { StrictSessionBindingForm } from "./strictSessionBindingForm";

const props = defineProps<{
  form: StrictSessionBindingForm;
  groups: AdminGroup[];
}>();

const emit = defineEmits<{
  "update:form": [value: StrictSessionBindingForm];
}>();

function updateField<K extends keyof StrictSessionBindingForm>(key: K, value: StrictSessionBindingForm[K]) {
  emit("update:form", { ...props.form, [key]: value });
}

const { t } = useI18n();
</script>
