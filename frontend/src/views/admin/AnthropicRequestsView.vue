<template>
  <AppLayout>
    <div class="space-y-6 pb-10">
      <header class="space-y-2">
        <div class="flex items-center gap-3"><span class="h-3 w-3 rounded-full bg-orange-500" /><h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ tr('title') }}</h1></div>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ tr('description') }}</p>
      </header>
      <form class="card flex flex-wrap items-end gap-4 p-5" @submit.prevent="fetchData(true)">
        <label class="space-y-2 text-sm"><span class="block font-medium">{{ tr('account') }}</span><input v-model="account" data-testid="account" type="number" min="1" step="1" class="input w-44" placeholder="11" required /></label>
        <label class="space-y-2 text-sm"><span class="block font-medium">{{ tr('window') }}</span><select v-model="hours" data-testid="time-window" class="input w-48"><option :value="1">{{ tr('hour') }}</option><option :value="24">{{ tr('day') }}</option><option :value="168">{{ tr('week') }}</option><option :value="720">{{ tr('month') }}</option></select></label>
        <button type="submit" class="btn btn-primary" :disabled="loading">{{ tr(loading ? 'refreshing' : 'refresh') }}</button>
        <label class="flex items-center gap-2 pb-2 text-sm"><input v-model="autoRefresh" type="checkbox" />{{ tr('auto') }}</label>
        <label class="flex items-center gap-2 pb-2 text-sm"><input v-model="onlyMismatch" data-testid="mismatch-only" type="checkbox" />{{ tr('onlyMismatch') }}</label>
      </form>
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <template v-if="data">
        <p v-if="!data.collection_enabled" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ tr('inactive') }}</p>
        <p v-if="sinkLoss" role="alert" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ tr('sinkWarning', { dropped: data.sink_health.dropped_count, failed: data.sink_health.write_failed_count, queued: data.sink_health.queue_depth }) }}</p>
        <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <div v-for="metric in metrics" :key="metric.key" class="card p-4"><p class="text-xs text-gray-500 dark:text-gray-400">{{ tr(metric.label) }}</p><p class="mt-2 text-3xl font-semibold tabular-nums" :class="metric.alert && data.summary[metric.key] > 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-white'">{{ data.summary[metric.key].toLocaleString() }}</p></div>
        </div>
        <div class="space-y-1 text-xs text-gray-500 dark:text-gray-400"><p>{{ tr('scope', { count: data.summary.uncorrelated_attempts }) }}</p><p>{{ tr('updated', { time: formatTime(data.end_time) }) }}</p></div>
        <section v-if="data.variants.length" class="card overflow-hidden">
          <div class="space-y-1 border-b border-gray-100 p-5 dark:border-dark-700"><h2 class="font-semibold">{{ tr('variants') }} · {{ data.summary.identity_variants }}</h2><p class="text-xs text-gray-500 dark:text-gray-400">{{ tr('variantHint') }}</p></div>
          <div class="overflow-x-auto"><table class="w-full text-left text-sm"><thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400"><tr><th class="p-3">{{ tr('ua') }}</th><th class="p-3">{{ tr('entrypoint') }}</th><th class="p-3">{{ tr('device') }}</th><th class="p-3">{{ tr('count') }}</th><th class="p-3">{{ tr('lastSeen') }}</th></tr></thead><tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="variant in data.variants" :key="variant.signature"><td class="p-3 font-mono text-xs">{{ variant.user_agent || '—' }}</td><td class="p-3">{{ variant.entrypoint || '—' }}<span class="block font-mono text-xs text-gray-400">{{ variant.version || '—' }}</span></td><td class="p-3 font-mono text-xs" :title="variant.device_hash">{{ shortHash(variant.device_hash) }}</td><td class="p-3 font-semibold tabular-nums">{{ variant.count }}</td><td class="whitespace-nowrap p-3 text-xs">{{ formatTime(variant.last_seen) }}</td></tr></tbody></table></div>
        </section>
        <section class="card overflow-hidden" :aria-busy="loading">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700"><p class="text-xs text-gray-500 dark:text-gray-400">{{ tr('compareHint') }}</p><button class="btn btn-secondary text-xs" :disabled="!data.records.length" @click="exportPage">{{ tr('export') }}</button></div>
          <div class="overflow-x-auto"><table class="w-full text-left text-sm"><thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400"><tr><th class="p-3">{{ tr('time') }}</th><th class="p-3">{{ tr('model') }}</th><th class="p-3">{{ tr('ua') }}</th><th class="p-3">{{ tr('entrypoint') }}</th><th class="p-3">{{ tr('consistency') }}</th><th class="p-3">{{ tr('result') }}</th><th class="p-3">{{ tr('actions') }}</th></tr></thead><tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="row in data.records" :key="row.id" :data-testid="`request-${row.id}`" class="align-top">
              <td class="whitespace-nowrap p-3 text-xs">{{ formatTime(row.audit.started_at) }}<span class="mt-1 block text-gray-400">#{{ row.id }} · {{ row.audit.endpoint.endsWith('count_tokens') ? tr('countTokens') : tr('messages') }}</span></td>
              <td class="max-w-56 break-words p-3 font-mono text-xs">{{ row.audit.parameters.model || '—' }}<span class="mt-1 block text-gray-400">max_tokens: {{ row.audit.parameters.max_tokens ?? '—' }}<br />stream: {{ row.audit.parameters.stream ?? '—' }}</span></td>
              <td class="max-w-64 break-words p-3 font-mono text-xs">{{ row.audit.headers['user-agent'] || '—' }}</td>
              <td class="p-3 text-xs">{{ row.audit.cc_entrypoint || '—' }}<span class="mt-1 block font-mono text-gray-400">{{ row.audit.cc_version || '—' }}</span></td>
              <td class="p-3"><span class="inline-flex whitespace-nowrap rounded-full px-2 py-1 text-xs font-medium" :class="badge(row.audit.consistency)">{{ tr(row.audit.consistency === 'unknown' ? 'unknownState' : row.audit.consistency) }}</span><p v-for="issue in row.audit.issues" :key="issue" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ issueText(issue) }}</p></td>
              <td class="whitespace-nowrap p-3 font-mono text-xs"><span :class="row.audit.status >= 400 || row.audit.error_class ? 'text-red-600' : ''">{{ row.audit.status || tr('transport') }}</span><span v-if="row.audit.mock" data-testid="mock-badge" class="mt-1 inline-flex rounded-full bg-violet-100 px-2 py-1 text-xs font-medium text-violet-700 dark:bg-violet-900/30 dark:text-violet-300">{{ tr('mock') }}</span><span class="mt-1 block text-gray-400">{{ row.audit.headers_ms }} ms</span><span v-if="row.audit.error_class" class="block text-red-500">{{ row.audit.error_class }}</span></td>
              <td class="space-y-2 p-3 text-xs"><button class="block text-primary-600 hover:underline" @click="detail = row">{{ tr('detail') }}</button><label class="flex items-center gap-1 whitespace-nowrap"><input type="checkbox" :checked="selected.some(item => item.id === row.id)" :disabled="selected.length >= 2 && !selected.some(item => item.id === row.id)" :aria-label="tr('compareSelect', { id: row.id })" @change="toggleCompare(row)" />{{ tr('compare') }}</label></td>
            </tr>
            <tr v-if="!data.records.length"><td colspan="7" class="p-10 text-center text-gray-500">{{ tr('empty') }}</td></tr>
          </tbody></table></div>
          <div class="flex items-center justify-between border-t border-gray-100 p-4 dark:border-dark-700"><span class="text-xs text-gray-500">{{ tr('page', { page: data.page, total: data.total }) }}</span><div class="flex gap-2"><button class="btn btn-secondary" :disabled="loading || data.page <= 1" @click="goPage(-1)">{{ tr('previous') }}</button><button class="btn btn-secondary" :disabled="loading || data.page * data.page_size >= data.total" @click="goPage(1)">{{ tr('next') }}</button></div></div>
        </section>
        <section v-if="selected.length" class="card p-5">
          <div class="mb-4 flex items-center justify-between"><h2 class="font-semibold">{{ tr('comparison') }} <span class="text-sm text-gray-400">{{ selected.map(row => '#' + row.id).join(' / ') }}</span></h2><button class="btn btn-secondary" @click="selected = []">{{ tr('close') }}</button></div>
          <p v-if="selected.length < 2" class="text-sm text-gray-500">{{ tr('compareHint') }}</p>
          <div v-else class="overflow-x-auto"><table class="w-full table-fixed text-left text-xs"><thead><tr><th class="w-1/4 p-2">{{ tr('field') }}</th><th v-for="row in selected" :key="row.id" class="p-2">#{{ row.id }}</th></tr></thead><tbody><tr v-for="field in comparison" :key="field.key" :class="field.different ? 'bg-amber-50 dark:bg-amber-900/20' : ''"><td class="break-words p-2 font-mono">{{ field.key }}<span v-if="field.different" class="ml-1 text-amber-700 dark:text-amber-300">{{ tr('different') }}</span></td><td v-for="(value, index) in field.values" :key="index" class="break-words p-2 font-mono">{{ value ?? tr('absent') }}</td></tr></tbody></table></div>
        </section>
        <section v-if="detail" class="card p-5">
          <div class="mb-4 flex items-center justify-between"><h2 class="font-semibold">{{ tr('detail') }} #{{ detail.id }}</h2><button class="btn btn-secondary" @click="detail = null">{{ tr('close') }}</button></div>
          <pre class="max-h-[32rem] overflow-auto rounded-xl bg-gray-50 p-4 text-xs dark:bg-dark-900">{{ JSON.stringify(detail, null, 2) }}</pre>
        </section>
      </template>
      <p v-else-if="!loading && !error" class="card p-10 text-center text-gray-500">{{ tr('initial') }}</p>
      <footer class="space-y-2 text-xs leading-relaxed text-gray-500 dark:text-gray-400"><p>{{ tr('ruleHint') }}</p><p>{{ tr('limits') }}</p></footer>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { listAnthropicRequests, type AnthropicRecord, type AnthropicRequestList } from '@/api/admin/anthropicRequests'
