<template>
  <div class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <p class="text-sm font-medium">{{ t('admin.accounts.reverse.operations') }}</p>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-secondary" :disabled="!!busy" @click="loadQuota">{{ t('admin.accounts.reverse.queryQuota') }}</button>
      <button type="button" class="btn btn-secondary" :disabled="!!busy" @click="checkin">{{ t('admin.accounts.reverse.checkinNow') }}</button>
      <button v-if="workbuddy" type="button" class="btn btn-secondary" :disabled="!!busy" @click="loadTasks">{{ t('admin.accounts.reverse.scanTasks') }}</button>
    </div>
    <p v-if="busy" class="text-xs text-primary-600" role="status">{{ t('admin.accounts.reverse.operationRunning') }}</p>
    <p v-if="error" class="text-xs text-red-600" role="alert">{{ error }}</p>
    <p v-if="notice" class="text-xs text-gray-600 dark:text-gray-400">{{ notice }}</p>

    <div v-if="quota" class="space-y-2 rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-700">
      <p v-if="quota.subscription?.tierName" class="font-medium">{{ quota.subscription.tierName }}</p>
      <div class="grid gap-2 sm:grid-cols-2">
        <p v-for="metric in quota.summary" :key="metric.key">{{ metric.label }}: {{ formatMetric(metric.value, metric.unit) }}</p>
      </div>
      <div v-for="(group, index) in quota.groups" :key="index" class="space-y-1 border-t border-gray-200 pt-2 dark:border-dark-600">
        <p class="font-medium">{{ group.displayName }}</p>
        <div v-for="(bucket, bucketIndex) in group.buckets" :key="bucketIndex">
          <p>{{ bucket.description || bucket.window }}</p>
          <p v-if="bucket.resetTime" class="text-gray-500">{{ bucket.resetTime }}</p>
        </div>
      </div>
    </div>

    <div v-if="tasks" class="space-y-3">
      <p class="input-hint">{{ t('admin.accounts.reverse.taskChatHint') }}</p>
      <button v-if="platform.endsWith('_cn')" type="button" class="btn btn-secondary" :disabled="!!busy" @click="run({ action: 'all' })">{{ t('admin.accounts.reverse.runAllTasks') }}</button>
      <div v-for="spec in tasks.specs" :key="spec.kind" class="flex items-center justify-between gap-3 rounded border border-gray-200 p-3 text-xs dark:border-dark-600">
        <div>
          <p class="font-medium">{{ spec.label }} <span v-if="spec.uses_chat" class="text-amber-600">{{ t('admin.accounts.reverse.usesChat') }}</span></p>
          <p class="mt-1 text-gray-500">{{ spec.description }}</p>
          <p class="mt-1 text-gray-500">{{ spec.after_checkin ? t('admin.accounts.reverse.afterCheckin') : `${(spec.default_hours || []).map(hour => `${hour}:00`).join(' / ')} (UTC+8)` }}</p>
        </div>
        <button type="button" class="btn btn-secondary shrink-0" :disabled="!!busy" @click="run({ action: 'scheduled', kind: spec.kind })">{{ t('admin.accounts.reverse.runTask') }}</button>
      </div>
      <div v-for="item in tasks.inspection.items || []" :key="item.code" class="flex items-center justify-between gap-3 text-xs">
        <span>{{ item.title }} · {{ item.current }}/{{ item.target }} <span v-if="item.uses_chat" class="text-amber-600">{{ t('admin.accounts.reverse.usesChat') }}</span></span>
        <button type="button" class="btn btn-secondary shrink-0" :disabled="!!busy" @click="run({ action: 'run', code: item.run_code || item.code })">{{ t('admin.accounts.reverse.runTask') }}</button>
      </div>
      <p v-if="tasks.status.error" class="text-xs text-gray-500">{{ tasks.status.error }}</p>
    </div>

    <div v-if="outcome" class="space-y-1 rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-700">
      <p :class="outcome.success ? 'text-emerald-600' : 'text-amber-600'">{{ t(outcome.success ? 'admin.accounts.reverse.operationSuccess' : 'admin.accounts.reverse.operationFailed') }} · {{ outcome.finished_at }}</p>
      <p v-if="outcome.message">{{ outcome.message }}</p>
      <p v-for="(item, index) in outcome.results" :key="index">{{ item.code }} · {{ item.status }} · {{ item.message }} <span v-if="item.credit || item.energy">(+{{ item.credit }} / +{{ item.energy }})</span></p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { checkinAccount, getReverseQuota, getReverseTasks, runReverseTask } from '@/api/admin/accounts'
import type { ReverseQuota, ReverseTasksOverview, ReverseTaskOutcome, ReverseTaskRequest } from '@/api/admin/accounts'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ accountId: number; platform: string }>()
const { t } = useI18n()
const workbuddy = computed(() => props.platform.startsWith('workbuddy_'))
const quota = ref<ReverseQuota | null>(null)
const tasks = ref<ReverseTasksOverview | null>(null)
const outcome = ref<ReverseTaskOutcome | null>(null)
const busy = ref('')
const error = ref('')
const notice = ref('')
let generation = 0

function formatMetric(value: number, unit?: string) {
  if (unit === 'unix') return new Date(value * 1000).toLocaleString()
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 2 })}${unit ? ` ${unit}` : ''}`
}

async function execute<T>(kind: string, action: (accountId: number) => Promise<T>, commit: (result: T) => void) {
  if (busy.value) return
  const accountId = props.accountId
  const operationGeneration = generation
  busy.value = kind
  error.value = ''
  notice.value = ''
  try {
    const result = await action(accountId)
    if (operationGeneration === generation) commit(result)
  } catch (err) {
    if (operationGeneration === generation) error.value = extractApiErrorMessage(err, t('admin.accounts.reverse.operationFailed'))
  } finally { if (operationGeneration === generation) busy.value = '' }
}

function loadQuota() { return execute('quota', getReverseQuota, result => { quota.value = result }) }
function loadTasks() { return execute('tasks', getReverseTasks, result => {
  tasks.value = result
  outcome.value = result.last_run || null
}) }
function checkin() { return execute('checkin', checkinAccount, result => {
  if (result.result?.success) notice.value = result.result.message || t('admin.accounts.reverse.checkinSuccess')
  else error.value = result.result?.message || t('admin.accounts.reverse.checkinFailed')
}) }
function run(request: ReverseTaskRequest) { return execute('run', accountId => runReverseTask(accountId, request), result => { outcome.value = result }) }

watch(() => [props.accountId, props.platform], () => { generation += 1; quota.value = null; tasks.value = null; outcome.value = null; error.value = ''; notice.value = ''; busy.value = '' })
onBeforeUnmount(() => { generation += 1 })
</script>
