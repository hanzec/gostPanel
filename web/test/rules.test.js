import { flushPromises } from '@vue/test-utils'
import { ElForm, ElMessage, ElMessageBox, ElOption, ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Rules from '@/views/Rules.vue'
import { getNodeList } from '@/api/node'
import { checkRuleListener, createRule, deleteRule, getRuleList, startRule, stopRule, updateRule } from '@/api/rule'
import { button, changePage, choose, command, field, mountInventory, result, row, select } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getNodeList: vi.fn() }))
vi.mock('@/api/rule', () => ({
  checkRuleListener: vi.fn(), createRule: vi.fn(), deleteRule: vi.fn(), getRuleList: vi.fn(),
  startRule: vi.fn(), stopRule: vi.fn(), updateRule: vi.fn()
}))

const entryNode = { id: 4, name: '入口 A', ipv4: '192.0.2.10', ipv6: '2001:db8::8', manual_addresses: ['198.51.100.10'] }
const direct = {
  id: 1, name: 'HTTPS', status: 'running', node: entryNode, node_id: entryNode.id,
  protocol: 'tcp', listen_ip: '192.0.2.10', listen_port: 443, targets: ['primary.test:443', 'fallback.test:443'],
  strategy: 'rand', remark: 'Office', input_bytes: 1024, output_bytes: 2048, total_bytes: 3072
}
const relayRule = {
  id: 2, name: 'DNS', status: 'stopped', node: entryNode, node_id: entryNode.id,
  protocol: 'udp', listen_ip: '2001:db8::8', listen_port: 65535, targets: ['dns.test:53']
}
const rules = [
  direct,
  relayRule,
  { id: 3, name: 'Relay destination', status: 'error', listen_port: 8080, linked_tunnel_id: 8, tunnel_role: 'exit' },
  { id: 4, name: 'Unlinked port', listen_port: 8081 }
]
const selectField = (wrapper, prop) => field(wrapper, prop).getComponent(ElSelect)
const editDialog = wrapper => wrapper.get('[role="dialog"]')

beforeEach(() => {
  getNodeList.mockResolvedValue(result([entryNode]))
  getRuleList.mockResolvedValue(result(rules, 28))
  checkRuleListener.mockResolvedValue({ data: { available: true } })
  for (const action of [createRule, updateRule, deleteRule, startRule, stopRule]) action.mockResolvedValue({})
})

