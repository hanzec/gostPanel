import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElMessage, ElMessageBox } from 'element-plus'
import { defineComponent, h, inject, provide, watch } from 'vue'
import { vi } from 'vitest'

const Dialog = defineComponent({
  props: { modelValue: Boolean, title: String },
  emits: ['close', 'closed', 'update:modelValue'],
  setup(props, { emit, slots }) {
    watch(() => props.modelValue, (open, wasOpen) => {
      if (wasOpen && !open) {
        emit('close')
        emit('closed')
      }
    })
    return () => props.modelValue && h('section', { role: 'dialog', 'aria-label': props.title }, [
      h('h2', props.title),
      h('button', { type: 'button', 'aria-label': '关闭弹窗', onClick: () => emit('update:modelValue', false) }, '×'),
      slots.default?.(), slots.footer?.()
    ])
  }
})

const dropdownCommand = Symbol('dropdown command')
const Dropdown = defineComponent({
  emits: ['command'],
  setup(_, { emit, slots }) {
    provide(dropdownCommand, command => emit('command', command))
    return () => h('div', [slots.default?.(), slots.dropdown?.()])
  }
})
const DropdownItem = defineComponent({
  props: { command: String },
  setup(props, { slots }) {
    const command = inject(dropdownCommand)
    return () => h('button', { type: 'button', onClick: () => command(props.command) }, slots.default?.())
  }
})

// Keep Element Plus validation and inputs in the DOM. Replace the portal and
// popper lifecycles so tests can interact with dialogs and menu commands directly.
export function mountNodeComponent(component, props = {}) {
  return mount(component, {
    props,
    attachTo: document.body,
    global: {
      plugins: [ElementPlus],
      stubs: {
        ElDialog: Dialog,
        ElDropdown: Dropdown,
        ElDropdownItem: DropdownItem,
        ElDropdownMenu: { render() { return h('div', this.$slots.default?.()) } }
      }
    }
  })
}

export function mockNodeMessages() {
  return {
    confirm: vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm'),
    success: vi.spyOn(ElMessage, 'success').mockImplementation(() => {}),
    error: vi.spyOn(ElMessage, 'error').mockImplementation(() => {})
  }
}

export function button(wrapper, label) {
  const control = wrapper.findAll('button').find(item => item.text() === label)
  if (!control) throw new Error(`Button not found: ${label}`)
  return control
}

export async function click(wrapper, label) {
  await button(wrapper, label).trigger('click')
  await flushPromises()
}

export function deferred() {
  let resolve
  let reject
  const promise = new Promise((complete, fail) => {
    resolve = complete
    reject = fail
  })
  return { promise, resolve, reject }
}

export function dialog(wrapper, title) {
  return wrapper.get(`[role="dialog"][aria-label="${title}"]`)
}

export const response = data => ({ data })
export const apiError = message => ({ response: { data: { message } } })
