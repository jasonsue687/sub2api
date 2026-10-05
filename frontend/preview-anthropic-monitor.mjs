// Loopback-only UI preview. All records are synthetic; no production API is used.
import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'

const root = dirname(fileURLToPath(import.meta.url))
const temp = await mkdtemp(resolve(tmpdir(), 'sub2api-anthropic-preview-'))
const fsURL = path => '/@fs' + path
const layout = resolve(temp, 'Layout.vue')
await writeFile(layout, `<template><div class="min-h-screen bg-gray-50 text-gray-900"><div class="bg-amber-100 px-6 py-3 text-sm text-amber-900">本地演示 · 全部为合成数据 · 未连接生产环境</div><main class="mx-auto max-w-[1600px] p-6"><slot /></main></div></template>`)
const redacted = (len, sha) => ({ _redacted: true, len, ...(sha ? { sha256: sha } : {}) })
const body = (model, extra = {}) => ({
  model,
  max_tokens: 32000,
  stream: true,
  system: [{ type: 'text', text: 'x-anthropic-billing-header: cc_version=2.0.14' }, { type: 'text', text: redacted(80, 'aa11bb22cc33dd44') }],
  messages: [{ role: 'user', content: [{ type: 'text', text: redacted(1234, 'sha256placeholder00') }] }],
  tools: Array.from({ length: 18 }, () => ({ type: 'custom', name: redacted(6) })),
  metadata: { user_id: 'device:sha256:demo account:sha256:demo session:sha256:demo' },
  ...extra
})
const headers = (extra = []) => ([
  { name: 'accept', value: 'application/json' },
  { name: 'anthropic-version', value: '2023-06-01' },
  { name: 'x-api-key', value: '***' },
  ...extra
])
const rows = [
  { client_request_id: 'c7f3a1e2abcd', request_id: 'req-1', created_at: '2026-10-03T13:14:07Z', api_key_id: 12, api_key_name: '研发共享', username: 'dev-alice', user_id: 3, endpoint: '/v1/messages', inbound_model: 'claude-sonnet-4-5', outbound_model: 'claude-sonnet-4-5-20250929', stream: true, status: 200, account_id: 2, account_name: 'oauth-acc-02', attempt_count: 1, duration_ms: 2410, has_inbound: true, has_outbound: true, error_class: '', consistency: 'matched', truncated: false },
  { client_request_id: '9b2e47d0ffff', request_id: 'req-2', created_at: '2026-10-03T13:13:58Z', api_key_id: 7, api_key_name: 'CI 流水线', username: 'ci-bot', user_id: 4, endpoint: '/v1/messages', inbound_model: 'claude-opus-4-1', outbound_model: 'claude-opus-4-1-20250805', stream: true, status: 200, account_id: 5, account_name: 'oauth-acc-05', attempt_count: 3, duration_ms: 5620, has_inbound: true, has_outbound: true, error_class: '', consistency: 'matched', truncated: false },
  { client_request_id: 'e58d3b71aaaa', request_id: 'req-3', created_at: '2026-10-03T13:13:44Z', api_key_id: 3, api_key_name: '产品试用', username: 'pm-carol', user_id: 8, endpoint: '/v1/chat/completions', inbound_model: '', outbound_model: 'claude-sonnet-4-5-20250929', stream: true, status: 200, account_id: 4, account_name: 'oauth-acc-04', attempt_count: 1, duration_ms: 3070, has_inbound: false, has_outbound: true, error_class: '', consistency: 'unknown', truncated: false },
  { client_request_id: 'b90e6d24bbbb', request_id: 'req-4', created_at: '2026-10-03T13:12:40Z', api_key_id: 3, api_key_name: '产品试用', username: 'pm-carol', user_id: 8, endpoint: '/v1/messages', inbound_model: 'claude-opus-4-1', outbound_model: '', stream: true, status: 503, account_id: 0, account_name: '', attempt_count: 0, duration_ms: 20, has_inbound: true, has_outbound: false, error_class: 'unavailable', consistency: '', truncated: false }
]
const capture = (patch) => ({
  direction: 'outbound', client_request_id: '9b2e47d0ffff', request_id: 'req-2', account_id: 5, account_name: 'oauth-acc-05', user_id: 4, api_key_id: 7, api_key_name: 'CI 流水线', username: 'ci-bot', endpoint: '/v1/messages', client_path: '/v1/messages', model: 'claude-opus-4-1-20250805', stream: true, attempt_seq: 3, retry_reason: 'account_switch', account_switch_count: 1, attempt_count: 3, status: 200, error_class: '', upstream_request_id: 'req_up_3', duration_ms: 5620, headers_ms: 710, headers: headers([{ name: 'x-anthropic-billing-header', value: 'cc_version=2.0.14' }]), body: body('claude-opus-4-1-20250805', { max_tokens: 64000 }), body_state: 'parsed', consistency: 'matched', truncated: false, original_bytes: 900, created_at: '2026-10-03T13:13:58Z', ...patch
})
const api = resolve(temp, 'api.ts')
await writeFile(api, `
const rows = ${JSON.stringify(rows)}
export async function listAnthropicRequests(query) { return { records: [{ id: 123, created_at: rows[0].created_at, request_id: 'demo-history', client_request_id: 'demo-client', audit: { schema: 1, attempt_id: 'demo-attempt', started_at: rows[0].created_at, endpoint: '/v1/messages', origin: { source: 'gateway', subscription: true, mimic: false }, headers: { 'user-agent': 'claude-cli/2.1.283 (external, cli)' }, parameters: { model: 'claude-sonnet-4-5', stream: false }, body_state: 'parsed', cc_entrypoint: 'local-agent', cc_version: '2.1.283', consistency: 'mismatch', issues: ['entrypoint_mismatch'], status: 200, headers_ms: 710 } }], summary: { attempts: 1, correlated_requests: 1, uncorrelated_attempts: 0, matched: 0, mismatched: 1, unknown: 0, http_failures: 0, transport_errors: 0, identity_variants: 1, parameter_variants: 1 }, variants: [], total: 1, page: 1, page_size: 50, account_id: query.account_id, start_time: query.start_time, end_time: query.end_time, collection_enabled: true, sink_health: { queue_capacity: 512, queue_depth: 0, dropped_count: 0, write_failed_count: 0, written_count: 1, avg_write_delay_ms: 0, last_error: '' } } }
export async function listAnthropicSessions(query) {
  let records = rows
  if (query.only_multi) records = records.filter(row => row.attempt_count > 1)
  if (query.only_failed) records = records.filter(row => row.status >= 400 || row.error_class)
  if (query.q) records = records.filter(row => (row.client_request_id + row.request_id + row.api_key_name + row.username).includes(query.q))
  return { summary: { inbound_requests: 1284, outbound_attempts: 1327, multi_attempts: 31, failed_requests: 12 }, records, total: records.length, page: 1, page_size: 50, accounts: [{ id: 2, name: 'oauth-acc-02' }, { id: 5, name: 'oauth-acc-05' }], models: ['claude-sonnet-4-5', 'claude-opus-4-1'], start_time: query.start_time, end_time: query.end_time, inbound_enabled: true, capture_health: { queue_capacity: 512, queue_depth: 0, dropped_count: 0, write_failed_count: 0, written_count: 4 } }
}
export async function getAnthropicSession(id) {
  if (id.startsWith('b90e6d24')) {
    return { session: rows[3], inbound: ${JSON.stringify(capture({ direction: 'inbound', client_request_id: 'b90e6d24bbbb', model: 'claude-opus-4-1', body: body('claude-opus-4-1'), account_name: '', status: 503 }))}, attempts: [] }
  }
  if (id.startsWith('e58d3b71')) {
    return { session: rows[2], inbound: null, attempts: [${JSON.stringify(capture({ client_request_id: 'e58d3b71aaaa', client_path: '/v1/chat/completions', attempt_seq: 1, retry_reason: 'initial', account_name: 'oauth-acc-04', status: 200 }))}] }
  }
  if (id.startsWith('9b2e47d0')) {
    const session = rows[1]
    return { session, inbound: ${JSON.stringify(capture({ direction: 'inbound', model: 'claude-opus-4-1', body: body('claude-opus-4-1') }))}, attempts: [
      ${JSON.stringify(capture({ attempt_seq: 1, status: 429, retry_reason: 'initial', account_name: 'oauth-acc-01', upstream_request_id: 'req_up_1', body: body('claude-opus-4-1') }))},
      ${JSON.stringify(capture({ attempt_seq: 2, status: 529, retry_reason: 'account_switch', account_name: 'oauth-acc-03', upstream_request_id: 'req_up_2', body: body('claude-opus-4-1') }))},
      ${JSON.stringify(capture({ attempt_seq: 3, status: 200, retry_reason: 'account_switch', account_name: 'oauth-acc-05', upstream_request_id: 'req_up_3', body: body('claude-opus-4-1-20250805', { max_tokens: 64000 }) }))}
    ] }
  }
  return { session: rows[0], inbound: ${JSON.stringify(capture({ direction: 'inbound', client_request_id: 'c7f3a1e2abcd', model: 'claude-sonnet-4-5', body: body('claude-sonnet-4-5'), account_name: 'oauth-acc-02', attempt_seq: 0 }))}, attempts: [${JSON.stringify(capture({ client_request_id: 'c7f3a1e2abcd', attempt_seq: 1, retry_reason: 'initial', account_name: 'oauth-acc-02', model: 'claude-sonnet-4-5-20250929', body: body('claude-sonnet-4-5-20250929') }))}] }
}
`)
const entry = resolve(temp, 'entry.ts')
await writeFile(entry, `import {createApp, h} from 'vue';import {createI18n} from 'vue-i18n';import {createRouter,createWebHistory,RouterView} from 'vue-router';
import List from '${fsURL(resolve(root, 'src/views/admin/AnthropicRequestsView.vue'))}';
import Detail from '${fsURL(resolve(root, 'src/views/admin/AnthropicRequestDetailView.vue'))}';
import Outbound from '${fsURL(resolve(root, 'src/views/admin/AnthropicOutboundAuditView.vue'))}';
import outboundZh from '${fsURL(resolve(root, 'src/i18n/locales/zh/admin/anthropicOutbound.ts'))}';
import zh from '${fsURL(resolve(root, 'src/i18n/locales/zh/admin/anthropicRequests.ts'))}';
import '${fsURL(resolve(root, 'src/style.css'))}';
const router=createRouter({history:createWebHistory(),routes:[
  {path:'/',name:'AdminAnthropicRequests',component:List},
  {path:'/detail/:clientRequestId',name:'AdminAnthropicRequestDetail',component:Detail},
  {path:'/outbound',name:'AdminAnthropicOutboundAudit',component:Outbound}
]});
createApp({render:()=>h(RouterView)}).use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh:{admin:{...zh,...outboundZh}}}})).mount('#app');`)
const server = await createServer({
  configFile: false, root, plugins: [vue(), { name: 'preview-entry', configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (req.url !== '/' && !req.url.startsWith('/detail/') && !req.url.startsWith('/outbound')) return next()
      res.setHeader('Content-Type', 'text/html; charset=utf-8')
      res.end(`<!doctype html><html lang="zh"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Anthropic 请求监控 · 本地演示</title></head><body><div id="app"></div><script type="module" src="${fsURL(entry)}"></script></body></html>`)
    })
  } }],
  resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: layout }, { find: '@/api/admin/anthropicRequests', replacement: api }, { find: '@', replacement: resolve(root, 'src') }] },
  server: { host: '127.0.0.1', port: 5178, strictPort: true, fs: { allow: [root, temp] } }
})
await server.listen()
server.printUrls()
setInterval(() => {}, 1 << 30)
