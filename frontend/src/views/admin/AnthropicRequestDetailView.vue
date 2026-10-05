<template>
  <AppLayout>
    <div class="space-y-5 pb-10">
      <header class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-3">
          <button type="button" class="btn btn-secondary btn-sm" data-testid="back" @click="back">{{ tr('back') }}</button>
          <div>
            <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ tr('detailTitle') }} <span class="font-mono text-base text-gray-500">{{ shortId(session?.client_request_id || '') }}</span></h1>
          </div>
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm" data-testid="copy-inbound" :disabled="!detail?.inbound" @click="copySide('inbound')">{{ tr('copyInbound') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-testid="copy-outbound" :disabled="!selected" @click="copySide('outbound')">{{ tr('copyOutbound') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-testid="export-detail" :disabled="!detail" @click="exportDetail">{{ tr('exportDetail') }}</button>
        </div>
      </header>
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-700">{{ error }}</p>
      <template v-if="detail && session">
        <section class="card grid gap-4 p-5 sm:grid-cols-2 xl:grid-cols-4">
          <div><p class="text-xs text-gray-500">{{ tr('metaClient') }}</p><p class="mt-1 break-all font-mono text-sm">{{ session.client_request_id || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaRequest') }}</p><p class="mt-1 break-all font-mono text-sm">{{ session.request_id || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaTime') }}</p><p class="mt-1 font-mono text-sm">{{ splitLocalTime(session.created_at).date }} {{ splitLocalTime(session.created_at).time }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaCaller') }}</p><p class="mt-1 text-sm">{{ caller }}<span class="block text-xs text-gray-400">{{ session.username || tr('dash') }}</span></p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaPath') }}</p><p class="mt-1 font-mono text-sm">POST {{ session.endpoint || tr('dash') }}<span v-if="session.stream" class="text-gray-400"> · {{ tr('streamSuffix') }}</span></p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaModel') }}</p><p class="mt-1 font-mono text-sm">{{ session.inbound_model || tr('dash') }} → {{ session.outbound_model || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaStatus') }}</p><p class="mt-1"><span class="inline-flex rounded-full px-2.5 py-0.5 font-mono text-xs" :class="statusClass(session.status)">{{ session.status || tr('dash') }}</span></p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaAccount') }}</p><p class="mt-1 font-mono text-sm">{{ session.account_name || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaUpstream') }}</p><p class="mt-1 break-all font-mono text-sm">{{ selected?.upstream_request_id || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaDuration') }}</p><p class="mt-1 font-mono text-sm">{{ formatDuration(session.duration_ms) }} · {{ formatDuration(selected?.headers_ms || 0) }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaError') }}</p><p class="mt-1 text-sm">{{ session.error_class || tr('dash') }}</p></div>
          <div><p class="text-xs text-gray-500">{{ tr('metaAttempts') }}</p><p class="mt-1 text-sm">{{ session.attempt_count }}</p></div>
        </section>

        <section class="card space-y-3 p-5" data-testid="outbound-check">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-sm font-semibold">{{ tr('outboundConsistency') }}</h2>
            <button type="button" class="btn btn-secondary btn-sm" data-testid="outbound-audit" @click="router.push({ name: 'AdminAnthropicOutboundAudit', query: selected?.account_id ? { account_id: String(selected.account_id) } : {} })">{{ tr('outboundAudit') }}</button>
          </div>
          <div v-if="session.attempt_count > 0" class="flex items-center gap-2 text-xs" data-testid="all-outbound-check"><span>{{ tr('allOutboundAttempts') }}</span><ConsistencyBadge :state="session.consistency" /></div>
          <template v-if="selected">
            <p class="text-xs text-gray-500">{{ tr('selectedOutboundAttempt', { seq: selected.attempt_seq }) }}</p>
            <ConsistencyBadge data-testid="selected-outbound-check" :state="selected.consistency" :issues="selected.summary?.issues" />
            <dl class="grid gap-3 text-xs md:grid-cols-3">
              <div><dt class="text-gray-500">User-Agent</dt><dd class="mt-1 break-all font-mono" data-testid="outbound-ua">{{ outboundUA || tr('dash') }}</dd></div>
              <div><dt class="text-gray-500">cc_entrypoint</dt><dd class="mt-1 font-mono">{{ selected.summary?.cc_entrypoint || tr('dash') }}</dd></div>
              <div><dt class="text-gray-500">cc_version</dt><dd class="mt-1 font-mono">{{ selected.summary?.cc_version || tr('dash') }}</dd></div>
            </dl>
          </template>
          <p v-else class="text-xs text-gray-500">{{ tr(session.attempt_count > 0 ? 'outboundNotCaptured' : 'noOutbound') }}</p>
          <p class="text-xs text-gray-500">{{ tr('outboundRuleHint') }}</p>
        </section>

        <div class="flex flex-wrap items-center gap-3 text-xs text-gray-600">
          <span class="inline-flex items-center gap-1"><span class="h-3 w-1 rounded bg-emerald-500" />{{ tr('legendAdd') }}</span>
          <span class="inline-flex items-center gap-1"><span class="h-3 w-1 rounded bg-red-500" />{{ tr('legendDel') }}</span>
          <span class="inline-flex items-center gap-1"><span class="h-3 w-1 rounded bg-amber-400" />{{ tr('legendChg') }}</span>
          <span class="italic text-gray-400">{{ tr('legendRedacted') }}</span>
          <button type="button" class="ml-auto inline-flex items-center gap-2" role="switch" :aria-checked="onlyDiff" data-testid="only-diff" :disabled="!comparable" @click="onlyDiff = !onlyDiff">
            <span class="relative inline-flex h-5 w-9 items-center rounded-full" :class="onlyDiff && comparable ? 'bg-primary-500' : 'bg-gray-300'">
              <span class="absolute h-4 w-4 rounded-full bg-white shadow" :class="onlyDiff && comparable ? 'left-4' : 'left-0.5'" />
            </span>
            {{ tr('onlyDiff') }}
          </button>
        </div>

        <div v-if="detail.attempts.length > 1" data-testid="attempt-chips" class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-gray-500">{{ tr('attemptTotal', { count: detail.attempts.length }) }}</span>
          <button v-for="attempt in orderedAttempts" :key="attempt.attempt_seq" type="button" class="rounded-full px-3 py-1 text-xs" :data-testid="'attempt-' + attempt.attempt_seq" :class="attempt.attempt_seq === selected?.attempt_seq ? 'bg-primary-600 text-white' : 'bg-white text-gray-700 ring-1 ring-gray-200'" @click="selectedSeq = attempt.attempt_seq">
            {{ tr('attemptChip', { seq: attempt.attempt_seq, account: attempt.account_name || tr('dash') }) }}
            <span class="ml-1 font-mono">{{ attempt.status }}</span>
          </button>
          <p class="w-full text-xs text-gray-500">{{ tr('attemptCurrent', { seq: selected?.attempt_seq || 0, upstream: selected?.upstream_request_id || tr('dash'), reason: reasonLabel(selected?.retry_reason || '') }) }}</p>
        </div>

        <p v-if="!detail.inbound" data-testid="no-inbound-banner" class="rounded-xl bg-gray-100 px-4 py-3 text-sm text-gray-600">{{ tr('noInbound') }}</p>
        <p v-if="truncated" data-testid="truncated-banner" class="rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-800">{{ tr('truncated') }}</p>

        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <section data-testid="inbound-pane" class="card overflow-hidden">
            <div class="border-b border-gray-100 px-4 py-3">
              <h2 class="text-sm font-semibold">{{ tr('inboundTitle') }}</h2>
              <p class="text-xs text-gray-500">{{ tr('inboundHint') }}</p>
            </div>
            <div v-if="detail.inbound" class="max-h-[48rem] overflow-auto p-3 font-mono text-xs leading-6">
              <p class="mb-1 text-[11px] font-semibold tracking-wide text-gray-400">{{ tr('headers') }}</p>
              <div v-for="line in headerLines" :key="'h' + line.id" :data-kind="sideKind(line, 'left')" class="whitespace-pre border-l-4 border-transparent pl-2" :class="lineClass(line, 'left')">{{ line.left }}</div>
              <p class="mb-1 mt-3 text-[11px] font-semibold tracking-wide text-gray-400">{{ tr('body') }}</p>
              <p v-if="detail.inbound.body_state && detail.inbound.body_state !== 'parsed'" class="text-gray-400">{{ tr('bodyUnavailable', { state: detail.inbound.body_state }) }}</p>
              <div v-for="line in bodyLines" :key="'b' + line.id" :data-kind="sideKind(line, 'left')" data-testid="diff-line" class="whitespace-pre border-l-4 border-transparent pl-2" :class="lineClass(line, 'left')">{{ line.left }}</div>
            </div>
          </section>
          <section data-testid="outbound-pane" class="card overflow-hidden">
            <div class="border-b border-gray-100 px-4 py-3">
              <h2 class="text-sm font-semibold">{{ tr('outboundTitle') }}</h2>
              <p class="text-xs text-gray-500">{{ tr('outboundHint', { endpoint: selected?.endpoint || session.endpoint || '', account: selected?.account_name || tr('dash') }) }}</p>
            </div>
            <div v-if="selected" class="max-h-[48rem] overflow-auto p-3 font-mono text-xs leading-6">
              <p class="mb-1 text-[11px] font-semibold tracking-wide text-gray-400">{{ tr('headers') }}</p>
              <div v-for="line in headerLines" :key="'oh' + line.id" :data-kind="sideKind(line, 'right')" class="whitespace-pre border-l-4 border-transparent pl-2" :class="lineClass(line, 'right')">{{ line.right }}</div>
              <p class="mb-1 mt-3 text-[11px] font-semibold tracking-wide text-gray-400">{{ tr('body') }}</p>
              <p v-if="selected.body_state && selected.body_state !== 'parsed'" class="text-gray-400">{{ tr('bodyUnavailable', { state: selected.body_state }) }}</p>
              <div v-for="line in bodyLines" :key="'ob' + line.id" :data-kind="sideKind(line, 'right')" data-testid="diff-line" class="whitespace-pre border-l-4 border-transparent pl-2" :class="lineClass(line, 'right')">{{ line.right }}</div>
            </div>
            <p v-else data-testid="no-outbound-pane" class="p-6 text-sm text-gray-500">{{ tr(session.attempt_count > 0 ? 'outboundNotCaptured' : 'noOutboundBody') }}</p>
          </section>
        </div>
        <p v-if="comparable && onlyDiff && !bodyLines.some(line => line.kind !== 'same')" data-testid="no-diff" class="text-sm text-gray-500">{{ tr('noDiff') }}</p>
        <footer class="text-xs leading-relaxed text-gray-500">{{ tr('footer') }}</footer>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConsistencyBadge from '@/components/anthropic/ConsistencyBadge.vue'
