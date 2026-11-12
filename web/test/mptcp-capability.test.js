import { flushPromises } from '@vue/test-utils'
import { ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Rules from '@/views/Rules.vue'
import Tunnels from '@/views/Tunnels.vue'
import { getRoutingNodes } from '@/api/node'
import { getGostRulesets } from '@/api/system'
import { checkRuleListener, createRule, getRuleList, updateRule } from '@/api/rule'
import { createTunnel, getTunnelList, updateTunnel } from '@/api/tunnel'
import { button, choose, command, field, mountInventory, result, row } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getRoutingNodes: vi.fn() }))
vi.mock('@/api/system', () => ({ getGostRulesets: vi.fn() }))
vi.mock('@/api/rule', () => ({
  checkRuleListener: vi.fn(), createRule: vi.fn(), deleteRule: vi.fn(), getRuleList: vi.fn(),
  startRule: vi.fn(), stopRule: vi.fn(), updateRule: vi.fn()
}))
vi.mock('@/api/tunnel', () => ({
  createTunnel: vi.fn(), deleteTunnel: vi.fn(), getTunnelList: vi.fn(),
  startTunnel: vi.fn(), stopTunnel: vi.fn(), updateTunnel: vi.fn(), updateTunnelAllowlist: vi.fn()
}))

const ingress = { id: 1, name: '入口', mptcp_supported: false }
const supported = { id: 2, name: '支持的出口', mptcp_supported: true }
const unknown = { id: 3, name: '尚未上报的出口' }
const unsupported = { id: 4, name: '不支持的出口', mptcp_supported: false }
const source = { id: 11, name: '源端点', node_id: ingress.id, node: ingress, role: 'entry', protocol: 'tcp', listen_ip: '', listen_port: 8080, status: 'stopped' }
const destination = { id: 12, name: '旧目的端点', node_id: supported.id, node: supported, protocol: 'tcp', listen_ip: '', listen_port: 9443, status: 'stopped' }
const otherDestination = { ...destination, id: 13, name: '另一目的端点', node_id: unknown.id, node: unknown }
const exitRule = { ...destination, name: '出口端点', role: 'exit', targets: ['app.example:443'], protocol_options: { mptcp: 'true' } }
const selectField = (wrapper, prop) => field(wrapper, prop).getComponent(ElSelect)
const dialog = wrapper => wrapper.get('[role="dialog"]')
const mptcpField = wrapper => wrapper.find('[data-protocol-option="mptcp"]')

beforeEach(() => {
  getRoutingNodes.mockResolvedValue([ingress, supported, unknown, unsupported])
  getGostRulesets.mockResolvedValue({ data: [] })
  getRuleList.mockResolvedValue(result([source, destination, otherDestination]))
  getTunnelList.mockResolvedValue(result())
  checkRuleListener.mockResolvedValue({ data: { available: true } })
  for (const action of [createRule, updateRule, createTunnel, updateTunnel]) action.mockResolvedValue({})
})

