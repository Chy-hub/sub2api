import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ReverseAccountOperations from '../ReverseAccountOperations.vue'

const mocks = vi.hoisted(() => ({ quota: vi.fn(), tasks: vi.fn(), run: vi.fn(), checkin: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getReverseQuota: mocks.quota, getReverseTasks: mocks.tasks, runReverseTask: mocks.run, checkinAccount: mocks.checkin }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const button = (wrapper: ReturnType<typeof mount>, key: string) => wrapper.findAll('button').find(item => item.text() === `admin.accounts.reverse.${key}`)!

beforeEach(() => { vi.clearAllMocks() })

describe('Reverse account operations', () => {
  it('performs no upstream operations on mount and shows unsuccessful check-in as a failure', async () => {
    const wrapper = mount(ReverseAccountOperations, { props: { accountId: 3, platform: 'qoder_global' } })
    expect(mocks.quota).not.toHaveBeenCalled()
    expect(mocks.checkin).not.toHaveBeenCalled()
    mocks.checkin.mockResolvedValue({ result: { success: false, message: 'No claimable campaign' } })
    await button(wrapper, 'checkinNow').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('No claimable campaign')
    expect(mocks.run).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('separates task inspection from execution and keeps partial failure details', async () => {
    mocks.tasks.mockResolvedValue({ specs: [{ kind: 'travel', label: 'travel', default_hours: [9, 21] }], inspection: { items: [{ code: 'chat_5', title: 'Chat', current: 0, target: 5, uses_chat: true }] }, status: {} })
    mocks.run.mockResolvedValue({ success: false, finished_at: '2026-10-29', results: [{ code: 'chat_5', status: 'error', message: 'Claim rejected' }] })
    const wrapper = mount(ReverseAccountOperations, { props: { accountId: 3, platform: 'workbuddy_cn' } })
    await button(wrapper, 'scanTasks').trigger('click')
    await flushPromises()
    expect(mocks.run).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.accounts.reverse.usesChat')
    await button(wrapper, 'runAllTasks').trigger('click')
    await flushPromises()
    expect(mocks.run).toHaveBeenCalledWith(3, { action: 'all' })
    expect(wrapper.text()).toContain('Claim rejected')
    expect(wrapper.text()).toContain('admin.accounts.reverse.operationFailed')
    wrapper.unmount()
  })

  it('discards a pending quota response after switching accounts', async () => {
    let resolve!: (value: unknown) => void
    mocks.quota.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mount(ReverseAccountOperations, { props: { accountId: 3, platform: 'qoder_global' } })
    await button(wrapper, 'queryQuota').trigger('click')
    await wrapper.setProps({ accountId: 4 })
    resolve({ subscription: { tierName: 'Previous account secret tier' } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('Previous account secret tier')
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(mocks.quota).toHaveBeenCalledWith(3)
    wrapper.unmount()
  })
})
