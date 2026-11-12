import { flushPromises } from '@vue/test-utils'
import { ElForm, ElMessage, ElMessageBox, ElOption, ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Tunnels from '@/views/Tunnels.vue'
import { getRoutingNodes } from '@/api/node'
import { getGostRulesets } from '@/api/system'
import { getRuleList } from '@/api/rule'
import { createTunnel, deleteTunnel, getTunnelList, startTunnel, stopTunnel, updateTunnel, updateTunnelAllowlist } from '@/api/tunnel'
import { button, changePage, choose, command, field, mountInventory, result, row, select } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getRoutingNodes: vi.fn() }))
vi.mock('@/api/system', () => ({ getGostRulesets: vi.fn() }))
vi.mock('@/api/rule', () => ({ getRuleList: vi.fn() }))
vi.mock('@/api/tunnel', () => ({
  createTunnel: vi.fn(), deleteTunnel: vi.fn(), getTunnelList: vi.fn(),
  startTunnel: vi.fn(), stopTunnel: vi.fn(), updateTunnel: vi.fn(), updateTunnelAllowlist: vi.fn()
}))

const ingress = { id: 4, name: '入口 A', addresses: ['192.0.2.1', '192.0.2.2', '2001:db8::1'] }
const legacyNode = { id: 5, name: '旧版节点', addresses: [], ipv4: '198.51.100.3' }
const egress = { id: 9, name: '出口 B' }
const source = { id: 21, name: 'Web A', node: ingress, node_id: ingress.id, protocol: 'tcp', listen_ip: '127.0.0.1', listen_port: 443, status: 'stopped', use_clash_ruleset: true }
const destination = { id: 22, name: 'Relay B', node: egress, node_id: egress.id, protocol: 'udp', listen_ip: '', listen_port: 9553, status: 'stopped', targets: [] }
const relaySource = { ...source, id: 31, name: 'Office A', status: 'running', linked_tunnel_id: 12 }
const relayDestination = { ...destination, id: 32, name: 'Office B', protocol: 'tcp', listen_port: 65535, status: 'running', linked_tunnel_id: 12 }
const mediaSource = { ...source, id: 41, linked_tunnel_id: 13 }
const mediaDestination = { ...destination, id: 42, linked_tunnel_id: 13 }
const entrances = [source, destination, relaySource, relayDestination, mediaSource, mediaDestination,
  { ...destination, id: 23, node: ingress, node_id: ingress.id },
  { ...destination, id: 24, listen_ip: '127.0.0.2' },
  { ...source, id: 25, status: 'running' }, { ...source, id: 26, tunnel_id: 30 }]
const relay = {
  id: 12, name: 'Office relay', status: 'running', protocol: 'tcp', relay_port: 65535,
  entry_node: ingress, entry_node_id: ingress.id, exit_node: egress, exit_node_id: egress.id, remark: 'Private network',
  entry_rule_id: relaySource.id, entry_rule: relaySource, exit_rule_id: relayDestination.id, exit_rule: relayDestination,
  allowlist_node_ids: [ingress.id, 88]
}
const udpRelay = {
  id: 13, name: 'Media relay', status: 'stopped', protocol: 'udp', relay_port: 9553,
  entry_node_id: ingress.id, exit_node_id: egress.id, entry_rule_id: mediaSource.id, exit_rule_id: mediaDestination.id,
  entry_rule: mediaSource, exit_rule: mediaDestination,
  allowlist_node_ids: [egress.id]
}
const missingRelay = { id: 14, name: 'Unknown relay', entry_node_id: 31, exit_node_id: 32, relay_port: 8443 }
const tunnels = [relay, udpRelay, { ...missingRelay, id: 15, name: 'Broken relay', status: 'error' }, missingRelay]
const dialog = wrapper => wrapper.get('[role="dialog"]')
const selectField = (wrapper, prop) => field(wrapper, prop).getComponent(ElSelect)

beforeEach(() => {
  getGostRulesets.mockResolvedValue({ data: [{ id: 1, name: '旧 Clash 订阅' }, { id: 2, name: '工作域名' }] })
  getRoutingNodes.mockResolvedValue([ingress, egress, legacyNode])
  getRuleList.mockResolvedValue(result(entrances))
  getTunnelList.mockResolvedValue(result(tunnels, 32))
  for (const action of [createTunnel, updateTunnel, updateTunnelAllowlist, deleteTunnel, startTunnel, stopTunnel]) action.mockResolvedValue({})
})