import { comparisonFields } from '@/utils/anthropicRequests'

const { t, te } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`admin.anthropicRequests.${key}`, params)
const route = useRoute()
const account = ref(String(route.query.account_id || ''))
const hours = ref(24)
const onlyMismatch = ref(false)
const autoRefresh = ref(false)
const loading = ref(false)
const error = ref('')
const data = ref<AnthropicRequestList | null>(null)
const detail = ref<AnthropicRecord | null>(null)
const selected = ref<AnthropicRecord[]>([])
let controller: AbortController | undefined
let sequence = 0
let page = 1
let windowEnd = new Date()
let timer: ReturnType<typeof setInterval> | undefined
const metrics: Array<{ key: keyof AnthropicRequestList['summary']; label: string; alert?: boolean }> = [
  { key: 'attempts', label: 'attempts' }, { key: 'correlated_requests', label: 'correlated' },
  { key: 'identity_variants', label: 'identities' }, { key: 'parameter_variants', label: 'parameters' },
  { key: 'mismatched', label: 'mismatches', alert: true }, { key: 'unknown', label: 'unknown' },
  { key: 'http_failures', label: 'httpErrors', alert: true }, { key: 'transport_errors', label: 'transportErrors', alert: true }
]
const sinkLoss = computed(() => data.value && (data.value.sink_health.dropped_count > 0 || data.value.sink_health.write_failed_count > 0 || data.value.sink_health.queue_capacity === 0))
const comparison = computed(() => selected.value.length === 2 ? comparisonFields(selected.value[0].audit, selected.value[1].audit) : [])
const shortHash = (value?: string) => value ? value.slice(0, 12) + '…' : '—'
const formatTime = (value: string) => new Date(value).toLocaleString()
const issueText = (issue: string) => te(`admin.anthropicRequests.issue_${issue}`) ? tr(`issue_${issue}`) : issue
const badge = (state: string) => state === 'mismatch' ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300' : state === 'matched' ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
function toggleCompare(row: AnthropicRecord) {
  selected.value = selected.value.some(item => item.id === row.id) ? selected.value.filter(item => item.id !== row.id) : [...selected.value, row].slice(0, 2)
}
function clearQuery() {
  sequence++; controller?.abort(); data.value = null; error.value = ''; loading.value = false
  selected.value = []; detail.value = null; page = 1
}
watch([account, hours], clearQuery)
watch(onlyMismatch, () => { if (data.value) { page = 1; void fetchData(false) } })
async function fetchData(refreshWindow = true) {
  const id = Number(account.value)
  if (!Number.isSafeInteger(id) || id <= 0) { clearQuery(); error.value = tr('invalidAccount'); return }
  controller?.abort(); controller = new AbortController()
  const current = ++sequence
  if (refreshWindow) { page = 1; windowEnd = new Date() }
  loading.value = true; error.value = ''
  try {
    const result = await listAnthropicRequests({ account_id: id, start_time: new Date(windowEnd.getTime() - hours.value * 3600000).toISOString(), end_time: windowEnd.toISOString(), page, page_size: 50, only_mismatch: onlyMismatch.value }, controller.signal)
    if (current === sequence) data.value = result
  } catch {
    if (current === sequence) { data.value = null; error.value = tr('loadFailed') }
  } finally { if (current === sequence) loading.value = false }
}
function goPage(delta: number) { page = (data.value?.page || 1) + delta; void fetchData(false) }
function exportPage() {
  if (!data.value) return
  const url = URL.createObjectURL(new Blob([JSON.stringify(data.value, null, 2)], { type: 'application/json' }))
  const link = document.createElement('a'); link.href = url; link.download = `anthropic-account-${data.value.account_id}-page-${data.value.page}.json`; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
onMounted(() => {
  if (account.value) void fetchData(true)
  timer = setInterval(() => { if (autoRefresh.value && data.value && !loading.value && page === 1 && !document.hidden) void fetchData(true) }, 30000)
})
onUnmounted(() => { sequence++; controller?.abort(); if (timer) clearInterval(timer) })
</script>
