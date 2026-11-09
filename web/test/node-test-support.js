import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElMessage, ElMessageBox } from 'element-plus'
import { defineComponent, h, watch } from 'vue'
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

// Keep Element Plus validation and inputs in the DOM while replacing dialog transitions and portals.
export function mountNodeComponent(component, props = {}, plugins = []) {
  return mount(component, {
    props,
    attachTo: document.body,
    global: {
      plugins: [ElementPlus, ...plugins],
      stubs: { ElDialog: Dialog }
    }
  })
}

export function mockNodeMessages() {
  return {
    confirm: vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm'),
    success: vi.spyOn(ElMessage, 'success').mockImplementation(() => {}),
    warning: vi.spyOn(ElMessage, 'warning').mockImplementation(() => {}),
    error: vi.spyOn(ElMessage, 'error').mockImplementation(() => {})
  }
}

export function button(wrapper, label) {
  const control = wrapper.findAll('button').find(item => item.text() === label || item.attributes('aria-label') === label)
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
