import { describe, expect, it } from 'vitest'
import {
  deadPercentOf,
  durationMs,
  normalizeQuotaWindow,
  normalizeScalarBudget,
  windowReach,
  type QuotaWindow,
} from '../upstreamQuota'

const NOW = Date.parse('2026-10-04T10:00:00Z')
const inHours = (hours: number) => new Date(NOW + hours * 3_600_000).toISOString()

function windowAt(
  duration: string,
  limit: number,
  spend: number | null,
  resetAt: string | null
): QuotaWindow {
  const usedKnown = spend !== null
  return {
    duration,
    limit,
    windowSpend: spend,
    remaining: spend === null ? null : limit - spend,
    usedPercent: spend === null ? null : (spend / limit) * 100,
    usedKnown,
    resetAt,
  }
}

describe('durationMs', () => {
  it('parses upstream duration units', () => {
    expect(durationMs('3h')).toBe(3 * 3_600_000)
    expect(durationMs('30m')).toBe(30 * 60_000)
    expect(durationMs('1d')).toBe(86_400_000)
    expect(durationMs('1mo')).toBe(30 * 86_400_000)
    expect(durationMs('90s')).toBe(90_000)
    expect(durationMs(' 12H ')).toBe(12 * 3_600_000)
  })

  it('rejects unrecognized or non-positive durations', () => {
    for (const raw of ['', '0h', '-3h', '3', 'h', '3 hours', '1h30m', 'abc']) {
      expect(durationMs(raw)).toBeNull()
    }
    expect(durationMs(`${'9'.repeat(303)}h`)).toBeNull()
  })
})

describe('normalizeQuotaWindow', () => {
  it('drops windows without a positive limit', () => {
    expect(normalizeQuotaWindow({ limit: 0 })).toBeNull()
    expect(normalizeQuotaWindow({ limit: -1 })).toBeNull()
    expect(normalizeQuotaWindow({})).toBeNull()
  })

  it('uses window_spend first and preserves overspend', () => {
    const window = normalizeQuotaWindow({
      duration: '3h',
      limit: 30,
      window_spend: 35,
      remaining: 0,
      used_known: true,
      reset_at: inHours(2),
    })
    expect(window).not.toBeNull()
    expect(window!.windowSpend).toBe(35)
    expect(window!.remaining).toBe(-5)
    expect(window!.usedPercent).toBeCloseTo(116.67, 2)
    expect(window!.resetAt).toBe(inHours(2))
  })

  it('derives spend from legacy remaining or used_percent', () => {
    expect(normalizeQuotaWindow({ limit: 30, remaining: 28.5, used_known: true })!.windowSpend).toBe(1.5)
    expect(normalizeQuotaWindow({ limit: 30, used_percent: 10, used_known: true })!.windowSpend).toBe(3)
  })

  it('keeps usage unknown when upstream reports used_known=false', () => {
    const window = normalizeQuotaWindow({
      duration: '12h',
      limit: 30,
      window_spend: 5,
      remaining: 0,
      used_known: false,
    })
    expect(window!.usedKnown).toBe(false)
    expect(window!.windowSpend).toBeNull()
    expect(window!.remaining).toBeNull()
    expect(window!.usedPercent).toBeNull()
  })

  it('ignores invalid reset timestamps', () => {
    expect(normalizeQuotaWindow({ limit: 30, used_known: false, reset_at: 'not-a-time' })!.resetAt).toBeNull()
  })
})

describe('normalizeScalarBudget', () => {
  it('derives remaining and overspend from the scalar budget', () => {
    expect(normalizeScalarBudget({ maxBudget: 100, spend: 120 })!.remaining).toBe(-20)
    expect(normalizeScalarBudget({ maxBudget: 100, remaining: 40 })!.windowSpend).toBe(60)
  })

  it('returns null without a positive limit', () => {
    expect(normalizeScalarBudget({ spend: 1 })).toBeNull()
    expect(normalizeScalarBudget({ maxBudget: 0 })).toBeNull()
  })
})

describe('windowReach', () => {
  it('caps a longer window by the remaining quota of a shorter one', () => {
    // 24h 档剩 ¥70、2h 后重置；3h 档剩 ¥30，3h 后重置。
    const three = windowAt('3h', 30, 0, inHours(3))
    const day = windowAt('24h', 100, 30, inHours(2))
    const reach = windowReach(day, [three, day], NOW)!
    expect(reach.reachable).toBe(30)
    expect(reach.unreachable).toBe(40)
    expect(reach.limitedBy).toBe(three)
    expect(windowReach(three, [three, day], NOW)!.unreachable).toBe(0)
  })

  it('counts a short-window refill inside the interval', () => {
    // 3h 档 1h 后回满，24h 档 2h 后重置：这 2h 里能用 ¥30 + ¥30。
    const three = windowAt('3h', 30, 0, inHours(1))
    const day = windowAt('24h', 100, 30, inHours(2))
    const reach = windowReach(day, [three, day], NOW)!
    expect(reach.reachable).toBe(60)
    expect(reach.unreachable).toBe(10)
  })

  it('does not count a refill landing exactly at the target reset', () => {
    const three = windowAt('3h', 30, 0, inHours(1))
    const day = windowAt('24h', 100, 30, inHours(4))
    const reach = windowReach(day, [three, day], NOW)!
    expect(reach.reachable).toBe(60)
    expect(reach.unreachable).toBe(10)
  })

  it('skips windows with missing or stale reset information', () => {
    const day = windowAt('24h', 100, 30, inHours(2))
    expect(windowReach(windowAt('24h', 100, null, inHours(2)), [day], NOW)).toBeNull()
    expect(windowReach(windowAt('24h', 100, 30, null), [day], NOW)).toBeNull()
    expect(windowReach(windowAt('24h', 100, 120, inHours(2)), [day], NOW)!.unreachable).toBe(0)
    expect(windowReach(windowAt('24h', 100, 30, inHours(-1)), [day], NOW)!.unreachable).toBe(0)

    const unknownDuration = windowAt('1h30m', 30, 0, inHours(1))
    const noReset = windowAt('3h', 5, 0, null)
    const staleReset = windowAt('3h', 5, 0, inHours(-0.1))
    for (const other of [unknownDuration, noReset, staleReset]) {
      expect(windowReach(day, [other, day], NOW)!.unreachable).toBe(0)
    }
  })

  it('uses the limit when another window spend is unknown', () => {
    const three = windowAt('3h', 30, null, inHours(3))
    const day = windowAt('24h', 100, 30, inHours(2))
    const reach = windowReach(day, [three, day], NOW)!
    expect(reach.reachable).toBe(30)
    expect(reach.unreachable).toBe(40)
  })
})

describe('deadPercentOf', () => {
  it('draws unreachable quota within the remaining segment', () => {
    expect(deadPercentOf(70, { reachable: 60, unreachable: 40, limitedBy: null }, 100)).toBe(40)
    expect(deadPercentOf(20, { reachable: 10, unreachable: 30, limitedBy: null }, 100)).toBe(20)
  })

  it('ignores amounts below one cent', () => {
    expect(deadPercentOf(70, { reachable: 70, unreachable: 0.005, limitedBy: null }, 100)).toBe(0)
    expect(deadPercentOf(70, null, 100)).toBe(0)
  })
})