describe('forwarding rule inventory', () => {
  it('keeps the previous rules on refresh failure and applies filters from page one', async () => {
    const response = Promise.withResolvers()
    getRuleList.mockReturnValueOnce(response.promise)
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('正在读取入口规则')
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['—', '—', '—', '—'])
    response.resolve(result(rules, 28))
    await flushPromises()
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['28', '1', '1', '1'])
    expect(row(wrapper, 'HTTPS').get('details li').text()).toBe('fallback.test:443')
    expect(row(wrapper, 'HTTPS').get('.rule-traffic-total').text()).toBe('3 KB')
    expect(row(wrapper, 'DNS').text()).toContain('入口 A')
    expect(row(wrapper, 'HTTPS').text()).toContain('192.0.2.10:443')
    expect(row(wrapper, 'DNS').text()).toContain('[2001:db8::8]:65535')
    expect(row(wrapper, 'Relay destination').text()).toContain('隧道目的端')
    expect(row(wrapper, 'Relay destination').find('[aria-label="启动 Relay destination"]').exists()).toBe(false)
    expect(row(wrapper, 'Relay destination').find('[aria-label="编辑 Relay destination"]').exists()).toBe(false)
    expect(row(wrapper, 'Relay destination').text()).toContain('隧道管理')
    expect(row(wrapper, 'Unlinked port').text()).toContain('未知节点')
    expect(row(wrapper, 'Unlinked port').get('.rule-traffic-total').text()).toBe('0 B')

    await changePage(wrapper, 2)
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 2, pageSize: 10, keyword: '', node_id: '', status: '' })
    getRuleList.mockRejectedValueOnce(new Error('offline'))
    await button(wrapper, '刷新').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('下方保留了上次获取的结果。')
    expect(row(wrapper, 'HTTPS').exists()).toBe(true)

    await wrapper.get('#rule-keyword').setValue('  office  ')
    await choose(select(wrapper, 'rule-node'), 4)
    await choose(select(wrapper, 'rule-status'), 'running')
    getRuleList.mockResolvedValueOnce({ data: {} })
    await wrapper.get('form[aria-label="规则筛选"]').trigger('submit')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: 'office', node_id: 4, status: 'running' })
    expect(wrapper.get('.rule-placeholder').text()).toContain('没有匹配的规则')
    getRuleList.mockResolvedValueOnce(result())
    await button(wrapper, '清除筛选').trigger('click')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '', node_id: '', status: '' })
    expect(wrapper.get('.rule-placeholder').text()).toContain('还没有入口规则')
  })

  it('pauses automatic polling for background tabs or failures and resumes after a manual retry', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    getNodeList.mockRejectedValueOnce(new Error('nodes unavailable'))
    getRuleList.mockRejectedValueOnce(new Error('rules unavailable'))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('请检查连接后重试。')
    await vi.advanceTimersByTimeAsync(5000)
    expect(getRuleList).toHaveBeenCalledTimes(1)
    await button(wrapper, '重试').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)

    hidden.mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(5000)
    expect(getRuleList).toHaveBeenCalledTimes(2)
    hidden.mockReturnValue(false)
    await vi.advanceTimersByTimeAsync(5000)
    expect(getRuleList).toHaveBeenCalledTimes(3)
    expect(row(wrapper, 'HTTPS').exists()).toBe(true)
  })

  it('ignores a slow response after the user changes pages again', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    const older = Promise.withResolvers()
    getRuleList.mockReturnValueOnce(older.promise).mockResolvedValueOnce(result([relayRule], 40))
    await changePage(wrapper, 2)
    await changePage(wrapper, 3)
    older.resolve(result([direct], 40))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('DNS')
    expect(wrapper.get('tbody').text()).not.toContain('HTTPS')
  })
})

