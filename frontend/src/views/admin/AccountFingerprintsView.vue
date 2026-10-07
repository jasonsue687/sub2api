<template>
  <AppLayout>
    <div class="space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-2xl font-semibold">{{ t('admin.fingerprints.title') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('admin.fingerprints.description') }}</p></div>
      </header>
      <p class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ t('admin.fingerprints.stagedNotice') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <form class="flex flex-wrap gap-3" @submit.prevent="page = 1; load()">
        <input v-model="search" class="input w-full sm:w-80" :placeholder="t('admin.fingerprints.search')" :aria-label="t('admin.fingerprints.search')" />
        <select v-model="source" class="input" :aria-label="t('admin.fingerprints.source')"><option value="">{{ t('admin.fingerprints.allSources') }}</option><option value="request">{{ t('admin.fingerprints.request') }}</option><option value="cache">{{ t('admin.fingerprints.cache') }}</option></select>
        <button class="btn btn-primary" :disabled="loading">{{ t('common.search') }}</button>
      </form>
      <div class="card overflow-x-auto" :aria-busy="loading">
        <table class="w-full text-left text-sm">
          <thead><tr class="border-b dark:border-dark-700"><th class="p-3">{{ t('admin.fingerprints.identity') }}</th><th class="p-3">{{ t('admin.fingerprints.source') }}</th><th class="p-3">{{ t('admin.fingerprints.observations') }}</th><th class="p-3">{{ t('admin.fingerprints.boundAccounts') }}</th><th class="p-3">{{ t('common.actions') }}</th></tr></thead>
          <tbody class="divide-y dark:divide-dark-700">
            <tr v-for="item in items" :key="item.id" class="align-top">
              <td class="max-w-md space-y-1 break-words p-3">
                <p class="font-medium">#{{ item.id }} · {{ item.fingerprint.UserAgent }}</p>
                <p class="text-xs text-gray-500">{{ item.fingerprint.StainlessOS }} / {{ item.fingerprint.StainlessArch }} · SDK {{ item.fingerprint.StainlessPackageVersion }} · {{ item.fingerprint.StainlessRuntime }} {{ item.fingerprint.StainlessRuntimeVersion }}</p>
                <p class="font-mono text-xs" :title="item.fingerprint.ClientID">{{ t('admin.fingerprints.device') }}: {{ item.fingerprint.ClientID.slice(0, 16) }}…</p>
                <p class="text-xs text-gray-500">{{ t(originKeys[item.client_id_origin]) }}</p>
                <details><summary class="cursor-pointer text-primary-600">{{ t('admin.fingerprints.details') }}</summary><dl class="mt-2 space-y-1 text-xs"><template v-for="(value, key) in item.fingerprint" :key="key"><dt class="font-medium">{{ key }}</dt><dd class="break-all font-mono">{{ value }}</dd></template></dl><template v-if="item.source === 'request'"><p class="mt-3 font-medium">{{ t('admin.fingerprints.incoming') }}</p><pre class="mt-1 whitespace-pre-wrap break-all text-xs">{{ JSON.stringify(item.incoming_headers, null, 2) }}</pre></template></details>
              </td>
              <td class="p-3">{{ t('admin.fingerprints.' + item.source) }}<span v-if="item.source_account_id" class="mt-1 block text-xs">{{ t('admin.fingerprints.account') }} #{{ item.source_account_id }}</span></td>
              <td class="space-y-1 whitespace-nowrap p-3 text-xs"><p>{{ t('admin.fingerprints.count', { count: item.request_count }) }}</p><p>{{ t('admin.fingerprints.firstSeen') }}: {{ formatTime(item.first_seen_at) }}</p><p>{{ t('admin.fingerprints.lastSeen') }}: {{ formatTime(item.last_seen_at) }}</p></td>
              <td class="p-3"><p v-if="!item.bound_accounts.length" class="text-gray-500">{{ t('admin.fingerprints.unbound') }}</p><button v-for="account in item.bound_accounts" :key="account.id" class="mb-2 block text-primary-600 hover:underline" @click="bindingAccount = account; bindingFingerprint = null; showBinding = true">#{{ account.id }} · {{ account.name }}</button></td>
              <td class="p-3"><button class="btn btn-secondary btn-sm whitespace-nowrap" @click="bindingAccount = null; bindingFingerprint = item; showBinding = true">{{ t('admin.fingerprints.bindAccount') }}</button></td>
            </tr>
            <tr v-if="!items.length"><td colspan="5" class="p-10 text-center text-gray-500">{{ t(loading ? 'common.loading' : 'admin.fingerprints.empty') }}</td></tr>
          </tbody>
        </table>
        <Pagination :page="page" :total="total" :page-size="20" :show-page-size-selector="false" @update:page="page = $event; load()" />
      </div>
    </div>
    <FingerprintBindingModal :show="showBinding" :account="bindingAccount" :fingerprint="bindingFingerprint" @close="showBinding = false" @saved="load" />
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import FingerprintBindingModal from '@/components/account/FingerprintBindingModal.vue'
import { listFingerprints, type FingerprintAccount, type FingerprintRecord } from '@/api/admin/accountFingerprints'
const { t } = useI18n()
const originKeys = { client: 'admin.fingerprints.origin_client', generated: 'admin.fingerprints.origin_generated', cache: 'admin.fingerprints.origin_cache' } as const
const items = ref<FingerprintRecord[]>([])
const total = ref(0)
const page = ref(1)
const search = ref('')
const source = ref('')
const loading = ref(false)
const error = ref('')
const showBinding = ref(false)
const bindingAccount = ref<FingerprintAccount | null>(null)
const bindingFingerprint = ref<FingerprintRecord | null>(null)
let request = 0
const formatTime = (value: string) => new Date(value).toLocaleString()
async function load() {
  const run = ++request
  loading.value = true
  error.value = ''
  try {
    const result = await listFingerprints(page.value, search.value, source.value)
    if (run === request) { items.value = result.items; total.value = result.total }
  } catch { if (run === request) error.value = t('admin.fingerprints.loadFailed') }
  finally { if (run === request) loading.value = false }
}
onMounted(load)
</script>
