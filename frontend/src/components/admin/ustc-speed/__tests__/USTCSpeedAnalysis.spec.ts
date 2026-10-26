import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import USTCSpeedAnalysis from '../USTCSpeedAnalysis.vue'
import type { USTCSpeedAnalysis as Analysis, USTCSpeedAnalysisStats as Stats } from '@/api/admin/ustcSpeed'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => key + (values ? JSON.stringify(values) : ''), locale: { value: 'en' } }) }))

vi.mock('vue-chartjs', () => ({
  Chart: defineComponent({ props: ['data', 'options', 'type'], template: '<div data-testid="chart-data">{{ JSON.stringify(data) }}</div>' }),
}))

function stats(period: 'current' | 'previous', count: number, mean: number): Stats {
  return { period, model: 'model-a', source: 'real', count, valid_count: count, mean, p10: mean / 2, p50: mean, p90: mean * 2, stddev: mean / 4, min: 0, max: mean * 3 }
}
function analysis(currentCount = 20, previousCount = 40): Analysis {
  return {
    current_start: '2026-10-26T00:00:00Z', current_end: '2026-10-26T12:00:00Z',
    previous_start: '2026-10-25T00:00:00Z', previous_end: '2026-10-25T12:00:00Z',
    comparison: 'previous', bucket_seconds: 360,
    groups: [stats('current', currentCount, 40), stats('previous', previousCount, 50)],
    distribution: (['current', 'previous'] as const).flatMap((period) => [0, 15].map((bin) => ({ period, model: 'model-a', source: 'real', bin, lower: bin * 10, upper: (bin + 1) * 10, count: (period === 'current' ? currentCount : previousCount) / 2 }))),
    lengths: [], trend: [],
  }
}
function render(data = analysis(), metric = 'generation_tps' as 'generation_tps' | 'ttft_ms') {
  return mount(USTCSpeedAnalysis, {
    props: { analysis: data, view: 'distribution', metric, aggregate: 'mean', timezone: 'UTC' },
  })
}

describe('USTC analysis interpretation', () => {
  it('normalizes different period volumes to shares and retains empty bins', () => {
    const wrapper = render()
    const chart = JSON.parse(wrapper.get('[data-testid="chart-data"]').text())
    expect(chart.datasets).toHaveLength(2)
    for (const dataset of chart.datasets) {
      expect(dataset.data).toHaveLength(16)
      expect(dataset.data[0]).toBe(50)
      expect(dataset.data[15]).toBe(50)
      expect(dataset.data.reduce((sum: number, n: number) => sum + n, 0)).toBe(100)
    }
    wrapper.unmount()
  })

  it('withholds quantiles, variability, and percentage changes when samples are too few', () => {
    const wrapper = render(analysis(4, 3))
    const cards = wrapper.get('.grid').findAll(':scope > div')
    expect(cards[0].text()).toContain('40')
    expect(cards[0].text()).not.toContain('-20.0%')
    for (const card of cards.slice(1)) expect(card.findAll('p')[1].text()).toBe('—')
    expect(wrapper.get('[role="status"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('treats lower latency as an improvement and uses P90 for the slow tail', () => {
    const wrapper = render(analysis(), 'ttft_ms')
    const cards = wrapper.get('.grid').findAll(':scope > div')
    expect(cards[0].get('.text-emerald-600').text()).toContain('-20.0%')
    expect(cards[2].text()).toContain('slowLatency')
    expect(cards[2].text()).toContain('80')
    wrapper.unmount()
  })

  it('keeps real and benchmark observations as distinct selectable populations', async () => {
    const data = analysis()
    data.groups.push({ ...stats('current', 10, 99), source: 'probe' })
    const wrapper = render(data)
    const subject = wrapper.get('[data-testid="analysis-subject"]')
    expect(subject.findAll('option')).toHaveLength(2)
    await subject.setValue(JSON.stringify(['model-a', 'probe']))
    const cards = wrapper.get('.grid').findAll(':scope > div')
    expect(cards[0].text()).toContain('99')
    expect(cards[0].text()).not.toContain('-20.0%')
    wrapper.unmount()
  })
})
