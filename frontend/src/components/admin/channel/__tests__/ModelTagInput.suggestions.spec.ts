import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ModelTagInput from '../ModelTagInput.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

function options(): HTMLElement[] {
  return Array.from(document.body.querySelectorAll<HTMLElement>('[role="option"]'))
}

function optionLabels(): string[] {
  return options().map(el => el.textContent?.trim() ?? '')
}

function listbox(): HTMLElement | null {
  return document.body.querySelector<HTMLElement>('[role="listbox"]')
}

describe('model tag suggestions', () => {
  it('opens the candidate list on focus and hides already selected models', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: ['gpt-4o'], suggestions: ['gpt-4o', 'gpt-4.1', 'o3'] },
    })

    await wrapper.get('input').trigger('focus')
    await nextTick()

    expect(optionLabels()).toEqual(['gpt-4.1', 'o3'])
  })

  it('filters candidates by the typed text', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['claude-opus-4', 'claude-sonnet-4', 'gpt-4o'] },
    })

    const input = wrapper.get('input')
    await input.trigger('focus')
    await input.setValue('sonnet')
    await nextTick()

    expect(optionLabels()).toEqual(['claude-sonnet-4'])
  })

  it('adds a candidate when clicked', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o', 'o3'] },
    })

    await wrapper.get('input').trigger('focus')
    await nextTick()
    options()[1].click()
    await nextTick()

    expect(wrapper.emitted('update:models')).toEqual([[['o3']]])
  })

  it('adds the highlighted candidate on Enter', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o', 'o3'] },
    })

    const input = wrapper.get('input')
    await input.trigger('focus')
    await nextTick()
    await input.trigger('keydown', { key: 'ArrowDown' })
    await input.trigger('keydown', { key: 'ArrowDown' })
    const event = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.element.dispatchEvent(event)
    await nextTick()

    expect(event.defaultPrevented).toBe(true)
    expect(wrapper.emitted('update:models')).toEqual([[['o3']]])
  })

  it('does not open a list when no suggestions are provided', async () => {
    const wrapper = mount(ModelTagInput, { props: { models: [] } })

    await wrapper.get('input').trigger('focus')
    await nextTick()

    expect(options()).toHaveLength(0)
  })

  it('opens the list when suggestions arrive after focus', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [] },
      attachTo: document.body,
    })
    const input = wrapper.get('input').element as HTMLInputElement
    input.focus()
    await nextTick()
    expect(options()).toHaveLength(0)

    await wrapper.setProps({ suggestions: ['gpt-4o'] })
    await nextTick()

    expect(optionLabels()).toEqual(['gpt-4o'])
  })

  it('stops Escape from reaching dialog-level handlers while the list is open', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o'] },
      attachTo: document.body,
    })
    const input = wrapper.get('input')
    await input.trigger('focus')
    await nextTick()

    const onDocumentKeydown = vi.fn()
    document.addEventListener('keydown', onDocumentKeydown)
    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    input.element.dispatchEvent(event)
    document.removeEventListener('keydown', onDocumentKeydown)
    await nextTick()

    expect(event.defaultPrevented).toBe(true)
    expect(onDocumentKeydown).not.toHaveBeenCalled()
    expect(options()).toHaveLength(0)
  })

  it('leaves Escape to dialogs when the list is closed', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o'] },
      attachTo: document.body,
    })
    const input = wrapper.get('input')

    const onDocumentKeydown = vi.fn()
    document.addEventListener('keydown', onDocumentKeydown)
    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    input.element.dispatchEvent(event)
    document.removeEventListener('keydown', onDocumentKeydown)

    expect(event.defaultPrevented).toBe(false)
    expect(onDocumentKeydown).toHaveBeenCalledOnce()
  })

  it('does not intercept arrow keys during IME composition', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o'] },
    })
    const input = wrapper.get('input')
    await input.trigger('focus')
    await nextTick()

    const event = new KeyboardEvent('keydown', {
      key: 'ArrowDown',
      isComposing: true,
      bubbles: true,
      cancelable: true,
    })
    input.element.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(false)
  })

  it('does not intercept arrow keys when no suggestions exist', async () => {
    const wrapper = mount(ModelTagInput, { props: { models: [] } })

    const event = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true })
    wrapper.get('input').element.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(false)
  })

  it('prevents blur when the list container is pressed', async () => {
    const wrapper = mount(ModelTagInput, {
      props: { models: [], suggestions: ['gpt-4o'] },
    })
    await wrapper.get('input').trigger('focus')
    await nextTick()

    const event = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    listbox()!.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
  })
})
