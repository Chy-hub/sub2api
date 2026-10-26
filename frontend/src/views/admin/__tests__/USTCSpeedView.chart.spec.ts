import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedView from '../USTCSpeedView.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'

const { getOverview, getDimensions, getSettings, showToast } = vi.hoisted(() => ({
  getOverview: vi.fn(),
  getDimensions: vi.fn(),
  getSettings: vi.fn(),
  showToast: vi.fn(),
}))

vi.mock('@/api/admin/ustcSpeed', () => ({
  adminUSTCSpeedAPI: {
    getOverview,
    getDimensions,
    getSettings,
    listTasks: vi.fn().mockResolvedValue({ items: [] }),
    getRuntime: vi.fn().mockResolvedValue({ items: [] }),
  },
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showToast }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/admin/ustc-speed', query: {} }),
  useRouter: () => ({ push: vi.fn() }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'dates.today': 'Today',
    'dates.yesterday': 'Yesterday',
    'dates.last24Hours': 'Last 24 hours',
    'dates.last7Days': 'Last 7 days',
    'dates.last14Days': 'Last 14 days',
    'dates.last30Days': 'Last 30 days',
    'dates.thisMonth': 'This month',
    'dates.lastMonth': 'Last month',
    'dates.startDate': 'Start date',
    'dates.endDate': 'End date',
    'dates.apply': 'Apply',
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
      locale: { value: 'en' },
    }),
  }
})
vi.mock('vue-chartjs', () => ({
  Chart: defineComponent({
    props: ['type', 'data', 'options'],
    template: '<div data-testid="speed-chart" :data-chart-type="type" :data-x-min="options.scales.x.min" :data-x-max="options.scales.x.max" :data-y-unit="options.scales.y.title.text">{{ JSON.stringify(data.datasets) }}</div>',
  }),
}))

const AppLayoutStub = defineComponent({ template: '<main><slot /></main>' })