describe('forwarding rule editor and actions', () => {
  it('creates a standalone endpoint after validating the node and port, then checks that listener', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建规则').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('  internal API  ')
    expect(field(dialog, 'listen_port').get('input').element.value).toBe('')
    await button(dialog, '创建规则').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['node_id', false, '请选择所在节点'])
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['listen_port', false, '请输入监听端口'])
    expect(createRule).not.toHaveBeenCalled()
    expect(checkRuleListener).not.toHaveBeenCalled()
    await choose(selectField(dialog, 'node_id'), 4)
    await choose(selectField(dialog, 'protocol'), 'udp')
    const listenIP = selectField(dialog, 'listen_ip')
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('label'))).toEqual([
      '所有接口', 'IPv4 回环 · 127.0.0.1', 'IPv6 回环 · ::1',
      '入口探测地址 · 192.0.2.10', '入口探测地址 · 2001:db8::8'
    ])
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('value'))).not.toContain(entryNode.manual_addresses[0])
    await choose(listenIP, entryNode.ipv4)
    await field(dialog, 'listen_port').get('input').setValue('5353')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('192.0.2.10:5353')
    await dialog.get('[aria-label="目标地址 1"]').setValue(' api.test:53 ')
    await button(dialog, '添加目标地址').trigger('click')
    await button(dialog, '添加目标地址').trigger('click')
    await dialog.get('[aria-label="目标地址 3"]').setValue('dns.test:53')
    await dialog.get('[aria-label="移除目标地址 3"]').trigger('click')
    const created = { ...relayRule, id: 99, name: 'internal API', listen_ip: entryNode.ipv4, listen_port: 5353 }
    createRule.mockResolvedValueOnce({ data: created })
    getRuleList.mockResolvedValueOnce(result([created]))
    checkRuleListener.mockResolvedValueOnce({ data: { available: false, reason: 'occupied' } })
    await button(dialog, '创建规则').trigger('click')
    await flushPromises()

    expect(createRule).toHaveBeenCalledWith({
      node_id: 4, name: 'internal API', protocol: 'udp',
      listen_ip: '192.0.2.10', listen_port: 5353, use_clash_ruleset: false,
      targets: ['api.test:53'], strategy: 'round', remark: ''
    })
    expect(ElMessage.success).toHaveBeenCalledWith('规则已创建')
    expect(checkRuleListener).toHaveBeenCalledWith(99)
    expect(vi.mocked(createRule).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(checkRuleListener).mock.invocationCallOrder[0])
    expect(row(wrapper, 'internal API').text()).toContain('监听地址已被占用')
    await command(row(wrapper, 'internal API'), 'listener-check')
    expect(row(wrapper, 'internal API').text()).toContain('监听地址可用')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('configures a local TCP source with Clash before a tunnel is assigned', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建规则').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('透明入口')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    expect(dialog.get('#rule-target-mode-help').text()).toContain('需要 TCP 与 127.0.0.1')
    const listenIP = selectField(dialog, 'listen_ip')
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('value'))).toContain(entryNode.ipv6)
    const custom = listenIP.get('input')
    await custom.setValue(' 10.0.0.4 ')
    await custom.trigger('keydown', { key: 'Enter', code: 'Enter' })
    await field(dialog, 'listen_port').get('input').setValue('443')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('10.0.0.4:443')
    await choose(listenIP, '127.0.0.1')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('127.0.0.1:443')
    await dialog.get('[aria-label="目标地址 1"]').setValue('unused.test:443')
    await choose(selectField(dialog, 'use_clash_ruleset'), true)
    expect(dialog.find('[aria-label="目标地址 1"]').exists()).toBe(false)
    await choose(selectField(dialog, 'protocol'), 'udp')
    await button(dialog, '创建规则').trigger('click')
    await flushPromises()
    expect(createRule).not.toHaveBeenCalled()
    expect(field(dialog, 'use_clash_ruleset').text()).toContain('Clash 分流需要 TCP 与 127.0.0.1')
    await choose(selectField(dialog, 'protocol'), 'tcp')
    await button(dialog, '创建规则').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith({
      node_id: entryNode.id, name: '透明入口', protocol: 'tcp',
      listen_ip: '127.0.0.1', listen_port: 443, use_clash_ruleset: true,
      targets: [], strategy: 'round', remark: ''
    })
  })

  it('shows the tunnel link for a running Clash entry', async () => {
    getRuleList.mockResolvedValueOnce(result([{
      ...relayRule, protocol: 'tcp', listen_ip: '127.0.0.1', listen_port: 443,
      use_clash_ruleset: true, targets: [], status: 'running', linked_tunnel_id: 8, tunnel_role: 'entry'
    }]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(row(wrapper, 'DNS').text()).toContain('Clash 规则集')
    expect(row(wrapper, 'DNS').text()).toContain('隧道源端')
    expect(row(wrapper, 'DNS').find('[aria-label="停止 DNS"]').exists()).toBe(false)
    expect(row(wrapper, 'DNS').text()).toContain('隧道管理')
  })

  it('keeps edits on a failed save and requires a port when copying', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 HTTPS"]').trigger('click')
    const dialog = editDialog(wrapper)
    expect(selectField(dialog, 'node_id').props('disabled')).toBe(true)
    await field(dialog, 'name').get('input').setValue('HTTPS updated')
    updateRule.mockRejectedValueOnce(new Error('port conflict'))
    await button(dialog, '保存规则').trigger('click')
    await flushPromises()
    expect(editDialog(wrapper).exists()).toBe(true)
    expect(field(dialog, 'name').get('input').element.value).toBe('HTTPS updated')
    await button(dialog, '保存规则').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenLastCalledWith(1, {
      node_id: 4, name: 'HTTPS updated', protocol: 'tcp',
      listen_ip: '192.0.2.10', listen_port: 443, use_clash_ruleset: false,
      targets: direct.targets, strategy: 'rand', remark: 'Office'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('规则已保存')

    await command(row(wrapper, 'DNS'), 'copy')
    expect(field(editDialog(wrapper), 'name').get('input').element.value).toBe('DNS-copy')
    expect(field(editDialog(wrapper), 'listen_port').get('input').element.value).toBe('')
    await field(editDialog(wrapper), 'listen_port').get('input').setValue('5354')
    await button(editDialog(wrapper), '创建规则').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith({
      node_id: 4, name: 'DNS-copy', protocol: 'udp',
      listen_ip: '2001:db8::8', listen_port: 5354, use_clash_ruleset: false,
      targets: ['dns.test:53'], strategy: 'round', remark: ''
    })
  })

  it('starts, stops and deletes rules through their row actions', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    const pending = Promise.withResolvers()
    startRule.mockReturnValueOnce(pending.promise)
    const start = wrapper.get('[aria-label="启动 DNS"]')
    await start.trigger('click')
    expect(start.classes()).toContain('is-loading')
    await start.trigger('click')
    expect(startRule).toHaveBeenCalledTimes(1)
    pending.reject(new Error('startup denied'))
    await flushPromises()
    expect(start.classes()).not.toContain('is-loading')
    await start.trigger('click')
    await flushPromises()
    expect(startRule).toHaveBeenLastCalledWith(2)
    expect(ElMessage.success).toHaveBeenCalledWith('规则已启动')

    await wrapper.get('[aria-label="停止 HTTPS"]').trigger('click')
    await flushPromises()
    expect(stopRule).toHaveBeenCalledWith(1)
    expect(ElMessage.success).toHaveBeenCalledWith('规则已停止')

    ElMessageBox.confirm.mockRejectedValueOnce('cancel')
    await command(row(wrapper, 'DNS'), 'delete')
    expect(deleteRule).not.toHaveBeenCalled()
    await command(row(wrapper, 'DNS'), 'delete')
    expect(deleteRule).toHaveBeenCalledWith(2)
    expect(ElMessage.success).toHaveBeenCalledWith('规则已删除')
  })
})

