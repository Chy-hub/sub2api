<template>
  <div>
    <!-- Window stats row (above progress bar) -->
    <div
      v-if="windowStats && (windowStats.requests > 0 || windowStats.tokens > 0)"
      class="mb-0.5 flex items-center"
    >
      <div class="flex items-center gap-1.5 text-[9px] text-gray-500 dark:text-gray-400">
        <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
          {{ formatRequests }} req
        </span>
        <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
          {{ formatTokens }}
        </span>
        <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800" :title="t('usage.accountBilled')">
          A ${{ formatAccountCost }}
        </span>
        <span
          v-if="windowStats?.user_cost != null"
          class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
          :title="t('usage.userBilled')"
        >
          U ${{ formatUserCost }}
        </span>
        <span
          v-if="estimatedTotalCost != null"
          data-test="estimated-total-cost"
          class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
          :title="t('admin.accounts.usageWindow.estimatedTotalCostTooltip')"
        >
          {{ t('admin.accounts.usageWindow.estimatedTotalCost', { cost: estimatedTotalCost.toFixed(2) }) }}
        </span>
      </div>
    </div>

    <!-- Progress bar row -->
    <div class="flex items-center gap-1">
      <!-- Label badge (label-width: fixed = 定宽居中, auto = 限宽截断左对齐) -->
      <span data-test="usage-progress-label" :class="[labelSizeClass, labelClass]">
        {{ label }}
      </span>

      <!-- Progress bar container -->
      <div
        data-test="usage-progress-track"
        class="flex h-1.5 w-8 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-gray-700"
        :title="deadWidth > 0 ? deadTitle : undefined"
      >
        <div
          data-test="usage-progress-live"
          :class="['h-full flex-none transition-all duration-300', barClass]"
          :style="{ width: barWidth }"
        ></div>
        <div
          v-if="deadWidth > 0"
          data-test="usage-progress-dead"
          class="h-full flex-none bg-red-100 dark:bg-red-950/40"
          :style="{ width: `${deadWidth}%`, backgroundImage: DEAD_STRIPES }"
        ></div>
      </div>

      <!-- Percentage -->
      <span :class="['w-[32px] shrink-0 text-right text-[10px] font-medium', textClass]">
        {{ displayPercent }}
      </span>

      <!-- 金额（如 ¥25 / ¥30），可选；定宽等宽数字，保证各行金额/时间列对齐 -->
      <span
        v-if="money"
        data-test="usage-progress-money"
        :class="[
          'shrink-0 truncate whitespace-nowrap tabular-nums text-[10px] text-gray-500 dark:text-gray-400',
          moneyWidthClass,
        ]"
        :title="money"
      >
        {{ money }}
      </span>

      <!-- Reset time -->
      <span
        v-if="shouldShowResetTime"
        data-test="usage-progress-reset"
        class="w-[44px] shrink-0 truncate whitespace-nowrap text-right tabular-nums text-[10px] text-gray-400"
        :title="resetTitle || formatResetTime"
      >
        {{ formatResetTime }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber } from '@/utils/format'

const props = withDefaults(
  defineProps<{
    label: string
    utilization: number // Percentage (0-100+)
    resetsAt?: string | null
    color: 'indigo' | 'emerald' | 'purple' | 'amber'
    windowStats?: WindowStats | null
    estimatedTotalCost?: number | null
    showNowWhenIdle?: boolean
    remainingCapacity?: boolean
    /** fixed: 定宽居中徽章（账号页纵向对齐）；auto: 限宽截断左对齐（监控页组合标签） */
    labelWidth?: 'fixed' | 'auto'
    /** 百分比右侧的金额文本（如 ¥25 / ¥30），可选 */
    money?: string
    /** 剩余额度模式下，剩余部分中重置前用不完的百分比 */
    deadPercent?: number
    /** 废额度悬停说明 */
    deadTitle?: string
    /** 重置时间的悬停文案覆盖 */
    resetTitle?: string
  }>(),
  { labelWidth: 'fixed', deadPercent: 0 }
)

const { t } = useI18n()

// Reactive clock for countdown — only runs when a reset time is shown,
// to avoid creating many idle timers across large account lists.
const now = ref(new Date())
const { pause: pauseClock, resume: resumeClock } = useIntervalFn(
  () => {
    now.value = new Date()
  },
  60_000,
  { immediate: false },
)
if (props.resetsAt) resumeClock()
const refreshClockWhenVisible = () => {
  if (!document.hidden && props.resetsAt) now.value = new Date()
}
onMounted(() => document.addEventListener('visibilitychange', refreshClockWhenVisible))
onUnmounted(() => document.removeEventListener('visibilitychange', refreshClockWhenVisible))
watch(
  () => props.resetsAt,
  (val) => {
    if (val) {
      now.value = new Date()
      resumeClock()
    } else {
      pauseClock()
    }
  },
)

