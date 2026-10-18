import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamUserInfoQuotaCell from '../UpstreamUserInfoQuotaCell.vue'

const { getUserInfoQuota } = vi.hoisted(() => ({ getUserInfoQuota: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getUserInfoQuota } } }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params && Object.keys(params).length > 0 ? `${key} ${JSON.stringify(params)}` : key,
    }),
  }
})

const freshUpdatedAt = () => new Date().toISOString()
const futureIso = (hours: number) => new Date(Date.now() + hours * 3_600_000).toISOString()

const baseExtra = { upstream_userinfo_updated_at: freshUpdatedAt() }

const snapshotAccount = (id: number, extra: Record<string, unknown>) =>
  ({
    id,
    platform: 'openai',
    type: 'apikey',
    credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
    extra,
  }) as any

const successResult = (id: number, remaining: number, maxBudget: number) => ({
  success: true,
  remaining,
  max_budget: maxBudget,
  spend: maxBudget - remaining,
  valid: true,
  unit: 'CNY',
  limits_known: false,
  fetched_at: Math.floor(Date.now() / 1000),
})

describe('USTC quota snapshot sync', () => {
  beforeEach(() => {
    getUserInfoQuota.mockReset()
  })

  it('replaces a manual probe result when a newer persisted snapshot arrives', async () => {
    const timestamp = Date.now()
    getUserInfoQuota.mockResolvedValue({
      success: true,
      remaining: 8,
      max_budget: 10,
      spend: 2,
      valid: true,
      unit: 'CNY',
      rpm_limit: null,
      max_parallel_requests: null,
      tpm_limit: null,
      limits_known: false,
      fetched_at: timestamp / 1000,
    })
    const account = snapshotAccount(101, baseExtra)
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('8.00')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('—')
    await wrapper.setProps({
      account: {
        ...account,
        extra: {
          upstream_userinfo_remaining: 7,
          upstream_userinfo_max_budget: 10,
          upstream_userinfo_spend: 3,
          upstream_userinfo_updated_at: new Date(timestamp + 30_000).toISOString(),
        },
      },
    })
    expect(wrapper.text()).toContain('7.00')
    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('shows read-only upstream RPM, concurrency, and TPM limits from the saved snapshot', () => {
    const account = snapshotAccount(102, {
      ...baseExtra,
      upstream_userinfo_remaining: 7,
      upstream_userinfo_max_budget: 10,
      upstream_userinfo_spend: 3,
      upstream_userinfo_limits_known: true,
      upstream_userinfo_rpm_limit: 20,
      upstream_userinfo_max_parallel_requests: 4,
      upstream_userinfo_tpm_limit: 12000,
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('20')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('4')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('12000')
    wrapper.unmount()
  })

  it('ignores a probe response after the account row changes', async () => {
    let resolveProbe!: (value: Record<string, unknown>) => void
    getUserInfoQuota.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveProbe = resolve
      })
    )
    const account = snapshotAccount(103, baseExtra)
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ account: { ...account, id: 104 } })
    resolveProbe(successResult(103, 8, 10))
    await flushPromises()

    expect(getUserInfoQuota).toHaveBeenCalledWith(103)
    expect(wrapper.text()).toContain('admin.accounts.userInfoQuota.empty')
    expect(wrapper.text()).not.toContain('8.00')
    wrapper.unmount()
  })
})

