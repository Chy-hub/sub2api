import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import BulkAssignSubscriptionModal from '../BulkAssignSubscriptionModal.vue'

const { bulkAssign, showSuccess, showError } = vi.hoisted(() => ({
  bulkAssign: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: {
      bulkAssign
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showError
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${JSON.stringify(params)}` : key
  })
}))

const groups = [
  {
    id: 7,
    name: 'Pro',
    description: 'Pro plan',
    platform: 'anthropic',
    status: 'active',
    subscription_type: 'subscription',
    rate_multiplier: 1
  },
  {
    id: 8,
    name: 'Standard',
    description: '',
    platform: 'anthropic',
    status: 'active',
    subscription_type: 'standard',
    rate_multiplier: 1
  },
  {
    id: 9,
    name: 'Disabled',
    description: '',
    platform: 'anthropic',
    status: 'disabled',
    subscription_type: 'subscription',
    rate_multiplier: 1
  }
] as any[]

const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: `
    <select
      data-test="group-select"
      :value="modelValue ?? ''"
      @change="$emit('update:modelValue', $event.target.value === '' ? null : Number($event.target.value))"
    >
      <option value="">select</option>
      <option v-for="option in options" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
}

const mountModal = (selectedIds: number[] = [4, 7]) =>
  mount(BulkAssignSubscriptionModal, {
    props: {
      show: true,
      selectedIds,
      groups
    },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          emits: ['close'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        Select: SelectStub,
        GroupBadge: true,
        GroupOptionItem: true
      }
    }
  })

const successResult = (ids: number[]) => ({
  success_count: ids.length,
  created_count: ids.length,
  reused_count: 0,
  failed_count: 0,
  subscriptions: [],
  errors: [],
  statuses: Object.fromEntries(ids.map((id) => [String(id), 'created']))
})

describe('BulkAssignSubscriptionModal', () => {
  beforeEach(() => {
    bulkAssign.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('only offers active subscription groups', () => {
    const wrapper = mountModal()

    const options = wrapper.findAll('[data-test="group-select"] option').map((option) => option.text())
    expect(options).toContain('Pro')
    expect(options).not.toContain('Standard')
    expect(options).not.toContain('Disabled')
  })

  it('keeps submission disabled until a group and a valid validity are provided', async () => {
    const wrapper = mountModal()
    const submit = wrapper.get('[data-test="submit"]')

    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="group-select"]').setValue('7')
    expect(submit.attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-test="validity-days"]').setValue('0')
    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="validity-days"]').setValue('30')
    expect(submit.attributes('disabled')).toBeUndefined()
  })

  it('submits selected users and emits success', async () => {
    bulkAssign.mockResolvedValueOnce(successResult([4, 7]))
    const wrapper = mountModal()

    await wrapper.get('[data-test="group-select"]').setValue('7')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(window.confirm).toHaveBeenCalledTimes(1)
    expect(bulkAssign).toHaveBeenCalledWith({
      user_ids: [4, 7],
      group_id: 7,
      validity_days: 30
    })
    expect(wrapper.emitted('success')).toHaveLength(1)
    expect(wrapper.text()).toContain('admin.users.bulkAssign.resultSuccess')
  })

  it('splits more than 100 users into multiple requests', async () => {
    const selectedIds = Array.from({ length: 101 }, (_, index) => index + 1)
    bulkAssign
      .mockResolvedValueOnce(successResult(selectedIds.slice(0, 100)))
      .mockResolvedValueOnce(successResult(selectedIds.slice(100)))
    const wrapper = mountModal(selectedIds)

    expect(wrapper.get('[data-test="batch-hint"]').text()).toContain('admin.users.bulkAssign.batchHint')

    await wrapper.get('[data-test="group-select"]').setValue('7')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(bulkAssign).toHaveBeenCalledTimes(2)
    expect(bulkAssign.mock.calls[0][0].user_ids).toHaveLength(100)
    expect(bulkAssign.mock.calls[1][0].user_ids).toEqual([101])
  })

  it('shows failed users and retries only those users', async () => {
    bulkAssign
      .mockResolvedValueOnce({
        success_count: 1,
        created_count: 1,
        reused_count: 0,
        failed_count: 1,
        subscriptions: [],
        errors: ['user 7: boom'],
        statuses: { '4': 'created', '7': 'failed' }
      })
      .mockResolvedValueOnce(successResult([7]))
    const wrapper = mountModal()

    await wrapper.get('[data-test="group-select"]').setValue('7')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('user 7: boom')
    const retry = wrapper.get('[data-test="retry-failed"]')
    expect(retry.text()).toContain('admin.users.bulkAssign.retry')

    await retry.trigger('click')
    await flushPromises()

    expect(bulkAssign).toHaveBeenCalledTimes(2)
    expect(bulkAssign.mock.calls[1][0].user_ids).toEqual([7])
    expect(wrapper.emitted('success')).toHaveLength(2)
  })

  it('marks the current batch and later batches failed when a request throws', async () => {
    bulkAssign.mockRejectedValueOnce({})
    const wrapper = mountModal([1, 2])

    await wrapper.get('[data-test="group-select"]').setValue('7')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.users.bulkAssign.failed')
    expect(wrapper.get('[data-test="bulk-assign-result"]').text()).toContain(
      'admin.users.bulkAssign.resultSuccess'
    )
    expect(wrapper.get('[data-test="retry-failed"]').exists()).toBe(true)
    expect(wrapper.emitted('success')).toBeUndefined()
  })
})
