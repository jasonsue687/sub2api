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
      <div class="grid gap-4 md:grid-cols-2">
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from "vue-i18n";
import Toggle from "@/components/common/Toggle.vue";
import type { StrictSessionBindingForm } from "./strictSessionBindingForm";

const props = defineProps<{
  form: StrictSessionBindingForm;
}>();

const emit = defineEmits<{
  "update:form": [value: StrictSessionBindingForm];
}>();

function updateField<K extends keyof StrictSessionBindingForm>(key: K, value: StrictSessionBindingForm[K]) {
  emit("update:form", { ...props.form, [key]: value });
}

const { t } = useI18n();
</script>