describe('Relay tunnel inventory', () => {
  it('shows loading and offline states, retries both requests, and filters paginated results', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const list = Promise.withResolvers()
    getTunnelList.mockReturnValueOnce(list.promise)
    getRoutingNodes.mockRejectedValueOnce(new Error('nodes offline'))
    getRuleList.mockRejectedValueOnce(new Error('entrances offline'))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载隧道')
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['—', '—', '—', '—'])
    await vi.advanceTimersByTimeAsync(5000)
    expect(getTunnelList).toHaveBeenCalledTimes(1)
    list.reject(new Error('tunnels offline'))
    await flushPromises()
    expect(wrapper.text()).toContain('无法加载链路清单')
    expect(wrapper.text()).toContain('无法加载节点选项')
    await vi.advanceTimersByTimeAsync(5000)
    expect(getTunnelList).toHaveBeenCalledTimes(1)

    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    expect(dialog(wrapper).text()).toContain('无法加载端点选项')
    await button(dialog(wrapper), '重新加载端点').trigger('click')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 100 })
    expect(dialog(wrapper).text()).not.toContain('无法加载端点选项')
    await button(dialog(wrapper), '重新加载来源节点').trigger('click')
    await flushPromises()
    expect(getRoutingNodes).toHaveBeenCalledTimes(2)
    expect(dialog(wrapper).text()).not.toContain('无法加载来源节点选项')
    await button(dialog(wrapper), '取消').trigger('click')

    await button(wrapper, '重新加载').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['32', '1', '1', '1'])
    expect(row(wrapper, 'Office relay').text()).toContain('Private network')
    expect(row(wrapper, 'Office relay').text()).toContain('出口 B')
    expect(row(wrapper, 'Office relay').text()).toContain('Office A')
    expect(row(wrapper, 'Office relay').text()).toContain('Office B')
    expect(row(wrapper, 'Office relay').text()).toContain('TCP')
    expect(row(wrapper, 'Unknown relay').text()).toContain('节点 #31')
    expect(row(wrapper, 'Unknown relay').text()).toContain('未知')
    expect(row(wrapper, 'Unknown relay').text()).toContain('—')
    await vi.advanceTimersByTimeAsync(5000)
    expect(getTunnelList).toHaveBeenCalledTimes(3)

    await changePage(wrapper, 2)
    expect(getTunnelList).toHaveBeenLastCalledWith({ page: 2, pageSize: 10, node_id: '', status: '', keyword: '' })
    const pageSize = wrapper.findAllComponents(ElSelect).find(item => item.props('ariaLabel') === '每页隧道数')
    expect(pageSize).toBeDefined()
    await choose(pageSize, 20)
    expect(getTunnelList).toHaveBeenLastCalledWith({ page: 1, pageSize: 20, node_id: '', status: '', keyword: '' })
    await choose(select(wrapper, 'tunnel-node'), 4)
    await choose(select(wrapper, 'inventory-status'), 'running')
    await wrapper.get('#tunnel-keyword').setValue('  office  ')
    getTunnelList.mockResolvedValueOnce({ data: {} })
    await wrapper.get('form[aria-label="筛选隧道"]').trigger('submit')
    await flushPromises()
    expect(getTunnelList).toHaveBeenLastCalledWith({ page: 1, pageSize: 20, node_id: 4, status: 'running', keyword: '  office  ' })
    expect(wrapper.text()).toContain('没有匹配的隧道')
    getTunnelList.mockResolvedValueOnce(result())
    await button(wrapper, '清除筛选').trigger('click')
    await flushPromises()
    expect(getTunnelList).toHaveBeenLastCalledWith({ page: 1, pageSize: 20, node_id: '', status: '', keyword: '' })
    expect(wrapper.text()).toContain('尚未建立隧道')
    expect(wrapper.text()).toContain('先在端点管理创建入口与出口，再在这里连接两个端点')
  })

  it('adds from the inventory heading and links to entrance management when no endpoint pair is available', async () => {
    getRuleList.mockResolvedValueOnce({ data: {} })
    getTunnelList.mockResolvedValueOnce(result())
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    const heading = wrapper.get('section[aria-labelledby="tunnel-inventory-title"] > header')
    const actions = wrapper.findAll('button').map(item => item.text().trim())
    expect(actions.filter(label => label === '添加隧道')).toHaveLength(1)
    expect(actions).not.toContain('刷新')
    expect(heading.text()).not.toContain('RELAY')
    expect(heading.get('form[aria-label="筛选隧道"]').find('#tunnel-keyword').exists()).toBe(true)
    await button(heading, '添加隧道').trigger('click')
    await flushPromises()
    expect(dialog(wrapper).text()).toContain('源端或目的端缺少可用端点')
    await dialog(wrapper).get('.tunnel-link').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

describe('Relay tunnel configuration', () => {
  it('pairs transparent entries with original-target exits and rejects both mismatched combinations', async () => {
    const transparent = { ...source, role: 'entry', protocol: 'red', use_clash_ruleset: false }
    const tcp = { ...source, id: 71, role: 'entry', use_clash_ruleset: false }
    const original = { ...destination, role: 'exit', use_original_target: true, targets: [], ruleset_ids: [2], manual_domains: ['manual.example'] }
    const fixed = { ...destination, id: 72, role: 'exit', targets: ['fixed.example:443'], use_original_target: false }
    getRuleList.mockResolvedValue(result([transparent, tcp, original, fixed]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await field(editor, 'name').get('input').setValue('Transparent tunnel')
    await choose(selectField(editor, 'entry_rule_id'), tcp.id)
    await choose(selectField(editor, 'exit_rule_id'), original.id)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['exit_rule_id', false, '按原始目标需要选择“透明转发（HTTP/HTTPS）”入口'])
    await choose(selectField(editor, 'entry_rule_id'), transparent.id)
    await choose(selectField(editor, 'exit_rule_id'), fixed.id)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['exit_rule_id', false, '透明转发入口需要选择“按原始目标”的出口'])
    expect(createTunnel).not.toHaveBeenCalled()
    await choose(selectField(editor, 'exit_rule_id'), original.id)
    expect(editor.get('.tunnel-policy-summary').text()).toContain('按原始目标')
    expect(editor.get('.target-routing-summary').text()).toContain('匹配规则后走隧道')
    expect(editor.get('.target-routing-summary').text()).toContain('工作域名')
    expect(editor.get('.target-routing-summary').text()).toContain('manual.example')
    expect(editor.get('.target-routing-summary').findAll('table')).toHaveLength(1)
    expect(editor.get('.target-routing-summary').findAll('tr[data-rule-type]')).toHaveLength(2)
    expect(editor.get('.target-routing-summary').find('input').exists()).toBe(false)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledExactlyOnceWith({ name: 'Transparent tunnel', entry_rule_id: transparent.id, exit_rule_id: original.id, remark: '', allowlist_node_ids: [] })
  })

  it('preserves advanced options on a legacy tunnel and clears them when its transport changes', async () => {
    const legacy = { ...missingRelay, protocol: 'wss', protocol_options: { path: '/legacy', tls_server_name: 'relay.example' } }
    getTunnelList.mockResolvedValue(result([legacy]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, legacy.name), 'edit')
    let editor = dialog(wrapper)
    expect(editor.get('[data-protocol-option="path"] input').element.value).toBe('/legacy')
    expect(editor.get('[data-protocol-option="tls_server_name"] input').element.value).toBe('relay.example')
    await editor.get('[data-protocol-option="path"] input').setValue('/updated')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(legacy.id, {
      name: legacy.name, protocol: 'wss', relay_port: legacy.relay_port, remark: '',
      protocol_options: { path: '/updated', tls_server_name: 'relay.example' }
    })
    expect(legacy.protocol_options.path).toBe('/legacy')
    await command(row(wrapper, legacy.name), 'edit')
    editor = dialog(wrapper)
    await choose(selectField(editor, 'protocol'), 'quic')
    expect(editor.find('[data-protocol-option="path"]').exists()).toBe(false)
    expect(editor.get('[data-protocol-option="max_idle_timeout"] input').element.value).toBe('')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(legacy.id, {
      name: legacy.name, protocol: 'quic', relay_port: legacy.relay_port, remark: '', protocol_options: {}
    })
  })

  it('shows the current exit protocol even when the saved tunnel snapshot is older', async () => {
    getTunnelList.mockResolvedValue(result([
      { ...udpRelay, protocol: 'tls', exit_rule: { ...mediaDestination, role: 'exit', protocol: 'quic' } },
      { ...relay, status: 'stopped', protocol: 'tls', exit_rule: { ...relayDestination, protocol: 'quic' } }
    ]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    expect(row(wrapper, udpRelay.name).findAll('td')[2].text()).toContain('QUIC')
    expect(row(wrapper, udpRelay.name).findAll('td')[2].text()).not.toContain('TLS')
    expect(row(wrapper, relay.name).findAll('td')[2].text()).toContain('TLS')
    expect(row(wrapper, relay.name).findAll('td')[2].text()).not.toContain('QUIC')
  })

  it('filters endpoint roles and creates a tunnel using the selected exit policy without duplicating it', async () => {
    const entry = { ...source, role: 'entry' }
    const exit = { ...destination, role: 'exit', protocol: 'tls', targets: ['exit.example:443'], strategy: 'hash', protocol_options: { tls_server_name: 'relay.example' } }
    const legacy = { ...destination, id: 72, node_id: 72 }
    getRuleList.mockResolvedValue(result([entry, exit, legacy]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    expect(selectField(editor, 'entry_rule_id').findAllComponents(ElOption).map(option => option.props('value'))).toEqual([entry.id, legacy.id])
    expect(selectField(editor, 'exit_rule_id').findAllComponents(ElOption).map(option => option.props('value'))).toEqual([exit.id, legacy.id])
    expect(editor.find('#tunnel-target-title').exists()).toBe(false)
    await field(editor, 'name').get('input').setValue('Exit policy')
    await choose(selectField(editor, 'entry_rule_id'), entry.id)
    await choose(selectField(editor, 'exit_rule_id'), exit.id)
    expect(editor.get('#tunnel-exit-policy-title').text()).toBe('出口目标与策略')
    expect(editor.get('.tunnel-policy-summary').text()).toContain('TLS')
    expect(editor.get('.tunnel-policy-summary').text()).toContain('exit.example:443')
    expect(editor.get('.tunnel-policy-summary').text()).toContain('哈希')
    expect(editor.find('input[aria-label="目标地址 1"]').exists()).toBe(false)
    expect(editor.find('#tunnel-protocol').exists()).toBe(false)
    expect(editor.get('.gost-options-summary').text()).toContain('relay.example')
    expect(editor.find('[data-protocol-option="tls_server_name"]').exists()).toBe(false)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledExactlyOnceWith({
      name: 'Exit policy', entry_rule_id: entry.id, exit_rule_id: exit.id, remark: '', allowlist_node_ids: []
    })
  })

  it('validates the entry for a Clash exit and keeps policy ownership when editing and copying', async () => {
    const entry = { ...mediaSource, role: 'entry' }
    const exit = { ...mediaDestination, role: 'exit', protocol: 'quic', use_clash_ruleset: true, targets: [], strategy: 'round' }
    const badEntry = { ...source, role: 'entry', id: 75, protocol: 'udp' }
    const freshEntry = { ...source, role: 'entry' }
    const otherExit = { ...destination, role: 'exit', protocol: 'ws', targets: ['other.example:80'], strategy: 'rand' }
    const configured = { ...udpRelay, protocol: 'quic', entry_rule: entry, exit_rule: exit }
    getRuleList.mockResolvedValue(result([entry, exit, badEntry, freshEntry, otherExit]))
    getTunnelList.mockResolvedValue(result([configured]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, configured.name), 'edit')
    let editor = dialog(wrapper)
    expect(editor.get('.tunnel-policy-summary').text()).toContain('QUIC')
    expect(editor.get('.tunnel-policy-summary').text()).toContain('按原始目标')
    expect(editor.get('.target-routing-summary').text()).toContain('旧 Clash 订阅')
    await choose(selectField(editor, 'entry_rule_id'), badEntry.id)
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['exit_rule_id', false, '按原始目标需要选择“透明转发（HTTP/HTTPS）”入口'])
    expect(updateTunnel).not.toHaveBeenCalled()
    await choose(selectField(editor, 'entry_rule_id'), entry.id)
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledExactlyOnceWith(configured.id, {
      name: configured.name, entry_rule_id: entry.id, exit_rule_id: exit.id, remark: ''
    })
    await command(row(wrapper, configured.name), 'copy')
    editor = dialog(wrapper)
    expect(selectField(editor, 'entry_rule_id').props('modelValue')).toBe(null)
    expect(selectField(editor, 'exit_rule_id').props('modelValue')).toBe(null)
    expect(editor.find('#tunnel-exit-policy-title').exists()).toBe(false)
    await choose(selectField(editor, 'entry_rule_id'), freshEntry.id)
    await choose(selectField(editor, 'exit_rule_id'), otherExit.id)
    expect(editor.get('.tunnel-policy-summary').text()).toContain('other.example:80')
    expect(editor.get('.tunnel-policy-summary').text()).not.toContain('Clash 规则集')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledExactlyOnceWith({
      name: configured.name + '-copy', entry_rule_id: freshEntry.id, exit_rule_id: otherExit.id, remark: '', allowlist_node_ids: []
    })
  })

  it('assigns endpoint direction and saves an independent tunnel protocol, targets and balancing strategy', async () => {
    const neutralSource = { ...source, listen_ip: '192.0.2.1', use_clash_ruleset: false, targets: [] }
    const oldDestination = { ...destination, targets: ['old.example:80'] }
    getRuleList.mockResolvedValue(result([neutralSource, oldDestination]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await choose(selectField(editor, 'exit_rule_id'), destination.id)
    expect(editor.text()).toContain('目标与策略')
    expect(editor.text()).not.toContain('目标留空')
    const protocol = selectField(editor, 'protocol')
    expect(protocol.props('modelValue')).toBe('tcp')
    expect(protocol.findAllComponents(ElOption).map(option => option.props('value'))).toEqual([
      'tcp', 'mtcp', 'udp', 'tls', 'mtls', 'ws', 'mws', 'wss', 'mwss',
      'h2', 'h2c', 'grpc', 'pht', 'phts', 'kcp', 'quic', 'h3', 'ohttp', 'otls'
    ])
    expect(protocol.props('filterable')).toBe(true)
    expect(editor.get('label[for="tunnel-protocol"]').text()).toContain('隧道协议')
    expect(protocol.attributes('aria-describedby')).toBe('tunnel-protocol-help')
    expect(protocol.get('#tunnel-protocol').attributes('role')).toBe('combobox')
    expect(editor.get('#tunnel-protocol-help').text()).toContain('源节点到目的节点')
    expect(editor.get('input[aria-label="目标地址 1"]').attributes('aria-describedby')).toBe('tunnel-target-address-help')
    expect(editor.get('#tunnel-target-address-help').text()).toContain('HTTPS 会透传客户端发送的 SNI，无需额外开启')
    await choose(protocol, 'kcp')
    expect(selectField(editor, 'entry_rule_id').findAllComponents(ElOption).map(option => option.props('value'))).toEqual([source.id, destination.id])
    expect(selectField(editor, 'exit_rule_id').findAllComponents(ElOption).map(option => option.props('value'))).toEqual([source.id, destination.id])
    await field(editor, 'name').get('input').setValue('Fixed targets')
    await choose(selectField(editor, 'entry_rule_id'), source.id)
    await choose(selectField(editor, 'exit_rule_id'), destination.id)
    expect(protocol.props('modelValue')).toBe('kcp')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['targetList', false, '请至少填写一个目标地址'])
    expect(document.activeElement).toBe(editor.get('input[aria-label="目标地址 1"]').element)
    expect(createTunnel).not.toHaveBeenCalled()
    await editor.get('input[aria-label="目标地址 1"]').setValue('  app.example:443  ')
    await button(editor, '添加目标地址').trigger('click')
    await editor.get('input[aria-label="目标地址 2"]').setValue('[2001:db8::10]:443')
    await button(editor, '添加目标地址').trigger('click')
    await editor.get('input[aria-label="目标地址 3"]').setValue('unused.example:443')
    await editor.get('button[aria-label="移除目标地址 3"]').trigger('click')
    await choose(selectField(editor, 'strategy'), 'hash')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledExactlyOnceWith({
      name: 'Fixed targets', entry_rule_id: source.id, exit_rule_id: destination.id, protocol: 'kcp', remark: '', allowlist_node_ids: [],
      use_clash_ruleset: false, targets: ['app.example:443', '[2001:db8::10]:443'], strategy: 'hash'
    })
  })

  it('restores tunnel-owned protocol and targets when editing or copying and leaves endpoint choices unassigned in a copy', async () => {
    const configured = { ...udpRelay, protocol: 'h2', use_clash_ruleset: false, targets: ['saved.example:8443'], strategy: 'rand' }
    getTunnelList.mockResolvedValue(result([configured]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, configured.name), 'edit')
    const editor = dialog(wrapper)
    expect(selectField(editor, 'protocol').props('modelValue')).toBe('h2')
    expect(selectField(editor, 'use_original_target').props('modelValue')).toBe(false)
    expect(editor.get('input[aria-label="目标地址 1"]').element.value).toBe('saved.example:8443')
    expect(selectField(editor, 'strategy').props('modelValue')).toBe('rand')
    await choose(selectField(editor, 'protocol'), 'wss')
    await editor.get('input[aria-label="目标地址 1"]').setValue('changed.example:443')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledExactlyOnceWith(configured.id, {
      name: configured.name, entry_rule_id: mediaSource.id, exit_rule_id: mediaDestination.id, protocol: 'wss', remark: '',
      use_clash_ruleset: false, targets: ['changed.example:443'], strategy: 'rand'
    })
    await command(row(wrapper, configured.name), 'copy')
    const copy = dialog(wrapper)
    expect(selectField(copy, 'entry_rule_id').props('modelValue')).toBe(null)
    expect(selectField(copy, 'exit_rule_id').props('modelValue')).toBe(null)
    await choose(selectField(copy, 'exit_rule_id'), destination.id)
    expect(selectField(copy, 'protocol').props('modelValue')).toBe('h2')
    expect(copy.get('input[aria-label="目标地址 1"]').element.value).toBe('saved.example:8443')
    expect(selectField(copy, 'strategy').props('modelValue')).toBe('rand')
    await button(copy, '取消').trigger('click')
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    await choose(selectField(dialog(wrapper), 'exit_rule_id'), destination.id)
    expect(selectField(dialog(wrapper), 'protocol').props('modelValue')).toBe('tcp')
  })

  it('requires a transparent entry for original targets and keeps fixed addresses when switching modes', async () => {
    const incompatible = { ...source, id: 70, protocol: 'udp', use_clash_ruleset: false, targets: [] }
    const transparent = { ...source, protocol: 'red', use_clash_ruleset: false }
    getRuleList.mockResolvedValue(result([transparent, incompatible, destination]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await field(editor, 'name').get('input').setValue('Clash tunnel')
    await choose(selectField(editor, 'entry_rule_id'), source.id)
    await choose(selectField(editor, 'exit_rule_id'), destination.id)
    await editor.get('input[aria-label="目标地址 1"]').setValue('saved.example:443')
    await choose(selectField(editor, 'use_original_target'), true)
    expect(editor.find('input[aria-label="目标地址 1"]').exists()).toBe(false)
    await choose(selectField(editor, 'entry_rule_id'), incompatible.id)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['use_original_target', false, '按原始目标需要选择“透明转发（HTTP/HTTPS）”入口'])
    expect(createTunnel).not.toHaveBeenCalled()
    await choose(selectField(editor, 'use_original_target'), false)
    expect(editor.get('input[aria-label="目标地址 1"]').element.value).toBe('saved.example:443')
    await editor.get('button[aria-label="移除目标地址 1"]').trigger('click')
    expect(editor.get('input[aria-label="目标地址 1"]').element.value).toBe('')
    await choose(selectField(editor, 'entry_rule_id'), source.id)
    await choose(selectField(editor, 'use_original_target'), true)
    await choose(selectField(editor, 'match_rules'), true)
    await button(editor, '添加订阅规则集').trigger('click')
    await choose(editor.get('[data-rule-type="subscription"]').getComponent(ElSelect), 2)
    await button(editor, '添加手动域名').trigger('click')
    await editor.get('[data-rule-type="domain"] input').setValue('manual.example')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenCalledExactlyOnceWith({
      name: 'Clash tunnel', entry_rule_id: source.id, exit_rule_id: destination.id, protocol: 'tcp', remark: '', allowlist_node_ids: [],
      use_original_target: true, use_clash_ruleset: false, targets: [], strategy: 'round', ruleset_ids: [2], manual_domains: ['manual.example']
    })
  })

  it('shows the selected destination bind and the mapped source connection address', async () => {
    const mappedNode = { ...egress, address_mappings: [
      { listen_ip: '10.10.0.7', connect_ip: '198.51.100.77' },
      { listen_ip: '10.10.0.8', connect_ip: '2001:db8::80' }
    ] }
    const mappedDestination = { ...destination, node: mappedNode, listen_ip: '10.10.0.8' }
    const unmappedDestination = { ...mappedDestination, id: 70, listen_ip: '10.10.0.9' }
    getRuleList.mockResolvedValueOnce(result([source, mappedDestination, unmappedDestination]))
    getTunnelList.mockResolvedValueOnce(result([{
      ...relay, exit_node: mappedNode, exit_rule: { ...relayDestination, listen_ip: mappedDestination.listen_ip }
    }, udpRelay]))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    const relayRow = row(wrapper, relay.name)
    expect(relayRow.text()).toContain('本机监听 10.10.0.8:65535')
    expect(relayRow.text()).toContain('源端连接 [2001:db8::80]:65535')
    expect(row(wrapper, udpRelay.name).text()).not.toContain('源端连接')

    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    const destinationSelect = selectField(editor, 'exit_rule_id')
    expect(destinationSelect.findAllComponents(ElOption)
      .find(option => option.props('value') === mappedDestination.id).props('label')).toContain('10.10.0.8:9553')
    await choose(destinationSelect, mappedDestination.id)
    expect(editor.get('#destination-entry-help').text()).toContain('源端连接 [2001:db8::80]:9553')
    await choose(destinationSelect, unmappedDestination.id)
    expect(editor.get('#destination-entry-help').text()).not.toContain('源端连接')
  })

  it('loads later entrance pages and preserves a tunnel pair found after the first hundred', async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) => ({ ...source, id: index + 1000 }))
    const secondPage = [destination, mediaSource, mediaDestination]
    getRuleList.mockResolvedValueOnce(result(firstPage, 103)).mockResolvedValueOnce(result(secondPage, 103))
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    expect(getRuleList.mock.calls).toEqual([[{ page: 1, pageSize: 100 }], [{ page: 2, pageSize: 100 }]])
    expect(selectField(dialog(wrapper), 'exit_rule_id').findAllComponents(ElOption).map(option => option.props('value'))).toContain(destination.id)
    await button(dialog(wrapper), '取消').trigger('click')

    getRuleList.mockResolvedValueOnce(result(firstPage, 103)).mockResolvedValueOnce(result(secondPage, 103))
    await command(row(wrapper, 'Media relay'), 'edit')
    const editor = dialog(wrapper)
    expect(selectField(editor, 'entry_rule_id').props('modelValue')).toBe(mediaSource.id)
    expect(selectField(editor, 'exit_rule_id').props('modelValue')).toBe(mediaDestination.id)
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledWith(udpRelay.id, expect.objectContaining({
      entry_rule_id: mediaSource.id, exit_rule_id: mediaDestination.id
    }))
  })

  it('retains the loaded endpoint pair and blocks saving when a later refresh page fails', async () => {
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, 'Media relay'), 'edit')
    await button(dialog(wrapper), '取消').trigger('click')

    const firstPage = Array.from({ length: 100 }, (_, index) => ({ ...source, id: index + 1000 }))
    const secondPage = Promise.withResolvers()
    getRuleList.mockResolvedValueOnce(result(firstPage, 102)).mockReturnValueOnce(secondPage.promise)
    await command(row(wrapper, 'Media relay'), 'edit')
    const editor = dialog(wrapper)
    const entry = selectField(editor, 'entry_rule_id')
    const exit = selectField(editor, 'exit_rule_id')
    expect(entry.props('modelValue')).toBe(mediaSource.id)
    expect(exit.props('modelValue')).toBe(mediaDestination.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).not.toContain(firstPage[0].id)
    expect(button(editor, '保存更改').element.disabled).toBe(true)

    secondPage.reject(new Error('second page unavailable'))
    await flushPromises()
    expect(editor.get('[role="alert"]').text()).toContain('无法加载端点选项')
    expect(entry.props('modelValue')).toBe(mediaSource.id)
    expect(exit.props('modelValue')).toBe(mediaDestination.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).toContain(mediaSource.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).not.toContain(firstPage[0].id)
    await editor.get('form').trigger('submit')
    await flushPromises()
    expect(updateTunnel).not.toHaveBeenCalled()

    getRuleList.mockResolvedValueOnce(result(firstPage, 102)).mockResolvedValueOnce(result([mediaSource, mediaDestination], 102))
    await button(editor, '重新加载端点').trigger('click')
    await flushPromises()
    expect(getRuleList.mock.calls.slice(-2)).toEqual([[{ page: 1, pageSize: 100 }], [{ page: 2, pageSize: 100 }]])
    expect(editor.find('[role="alert"]').exists()).toBe(false)
    expect(entry.props('modelValue')).toBe(mediaSource.id)
    expect(exit.props('modelValue')).toBe(mediaDestination.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).toContain(firstPage[0].id)
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledExactlyOnceWith(udpRelay.id, {
      name: udpRelay.name, entry_rule_id: mediaSource.id, exit_rule_id: mediaDestination.id, protocol: 'udp', remark: '',
      use_clash_ruleset: true, targets: [], strategy: 'round'
    })
  })

  it('validates endpoint roles and preserves the selected pair after a failed create', async () => {
    const error = new Error('relay already listening')
    const log = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    await flushPromises()
    const editor = dialog(wrapper)
    await field(editor, 'name').get('input').setValue('New UDP relay')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['entry_rule_id', false, '请选择源端点'])
    expect(document.activeElement).toBe(selectField(editor, 'entry_rule_id').get('input').element)
    expect(createTunnel).not.toHaveBeenCalled()

    const sourceSelect = selectField(editor, 'entry_rule_id')
    const destinationSelect = selectField(editor, 'exit_rule_id')
    expect(sourceSelect.props('fitInputWidth')).toBe(true)
    expect(destinationSelect.props('fitInputWidth')).toBe(true)
    expect(sourceSelect.findAllComponents(ElOption).map(option => option.props('value'))).toEqual([source.id, destination.id, 23, 24])
    expect(sourceSelect.getComponent(ElOption).props('label')).toContain('TCP 127.0.0.1:443')
    await choose(sourceSelect, source.id)
    expect(destinationSelect.findAllComponents(ElOption).map(option => [option.props('value'), option.props('disabled')])).toEqual([
      [destination.id, false], [23, true]
    ])
    await choose(destinationSelect, destination.id)
    await choose(selectField(editor, 'protocol'), 'udp')
    await editor.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await choose(selectField(editor, 'allowlist_node_ids'), [ingress.id])
    await field(editor, 'remark').get('textarea').setValue('new service')
    createTunnel.mockRejectedValueOnce(error)
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(log).toHaveBeenCalledWith('保存隧道失败:', error)
    expect(getRuleList).toHaveBeenCalledTimes(2)
    expect(field(dialog(wrapper), 'name').get('input').element.value).toBe('New UDP relay')
    await button(editor, '添加并启动').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenLastCalledWith({
      name: 'New UDP relay', entry_rule_id: source.id, exit_rule_id: destination.id, protocol: 'udp', remark: 'new service',
      allowlist_node_ids: [ingress.id], use_clash_ruleset: false, targets: ['app.example:443'], strategy: 'round'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已添加并启动')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('protects running tunnels, reassigns stopped endpoints, and keeps legacy transport editing', async () => {
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, 'Office relay'), 'edit')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(updateTunnel).not.toHaveBeenCalled()

    await command(row(wrapper, 'Office relay'), 'copy')
    expect(field(dialog(wrapper), 'name').get('input').element.value).toBe('Office relay-copy')
    expect(selectField(dialog(wrapper), 'entry_rule_id').props('modelValue')).toBe(null)
    expect(selectField(dialog(wrapper), 'exit_rule_id').props('modelValue')).toBe(null)
    expect(selectField(dialog(wrapper), 'allowlist_node_ids').props('modelValue')).toEqual([])
    await button(dialog(wrapper), '取消').trigger('click')
    await command(row(wrapper, 'Media relay'), 'edit')
    expect(selectField(dialog(wrapper), 'entry_rule_id').props('modelValue')).toBe(mediaSource.id)
    expect(selectField(dialog(wrapper), 'allowlist_node_ids').props('modelValue')).toEqual([egress.id])
    await choose(selectField(dialog(wrapper), 'entry_rule_id'), source.id)
    await choose(selectField(dialog(wrapper), 'exit_rule_id'), destination.id)
    await choose(selectField(dialog(wrapper), 'use_original_target'), false)
    await dialog(wrapper).get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await button(dialog(wrapper), '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(13, {
      name: 'Media relay', entry_rule_id: source.id, exit_rule_id: destination.id, protocol: 'udp', remark: '',
      use_original_target: false, use_clash_ruleset: false, targets: ['app.example:443'], strategy: 'round', ruleset_ids: [], manual_domains: []
    })
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已更新')
    await command(row(wrapper, 'Unknown relay'), 'edit')
    expect(selectField(dialog(wrapper), 'protocol').props('modelValue')).toBe('tcp')
    expect(field(dialog(wrapper), 'relay_port').get('input').element.value).toBe('8443')
    expect(selectField(dialog(wrapper), 'protocol').props('filterable')).toBe(true)
    expect(selectField(dialog(wrapper), 'protocol').findAllComponents(ElOption).map(option => option.props('value'))).toEqual(expect.arrayContaining(['h2', 'wss', 'kcp']))
    await choose(selectField(dialog(wrapper), 'protocol'), 'h2')
    await choose(selectField(dialog(wrapper), 'allowlist_node_ids'), [ingress.id])
    await button(dialog(wrapper), '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(14, {
      name: 'Unknown relay', protocol: 'h2', relay_port: 8443, remark: '', allowlist_node_ids: [ingress.id]
    })
  })

  it('keeps previously saved source IPs in allowlist previews and excludes local listener IPs', async () => {
    getRoutingNodes.mockResolvedValue([
      { ...ingress, manual_addresses: ['192.0.2.1', '198.51.100.40'], local_addresses: ['10.0.0.5'] },
      { ...egress, manual_addresses: ['198.51.100.90', '2001:db8:1::9'] },
      { ...legacyNode, manual_addresses: ['198.51.100.3', '198.51.100.50'] }
    ])
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, 'Office relay'), 'allowlist')
    const editor = dialog(wrapper)
    const allowlist = selectField(editor, 'allowlist_node_ids')
    expect(allowlist.findAllComponents(ElOption).map(option => [option.props('label'), option.props('disabled')])).toEqual([
      ['入口 A（192.0.2.1 / 192.0.2.2 / 2001:db8::1 / 198.51.100.40）', false],
      ['出口 B（198.51.100.90 / 2001:db8:1::9）', false],
      ['旧版节点（198.51.100.3 / 198.51.100.50）', false],
      ['节点 #88（未找到）', false]
    ])
    expect(editor.findAll('code').map(item => item.text())).toEqual(['192.0.2.1', '192.0.2.2', '2001:db8::1', '198.51.100.40'])
    await choose(allowlist, [egress.id])
    expect(editor.findAll('code').map(item => item.text())).toEqual(['198.51.100.90', '2001:db8:1::9'])
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnelAllowlist).toHaveBeenCalledWith(relay.id, [egress.id])
  })

  it('edits and clears a running tunnel allowlist without changing its endpoints or other tunnels', async () => {
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, 'Office relay'), 'allowlist')
    const editor = dialog(wrapper)
    expect(editor.attributes('aria-label')).toBe('Gost IP 白名单')
    expect(editor.text()).toContain('Office relay')
    expect(getRuleList).not.toHaveBeenCalled()
    const allowlist = selectField(editor, 'allowlist_node_ids')
    expect(editor.get('label[for="tunnel-allowlist-nodes"]').text()).toBe('Gost IP 白名单')
    expect(allowlist.get('#tunnel-allowlist-nodes').attributes('role')).toBe('combobox')
    expect(allowlist.props('modelValue')).toEqual([ingress.id, 88])
    expect(allowlist.findAllComponents(ElOption).map(option => [option.props('label'), option.props('disabled')])).toEqual([
      ['入口 A（192.0.2.1 / 192.0.2.2 / 2001:db8::1）', false],
      ['出口 B（未获取 IP）', true],
      ['旧版节点（198.51.100.3）', false],
      ['节点 #88（未找到）', false]
    ])
    expect(editor.findAll('code').map(item => item.text())).toEqual(['192.0.2.1', '192.0.2.2', '2001:db8::1'])
    await choose(allowlist, [legacyNode.id])
    expect(editor.findAll('code').map(item => item.text())).toEqual(['198.51.100.3'])
    await choose(allowlist, [])
    updateTunnelAllowlist.mockRejectedValueOnce({ response: { data: { message: '白名单保存失败' } } })
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    const alert = editor.get('[role="alert"]')
    expect(alert.text()).toContain('白名单保存失败')
    expect(document.activeElement).toBe(alert.element)

    const pending = Promise.withResolvers()
    updateTunnelAllowlist.mockClear().mockReturnValueOnce(pending.promise)
    await editor.get('form').trigger('submit')
    await editor.get('form').trigger('submit')
    expect(updateTunnelAllowlist).toHaveBeenCalledExactlyOnceWith(relay.id, [])
    expect(updateTunnel).not.toHaveBeenCalled()
    pending.resolve({ data: { ...relay, allowlist_node_ids: [] } })
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledWith('Gost IP 白名单已保存')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)

    await command(row(wrapper, 'Media relay'), 'allowlist')
    expect(selectField(dialog(wrapper), 'allowlist_node_ids').props('modelValue')).toEqual([egress.id])
    expect(dialog(wrapper).text()).toContain('所选节点暂无可用 IP')
  })

  it('disables competing toggles during a pending start and confirms deletions', async () => {
    const log = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    const pending = Promise.withResolvers()
    startTunnel.mockReturnValueOnce(pending.promise)
    const start = button(row(wrapper, 'Media relay'), '启动')
    await start.trigger('click')
    expect(start.classes()).toContain('is-loading')
    expect(button(row(wrapper, 'Office relay'), '停止').element.disabled).toBe(true)
    const failure = new Error('relay unreachable')
    pending.reject(failure)
    await flushPromises()
    expect(log).toHaveBeenCalledWith('更新隧道状态失败:', failure)
    expect(start.classes()).not.toContain('is-loading')
    await start.trigger('click')
    await flushPromises()
    expect(startTunnel).toHaveBeenLastCalledWith(13)
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已启动')
    await button(row(wrapper, 'Office relay'), '停止').trigger('click')
    await flushPromises()
    expect(stopTunnel).toHaveBeenCalledWith(12)
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已停止')

    ElMessageBox.confirm.mockRejectedValueOnce('cancel').mockRejectedValueOnce('close')
    await command(row(wrapper, 'Media relay'), 'delete')
    await command(row(wrapper, 'Media relay'), 'delete')
    expect(deleteTunnel).not.toHaveBeenCalled()
    deleteTunnel.mockRejectedValueOnce(failure)
    await command(row(wrapper, 'Media relay'), 'delete')
    expect(log).toHaveBeenCalledWith('删除隧道失败:', failure)
    await command(row(wrapper, 'Media relay'), 'delete')
    expect(deleteTunnel).toHaveBeenLastCalledWith(13)
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已删除')
  })
})
