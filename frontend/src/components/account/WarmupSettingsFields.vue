<template>
  <div class="mt-3 space-y-3">
    <p class="input-hint">{{ t('admin.accounts.warmup.scope') }}</p>
    <label class="block">
      <span class="input-label">{{ t('admin.accounts.warmup.mode') }}</span>
      <select :value="modelValue.mode" class="input" @change="update('mode', ($event.target as HTMLSelectElement).value)">
        <option value="mock">{{ t('admin.accounts.warmup.mock') }}</option>
        <option value="forward">{{ t('admin.accounts.warmup.forward') }}</option>
      </select>
    </label>
    <template v-if="modelValue.mode === 'forward'">
      <p class="input-hint">{{ t('admin.accounts.warmup.forwardHint') }}</p>
      <label class="block">
        <span class="input-label">{{ t('admin.accounts.warmup.protocol') }}</span>
        <select :value="modelValue.protocol" class="input" @change="update('protocol', ($event.target as HTMLSelectElement).value)">
          <option value="openai">OpenAI Chat Completions</option>
          <option value="anthropic">Anthropic Messages</option>
        </select>
      </label>
      <label class="block">
        <span class="input-label">Base URL</span>
        <input :value="modelValue.baseURL" type="url" required class="input" placeholder="https://api.deepseek.com/v1" @input="update('baseURL', ($event.target as HTMLInputElement).value)" />
      </label>
      <label class="block">
        <span class="input-label">API Key</span>
        <input :value="modelValue.apiKey" type="password" autocomplete="new-password" :required="!modelValue.hasAPIKey" class="input" :placeholder="modelValue.hasAPIKey ? t('admin.accounts.warmup.keyStored') : 'API Key'" @input="update('apiKey', ($event.target as HTMLInputElement).value)" />
      </label>
      <label class="block">
        <span class="input-label">{{ t('admin.accounts.warmup.model') }}</span>
        <input :value="modelValue.model" required maxlength="200" class="input" placeholder="deepseek-chat" @input="update('model', ($event.target as HTMLInputElement).value)" />
      </label>
      <label class="block">
        <span class="input-label">{{ t('admin.accounts.warmup.timeout') }}</span>
        <input :value="modelValue.timeoutSeconds" type="number" min="1" max="120" required class="input" @input="update('timeoutSeconds', Number(($event.target as HTMLInputElement).value))" />
      </label>
    </template>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { WarmupSettings } from './warmupSettings'
const { t } = useI18n()
const props = defineProps<{ modelValue: WarmupSettings }>()
const emit = defineEmits<{ 'update:modelValue': [value: WarmupSettings] }>()
function update(key: keyof WarmupSettings, value: string | number) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}
</script>
