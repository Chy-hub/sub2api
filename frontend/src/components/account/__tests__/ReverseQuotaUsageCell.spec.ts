import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Account } from '@/types'
import type { ReverseQuota } from '@/api/admin/accounts'
import accountsEn from '@/i18n/locales/en/admin/accounts'
import { loadReverseQuota } from '@/utils/reverseQuota'
import ReverseQuotaUsageCell from '../ReverseQuotaUsageCell.vue'
import UsageProgressBar from '../UsageProgressBar.vue'

const { getReverseQuota } = vi.hoisted(() => ({ getReverseQuota: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getReverseQuota }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({
    t: (key: string, params: Record<string, unknown> = {}) => {
      let value: unknown = accountsEn
      for (const part of key.replace(/^admin\./, '').split('.')) {
        value = value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined
      }
      return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name) => String(params[name])) : key
    }
  })
}))

let nextId = 20000
const teams: ReverseQuota = {
  subscription: { tierName: 'Teams' },
  summary: [
    { key: 'user_quota_used', label: 'Used', value: 3000 },
    { key: 'user_quota_total', label: 'Total', value: 3000 },
    { key: 'user_quota_remaining', label: 'Remaining', value: 0 },
    { key: 'expires_at', label: 'Expires', value: 1794240000000, unit: 'unix' }
  ]
}

function account(quota?: ReverseQuota, age = 0, platform: Account['platform'] = 'qoder_cn'): Account {
  return {
    id: nextId++, platform, type: 'reverse',
    extra: quota ? { reverse_quota: { quota, updated_at: new Date(Date.now() - age).toISOString() } } : {}
  } as Account
}

function render(acc: Account) {
  return mount(ReverseQuotaUsageCell, { props: { account: acc } })
}

beforeEach(() => { getReverseQuota.mockReset() })

