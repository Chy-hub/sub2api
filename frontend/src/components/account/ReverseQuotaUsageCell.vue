<template>
  <div class="min-w-[220px] space-y-1" data-test="reverse-quota-usage">
    <span
      v-if="quota?.subscription?.tierName || quota?.subscription?.plan"
      class="inline-block rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-600 dark:bg-gray-800 dark:text-gray-300"
      :title="updatedTitle"
    >{{ quota.subscription.tierName || quota.subscription.plan }}</span>

    <div v-for="row in rows" :key="row.key" :data-test="`reverse-quota-${row.key}`" class="space-y-0.5">
      <UsageProgressBar
        v-if="row.percent != null"
        :label="row.label"
        :utilization="row.percent"
        :color="row.key === 'addon' ? 'emerald' : 'indigo'"
        label-width="auto"
        :title="row.text"
      />
      <p class="text-[10px] text-gray-600 dark:text-gray-300">
        <span v-if="row.percent == null">{{ row.label }} · </span>{{ row.text }}
      </p>
    </div>

    <template v-if="!rows.length">
      <div v-for="(group, groupIndex) in quota?.groups || []" :key="groupIndex" class="space-y-1">
        <div v-for="(bucket, index) in group.buckets || []" :key="index">
          <UsageProgressBar
            :label="group.displayName || bucket.window || t('admin.accounts.reverse.quotaCredits')"
            :utilization="(1 - bucket.remainingFraction) * 100"
            :resets-at="bucket.resetTime"
            color="indigo"
            label-width="auto"
          />
          <p v-if="bucket.description" class="text-[10px] text-gray-500">{{ bucket.description }}</p>
        </div>
      </div>
    </template>

    <p v-if="expiresAt" class="text-[10px] text-gray-500" data-test="reverse-quota-expiry">
      {{ t('admin.accounts.reverse.quotaExpiresAt', { time: expiresAt }) }}
    </p>
    <p v-if="exceeded" class="text-[10px] text-red-600">{{ t('admin.accounts.reverse.quotaExceeded') }}</p>
    <p v-if="!quota && loading" class="text-xs text-gray-400" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="quota && !rows.length && !quota.groups?.length" class="text-[10px] text-gray-400">
      {{ t('admin.accounts.reverse.quotaEmpty') }}
    </p>
    <p v-if="error" class="max-w-[260px] truncate text-[10px] text-red-600" role="alert" :title="error">{{ error }}</p>
    <button
      type="button"
      class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
      :disabled="loading"
      :title="updatedTitle"
      data-test="reverse-quota-refresh"
      @click="load(true)"
    >
      <svg class="h-2.5 w-2.5" :class="{ 'animate-spin': loading }" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
      </svg>
      {{ t('admin.accounts.reverse.queryQuota') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { loadReverseQuota, readReverseQuotaSnapshot, reverseQuotaDate, type ReverseQuotaSnapshot } from '@/utils/reverseQuota'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateOnly, formatDateTime } from '@/utils/format'
import UsageProgressBar from './UsageProgressBar.vue'

const props = withDefaults(defineProps<{ account: Account; manualRefreshToken?: number }>(), { manualRefreshToken: 0 })
const { t } = useI18n()
const snapshot = ref<ReverseQuotaSnapshot | null>(null)
const loading = ref(false)
const error = ref('')
let generation = 0

const quota = computed(() => snapshot.value?.quota)
const metric = (key: string): number | undefined => {
  const value = quota.value?.summary?.find(item => item.key === key)?.value
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
const formatNumber = (value: number) => value.toLocaleString(undefined, { maximumFractionDigits: 2 })
const rows = computed(() => [
  { key: 'plan', label: 'quotaPlan', prefix: 'user_quota', remaining: 'user_quota_remaining' },
  { key: 'addon', label: 'quotaAddon', prefix: 'addon_quota', remaining: 'addon_quota_remaining' },
  { key: 'credits', label: 'quotaCredits', prefix: 'credit', remaining: 'credit_remain' }
].flatMap(definition => {
  const used = metric(`${definition.prefix}_used`)
  const total = metric(`${definition.prefix}_total`)
  const remaining = metric(definition.remaining)
  if (used == null && total == null && remaining == null) return []
  const parts: string[] = []
  if (used != null && total != null) {
    parts.push(t('admin.accounts.reverse.quotaUsedTotal', { used: formatNumber(used), total: formatNumber(total) }))
  } else if (total != null) {
    parts.push(t('admin.accounts.reverse.quotaTotal', { total: formatNumber(total) }))
  } else if (used != null) {
    parts.push(t('admin.accounts.reverse.quotaUsed', { used: formatNumber(used) }))
  }
  if (remaining != null) parts.push(t('admin.accounts.reverse.quotaRemaining', { remaining: formatNumber(remaining) }))
  const spent = used ?? (total != null && remaining != null ? Math.max(0, total - remaining) : undefined)
  return [{
    key: definition.key,
    label: t(`admin.accounts.reverse.${definition.label}`),
    percent: total != null && total > 0 && spent != null ? (spent / total) * 100 : null,
    text: parts.join(' · ')
  }]
}))
const expiresAt = computed(() => {
  const date = reverseQuotaDate(metric('expires_at') ?? 0)
  return date ? formatDateOnly(date) : ''
})
const exceeded = computed(() => metric('quota_exceeded') === 1)
const updatedTitle = computed(() => snapshot.value
  ? t('admin.accounts.reverse.quotaUpdatedAt', { time: formatDateTime(snapshot.value.updated_at) })
  : '')

async function load(force = false) {
  if (loading.value && !force) return
  const requestGeneration = ++generation
  loading.value = true
  error.value = ''
  try {
    const result = await loadReverseQuota(props.account, force)
    if (requestGeneration === generation) snapshot.value = result
  } catch (err) {
    if (requestGeneration === generation) error.value = extractApiErrorMessage(err, t('admin.accounts.reverse.quotaLoadFailed'))
  } finally {
    if (requestGeneration === generation) loading.value = false
  }
}

watch(() => [props.account.id, props.account.platform, (props.account.extra?.reverse_quota as ReverseQuotaSnapshot | undefined)?.updated_at], () => {
  generation += 1
  snapshot.value = readReverseQuotaSnapshot(props.account)
  loading.value = false
  error.value = ''
  void load()
}, { immediate: true })
watch(() => props.manualRefreshToken, () => { void load(true) })
onBeforeUnmount(() => { generation += 1 })
</script>
