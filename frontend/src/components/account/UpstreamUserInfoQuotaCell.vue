<template>
  <div v-if="visible" class="min-w-[240px] space-y-1">
    <!-- 状态行：失效标记 / 无限额 / 无数据占位 -->
    <div v-if="!isValid && hasData" class="flex flex-wrap items-center gap-1.5">
      <span
        class="inline-flex items-center rounded bg-red-100 px-1 py-0.5 text-[10px] font-medium text-red-700 dark:bg-red-900/30 dark:text-red-300"
      >
        {{ t('admin.accounts.userInfoQuota.invalid') }}
      </span>
    </div>
    <div
      v-else-if="hasData && !hasBudget"
      data-test="userinfo-quota-value"
      class="text-[10px] font-medium leading-4 text-cyan-700 dark:text-cyan-300"
    >
      {{ t('admin.accounts.userInfoQuota.unlimited') }}
    </div>
    <div v-else-if="!hasData" class="text-[10px] text-gray-400">
      {{ t('admin.accounts.userInfoQuota.empty') }}
    </div>

    <!-- 单行窗口：标签 + 剩余额度条 + 剩余百分比 + 金额 + 倒计时 -->
    <div v-if="barWindows.length" class="space-y-1">
      <UsageProgressBar
        v-for="w in barWindows"
        :key="w.key"
        data-test="userinfo-quota-bar"
        :label="w.label"
        :color="w.color"
        :utilization="w.remainingPercent"
        remaining-capacity
        :dead-percent="w.deadPercent"
        :dead-title="w.deadTitle"
        :money="w.money"
        :resets-at="w.resetAt"
        :reset-title="w.resetTitle"
      />
    </div>
    <!-- 上游未提供用量的窗口：降级为「限额 · 未提供 · 倒计时」，不给虚假百分比 -->
    <div v-if="chipWindows.length" class="flex flex-wrap items-center gap-1.5">
      <span
        v-for="w in chipWindows"
        :key="w.key"
        data-test="userinfo-quota-chip"
        class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[9px] leading-4 text-gray-600 dark:bg-gray-800 dark:text-gray-300"
        :title="t('admin.accounts.userInfoQuota.usageUnknown')"
      >
        {{ w.label }} · {{ w.limitLabel }} · {{ t('admin.accounts.userInfoQuota.notProvided') }}<template v-if="w.resetLabel"> · {{ w.resetLabel }}</template>
      </span>
    </div>

    <!-- 上游限额只读展示；金额预算继续由上方窗口显示。 -->
    <div
      v-if="showUpstreamLimits"
      data-test="userinfo-upstream-limits"
      class="flex flex-wrap items-center gap-x-2 text-[10px] leading-4 text-gray-600 dark:text-gray-300"
    >
      <span>{{ t('admin.accounts.userInfoQuota.rpmLimit') }} {{ formatUpstreamLimit(upstreamLimits.rpm, limitsKnown) }}</span>
      <span>{{ t('admin.accounts.userInfoQuota.parallelLimit') }} {{ formatUpstreamLimit(upstreamLimits.parallel, limitsKnown) }}</span>
      <span>{{ t('admin.accounts.userInfoQuota.tpmLimit') }} {{ formatUpstreamLimit(upstreamLimits.tpm, limitsKnown) }}</span>
    </div>

    <div
      v-if="keyAlias || updatedLabel"
      data-test="userinfo-quota-meta"
      class="flex flex-wrap items-center gap-x-2 text-[9px] leading-4 text-gray-400"
    >
      <span
        v-if="keyAlias"
        class="max-w-[120px] truncate"
        :title="t('admin.accounts.userInfoQuota.keyAlias', { alias: keyAlias })"
      >
        {{ t('admin.accounts.userInfoQuota.keyAlias', { alias: keyAlias }) }}
      </span>
      <span v-if="updatedLabel" :title="updatedLabel.title">{{ updatedLabel.label }}</span>
    </div>

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

