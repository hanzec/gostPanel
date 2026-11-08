import { enableAutoUnmount, flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import ElementPlus, { ElDropdown, ElFormItem, ElMessage, ElMessageBox, ElPagination, ElSelect } from 'element-plus'
import { defineComponent, h, nextTick } from 'vue'
import { afterEach, beforeEach, expect, vi } from 'vitest'

// Dialog visibility and its slots are all the inventory pages require from the popup.
// The form, validation rules and inputs inside it remain real Element Plus components.
const Dialog = defineComponent({
  props: ['modelValue', 'title'],
  setup(props, { slots }) {
    return () => props.modelValue
      ? h('section', { role: 'dialog', 'aria-label': props.title }, [slots.default?.(), slots.footer?.()])
      : null
  }
})

const Pagination = defineComponent({
  props: ['currentPage', 'pageSize', 'total'],
  render: () => h('nav')
})

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.resetAllMocks()
  vi.spyOn(ElMessage, 'success').mockImplementation(() => {})
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => {})
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm')
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

export const result = (list = [], total = list.length) => ({ data: { list, total } })

export function mountInventory(view) {
  return mount(view, {
    attachTo: document.body,
    global: { plugins: [ElementPlus], stubs: { ElDialog: Dialog, ElPagination: Pagination, RouterLink: RouterLinkStub } }
  })
}

export function button(wrapper, label) {
  const match = wrapper.findAll('button').find(item => item.text().trim() === label)
  expect(match, `Expected button "${label}"`).toBeDefined()
  return match
}

export function row(wrapper, name) {
  const match = wrapper.findAll('tbody tr').find(item => item.get('strong').text() === name)
  expect(match, `Expected row "${name}"`).toBeDefined()
  return match
}

export function select(wrapper, id) {
  const match = wrapper.findAllComponents(ElSelect).find(item => item.props('id') === id)
  expect(match, `Expected select "${id}"`).toBeDefined()
  return match
}

export function field(wrapper, prop) {
  const match = wrapper.findAllComponents(ElFormItem).find(item => item.props('prop') === prop)
  expect(match, `Expected form field "${prop}"`).toBeDefined()
  return match
}

export async function choose(wrapper, value) {
  wrapper.vm.$emit('update:modelValue', value)
  wrapper.vm.$emit('change', value)
  await flushPromises()
}

export async function changePage(wrapper, value) {
  const pagination = wrapper.getComponent(ElPagination)
  pagination.vm.$emit('update:current-page', value)
  await nextTick()
  pagination.vm.$emit('current-change', value)
  await flushPromises()
}

export async function command(wrapper, value) {
  wrapper.getComponent(ElDropdown).vm.$emit('command', value)
  await flushPromises()
}
