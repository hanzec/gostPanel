import { flushPromises } from '@vue/test-utils'
import { ElForm, ElMessage, ElMessageBox, ElOption, ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Rules from '@/views/Rules.vue'
import { getRoutingNodes } from '@/api/node'
import { getGostRulesets } from '@/api/system'
import request from '@/utils/request'
import { checkRuleListener, createRule, deleteRule, getRuleList, startRule, stopRule, updateRule } from '@/api/rule'
import { button, changePage, choose, command, field, mountInventory, result, row, select } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getRoutingNodes: vi.fn() }))
vi.mock('@/api/system', () => ({ getGostRulesets: vi.fn() }))
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
const routingRows = wrapper => wrapper.findAll('.target-rule-table tbody tr[data-rule-type]')

beforeEach(() => {
  getGostRulesets.mockResolvedValue({ data: [{ id: 1, name: '旧 Clash 订阅' }, { id: 2, name: '工作域名' }] })
  getRoutingNodes.mockResolvedValue([entryNode])
  getRuleList.mockResolvedValue(result(rules, 28))
  checkRuleListener.mockResolvedValue({ data: { available: true } })
  for (const action of [createRule, updateRule, deleteRule, startRule, stopRule]) action.mockResolvedValue({})
})

describe('forwarding rule inventory', () => {
  it('keeps the title, filters and rule creation in one inventory toolbar', async () => {
    getRuleList.mockResolvedValueOnce(result())
    const wrapper = mountInventory(Rules)
    await flushPromises()
    const toolbar = wrapper.get('section[aria-labelledby="rule-list-title"] > header')
    expect(toolbar.get('h2').text()).toBe('端点')
    const filters = toolbar.get('form[aria-label="端点筛选"]')
    expect(button(filters, '应用筛选').attributes('type')).toBe('submit')
    expect(wrapper.findAll('button').filter(item => item.text() === '新建端点')).toHaveLength(1)
    expect(wrapper.text()).not.toMatch(/导入 JSON|导出 JSON|每 5 秒更新/)
    expect(wrapper.findAll('button').some(item => item.text() === '刷新')).toBe(false)
    await button(toolbar, '新建端点').trigger('click')
    expect(editDialog(wrapper).attributes('aria-label')).toBe('新建端点')
  })

  it('shows node option loading and lets the filter and editor retry a failed request', async () => {
    const pending = Promise.withResolvers()
    getRoutingNodes.mockReturnValueOnce(pending.promise).mockRejectedValueOnce(new Error('still offline'))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(wrapper.get('.rule-node-status').text()).toContain('正在加载节点选项')
    expect(select(wrapper, 'rule-node').props('disabled')).toBe(true)

    pending.reject(new Error('nodes unavailable'))
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('无法加载节点选项')
    await button(wrapper, '重新加载节点').trigger('click')
    await flushPromises()
    expect(getRoutingNodes).toHaveBeenCalledTimes(2)

    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    expect(dialog.get('[role="alert"]').text()).toContain('无法加载节点选项')
    expect(selectField(dialog, 'node_id').props('disabled')).toBe(true)
    expect(button(dialog, '创建端点').element.disabled).toBe(true)
    await dialog.get('form').trigger('submit')
    expect(createRule).not.toHaveBeenCalled()

    const retry = Promise.withResolvers()
    getRoutingNodes.mockReturnValueOnce(retry.promise)
    await button(dialog, '重新加载节点').trigger('click')
    expect(dialog.get('[role="status"]').text()).toContain('正在加载节点选项')
    retry.resolve([entryNode])
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(select(wrapper, 'rule-node').props('disabled')).toBe(false)
    expect(selectField(dialog, 'node_id').props('disabled')).toBe(false)
    expect(button(dialog, '创建端点').element.disabled).toBe(false)
  })

  it('uses all node pages for filtering and creating an entrance on a later node', async () => {
    const actualNodeAPI = await vi.importActual('@/api/node')
    getRoutingNodes.mockImplementationOnce(actualNodeAPI.getRoutingNodes)
    const firstPage = Array.from({ length: 100 }, (_, index) => ({ id: index + 1, name: '节点 ' + (index + 1) }))
    const lastNode = { id: 101, name: '第二页节点', ipv4: '192.0.2.101' }
    const originalAdapter = request.defaults.adapter
    const adapter = vi.fn(config => Promise.resolve({
      status: 200, statusText: 'OK', headers: {}, config,
      data: { code: 0, data: { total: 101, list: config.params.page === 1 ? firstPage : [lastNode] } }
    }))
    request.defaults.adapter = adapter
    try {
      const wrapper = mountInventory(Rules)
      await flushPromises()
      expect(adapter.mock.calls.map(([config]) => [config.url, config.params])).toEqual([
        ['/nodes', { page: 1, pageSize: 100 }], ['/nodes', { page: 2, pageSize: 100 }]
      ])
      expect(select(wrapper, 'rule-node').findAllComponents(ElOption)).toHaveLength(101)
      await choose(select(wrapper, 'rule-node'), lastNode.id)
      await wrapper.get('form[aria-label="端点筛选"]').trigger('submit')
      await flushPromises()
      expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '', node_id: lastNode.id, status: '' })

      await button(wrapper, '新建端点').trigger('click')
      const dialog = editDialog(wrapper)
      await choose(selectField(dialog, 'node_id'), lastNode.id)
      await field(dialog, 'name').get('input').setValue('后续节点入口')
      await field(dialog, 'listen_port').get('input').setValue('8080')
      await button(dialog, '创建端点').trigger('click')
      await flushPromises()
      expect(createRule).toHaveBeenCalledWith(expect.objectContaining({ node_id: lastNode.id, name: '后续节点入口', listen_port: 8080 }))
    } finally {
      request.defaults.adapter = originalAdapter
    }
  })

  it('does not publish partial node options or allow creation when a later node page fails', async () => {
    const actualNodeAPI = await vi.importActual('@/api/node')
    getRoutingNodes.mockImplementation(actualNodeAPI.getRoutingNodes)
    vi.spyOn(ElMessage, 'error').mockImplementation(() => {})
    const firstPage = Array.from({ length: 100 }, (_, index) => ({ id: index + 1, name: '节点 ' + (index + 1) }))
    const lastNode = { id: 101, name: '第二页节点' }
    const secondPage = Promise.withResolvers()
    const originalAdapter = request.defaults.adapter
    let retrying = false
    const adapter = vi.fn(config => {
      if (config.params.page === 2 && !retrying) return secondPage.promise
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, config,
        data: { code: 0, data: { total: 101, list: config.params.page === 1 ? firstPage : [lastNode] } }
      })
    })
    request.defaults.adapter = adapter
    try {
      const wrapper = mountInventory(Rules)
      await flushPromises()
      expect(adapter.mock.calls.map(([config]) => config.params.page)).toEqual([1, 2])
      await command(row(wrapper, 'DNS'), 'copy')
      const dialog = editDialog(wrapper)
      await field(dialog, 'listen_port').get('input').setValue('5354')
      expect(selectField(dialog, 'node_id').props('modelValue')).toBe(entryNode.id)
      expect(selectField(dialog, 'node_id').findAllComponents(ElOption)).toHaveLength(0)
      expect(button(dialog, '创建端点').element.disabled).toBe(true)
      secondPage.reject(new Error('second page unavailable'))
      await flushPromises()
      expect(dialog.get('[role="alert"]').text()).toContain('无法加载节点选项')
      expect(selectField(dialog, 'node_id').findAllComponents(ElOption)).toHaveLength(0)
      expect(select(wrapper, 'rule-node').findAllComponents(ElOption)).toHaveLength(0)
      expect(button(dialog, '创建端点').element.disabled).toBe(true)
      await dialog.get('form').trigger('submit')
      await flushPromises()
      expect(createRule).not.toHaveBeenCalled()

      retrying = true
      await button(dialog, '重新加载节点').trigger('click')
      await flushPromises()
      expect(adapter.mock.calls.map(([config]) => config.params.page)).toEqual([1, 2, 1, 2])
      expect(dialog.find('[role="alert"]').exists()).toBe(false)
      expect(selectField(dialog, 'node_id').findAllComponents(ElOption).map(option => option.props('value'))).toEqual([
        ...firstPage.map(node => node.id), lastNode.id
      ])
      expect(button(dialog, '创建端点').element.disabled).toBe(false)
    } finally {
      request.defaults.adapter = originalAdapter
    }
  })

  it('keeps the previous rules on polling failure and applies filters from page one', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const response = Promise.withResolvers()
    getRuleList.mockReturnValueOnce(response.promise)
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('正在读取端点')
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['—', '—', '—', '—'])
    response.resolve(result(rules, 28))
    await flushPromises()
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['28', '1', '1', '1'])
    expect(wrapper.findAll('thead th').map(item => item.text())).toContain('所在节点')
    expect(row(wrapper, 'HTTPS').text()).not.toContain('primary.test:443')
    expect(row(wrapper, 'Unlinked port').text()).not.toContain('未配置目标')
    expect(row(wrapper, 'HTTPS').get('.rule-traffic-total').text()).toBe('3.07 KB')
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
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.get('[role="alert"]').text()).toContain('下方保留了上次获取的结果。')
    expect(row(wrapper, 'HTTPS').exists()).toBe(true)

    await wrapper.get('#rule-keyword').setValue('  office  ')
    await choose(select(wrapper, 'rule-node'), 4)
    await choose(select(wrapper, 'rule-status'), 'running')
    getRuleList.mockResolvedValueOnce({ data: {} })
    await wrapper.get('form[aria-label="端点筛选"]').trigger('submit')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: 'office', node_id: 4, status: 'running' })
    expect(wrapper.get('.rule-placeholder').text()).toContain('没有匹配的端点')
    getRuleList.mockResolvedValueOnce(result())
    await button(wrapper, '清除筛选').trigger('click')
    await flushPromises()
    expect(getRuleList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '', node_id: '', status: '' })
    expect(wrapper.get('.rule-placeholder').text()).toContain('还没有端点')
  })

  it('pauses automatic polling for background tabs or failures and resumes after a manual retry', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
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
  it('creates a transparent HTTP/HTTPS entry with an automatic listener address', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('Transparent entry')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await choose(selectField(dialog, 'listen_ip'), entryNode.ipv4)
    await choose(selectField(dialog, 'protocol'), 'red')
    expect(selectField(dialog, 'protocol').findAllComponents(ElOption).find(option => option.props('value') === 'red').props('label')).toBe('透明转发（HTTP/HTTPS）')
    expect(dialog.findAll('.el-form-item__label').map(item => item.text())).not.toContain('监听 IP')
    expect(dialog.find('#rule-target-title').exists()).toBe(false)
    await field(dialog, 'listen_port').get('input').setValue('18080')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('127.0.0.1:18080')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'Transparent entry', role: 'entry', protocol: 'red', listen_ip: '127.0.0.1', listen_port: 18080, remark: '',
      use_clash_ruleset: false, targets: [], strategy: 'round'
    })
  })

  it('restores and copies transparent entries, and exposes the IP control when changed to TCP', async () => {
    const transparent = { ...relayRule, role: 'entry', protocol: 'red', listen_ip: '127.0.0.1', targets: [] }
    getRuleList.mockResolvedValue(result([transparent]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(row(wrapper, transparent.name).text()).toContain('透明转发（HTTP/HTTPS）')
    await wrapper.get('[aria-label="编辑 DNS"]').trigger('click')
    let dialog = editDialog(wrapper)
    expect(selectField(dialog, 'protocol').props('modelValue')).toBe('red')
    expect(dialog.findAll('.el-form-item__label').map(item => item.text())).not.toContain('监听 IP')
    await choose(selectField(dialog, 'protocol'), 'tcp')
    await choose(selectField(dialog, 'listen_ip'), '')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledWith(transparent.id, expect.objectContaining({ protocol: 'tcp', listen_ip: '' }))
    await command(row(wrapper, transparent.name), 'copy')
    dialog = editDialog(wrapper)
    expect(selectField(dialog, 'protocol').props('modelValue')).toBe('red')
    expect(dialog.findAll('.el-form-item__label').map(item => item.text())).not.toContain('监听 IP')
    await field(dialog, 'listen_port').get('input').setValue('18081')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith(expect.objectContaining({ name: 'DNS-copy', protocol: 'red', listen_ip: '127.0.0.1', listen_port: 18081 }))
  })

  it('adds multiple subscriptions and domains to one rule table and deletes individual rows', async () => {
    getGostRulesets.mockRejectedValueOnce(new Error('rulesets offline'))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('Matched exit')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await field(dialog, 'listen_port').get('input').setValue('9443')
    await choose(selectField(dialog, 'use_original_target'), true)
    await choose(selectField(dialog, 'match_rules'), true)
    expect(dialog.text()).toContain('无法加载订阅规则集')
    await button(dialog, '重新加载').trigger('click')
    await flushPromises()
    expect(dialog.get('.target-rule-table').text()).toContain('尚未添加匹配规则')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['ruleset_ids', false, '请选择订阅规则集或填写手动域名'])
    expect(createRule).not.toHaveBeenCalled()
    await button(dialog, '添加手动域名').trigger('click')
    await routingRows(dialog)[0].get('input').setValue('example.com')
    await expect(wrapper.getComponent(ElForm).vm.validate()).resolves.toBe(true)
    await button(dialog, '添加手动域名').trigger('click')
    await routingRows(dialog)[1].get('input').setValue('  example.net  ')
    await button(dialog, '添加订阅规则集').trigger('click')
    const firstSubscription = routingRows(dialog)[2].getComponent(ElSelect)
    expect(firstSubscription.findAllComponents(ElOption).map(option => option.props('label'))).toEqual(['旧 Clash 订阅', '工作域名'])
    await choose(firstSubscription, 1)
    await button(dialog, '添加订阅规则集').trigger('click')
    const secondSubscription = routingRows(dialog)[3].getComponent(ElSelect)
    expect(secondSubscription.findAllComponents(ElOption).find(option => option.props('value') === 1).props('disabled')).toBe(true)
    await choose(secondSubscription, 2)
    expect(routingRows(dialog).map(item => item.attributes('data-rule-type'))).toEqual(['domain', 'domain', 'subscription', 'subscription'])
    await button(dialog, '添加手动域名').trigger('click')
    await routingRows(dialog)[4].get('input').setValue('unused.example')
    await routingRows(dialog)[4].get('button').trigger('click')
    await routingRows(dialog)[2].get('button').trigger('click')
    expect(routingRows(dialog)).toHaveLength(3)
    expect(routingRows(dialog)[2].getComponent(ElSelect).props('modelValue')).toBe(2)
    expect(dialog.text()).toContain('example.com 匹配自身和子域')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'Matched exit', role: 'exit', protocol: 'tcp', listen_ip: '', listen_port: 9443, remark: '',
      use_original_target: true, ruleset_ids: [2], manual_domains: ['example.com', 'example.net'], use_clash_ruleset: false, targets: [], strategy: 'round'
    })
  })

  it('explains domain patterns and preserves their wildcard and port syntax when editing an exit', async () => {
    const patterns = ['example.com', '*.example.com:443', '.example.com:400-500', 'api?.example.com:80', 'example.com:443']
    const exit = { ...relayRule, name: 'Pattern exit', role: 'exit', targets: [], use_original_target: true, ruleset_ids: [], manual_domains: patterns }
    getRuleList.mockResolvedValue(result([exit]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 Pattern exit"]').trigger('click')
    const dialog = editDialog(wrapper)
    const inputs = routingRows(dialog).map(item => item.get('input'))
    expect(inputs.map(input => input.element.value)).toEqual(patterns)
    expect(inputs[0].attributes('placeholder')).toBe('例如 *.example.com:443')
    const help = dialog.get(`[id="${inputs[0].attributes('aria-describedby')}"]`).text()
    expect(help).toContain('未写端口默认匹配 443')
    expect(help).toContain('example.com:443 仅匹配该域名和端口')
    expect(help).toContain('支持 *、? 和端口范围')
    expect(help).toContain('点号前缀（如 .example.com:443）包含自身和子域')
    expect(help).toContain('仅作用于透明入口接管的 80/443 流量')
    await inputs[1].setValue('  *.updated.example:443  ')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledExactlyOnceWith(exit.id, expect.objectContaining({
      manual_domains: ['example.com', '*.updated.example:443', '.example.com:400-500', 'api?.example.com:80', 'example.com:443']
    }))
    expect(exit.manual_domains).toEqual(patterns)
  })

  it('restores original-target rules and clears them when switching to all traffic or fixed targets', async () => {
    const exit = { ...relayRule, name: 'Matched exit', role: 'exit', targets: [], use_original_target: true, ruleset_ids: [2], manual_domains: ['saved.example'] }
    getRuleList.mockResolvedValue(result([exit]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 Matched exit"]').trigger('click')
    let dialog = editDialog(wrapper)
    expect(selectField(dialog, 'use_original_target').props('modelValue')).toBe(true)
    expect(selectField(dialog, 'match_rules').props('modelValue')).toBe(true)
    expect(routingRows(dialog)).toHaveLength(2)
    expect(routingRows(dialog)[0].getComponent(ElSelect).props('modelValue')).toBe(2)
    expect(routingRows(dialog)[1].get('input').element.value).toBe('saved.example')
    await button(dialog, '添加手动域名').trigger('click')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['ruleset_ids', false, '请填写每一项规则，或删除空白项'])
    expect(updateRule).not.toHaveBeenCalled()
    await routingRows(dialog)[2].get('button').trigger('click')
    await choose(selectField(dialog, 'match_rules'), false)
    expect(dialog.find('.target-rule-table').exists()).toBe(false)
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenLastCalledWith(exit.id, expect.objectContaining({ use_original_target: true, ruleset_ids: [], manual_domains: [], use_clash_ruleset: false }))
    await wrapper.get('[aria-label="编辑 Matched exit"]').trigger('click')
    dialog = editDialog(wrapper)
    await choose(selectField(dialog, 'use_original_target'), false)
    await dialog.get('input[aria-label="目标地址 1"]').setValue('fixed.example:443')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenLastCalledWith(exit.id, expect.objectContaining({ use_original_target: false, ruleset_ids: [], manual_domains: [], use_clash_ruleset: false, targets: ['fixed.example:443'] }))
  })

  it('removes saved rules individually without changing the other entries and rejects an empty table', async () => {
    const exit = { ...relayRule, name: 'Matched exit', role: 'exit', targets: [], use_original_target: true, ruleset_ids: [1, 2], manual_domains: ['first.example', 'second.example'] }
    getRuleList.mockResolvedValue(result([exit]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 Matched exit"]').trigger('click')
    let dialog = editDialog(wrapper)
    expect(routingRows(dialog)).toHaveLength(4)
    await routingRows(dialog)[1].get('button').trigger('click')
    await routingRows(dialog)[1].get('button').trigger('click')
    expect(routingRows(dialog)[0].getComponent(ElSelect).props('modelValue')).toBe(1)
    expect(routingRows(dialog)[1].get('input').element.value).toBe('second.example')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledExactlyOnceWith(exit.id, expect.objectContaining({ ruleset_ids: [1], manual_domains: ['second.example'] }))
    expect(exit.ruleset_ids).toEqual([1, 2])
    expect(exit.manual_domains).toEqual(['first.example', 'second.example'])

    await wrapper.get('[aria-label="编辑 Matched exit"]').trigger('click')
    dialog = editDialog(wrapper)
    for (let count = 0; count < 4; count++) await routingRows(dialog)[0].get('button').trigger('click')
    expect(dialog.get('.target-rule-table').text()).toContain('尚未添加匹配规则')
    expect(document.activeElement).toBe(button(dialog, '添加订阅规则集').element)
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledTimes(1)
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['ruleset_ids', false, '请选择订阅规则集或填写手动域名'])
  })

  it('maps an old Clash exit to the legacy subscription while preserving its compatibility flag', async () => {
    const exit = { ...relayRule, name: 'Legacy matching exit', role: 'exit', targets: [], use_clash_ruleset: true }
    getRuleList.mockResolvedValue(result([exit]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 Legacy matching exit"]').trigger('click')
    const dialog = editDialog(wrapper)
    expect(selectField(dialog, 'use_original_target').props('modelValue')).toBe(true)
    expect(selectField(dialog, 'match_rules').props('modelValue')).toBe(true)
    expect(routingRows(dialog)).toHaveLength(1)
    expect(routingRows(dialog)[0].getComponent(ElSelect).props('modelValue')).toBe(1)
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    const payload = updateRule.mock.calls[0][1]
    expect(payload.use_clash_ruleset).toBe(true)
    expect(payload).not.toHaveProperty('use_original_target')
    expect(payload).not.toHaveProperty('ruleset_ids')
    expect(payload).not.toHaveProperty('manual_domains')
  })

  it('saves explicit WebSocket and TLS options while leaving unrelated protocol fields hidden', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('WSS exit')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await choose(selectField(dialog, 'protocol'), 'wss')
    await field(dialog, 'listen_port').get('input').setValue('9443')
    await dialog.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await dialog.get('.gost-transport-options [role="button"]').trigger('click')
    await dialog.get('[data-protocol-option="path"] input').setValue('/relay')
    await dialog.get('[data-protocol-option="tls_server_name"] input').setValue('relay.example')
    expect(dialog.find('[data-protocol-option="max_idle_timeout"]').exists()).toBe(false)
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'WSS exit', role: 'exit', protocol: 'wss', listen_ip: '', listen_port: 9443, remark: '',
      use_clash_ruleset: false, targets: ['app.example:443'], strategy: 'round',
      protocol_options: { path: '/relay', tls_server_name: 'relay.example' }
    })
  })

  it('submits an empty options object after clearing every saved advanced field', async () => {
    const exit = { ...relayRule, name: 'WSS exit', role: 'exit', protocol: 'wss', protocol_options: { path: '/saved' } }
    getRuleList.mockResolvedValue(result([exit]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 WSS exit"]').trigger('click')
    const dialog = editDialog(wrapper)
    expect(dialog.get('[data-protocol-option="path"] input').element.value).toBe('/saved')
    await dialog.get('[data-protocol-option="path"] input').setValue('')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledWith(exit.id, expect.objectContaining({ protocol: 'wss', protocol_options: {} }))
    expect(exit.protocol_options).toEqual({ path: '/saved' })
  })

  it('creates an exit with its own Gost transport, targets and strategy', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('Web exit')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await field(dialog, 'listen_port').get('input').setValue('8443')
    expect(dialog.get('#rule-target-title').text()).toBe('目标与策略')
    const protocol = selectField(dialog, 'protocol')
    expect(protocol.findAllComponents(ElOption).map(option => option.props('value'))).toEqual([
      'tcp', 'mtcp', 'udp', 'tls', 'mtls', 'ws', 'mws', 'wss', 'mwss',
      'h2', 'h2c', 'grpc', 'pht', 'phts', 'kcp', 'quic', 'h3', 'ohttp', 'otls'
    ])
    expect(protocol.props('filterable')).toBe(true)
    await choose(protocol, 'h2')
    await choose(selectField(dialog, 'listen_ip'), '127.0.0.1')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['listen_ip', false, '出口须使用可达的本机 IP，或选择所有接口'])
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['targetList', false, '请至少填写一个目标地址'])
    expect(createRule).not.toHaveBeenCalled()
    await choose(selectField(dialog, 'listen_ip'), '::1')
    await expect(wrapper.getComponent(ElForm).vm.validateField('listen_ip')).rejects.toBeDefined()
    await choose(selectField(dialog, 'listen_ip'), '')
    await dialog.get('input[aria-label="目标地址 1"]').setValue(' app.example:443 ')
    await button(dialog, '添加目标地址').trigger('click')
    await dialog.get('input[aria-label="目标地址 2"]').setValue('[2001:db8::10]:443')
    await button(dialog, '添加目标地址').trigger('click')
    await dialog.get('button[aria-label="移除目标地址 3"]').trigger('click')
    await choose(selectField(dialog, 'strategy'), 'hash')
    expect(dialog.get('#rule-target-address-help').text()).toContain('HTTPS 会透传客户端发送的 SNI')
    expect(dialog.get('input[aria-label="目标地址 1"]').attributes('aria-describedby')).toBe('rule-target-address-help')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'Web exit', role: 'exit', protocol: 'h2', listen_ip: '', listen_port: 8443, remark: '',
      use_clash_ruleset: false, targets: ['app.example:443', '[2001:db8::10]:443'], strategy: 'hash'
    })
  })

  it.each(['0.0.0.0', '::'])('accepts %s for an exit listening on all interfaces', async listenIP => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('Wildcard exit')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await choose(selectField(dialog, 'listen_ip'), listenIP)
    await field(dialog, 'listen_port').get('input').setValue('8443')
    await dialog.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'Wildcard exit', role: 'exit', protocol: 'tcp', listen_ip: listenIP, listen_port: 8443, remark: '',
      use_clash_ruleset: false, targets: ['app.example:443'], strategy: 'round'
    })
  })

  it('allows original targets with QUIC and clears exit settings when changing direction to entry', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    let dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('Clash exit')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await field(dialog, 'listen_port').get('input').setValue('9443')
    await choose(selectField(dialog, 'protocol'), 'quic')
    await dialog.get('.gost-transport-options [role="button"]').trigger('click')
    await dialog.get('[data-protocol-option="max_idle_timeout"] input').setValue('45s')
    await choose(selectField(dialog, 'use_original_target'), true)
    expect(selectField(dialog, 'match_rules').props('modelValue')).toBe(false)
    expect(dialog.find('input[aria-label="目标地址 1"]').exists()).toBe(false)
    expect(dialog.text()).toContain('按原始目标需要连接“透明转发（HTTP/HTTPS）”入口')
    expect(dialog.findAll('.el-form-item__label').map(item => item.text())).not.toContain('负载均衡')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledExactlyOnceWith({
      node_id: entryNode.id, name: 'Clash exit', role: 'exit', protocol: 'quic', listen_ip: '', listen_port: 9443, remark: '',
      use_original_target: true, ruleset_ids: [], manual_domains: [],
      use_clash_ruleset: false, targets: [], strategy: 'round', protocol_options: { max_idle_timeout: '45s' }
    })
    await button(wrapper, '新建端点').trigger('click')
    dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('New entry')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    await field(dialog, 'listen_port').get('input').setValue('9444')
    await choose(selectField(dialog, 'role'), 'exit')
    await choose(selectField(dialog, 'protocol'), 'mtls')
    await dialog.get('.gost-transport-options [role="button"]').trigger('click')
    await dialog.get('[data-protocol-option="tls_server_name"] input').setValue('unused.example')
    await dialog.get('input[aria-label="目标地址 1"]').setValue('app.example:443')
    await choose(selectField(dialog, 'strategy'), 'rand')
    await choose(selectField(dialog, 'use_original_target'), true)
    await choose(selectField(dialog, 'role'), 'entry')
    expect(selectField(dialog, 'protocol').props('modelValue')).toBe('tcp')
    expect(dialog.find('#rule-target-title').exists()).toBe(false)
    expect(dialog.find('.gost-transport-options').exists()).toBe(false)
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenLastCalledWith({
      node_id: entryNode.id, name: 'New entry', role: 'entry', protocol: 'tcp', listen_ip: '', listen_port: 9444, remark: '',
      use_clash_ruleset: false, targets: [], strategy: 'round'
    })
  })

  it('edits a stopped tunnel exit policy while locking its role and listener, and copies the exit policy', async () => {
    const exit = { ...relayRule, name: 'Saved exit', role: 'exit', protocol: 'wss', linked_tunnel_id: 8, linked_tunnel_status: 'stopped', targets: ['saved.example:443'], strategy: 'rand',
      protocol_options: { path: '/saved', tls_server_name: 'relay.example' } }
    getRuleList.mockResolvedValue(result([exit, { ...exit, id: 20, name: 'Running exit', status: 'running', linked_tunnel_status: 'running' }, direct]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(row(wrapper, 'Saved exit').text()).toContain('出口')
    expect(row(wrapper, 'HTTPS').text()).toContain('未指定')
    expect(row(wrapper, 'Running exit').find('[aria-label="编辑 Running exit"]').exists()).toBe(false)
    expect(row(wrapper, 'Saved exit').find('[aria-label="启动 Saved exit"]').exists()).toBe(false)
    await wrapper.get('[aria-label="编辑 Saved exit"]').trigger('click')
    let dialog = editDialog(wrapper)
    expect(selectField(dialog, 'node_id').props('disabled')).toBe(true)
    expect(selectField(dialog, 'role').props('disabled')).toBe(true)
    expect(selectField(dialog, 'listen_ip').props('disabled')).toBe(true)
    expect(field(dialog, 'listen_port').get('input').element.disabled).toBe(true)
    expect(selectField(dialog, 'protocol').props('modelValue')).toBe('wss')
    expect(dialog.get('[data-protocol-option="path"] input').element.value).toBe('/saved')
    expect(dialog.get('[data-protocol-option="tls_server_name"] input').element.value).toBe('relay.example')
    expect(dialog.get('input[aria-label="目标地址 1"]').element.value).toBe('saved.example:443')
    await choose(selectField(dialog, 'protocol'), 'kcp')
    await dialog.get('input[aria-label="目标地址 1"]').setValue('updated.example:8443')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenCalledExactlyOnceWith(exit.id, {
      node_id: entryNode.id, name: exit.name, role: 'exit', protocol: 'kcp', listen_ip: exit.listen_ip, listen_port: exit.listen_port, remark: '',
      use_clash_ruleset: false, targets: ['updated.example:8443'], strategy: 'rand', protocol_options: {}
    })
    await command(row(wrapper, 'Saved exit'), 'copy')
    dialog = editDialog(wrapper)
    expect(selectField(dialog, 'role').props('modelValue')).toBe('exit')
    expect(selectField(dialog, 'role').props('disabled')).toBe(false)
    expect(selectField(dialog, 'protocol').props('modelValue')).toBe('wss')
    expect(dialog.get('[data-protocol-option="path"] input').element.value).toBe('/saved')
    expect(dialog.get('[data-protocol-option="tls_server_name"] input').element.value).toBe('relay.example')
    expect(dialog.get('input[aria-label="目标地址 1"]').element.value).toBe('saved.example:443')
    expect(field(dialog, 'listen_port').get('input').element.value).toBe('')
  })

  it('binds a mapped local IP and displays its public endpoint with the same port', async () => {
    const mapping = { listen_ip: '10.10.0.8', connect_ip: '2001:db8::80' }
    const mappedNode = { ...entryNode, address_mappings: [mapping] }
    const mappedRule = { ...direct, node: mappedNode, listen_ip: mapping.listen_ip }
    getRoutingNodes.mockResolvedValueOnce([mappedNode])
    getRuleList.mockResolvedValueOnce(result([mappedRule, relayRule]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(row(wrapper, mappedRule.name).text()).toContain('对外连接 [2001:db8::80]:443')
    expect(row(wrapper, relayRule.name).text()).not.toContain('对外连接')

    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await choose(selectField(dialog, 'node_id'), mappedNode.id)
    const listenIP = selectField(dialog, 'listen_ip')
    const options = listenIP.findAllComponents(ElOption)
    expect(options.find(option => option.props('value') === mapping.listen_ip).props('label')).toBe('本机 10.10.0.8 · 对外 2001:db8::80')
    expect(options.map(option => option.props('value'))).not.toContain(mapping.connect_ip)
    await choose(listenIP, mapping.listen_ip)
    await field(dialog, 'name').get('input').setValue('Mapped ingress')
    await field(dialog, 'listen_port').get('input').setValue('9443')
    const preview = dialog.get('#rule-listen-endpoint')
    expect(preview.text()).toContain('监听地址 10.10.0.8:9443')
    expect(preview.text()).toContain('对外连接 [2001:db8::80]:9443')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith(expect.objectContaining({ listen_ip: mapping.listen_ip, listen_port: 9443 }))
  })

  it('creates an entry without target settings, then checks that listener', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    expect(dialog.findAll('.el-form-item__label').map(item => item.text())).toEqual([
      '端点名称', '端点方向', '所在节点', '入口类型', '监听 IP', '监听端口', '备注'
    ])
    expect(dialog.text()).toContain('入口接收流量，出口配置目标与策略')
    expect(selectField(dialog, 'role').props('modelValue')).toBe('entry')
    expect(selectField(dialog, 'protocol').findAllComponents(ElOption).map(option => option.props('value'))).toEqual(['tcp', 'udp', 'red'])
    expect(dialog.find('#rule-target-title').exists()).toBe(false)
    await field(dialog, 'name').get('input').setValue('  internal API  ')
    expect(field(dialog, 'listen_port').get('input').element.value).toBe('')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['node_id', false, '请选择所在节点'])
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['listen_port', false, '请输入监听端口'])
    expect(document.activeElement).toBe(selectField(dialog, 'node_id').get('input').element)
    expect(createRule).not.toHaveBeenCalled()
    expect(checkRuleListener).not.toHaveBeenCalled()
    await choose(selectField(dialog, 'node_id'), 4)
    await choose(selectField(dialog, 'protocol'), 'udp')
    const listenIP = selectField(dialog, 'listen_ip')
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('label'))).toEqual([
      '所有接口', 'IPv4 回环 · 127.0.0.1', 'IPv6 回环 · ::1',
      '节点探测地址 · 192.0.2.10', '节点探测地址 · 2001:db8::8'
    ])
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('value'))).not.toContain(entryNode.manual_addresses[0])
    await choose(listenIP, entryNode.ipv4)
    await field(dialog, 'listen_port').get('input').setValue('5353')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('192.0.2.10:5353')
    await field(dialog, 'remark').get('textarea').setValue('可用于隧道任一端')
    const created = { ...relayRule, id: 99, name: 'internal API', listen_ip: entryNode.ipv4, listen_port: 5353 }
    createRule.mockResolvedValueOnce({ data: created })
    getRuleList.mockResolvedValueOnce(result([created]))
    checkRuleListener.mockResolvedValueOnce({ data: { available: false, reason: 'occupied' } })
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()

    expect(createRule).toHaveBeenCalledWith({
      node_id: 4, name: 'internal API', protocol: 'udp',
      listen_ip: '192.0.2.10', listen_port: 5353, remark: '可用于隧道任一端',
      role: 'entry', targets: [], use_clash_ruleset: false, strategy: 'round'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('端点已创建')
    expect(checkRuleListener).toHaveBeenCalledWith(99)
    expect(vi.mocked(createRule).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(checkRuleListener).mock.invocationCallOrder[0])
    expect(row(wrapper, 'internal API').text()).toContain('监听地址已被占用')
    await command(row(wrapper, 'internal API'), 'listener-check')
    expect(row(wrapper, 'internal API').text()).toContain('监听地址可用')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('creates a loopback entry without assigning a Clash target', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await button(wrapper, '新建端点').trigger('click')
    const dialog = editDialog(wrapper)
    await field(dialog, 'name').get('input').setValue('透明入口')
    await choose(selectField(dialog, 'node_id'), entryNode.id)
    expect(dialog.find('#rule-target-mode-help').exists()).toBe(false)
    expect(dialog.find('[aria-label="目标地址 1"]').exists()).toBe(false)
    const listenIP = selectField(dialog, 'listen_ip')
    expect(listenIP.findAllComponents(ElOption).map(option => option.props('value'))).toContain(entryNode.ipv6)
    const custom = listenIP.get('input')
    await custom.setValue(' 10.0.0.4 ')
    await custom.trigger('keydown', { key: 'Enter', code: 'Enter' })
    await field(dialog, 'listen_port').get('input').setValue('443')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('10.0.0.4:443')
    await choose(listenIP, '127.0.0.1')
    expect(dialog.get('#rule-listen-endpoint').text()).toContain('127.0.0.1:443')
    await choose(selectField(dialog, 'protocol'), 'udp')
    await button(dialog, '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith({
      node_id: entryNode.id, name: '透明入口', protocol: 'udp',
      listen_ip: '127.0.0.1', listen_port: 443, remark: '',
      role: 'entry', targets: [], use_clash_ruleset: false, strategy: 'round'
    })
  })

  it('shows the tunnel link for a running Clash entry', async () => {
    getRuleList.mockResolvedValueOnce(result([{
      ...relayRule, protocol: 'tcp', listen_ip: '127.0.0.1', listen_port: 443,
      use_clash_ruleset: true, targets: [], status: 'running', linked_tunnel_id: 8, tunnel_role: 'entry'
    }]))
    const wrapper = mountInventory(Rules)
    await flushPromises()
    expect(row(wrapper, 'DNS').text()).not.toContain('Clash 规则集')
    expect(row(wrapper, 'DNS').text()).toContain('隧道源端')
    expect(row(wrapper, 'DNS').find('[aria-label="停止 DNS"]').exists()).toBe(false)
    expect(row(wrapper, 'DNS').text()).toContain('隧道管理')
  })

  it('keeps edits on a failed save, leaves existing targets unchanged, and copies only endpoint settings', async () => {
    const wrapper = mountInventory(Rules)
    await flushPromises()
    await wrapper.get('[aria-label="编辑 HTTPS"]').trigger('click')
    const dialog = editDialog(wrapper)
    expect(selectField(dialog, 'node_id').props('disabled')).toBe(true)
    await field(dialog, 'name').get('input').setValue('HTTPS updated')
    updateRule.mockRejectedValueOnce(new Error('port conflict'))
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(editDialog(wrapper).exists()).toBe(true)
    expect(field(dialog, 'name').get('input').element.value).toBe('HTTPS updated')
    await button(dialog, '保存端点').trigger('click')
    await flushPromises()
    expect(updateRule).toHaveBeenLastCalledWith(1, {
      node_id: 4, name: 'HTTPS updated', protocol: 'tcp',
      listen_ip: '192.0.2.10', listen_port: 443, remark: 'Office'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('端点已保存')

    await command(row(wrapper, 'DNS'), 'copy')
    expect(field(editDialog(wrapper), 'name').get('input').element.value).toBe('DNS-copy')
    expect(field(editDialog(wrapper), 'listen_port').get('input').element.value).toBe('')
    await field(editDialog(wrapper), 'listen_port').get('input').setValue('5354')
    await button(editDialog(wrapper), '创建端点').trigger('click')
    await flushPromises()
    expect(createRule).toHaveBeenCalledWith({
      node_id: 4, name: 'DNS-copy', protocol: 'udp',
      listen_ip: '2001:db8::8', listen_port: 5354, remark: '',
      role: 'entry', targets: [], use_clash_ruleset: false, strategy: 'round'
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
    expect(ElMessage.success).toHaveBeenCalledWith('端点已启动')

    await wrapper.get('[aria-label="停止 HTTPS"]').trigger('click')
    await flushPromises()
    expect(stopRule).toHaveBeenCalledWith(1)
    expect(ElMessage.success).toHaveBeenCalledWith('端点已停止')

    ElMessageBox.confirm.mockRejectedValueOnce('cancel')
    await command(row(wrapper, 'DNS'), 'delete')
    expect(deleteRule).not.toHaveBeenCalled()
    await command(row(wrapper, 'DNS'), 'delete')
    expect(deleteRule).toHaveBeenCalledWith(2)
    expect(ElMessage.success).toHaveBeenCalledWith('端点已删除')
  })
})