<script lang="ts">
// 模块级去抖：<script setup> 顶层变量是每实例的，翻页/刷新会重新探测。
const lastAutoProbeAt = new Map<number, number>()
</script>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { UserInfoQuotaResult } from '@/api/admin/accounts'
import type { Account } from '@/types'
import { userInfoQuotaCellVisible } from './credentialsBuilder'
import { buildUserInfoQuotaRefreshKey } from '@/utils/accountUsageRefresh'
import { formatDateTime, formatDateTimeToMinute } from '@/utils/format'
import {
  deadPercentOf,
  normalizeQuotaWindow,
  normalizeScalarBudget,
  windowReach,
  type QuotaWindow,
  type RawQuotaWindow,
} from '@/utils/upstreamQuota'
import UsageProgressBar from './UsageProgressBar.vue'

const props = defineProps<{
  account: Account
}>()

const { t } = useI18n()

const visible = computed(() => userInfoQuotaCellVisible(props.account))

const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<UserInfoQuotaResult | null>(null)
let probeSequence = 0

const SNAPSHOT_STALE_MS = 60_000
const AUTO_PROBE_DEBOUNCE_MS = 5 * 60_000

const readExtraNumber = (key: string): number | null => {
  const value = (props.account.extra as Record<string, unknown> | undefined)?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
const readExtraString = (key: string): string => {
  const value = (props.account.extra as Record<string, unknown> | undefined)?.[key]
  return typeof value === 'string' ? value : ''
}

interface QuotaSource {
  unitSymbol: string
  valid: boolean
  keyAlias: string | null
  updatedAtMs: number | null
  maxBudget: number | null
  spend: number | null
  remaining: number | null
  resetAt: string | null
  rawWindows: RawQuotaWindow[]
  limits: { rpm: number | null; parallel: number | null; tpm: number | null }
  limitsKnown: boolean
  showLimits: boolean
}

const snapshotRawWindows = computed<RawQuotaWindow[]>(() => {
  const raw = props.account.extra?.upstream_userinfo_windows
  if (!Array.isArray(raw)) return []
  return raw.filter((item) => typeof item === 'object' && item !== null) as RawQuotaWindow[]
})

const snapshotUpdatedAtMs = computed(() => {
  const raw = readExtraString('upstream_userinfo_updated_at')
  if (raw === '') return null
  const ms = Date.parse(raw)
  return Number.isNaN(ms) ? null : ms
})

// 探测结果优先；没有本次探测结果时读取后端落库的快照。
const source = computed<QuotaSource | null>(() => {
  const probe = data.value
  if (probe?.success) {
    return {
      unitSymbol: probe.unit === 'USD' ? '$' : '¥',
      valid: probe.valid !== false,
      keyAlias: typeof probe.key_alias === 'string' && probe.key_alias.trim() !== '' ? probe.key_alias.trim() : null,
      updatedAtMs: probe.fetched_at > 0 ? probe.fetched_at * 1000 : null,
      maxBudget: typeof probe.max_budget === 'number' ? probe.max_budget : null,
      spend: typeof probe.spend === 'number' ? probe.spend : null,
      remaining: typeof probe.remaining === 'number' ? probe.remaining : null,
      resetAt: typeof probe.budget_reset_at === 'string' && probe.budget_reset_at !== '' ? probe.budget_reset_at : null,
      rawWindows: Array.isArray(probe.windows) ? probe.windows : [],
      limits: {
        rpm: typeof probe.rpm_limit === 'number' ? probe.rpm_limit : null,
        parallel: typeof probe.max_parallel_requests === 'number' ? probe.max_parallel_requests : null,
        tpm: typeof probe.tpm_limit === 'number' ? probe.tpm_limit : null,
      },
      limitsKnown: probe.limits_known === true,
      showLimits: true,
    }
  }

  const remaining = readExtraNumber('upstream_userinfo_remaining')
  if (remaining === null) return null
  const limitsKnownRaw = props.account.extra?.upstream_userinfo_limits_known
  return {
    unitSymbol: '¥',
    valid: props.account.extra?.upstream_userinfo_valid !== false,
    keyAlias: readExtraString('upstream_userinfo_key_alias').trim() || null,
    updatedAtMs: snapshotUpdatedAtMs.value,
    maxBudget: readExtraNumber('upstream_userinfo_max_budget'),
    spend: readExtraNumber('upstream_userinfo_spend'),
    remaining,
    resetAt: readExtraString('upstream_userinfo_budget_reset_at') || null,
    rawWindows: snapshotRawWindows.value,
    limits: {
      rpm: readExtraNumber('upstream_userinfo_rpm_limit'),
      parallel: readExtraNumber('upstream_userinfo_max_parallel_requests'),
      tpm: readExtraNumber('upstream_userinfo_tpm_limit'),
    },
    limitsKnown: limitsKnownRaw === true,
    showLimits: typeof limitsKnownRaw === 'boolean',
  }
})

const hasData = computed(() => source.value !== null)
const isValid = computed(() => source.value?.valid !== false)
const unitSymbol = computed(() => source.value?.unitSymbol ?? '¥')

const windows = computed<QuotaWindow[]>(() =>
  (source.value?.rawWindows ?? []).flatMap((raw) => normalizeQuotaWindow(raw) ?? [])
)

const scalar = computed<QuotaWindow | null>(() => {
  const current = source.value
  if (!current || current.rawWindows.length > 0) return null
  return normalizeScalarBudget({
    maxBudget: current.maxBudget,
    spend: current.spend,
    remaining: current.remaining,
    resetAt: current.resetAt,
  })
})

const allWindows = computed<QuotaWindow[]>(() => {
  if (windows.value.length > 0) return windows.value
  return scalar.value ? [scalar.value] : []
})

const hasBudget = computed(() => allWindows.value.length > 0)

const upstreamLimits = computed(() => source.value?.limits ?? { rpm: null, parallel: null, tpm: null })
const limitsKnown = computed(() => source.value?.limitsKnown === true)
const showUpstreamLimits = computed(() => source.value?.showLimits === true)
const formatUpstreamLimit = (limit: number | null, known: boolean) => {
  if (!known) return '—'
  return limit == null ? '∞' : String(limit)
}

const keyAlias = computed(() => source.value?.keyAlias ?? null)

const formatAmount = (value: number, unit: string): string => `${unit}${value.toFixed(2)}`

const windowLabel = (duration: string): string => duration || t('admin.accounts.userInfoQuota.barLabel')

const formatResetShort = (raw: string | null): string => {
  if (!raw) return ''
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return ''
  // 距重置不足 24h 显示倒计时，否则显示时刻。
  const ms = date.getTime() - Date.now()
  if (ms > 0 && ms < 24 * 3600 * 1000) {
    const mins = Math.round(ms / 60000)
    if (mins < 60) return `${mins}m`
    return `${Math.floor(mins / 60)}h${mins % 60 ? `${mins % 60}m` : ''}`
  }
  return date.toLocaleString()
}

const resetTitleFor = (resetAt: string | null): string | undefined => {
  if (!resetAt) return undefined
  return t('admin.accounts.userInfoQuota.resetClock', {
    time: formatDateTime(resetAt, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }),
  })
}

