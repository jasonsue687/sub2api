<template>
  <div>
    <span class="inline-flex rounded-full px-2 py-1 text-xs font-medium" :data-consistency="normalized" :class="badgeClass">{{ t('admin.anthropicOutbound.' + label) }}</span>
    <p v-for="issue in issues || []" :key="issue" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ issueLabel(issue) }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ state?: string; issues?: string[] }>()
const { t } = useI18n()
const normalized = computed(() => props.state === 'matched' || props.state === 'mismatch' ? props.state : 'unknown')
const label = computed(() => normalized.value === 'unknown' ? 'unknownState' : normalized.value)
const badgeClass = computed(() => normalized.value === 'mismatch'
  ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  : normalized.value === 'matched'
    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300')
function issueLabel(issue: string) {
  return ['entrypoint_mismatch', 'version_mismatch', 'multiple_billing_blocks'].includes(issue)
    ? t(`admin.anthropicOutbound.issue_${issue}`)
    : issue
}
</script>
