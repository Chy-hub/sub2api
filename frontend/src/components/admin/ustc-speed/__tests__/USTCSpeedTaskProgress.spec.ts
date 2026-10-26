import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import USTCSpeedTaskProgress from '../USTCSpeedTaskProgress.vue'
import type { USTCSpeedTask } from '@/api/admin/ustcSpeed'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: unknown) => `${key}${values ? JSON.stringify(values) : ''}` }) }))
const now = Date.parse('2026-10-26T04:00:30Z')
const task = { id: 7, last_status: 'running', lease_until: '2026-10-26T04:01:00Z', progress: { run_id: 'run', state: 'running', started_at: '2026-10-26T04:00:00Z', updated_at: '2026-10-26T04:00:29Z', total: 4, completed: 1, successful: 1, failed: 0, skipped: 0, cancelled: 0, active: [{ account_id: 2, account_name: 'Key 2', model: 'model', phase: 'requesting', started_at: '2026-10-26T04:00:02Z' }] } } as USTCSpeedTask
function render(value: USTCSpeedTask) { return mount(USTCSpeedTaskProgress, { props: { task: value, now, reasonLabel: (reason: string) => `Reason: ${reason}` } }) }
describe('benchmark execution progress', () => {
  it('distinguishes preparation from in-flight test work', async () => {
    const wrapper = render({ ...task, progress: { ...task.progress!, state: 'preparing', total: 0, completed: 0, active: [] } })
    expect(wrapper.get('[data-testid="task-run-state"]').text()).toContain('progress.preparing')
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    await wrapper.setProps({ task })
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('1')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuemax')).toBe('4')
    expect(wrapper.text()).toContain('Key 2 · model')
    expect(wrapper.text()).toContain('"active":1,"pending":2')
    wrapper.unmount()
  })
  it('does not report stale or expired execution as currently active', async () => {
    const wrapper = render(task)
    await wrapper.setProps({ now: now + 16000 })
    expect(wrapper.get('[data-testid="task-run-state"]').text()).toContain('progress.stale')
    expect(wrapper.text()).not.toContain('Key 2')
    await wrapper.setProps({ now, task: { ...task, lease_until: '2026-10-26T04:00:20Z' } })
    expect(wrapper.text()).toContain('progress.staleHint')
    wrapper.unmount()
  })
  it('retains final counters, reasons and a fixed elapsed time after completion', async () => {
    const wrapper = render({ ...task, last_status: 'partial', progress: { ...task.progress!, state: 'partial', completed: 4, successful: 2, failed: 1, skipped: 1, reason: 'upstream_network_error×1', active: [], finished_at: '2026-10-26T04:00:12Z' } })
    expect(wrapper.text()).toContain('status.partial')
    expect(wrapper.text()).toContain('"seconds":12')
    expect(wrapper.text()).toContain('Reason: upstream_network_error×1')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('4')
    expect(wrapper.text()).not.toContain('progress.stale')
    await wrapper.setProps({ task: { ...task, last_status: 'running', progress: { ...task.progress!, state: 'completed', completed: 4, active: [], finished_at: '2026-10-26T04:00:12Z' } } })
    expect(wrapper.get('[data-testid="task-run-state"]').text()).toContain('status.completed')
    expect(wrapper.text()).not.toContain('progress.running')
    wrapper.unmount()
  })
})
