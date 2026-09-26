<template>
  <div v-if="visible" class="min-w-[220px] space-y-1">
    <!-- 状态行：失效标记 / 无限额 / 无数据占位 -->
    <div v-if="!isValid && hasData" class="flex flex-wrap items-center gap-1.5">
      <span
        class="inline-flex items-center rounded bg-red-100 px-1 py-0.5 text-[10px] font-medium text-red-700 dark:bg-red-900/30 dark:text-red-300"
      >
        {{ t('admin.accounts.userInfoQuota.invalid') }}
      </span>
    </div>
    <div
      v-else-if="hasData && !hasBudget && !barWindows.length && !chipWindows.length"
      data-test="userinfo-quota-value"
      class="text-[10px] font-medium leading-4 text-cyan-700 dark:text-cyan-300"
    >
      {{ t('admin.accounts.userInfoQuota.unlimited') }}
    </div>
    <div v-else-if="!hasData" class="text-[10px] text-gray-400">
      {{ t('admin.accounts.userInfoQuota.empty') }}
    </div>

    <!-- 多档预算进度条（3h / 12h / 24h）：每条右侧带本窗口金额 -->
    <div v-if="barWindows.length" class="space-y-1">
      <UsageProgressBar
        v-for="(w, i) in barWindows"
        :key="w.label || i"
        data-test="userinfo-quota-bar"
        :label="w.label"
        :color="w.color"
        :utilization="w.usedPercent"
        :money="w.money"
        :resets-at="w.resetAt ?? null"
      />
    </div>
    <!-- 上游未提供用量的窗口：降级为「限额 · 倒计时」，不给虚假百分比 -->
    <div v-if="chipWindows.length" class="flex flex-wrap items-center gap-1.5">
      <span
        v-for="w in chipWindows"
        :key="w.label"
        data-test="userinfo-quota-chip"
        class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[9px] leading-4 text-gray-600 dark:bg-gray-800 dark:text-gray-300"
        :title="t('admin.accounts.userInfoQuota.usageUnknown')"
      >
        {{ w.label }} · {{ w.limitLabel }}<template v-if="w.resetLabel"> · {{ w.resetLabel }}</template>
      </span>
    </div>
    <!-- 单窗口兜底（无 windows 数组但有 max_budget） -->
    <UsageProgressBar
      v-if="!barWindows.length && !chipWindows.length && hasBudget"
      data-test="userinfo-quota-bar"
      :label="t('admin.accounts.userInfoQuota.barLabel')"
      color="indigo"
      :utilization="usedPercent"
      :money="fallbackMoney"
      :resets-at="current?.budget_reset_at ?? null"
    />

    <!-- 探测按钮 -->
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        data-test="userinfo-quota-probe"
        class="inline-flex items-center gap-0.5 whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] font-medium leading-4 text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="loading"
        :title="t('admin.accounts.userInfoQuota.probeTooltip')"
        @click="handleProbe"
      >
        <svg
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': loading }"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
        {{ t('admin.accounts.userInfoQuota.probe') }}
      </button>
    </div>

    <div v-if="error" class="truncate text-[10px] text-red-600 dark:text-red-400" :title="error">
      {{ truncatedError }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { UserInfoQuotaResult } from '@/api/admin/accounts'
import type { Account } from '@/types'
import { userInfoQuotaCellVisible } from './credentialsBuilder'
import UsageProgressBar from './UsageProgressBar.vue'

const props = defineProps<{
  account: Account
}>()

const { t } = useI18n()

const visible = computed(() => userInfoQuotaCellVisible(props.account))

const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<UserInfoQuotaResult | null>(null)

// 落库快照（后端探测写入 account.Extra，键在 upstream_userinfo_ 命名空间下）。
const snapshotRemaining = computed(() => {
  const v = props.account.extra?.upstream_userinfo_remaining
  return typeof v === 'number' ? v : null
})
const snapshotMaxBudget = computed(() => {
  const v = props.account.extra?.upstream_userinfo_max_budget
  return typeof v === 'number' ? v : null
})
const snapshotSpend = computed(() => {
  const v = props.account.extra?.upstream_userinfo_spend
  return typeof v === 'number' ? v : 0
})
// 键缺失按未知处理（当作可用），只有显式 false 才判定失效；
// 与 API 路径的 valid !== false 语义对齐，避免部分写入的 Extra 误报「已失效」。
const snapshotValid = computed(() => props.account.extra?.upstream_userinfo_valid !== false)
const snapshotExpiresAt = computed(() => {
  const v = props.account.extra?.upstream_userinfo_expires_at
  return typeof v === 'string' ? v : ''
})
const snapshotResetAt = computed(() => {
  const v = props.account.extra?.upstream_userinfo_budget_reset_at
  return typeof v === 'string' ? v : ''
})
// Extra 快照里的多档窗口（后端写 upstream_userinfo_windows）。
const snapshotWindows = computed<UserInfoQuotaResult['windows']>(() => {
  const v = props.account.extra?.upstream_userinfo_windows
  if (!Array.isArray(v)) return undefined
  const windows = v.flatMap((item): NonNullable<UserInfoQuotaResult['windows']> => {
    if (!item || typeof item !== 'object') return []
    const { duration, limit, remaining, used_percent, window_spend, used_known, reset_at } = item as Record<string, unknown>
    if (typeof limit !== 'number') return []
    return [{
      duration: typeof duration === 'string' ? duration : undefined,
      limit,
      remaining: typeof remaining === 'number' ? remaining : 0,
      used_percent: typeof used_percent === 'number' ? used_percent : 0,
      window_spend: typeof window_spend === 'number' ? window_spend : 0,
      used_known: used_known !== false,
      reset_at: typeof reset_at === 'string' ? reset_at : undefined
    }]
  })
  return windows.length > 0 ? windows : undefined
})

const current = computed(() => {
  if (data.value?.success) return data.value
  if (snapshotRemaining.value != null) {
    return {
      provider: 'upstream_userinfo',
      success: true,
      remaining: snapshotRemaining.value,
      max_budget: snapshotMaxBudget.value ?? 0,
      spend: snapshotSpend.value,
      unit: 'CNY',
      valid: snapshotValid.value,
      expires_at: snapshotExpiresAt.value || undefined,
      budget_reset_at: snapshotResetAt.value || undefined,
      windows: snapshotWindows.value,
      fetched_at: 0,
      persisted: true
    } satisfies UserInfoQuotaResult
  }
  return null
})

const hasData = computed(() => current.value != null)
const isValid = computed(() => current.value?.valid !== false)
// 有上限预算才画进度条；无限额只显示文字。
const hasBudget = computed(() => (current.value?.max_budget ?? 0) > 0)

const usedPercent = computed(() => {
  const cur = current.value
  if (!cur || cur.max_budget <= 0) return 0
  return Math.min(100, Math.round((cur.spend / cur.max_budget) * 1000) / 10)
})

// 多档预算窗口：优先探测结果，其次 Extra 快照；颜色按窗口序号轮转。
// used_known=false（上游未提供用量）的窗口走 chip 降级，不画进度条。
const barColors = ['indigo', 'emerald', 'purple'] as const

interface WindowBar {
  label: string
  color: (typeof barColors)[number]
  usedPercent: number
  money: string
  resetAt?: string
}
interface WindowChip {
  label: string
  limitLabel: string
  resetLabel: string
}

// 固定两位小数，保证各行金额字符串等宽（¥100.00 而非 ¥100），配合
// UsageProgressBar 金额列定宽，时间列才能纵向对齐。
const formatMoney = (n: number): string => n.toFixed(2)

const formatResetShort = (raw?: string): string => {
  if (!raw) return ''
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return ''
  // 距重置不足 24h 显示倒计时，否则显示时刻。
  const ms = d.getTime() - Date.now()
  if (ms > 0 && ms < 24 * 3600 * 1000) {
    const mins = Math.round(ms / 60000)
    if (mins < 60) return `${mins}m`
    return `${Math.floor(mins / 60)}h${mins % 60 ? `${mins % 60}m` : ''}`
  }
  return d.toLocaleString()
}

const windowLabel = (duration?: string) => duration || t('admin.accounts.userInfoQuota.barLabel')

const barWindows = computed<WindowBar[]>(() => {
  const cur = current.value
  if (!cur?.windows?.length) return []
  const unit = cur.unit === 'USD' ? '$' : '¥'
  const bars: WindowBar[] = []
  cur.windows.forEach((w, i) => {
    if (!w.used_known) return
    bars.push({
      label: windowLabel(w.duration),
      color: barColors[i % barColors.length],
      usedPercent: w.used_percent,
      money: `${unit}${formatMoney(w.remaining)} / ${unit}${formatMoney(w.limit)}`,
      resetAt: w.reset_at
    })
  })
  return bars
})

const chipWindows = computed<WindowChip[]>(() => {
  const cur = current.value
  if (!cur?.windows?.length) return []
  const chips: WindowChip[] = []
  cur.windows.forEach((w) => {
    if (w.used_known) return
    chips.push({
      label: windowLabel(w.duration),
      limitLabel: `${cur.unit === 'USD' ? '$' : '¥'}${formatMoney(w.limit)}`,
      resetLabel: formatResetShort(w.reset_at)
    })
  })
  return chips
})

// 单窗口兜底（无 windows 数组时）的金额文本。
const fallbackMoney = computed(() => {
  const cur = current.value
  if (!cur || cur.max_budget <= 0) return ''
  const unit = cur.unit === 'USD' ? '$' : '¥'
  return `${unit}${formatMoney(cur.remaining)} / ${unit}${formatMoney(cur.max_budget)}`
})

const extractErrorMessage = (e: unknown): string => {
  const err = e as {
    message?: string
    reason?: string
    response?: { data?: { message?: string; error?: string } }
  }
  return (
    err?.message ||
    err?.reason ||
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    t('common.error')
  )
}

const truncatedError = computed(() => {
  if (!error.value) return ''
  return error.value.length > 80 ? `${error.value.slice(0, 80)}...` : error.value
})

const handleProbe = async () => {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    const result = await adminAPI.accounts.getUserInfoQuota(props.account.id)
    if (result.success) {
      data.value = result
    } else {
      error.value = result.error || t('common.error')
    }
  } catch (e) {
    error.value = extractErrorMessage(e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.account.id,
  () => {
    data.value = null
    error.value = null
    loading.value = false
  }
)
</script>
