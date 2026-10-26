import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import USTCSpeedTaskDialog from '../USTCSpeedTaskDialog.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const BaseDialogStub = defineComponent({
  props: { show: Boolean, title: String, width: String, closeOnClickOutside: Boolean },
  setup(_, { slots }) {
    return () => h('div', [slots.default?.(), slots.footer?.()])
  },
})

const account = { id: 1, key_account_id: 1, name: 'Key 1', models: ['fixture-model'] }

const makeTask = (intervalSeconds: number, maxIntervalSeconds: number, jitterSeconds = 180) => ({
  id: 7,
  name: 'Existing task',
  enabled: false,
  all_accounts: true,
  account_ids: [],
  models: ['fixture-model'],
  interval_seconds: intervalSeconds,
  jitter_seconds: jitterSeconds,
  cron_expression: '',
  timezone: 'Asia/Shanghai',
  active_start: '00:00',
  active_end: '23:59',
  days_of_week: [0, 1, 2, 3, 4, 5, 6],
  api_mode: 'chat_completions',
  stream: true,
  prompt: 'fixture',
  prompt_variants: [],
  max_tokens: 128,
  temperature: 0.2,
  top_p: null,
  seed: null,
  stop: [],
  thinking_mode: 'off',
  reasoning_effort: '',
  timeout_seconds: 60,
  first_output_timeout_seconds: 30,
  max_parallel_accounts: 4,
  max_start_skew_seconds: 10,
  reserve_rpm: 0,
  reserve_concurrency: 1,
  max_active_requests: 0,
  skip_if_user_waiting: true,
  backoff_after_429: 2,
  backoff_window_seconds: 600,
  backoff_multiplier: 2,
  max_interval_seconds: maxIntervalSeconds,
  pause_after_429: 6,
  recovery_successes: 1,
})

const mountDialog = (task: ReturnType<typeof makeTask>) => mount(USTCSpeedTaskDialog, {
  props: { show: true, task: task as any, accounts: [account], models: ['fixture-model'] },
  global: { stubs: { BaseDialog: BaseDialogStub } },
})

const getIntervalFields = (wrapper: ReturnType<typeof mountDialog>) => {
  const label = wrapper.findAll('label').find((item) => item.text().includes('admin.ustcSpeed.task.field.interval'))
  if (!label) throw new Error('interval editor was not rendered')
  return {
    input: label.get('input[type="number"]'),
    unit: label.get('select'),
  }
}

afterEach(() => vi.restoreAllMocks())

it('uses schedulable admission defaults when creating a task', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mount(USTCSpeedTaskDialog, {
    props: { show: true, task: null, accounts: [account], models: ['fixture-model'] },
    global: { stubs: { BaseDialog: BaseDialogStub } },
  })
  const field = (key: string) => wrapper.findAll('label').find((item) => item.text().includes(key))!
  await field('admin.ustcSpeed.task.field.name').get('input').setValue('New probe')
  await field('admin.ustcSpeed.task.allAccounts').get('input').setValue(true)
  await field('fixture-model').get('input[type="checkbox"]').setValue(true)
  await wrapper.get('button.btn-primary').trigger('click')
  expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({
    enabled: false,
    interval_seconds: 1800,
    max_start_skew_seconds: 5,
    max_parallel_accounts: 4,
    reserve_concurrency: 2,
  })
  expect(field('admin.ustcSpeed.task.field.maxStartSkew').get('input').attributes('max')).toBe('30')
})

it('preserves a non-minute interval and untouched jitter when saving an edit', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mountDialog(makeTask(1237, 7200, 180))
  const { input, unit } = getIntervalFields(wrapper)

  expect((input.element as HTMLInputElement).value).toBe('1237')
  expect((unit.element as HTMLSelectElement).value).toBe('seconds')
  await wrapper.get('button.btn-primary').trigger('click')

  expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({
    interval_seconds: 1237,
    max_interval_seconds: 7200,
    jitter_seconds: 180,
  })
})

it('raises the maximum interval when a longer interval is selected and keeps a larger maximum', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mountDialog(makeTask(1800, 7200))
  const { input, unit } = getIntervalFields(wrapper)

  await unit.setValue('hours')
  await input.setValue('6')
  await wrapper.get('button.btn-primary').trigger('click')
  const raised = wrapper.emitted('submit')?.[0]?.[0] as any
  expect(raised.interval_seconds).toBe(21600)
  expect(raised.max_interval_seconds).toBe(21600)

  await wrapper.setProps({ task: makeTask(1800, 36000) as any })
  const largerTaskFields = getIntervalFields(wrapper)
  await largerTaskFields.unit.setValue('hours')
  await largerTaskFields.input.setValue('6')
  await wrapper.get('button.btn-primary').trigger('click')
  const preserved = wrapper.emitted('submit')?.[1]?.[0] as any
  expect(preserved.interval_seconds).toBe(21600)
  expect(preserved.max_interval_seconds).toBe(36000)
})

it('clamps jitter below a one-minute interval when the interval is changed', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mountDialog(makeTask(1800, 7200, 180))
  const { input, unit } = getIntervalFields(wrapper)

  await unit.setValue('seconds')
  await input.setValue('60')
  await wrapper.get('button.btn-primary').trigger('click')
  const submitted = wrapper.emitted('submit')?.[0]?.[0] as any
  expect(submitted.interval_seconds).toBe(60)
  expect(submitted.jitter_seconds).toBe(59)
  expect(submitted.jitter_seconds).toBeLessThan(submitted.interval_seconds)
})

it('saves the task constraint policy without discarding its schedule or reserve values', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mountDialog(makeTask(1800, 7200))
  await wrapper.get('[data-testid="ignore-task-constraints"]').setValue(true)
  await wrapper.get('button.btn-primary').trigger('click')
  expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({
    ignore_task_constraints: true, interval_seconds: 1800, reserve_concurrency: 1,
  })
  wrapper.unmount()
})

it.each([
  ['maxTokens', 8], ['timeout', 2], ['firstOutputTimeout', 61],
  ['pauseAfter429', 1], ['maxInterval', 120],
])('rejects invalid %s before submitting even when browser validation is bypassed', async (key, value) => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const wrapper = mountDialog(makeTask(1800, 7200))
  const field = wrapper.findAll('label').find((item) => item.text().includes(`admin.ustcSpeed.task.field.${key}`))!
  await field.get('input[type="number"]').setValue(String(value))
  await wrapper.get('button.btn-primary').trigger('click')
  expect(wrapper.emitted('submit')).toBeUndefined()
  expect(wrapper.text()).toContain('admin.ustcSpeed.task.validation.range')
  wrapper.unmount()
})

it('uses server task defaults and presents empty legacy weekdays as every day', async () => {
  vi.spyOn(HTMLFormElement.prototype, 'reportValidity').mockReturnValue(true)
  const task = { ...makeTask(2700, 9000), days_of_week: [], temperature: 0, timeout_seconds: 90 }
  const wrapper = mount(USTCSpeedTaskDialog, { props: { show: true, task: null, defaults: task as any, accounts: [account], models: ['fixture-model'] }, global: { stubs: { BaseDialog: BaseDialogStub } } })
  await wrapper.get('button.btn-primary').trigger('click')
  expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({ interval_seconds: 2700, timeout_seconds: 90, temperature: 0, days_of_week: [0, 1, 2, 3, 4, 5, 6] })
  wrapper.unmount()
})
