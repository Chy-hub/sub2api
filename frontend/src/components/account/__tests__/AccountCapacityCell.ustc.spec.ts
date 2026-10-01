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
  it('shows upstream RPM, in-flight concurrency, available slots, and state', () => {
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
    expect(capacity.text()).toContain('16/30')
    expect(capacity.text()).toContain('2/3')
    expect(capacity.text()).toContain('"count":"1"')
    expect(capacity.text()).toContain('admin.accounts.capacity.ustc.state.ready')
    expect(capacity.text()).toContain('admin.accounts.capacity.ustc.resetAt')
    expect(capacity.attributes('title')).toContain('"rpm":"16/30"')
    expect(capacity.attributes('title')).toContain('"parallel":"2/3"')
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
      ustc_capacity: { rpm_limit: 20, parallel_limit: 20, used, in_flight, available: 0, state: 'ready' }
    } as any
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    expect(wrapper.get('[data-testid="ustc-capacity"]').text()).toContain(`admin.accounts.capacity.ustc.state.${state}`)
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
    }, '7/—']
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

    expect(capacity.text()).toContain(expected)
    expect(capacity.text()).not.toContain('∞')
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

    expect(wrapper.get('[data-testid="ustc-capacity"]').text()).toContain('7/∞')
    expect(wrapper.get('[data-testid="ustc-capacity"]').text()).toContain('2/∞')
    wrapper.unmount()
  })
})
