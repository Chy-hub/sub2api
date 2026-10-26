import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedView from '../USTCSpeedView.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import UsageStatsCards from '@/components/admin/usage/UsageStatsCards.vue'

const { benchmark, usage, dimensions, settings, tasks, runtime, toast, createTask } = vi.hoisted(() => ({
  benchmark: vi.fn(), usage: vi.fn(), dimensions: vi.fn(), settings: vi.fn(), tasks: vi.fn(), runtime: vi.fn(), toast: vi.fn(), createTask: vi.fn(),
}))
vi.mock('@/api/admin/ustcSpeed', () => ({ adminUSTCSpeedAPI: { getOverview: benchmark, getDimensions: dimensions, getSettings: settings, listTasks: tasks, getRuntime: runtime, createTask, listSamples: vi.fn().mockResolvedValue({ items: [], total: 0 }) } }))
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
  const end = q.last_hours ? new Date() : new Date(`${q.end_date}T00:00:00+08:00`)
  if (!q.last_hours) end.setUTCDate(end.getUTCDate() + 1)
  const start = q.last_hours ? new Date(end.getTime() - q.last_hours * 3600000).toISOString() : `${q.start_date}T00:00:00+08:00`
  return { range_start: start, range_end: end.toISOString(), metric: q.metric, interval: q.interval === 'auto' ? '15m' : q.interval,
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
  afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.restoreAllMocks(); vi.useRealTimers() })

  it('shows benchmark data and task management together and provides no real/mixed source or Time option', async () => {
    const wrapper = render()
    await flushPromises()
    expect(benchmark.mock.calls[0][0]).toMatchObject({ source: 'probe', aggregate: 'mean', group_by: 'model', metric: 'overall_tps', hourly_metric: 'overall_tps', last_hours: 24, interval: 'auto' })
    expect(wrapper.getComponent(DateRangePicker).text()).toContain('dates.last24Hours')
    expect(wrapper.get<HTMLSelectElement>('[data-testid="chart-metric"]').element.value).toBe('overall_tps')
    expect(wrapper.get<HTMLSelectElement>('[data-testid="hourly-metric"]').element.value).toBe('overall_tps')
    expect(Number(chart(wrapper).attributes('data-max')) - Number(chart(wrapper).attributes('data-min'))).toBe(24 * 3600000)
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
    expect(usage.mock.calls[0][0]).toMatchObject({ ...scopes, source: 'real', aggregate: 'mean', group_by: 'model', metric: 'overall_tps', hourly_metric: 'overall_tps', last_hours: undefined })
    expect(benchmark).not.toHaveBeenCalled(); expect(tasks).not.toHaveBeenCalled(); expect(dimensions).not.toHaveBeenCalled()
    expect(wrapper.find('#benchmark-tasks').exists()).toBe(false)
    expect(wrapper.findComponent(DateRangePicker).exists()).toBe(false)
    expect(wrapper.findComponent(UsageStatsCards).exists()).toBe(false)
    expect(datasets(wrapper)[0].type).toBe('bar')
    expect(wrapper.text()).not.toContain('admin.ustcSpeed.overview.outputTokens')
  })

  it('reuses usage cards with filtered benchmark totals and preserves missing values', async () => {
    benchmark.mockImplementation(async (q: any) => ({ ...response(q), stats: q.account_ids ? {
      total_requests: 1, total_input_tokens: null, total_output_tokens: null, total_tokens: null, total_provider_cost: null, average_duration_ms: 1000, input_samples: 0, output_samples: 0, cost_samples: 0, duration_samples: 1,
    } : {
      total_requests: 3, total_input_tokens: 15, total_output_tokens: 20, total_tokens: 35, total_provider_cost: 0.1, average_duration_ms: 4000 / 3, input_samples: 2, output_samples: 2, cost_samples: 2, duration_samples: 3,
    } }))
    const wrapper = render()
    await flushPromises()
    const cards = wrapper.getComponent(UsageStatsCards)
    const values = () => cards.findAll('p.text-xl').map((node) => node.text())
    expect(values()).toEqual(['3', '35', '0.1000', '1.33s'])
    expect(cards.text()).toContain('stats.tokenCoverage')
    expect(cards.text()).toContain('stats.costCoverage')
    expect(cards.text()).not.toContain('usage.standardCost')
    await wrapper.get('[data-testid="benchmark-filters"] input[type="checkbox"]').setValue(true)
    await flushPromises()
    expect(values()).toEqual(['1', '—', '—', '1.00s'])
    expect(cards.text()).toContain('stats.costUnavailable')
    expect(cards.text()).not.toContain('$0.0000')
  })

  it.each([false, true])('shows all ten hourly model labels outside the canvas (embedded=%s)', async (embedded) => {
    const api = embedded ? usage : benchmark
    api.mockImplementation(async (q: any) => {
      const result = response(q)
      result.hourly = Array.from({ length: 10 }, (_, index) => ({ ...result.hourly[0], model: `model-${index}-complete-long-name`, hour: 12 }))
      return result
    })
    const wrapper = render(embedded)
    await flushPromises()
    const options = (wrapper.vm as any).hourlyChartOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    options.plugins.tooltip.external({ chart: { width: 600, height: 260, isDatasetVisible: () => true, canvas: { getBoundingClientRect: () => ({ left: 20, top: 100, width: 600, height: 260 }) } }, tooltip: { opacity: 1, caretX: 500, caretY: 100, dataPoints: Array.from({ length: 10 }, (_, datasetIndex) => ({ datasetIndex, dataIndex: 12 })) } })
    await flushPromises()
    expect(document.querySelectorAll('[data-testid="hourly-tooltip-row"]')).toHaveLength(10)
    expect(document.querySelector('[data-testid="hourly-tooltip"]')?.textContent).toContain('model-9-complete-long-name')
    if (embedded) {
      await wrapper.setProps({ active: false })
      expect(document.querySelector('[data-testid="hourly-tooltip"]')).toBeNull()
    } else {
      await wrapper.get('[data-testid="chart-metric"]').setValue('generation_tps')
      await flushPromises()
      expect(document.querySelector('[data-testid="hourly-tooltip"]')).toBeNull()
    }
  })

  it('includes missing-hour models as unavailable and respects hidden legend items', async () => {
    benchmark.mockImplementation(async (q: any) => {
      const result = response(q)
      result.hourly = Array.from({ length: 10 }, (_, index) => ({ ...result.hourly[0], model: `model-${index}`, hour: index < 5 ? 12 : 8 }))
      return result
    })
    const wrapper = render()
    await flushPromises()
    const external = (wrapper.vm as any).hourlyChartOptions.plugins.tooltip.external
    const chart = { width: 600, height: 260, isDatasetVisible: (index: number) => index !== 1, canvas: { getBoundingClientRect: () => ({ left: 20, top: 100, width: 600, height: 260 }) } }
    external({ chart, tooltip: { opacity: 1, caretX: 500, caretY: 100, dataPoints: [{ datasetIndex: 0, dataIndex: 12 }] } })
    await flushPromises()
    expect(document.querySelectorAll('[data-testid="hourly-tooltip-row"]')).toHaveLength(9)
    const last = Array.from(document.querySelectorAll('[data-testid="hourly-tooltip-row"]')).at(-1)!
    expect(last.textContent).toContain('model-9')
    expect(last.textContent).toContain('—')
    expect(last.textContent).toContain('hourly.noHourData')
    expect(document.querySelector('[data-testid="hourly-tooltip"]')?.textContent).not.toContain('model-1')
  })

  it('updates benchmark filters immediately with one request for each change and no Apply button', async () => {
    const wrapper = render()
    await flushPromises()
    const filters = wrapper.get('[data-testid="benchmark-filters"]')
    expect(filters.text()).not.toContain('admin.ustcSpeed.action.apply')
    const changes = [
      ['7', { task_id: 7 }], ['model-a', { model: 'model-a' }],
      ['on', { reasoning: 'on' }], ['false', { stream: 'false' }],
      ['success', { status: 'success' }], ['p90', { aggregate: 'p90' }],
    ] as const
    for (const [index, [value, query]] of changes.entries()) {
      const before = benchmark.mock.calls.length
      await filters.findAll('select')[index].setValue(value)
      await flushPromises()
      expect(benchmark).toHaveBeenCalledTimes(before + 1)
      expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ ...query, last_hours: 24 })
    }
    const before = benchmark.mock.calls.length
    await filters.get('input[type="checkbox"]').setValue(true)
    await flushPromises()
    expect(benchmark).toHaveBeenCalledTimes(before + 1)
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ account_ids: '2' })
  })

  it('shows normalized API validation messages when saving a task fails', async () => {
    createTask.mockRejectedValueOnce({ status: 400, message: 'invalid request timeouts' })
    const wrapper = render()
    await flushPromises()
    await (wrapper.vm as any).saveTask({ name: 'Probe' })
    expect(toast).toHaveBeenCalledWith('error', expect.stringContaining('invalid request timeouts'))
  })

  it('uses the actual calendar bucket end across a 25-hour DST day', async () => {
    const start = Date.parse('2026-11-01T00:00:00-04:00')
    const end = Date.parse('2026-11-02T00:00:00-05:00')
    benchmark.mockImplementation(async (q: any) => ({ ...response(q), interval: '1d', range_start: new Date(start).toISOString(), range_end: new Date(end).toISOString(), series: [{ ...response(q).series[0], time: new Date(start).toISOString(), bucket_end: new Date(end).toISOString() }] }))
    const wrapper = render()
    await flushPromises()
    expect(datasets(wrapper)[0].data[0].x).toBe(start + (end - start) / 2)
  })

  it('does not display conditional success and 429 rates when status is filtered', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.ustcSpeed.overview.successRate')
    await wrapper.get('[data-testid="benchmark-filters"]').findAll('select')[4]!.setValue('success')
    await flushPromises()
    expect(wrapper.text()).not.toContain('admin.ustcSpeed.overview.successRate')
    expect(wrapper.text()).not.toContain('admin.ustcSpeed.overview.rateLimitedRate')
  })

  it('leaves missing hours disconnected and gates tail percentiles by valid sample count', async () => {
    benchmark.mockImplementation(async (q: any) => {
      const out = response(q)
      out.hourly = [
        { ...out.hourly[0], hour: 10, valid_count: 19, p90: 42 },
        { ...out.hourly[0], hour: 12, valid_count: 20, p90: 44 },
      ]
      return out
    })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-testid="benchmark-filters"]').findAll('select')[5]!.setValue('p90')
    await flushPromises()
    const ds = (wrapper.vm as any).hourlyChartData.datasets[0]
    expect(ds.spanGaps).toBe(false)
    expect(ds.data[10]).toBeNull()
    expect(ds.data[11]).toBeNull()
    expect(ds.data[12]).toBe(44)
  })

  it('applies date presets and custom dates immediately, clearing and restoring the rolling window', async () => {
    const wrapper = render()
    await flushPromises()
    const picker = wrapper.getComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    expect(picker.find('.date-picker-apply').exists()).toBe(false)
    const before = benchmark.mock.calls.length
    await picker.findAll('.date-picker-preset').find((button) => button.text() === 'dates.last7Days')!.trigger('click')
    await flushPromises()
    expect(benchmark).toHaveBeenCalledTimes(before + 1)
    expect(benchmark.mock.calls.at(-1)?.[0].last_hours).toBeUndefined()
    await picker.get('.date-picker-trigger').trigger('click')
    await picker.findAll('input[type="date"]')[0].setValue('2026-01-01')
    await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ start_date: '2026-01-01', last_hours: undefined })
    await picker.findAll('input[type="date"]')[1].setValue('2026-01-02')
    await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ start_date: '2026-01-01', end_date: '2026-01-02', last_hours: undefined })
    const validRequestCount = benchmark.mock.calls.length
    await picker.findAll('input[type="date"]')[0].setValue('2026-01-03')
    await flushPromises()
    expect(benchmark).toHaveBeenCalledTimes(validRequestCount)
    await picker.findAll('input[type="date"]')[1].setValue('2026-01-04')
    await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ start_date: '2026-01-03', end_date: '2026-01-04' })
    await picker.findAll('.date-picker-preset').find((button) => button.text() === 'dates.last24Hours')!.trigger('click')
    await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0].last_hours).toBe(24)
    const rollingRequestCount = benchmark.mock.calls.length
    await picker.get('.date-picker-trigger').trigger('click')
    await picker.findAll('.date-picker-preset').find((button) => button.text() === 'dates.last24Hours')!.trigger('click')
    await flushPromises()
    expect(benchmark).toHaveBeenCalledTimes(rollingRequestCount + 1)
    expect(benchmark.mock.calls.at(-1)?.[0].last_hours).toBe(24)
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
    await group.setValue('tps'); await flushPromises()
    expect(usage.mock.calls.at(-1)?.[0].metric).toBe('overall_tps')
    await wrapper.get('[data-testid="chart-metric"]').setValue('generation_tps'); await flushPromises()
    expect(usage.mock.calls.at(-1)?.[0].metric).toBe('generation_tps')
  })

  it('keeps the hourly metric independent while the main chart shows TTFT', async () => {
    const wrapper = render()
    await flushPromises()
    const group = wrapper.findAll('select').find((select) => select.find('option[value="ttft"]').exists())!
    await group.setValue('ttft'); await flushPromises()
    await wrapper.get('[data-testid="hourly-metric"]').setValue('generation_tps'); await flushPromises()
    expect(benchmark.mock.calls.at(-1)?.[0]).toMatchObject({ metric: 'ttft_ms', hourly_metric: 'generation_tps', source: 'probe' })
  })

  it('polls active progress and refreshes results as test items complete', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-26T04:00:00Z'))
    const progress = { run_id: 'run', state: 'running', started_at: new Date().toISOString(), updated_at: new Date().toISOString(), total: 4, completed: 0, successful: 0, failed: 0, skipped: 0, cancelled: 0, active: [] }
    const task = { id: 7, name: 'Live test', models: ['model-a'], account_ids: [], all_accounts: true, enabled: true, interval_seconds: 1800, last_status: 'running', lease_until: '2026-10-26T04:01:00Z', progress }
    tasks.mockResolvedValue({ items: [task] })
    const wrapper = render()
    await flushPromises()
    expect(benchmark).toHaveBeenCalledTimes(1)
    tasks.mockResolvedValueOnce({ items: [{ ...task, progress: { ...progress, completed: 1, successful: 1, updated_at: '2026-10-26T04:00:02Z' } }] })
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('1')
    expect(benchmark).toHaveBeenCalledTimes(2)
    tasks.mockResolvedValueOnce({ items: [{ ...task, last_status: 'partial', progress: { ...progress, state: 'partial', completed: 4, successful: 2, skipped: 1, failed: 1, finished_at: '2026-10-26T04:00:04Z' } }] })
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('4')
    expect(wrapper.get('[data-testid="task-run-state"]').text()).toContain('status.partial')
    expect(benchmark).toHaveBeenCalledTimes(3)
  })
})
