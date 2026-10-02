import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupCapacityBadge from '../GroupCapacityBadge.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key} ${values ? JSON.stringify(values) : ''}` })
}))

describe('GroupCapacityBadge', () => {
  it('marks partial USTC subtotals with question marks and neutral colors', () => {
    const wrapper = mount(GroupCapacityBadge, {
      props: {
        concurrencyUsed: 2,
        concurrencyMax: 5,
        concurrencyUsedIncompleteCount: 1,
        concurrencyMaxIncompleteCount: 1,
        sessionsUsed: 0,
        sessionsMax: 0,
        rpmUsed: 3,
        rpmMax: 10,
        rpmUsedIncompleteCount: 1,
        rpmMaxIncompleteCount: 0
      }
    })
    const badges = wrapper.findAll('.rounded-md')

    expect(wrapper.text().replace(/\s/g, '')).toContain('2+?/5+?')
    expect(wrapper.text().replace(/\s/g, '')).toContain('3+?/10')
    expect(badges[0].classes()).toContain('bg-gray-100')
    expect(badges[1].classes()).toContain('bg-gray-100')
    expect(badges[0].attributes('title')).toContain('capacityIncomplete.knownSubtotal')
    expect(badges[0].attributes('title')).toContain('limitsUnknown')
    expect(badges[0].attributes('title')).toContain('countersUnavailable')
    wrapper.unmount()
  })

  it('shows an RPM row when its known subtotal is zero but sources are incomplete', () => {
    const wrapper = mount(GroupCapacityBadge, {
      props: {
        concurrencyUsed: 0,
        concurrencyMax: 0,
        sessionsUsed: 0,
        sessionsMax: 0,
        rpmUsed: 0,
        rpmMax: 0,
        rpmMaxIncompleteCount: 1,
        rpmUsedIncompleteCount: 1
      }
    })

    expect(wrapper.text().replace(/\s/g, '')).toContain('0+?/0+?')
    wrapper.unmount()
  })
})