import { getAnthropicSession, type AnthropicCapture, type AnthropicSessionDetail } from '@/api/admin/anthropicRequests'
import { alignDocuments, alignHeaders, formatDuration, presentDocument, shortId, splitLocalTime, storedDocument, type AlignedLine, type FormatLabels } from '@/utils/anthropicRequestDiff'

const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`admin.anthropicRequests.${key}`, params)
const route = useRoute()
const router = useRouter()
const detail = ref<AnthropicSessionDetail | null>(null)
const error = ref('')
const onlyDiff = ref(false)
const selectedSeq = ref(0)

const labels = computed<FormatLabels>(() => ({
  omitted: (len, hash) => hash ? String(tr('omitted', { len, hash: hash.slice(0, 4) })) : String(tr('omittedPlain', { len })),
  omittedItems: (count, breakdown) => String(tr('omittedItems', { count, breakdown }))
}))
const session = computed(() => detail.value?.session)
const orderedAttempts = computed(() => [...(detail.value?.attempts || [])].sort((a, b) => a.attempt_seq - b.attempt_seq))
const selected = computed(() => orderedAttempts.value.find(item => item.attempt_seq === selectedSeq.value) || null)
const outboundUA = computed(() => {
  const headers = selected.value?.headers
  if (!Array.isArray(headers)) return ''
  return headers.filter(item => item && typeof item.name === 'string' && item.name.toLowerCase() === 'user-agent')
    .map(item => String(item.value ?? '')).join(', ')
})
const comparable = computed(() => !!detail.value?.inbound && !!selected.value)
const truncated = computed(() => !!detail.value?.inbound?.truncated || !!selected.value?.truncated)
const caller = computed(() => {
  const row = session.value
  if (!row) return ''
  if (!row.api_key_id && !row.api_key_name) return row.username || tr('dash')
  return 'key#' + row.api_key_id + (row.api_key_name ? ' ' + row.api_key_name : '')
})