describe('Multipath TCP node capability', () => {
  it('only offers confirmed support and drops hidden options when creating on another node', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const editor = dialog(wrapper)
    await choose(selectField(editor, 'role'), 'exit')
    await field(editor, 'name').get('input').setValue('新出口')
    await field(editor, 'listen_port').get('input').setValue('9443')
    await editor.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    expect(mptcpField(editor).exists()).toBe(false)
    await choose(selectField(editor, 'node_id'), unsupported.id)
    expect(mptcpField(editor).exists()).toBe(false)
    await choose(selectField(editor, 'node_id'), supported.id)
    expect(mptcpField(editor).exists()).toBe(true)
    await editor.get('.gost-transport-options [role="button"]').trigger('click')
    await choose(mptcpField(editor).getComponent(ElSelect), 'true')
    await choose(selectField(editor, 'node_id'), unknown.id)
    expect(mptcpField(editor).exists()).toBe(false)
    await button(editor, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith(expect.objectContaining({ node_id: unknown.id, protocol_options: {} }))
  })

  it('retains a saved value when editing the same exit before its daemon reports capabilities', async () => {
    const saved = { ...exitRule, node: unknown, node_id: unknown.id }
    getRuleList.mockResolvedValue(result([saved]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 出口端点"]').trigger('click')
    const editor = dialog(wrapper)
    expect(mptcpField(editor).exists()).toBe(false)
    await field(editor, 'remark').get('textarea').setValue('更新备注')
    await button(editor, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledWith(saved.id, expect.objectContaining({ remark: '更新备注', protocol_options: { mptcp: 'true' } }))
  })

  it('drops unconfirmed saved Multipath TCP when copying an exit', async () => {
    getRuleList.mockResolvedValue(result([{ ...exitRule, node: unknown, node_id: unknown.id }]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await command(row(wrapper, exitRule.name), 'copy')
    const editor = dialog(wrapper)
    expect(mptcpField(editor).exists()).toBe(false)
    await field(editor, 'listen_port').get('input').setValue('9444')
    await button(editor, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith(expect.objectContaining({ node_id: unknown.id, protocol_options: {} }))
  })

  it('keeps confirmed capabilities from an endpoint when the node list is unavailable', async () => {
    getRuleList.mockResolvedValue(result([exitRule]))
    getRoutingNodes.mockRejectedValue(new Error('node list unavailable'))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 出口端点"]').trigger('click')
    expect(mptcpField(dialog(wrapper)).exists()).toBe(true)
  })

  it('prefers the current node capability over an older endpoint snapshot', async () => {
    getRuleList.mockResolvedValue(result([exitRule]))
    getRoutingNodes.mockResolvedValue([{ ...supported, mptcp_supported: false }])
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 出口端点"]').trigger('click')
    expect(mptcpField(dialog(wrapper)).exists()).toBe(false)
  })

  it('uses only the exit capability for a linked tunnel summary', async () => {
    getRuleList.mockResolvedValue(result([source, exitRule, { ...otherDestination, role: 'exit', targets: ['app.example:443'], protocol_options: { mptcp: 'true' } }]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await choose(selectField(editor, 'entry_rule_id'), source.id)
    await choose(selectField(editor, 'exit_rule_id'), exitRule.id)
    expect(editor.get('.gost-options-summary').text()).toContain('Multipath TCP')
    await choose(selectField(editor, 'exit_rule_id'), otherDestination.id)
    expect(editor.text()).not.toContain('Multipath TCP')
  })

  it('drops hidden Multipath TCP when creating a tunnel after switching its exit', async () => {
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await field(editor, 'name').get('input').setValue('新隧道')
    await choose(selectField(editor, 'entry_rule_id'), source.id)
    await choose(selectField(editor, 'exit_rule_id'), destination.id)
    await editor.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await editor.get('.gost-transport-options [role="button"]').trigger('click')
    await choose(mptcpField(editor).getComponent(ElSelect), 'true')
    await choose(selectField(editor, 'exit_rule_id'), otherDestination.id)
    expect(mptcpField(editor).exists()).toBe(false)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledWith(expect.objectContaining({ exit_rule_id: otherDestination.id, protocol_options: {} }))
  })

  it.each([false, true])('preserves saved tunnel options only for the same exit: changed=%s', async changed => {
    const saved = {
      id: 21, name: '已存隧道', status: 'stopped', protocol: 'tcp', relay_port: 9443,
      entry_rule_id: source.id, exit_rule_id: otherDestination.id, entry_node_id: ingress.id, exit_node_id: unknown.id,
      protocol_options: { mptcp: 'true' }, targets: ['app.example:443'], strategy: 'round'
    }
    getRuleList.mockResolvedValue(result([source, otherDestination, { ...destination, node: unsupported, node_id: unsupported.id }]))
    getTunnelList.mockResolvedValue(result([saved]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, saved.name), 'edit')
    const editor = dialog(wrapper)
    expect(mptcpField(editor).exists()).toBe(false)
    if (changed) await choose(selectField(editor, 'exit_rule_id'), destination.id)
    await field(editor, 'remark').get('textarea').setValue('更新备注')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledWith(saved.id, expect.objectContaining({ protocol_options: changed ? {} : { mptcp: 'true' } }))
  })

  it('uses the reported exit node for legacy tunnels before the node list arrives', async () => {
    const saved = {
      id: 21, name: '旧隧道', status: 'stopped', protocol: 'tcp', relay_port: 9443,
      entry_node_id: ingress.id, exit_node_id: supported.id, exit_node: supported,
      protocol_options: { mptcp: 'true' }
    }
    getTunnelList.mockResolvedValue(result([saved]))
    getRoutingNodes.mockReturnValue(new Promise(() => {}))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, saved.name), 'edit')
    const editor = dialog(wrapper)
    expect(mptcpField(editor).exists()).toBe(true)
    await field(editor, 'remark').get('textarea').setValue('更新备注')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledWith(saved.id, expect.objectContaining({ protocol_options: { mptcp: 'true' } }))
  })
})