describe('forwarding rule JSON exchange', () => {
  it('exports every page with only editable fields and handles clipboard failure', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    const batch = Array.from({ length: 100 }, (_, index) => ({ ...direct, id: index + 1 }))
    getRuleList.mockResolvedValueOnce(result(batch, 101)).mockResolvedValueOnce(result([relayRule], 101))
    await button(wrapper, '导出 JSON').trigger('click')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 2, pageSize: 100 })
    const dialog = editDialog(wrapper)
    const json = dialog.get('textarea[aria-label="导出的 JSON 配置"]').element.value
    const exported = JSON.parse(json)
    expect(exported).toHaveLength(101)
    expect(exported[0]).toEqual({
      name: 'HTTPS', node_id: 4, protocol: 'tcp',
      listen_ip: '192.0.2.10', listen_port: 443, use_clash_ruleset: false,
      targets: direct.targets, strategy: 'rand', remark: 'Office'
    })
    expect(exported.at(-1)).toMatchObject({ name: 'DNS', node_id: 4, remark: '' })
    const writeText = vi.fn().mockResolvedValueOnce().mockRejectedValueOnce(new Error('denied'))
    const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    try {
      await button(dialog, '复制 JSON').trigger('click')
      await flushPromises()
      expect(writeText).toHaveBeenCalledWith(json)
      expect(ElMessage.success).toHaveBeenCalledWith('已复制 JSON')
      await button(dialog, '复制 JSON').trigger('click')
      await flushPromises()
      expect(ElMessage.warning).toHaveBeenCalledWith('剪贴板不可用，请选中文本手动复制。')
    } finally {
      if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard)
      else delete navigator.clipboard
    }
    await button(dialog, '关闭').trigger('click')
    getRuleList.mockResolvedValueOnce(result())
    await button(wrapper, '导出 JSON').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(ElMessage.warning).toHaveBeenCalledWith('没有可导出的规则')
    getRuleList.mockRejectedValueOnce(new Error('offline'))
    await button(wrapper, '导出 JSON').trigger('click')
    await flushPromises()
    expect(button(wrapper, '导出 JSON').classes()).not.toContain('is-loading')
  })

  it('preserves legacy tunnel routing when exported rules are imported again', async () => {
    const legacy = {
      ...relayRule, type: 'tunnel', node_id: null, tunnel_id: 8,
      tunnel: { id: 8, entry_node_id: entryNode.id }
    }
    const standalone = { ...direct, type: 'forward', tunnel_id: null }
    getRuleList.mockResolvedValue(result([legacy, standalone]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '导出 JSON').trigger('click')
    await flushPromises()
    const json = editDialog(wrapper).get('textarea').element.value
    const exported = JSON.parse(json)
    expect(exported[0]).toMatchObject({ type: 'tunnel', tunnel_id: 8, node_id: null })
    expect(exported[0]).not.toHaveProperty('tunnel')
    expect(exported[1]).toMatchObject({ type: 'forward', tunnel_id: null, node_id: entryNode.id })
    await button(editDialog(wrapper), '关闭').trigger('click')
    await button(wrapper, '导入 JSON').trigger('click')
    await editDialog(wrapper).get('textarea').setValue(json)
    await button(editDialog(wrapper), '创建导入规则').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledTimes(2)
    expect(createRule).toHaveBeenNthCalledWith(1, {
      ...exported[0], strategy: 'round'
    })
    expect(createRule).toHaveBeenNthCalledWith(2, exported[1])
    expect(startRule).not.toHaveBeenCalled()
    expect(ElMessage.success).toHaveBeenCalledWith('已导入 2 条规则')
  })

  it('validates JSON and reports complete, partial and unsuccessful imports', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    const importJson = async source => {
      const dialog = editDialog(wrapper)
      await dialog.get('textarea').setValue(source)
      await button(dialog, '创建导入规则').trigger('click')
      await flushPromises()
    }

    await button(wrapper, '导入 JSON').trigger('click')
    for (const [source, error] of [
      ['  ', '请粘贴规则配置。'],
      ['{ invalid', 'JSON 格式有误'],
      ['[null]', '请输入规则对象'],
      ['[]', '请输入规则对象']
    ]) {
      await importJson(source)
      expect(editDialog(wrapper).get('#rule-import-error').text()).toContain(error)
    }
    expect(createRule).not.toHaveBeenCalled()

    createRule.mockRejectedValueOnce(new Error('invalid port'))
    await importJson('{"listen_port":0}')
    expect(editDialog(wrapper).get('#rule-import-error').text()).toContain('导入失败。')
    expect(createRule).toHaveBeenLastCalledWith({
      name: '导入规则', node_id: undefined,
      protocol: 'tcp', listen_ip: '', listen_port: 0, use_clash_ruleset: false,
      targets: [], strategy: 'round', remark: ''
    })
    createRule.mockRejectedValueOnce(new Error('port conflict'))
    await importJson('[{"name":"duplicate"},{"name":"new proxy","node_id":4,"protocol":"tcp","listen_ip":"127.0.0.1","listen_port":443,"use_clash_ruleset":true,"strategy":"hash","remark":"TLS"}]')
    expect(createRule).toHaveBeenLastCalledWith({
      name: 'new proxy', node_id: 4, protocol: 'tcp',
      listen_ip: '127.0.0.1', listen_port: 443, use_clash_ruleset: true,
      targets: [], strategy: 'hash', remark: 'TLS'
    })
    expect(ElMessage.warning).toHaveBeenCalledWith('已导入 1 条，失败 1 条')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)

    await button(wrapper, '导入 JSON').trigger('click')
    expect(editDialog(wrapper).get('textarea').element.value).toBe('')
    await importJson('{"name":"web","node_id":4,"listen_port":80}')
    expect(ElMessage.success).toHaveBeenCalledWith('已导入 1 条规则')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})
