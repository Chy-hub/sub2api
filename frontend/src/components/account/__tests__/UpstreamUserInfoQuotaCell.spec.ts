import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamUserInfoQuotaCell from '../UpstreamUserInfoQuotaCell.vue'

const { getUserInfoQuota } = vi.hoisted(() => ({ getUserInfoQuota: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getUserInfoQuota } } }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

describe('USTC quota snapshot sync', () => {
  it('replaces a manual probe result when a newer persisted snapshot arrives', async () => {
    const timestamp = Date.parse('2026-10-01T05:10:00Z')
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
      fetched_at: timestamp / 1000
    })
    const account = { id: 1, type: 'apikey', credentials: { base_url: 'https://api.llm.ustc.edu.cn' }, extra: {} } as any
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: { account }, global: { stubs: { UsageProgressBar: { props: ['money'], template: '<span>{{ money }}</span>' } } }
    })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('8.00')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('—')
    await wrapper.setProps({ account: {
      ...account, extra: { upstream_userinfo_remaining: 7, upstream_userinfo_max_budget: 10, upstream_userinfo_spend: 3,
        upstream_userinfo_updated_at: '2026-10-01T05:10:30Z' }
    } })
    expect(wrapper.text()).toContain('7.00')
    expect(getUserInfoQuota).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('shows read-only upstream RPM, concurrency, and TPM limits from the saved snapshot', () => {
    const account = {
      id: 1,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      extra: {
        upstream_userinfo_remaining: 7,
        upstream_userinfo_max_budget: 10,
        upstream_userinfo_spend: 3,
        upstream_userinfo_limits_known: true,
        upstream_userinfo_rpm_limit: 20,
        upstream_userinfo_max_parallel_requests: 4,
        upstream_userinfo_tpm_limit: 12000
      }
    } as any
    const wrapper = mount(UpstreamUserInfoQuotaCell, { props: { account } })

    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('20')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('4')
    expect(wrapper.get('[data-test="userinfo-upstream-limits"]').text()).toContain('12000')
    wrapper.unmount()
  })

  it('ignores a probe response after the account row changes', async () => {
    let resolveProbe!: (value: Record<string, unknown>) => void
    getUserInfoQuota.mockReturnValueOnce(new Promise((resolve) => {
      resolveProbe = resolve
    }))
    const account = { id: 1, type: 'apikey', credentials: { base_url: 'https://api.llm.ustc.edu.cn' }, extra: {} } as any
    const wrapper = mount(UpstreamUserInfoQuotaCell, {
      props: { account }, global: { stubs: { UsageProgressBar: true } }
    })

    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ account: { ...account, id: 2 } })
    resolveProbe({
      success: true,
      remaining: 8,
      max_budget: 10,
      spend: 2,
      valid: true,
      unit: 'CNY',
      limits_known: false,
      fetched_at: Date.now() / 1000
    })
    await flushPromises()

    expect(getUserInfoQuota).toHaveBeenCalledWith(1)
    expect(wrapper.text()).toContain('admin.accounts.userInfoQuota.empty')
    expect(wrapper.text()).not.toContain('8.00')
    wrapper.unmount()
  })
})
