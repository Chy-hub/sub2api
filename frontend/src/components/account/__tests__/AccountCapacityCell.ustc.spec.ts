import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountCapacityCell from '../AccountCapacityCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, unknown>) => `${key} ${values ? JSON.stringify(values) : ''}`
    })
  }
})

describe('AccountCapacityCell USTC capacity', () => {
  it('shows compact upstream badges and keeps availability, state, and reset details in the tooltip', () => {
    const account = {
      id: 1,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 2,
      current_concurrency: 1,
      ustc_capacity: {
        rpm_limit: 30,
        parallel_limit: 3,
        used: 16,
        in_flight: 2,
        available: 1,
        reset_at: '2026-10-02T09:00:00Z',
        state: 'ready'
      }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    const capacity = wrapper.get('[data-testid="ustc-capacity"]')

    expect(wrapper.find('[data-testid="local-concurrency-capacity"]').exists()).toBe(false)
    expect(capacity.text().replace(/\s/g, '')).toContain('16/30')
    expect(capacity.text().replace(/\s/g, '')).toContain('2/3')
    expect(wrapper.find('[data-testid="ustc-capacity-status"]').exists()).toBe(false)
    expect(capacity.attributes('title')).toContain('"rpm":"16/30"')
    expect(capacity.attributes('title')).toContain('"parallel":"2/3"')
    expect(capacity.attributes('title')).toContain('"available":"1"')
    expect(capacity.attributes('title')).toContain('admin.accounts.capacity.ustc.state.ready')
    expect(capacity.attributes('title')).toContain('admin.accounts.capacity.ustc.resetAt')
    wrapper.unmount()
  })

  it('hides legacy manual OpenAI RPM fields for non-USTC hosts', () => {
    const account = {
      id: 2,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.openai.com/v1' },
      concurrency: 1,
      extra: { base_rpm: 15 }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })

    expect(wrapper.find('[data-testid="local-concurrency-capacity"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ustc-capacity"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('15')
    wrapper.unmount()
  })

  it('keeps USTC quota upstream accounts on local capacity display', () => {
    const account = {
      id: 8,
      platform: 'openai',
      type: 'upstream',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 3,
      current_concurrency: 1
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })

    expect(wrapper.find('[data-testid="ustc-capacity"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="local-concurrency-capacity"]').text()).toContain('1')
    wrapper.unmount()
  })

  it.each([
    [20, 0, 'rpm_wait'],
    [2, 20, 'parallel_wait']
  ])('shows the actual capacity wait when used=%s and in-flight=%s', (used, in_flight, state) => {
    const account = {
      id: 5,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 1,
      ustc_capacity: {
        rpm_limit: 20, parallel_limit: 20, used, in_flight, available: 0,
        reset_at: '2026-10-02T09:00:00Z', state: 'ready'
      }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    expect(wrapper.get('[data-testid="ustc-capacity-status"]').text()).toContain(`admin.accounts.capacity.ustc.stateShort.${state}`)
    const reset = wrapper.find('[data-testid="ustc-capacity-reset"]')
    expect(reset.exists()).toBe(state === 'rpm_wait')
    if (reset.exists()) expect(reset.text()).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    wrapper.unmount()
  })

  it.each([
    ['missing snapshot', undefined, '—/—'],
    ['unknown state', {
      rpm_limit: null,
      parallel_limit: null,
      used: 7,
      in_flight: 2,
      available: 0,
      state: 'unknown'
    }, '—/—']
  ])('shows unknown limits for %s', (_label, ustc_capacity, expected) => {
    const account = {
      id: 3,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 1,
      ustc_capacity
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    const capacity = wrapper.get('[data-testid="ustc-capacity"]')

    expect(capacity.text().replace(/\s/g, '')).toContain(expected)
    expect(capacity.text()).not.toContain('∞')
    wrapper.unmount()
  })

  it('shows unknown Redis counts as dashes while retaining known limits', () => {
    const account = {
      id: 9,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 1,
      ustc_capacity: {
        rpm_limit: 20,
        parallel_limit: 3,
        limits_known: true,
        counts_known: false,
        used: 0,
        in_flight: 0,
        available: 0,
        state: 'unknown'
      }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    const capacity = wrapper.get('[data-testid="ustc-capacity"]')

    expect(capacity.text().replace(/\s/g, '')).toContain('—/20')
    expect(capacity.text().replace(/\s/g, '')).toContain('—/3')
    expect(wrapper.get('[data-testid="ustc-concurrency-capacity"]').classes().join(' ')).toContain('bg-gray-100')
    expect(wrapper.get('[data-testid="ustc-rpm-capacity"]').classes().join(' ')).toContain('bg-gray-100')
    wrapper.unmount()
  })

  it('shows infinity only for an explicit null limit with known capacity metadata', () => {
    const account = {
      id: 4,
      platform: 'openai',
      type: 'apikey',
      credentials: { base_url: 'https://api.llm.ustc.edu.cn/v1' },
      concurrency: 1,
      ustc_capacity: {
        rpm_limit: null,
        parallel_limit: null,
        used: 7,
        in_flight: 2,
        available: 99,
        state: 'ready'
      }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })

    expect(wrapper.get('[data-testid="ustc-capacity"]').text().replace(/\s/g, '')).toContain('7/∞')
    expect(wrapper.get('[data-testid="ustc-capacity"]').text().replace(/\s/g, '')).toContain('2/∞')
    wrapper.unmount()
  })
})