describe('USTC quota display', () => {
  beforeEach(() => {
    getUserInfoQuota.mockReset()
  })

  it('renders remaining quota rows and highlights the binding tier', () => {
    const account = snapshotAccount(201, {
      ...baseExtra,
      upstream_userinfo_remaining: 29.6,
      upstream_userinfo_max_budget: 30,
      upstream_userinfo_spend: 0.4,
      upstream_userinfo_windows: [
        {
          duration: '3h',
          limit: 30,
          remaining: 29.6,
          used_percent: 1.3,
          window_spend: 0.4,
          used_known: true,
          reset_at: futureIso(2),
        },
        {
          duration: '12h',
          limit: 70,
          remaining: 69.6,
          used_percent: 0.6,
          window_spend: 0.4,
          used_known: true,
          reset_at: futureIso(3),
        },
        {
          duration: '24h',
          limit: 100,
          remaining: 99.6,
          used_percent: 0.4,
          window_spend: 0.4,
          used_known: true,
          reset_at: futureIso(4),
        },
      ],
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    const bars = wrapper.findAll('[data-test="userinfo-quota-bar"]')
    expect(bars).toHaveLength(3)
    expect(bars[0].text()).toContain('3h')
    expect(bars[0].text()).toContain('29.60 / ¥30.00')
    expect(bars[2].text()).toContain('99.60 / ¥100.00')

    // 24h 档被 3h 档卡住 ¥40，重置前用不完。
    const tracks = wrapper.findAll('[data-test="usage-progress-track"]')
    expect(tracks[2].attributes('title')).toContain('admin.accounts.userInfoQuota.deadTitle')
    const dead = wrapper.findAll('[data-test="usage-progress-dead"]')
    expect(dead.length).toBeGreaterThan(0)
    wrapper.unmount()
  })

  it('shows overage when window spend exceeds the limit', () => {
    const account = snapshotAccount(202, {
      ...baseExtra,
      upstream_userinfo_remaining: 0,
      upstream_userinfo_max_budget: 30,
      upstream_userinfo_spend: 35,
      upstream_userinfo_windows: [
        {
          duration: '3h',
          limit: 30,
          remaining: 0,
          used_percent: 116.7,
          window_spend: 35,
          used_known: true,
          reset_at: futureIso(2),
        },
      ],
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.get('[data-test="usage-progress-money"]').text()).toContain('admin.accounts.userInfoQuota.over')
    expect(wrapper.get('[data-test="usage-progress-money"]').text()).toContain('5.00 / ¥30.00')
    expect(wrapper.get('[data-test="usage-progress-live"]').attributes('style')).toContain('width: 0%')
    wrapper.unmount()
  })

  it('degrades to "not provided" when the upstream omits window spend', () => {
    const account = snapshotAccount(203, {
      ...baseExtra,
      upstream_userinfo_remaining: 0,
      upstream_userinfo_max_budget: 30,
      upstream_userinfo_spend: 0,
      upstream_userinfo_windows: [
        {
          duration: '12h',
          limit: 30,
          remaining: 0,
          used_percent: 0,
          window_spend: 0,
          used_known: false,
          reset_at: futureIso(5),
        },
      ],
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.find('[data-test="userinfo-quota-bar"]').exists()).toBe(false)
    const chip = wrapper.get('[data-test="userinfo-quota-chip"]')
    expect(chip.text()).toContain('admin.accounts.userInfoQuota.notProvided')
    expect(chip.text()).toContain('¥30.00')
    expect(chip.text()).toContain('5h')
    wrapper.unmount()
  })

  it('shows the key alias and snapshot update time', () => {
    const account = snapshotAccount(204, {
      ...baseExtra,
      upstream_userinfo_remaining: 7,
      upstream_userinfo_max_budget: 10,
      upstream_userinfo_spend: 3,
      upstream_userinfo_key_alias: 'SYNTH_KEY',
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    const meta = wrapper.get('[data-test="userinfo-quota-meta"]')
    expect(meta.text()).toContain('admin.accounts.userInfoQuota.keyAlias')
    expect(meta.text()).toContain('SYNTH_KEY')
    expect(meta.text()).toContain('admin.accounts.userInfoQuota.updatedAt')
    wrapper.unmount()
  })

  it('falls back to the scalar budget when no windows are persisted', () => {
    const account = snapshotAccount(205, {
      ...baseExtra,
      upstream_userinfo_remaining: 8,
      upstream_userinfo_max_budget: 10,
      upstream_userinfo_spend: 2,
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.findAll('[data-test="userinfo-quota-bar"]')).toHaveLength(1)
    expect(wrapper.get('[data-test="usage-progress-money"]').text()).toBe('¥8.00 / ¥10.00')
    wrapper.unmount()
  })

  it('marks a consumed window whose reset time has passed as pending refresh', () => {
    const account = snapshotAccount(207, {
      ...baseExtra,
      upstream_userinfo_remaining: 1,
      upstream_userinfo_max_budget: 30,
      upstream_userinfo_spend: 29,
      upstream_userinfo_windows: [
        {
          duration: '3h',
          limit: 30,
          remaining: 1,
          used_percent: 96.7,
          window_spend: 29,
          used_known: true,
          reset_at: new Date(Date.now() - 60_000).toISOString(),
        },
      ],
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.get('[data-test="usage-progress-reset"]').text()).toBe('usage.resetPending')
    wrapper.unmount()
  })

  it('renders explicit-null limits as unlimited', () => {
    const account = snapshotAccount(206, {
      ...baseExtra,
      upstream_userinfo_remaining: 7,
      upstream_userinfo_max_budget: 10,
      upstream_userinfo_spend: 3,
      upstream_userinfo_limits_known: true,
      upstream_userinfo_rpm_limit: null,
      upstream_userinfo_max_parallel_requests: 4,
      upstream_userinfo_tpm_limit: 12000,
    })
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('∞')
    wrapper.unmount()
  })
})

describe('USTC quota auto-probe', () => {
  beforeEach(() => {
    getUserInfoQuota.mockReset()
  })

  it('probes once on mount when the snapshot is missing', async () => {
    getUserInfoQuota.mockResolvedValueOnce(successResult(301, 8, 10))
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: { account: snapshotAccount(301, {}) },
    })
    await flushPromises()

    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    expect(getUserInfoQuota).toHaveBeenCalledWith(301)
    expect(wrapper.text()).toContain('8.00')
    wrapper.unmount()
  })

  it('probes when the persisted snapshot is stale', async () => {
    getUserInfoQuota.mockResolvedValueOnce(successResult(304, 9, 10))
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: {
        account: snapshotAccount(304, {
          upstream_userinfo_remaining: 8,
          upstream_userinfo_max_budget: 10,
          upstream_userinfo_spend: 2,
          upstream_userinfo_updated_at: new Date(Date.now() - 120_000).toISOString(),
        }),
      },
    })
    await flushPromises()

    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('9.00')
    wrapper.unmount()
  })

  it('probes when the snapshot timestamp is fresh but the balance is missing', async () => {
    getUserInfoQuota.mockResolvedValueOnce(successResult(305, 9, 10))
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: { account: snapshotAccount(305, { ...baseExtra }) },
    })
    await flushPromises()

    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('9.00')
    wrapper.unmount()
  })

  it('does not probe when the persisted snapshot is fresh', async () => {
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: {
        account: snapshotAccount(302, {
          ...baseExtra,
          upstream_userinfo_remaining: 8,
          upstream_userinfo_max_budget: 10,
          upstream_userinfo_spend: 2,
        }),
      },
    })
    await flushPromises()

    expect(getUserInfoQuota).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('debounces repeated mounts of the same account', async () => {
    getUserInfoQuota.mockResolvedValue(successResult(303, 8, 10))
    const props = { account: snapshotAccount(303, {}) }
    const first = mount(UpstreamUserInfoQuotaCell, { props })
    await flushPromises()
    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    first.unmount()

    const second = mount(UpstreamUserInfoQuotaCell, { props })
    await flushPromises()
    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    second.unmount()
  })
})
