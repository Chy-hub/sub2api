<template>
  <div class="min-w-60 max-w-80 space-y-1.5" data-testid="task-progress">
    <div class="flex flex-wrap items-center gap-2">
      <span class="inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium" :class="badgeClass" data-testid="task-run-state">
        <span v-if="live && !stale" class="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
        {{ stateLabel }}
      </span>
      <span v-if="progress?.total" class="text-xs tabular-nums text-gray-600 dark:text-gray-300">{{ progress.completed }} / {{ progress.total }}</span>
      <span v-if="elapsed !== null" class="text-xs tabular-nums text-gray-400">{{ t('admin.ustcSpeed.progress.elapsed', { seconds: elapsed }) }}</span>
    </div>
    <template v-if="progress">
      <div v-if="progress.total" role="progressbar" :aria-label="t('admin.ustcSpeed.progress.title')" :aria-valuenow="progress.completed" :aria-valuemax="progress.total" aria-valuemin="0" class="h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-600">
        <div class="h-full rounded-full transition-[width] duration-300" :class="live ? 'bg-primary-500' : 'bg-gray-400 dark:bg-gray-500'" :style="{ width: `${percent}%` }" />
      </div>
      <p v-if="progress.total" class="text-[11px] text-gray-500 dark:text-gray-400">
        {{ t('admin.ustcSpeed.progress.counts', { success: progress.successful, failed: progress.failed, skipped: progress.skipped, cancelled: progress.cancelled }) }}
      </p>
      <template v-if="live && !stale">
        <p v-if="progress.total" class="text-xs text-primary-600 dark:text-primary-300">{{ t('admin.ustcSpeed.progress.working', { active: progress.active.length, pending: Math.max(0, progress.total - progress.completed - progress.active.length) }) }}</p>
        <ul v-if="progress.active.length" class="max-h-24 space-y-0.5 overflow-auto text-[11px] text-gray-500 dark:text-gray-400">
          <li v-for="item in progress.active" :key="`${item.account_id}:${item.model}`" :title="`${item.account_name} · ${item.model}`" class="truncate">
            {{ item.account_name || `#${item.account_id}` }} · {{ item.model }} · {{ t(`admin.ustcSpeed.progress.${item.phase}`) }}
          </li>
        </ul>
        <p v-else-if="progress.state === 'preparing'" class="text-xs text-gray-500">{{ t('admin.ustcSpeed.progress.preparingHint') }}</p>
        <p v-else-if="progress.current_model" class="truncate text-xs text-gray-500">{{ progress.current_model }}</p>
      </template>
    </template>
    <p v-if="detail" class="text-xs text-gray-500 dark:text-gray-400">{{ reasonLabel(detail) }}</p>
    <p v-if="stale" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.ustcSpeed.progress.staleHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { USTCSpeedTask } from '@/api/admin/ustcSpeed'

const props = defineProps<{ task: USTCSpeedTask; now: number; reasonLabel: (reason: string) => string }>()
const { t } = useI18n()
const progress = computed(() => props.task.progress)
const runState = computed(() => progress.value?.finished_at ? progress.value.state : props.task.last_status)
const live = computed(() => props.task.last_status === 'running' && !progress.value?.finished_at)
const stale = computed(() => live.value && (
  (props.task.lease_until && Date.parse(props.task.lease_until) <= props.now) ||
  (progress.value?.updated_at && props.now - Date.parse(progress.value.updated_at) > 15000)
))
const percent = computed(() => progress.value?.total ? Math.min(100, progress.value.completed / progress.value.total * 100) : 0)
const stateLabel = computed(() => {
  if (stale.value) return t('admin.ustcSpeed.progress.stale')
  if (live.value) return t(`admin.ustcSpeed.progress.${progress.value?.state === 'preparing' ? 'preparing' : 'running'}`)
  return runState.value ? t(`admin.ustcSpeed.status.${runState.value}`) : t('admin.ustcSpeed.progress.notStarted')
})
const badgeClass = computed(() => stale.value || ['failed', 'partial'].includes(runState.value)
  ? 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
  : live.value || runState.value === 'completed'
    ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
    : 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300')
const elapsed = computed(() => {
  const start = Date.parse(progress.value?.started_at || props.task.last_run_at || '')
  const end = progress.value?.finished_at ? Date.parse(progress.value.finished_at) : props.now
  return Number.isFinite(start) && (live.value || progress.value?.finished_at) ? Math.max(0, Math.floor((end - start) / 1000)) : null
})
const detail = computed(() => progress.value?.reason || (!live.value ? props.task.last_detail : ''))
</script>