const headerLines = computed(() => visible(headersOf()))
const bodyLines = computed(() => visible(bodyOf()))

function headersOf(): AlignedLine[] {
  if (comparable.value && detail.value?.inbound) return alignHeaders(detail.value.inbound.headers, selected.value?.headers)
  if (detail.value?.inbound) return alignHeaders(detail.value.inbound.headers, []).map(line => ({ ...line, kind: 'same' as const }))
  if (selected.value) return alignHeaders([], selected.value.headers).map(line => ({ ...line, kind: 'same' as const }))
  return []
}
function bodyOf(): AlignedLine[] {
  if (comparable.value && detail.value?.inbound) return alignDocuments(detail.value.inbound.body ?? null, selected.value?.body ?? null, labels.value)
  if (detail.value?.inbound) return presentDocument(detail.value.inbound.body ?? null, labels.value, 'left')
  if (selected.value) return presentDocument(selected.value.body ?? null, labels.value, 'right')
  return []
}
function visible(lines: AlignedLine[]) {
  if (!comparable.value || !onlyDiff.value) return lines
  return lines.filter(line => line.kind !== 'same')
}
function sideKind(line: AlignedLine, side: 'left' | 'right') {
  const text = side === 'left' ? line.left : line.right
  if (text == null) return 'pad'
  return line.kind
}
function lineClass(line: AlignedLine, side: 'left' | 'right') {
  if (!comparable.value || sideKind(line, side) === 'pad' || line.kind === 'same') return ''
  if (line.kind === 'add') return 'border-emerald-500 bg-emerald-50 text-emerald-900'
  if (line.kind === 'del') return 'border-red-500 bg-red-50 text-red-900'
  return 'border-amber-400 bg-amber-50 text-amber-900'
}
function statusClass(status: number) {
  if (status === 429) return 'bg-amber-100 text-amber-700'
  if (status >= 200 && status < 300) return 'bg-emerald-100 text-emerald-700'
  if (status >= 400) return 'bg-red-100 text-red-700'
  return 'bg-gray-100 text-gray-600'
}
function reasonLabel(reason: string) {
  return reason ? tr('reason_' + reason) : tr('dash')
}
function back() {
  void router.push({ name: 'AdminAnthropicRequests' })
}
async function copySide(side: 'inbound' | 'outbound') {
  const capture: AnthropicCapture | null | undefined = side === 'inbound' ? detail.value?.inbound : selected.value
  if (!capture) return
  await navigator.clipboard.writeText(storedDocument(capture.headers, capture.body))
}
function exportDetail() {
  const blob = new Blob([JSON.stringify(detail.value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = (session.value?.client_request_id || 'anthropic-request') + '.json'
  link.click()
  URL.revokeObjectURL(url)
}
function pick(attempts: AnthropicCapture[]) {
  const ordered = [...attempts].sort((a, b) => a.attempt_seq - b.attempt_seq)
  const success = [...ordered].reverse().find(item => item.status >= 200 && item.status < 300)
  return (success || ordered[ordered.length - 1])?.attempt_seq || 0
}
onMounted(async () => {
  try {
    const result = await getAnthropicSession(String(route.params.clientRequestId || ''))
    detail.value = result
    selectedSeq.value = pick(result.attempts || [])
  } catch {
    error.value = tr('loadFailed')
  }
})
</script>
