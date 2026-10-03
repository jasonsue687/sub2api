<template>
  <AppLayout>
    <div class="space-y-6 pb-10">
      <header class="space-y-2">
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ tr('title') }}</h1>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ tr('description') }}</p>
      </header>
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <section v-if="status" class="card space-y-4 p-5">
        <div class="flex flex-wrap items-center justify-between gap-4">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-white">{{ tr('enabled') }}</p>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ status.enabled ? tr('enabledOn') : tr('enabledOff') }}</p>
            <p class="mt-1 text-xs text-gray-400">{{ tr('source') }}: {{ status.source === 'db' ? tr('sourceDb') : tr('sourceConfig') }} · {{ tr('hook') }}: {{ status.hook_armed && status.replay_ready ? tr('ready') : tr('notReady') }}</p>
            <p class="mt-1 text-xs text-gray-400">{{ tr('dropped', { inbound: status.inbound_dropped, outbound: status.outbound_dropped }) }}</p>
          </div>
          <button type="button" class="btn btn-primary" data-testid="mock-switch" :disabled="saving" :aria-pressed="status.enabled" @click="toggle">
            {{ status.enabled ? tr('enabledOn') : tr('enabledOff') }}
          </button>
        </div>
        <div class="flex flex-wrap items-end gap-3">
          <label class="space-y-2 text-sm">
            <span class="block font-medium">{{ tr('apiKey') }}</span>
            <input v-model="apiKey" data-testid="run-api-key" type="password" autocomplete="off" class="input w-72" />
            <span class="block text-xs text-gray-400">{{ tr('apiKeyHint') }}</span>
          </label>
          <button type="button" class="btn btn-secondary" data-testid="run-tests" :disabled="running" @click="run">{{ tr(running ? 'running' : 'run') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ tr('refresh') }}</button>
        </div>
        <p class="text-xs leading-relaxed text-amber-700 dark:text-amber-300">{{ tr('runHint') }}</p>
        <p v-if="report" data-testid="run-summary" class="text-sm text-gray-700 dark:text-gray-200">{{ tr('runSummary', { completed: report.completed, total: report.total, failed: report.failed }) }}</p>
      </section>
      <section class="card overflow-hidden">
        <div class="border-b border-gray-100 p-5 dark:border-dark-700">
          <h2 class="font-semibold">{{ tr('samples') }}</h2>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ tr('samplesHint') }}</p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
              <tr><th class="p-3">{{ tr('time') }}</th><th class="p-3">{{ tr('client') }}</th><th class="p-3">{{ tr('path') }}</th><th class="p-3">{{ tr('preview') }}</th></tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="row in samples" :key="row.id" :data-testid="`sample-${row.id}`">
                <td class="whitespace-nowrap p-3 text-xs">{{ row.created_at }}</td>
                <td class="p-3 text-xs">{{ row.client_label || '—' }}</td>
                <td class="p-3 font-mono text-xs">{{ row.method }} {{ row.path }}<span class="mt-1 block text-gray-400">{{ tr('bytes', { count: row.body_bytes }) }}</span></td>
                <td class="max-w-xl break-words p-3 font-mono text-xs">{{ row.body_preview || '—' }}</td>
              </tr>
              <tr v-if="!samples.length"><td colspan="4" class="p-10 text-center text-gray-500">{{ tr('emptySamples') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="card overflow-hidden">
        <div class="border-b border-gray-100 p-5 dark:border-dark-700"><h2 class="font-semibold">{{ tr('outbound') }}</h2></div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
              <tr><th class="p-3">{{ tr('time') }}</th><th class="p-3">{{ tr('path') }}</th><th class="p-3">{{ tr('reason') }}</th><th class="p-3">{{ tr('status') }}</th><th class="p-3">{{ tr('preview') }}</th></tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="row in outbound" :key="row.id" :data-testid="`outbound-${row.id}`">
                <td class="whitespace-nowrap p-3 text-xs">{{ row.created_at }}</td>
                <td class="max-w-md break-all p-3 font-mono text-xs">{{ row.method }} {{ row.url }}</td>
                <td class="p-3 text-xs">{{ row.mock_reason }}</td>
                <td class="p-3 font-mono text-xs">{{ row.status_code }}</td>
                <td class="max-w-xl break-words p-3 font-mono text-xs">{{ row.body_preview || '—' }}</td>
              </tr>
              <tr v-if="!outbound.length"><td colspan="5" class="p-10 text-center text-gray-500">{{ tr('emptyOutbound') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import {
  getAnthropicMockStatus, listAnthropicMockOutbound, listAnthropicMockSamples, runAnthropicMockTests, updateAnthropicMock,
  type AnthropicMockOutbound, type AnthropicMockRunReport, type AnthropicMockSample, type AnthropicMockStatus
} from '@/api/admin/anthropicMock'

const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`admin.anthropicMock.${key}`, params)
const status = ref<AnthropicMockStatus | null>(null)
const samples = ref<AnthropicMockSample[]>([])
const outbound = ref<AnthropicMockOutbound[]>([])
const report = ref<AnthropicMockRunReport | null>(null)
const apiKey = ref('')
const error = ref('')
const loading = ref(false)
const saving = ref(false)
const running = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [nextStatus, nextSamples, nextOutbound] = await Promise.all([
      getAnthropicMockStatus(),
      listAnthropicMockSamples(),
      listAnthropicMockOutbound()
    ])
    status.value = nextStatus
    samples.value = nextSamples.items
    outbound.value = nextOutbound.items
  } catch {
    error.value = tr('loadFailed')
  } finally {
    loading.value = false
  }
}

async function toggle() {
  if (!status.value) return
  saving.value = true
  error.value = ''
  try {
    status.value = await updateAnthropicMock(!status.value.enabled)
  } catch {
    error.value = tr('saveFailed')
  } finally {
    saving.value = false
  }
}

async function run() {
  running.value = true
  error.value = ''
  report.value = null
  try {
    report.value = await runAnthropicMockTests(apiKey.value)
    await load()
  } catch {
    error.value = tr('runFailed')
  } finally {
    running.value = false
  }
}

onMounted(() => { void load() })
</script>
