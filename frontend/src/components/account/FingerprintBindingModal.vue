<template>
  <BaseDialog :show="show" :show-close-button="!saving" :title="t('admin.fingerprints.bindingTitle')" @close="!saving && emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.fingerprints.stagedNotice') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="account" class="font-medium">#{{ account.id }} · {{ account.name }}</p>
      <template v-else>
        <form class="flex gap-2" @submit.prevent="accountPage = 1; loadAccounts()">
          <input v-model="accountSearch" class="input min-w-0 flex-1" :aria-label="t('admin.fingerprints.searchAccount')" :placeholder="t('admin.fingerprints.searchAccount')" />
          <button class="btn btn-secondary" :disabled="busy">{{ t('common.search') }}</button>
        </form>
        <label class="block space-y-1 text-sm">
          <span>{{ t('admin.fingerprints.account') }}</span>
          <select v-model="accountId" class="input w-full" data-testid="binding-account" :disabled="busy">
            <option :value="null">{{ t('admin.fingerprints.selectAccount') }}</option>
            <option v-for="item in accountOptions" :key="item.id" :value="item.id">#{{ item.id }} · {{ item.name }} · {{ item.fingerprint_id ? '#' + item.fingerprint_id : t('admin.fingerprints.unbound') }}</option>
          </select>
        </label>
        <div class="flex justify-between">
          <button class="btn btn-secondary btn-sm" :disabled="busy || accountPage <= 1" @click="accountPage--; loadAccounts()">{{ t('pagination.previous') }}</button>
          <button class="btn btn-secondary btn-sm" :disabled="busy || accountPage * 20 >= accountTotal" @click="accountPage++; loadAccounts()">{{ t('pagination.next') }}</button>
        </div>
      </template>
      <p v-if="accountId" class="text-sm">{{ t('admin.fingerprints.currentBinding') }}: {{ current ? '#' + current.id + ' · ' + current.fingerprint.UserAgent : t('admin.fingerprints.unbound') }}</p>
      <template v-if="fingerprint">
        <p class="break-words text-sm">{{ t('admin.fingerprints.selectedFingerprint') }}: #{{ fingerprint.id }} · {{ fingerprint.fingerprint.UserAgent }}</p>
      </template>
      <template v-else>
        <form class="flex gap-2" @submit.prevent="fingerprintPage = 1; loadFingerprints()">
          <input v-model="fingerprintSearch" class="input min-w-0 flex-1" :aria-label="t('admin.fingerprints.search')" :placeholder="t('admin.fingerprints.search')" />
          <button class="btn btn-secondary" :disabled="busy">{{ t('common.search') }}</button>
        </form>
        <label class="block space-y-1 text-sm">
          <span>{{ t('admin.fingerprints.selectedFingerprint') }}</span>
          <select v-model="fingerprintId" class="input w-full" data-testid="binding-fingerprint" :disabled="busy || !bindingLoaded">
            <option :value="null">{{ t('admin.fingerprints.unbound') }}</option>
            <option v-for="item in fingerprintOptions" :key="item.id" :value="item.id">#{{ item.id }} · {{ item.fingerprint.UserAgent }} · {{ item.fingerprint.StainlessOS }} · {{ item.fingerprint.ClientID.slice(0, 12) }}</option>
          </select>
        </label>
        <div class="flex justify-between">
          <button class="btn btn-secondary btn-sm" :disabled="busy || fingerprintPage <= 1" @click="fingerprintPage--; loadFingerprints()">{{ t('pagination.previous') }}</button>
          <button class="btn btn-secondary btn-sm" :disabled="busy || fingerprintPage * 20 >= fingerprintTotal" @click="fingerprintPage++; loadFingerprints()">{{ t('pagination.next') }}</button>
        </div>
      </template>
    </div>
    <template #footer>
      <button class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button class="btn btn-primary" data-testid="save-binding" :disabled="busy || !accountId || !bindingLoaded" @click="save">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { bindFingerprint, getFingerprintBinding, listFingerprintAccounts, listFingerprints, type FingerprintAccount, type FingerprintRecord } from '@/api/admin/accountFingerprints'

