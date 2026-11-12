import { flushPromises } from '@vue/test-utils'
import { ElForm, ElMessage, ElMessageBox, ElOption, ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Tunnels from '@/views/Tunnels.vue'
import { getRoutingNodes } from '@/api/node'
import { getRuleList } from '@/api/rule'
import { createTunnel, deleteTunnel, getTunnelList, startTunnel, stopTunnel, updateTunnel, updateTunnelAllowlist } from '@/api/tunnel'
import { button, changePage, choose, command, field, mountInventory, result, row, select } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getRoutingNodes: vi.fn() }))
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
  allowlist_node_ids: [egress.id]
}
const missingRelay = { id: 14, name: 'Unknown relay', entry_node_id: 31, exit_node_id: 32, relay_port: 8443 }
const tunnels = [relay, udpRelay, { ...missingRelay, id: 15, name: 'Broken relay', status: 'error' }, missingRelay]
const dialog = wrapper => wrapper.get('[role="dialog"]')
const selectField = (wrapper, prop) => field(wrapper, prop).getComponent(ElSelect)

beforeEach(() => {
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
    expect(dialog(wrapper).text()).toContain('无法加载入口选项')
    await button(dialog(wrapper), '重新加载入口').trigger('click')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 100 })
    expect(dialog(wrapper).text()).not.toContain('无法加载入口选项')
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
    expect(dialog(wrapper).text()).toContain('源端或目的端缺少可用入口')
    await dialog(wrapper).get('.tunnel-link').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

describe('Relay tunnel configuration', () => {
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
    expect(editor.get('[role="alert"]').text()).toContain('无法加载入口选项')
    expect(entry.props('modelValue')).toBe(mediaSource.id)
    expect(exit.props('modelValue')).toBe(mediaDestination.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).toContain(mediaSource.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).not.toContain(firstPage[0].id)
    await editor.get('form').trigger('submit')
    await flushPromises()
    expect(updateTunnel).not.toHaveBeenCalled()

    getRuleList.mockResolvedValueOnce(result(firstPage, 102)).mockResolvedValueOnce(result([mediaSource, mediaDestination], 102))
    await button(editor, '重新加载入口').trigger('click')
    await flushPromises()
    expect(getRuleList.mock.calls.slice(-2)).toEqual([[{ page: 1, pageSize: 100 }], [{ page: 2, pageSize: 100 }]])
    expect(editor.find('[role="alert"]').exists()).toBe(false)
    expect(entry.props('modelValue')).toBe(mediaSource.id)
    expect(exit.props('modelValue')).toBe(mediaDestination.id)
    expect(entry.findAllComponents(ElOption).map(option => option.props('value'))).toContain(firstPage[0].id)
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenCalledExactlyOnceWith(udpRelay.id, {
      name: udpRelay.name, entry_rule_id: mediaSource.id, exit_rule_id: mediaDestination.id, remark: ''
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
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['entry_rule_id', false, '请选择源端入口'])
    expect(document.activeElement).toBe(selectField(editor, 'entry_rule_id').get('input').element)
    expect(createTunnel).not.toHaveBeenCalled()

    const sourceSelect = selectField(editor, 'entry_rule_id')
    const destinationSelect = selectField(editor, 'exit_rule_id')
    expect(sourceSelect.findAllComponents(ElOption).map(option => option.props('value'))).toEqual([source.id])
    expect(sourceSelect.getComponent(ElOption).props('label')).toContain('TCP 127.0.0.1:443')
    await choose(sourceSelect, source.id)
    expect(destinationSelect.findAllComponents(ElOption).map(option => [option.props('value'), option.props('disabled')])).toEqual([
      [destination.id, false], [23, true]
    ])
    await choose(destinationSelect, destination.id)
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
      name: 'New UDP relay', entry_rule_id: source.id, exit_rule_id: destination.id, remark: 'new service',
      allowlist_node_ids: [ingress.id]
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
    await button(dialog(wrapper), '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(13, {
      name: 'Media relay', entry_rule_id: source.id, exit_rule_id: destination.id, remark: ''
    })
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已更新')
    await command(row(wrapper, 'Unknown relay'), 'edit')
    expect(selectField(dialog(wrapper), 'protocol').props('modelValue')).toBe('tcp')
    expect(field(dialog(wrapper), 'relay_port').get('input').element.value).toBe('8443')
    await choose(selectField(dialog(wrapper), 'protocol'), 'tls')
    await choose(selectField(dialog(wrapper), 'allowlist_node_ids'), [ingress.id])
    await button(dialog(wrapper), '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(14, {
      name: 'Unknown relay', protocol: 'tls', relay_port: 8443, remark: '', allowlist_node_ids: [ingress.id]
    })
  })

  it('merges supplementary IPs into allowlist previews and allows manually addressed nodes', async () => {
    getRoutingNodes.mockResolvedValue([
      { ...ingress, manual_addresses: ['192.0.2.1', '198.51.100.40'] },
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
