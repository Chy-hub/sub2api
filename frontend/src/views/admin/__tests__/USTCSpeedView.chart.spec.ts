import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedView from '../USTCSpeedView.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'

const { benchmark, usage, dimensions, settings, tasks, runtime, toast } = vi.hoisted(() => ({
  benchmark: vi.fn(), usage: vi.fn(), dimensions: vi.fn(), settings: vi.fn(), tasks: vi.fn(), runtime: vi.fn(), toast: vi.fn(),
}))
vi.mock('@/api/admin/ustcSpeed', () => ({ adminUSTCSpeedAPI: { getOverview: benchmark, getDimensions: dimensions, getSettings: settings, listTasks: tasks, getRuntime: runtime } }))
vi.mock('@/api/admin/usage', () => ({ getSpeedOverview: usage, adminUsageAPI: { getSpeedOverview: usage }, default: { getSpeedOverview: usage } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showToast: toast }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/admin/ustc-speed', query: {} }), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
vi.mock('vue-chartjs', () => ({ Chart: defineComponent({
  props: ['type', 'data', 'options'],
  template: '<div data-testid="speed-chart" :data-type="type" :data-min="options.scales.x.min" :data-max="options.scales.x.max" :data-unit="options.scales.y.title.text">{{ JSON.stringify(data.datasets) }}</div>',
}) }))

function response(q: any) {
  const point = (value: number, id?: number) => ({ time: '2026-10-25T04:00:00Z', account_id: id ? 2 : 0, account_name: '', model: 'model-a', source: q.source, profile: '', round_id: '', sample_id: id, count: id ? 1 : 2, valid_count: id ? 1 : 2, value, mean: value, p10: value - 2, p50: value, p90: value + 2, min: value - 2, max: value + 2 })
  const series = q.interval === 'raw' ? [point(40, 101), point(42, 102)] : [point(40)]
  const end = new Date(`${q.end_date}T00:00:00+08:00`); end.setUTCDate(end.getUTCDate() + 1)
  return { range_start: `${q.start_date}T00:00:00+08:00`, range_end: end.toISOString(), metric: q.metric, interval: q.interval === 'auto' ? '5m' : q.interval,
    aggregate: q.aggregate, group_by: 'model', requested_source: q.source, fallback_applied: false, fallback_window_seconds: 0,
    summary: { samples: 2, successful: q.source === 'probe' ? 2 : 0, failed: 0, skipped: 0, cancelled: 0, rate_limited: 0, real_samples: q.source === 'real' ? 2 : 0, probe_samples: q.source === 'probe' ? 2 : 0, output_tokens: 1024, latest_at: '2026-10-25T04:00:00Z' },
    series, hourly: [{ ...series[0], hour: 12, day_count: 2 }], truncated: false, dropped_samples: 0,
  }
}
const scopes = { start_date: '2026-10-25', end_date: '2026-10-26', timezone: 'Asia/Shanghai', user_id: 3, api_key_id: 8, account_id: 2, group_id: 5, model: 'model-a', request_type: 'stream' as const, billing_type: 1, native_compaction_v2: true }
const wrappers: ReturnType<typeof mount>[] = []
function render(embedded = false) {
  const wrapper = mount(USTCSpeedView, { props: embedded ? { embedded: true, usageFilters: scopes } : {}, global: { stubs: {
    AppLayout: defineComponent({ template: '<main><slot /></main>' }), USTCSpeedTaskDialog: true, USTCSpeedSampleDialog: true, ConfirmDialog: true,
  } } })
  wrappers.push(wrapper)
  return wrapper
}
function chart(wrapper: ReturnType<typeof render>) { return wrapper.get('[data-testid="speed-chart"][data-type="bar"]') }
function datasets(wrapper: ReturnType<typeof render>) { return JSON.parse(chart(wrapper).text()).filter((row: any) => !row.distribution) }

describe('separate usage and benchmark views', () => {
  beforeEach(() => {
    benchmark.mockReset().mockImplementation(async (q: any) => response(q))
    usage.mockReset().mockImplementation(async (q: any) => response(q))
    dimensions.mockReset().mockResolvedValue({ accounts: [{ id: 2, name: 'Account 2', models: ['model-a'] }], models: ['model-a'] })
    settings.mockReset().mockResolvedValue({ capture_enabled: false, max_background_concurrency: 4 })
    tasks.mockReset().mockResolvedValue({ items: [{ id: 7, name: 'Periodic check', models: ['model-a'], all_accounts: true, account_ids: [], interval_seconds: 1800, enabled: false }] })
    runtime.mockReset().mockResolvedValue({ items: [] })
    toast.mockReset()
  })
  afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.restoreAllMocks() })

  it('shows benchmark data and task management together and provides no real/mixed source or Time option', async () => {
    const wrapper = render()
    await flushPromises()
    expect(benchmark.mock.calls[0][0]).toMatchObject({ source: 'probe', aggregate: 'mean', group_by: 'model' })
    expect(usage).not.toHaveBeenCalled()
    expect(datasets(wrapper)[0].type).toBe('line')
    expect(wrapper.get('#benchmark-tasks').text()).toContain('Periodic check')
    const options = wrapper.findAll('select option').map((option) => option.attributes('value'))
    expect(options).not.toContain('real'); expect(options).not.toContain('probe'); expect(options).not.toContain('time')
    expect(wrapper.text()).not.toContain('admin.ustcSpeed.settings.captureEnabled')
  })

  it('uses the usage endpoint and passes the detail tab filters without loading benchmark settings', async () => {
    const wrapper = render(true)
    await flushPromises()
    expect(usage.mock.calls[0][0]).toMatchObject({ ...scopes, source: 'real', aggregate: 'mean', group_by: 'model' })
    expect(benchmark).not.toHaveBeenCalled(); expect(tasks).not.toHaveBeenCalled(); expect(dimensions).not.toHaveBeenCalled()
    expect(wrapper.find('#benchmark-tasks').exists()).toBe(false)
    expect(wrapper.findComponent(DateRangePicker).exists()).toBe(false)
    expect(datasets(wrapper)[0].type).toBe('bar')
    expect(wrapper.text()).not.toContain('admin.ustcSpeed.overview.outputTokens')
  })

  it('preserves real raw observations with identical timestamps', async () => {
    const wrapper = render(true)
    await flushPromises()
    const interval = wrapper.findAll('select').find((select) => select.find('option[value="raw"]').exists())!
    await interval.setValue('raw'); await flushPromises()
    const rows = datasets(wrapper)
    expect(rows[0].type).toBe('scatter')
    expect(rows[0].data).toHaveLength(2)
    expect(rows[0].data[0].x).toBe(rows[0].data[1].x)
    expect(rows[0].data.map((point: any) => point.y)).toEqual([40, 42])
  })

  it('updates the analysis and chart domain when the shared usage date/account filters change', async () => {
    const wrapper = render(true)
    await flushPromises()
    const before = chart(wrapper).attributes('data-min')
    await wrapper.setProps({ usageFilters: { ...scopes, start_date: '2026-10-19', end_date: '2026-10-20', account_id: 9 } })
    await flushPromises()
    expect(usage.mock.calls.at(-1)?.[0]).toMatchObject({ start_date: '2026-10-19', end_date: '2026-10-20', account_id: 9 })
    expect(chart(wrapper).attributes('data-min')).not.toBe(before)
    expect(Number(chart(wrapper).attributes('data-min'))).toBe(Date.parse('2026-10-19T00:00:00+08:00'))
  })

  it('offers TPS and TTFT only, with recorded first-token timing for usage', async () => {
    const wrapper = render(true)
    await flushPromises()
    const group = wrapper.findAll('select').find((select) => select.find('option[value="ttft"]').exists())!
    expect(group.findAll('option').map((option) => option.attributes('value'))).toEqual(['tps', 'ttft'])
    await group.setValue('ttft'); await flushPromises()
    expect(wrapper.get('[data-testid="chart-metric"]').findAll('option').map((option) => option.attributes('value'))).toEqual(['ttft_ms'])
    expect(usage.mock.calls.at(-1)?.[0].metric).toBe('ttft_ms')
    expect(chart(wrapper).attributes('data-unit')).toContain('unitMs')
  })

  it('keeps the hourly metric independent while the main chart shows TTFT', async () => {
    const wrapper = render()
    await flushPromises()
    const group = wrapper.findAll('select').find((select) => select.find('option[value="ttft"]').exists())!
    await group.setValue('ttft'); await flushPromises()
    await wrapper.get('[data-testid="hourly-metric"]').setValue('overall_tps'); await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ metric: 'ttft_ms', hourly_metric: 'overall_tps', source: 'probe' })
  })
})
