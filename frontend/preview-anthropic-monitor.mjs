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
await writeFile(layout, `<template><div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-white"><div class="bg-amber-100 px-6 py-3 text-sm text-amber-900">本地演示 · 全部为合成数据 · 未连接生产环境</div><main class="mx-auto max-w-[1600px] p-6"><slot /></main></div></template>`)
const api = resolve(temp, 'api.ts')
await writeFile(api, `
const now = Date.now()
const records = [
 ['cli', 'local-agent', 'mismatch', 200, false, 'claude-sonnet-4-5', 2048],
 ['local-agent', 'local-agent', 'matched', 200, false, 'claude-sonnet-4-5', 4096],
 ['cli', 'cli', 'matched', 429, true, 'claude-opus-4-1', 8192],
 ['custom', '', 'unknown', 0, false, 'claude-sonnet-4-5', 2048],
].map(([ua, entry, consistency, status, mimic, model, max_tokens], i) => ({
 id:104-i,created_at:new Date(now-i*60000).toISOString(),request_id:'demo-request-'+i,client_request_id:i<2?'demo-inbound-1':'demo-inbound-'+i,
 audit:{schema:1,attempt_id:'demo-attempt-'+i,started_at:new Date(now-i*60000-1300).toISOString(),endpoint:'/v1/messages',origin:{source:'gateway',subscription:true,mimic},
 headers:{'user-agent':ua==='custom'?'demo-client/1':'claude-cli/2.1.283 (external, '+ua+')','anthropic-version':'2023-06-01','x-stainless-runtime':'node','x-stainless-os':'Linux'},
 parameters:{model,max_tokens,stream:true,temperature:1,'thinking.type':'enabled','thinking.budget_tokens':1024,messages_count:3,tools_count:2},
 body_state:'parsed',cc_version:entry?'2.1.283.abc':undefined,cc_entrypoint:entry||undefined,device_hash:'demo-device-hash-'+i,account_hash:'demo-account-hash',session_hash:'demo-session-hash-'+i,
 identity_signature:'demo-identity-'+i,parameter_signature:'demo-params-'+i,consistency,issues:consistency==='mismatch'?['entrypoint_mismatch']:[],status,error_class:status===0?'timeout':undefined,upstream_request_id:status?'demo-upstream-'+i:undefined,headers_ms:1300+i*200}
}))
export async function listAnthropicRequests(query) {
 await new Promise(resolve=>setTimeout(resolve,200))
 const selected=query.only_mismatch?records.filter(r=>r.audit.consistency==='mismatch'):records
 return {summary:{attempts:4,correlated_requests:3,uncorrelated_attempts:0,matched:2,mismatched:1,unknown:1,http_failures:1,transport_errors:1,identity_variants:4,parameter_variants:3},
 variants:records.map(r=>({signature:r.audit.identity_signature,count:1,user_agent:r.audit.headers['user-agent'],entrypoint:r.audit.cc_entrypoint,version:r.audit.cc_version?.slice(0,7),device_hash:r.audit.device_hash,first_seen:r.created_at,last_seen:r.created_at})),
 records:selected,total:selected.length,page:1,page_size:50,account_id:query.account_id,start_time:query.start_time,end_time:query.end_time,collection_enabled:true,
 sink_health:{queue_depth:0,queue_capacity:5000,dropped_count:0,write_failed_count:0,written_count:4,avg_write_delay_ms:2,last_error:''}}
}
`)
const entry = resolve(temp, 'entry.ts')
await writeFile(entry, `import {createApp} from 'vue';import {createI18n} from 'vue-i18n';import {createRouter,createMemoryHistory} from 'vue-router';
import View from '${fsURL(resolve(root, 'src/views/admin/AnthropicRequestsView.vue'))}';
import zh from '${fsURL(resolve(root, 'src/i18n/locales/zh/admin/anthropicRequests.ts'))}';
import '${fsURL(resolve(root, 'src/style.css'))}';
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:View}]});
await router.push('/?account_id=11');createApp(View).use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh:{admin:zh}}})).mount('#app');`)
const server = await createServer({
  configFile: false, root, plugins: [vue(), { name: 'preview-entry', configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (req.url !== '/') return next()
      res.setHeader('Content-Type', 'text/html; charset=utf-8')
      res.end(`<!doctype html><html lang="zh"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Anthropic 请求监控 · 本地演示</title></head><body><div id="app"></div><script type="module" src="${fsURL(entry)}"></script></body></html>`)
    })
  } }],
  resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: layout }, { find: '@/api/admin/anthropicRequests', replacement: api }, { find: '@', replacement: resolve(root, 'src') }] },
  server: { host: '127.0.0.1', port: 5178, strictPort: true, fs: { allow: [root, temp] } }
})
await server.listen(); server.printUrls()
