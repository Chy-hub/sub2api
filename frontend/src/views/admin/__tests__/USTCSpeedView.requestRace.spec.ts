import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedView from '../USTCSpeedView.vue'

const { getOverview, getDimensions, getSettings, listTasks, getRuntime, showToast } = vi.hoisted(() => ({
  getOverview: vi.fn(),
  getDimensions: vi.fn(),
  getSettings: vi.fn(),
  listTasks: vi.fn(), getRuntime: vi.fn(),
  showToast: vi.fn(),
}))

vi.mock('@/api/admin/ustcSpeed', () => ({
  adminUSTCSpeedAPI: {
    getOverview,
    getDimensions,
    getSettings,
    listTasks,
    getRuntime,
  },
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showToast }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/admin/ustc-speed', query: {} }),
  useRouter: () => ({ push: vi.fn() }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh' } }) }
})
vi.mock('vue-chartjs', () => ({
  Chart: defineComponent({
    props: ['type', 'data', 'options'],
    template: '<div data-testid="speed-chart">{{ options.scales.y.title.text }}|{{ JSON.stringify(data.datasets) }}</div>',
  }),
}))

const AppLayoutStub = defineComponent({ template: '<main><slot /></main>' })

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function makeOverview(metric: string, value: number) {
  return {
    range_start: '2026-10-25T16:00:00Z',
    range_end: '2026-10-26T16:00:00Z',
    group_by: 'model',
    metric,
    unit: metric.endsWith('_ms') ? 'ms' : 'tps',
    interval: '1m',
    aggregate: 'mean',
    requested_source: 'real',
    fallback_applied: false,
    fallback_window_seconds: 0,
    summary: {
      samples: 1, successful: 1, failed: 0, rate_limited: 0, skipped: 0, cancelled: 0,
      probe_samples: 0, real_samples: 1, output_tokens: 10, success_rate: 100,
      latest_at: '2026-10-26T00:00:00Z', latest_real_at: '2026-10-26T00:00:00Z', latest_probe_at: null,
    },
    series: [{
      time: '2026-10-26T00:00:00Z', account_id: 1, account_name: 'Key 1', model: 'fixture-model',
      source: 'real', profile: '', round_id: '', count: 1, valid_count: 1,
      value, mean: value, p10: value, p50: value, p90: value, min: value, max: value,
    }],
    truncated: false,
    dropped_samples: 0,
  }
}

function mountView() {
  return mount(USTCSpeedView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        DateRangePicker: true,
        USTCSpeedTaskDialog: true,
        USTCSpeedSampleDialog: true,
        ConfirmDialog: true,
      },
    },
  })
}

async function waitForInitialOverview() {
  await flushPromises()
  expect(getOverview).toHaveBeenCalledTimes(1)
}

describe('USTCSpeedView overview request races', () => {
  beforeEach(() => {
    getOverview.mockReset()
    getDimensions.mockReset().mockResolvedValue({ accounts: [], models: ['fixture-model'] })
    getSettings.mockReset().mockResolvedValue({ capture_enabled: true, max_background_concurrency: 4 })
    listTasks.mockReset().mockResolvedValue({ items: [] })
    getRuntime.mockReset().mockResolvedValue({ items: [] })
    showToast.mockReset()
  })

  afterEach(() => vi.restoreAllMocks())

  it('keeps the newer metric result when older requests resolve afterward', async () => {
    const initialRequest = deferred<ReturnType<typeof makeOverview>>()
    const latestRequest = deferred<ReturnType<typeof makeOverview>>()
    getOverview.mockReturnValueOnce(initialRequest.promise).mockReturnValueOnce(latestRequest.promise)
    const wrapper = mountView()
    await waitForInitialOverview()

    const groupSelect = wrapper.findAll('select').find((select) => select.find('option[value="ttft"]').exists())!
    await groupSelect.setValue('ttft')
    expect(getOverview).toHaveBeenCalledTimes(2)
    expect(getOverview.mock.calls[1][0]).toMatchObject({ metric: 'ttft_ms' })
    expect(getOverview.mock.calls[0][1].signal.aborted).toBe(true)

    latestRequest.resolve(makeOverview('ttft_ms', 250))
    await flushPromises()
    initialRequest.resolve(makeOverview('generation_tps', 40))
    await flushPromises()

    const chart = wrapper.get('[data-testid="speed-chart"]').text()
    expect(chart).toContain('admin.ustcSpeed.metric.unitMs')
    expect(chart).toContain('"y":250')
    expect(chart).not.toContain('"y":40')
    expect((wrapper.vm as any).overview.metric).toBe('ttft_ms')
    expect((wrapper.vm as any).loading).toBe(false)
    wrapper.unmount()
  })

  it('requeries aggregate changes during loading and ignores an old finally', async () => {
    const initialRequest = deferred<ReturnType<typeof makeOverview>>()
    const aggregateRequest = deferred<ReturnType<typeof makeOverview>>()
    getOverview.mockReturnValueOnce(initialRequest.promise).mockReturnValueOnce(aggregateRequest.promise)
    const wrapper = mountView()
    await waitForInitialOverview()

    ;(wrapper.vm as any).filters.aggregate = 'p50'
    await nextTick()
    expect(getOverview).toHaveBeenCalledTimes(2)
    expect(getOverview.mock.calls[1][0]).toMatchObject({ aggregate: 'p50' })
    expect(getOverview.mock.calls[0][1].signal.aborted).toBe(true)
    expect((wrapper.vm as any).loading).toBe(true)

    initialRequest.resolve(makeOverview('generation_tps', 40))
    await flushPromises()
    expect((wrapper.vm as any).loading).toBe(true)
    expect((wrapper.vm as any).overview).toBeNull()

    aggregateRequest.resolve(makeOverview('generation_tps', 50))
    await flushPromises()
    expect((wrapper.vm as any).overview.series[0].value).toBe(50)
    expect((wrapper.vm as any).loading).toBe(false)
    wrapper.unmount()
  })

  it('aborts and invalidates an in-flight request on unmount without showing an error toast', async () => {
    const request = deferred<ReturnType<typeof makeOverview>>()
    getOverview.mockReturnValueOnce(request.promise)
    const wrapper = mountView()
    await waitForInitialOverview()
    const signal = getOverview.mock.calls[0][1].signal as AbortSignal

    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    request.reject(new Error('late failure'))
    await flushPromises()
    expect(showToast).not.toHaveBeenCalled()
  })

  it('does not start an overview request when mount dependencies finish after unmount', async () => {
    const dimensionsRequest = deferred<{ accounts: []; models: string[] }>()
    const settingsRequest = deferred<{ capture_enabled: boolean; max_background_concurrency: number }>()
    getDimensions.mockReturnValueOnce(dimensionsRequest.promise)
    getSettings.mockReturnValueOnce(settingsRequest.promise)
    const wrapper = mountView()
    wrapper.unmount()

    dimensionsRequest.resolve({ accounts: [], models: ['fixture-model'] })
    settingsRequest.resolve({ capture_enabled: true, max_background_concurrency: 4 })
    await flushPromises()

    expect(getOverview).not.toHaveBeenCalled()
  })
})