// Label background colors
const labelClass = computed(() => {
  const colors = {
    indigo: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300',
    emerald: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300',
    purple: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
    amber: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  }
  return colors[props.color]
})

// Label badge width mode: fixed 定宽保证账号页纵向对齐；auto 限宽截断适配
// 监控页「Pro/7 天」类组合标签。百分比列在两种模式下保持不变。
const labelSizeClass = computed(() =>
  props.labelWidth === 'auto'
    ? 'max-w-[72px] shrink-0 truncate rounded px-1 text-left text-[10px] font-medium'
    : 'w-[32px] shrink-0 rounded px-1 text-center text-[10px] font-medium'
)

const DEAD_STRIPES =
  'repeating-linear-gradient(45deg, rgb(239 68 68 / 0.85) 0 2px, transparent 2px 4px)'

const trackPercent = computed(() => Math.min(Math.max(props.utilization, 0), 100))

// 废额度段只出现在剩余额度模式，且不能超过剩余部分。
const deadWidth = computed(() =>
  props.remainingCapacity
    ? Math.min(Math.max(props.deadPercent, 0), trackPercent.value)
    : 0
)

// Progress bar color based on utilization
const barClass = computed(() => {
  if (props.remainingCapacity) {
    // 剩余 ≤10% / ≤25% 等价于已用 ≥90% / ≥75%。
    if (trackPercent.value <= 10) {
      return 'bg-red-500'
    } else if (trackPercent.value <= 25) {
      return 'bg-amber-500'
    }
    return 'bg-green-500'
  }
  if (props.utilization >= 90) {
    return 'bg-red-500'
  } else if (props.utilization >= 75) {
    return 'bg-amber-500'
  } else {
    return 'bg-green-500'
  }
})

// Text color based on utilization
const textClass = computed(() => {
  if (props.remainingCapacity) {
    if (trackPercent.value <= 10) {
      return 'text-red-600 dark:text-red-400'
    } else if (trackPercent.value <= 25) {
      return 'text-amber-600 dark:text-amber-400'
    }
    return 'text-gray-600 dark:text-gray-400'
  }
  if (props.utilization >= 90) {
    return 'text-red-600 dark:text-red-400'
  } else if (props.utilization >= 75) {
    return 'text-amber-600 dark:text-amber-400'
  } else {
    return 'text-gray-600 dark:text-gray-400'
  }
})

// Bar width (capped at 100%)
const barWidth = computed(() => {
  const percent = props.remainingCapacity
    ? trackPercent.value - deadWidth.value
    : trackPercent.value
  return `${percent}%`
})

// Display percentage (cap at 999% for readability)
const displayPercent = computed(() => {
  const percent = Math.round(
    props.remainingCapacity ? trackPercent.value : props.utilization
  )
  return percent > 999 ? '>999%' : `${percent}%`
})

const isIdle = computed(() =>
  props.remainingCapacity ? trackPercent.value >= 100 : props.utilization <= 0
)
const isConsumed = computed(() =>
  props.remainingCapacity ? trackPercent.value < 100 : props.utilization > 0
)

const shouldShowResetTime = computed(() => {
  if (props.resetsAt) return true
  return Boolean(props.showNowWhenIdle && isIdle.value)
})

const moneyWidthClass = computed(() => (props.remainingCapacity ? 'w-[128px]' : 'w-[112px]'))

// Format reset time
const formatResetTime = computed(() => {
  if (!props.resetsAt) {
    // Idle rolling window without a known reset time: treat as immediately available.
    return props.showNowWhenIdle && isIdle.value ? t('usage.resetNow') : '-'
  }

  const date = new Date(props.resetsAt)
  const diffMs = date.getTime() - now.value.getTime()

  // resetsAt 已过期：仍有消耗说明后端窗口数据还没刷新（active poll 没回写），
  // 显示「待刷新」以区别于真正可用的「现在」。
  if (diffMs <= 0) {
    return isConsumed.value ? t('usage.resetPending') : t('usage.resetNow')
  }

  const diffHours = Math.floor(diffMs / (1000 * 60 * 60))
  const diffMins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60))

  if (diffHours >= 24) {
    const days = Math.floor(diffHours / 24)
    return `${days}d ${diffHours % 24}h`
  } else if (diffHours > 0) {
    return `${diffHours}h ${diffMins}m`
  } else {
    return `${diffMins}m`
  }
})

// Window stats formatters
const formatRequests = computed(() => {
  if (!props.windowStats) return ''
  return formatCompactNumber(props.windowStats.requests, { allowBillions: false })
})

const formatTokens = computed(() => {
  if (!props.windowStats) return ''
  return formatCompactNumber(props.windowStats.tokens)
})

const formatAccountCost = computed(() => {
  if (!props.windowStats) return '0.00'
  return props.windowStats.cost.toFixed(2)
})

const formatUserCost = computed(() => {
  if (!props.windowStats || props.windowStats.user_cost == null) return '0.00'
  return props.windowStats.user_cost.toFixed(2)
})

</script>
