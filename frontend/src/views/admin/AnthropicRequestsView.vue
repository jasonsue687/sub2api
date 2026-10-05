<template>
  <AppLayout>
    <div class="space-y-5 pb-10">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="space-y-1">
          <div class="flex items-center gap-3">
            <span class="h-3 w-3 rounded-full bg-orange-500" />
            <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ tr('title') }}</h1>
          </div>
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ tr('description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="outbound-audit" @click="router.push({ name: 'AdminAnthropicOutboundAudit', query: accountId > 0 ? { account_id: String(accountId) } : {} })">{{ tr('outboundAudit') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="export-page" :disabled="!data?.records.length" @click="exportPage">{{ tr('exportPage') }}</button>
      </header>

      <form class="card flex flex-wrap items-end gap-4 p-5" @submit.prevent="refresh">
        <label class="space-y-1.5 text-sm">
          <span class="block text-xs font-medium text-gray-600 dark:text-gray-300">{{ tr('window') }}</span>
          <select v-model.number="hours" data-testid="time-window" class="input w-36">
            <option :value="1">{{ tr('hour') }}</option>
            <option :value="24">{{ tr('day') }}</option>
            <option :value="168">{{ tr('week') }}</option>
            <option :value="720">{{ tr('month') }}</option>
          </select>
        </label>
        <label class="space-y-1.5 text-sm">
          <span class="block text-xs font-medium text-gray-600 dark:text-gray-300">{{ tr('account') }}</span>
          <select v-model.number="accountId" data-testid="account" class="input w-40">
            <option :value="0">{{ tr('allAccounts') }}</option>
            <option v-for="account in data?.accounts || []" :key="account.id" :value="account.id">{{ account.name || ('#' + account.id) }}</option>
          </select>
        </label>
        <label class="space-y-1.5 text-sm">
          <span class="block text-xs font-medium text-gray-600 dark:text-gray-300">{{ tr('status') }}</span>
          <select v-model="statusFilter" data-testid="status-filter" class="input w-32">
            <option value="">{{ tr('allStatus') }}</option>
            <option value="failed">{{ tr('onlyFailed') }}</option>
            <option value="mismatch">{{ tr('onlyMismatch') }}</option>
          </select>
        </label>
        <label class="space-y-1.5 text-sm">
          <span class="block text-xs font-medium text-gray-600 dark:text-gray-300">{{ tr('model') }}</span>
          <select v-model="model" data-testid="model-filter" class="input w-44">
            <option value="">{{ tr('allModels') }}</option>
            <option v-for="item in data?.models || []" :key="item" :value="item">{{ item }}</option>
          </select>
        </label>
        <button type="button" class="mb-2 inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200" role="switch" :aria-checked="onlyMulti" data-testid="only-multi" @click="onlyMulti = !onlyMulti">
          <span class="relative inline-flex h-5 w-9 items-center rounded-full" :class="onlyMulti ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'">
            <span class="absolute h-4 w-4 rounded-full bg-white shadow" :class="onlyMulti ? 'left-4' : 'left-0.5'" />
          </span>
          {{ tr('onlyMulti') }}
        </button>
        <label class="min-w-[220px] flex-1 space-y-1.5 text-sm">
          <span class="block text-xs font-medium text-gray-600 dark:text-gray-300">{{ tr('search') }}</span>
          <input v-model="queryText" data-testid="search" class="input" type="search" :placeholder="tr('searchPlaceholder')" />
        </label>
        <button type="submit" class="btn btn-primary" data-testid="refresh" :disabled="loading">{{ tr(loading ? 'refreshing' : 'refresh') }}</button>
      </form>

      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <p v-if="data && !data.inbound_enabled" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ tr('inactive') }}</p>
      <p v-if="sinkLoss" role="alert" data-testid="sink-warning" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ tr('sinkWarning', { dropped: data?.capture_health.dropped_count || 0, failed: data?.capture_health.write_failed_count || 0 }) }}</p>

      <div v-if="data" class="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <div class="card p-4"><p class="text-xs text-gray-500">{{ tr('statInbound') }}</p><p data-testid="stat-inbound" class="mt-1 text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ data.summary.inbound_requests.toLocaleString() }}</p></div>
        <div class="card p-4"><p class="text-xs text-gray-500">{{ tr('statOutbound') }}</p><p data-testid="stat-outbound" class="mt-1 text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ data.summary.outbound_attempts.toLocaleString() }}</p></div>
        <div class="card p-4"><p class="text-xs text-gray-500">{{ tr('statMulti') }}</p><p data-testid="stat-multi" class="mt-1 text-2xl font-semibold tabular-nums text-amber-600">{{ data.summary.multi_attempts.toLocaleString() }}</p></div>
        <div class="card p-4"><p class="text-xs text-gray-500">{{ tr('statFailed') }}</p><p data-testid="stat-failed" class="mt-1 text-2xl font-semibold tabular-nums text-red-600">{{ data.summary.failed_requests.toLocaleString() }}</p></div>
      </div>

      <section v-if="data" class="card overflow-hidden" :aria-busy="loading">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-3 dark:border-dark-700">
          <p class="text-xs text-gray-500">{{ tr('listHint') }}</p>
          <p class="text-xs text-gray-400">{{ tr('retention') }}</p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-400">
              <tr>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colTime') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colClientId') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colCaller') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colPath') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colModel') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colStream') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colStatus') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('outboundConsistency') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 font-medium">{{ tr('colAccount') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 text-center font-medium">{{ tr('colAttempts') }}</th>
                <th class="whitespace-nowrap px-2.5 py-3 text-right font-medium">{{ tr('colDuration') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in data.records" :key="row.client_request_id || row.request_id" :data-testid="'request-' + (row.client_request_id || row.request_id)" class="border-b border-gray-100 align-middle dark:border-dark-700" :class="row.attempt_count > 1 ? 'bg-amber-50/40' : ''">
                <td class="whitespace-nowrap px-2.5 py-2.5 text-xs text-gray-500">{{ splitLocalTime(row.created_at).date }}<span class="block font-mono text-gray-900 dark:text-gray-100">{{ splitLocalTime(row.created_at).time }}</span></td>
                <td class="px-2.5 py-2.5">
                  <button v-if="row.client_request_id" type="button" class="font-mono text-xs text-primary-600 underline decoration-primary-200 underline-offset-2" :data-testid="'open-' + row.client_request_id" @click="open(row)">{{ shortId(row.client_request_id) }}</button>
                  <span v-else class="font-mono text-xs text-gray-400">{{ tr('dash') }}</span>
                </td>
                <td class="px-2.5 py-2.5 text-xs">{{ caller(row) }}<span class="block text-gray-400">{{ row.username || tr('dash') }}</span></td>
                <td class="px-2.5 py-2.5">
                  <span class="font-mono text-xs text-gray-700 dark:text-gray-200">{{ row.endpoint || tr('dash') }}</span>
                  <span v-if="!row.has_inbound" data-testid="no-inbound" class="mt-1 block"><span class="inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600">{{ tr('noInbound') }}</span></span>
                  <span v-if="!row.has_outbound" data-testid="no-outbound" class="mt-1 block"><span class="inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600">{{ tr(row.attempt_count > 0 ? 'outboundNotCaptured' : 'noOutbound') }}</span></span>
                </td>
                <td class="px-2.5 py-2.5 font-mono text-xs leading-5">
                  <span>{{ row.inbound_model || tr('dash') }} <span class="text-gray-400">→</span></span>
                  <span class="block text-gray-500">{{ row.outbound_model || tr('dash') }}</span>
                </td>
                <td class="px-2.5 py-2.5 text-xs" :data-stream="row.stream" :class="row.stream ? 'text-emerald-600' : 'text-gray-400'">{{ tr(row.stream ? 'yes' : 'no') }}</td>
                <td class="px-2.5 py-2.5"><span data-testid="status" class="inline-flex rounded-full px-2.5 py-0.5 font-mono text-xs font-medium" :class="statusClass(row.status)">{{ row.status || tr('dash') }}</span></td>
                <td class="px-2.5 py-2.5" data-testid="outbound-consistency"><ConsistencyBadge v-if="row.has_outbound || row.attempt_count > 0" :state="row.consistency" /><span v-else class="text-xs text-gray-400">{{ tr('noOutbound') }}</span></td>
                <td class="px-2.5 py-2.5 font-mono text-xs">{{ row.account_name || tr('dash') }}</td>
                <td class="px-2.5 py-2.5 text-center">
                  <span v-if="row.attempt_count > 1" data-testid="attempt-count" :data-attempts="row.attempt_count" class="inline-flex rounded-full bg-amber-100 px-2 py-0.5 text-xs font-semibold text-amber-800 ring-1 ring-amber-300">{{ tr('attemptTimes', { count: row.attempt_count }) }}</span>
                  <span v-else data-testid="attempt-count" :data-attempts="row.attempt_count" class="text-xs tabular-nums text-gray-400">{{ row.attempt_count }}</span>
                </td>
                <td class="px-2.5 py-2.5 text-right font-mono text-xs tabular-nums">{{ formatDuration(row.duration_ms) }}</td>
              </tr>
              <tr v-if="!data.records.length"><td colspan="11" class="p-10 text-center text-gray-500">{{ tr('empty') }}</td></tr>
            </tbody>
          </table>
        </div>
        <div class="flex items-center justify-between border-t border-gray-100 p-4 dark:border-dark-700">
          <span class="text-xs text-gray-500">{{ tr('page', { page: data.page, total: data.total }) }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary" data-testid="prev-page" :disabled="loading || data.page <= 1" @click="goPage(-1)">{{ tr('previous') }}</button>
            <button type="button" class="btn btn-secondary" data-testid="next-page" :disabled="loading || data.page * data.page_size >= data.total" @click="goPage(1)">{{ tr('next') }}</button>
          </div>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConsistencyBadge from '@/components/anthropic/ConsistencyBadge.vue'
import { listAnthropicSessions, type AnthropicSessionList, type AnthropicSessionRow } from '@/api/admin/anthropicRequests'
import { formatDuration, shortId, splitLocalTime } from '@/utils/anthropicRequestDiff'

const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`admin.anthropicRequests.${key}`, params)
const route = useRoute()
const router = useRouter()
const hours = ref(Number(route.query.window || 24))
const accountId = ref(Number(route.query.account_id || 0))
const statusFilter = ref(String(route.query.status || ''))
const model = ref(String(route.query.model || ''))
const onlyMulti = ref(route.query.only_multi === '1')
const queryText = ref(String(route.query.q || ''))
const page = ref(1)
const loading = ref(false)
const error = ref('')
const data = ref<AnthropicSessionList | null>(null)
let controller: AbortController | undefined
let sequence = 0
let windowEnd = new Date()

const sinkLoss = computed(() => !!data.value && (data.value.capture_health.dropped_count > 0 || data.value.capture_health.write_failed_count > 0))

function statusClass(status: number) {
  if (status === 429) return 'bg-amber-100 text-amber-700'
  if (status >= 200 && status < 300) return 'bg-emerald-100 text-emerald-700'
  if (status >= 400) return 'bg-red-100 text-red-700'
  return 'bg-gray-100 text-gray-600'
}
function caller(row: AnthropicSessionRow) {
  if (!row.api_key_id && !row.api_key_name) return row.username || tr('dash')
  return 'key#' + row.api_key_id + (row.api_key_name ? ' ' + row.api_key_name : '')
}
function open(row: AnthropicSessionRow) {
  void router.push({ name: 'AdminAnthropicRequestDetail', params: { clientRequestId: row.client_request_id } })
}
watch([hours, accountId, statusFilter, model, onlyMulti], () => { page.value = 1; windowEnd = new Date(); void fetchData() })

async function refresh() {
  page.value = 1
  windowEnd = new Date()
  await fetchData()
}
async function fetchData() {
  controller?.abort()
  controller = new AbortController()
  const current = ++sequence
  loading.value = true
  error.value = ''
  try {
    const result = await listAnthropicSessions({
      start_time: new Date(windowEnd.getTime() - hours.value * 3600000).toISOString(),
      end_time: windowEnd.toISOString(),
      page: page.value,
      page_size: 50,
      account_id: accountId.value > 0 ? accountId.value : undefined,
      only_multi: onlyMulti.value || undefined,
      only_failed: statusFilter.value === 'failed' || undefined,
      only_mismatch: statusFilter.value === 'mismatch' || undefined,
      model: model.value || undefined,
      q: queryText.value.trim() || undefined
    }, controller.signal)
    if (current === sequence) data.value = result
  } catch {
    if (current === sequence) { data.value = null; error.value = tr('loadFailed') }
  } finally {
    if (current === sequence) loading.value = false
  }
}
function goPage(delta: number) {
  page.value = (data.value?.page || 1) + delta
  void fetchData()
}
function exportPage() {
  const blob = new Blob([JSON.stringify(data.value?.records ?? [], null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = 'anthropic-requests.json'
  link.click()
  URL.revokeObjectURL(url)
}
onMounted(() => { void fetchData() })
</script>
