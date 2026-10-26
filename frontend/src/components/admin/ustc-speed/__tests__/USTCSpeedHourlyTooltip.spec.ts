import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import USTCSpeedHourlyTooltip from '../USTCSpeedHourlyTooltip.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: unknown) => `${key}${JSON.stringify(values ?? {})}` }) }))
const wrappers: ReturnType<typeof mount>[] = []
const rows = Array.from({ length: 10 }, (_, i) => ({ key: String(i), label: `model-${i}-with-a-very-long-complete-name`, value: '40 tokens/s', detail: '12 samples · 3 days', color: '#3b82f6' }))
function render() {
  const wrapper = mount(USTCSpeedHourlyTooltip, { attachTo: document.body, global: { stubs: { Teleport: true } } })
  wrappers.push(wrapper)
  return wrapper
}
afterEach(() => { wrappers.splice(0).forEach((wrapper) => wrapper.unmount()); vi.useRealTimers() })
describe('complete hourly chart tooltip', () => {
  it('keeps every model and full name in a viewport-bounded scrollable panel', async () => {
    const wrapper = render()
    wrapper.vm.show(900, 700, '12:00–13:00', rows)
    await wrapper.vm.$nextTick()
    expect(wrapper.findAll('[data-testid="hourly-tooltip-row"]')).toHaveLength(10)
    expect(wrapper.text()).toContain(rows[9].label)
    expect(wrapper.text()).toContain('12 samples · 3 days')
    const panel = wrapper.get<HTMLElement>('[role="tooltip"]').element
    expect(parseFloat(panel.style.left)).toBeGreaterThanOrEqual(12)
    expect(parseFloat(panel.style.left) + parseFloat(panel.style.width)).toBeLessThanOrEqual(window.innerWidth - 12)
    expect(parseFloat(panel.style.maxHeight)).toBeLessThanOrEqual(window.innerHeight * 0.75)
    expect(wrapper.get('[data-testid="hourly-tooltip-scroll"]').classes()).toContain('overflow-y-auto')
  })
  it('stays open when moving from the chart into the panel and scrolling to later models', async () => {
    vi.useFakeTimers()
    const wrapper = render()
    wrapper.vm.show(500, 300, '12:00–13:00', rows)
    await wrapper.vm.$nextTick()
    wrapper.vm.hide()
    await wrapper.get('[role="tooltip"]').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(200)
    await wrapper.get('[data-testid="hourly-tooltip-scroll"]').trigger('scroll')
    expect(wrapper.findAll('[data-testid="hourly-tooltip-row"]')).toHaveLength(10)
    await wrapper.get('[role="tooltip"]').trigger('mouseleave')
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
  })
})