const updatedLabel = computed<{ label: string; title: string } | null>(() => {
  const ms = source.value?.updatedAtMs
  if (ms === null || ms === undefined) return null
  const at = new Date(ms)
  if (Number.isNaN(at.getTime())) return null
  const sameDay = at.toDateString() === new Date().toDateString()
  const time = formatDateTime(
    at,
    sameDay
      ? { hour: '2-digit', minute: '2-digit', hour12: false }
      : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
  )
  return {
    label: t('admin.accounts.userInfoQuota.updatedAt', { time }),
    title: formatDateTimeToMinute(at),
  }
})

const barColors = ['indigo', 'emerald', 'purple'] as const

interface WindowBar {
  key: string
  label: string
  color: (typeof barColors)[number]
  remainingPercent: number
  deadPercent: number
  deadTitle?: string
  money: string
  resetAt: string | null
  resetTitle?: string
}

const barWindows = computed<WindowBar[]>(() => {
  const unit = unitSymbol.value
  const currentWindows = allWindows.value
  const bars: WindowBar[] = []
  currentWindows.forEach((window, index) => {
    if (!window.usedKnown || window.remaining === null) return
    const remainingPercent = Math.min(Math.max((window.remaining / window.limit) * 100, 0), 100)
    const reach = windowReach(window, currentWindows, Date.now())
    const deadPercent = deadPercentOf(remainingPercent, reach, window.limit)
    const deadTitle =
      deadPercent > 0 && reach !== null
        ? t('admin.accounts.userInfoQuota.deadTitle', {
            reachable: formatAmount(Math.max(0, reach.reachable), unit),
            unreachable: formatAmount(reach.unreachable, unit),
            by: reach.limitedBy
              ? t('admin.accounts.userInfoQuota.deadBy', { duration: windowLabel(reach.limitedBy.duration) })
              : '',
          })
        : undefined
    const money =
      window.remaining < 0
        ? `${t('admin.accounts.userInfoQuota.over')} ${formatAmount(-window.remaining, unit)} / ${formatAmount(window.limit, unit)}`
        : `${formatAmount(window.remaining, unit)} / ${formatAmount(window.limit, unit)}`
    bars.push({
      key: `${window.duration || 'budget'}-${index}`,
      label: windowLabel(window.duration),
      color: barColors[index % barColors.length],
      remainingPercent,
      deadPercent,
      deadTitle,
      money,
      resetAt: window.resetAt,
      resetTitle: resetTitleFor(window.resetAt),
    })
  })
  return bars
})

