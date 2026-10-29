import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ReverseAccountFields, { type ReverseFormModel } from '../ReverseAccountFields.vue'

const mocks = vi.hoisted(() => ({ start: vi.fn(), poll: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ startReverseLogin: mocks.start, pollReverseLogin: mocks.poll }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function form(): ReverseFormModel {
  return {
    access_token: '', refresh_token: '', uid: '', machine_id: '', machine_token: '', machine_type: '', machine_code: '', user_type: '', device_token: '', session_id: '', enterprise_id: '', nickname: '', domain: '', desktop_version: '', machine_salt: '', clear_machine_identity: false, user_agent: '', client_version: '', cli_version: '', client_name: '', auto_checkin: false, auto_tasks: false, auto_refresh: false, sanitize_fingerprints: true, passthrough_ip: false, prompt_mode: 'passthrough', prompt_text: '', task_kinds: ['growth'], task_hours: {}
  }
}
function button(wrapper: ReturnType<typeof mount>, key: string) {
  const found = wrapper.findAll('button').find(item => item.text() === `admin.accounts.reverse.${key}`)
  expect(found).toBeDefined()
  return found!
}

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.clearAllMocks() })

describe('Reverse account configuration', () => {
  it('keeps sanitization enabled and automation disabled by default', async () => {
    const wrapper = mount(ReverseAccountFields, { props: { platform: 'workbuddy_cn', modelValue: form() } })
    expect(wrapper.get('[aria-label="admin.accounts.reverse.sanitizeFingerprints"]').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('[aria-label="admin.accounts.reverse.autoTasks"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[aria-label="admin.accounts.reverse.autoRefresh"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false)
    await wrapper.get('[aria-label="admin.accounts.reverse.sanitizeFingerprints"]').trigger('click')
    expect(wrapper.props('modelValue').sanitize_fingerprints).toBe(false)
    wrapper.unmount()
  })

  it('imports the Qoder machine pair as account fields and drops a stale login ticket', async () => {
    const wrapper = mount(ReverseAccountFields, { props: { platform: 'qoder_global', modelValue: form() } })
    await button(wrapper, 'manualTab').trigger('click')
    await wrapper.get('details textarea').setValue(JSON.stringify({ vendor: 'qoderglobal', secret: JSON.stringify({ device_token: 'dt-account', refresh_token: 'drt-account' }), qoder_machine_token: 'machine-a', qoder_machine_type: 'type-a', qoder_desktop_version: '0.5.0' }))
    await button(wrapper, 'applyCredentials').trigger('click')
    expect(wrapper.props('modelValue')).toMatchObject({ access_token: 'dt-account', refresh_token: 'drt-account', machine_token: 'machine-a', machine_type: 'type-a', desktop_version: '0.5.0' })
    expect(wrapper.emitted('login-reset')).toHaveLength(2)
    expect(wrapper.get('details textarea').element.value).toBe('')
    expect(wrapper.find('[aria-label="admin.accounts.reverse.sanitizeFingerprints"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('rejects imports from another region without modifying the account', async () => {
    const original = form()
    const wrapper = mount(ReverseAccountFields, { props: { platform: 'workbuddy_cn', modelValue: original } })
    await button(wrapper, 'manualTab').trigger('click')
    await wrapper.get('details textarea').setValue('{"vendor":"workbuddyglobal","access_token":"secret"}')
    await button(wrapper, 'applyCredentials').trigger('click')
    expect(wrapper.get('[role="alert"]').text()).toContain('credentialPlatformMismatch')
    expect(original.access_token).toBe('')
    wrapper.unmount()
  })

  it('does not resume login polling after unmount while a request is pending', async () => {
    vi.useFakeTimers()
    vi.spyOn(window, 'open').mockReturnValue(null)
    mocks.start.mockResolvedValue({ session_id: 's1', login_url: 'https://example.com/login' })
    let resolve!: (value: { pending: boolean }) => void
    mocks.poll.mockReturnValue(new Promise(done => { resolve = done }))
    const wrapper = mount(ReverseAccountFields, { props: { platform: 'qoder_cn', modelValue: form(), proxyId: 7 } })
    await button(wrapper, 'startLogin').trigger('click')
    await flushPromises()
    expect(mocks.start).toHaveBeenCalledWith({ platform: 'qoder_cn', account_id: undefined, proxy_id: 7 })
    await vi.advanceTimersByTimeAsync(2000)
    expect(mocks.poll).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    resolve({ pending: true })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(10000)
    expect(mocks.poll).toHaveBeenCalledTimes(1)
  })

  it('does not open a login page or start polling after the login start request was abandoned', async () => {
    vi.useFakeTimers()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    let resolve!: (value: { session_id: string; login_url: string }) => void
    mocks.start.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const wrapper = mount(ReverseAccountFields, { props: { platform: 'qoder_cn', modelValue: form() } })
    await button(wrapper, 'startLogin').trigger('click')
    await button(wrapper, 'manualTab').trigger('click')
    resolve({ session_id: 'abandoned', login_url: 'https://example.com/login' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    expect(open).not.toHaveBeenCalled()
    expect(mocks.poll).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
