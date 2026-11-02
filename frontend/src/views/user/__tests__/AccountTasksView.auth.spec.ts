import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountTasksView from '../AccountTasksView.vue'

const {
  listAccountTasks,
  listMySubmissions,
  submitAccount,
  startSubmissionAuth,
  pollSubmissionAuth,
  completeSubmissionAuth,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccountTasks: vi.fn(),
  listMySubmissions: vi.fn(),
  submitAccount: vi.fn(),
  startSubmissionAuth: vi.fn(),
  pollSubmissionAuth: vi.fn(),
  completeSubmissionAuth: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/accountTasks', () => ({
  listAccountTasks,
  listMySubmissions,
  submitAccount,
  startSubmissionAuth,
  pollSubmissionAuth,
  completeSubmissionAuth
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.count !== undefined ? `${key}:${params.count}` : key
    })
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = {
  name: 'BaseDialog',
  props: { show: Boolean, title: String },
  emits: ['close'],
  template: '<div v-if="show"><slot /></div>'
}
const IconStub = { name: 'Icon', template: '<span />' }
const PlatformIconStub = { name: 'PlatformIcon', props: { platform: String }, template: '<span />' }

function baseTask(overrides: Record<string, unknown>) {
  return {
    id: 1,
    name: '任务',
    description: '',
    platform: 'openai',
    account_types: ['apikey'],
    reward_days: 30,
    max_per_user: 1,
    verify_models: true,
    ...overrides
  }
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

async function openSubmit(wrapper: ReturnType<typeof mountView>) {
  const button = wrapper
    .findAll('button')
    .find((item) => item.text().includes('accountTasks.submit'))
  expect(button).toBeTruthy()
  await button!.trigger('click')
  await flushPromises()
}

describe('AccountTasksView submission modes', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAccountTasks.mockResolvedValue([])
    listMySubmissions.mockResolvedValue([])
  })

  it('passes the reverse login ticket to the completion request', async () => {
    vi.useFakeTimers()
    try {
      listAccountTasks.mockResolvedValue([
        baseTask({ platform: 'qoder_cn', account_types: ['reverse'] })
      ])
      startSubmissionAuth.mockResolvedValue({
        mode: 'reverse',
        session_id: 'session-1',
        auth_url: 'https://qoder.example/login'
      })
      pollSubmissionAuth.mockResolvedValue({
        pending: false,
        nickname: 'tester',
        login_ticket: 'ticket-1'
      })
      completeSubmissionAuth.mockResolvedValue({ id: 2 })

      const wrapper = mountView()
      await flushPromises()
      await openSubmit(wrapper)

      const startButton = wrapper
        .findAll('button')
        .find((item) => item.text().includes('accountTasks.startAuth'))
      expect(startButton).toBeTruthy()
      await startButton!.trigger('click')
      await flushPromises()

      await vi.advanceTimersByTimeAsync(2500)
      await flushPromises()

      await wrapper.find('form').trigger('submit')
      await flushPromises()

      expect(completeSubmissionAuth).toHaveBeenCalledWith(
        expect.objectContaining({
          task_id: 1,
          account_type: 'reverse',
          session_id: 'session-1',
          ticket: 'ticket-1'
        })
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it('submits an OAuth code without waiting for reverse polling', async () => {
    listAccountTasks.mockResolvedValue([
      baseTask({ platform: 'openai', account_types: ['oauth'] })
    ])
    startSubmissionAuth.mockResolvedValue({
      mode: 'oauth',
      session_id: 'session-2',
      auth_url: 'https://openai.example/authorize',
      state: 'state-2'
    })
    completeSubmissionAuth.mockResolvedValue({ id: 3 })

    const wrapper = mountView()
    await flushPromises()
    await openSubmit(wrapper)

    const startButton = wrapper
      .findAll('button')
      .find((item) => item.text().includes('accountTasks.startAuth'))
    expect(startButton).toBeTruthy()
    await startButton!.trigger('click')
    await flushPromises()

    const textInputs = wrapper.findAll('input[type="text"]')
    // 账号名称 + 授权码
    await textInputs[1].setValue('code-2')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(completeSubmissionAuth).toHaveBeenCalledWith(
      expect.objectContaining({
        task_id: 1,
        account_type: 'oauth',
        session_id: 'session-2',
        code: 'code-2',
        state: 'state-2'
      })
    )
  })

  it('builds Vertex service account credentials from pasted JSON', async () => {
    listAccountTasks.mockResolvedValue([
      baseTask({ platform: 'gemini', account_types: ['service_account'] })
    ])
    submitAccount.mockResolvedValue({ id: 2 })

    const wrapper = mountView()
    await flushPromises()
    await openSubmit(wrapper)

    const textarea = wrapper.find('textarea')
    expect(textarea.exists()).toBe(true)
    await textarea.setValue(
      JSON.stringify({
        project_id: 'proj-1',
        client_email: 'svc@proj.iam.gserviceaccount.com',
        private_key: '-----BEGIN PRIVATE KEY-----'
      })
    )

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(submitAccount).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: 'gemini',
        type: 'service_account',
        credentials: expect.objectContaining({
          project_id: 'proj-1',
          client_email: 'svc@proj.iam.gserviceaccount.com',
          location: 'global',
          tier_id: 'vertex'
        })
      })
    )
  })

  it('builds AWS SigV4 credentials for bedrock accounts', async () => {
    listAccountTasks.mockResolvedValue([
      baseTask({ platform: 'anthropic', account_types: ['bedrock'] })
    ])
    submitAccount.mockResolvedValue({ id: 2 })

    const wrapper = mountView()
    await flushPromises()
    await openSubmit(wrapper)

    const textInputs = wrapper.findAll('input[type="text"]')
    // 账号名称 + Access Key ID + Region
    await textInputs[1].setValue('AKIAEXAMPLE')
    const passwordInputs = wrapper.findAll('input[type="password"]')
    await passwordInputs[0].setValue('secret-key')
    await textInputs[2].setValue('us-west-2')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(submitAccount).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: 'anthropic',
        type: 'bedrock',
        credentials: expect.objectContaining({
          auth_mode: 'sigv4',
          aws_region: 'us-west-2',
          aws_access_key_id: 'AKIAEXAMPLE',
          aws_secret_access_key: 'secret-key'
        })
      })
    )
  })
})