function localDate(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function shiftDate(value: string, days: number): string {
  const date = new Date(`${value}T12:00:00`)
  date.setDate(date.getDate() + days)
  return localDate(date)
}

function atShanghaiStart(value: string): number {
  return Date.parse(`${value}T00:00:00+08:00`)
}

function makePoint(overrides: Record<string, unknown> = {}) {
  return {
    time: '2026-10-25T04:00:00Z',
    account_id: 0,
    account_name: '',
    model: 'fixture-model',
    source: 'real',
    profile: '',
    round_id: '',
    count: 2,
    valid_count: 2,
    value: 42,
    mean: 42,
    p10: 40,
    p50: 42,
    p90: 44,
    min: 40,
    max: 44,
    ...overrides,
  }
}

function makeOverview(query: any) {
  let rangeStart = atShanghaiStart(query.start_date)
  let endExclusive = atShanghaiStart(shiftDate(query.end_date, 1))
  if (query.last_hours) {
    endExclusive = Date.now() + 1
    rangeStart = endExclusive - Number(query.last_hours) * 60 * 60 * 1000
  }
  const startISO = new Date(rangeStart).toISOString()
  const endISO = new Date(endExclusive).toISOString()
  const today = localDate(new Date())
  const yesterday = shiftDate(today, -1)
  const stablePointTime = new Date(atShanghaiStart(yesterday) + 12 * 60 * 60 * 1000).toISOString()
  let series = [makePoint({ time: stablePointTime })]
  if (query.source === 'all' && query.interval !== 'raw') {
    series = [
      makePoint({ time: stablePointTime, source: 'real', count: 8, valid_count: 8, value: 38, mean: 38 }),
      makePoint({ time: stablePointTime, source: 'probe', count: 3, valid_count: 3, value: 52, mean: 52 }),
    ]
  }
  if (query.interval === 'raw') {
    series = [
      makePoint({ time: stablePointTime, sample_id: 101, account_id: 1, source: 'real', value: 38 }),
      makePoint({ time: stablePointTime, sample_id: 102, account_id: 2, source: 'real', value: 40 }),
      ...(query.source === 'all' || query.source === 'probe'
        ? [makePoint({ time: stablePointTime, sample_id: 201, account_id: 1, source: 'probe', value: 52 })]
        : []),
    ]
  }
  const hourly = query.hourly
    ? series.filter((point) => point.value !== null).map((point) => ({
        hour: Number(new Intl.DateTimeFormat('en-US', { timeZone: query.timezone || 'UTC', hour: 'numeric', hourCycle: 'h23' }).format(new Date(point.time))),
        model: point.model,
        source: point.source,
        profile: point.profile,
        count: point.count,
        valid_count: point.valid_count,
        day_count: 1,
        value: point.value,
        mean: point.mean,
        p10: point.p10,
        p50: point.p50,
        p90: point.p90,
        min: point.min,
        max: point.max,
      }))
    : []
  return {
    range_start: startISO,
    range_end: endISO,
    group_by: query.group_by ?? 'model',
    metric: query.metric,
    unit: query.metric?.endsWith('tps') ? 'tps' : 'ms',
    interval: query.interval === 'auto' ? '5m' : query.interval,
    aggregate: query.aggregate,
    requested_source: query.source,
    fallback_applied: false,
    fallback_window_seconds: 0,
    summary: {
      samples: series.reduce((sum, point) => sum + point.count, 0),
      successful: series.reduce((sum, point) => sum + point.valid_count, 0),
      failed: 0, rate_limited: 0, skipped: 0, cancelled: 0,
      probe_samples: series.filter((point) => point.source === 'probe').reduce((sum, point) => sum + point.count, 0),
      real_samples: series.filter((point) => point.source === 'real').reduce((sum, point) => sum + point.count, 0),
      output_tokens: 100, success_rate: 100,
      latest_at: endISO, latest_real_at: endISO, latest_probe_at: endISO,
    },
    series,
    hourly,
    truncated: false,
    dropped_samples: 0,
  }
}

function mountView() {
  return mount(USTCSpeedView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Icon: true,
        USTCSpeedTaskDialog: true,
        USTCSpeedSampleDialog: true,
        ConfirmDialog: true,
      },
    },
  })
}

async function waitForChart(wrapper: ReturnType<typeof mountView>) {
  await flushPromises()
  expect(wrapper.find('[data-testid="speed-chart"]').exists()).toBe(true)
}

function mainChart(wrapper: ReturnType<typeof mountView>) {
  return wrapper.get('[data-testid="speed-chart"][data-chart-type="bar"]')
}

function datasets(wrapper: ReturnType<typeof mountView>) {
  return JSON.parse(mainChart(wrapper).text()) as Array<{ type: string; label: string; distribution?: boolean; seriesIndex?: number; data: Array<{ x: number; y: number | null | number[] }> }>
}

function visibleDatasets(wrapper: ReturnType<typeof mountView>) {
  return datasets(wrapper).filter((dataset) => !dataset.distribution)
}

