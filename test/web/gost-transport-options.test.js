import { flushPromises } from '@vue/test-utils'
import { ElForm, ElSelect } from 'element-plus'
import { describe, expect, it } from 'vitest'
import { defineComponent, h, reactive } from 'vue'
import GostTransportOptions from '@/components/GostTransportOptions.vue'
import { choose, mountInventory } from './inventory-fixtures'

const option = (wrapper, id) => wrapper.get(`[data-protocol-option="${id}"]`)
const toggle = wrapper => wrapper.get('.gost-transport-options [role="button"]')

const mountOptions = (protocol, options = {}, readonly = false, mptcpSupported = false) => mountInventory(defineComponent({
  setup() {
    const form = reactive({ protocol_options: { ...options } })
    return () => h(ElForm, { model: form }, {
      default: () => h(GostTransportOptions, {
        protocol, readonly, mptcpSupported, modelValue: form.protocol_options,
        'onUpdate:modelValue': value => { form.protocol_options = value }
      })
    })
  }
}))

describe('Gost transport options', () => {
  it('hides Multipath TCP and empty option groups until the exit reports support', () => {
    const wrapper = mountOptions('ohttp', { mptcp: 'true' })
    expect(wrapper.find('[data-protocol-option="mptcp"]').exists()).toBe(false)
    expect(wrapper.find('section[aria-label="连接与监听"]').exists()).toBe(false)
    expect(toggle(wrapper).text()).not.toContain('项已设置')
    expect(wrapper.text()).not.toContain('Multipath TCP')
  })

  it('allows Multipath TCP after the exit reports support', async () => {
    const wrapper = mountOptions('tcp', {}, false, true)
    await toggle(wrapper).trigger('click')
    await choose(option(wrapper, 'mptcp').getComponent(ElSelect), 'true')
    expect(wrapper.getComponent(GostTransportOptions).props('modelValue')).toEqual({ mptcp: 'true' })
  })

  it.each([false, true])('uses exit capability for read-only Multipath TCP values: %s', mptcpSupported => {
    const wrapper = mountOptions('tcp', { mptcp: 'true', keepalive: 'false' }, true, mptcpSupported)
    expect(wrapper.text().includes('Multipath TCP')).toBe(mptcpSupported)
  })

  it('keeps explicit false and zero values distinct from Gost defaults', async () => {
    const wrapper = mountOptions('kcp')
    expect(toggle(wrapper).attributes('aria-expanded')).toBe('false')
    await toggle(wrapper).trigger('click')
    await choose(option(wrapper, 'kcp_no_compression').getComponent(ElSelect), 'false')
    await option(wrapper, 'kcp_data_shards').get('input').setValue('0')
    await expect(wrapper.getComponent(ElForm).vm.validate()).resolves.toBe(true)
    expect(wrapper.getComponent(GostTransportOptions).props('modelValue')).toEqual({ kcp_no_compression: 'false', kcp_data_shards: '0' })
    expect(option(wrapper, 'kcp_data_shards').get('input').element.value).toBe('0')
    await choose(option(wrapper, 'kcp_no_compression').getComponent(ElSelect), '')
    await option(wrapper, 'kcp_data_shards').get('input').setValue('')
    expect(wrapper.getComponent(GostTransportOptions).props('modelValue')).toEqual({})
  })

  it('validates paths, headers and integer bounds and expands to reveal invalid fields', async () => {
    const wrapper = mountOptions('wss')
    await toggle(wrapper).trigger('click')
    await option(wrapper, 'path').get('input').setValue('missing-slash')
    await option(wrapper, 'request_headers').get('textarea').setValue('["invalid"]')
    await option(wrapper, 'backlog').get('input').setValue('0')
    await toggle(wrapper).trigger('click')
    await expect(wrapper.getComponent(ElForm).vm.validate()).rejects.toMatchObject({
      'protocol_options.path': expect.any(Array),
      'protocol_options.request_headers': expect.any(Array),
      'protocol_options.backlog': expect.any(Array)
    })
    await flushPromises()
    expect(toggle(wrapper).attributes('aria-expanded')).toBe('true')
    await option(wrapper, 'path').get('input').setValue('/relay')
    await option(wrapper, 'request_headers').get('textarea').setValue('{"X-Example":"value"}')
    await option(wrapper, 'backlog').get('input').setValue('16')
    await expect(wrapper.getComponent(ElForm).vm.validate()).resolves.toBe(true)
    expect(wrapper.getComponent(GostTransportOptions).props('modelValue')).toEqual({
      path: '/relay', request_headers: '{"X-Example":"value"}', backlog: '16'
    })
  })

  it('shows configured exit options as read-only and masks secret values', () => {
    const wrapper = mountOptions('quic', { quic_cipher_key: 'private-key', quic_keepalive: 'false', max_idle_timeout: '45s' }, true)
    expect(toggle(wrapper).attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('.gost-options-summary').text()).toContain('已设置')
    expect(wrapper.get('.gost-options-summary').text()).toContain('禁用')
    expect(wrapper.get('.gost-options-summary').text()).toContain('45s')
    expect(wrapper.text()).not.toContain('private-key')
    expect(wrapper.find('input').exists()).toBe(false)
  })
})
