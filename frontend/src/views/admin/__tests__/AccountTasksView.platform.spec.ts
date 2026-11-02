import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import AccountTasksView from '../AccountTasksView.vue'

const { listAccountTasks, listPendingSubmissions, createAccountTask, showError, showSuccess } =
  vi.hoisted(() => ({
    listAccountTasks: vi.fn(),
    listPendingSubmissions: vi.fn(),
    createAccountTask: vi.fn(),
    showError: vi.fn(),
    showSuccess: vi.fn()
  }))

vi.mock('@/api/admin/accountTasks', () => ({
  listAccountTasks,
  listPendingSubmissions,
  createAccountTask,
  updateAccountTask: vi.fn(),
  deleteAccountTask: vi.fn(),
  approveSubmission: vi.fn(),
  rejectSubmission: vi.fn()
}))

vi.mock('@/api/admin/groups', () => ({
  default: {
    getAllIncludingInactive: vi.fn().mockResolvedValue([
      { id: 9, name: 'Pro 订阅', platform: 'composite', subscription_type: 'subscription' }
    ])
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = {
  name: 'BaseDialog',
  props: { show: Boolean, title: String },
  emits: ['close'],
  template: '<div v-if="show"><slot /></div>'
}
const GroupSelectorStub = {
  name: 'GroupSelector',
  props: { modelValue: Array, groups: Array, platform: String },
  emits: ['update:modelValue'],
  template: '<div />'
}
const IconStub = { name: 'Icon', template: '<span />' }

function mountView() {
  return mount(AccountTasksView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        BaseDialog: BaseDialogStub,
        GroupSelector: GroupSelectorStub,
        Icon: IconStub
      }
    }
  })
}

/** 打开新建对话框 */
async function openCreate(wrapper: ReturnType<typeof mountView>) {
  const button = wrapper.findAll('button').find((item) => item.text().includes('createTask'))
  expect(button).toBeTruthy()
  await button!.trigger('click')
  await nextTick()
}

/** 读取当前可见的账号类型复选框标签 */
function visibleTypes(wrapper: ReturnType<typeof mountView>): string[] {
  return wrapper
    .findAll('input[type="checkbox"]')
    .map((input) => input.element as HTMLInputElement)
    .filter((el) => el.value === 'apikey' || el.value === 'reverse')
    .map((el) => el.value)
}

describe('AccountTasksView platform adaptation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAccountTasks.mockResolvedValue([])
    listPendingSubmissions.mockResolvedValue([])
  })

  it('offers only reverse for a reverse platform once selected', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)

    // 默认平台是 openai，应看到 apikey 而不是 reverse。
    expect(visibleTypes(wrapper)).toContain('apikey')
    expect(visibleTypes(wrapper)).not.toContain('reverse')

    // 切换到 Qoder CN。
    const platformSelect = wrapper.find('select')
    await platformSelect.setValue('qoder_cn')
    await nextTick()

    // 这是回归点：watch 曾在 form 之前注册，导致切平台不收敛类型，
    // 最终把 apikey 提交给只接受 reverse 的反代平台。
    expect(visibleTypes(wrapper)).toEqual(['reverse'])
  })

  it('submits reverse type after switching to a reverse platform', async () => {
    createAccountTask.mockResolvedValue({})
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)

    await wrapper.find('select').setValue('qoder_cn')
    await nextTick()

    const nameInput = wrapper.findAll('input[type="text"]')[0]
    await nameInput.setValue('Qoder 贡献')

    // 奖励分组是必填项；页面上只有两个 select：平台、奖励分组。
    const selects = wrapper.findAll('select')
    expect(selects).toHaveLength(2)
    await selects[1].setValue('9')
    await nextTick()

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(createAccountTask).toHaveBeenCalledWith(
      expect.objectContaining({
        platform: 'qoder_cn',
        account_types: ['reverse']
      })
    )
  })
})