interface WindowChip {
  key: string
  label: string
  limitLabel: string
  resetLabel: string
}

const chipWindows = computed<WindowChip[]>(() => {
  const unit = unitSymbol.value
  const chips: WindowChip[] = []
  allWindows.value.forEach((window, index) => {
    if (window.usedKnown && window.remaining !== null) return
    chips.push({
      key: `${window.duration || 'budget'}-${index}`,
      label: windowLabel(window.duration),
      limitLabel: formatAmount(window.limit, unit),
      resetLabel: formatResetShort(window.resetAt),
    })
  })
  return chips
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
  const requestSequence = ++probeSequence
  const accountID = props.account.id
  loading.value = true
  error.value = null
  try {
    const result = await adminAPI.accounts.getUserInfoQuota(accountID)
    if (requestSequence !== probeSequence || props.account.id !== accountID) return
    if (result.success) {
      data.value = result
    } else {
      error.value = result.error || t('common.error')
    }
  } catch (e) {
    if (requestSequence !== probeSequence || props.account.id !== accountID) return
    error.value = extractErrorMessage(e)
  } finally {
    if (requestSequence === probeSequence) loading.value = false
  }
}

const snapshotIsStale = (): boolean => {
  const ms = snapshotUpdatedAtMs.value
  if (ms === null) return true
  // 时间戳存在但缺少剩余额度键（部分写入/旧快照）同样需要探测补齐。
  if (readExtraNumber('upstream_userinfo_remaining') === null) return true
  return Date.now() - ms > SNAPSHOT_STALE_MS
}

onMounted(() => {
  if (!visible.value) return
  if (!snapshotIsStale()) return
  const last = lastAutoProbeAt.get(props.account.id) ?? 0
  if (Date.now() - last < AUTO_PROBE_DEBOUNCE_MS) return
  lastAutoProbeAt.set(props.account.id, Date.now())
  void handleProbe()
})

watch(
  () => buildUserInfoQuotaRefreshKey(props.account),
  () => {
    const updatedAt = Date.parse(String(props.account.extra?.upstream_userinfo_updated_at ?? ''))
    if (data.value && updatedAt >= data.value.fetched_at * 1000) {
      data.value = null
      error.value = null
    }
  }
)

watch(
  () => props.account.id,
  () => {
    probeSequence += 1
    data.value = null
    error.value = null
    loading.value = false
  }
)
</script>