const props = defineProps<{ show: boolean; account?: { id: number; name: string } | null; fingerprint?: FingerprintRecord | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()
const { t } = useI18n()
const accounts = ref<FingerprintAccount[]>([])
const selectedAccount = ref<FingerprintAccount | null>(null)
const fingerprints = ref<FingerprintRecord[]>([])
const current = ref<FingerprintRecord | null>(null)
const selected = ref<FingerprintRecord | null>(null)
const accountId = ref<number | null>(null)
const fingerprintId = ref<number | null>(null)
const accountSearch = ref('')
const fingerprintSearch = ref('')
const accountPage = ref(1)
const fingerprintPage = ref(1)
const accountTotal = ref(0)
const fingerprintTotal = ref(0)
const pending = ref(0)
const saving = ref(false)
const error = ref('')
const bindingLoaded = ref(false)
const busy = computed(() => pending.value > 0 || saving.value)
const accountOptions = computed(() => selectedAccount.value && !accounts.value.some(item => item.id === selectedAccount.value?.id) ? [selectedAccount.value, ...accounts.value] : accounts.value)
const fingerprintOptions = computed(() => {
  const items = [...fingerprints.value]
  for (const item of [current.value, selected.value]) {
    if (item && !items.some(option => option.id === item.id)) items.unshift(item)
  }
  return items
})
watch(fingerprintId, id => { selected.value = fingerprintOptions.value.find(item => item.id === id) ?? null })
let generation = 0

async function loadAccounts() {
  const run = generation
  pending.value++
  try {
    const data = await listFingerprintAccounts(accountPage.value, accountSearch.value)
    if (run === generation) { accounts.value = data.items; accountTotal.value = data.total }
  } catch { if (run === generation) error.value = t('admin.fingerprints.loadFailed') }
  finally { pending.value-- }
}
async function loadFingerprints() {
  const run = generation
  pending.value++
  try {
    const data = await listFingerprints(fingerprintPage.value, fingerprintSearch.value)
    if (run === generation) { fingerprints.value = data.items; fingerprintTotal.value = data.total }
  } catch { if (run === generation) error.value = t('admin.fingerprints.loadFailed') }
  finally { pending.value-- }
}
watch(accountId, async id => {
  const run = generation
  selectedAccount.value = accountOptions.value.find(item => item.id === id) ?? null
  current.value = null
  selected.value = null
  bindingLoaded.value = false
  if (!id) return
  pending.value++
  try {
    const result = await getFingerprintBinding(id)
    if (run !== generation || accountId.value !== id) return
    current.value = result.fingerprint
    fingerprintId.value = props.fingerprint?.id ?? current.value?.id ?? null
    bindingLoaded.value = true
  } catch { if (run === generation) error.value = t('admin.fingerprints.loadFailed') }
  finally { pending.value-- }
})
watch(() => props.show, async show => {
  generation++
  accountId.value = null
  bindingLoaded.value = false
  if (!show) return
  error.value = ''
  current.value = null
  accountSearch.value = fingerprintSearch.value = ''
  accountPage.value = fingerprintPage.value = 1
  fingerprints.value = []
  accounts.value = []
  fingerprintId.value = props.fingerprint?.id ?? null
  // Yield so reopening the same account still triggers the account watcher.
  await Promise.resolve()
  if (!props.show) return
  accountId.value = props.account?.id ?? null
  if (!props.account) void loadAccounts()
  if (!props.fingerprint) void loadFingerprints()
}, { immediate: true })
async function save() {
  if (!accountId.value || !bindingLoaded.value || busy.value) return
  saving.value = true
  error.value = ''
  try {
    await bindFingerprint(accountId.value, fingerprintId.value ?? null)
    emit('saved')
    emit('close')
  } catch { error.value = t('admin.fingerprints.saveFailed') }
  finally { saving.value = false }
}
</script>
