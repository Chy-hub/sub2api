import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedSamples from '../USTCSpeedSamples.vue'
import type { USTCSpeedQuery, USTCSpeedSample, USTCSpeedTask } from '@/api/admin/ustcSpeed'

const { listSamples } = vi.hoisted(() => ({ listSamples: vi.fn() }))
vi.mock('@/api/admin/ustcSpeed', () => ({ adminUSTCSpeedAPI: { listSamples } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
const Table = defineComponent({ props: ['data', 'columns', 'loading'], emits: ['rowClick'], template: '<div><article v-for="row in data" :key="row.id" :data-record="row.id"><template v-for="column in columns"><slot :name="`cell-${column.key}`" :row="row" /></template></article></div>' })
const Pager = defineComponent({ props: ['page', 'pageSize', 'total'], emits: ['update:page', 'update:page-size'], template: '<div />' })
const query: USTCSpeedQuery = { start_date: '2026-10-25', end_date: '2026-10-26', last_hours: 24, timezone: 'Asia/Shanghai', source: 'probe', task_id: 7, account_ids: '2', model: 'model' }
const sample = { id: 101, task_id: 7, round_id: 'round-0', account_id: 2, account_name: 'Key 2', model: 'model', status: 'success', source: 'probe', started_at: '2026-10-26T04:00:00Z', stream: false, input_tokens_known: true, input_tokens: 12, token_count_known: true, output_tokens: 8, overall_tps: null, generation_tps: null, total_ms: 1000, ttft_ms: null } as USTCSpeedSample
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const wrapper = mount(USTCSpeedSamples, { props: { query, tasks: [{ id: 7, name: 'Periodic check' } as USTCSpeedTask], refreshVersion: 0, reasonLabel: (reason: string) => `Reason: ${reason}` }, global: { stubs: { DataTable: Table, Pagination: Pager } } })
  wrappers.push(wrapper)
  return wrapper
}
function deferred<T>() {
  let resolve!: (result: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
beforeEach(() => { listSamples.mockReset().mockResolvedValue({ items: [sample], total: 21 }) })
afterEach(() => { wrappers.splice(0).forEach((wrapper) => wrapper.unmount()) })

describe('benchmark test records', () => {
  it('shares scope filters, preserves recorded tokens and derives legacy non-stream overall speed', async () => {
    const wrapper = render()
    await flushPromises()
    expect(listSamples.mock.calls[0][0]).toEqual({ ...query, limit: 10, offset: 0 })
    const record = wrapper.get('[data-record="101"]')
    expect(record.text()).toContain('Periodic check')
    expect(record.text()).toContain('12 / 8')
    expect(record.text()).toContain('8')
    expect(record.text()).toContain('—')
    expect(record.text()).not.toContain('0 ms')
    await record.get('button').trigger('click')
    expect(wrapper.emitted('open')?.[0]).toEqual([101])
  })

  it('paginates, resets on filter changes, and rejects stale responses', async () => {
    const wrapper = render()
    await flushPromises()
    const old = deferred<{ items: USTCSpeedSample[]; total: number }>()
    listSamples.mockReturnValueOnce(old.promise)
    wrapper.getComponent(Pager).vm.$emit('update:page', 2)
    await flushPromises()
    expect(listSamples.mock.calls.at(-1)?.[0].offset).toBe(10)
    const oldSignal = listSamples.mock.calls.at(-1)?.[1].signal
    listSamples.mockResolvedValueOnce({ items: [{ ...sample, id: 202, account_id: 9 }], total: 1 })
    await wrapper.setProps({ query: { ...query, account_ids: '9', last_hours: undefined, start_date: '2026-10-01', end_date: '2026-10-02' } })
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    expect(listSamples.mock.calls.at(-1)?.[0]).toMatchObject({ account_ids: '9', offset: 0, last_hours: undefined })
    old.resolve({ items: [sample], total: 21 })
    await flushPromises()
    expect(wrapper.find('[data-record="101"]').exists()).toBe(false)
    expect(wrapper.find('[data-record="202"]').exists()).toBe(true)
  })

  it('keeps the current page on live updates and batches simultaneous filter/refresh changes', async () => {
    const wrapper = render()
    await flushPromises()
    wrapper.getComponent(Pager).vm.$emit('update:page', 2)
    await flushPromises()
    await wrapper.setProps({ refreshVersion: 1 })
    await flushPromises()
    expect(listSamples.mock.calls.at(-1)?.[0].offset).toBe(10)
    const count = listSamples.mock.calls.length
    await wrapper.setProps({ query: { ...query }, refreshVersion: 1 })
    await flushPromises()
    expect(listSamples).toHaveBeenCalledTimes(count)
    await wrapper.setProps({ query: { ...query, model: 'other' }, refreshVersion: 2 })
    await flushPromises()
    expect(listSamples).toHaveBeenCalledTimes(count + 1)
    expect(listSamples.mock.calls.at(-1)?.[0]).toMatchObject({ model: 'other', offset: 0 })
  })

  it('clears old-scope records on failures and aborts requests on unmount', async () => {
    const wrapper = render()
    await flushPromises()
    listSamples.mockRejectedValueOnce(new Error('network'))
    await wrapper.setProps({ query: { ...query, model: 'unavailable' } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('loadSamples')
    expect(wrapper.find('[data-record="101"]').exists()).toBe(false)
    listSamples.mockReturnValueOnce(new Promise(() => {}))
    await wrapper.setProps({ query: { ...query, model: 'pending' } })
    const signal = listSamples.mock.calls.at(-1)?.[1].signal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
  })
})
