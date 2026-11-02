import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountTasksView from '../AccountTasksView.vue'

const { listAccountTasks, listMySubmissions, submitAccount, showError, showSuccess } = vi.hoisted(() => ({
  listAccountTasks: vi.fn(),
  listMySubmissions: vi.fn(),
  submitAccount: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/accountTasks', () => ({
  listAccountTasks,
  listMySubmissions,
  submitAccount,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.count !== undefined ? `${key}:${params.count}` : key,
    }),
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = {
  name: 'BaseDialog',
  props: { show: Boolean, title: String },
  emits: ['close'],
  template: '<div v-if="show"><slot /></div>',
}
const IconStub = { name: 'Icon', template: '<span />' }
const PlatformIconStub = { name: 'PlatformIcon', props: { platform: String }, template: '<span />' }

const task = {
  id: 1,
  name: '贡献 OpenAI Key',
  description: '分享你的 OpenAI Key 换取订阅',
  platform: 'openai',
  account_types: ['apikey'],
  reward_days: 30,
  max_per_user: 1,
  verify_models: true,
  reward_group_id: 9
}

const submission = {
  id: 5,
  task_id: 1,
  account_id: 42,
  status: 'pending' as const,
  verification_status: 'passed' as const,
  model_count: 12,
  created_at: '2026-11-01T10:00:00Z'
}

function mountView() {
  return mount(AccountTasksView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        BaseDialog: BaseDialogStub,
        Icon: IconStub,
        PlatformIcon: PlatformIconStub
      }
    }
  })
}

describe('AccountTasksView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAccountTasks.mockResolvedValue([task])
    listMySubmissions.mockResolvedValue([])
  })

  it('renders available tasks with reward days', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listAccountTasks).toHaveBeenCalled()
    expect(wrapper.text()).toContain('贡献 OpenAI Key')
    expect(wrapper.text()).toContain('+30')
  })

  it('renders submission history with verification result', async () => {
    listMySubmissions.mockResolvedValue([submission])
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('accountTasks.status.pending')
    expect(wrapper.text()).toContain('accountTasks.verification.passed')
    expect(wrapper.text()).toContain('accountTasks.modelCount:12')
  })

  it('disables submit once the per-user limit is reached', async () => {
    listMySubmissions.mockResolvedValue([submission])
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('accountTasks.submit'))
    expect(button).toBeTruthy()
    expect(button!.attributes('disabled')).toBeDefined()
  })

  it('ignores rejected submissions when counting the limit', async () => {
    listMySubmissions.mockResolvedValue([{ ...submission, status: 'rejected' }])
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('accountTasks.submit'))
    expect(button!.attributes('disabled')).toBeUndefined()
  })

  it('submits credentials for the selected task', async () => {
    submitAccount.mockResolvedValue({ ...submission, id: 6 })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('button.btn-primary').trigger('click')

    const passwordInput = wrapper.find('input[type="password"]')
    expect(passwordInput.exists()).toBe(true)
    await passwordInput.setValue('sk-secret')

    const form = wrapper.find('form')
    await form.trigger('submit')
    await flushPromises()

    expect(submitAccount).toHaveBeenCalledWith(
      expect.objectContaining({
        task_id: 1,
        platform: 'openai',
        type: 'apikey',
        credentials: { api_key: 'sk-secret' }
      })
    )
    expect(showSuccess).toHaveBeenCalled()
  })

  it('reports load failures without breaking the view', async () => {
    listAccountTasks.mockRejectedValue(new Error('boom'))
    const wrapper = mountView()
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(wrapper.text()).toContain('accountTasks.emptyTitle')
  })
})
