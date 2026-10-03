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
        <Toggle v-model="form.strict_session_binding_enabled" />
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
          <select v-model="form.strict_session_fallback_order" class="input">
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
          <select v-model.number="form.strict_session_fallback_group_id" class="input">
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
          <input v-model.number="form.strict_session_same_account_retry_limit" type="number" min="-1" class="input" />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.retryLimitHint") }}
          </p>
        </div>
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.sessionHeader") }}
          </label>
          <input v-model="form.strict_session_session_header" type="text" class="input" />
        </div>
        <div>
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.strictSession.endUserHeader") }}
          </label>
          <input v-model="form.strict_session_end_user_header" type="text" class="input" />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.strictSession.endUserHeaderHint") }}
          </p>
        </div>
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.strictSession.endUserTrusted")
            }}</label>
          </div>
          <Toggle v-model="form.strict_session_end_user_header_trusted" />
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
          <Toggle v-model="form.strict_session_third_party_enabled" />
        </div>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyURL") }}
            </label>
            <input v-model="form.strict_session_third_party_base_url" type="url" class="input" placeholder="https://relay.example" />
          </div>
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyTimeout") }}
            </label>
            <input v-model.number="form.strict_session_third_party_timeout_seconds" type="number" min="0" class="input" />
          </div>
          <div class="md:col-span-2">
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.strictSession.thirdPartyKey") }}
            </label>
            <input
              v-model="form.strict_session_third_party_api_key"
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

defineProps<{
  form: StrictSessionBindingForm;
  groups: AdminGroup[];
}>();

const { t } = useI18n();
</script>