describe('Reverse quota in the account usage column', () => {
  it('immediately shows a saved exhausted Teams plan, zero remaining, and a millisecond expiry', async () => {
    const wrapper = render(account(teams))
    await flushPromises()
    expect(wrapper.text()).toContain('Teams')
    expect(wrapper.get('[data-test="reverse-quota-plan"]').text()).toContain('Used 3,000 / 3,000 · Remaining 0')
    expect(wrapper.findComponent(UsageProgressBar).props('utilization')).toBe(100)
    expect(wrapper.get('[data-test="reverse-quota-expiry"]').text()).toContain('2026')
    expect(getReverseQuota).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps a zero-total Free plan visible without inventing a percentage, and shows add-ons separately', async () => {
    const wrapper = render(account({
      subscription: { tierName: 'Free' },
      summary: [
        { key: 'user_quota_used', label: '', value: 0 },
        { key: 'user_quota_total', label: '', value: 0 },
        { key: 'user_quota_remaining', label: '', value: 0 },
        { key: 'addon_quota_used', label: '', value: 1 },
        { key: 'addon_quota_total', label: '', value: 11 },
        { key: 'addon_quota_remaining', label: '', value: 10 }
      ]
    }))
    await flushPromises()
    const plan = wrapper.get('[data-test="reverse-quota-plan"]')
    expect(plan.text()).toContain('Used 0 / 0 · Remaining 0')
    expect(plan.findComponent(UsageProgressBar).exists()).toBe(false)
    const addon = wrapper.get('[data-test="reverse-quota-addon"]')
    expect(addon.text()).toContain('Used 1 / 11 · Remaining 10')
    expect(addon.findComponent(UsageProgressBar).props('utilization')).toBeCloseTo(100 / 11)
    wrapper.unmount()
  })

  it('uses the WorkBuddy summary too, preserving fractional enterprise credits', async () => {
    const wrapper = render(account({
      subscription: { tierName: 'Enterprise' },
      summary: [
        { key: 'credit_remain', label: '', value: 2.59 },
        { key: 'credit_total', label: '', value: 4.11 },
        { key: 'credit_used', label: '', value: 1.52 }
      ]
    }, 0, 'workbuddy_global'))
    await flushPromises()
    expect(wrapper.get('[data-test="reverse-quota-credits"]').text()).toContain('Used 1.52 / 4.11 · Remaining 2.59')
    wrapper.unmount()
  })

  it('refreshes a stale snapshot automatically and reuses the result after remounting', async () => {
    const acc = account({ subscription: { tierName: 'Old plan' } }, 10 * 60 * 1000)
    getReverseQuota.mockResolvedValue(teams)
    const first = render(acc)
    expect(first.text()).toContain('Old plan')
    await flushPromises()
    expect(first.text()).toContain('Teams')
    expect(getReverseQuota).toHaveBeenCalledTimes(1)
    expect(getReverseQuota).toHaveBeenCalledWith(acc.id)
    first.unmount()
    const second = render(acc)
    await flushPromises()
    expect(second.text()).toContain('Teams')
    expect(getReverseQuota).toHaveBeenCalledTimes(1)
    await second.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getReverseQuota).toHaveBeenCalledTimes(2)
    second.unmount()
  })

  it('keeps the last quota visible when a manual refresh fails, and permits retry', async () => {
    const wrapper = render(account(teams))
    await flushPromises()
    getReverseQuota.mockRejectedValueOnce({ message: 'Upstream temporarily unavailable' })
    await wrapper.get('[data-test="reverse-quota-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Teams')
    expect(wrapper.get('[role="alert"]').text()).toBe('Upstream temporarily unavailable')
    getReverseQuota.mockResolvedValueOnce({ subscription: { tierName: 'Refreshed plan' } })
    await wrapper.get('[data-test="reverse-quota-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Refreshed plan')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not lose a global manual refresh when the initial cached load is still settling', async () => {
    getReverseQuota.mockResolvedValueOnce({ subscription: { tierName: 'Fresh after manual refresh' } })
    const wrapper = render(account(teams))
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getReverseQuota).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Fresh after manual refresh')
    wrapper.unmount()
  })

  it('discards a late result from a previous account', async () => {
    let resolveOld!: (quota: ReverseQuota) => void
    let resolveNew!: (quota: ReverseQuota) => void
    getReverseQuota.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    getReverseQuota.mockReturnValueOnce(new Promise(resolve => { resolveNew = resolve }))
    const wrapper = render(account())
    await wrapper.setProps({ account: account(undefined, 0, 'qoder_global') })
    resolveOld({ subscription: { tierName: 'Previous account plan' } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('Previous account plan')
    expect(wrapper.get('[data-test="reverse-quota-refresh"]').attributes('disabled')).toBeDefined()
    resolveNew(teams)
    await flushPromises()
    expect(wrapper.text()).toContain('Teams')
    wrapper.unmount()
  })

  it('deduplicates concurrent requests for the same account', async () => {
    let resolve!: (quota: ReverseQuota) => void
    getReverseQuota.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const acc = account()
    const first = render(acc)
    const second = render(acc)
    expect(getReverseQuota).toHaveBeenCalledTimes(1)
    expect(getReverseQuota).toHaveBeenCalledWith(acc.id)
    resolve(teams)
    await flushPromises()
    expect(first.text()).toContain('Teams')
    expect(second.text()).toContain('Teams')
    first.unmount()
    second.unmount()
  })

  it('limits automatic upstream quota queries across a page of accounts', async () => {
    const releases: Array<(quota: ReverseQuota) => void> = []
    getReverseQuota.mockImplementation(() => new Promise(resolve => { releases.push(resolve) }))
    const requests = Array.from({ length: 6 }, () => loadReverseQuota(account()))
    expect(getReverseQuota).toHaveBeenCalledTimes(4)
    releases[0](teams)
    releases[1](teams)
    await flushPromises()
    expect(getReverseQuota).toHaveBeenCalledTimes(6)
    releases.slice(2).forEach(release => release(teams))
    await Promise.all(requests)
  })
})
