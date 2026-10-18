// USTC / LiteLLM 预算窗口的展示归一与重置前可达额度估算。

export interface RawQuotaWindow {
  duration?: unknown
  limit?: unknown
  remaining?: unknown
  used_percent?: unknown
  window_spend?: unknown
  used_known?: unknown
  reset_at?: unknown
}

export interface QuotaWindow {
  duration: string
  limit: number
  /** null 表示上游没有提供窗口内用量。 */
  windowSpend: number | null
  /** limit - windowSpend；超支时保留负数。 */
  remaining: number | null
  /** 可超过 100。 */
  usedPercent: number | null
  usedKnown: boolean
  resetAt: string | null
}

export interface WindowReach {
  reachable: number
  unreachable: number
  limitedBy: QuotaWindow | null
}

const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

const DURATION_UNITS: Record<string, number> = {
  s: 1_000,
  m: MINUTE_MS,
  h: HOUR_MS,
  d: DAY_MS,
  w: 7 * DAY_MS,
  mo: 30 * DAY_MS,
  y: 365 * DAY_MS,
}

/** '30m' / '3h' / '1d' / '1mo' → 毫秒；无法识别时返回 null。 */
export function durationMs(duration: unknown): number | null {
  if (typeof duration !== 'string') return null
  const match = /^(\d+(?:\.\d+)?)\s*(mo|[smhdwy])$/i.exec(duration.trim())
  if (match === null) return null
  const value = Number(match[1])
  if (!Number.isFinite(value) || value <= 0) return null
  const ms = value * DURATION_UNITS[match[2].toLowerCase()]
  return Number.isFinite(ms) ? ms : null
}

function asNumber(value: unknown): number | null {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value !== 'string') return null
  const trimmed = value.trim()
  if (trimmed === '') return null
  const parsed = Number(trimmed)
  return Number.isFinite(parsed) ? parsed : null
}

function asResetAt(value: unknown): string | null {
  if (typeof value !== 'string' || value.trim() === '') return null
  return Number.isNaN(Date.parse(value)) ? null : value
}

/**
 * 归一单档预算窗口。
 * remaining 不信任后端夹紧后的 0，超支时重新计算为负数。
 */
export function normalizeQuotaWindow(raw: RawQuotaWindow): QuotaWindow | null {
  const limit = asNumber(raw.limit)
  if (limit === null || limit <= 0) return null

  let spend: number | null = null
  if (raw.used_known !== false) {
    spend = asNumber(raw.window_spend)
    if (spend === null) {
      const remaining = asNumber(raw.remaining)
      if (remaining !== null) spend = limit - remaining
    }
    if (spend === null) {
      const usedPercent = asNumber(raw.used_percent)
      if (usedPercent !== null) spend = (limit * usedPercent) / 100
    }
  }

  const windowSpend = spend === null ? null : Math.max(0, spend)
  const usedKnown = windowSpend !== null
  const remaining = windowSpend === null ? null : limit - windowSpend
  const usedPercent = windowSpend === null ? null : (windowSpend / limit) * 100
  const duration = typeof raw.duration === 'string' ? raw.duration.trim() : ''

  return {
    duration,
    limit,
    windowSpend,
    remaining,
    usedPercent,
    usedKnown,
    resetAt: asResetAt(raw.reset_at),
  }
}

/** 没有 windows 数组时，用顶层标量预算构造一档兜底窗口。 */
export function normalizeScalarBudget(input: {
  maxBudget?: unknown
  spend?: unknown
  remaining?: unknown
  resetAt?: unknown
}): QuotaWindow | null {
  const limit = asNumber(input.maxBudget)
  if (limit === null || limit <= 0) return null

  let spend = asNumber(input.spend)
  if (spend === null) {
    const remaining = asNumber(input.remaining)
    if (remaining !== null) spend = limit - remaining
  }
  const windowSpend = spend === null ? null : Math.max(0, spend)
  const usedKnown = windowSpend !== null

  return {
    duration: '',
    limit,
    windowSpend,
    remaining: windowSpend === null ? null : limit - windowSpend,
    usedPercent: windowSpend === null ? null : (windowSpend / limit) * 100,
    usedKnown,
    resetAt: asResetAt(input.resetAt),
  }
}

function resetTimeMs(window: QuotaWindow): number | null {
  if (window.resetAt === null) return null
  const ms = Date.parse(window.resetAt)
  return Number.isNaN(ms) ? null : ms
}

/**
 * 某一档在接下来 untilReset 毫秒里可动用的额度：眼下剩余 + 期间重置回满的次数 × 上限。
 * 剩余未知时按上限估算；缺少有效重置信息的档位跳过。
 */
function availableWithin(window: QuotaWindow, untilReset: number, now: number): number | null {
  const base = Math.max(0, window.remaining ?? window.limit)
  const reset = resetTimeMs(window)
  if (reset === null) return null
  const own = reset - now
  if (own >= untilReset) return base
  if (own <= 0) return null
  const period = durationMs(window.duration)
  if (period === null) return null
  // ceil 在周期整倍数时排除截止点的那次重置，计数区间为 [now, 目标重置时刻)。
  return base + Math.ceil((untilReset - own) / period) * window.limit
}

/**
 * 某一档重置前可用额度的上界，返回 { reachable, unreachable, limitedBy }。
 * 各档扣的是同一份消费，取各档上界的最小值；差额是至少会作废的额度。
 */
export function windowReach(
  target: QuotaWindow,
  windows: readonly QuotaWindow[],
  now = Date.now()
): WindowReach | null {
  if (!target.usedKnown || target.remaining === null) return null
  const targetReset = resetTimeMs(target)
  if (targetReset === null) return null

  const untilReset = targetReset - now
  if (target.remaining <= 0 || untilReset <= 0) {
    return { reachable: target.remaining, unreachable: 0, limitedBy: null }
  }

  let reachable = target.remaining
  let limitedBy: QuotaWindow | null = null
  for (const other of windows) {
    const available = availableWithin(other, untilReset, now)
    if (available === null || available >= reachable) continue
    reachable = Math.max(0, available)
    limitedBy = other
  }
  return { reachable, unreachable: target.remaining - reachable, limitedBy }
}

const DEAD_MIN_AMOUNT = 0.01

/** 作废额度占整条进度条的百分比，并夹在剩余比例内。 */
export function deadPercentOf(
  remainingPercent: number,
  reach: WindowReach | null,
  limit: number
): number {
  if (reach === null || reach.unreachable < DEAD_MIN_AMOUNT || limit <= 0) return 0
  const remaining = Math.min(Math.max(remainingPercent, 0), 100)
  return Math.min(remaining, (reach.unreachable / limit) * 100)
}
