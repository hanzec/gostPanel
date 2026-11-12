import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import InventoryToolbar from '@/components/InventoryToolbar.vue'

it('keeps actions in one header and opens the filter form in a dialog when space is limited', async () => {
  let resize
  const disconnect = vi.fn()
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { resize = width => callback([{ contentRect: { width } }]) }
    observe() {}
    disconnect() { disconnect() }
  })
  const toolbar = mount(InventoryToolbar, {
    props: { filterLabel: '筛选节点' },
    slots: {
      title: '<h2>节点清单</h2>',
      filters: '<label for="keyword">节点名称</label><input id="keyword"><button type="submit">应用</button>',
      actions: '<button type="button">添加节点</button>'
    },
    global: {
      plugins: [ElementPlus],
      stubs: {
        PanelDialog: {
          props: ['modelValue', 'title'],
          template: '<section v-if="modelValue" role="dialog" :aria-label="title"><slot /></section>'
        }
      }
    }
  })

  try {
    expect(toolbar.get('header form').attributes('aria-label')).toBe('筛选节点')
    await toolbar.get('header form').trigger('submit')
    expect(toolbar.emitted('submit')).toHaveLength(1)

    resize(240)
    await nextTick()
    expect(toolbar.find('header form').exists()).toBe(false)
    expect(toolbar.get('header').text()).toContain('添加节点')
    const trigger = toolbar.get('header button[aria-label="筛选节点"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    const dialog = toolbar.get('[role="dialog"]')
    expect(dialog.attributes('aria-label')).toBe('筛选节点')
    expect(dialog.get('label').attributes('for')).toBe('keyword')
    expect(toolbar.findAll('#keyword')).toHaveLength(1)
    await dialog.get('form').trigger('submit')
    expect(toolbar.emitted('submit')).toHaveLength(2)
    expect(trigger.attributes('aria-expanded')).toBe('false')

    await trigger.trigger('click')
    resize(720)
    await nextTick()
    expect(toolbar.find('[role="dialog"]').exists()).toBe(false)
    expect(toolbar.get('header form').attributes('aria-label')).toBe('筛选节点')
  } finally {
    toolbar.unmount()
    vi.unstubAllGlobals()
  }
  expect(disconnect).toHaveBeenCalledOnce()
})