describe('USTCSpeedView mixed chart and applied date range', () => {
  beforeEach(() => {
    getOverview.mockReset().mockImplementation(async (query: any) => makeOverview(query))
    getDimensions.mockReset().mockResolvedValue({
      accounts: [
        { id: 1, key_account_id: 1, name: 'Key 1', models: ['fixture-model'] },
        { id: 2, key_account_id: 2, name: 'Key 2', models: ['fixture-model'] },
      ],
      models: ['fixture-model'],
    })
    getSettings.mockReset().mockResolvedValue({ capture_enabled: true, max_background_concurrency: 4 })
    showToast.mockReset()
  })

  afterEach(() => vi.restoreAllMocks())

  it('uses mean/model by default, sends selected accounts, and keeps no client account-average selector', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    expect(getOverview.mock.calls[0][0]).toMatchObject({ aggregate: 'mean', group_by: 'model', hourly: true, hourly_metric: 'generation_tps' })
    expect(getOverview.mock.calls[0][0].account_ids).toBeUndefined()
    expect(wrapper.findAll('select').some((select) => select.find('option[value="equal"]').exists())).toBe(false)
    expect(visibleDatasets(wrapper)[0].type).toBe('bar')
    const spread = datasets(wrapper).find((dataset) => dataset.distribution)
    expect(spread?.type).toBe('bar')
    expect(spread?.data[0].y).toEqual([40, 44])

    const accountSelector = wrapper.get('details.relative')
    await accountSelector.get('summary').trigger('click')
    const accountBoxes = accountSelector.findAll('input[type="checkbox"]')
    await accountBoxes[1].setValue(true)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(getOverview.mock.calls.at(-1)?.[0]).toMatchObject({ account_ids: '2', aggregate: 'mean', group_by: 'model' })
    expect((wrapper.vm as any).accountView).toBeUndefined()
    wrapper.unmount()
  })

  it('renders real buckets as bars, probe buckets as lines, and raw points as scatter without timestamp deduplication', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)
    const groupSourceSelect = wrapper.findAll('select').find((select) => select.find('option[value="all"]').exists())!
    await groupSourceSelect.setValue('all')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(visibleDatasets(wrapper).map((dataset) => dataset.type)).toEqual(['bar', 'line'])
    expect(datasets(wrapper).map((dataset) => dataset.label).join(' ')).toContain('fixture-model')

    const intervalSelect = wrapper.findAll('select').find((select) => select.find('option[value="raw"]').exists())!
    await intervalSelect.setValue('raw')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const rawDatasets = visibleDatasets(wrapper)
    expect(rawDatasets.map((dataset) => dataset.type)).toEqual(['scatter', 'scatter'])
    expect(rawDatasets[0].data).toHaveLength(2)
    expect(rawDatasets[0].data[0].x).toBe(rawDatasets[0].data[1].x)
    expect(rawDatasets[0].data.map((point) => point.y)).toEqual([38, 40])
    wrapper.unmount()
  })

  it('groups the applied range by local hour for choosing a time slot', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    const hourlyChart = wrapper.get('[data-testid="speed-chart"][data-chart-type="line"]')
    const hourly = JSON.parse(hourlyChart.text()) as Array<{ label: string; data: Array<number | null> }>
    expect(hourly).toHaveLength(1)
    expect(hourly[0].label).toContain('fixture-model')
    expect(hourly[0].data).toHaveLength(24)
    expect(hourly[0].data.filter((value) => value !== null)).toHaveLength(1)
    expect(hourly[0].data.filter((value) => value !== null)).toEqual([42])
    expect(hourlyChart.attributes('data-x-min')).toBeUndefined()
    wrapper.unmount()
  })

  it('lets the hourly view use end-to-end speed without changing the trend metric', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    await wrapper.get('[data-testid="hourly-metric"]').setValue('overall_tps')
    await flushPromises()

    expect(getOverview.mock.calls.at(-1)?.[0]).toMatchObject({
      metric: 'generation_tps',
      hourly: true,
      hourly_metric: 'overall_tps',
    })
    expect((wrapper.get('[data-testid="chart-metric"]').element as HTMLSelectElement).value).toBe('generation_tps')
    wrapper.unmount()
  })

  it('hides probe-only TTFT and time-stage metrics from real usage records', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    const metricSelect = wrapper.get('[data-testid="chart-metric"]')
    const groupSelect = wrapper.findAll('select').find((select) => select.find('option[value="ttft"]').exists())!

    await groupSelect.setValue('ttft')
    await flushPromises()
    expect(metricSelect.findAll('option').map((option) => option.attributes('value'))).toEqual(['ttft_ms'])

    await groupSelect.setValue('time')
    await flushPromises()
    expect(metricSelect.findAll('option').map((option) => option.attributes('value'))).toEqual(['total_ms', 'output_ms'])

    const sourceSelect = wrapper.findAll('select').find((select) => select.find('option[value="all"]').exists())!
    await sourceSelect.setValue('all')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(metricSelect.findAll('option').map((option) => option.attributes('value'))).toEqual([
      'total_ms', 'output_ms', 'headers_ms', 'tail_ms', 'max_gap_ms',
    ])
    expect(getOverview.mock.calls.at(-1)?.[0]).toMatchObject({ source: 'all', metric: 'total_ms' })

    await groupSelect.setValue('ttft')
    await flushPromises()
    expect(metricSelect.findAll('option').map((option) => option.attributes('value'))).toEqual(['ttft_ms', 'first_output_ms'])
    wrapper.unmount()
  })

  it('pins the single point axis to the returned range and moves it when only that range changes', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)
    const chart = mainChart(wrapper)
    const initialBounds = [Number(chart.attributes('data-x-min')), Number(chart.attributes('data-x-max'))]
    const initialData = datasets(wrapper)

    const picker = wrapper.findComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    const yesterday = picker.findAll('.date-picker-preset').find((button) => button.text() === 'Yesterday')!
    await yesterday.trigger('click')
    await picker.get('.date-picker-apply').trigger('click')
    await flushPromises()

    const query = getOverview.mock.calls.at(-1)?.[0]
    const date = localDate(new Date())
    const expectedYesterday = shiftDate(date, -1)
    expect(query).toMatchObject({ start_date: expectedYesterday, end_date: expectedYesterday })
    const updatedChart = wrapper.get('[data-testid="speed-chart"]')
    const updatedBounds = [Number(updatedChart.attributes('data-x-min')), Number(updatedChart.attributes('data-x-max'))]
    expect(updatedBounds).toEqual([atShanghaiStart(expectedYesterday), atShanghaiStart(shiftDate(expectedYesterday, 1))])
    expect(updatedBounds).not.toEqual(initialBounds)
    expect(datasets(wrapper)).toEqual(initialData)
    expect(updatedChart.attributes('data-y-unit')).toBe('admin.ustcSpeed.metric.unitTps')
    wrapper.unmount()
  })

  it('commits a pending picker date when the main Apply button is used', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    const picker = wrapper.findComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    const yesterday = picker.findAll('.date-picker-preset').find((button) => button.text() === 'Yesterday')!
    await yesterday.trigger('click')

    // Use the page-level Apply, not the picker's own Apply.
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const query = getOverview.mock.calls.at(-1)?.[0]
    const expected = shiftDate(localDate(new Date()), -1)
    expect(query).toMatchObject({ start_date: expected, end_date: expected })
    expect(query.last_hours).toBeUndefined()
    expect(picker.get('.date-picker-value').text()).toBe('Yesterday')
    wrapper.unmount()
  })

  it('applies the last-24-hour preset as an exact rolling window', async () => {
    const wrapper = mountView()
    await waitForChart(wrapper)

    const picker = wrapper.findComponent(DateRangePicker)
    await picker.get('.date-picker-trigger').trigger('click')
    const preset = picker.findAll('.date-picker-preset').find((button) => button.text() === 'Last 24 hours')!
    expect(preset).toBeTruthy()
    await preset.trigger('click')
    await picker.get('.date-picker-apply').trigger('click')
    await flushPromises()

    const query = getOverview.mock.calls.at(-1)?.[0]
    const today = localDate(new Date())
    expect(query).toMatchObject({ last_hours: 24, start_date: shiftDate(today, -1), end_date: today })
    const chart = mainChart(wrapper)
    const span = Number(chart.attributes('data-x-max')) - Number(chart.attributes('data-x-min'))
    expect(span).toBe(24 * 60 * 60 * 1000)
    expect(wrapper.text()).toContain('admin.ustcSpeed.overview.lastHours')
    wrapper.unmount()
  })
})
