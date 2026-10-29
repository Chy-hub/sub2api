import { getReverseQuota, type ReverseQuota } from '@/api/admin/accounts'
import type { Account } from '@/types'

export interface ReverseQuotaSnapshot {
  updated_at: string
  quota: ReverseQuota
}

const CACHE_TTL = 5 * 60 * 1000
const cache = new Map<string, ReverseQuotaSnapshot>()
const pending = new Map<string, Promise<ReverseQuotaSnapshot>>()
const waiters: Array<() => void> = []
let activeRequests = 0

const cacheKey = (account: Pick<Account, 'id' | 'platform'>) => `${account.platform}:${account.id}`

export function readReverseQuotaSnapshot(account: Account): ReverseQuotaSnapshot | null {
  const saved = account.extra?.reverse_quota as ReverseQuotaSnapshot | undefined
  const cached = cache.get(cacheKey(account))
  const candidates = [saved, cached].filter((value): value is ReverseQuotaSnapshot => (
    !!value?.quota && typeof value.quota === 'object' && Number.isFinite(Date.parse(value.updated_at))
  ))
  return candidates.sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at))[0] ?? null
}

async function queryWithLimit(id: number): Promise<ReverseQuota> {
  if (activeRequests >= 4) await new Promise<void>(resolve => waiters.push(resolve))
  else activeRequests += 1
  try {
    return await getReverseQuota(id)
  } finally {
    const next = waiters.shift()
    if (next) next()
    else activeRequests -= 1
  }
}

export function loadReverseQuota(account: Account, force = false): Promise<ReverseQuotaSnapshot> {
  const key = cacheKey(account)
  const inflight = pending.get(key)
  if (inflight) return inflight
  const snapshot = readReverseQuotaSnapshot(account)
  if (!force && snapshot && Math.abs(Date.now() - Date.parse(snapshot.updated_at)) < CACHE_TTL) {
    return Promise.resolve(snapshot)
  }
  const request = queryWithLimit(account.id).then(quota => {
    const result = { quota, updated_at: new Date().toISOString() }
    cache.delete(key)
    cache.set(key, result)
    // Keep the browser cache bounded when navigating many account pages.
    if (cache.size > 200) cache.delete(cache.keys().next().value!)
    return result
  }).finally(() => { pending.delete(key) })
  pending.set(key, request)
  return request
}

// Older saved Qoder snapshots use milliseconds even though the metric unit is
// "unix". Accept both forms while the normalized contract uses Unix seconds.
export function reverseQuotaDate(value: number): Date | null {
  if (!Number.isFinite(value) || value <= 0) return null
  const date = new Date(value >= 1e12 ? value : value * 1000)
  return Number.isFinite(date.getTime()) ? date : null
}
