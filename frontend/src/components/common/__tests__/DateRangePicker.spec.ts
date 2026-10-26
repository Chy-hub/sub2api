import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

describe('DateRangePicker', () => {
  it('does not infer rolling 24 hours for a manually selected yesterday-through-today range', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 26, 15, 30))
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '2026-10-25', endDate: '2026-10-26', preset: 'last24Hours' },
      global: { stubs: { Icon: true } },
    })
    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.findAll('input[type="date"]')[0]!.setValue('2026-10-25')
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.at(-1)).toEqual([{ startDate: '2026-10-25', endDate: '2026-10-26', preset: null }])
    expect(wrapper.emitted('update:preset')?.at(-1)).toEqual([null])
    wrapper.unmount()
    vi.useRealTimers()
  })

  it('treats an uncontrolled yesterday-through-today value as calendar dates', () => {
    const now = new Date()
    const yesterday = new Date(now)
    yesterday.setDate(yesterday.getDate() - 1)
    const wrapper = mount(DateRangePicker, { props: { startDate: formatLocalDate(yesterday), endDate: formatLocalDate(now) }, global: { stubs: { Icon: true } } })
    expect(wrapper.text()).not.toContain('Last 24 Hours')
    wrapper.unmount()
  })

  it('uses an explicit rolling preset without inferring it from calendar dates', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatLocalDate(yesterday),
        preset: 'last24Hours',
        endDate: formatLocalDate(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const now = new Date()
    const today = formatLocalDate(now)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const nowAfterClick = new Date()
    const yesterdayAfterClick = new Date(nowAfterClick.getTime() - 24 * 60 * 60 * 1000)
    const expectedStart = formatLocalDate(yesterdayAfterClick)
    const expectedEnd = formatLocalDate(nowAfterClick)

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })
})
