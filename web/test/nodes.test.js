import { flushPromises } from '@vue/test-utils'
import { ElSelect } from 'element-plus'
import { nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Nodes from '@/views/Nodes.vue'
import * as api from '@/api/node'
import { getPublicSystemConfig } from '@/api/system'
import { button, click, deferred, dialog, mockNodeMessages, mountNodeComponent, response } from './node-test-support.js'

vi.mock('@/api/node', () => ({
  getNode: vi.fn(), getNodeList: vi.fn(), createNode: vi.fn(), updateNode: vi.fn(), deleteNode: vi.fn(),
  calibrateNodeTraffic: vi.fn(),
  issueNodeRegistrationToken: vi.fn(), getNodeConfig: vi.fn(),
  getGostService: vi.fn(), getGostLogs: vi.fn(), restartGostService: vi.fn(),
  getNodeNftConfig: vi.fn(),
  getNodeUpgradeStatus: vi.fn(), updateNodeUpgradePolicy: vi.fn(), upgradeNodeComponent: vi.fn()
}))
vi.mock('@/api/system', () => ({ getPublicSystemConfig: vi.fn() }))

const GIB = 1024 ** 3
const primary = {
  id: 7, name: '边缘节点', remark: '入口', status: 'online', username: 'node-7', region: 'HK',
  address: '2001:db8::7', ipv4: '192.0.2.7', ipv6: '2001:db8::7', manual_addresses: ['198.51.100.70', '2001:db8:1::7'], last_check_at: '2026-11-08T02:00:00Z',
  total_bytes: 1536, input_bytes: 512, output_bytes: 1024,
  traffic_limit_bytes: GIB + 1, traffic_reset_period: 'daily', traffic_quota_state: 'blocked', traffic_quota_used_bytes: GIB
}
const offlinePrimary = { ...primary, status: 'offline' }
const newNode = { id: 9, name: '新节点', status: 'offline' }
const list = (nodes, total = nodes.length) => response({ list: nodes, total })
const token = (value = 'secret', lifetime = 60_000) => response({ token: value, expires_at: new Date(Date.now() + lifetime).toISOString() })
const editForm = (view, title) => dialog(view, title).get('form.el-form')
const settingsTitle = '节点配置 · 边缘节点'
const quotaInput = '[placeholder="例如 100.5"]'
let wrapper
let messages
let router

beforeEach(async () => {
  vi.resetAllMocks()
  messages = mockNodeMessages()
  api.getNodeList.mockResolvedValue(list([primary]))
  api.getNode.mockResolvedValue(response(primary))
  api.getGostService.mockResolvedValue(response({ unit: 'gost.service', active_state: 'active', load_state: 'loaded' }))
  getPublicSystemConfig.mockResolvedValue(response({ host: 'panel.example.com' }))
  api.issueNodeRegistrationToken.mockImplementation(async () => token())
  router = createRouter({ history: createMemoryHistory(), routes: [{ name: 'Nodes', path: '/nodes', component: Nodes }] })
  await router.push('/nodes')
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
  delete window.navigator.clipboard
  delete document.execCommand
  vi.useRealTimers()
})

function open() {
  wrapper = mountNodeComponent(Nodes, {}, [router])
  return wrapper
}

async function openDetail(view) {
  await view.get('tbody tr').trigger('click')
  await flushPromises()
}

describe('node inventory', () => {
  it('opens a node from any row cell and returns to the filtered list', async () => {
    const view = open()
    await flushPromises()
    await view.get('#node-keyword').setValue(primary.name)
    await view.get('form[aria-label="筛选节点"]').trigger('submit')
    await flushPromises()
    expect(view.text()).not.toContain('更多')
    expect(view.get('#node-link-7').attributes('href')).toBe('/nodes?node=7')
    await view.get('tbody tr td:nth-child(2)').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.node).toBe('7')
    expect(api.getNode).toHaveBeenCalledWith(primary.id)
    expect(view.get('h1').text()).toBe(primary.name)
    expect(view.text()).toContain('中国香港')
    expect(view.get('progress').element.max).toBe(primary.traffic_limit_bytes)
    expect(view.get('progress').element.value).toBe(primary.traffic_quota_used_bytes)
    expect(view.find('tbody').exists()).toBe(false)
    await view.get('.node-detail-back').trigger('click')
    await flushPromises()
    expect(view.get('#node-keyword').element.value).toBe(primary.name)
    expect(document.activeElement).toBe(view.get('#node-link-7').element)
  })

  it('loads a direct detail link and can retry a failed read', async () => {
    api.getNode.mockRejectedValueOnce(new Error('network'))
    await router.push('/nodes?node=7')
    const view = open()
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toContain('无法读取节点详情')
    expect(api.getNodeList).not.toHaveBeenCalled()
    await click(view, '重新加载')
    expect(view.get('h1').text()).toBe(primary.name)
  })

  it('loads, filters, clears an empty search and recovers from a request error', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const pending = deferred()
    api.getNodeList.mockReturnValueOnce(pending.promise)
    const view = open()
    expect(view.text()).toContain('正在加载节点')
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getNodeList).toHaveBeenCalledTimes(1)
    pending.resolve(response({}))
    await flushPromises()
    expect(view.text()).toContain('尚未添加节点')
    api.getNodeList.mockResolvedValueOnce(list([]))
    await view.get('#node-keyword').setValue('缺失的节点')
    await view.get('form[aria-label="筛选节点"]').trigger('submit')
    await flushPromises()
    expect(api.getNodeList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '缺失的节点', status: '' })
    expect(view.text()).toContain('没有匹配的节点')
    api.getNodeList.mockRejectedValueOnce(new Error('offline'))
    await click(view, '清除筛选')
    expect(api.getNodeList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '', status: '' })
    expect(view.get('[role="alert"]').text()).toContain('无法加载节点清单')
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getNodeList).toHaveBeenCalledTimes(3)
    await click(view, '重新加载')
    expect(view.text()).toContain(primary.name)
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getNodeList).toHaveBeenCalledTimes(5)
    view.unmount()
    wrapper = undefined
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.getNodeList).toHaveBeenCalledTimes(5)
  })

  it('shows only the primary IP with quota states and sends the selected status and page', async () => {
    api.getNodeList.mockResolvedValue(list([
      { ...primary, addresses: ['192.0.2.7', '192.0.2.17', '2001:db8::7', '2001:db8::17'] },
      { id: 8, name: '待注册', status: 'offline', traffic_quota_state: 'pending' },
      { id: 9, name: '未限流', status: 'error', address: '192.0.2.9' },
      { id: 10, name: '旧守护程序', status: 'unknown', address: '2001:db8::10', traffic_limit_bytes: GIB, traffic_quota_state: 'unsupported' },
      { id: 11, name: '待同步', traffic_limit_bytes: GIB },
      { id: 12, name: '生效中', traffic_limit_bytes: GIB, traffic_quota_state: 'active' }
    ], 22))
    const view = open()
    await flushPromises()
    const rows = view.findAll('tbody tr').map(row => row.text())
    expect(rows[0]).toContain('2001:db8::7')
    expect(rows[0]).not.toContain('192.0.2.7')
    expect(rows[0]).not.toContain('192.0.2.17')
    expect(rows[0]).not.toContain('2001:db8::17')
    expect(rows[0]).not.toContain(primary.manual_addresses[0])
    expect(rows[0]).not.toContain(primary.manual_addresses[1])
    expect(view.get('tbody tr td:nth-child(2)').text()).toBe('节点 IP2001:db8::7')
    expect(rows[0]).toContain('1.5 KiB')
    expect(rows[0]).toContain('已达上限')
    expect(rows[0]).toContain('本期用量')
    expect(rows[1]).toContain('待获取地址')
    expect(rows[1]).toContain('解除限制待同步')
    expect(rows[2]).toContain('192.0.2.9')
    expect(rows[2]).not.toContain('IPv6')
    expect(rows[2]).not.toContain('检查')
    expect(rows[2]).toContain('未设上限')
    expect(rows[3]).toContain('2001:db8::10')
    expect(rows[3]).toContain('需要支持流量配额的 yagp-daemon')
    expect(rows[4]).toContain('待同步')
    expect(rows[5]).toContain('额度用量')

    const [status, pageSize] = view.findAllComponents(ElSelect)
    status.vm.$emit('update:modelValue', 'online')
    status.vm.$emit('change', 'online')
    await flushPromises()
    expect(api.getNodeList).toHaveBeenLastCalledWith({ page: 1, pageSize: 10, keyword: '', status: 'online' })
    await view.get('button.btn-next').trigger('click')
    await flushPromises()
    expect(api.getNodeList).toHaveBeenLastCalledWith({ page: 2, pageSize: 10, keyword: '', status: 'online' })
    pageSize.vm.$emit('update:modelValue', 20)
    pageSize.vm.$emit('change', 20)
    await flushPromises()
    expect(api.getNodeList).toHaveBeenLastCalledWith({ page: 1, pageSize: 20, keyword: '', status: 'online' })
  })

  it('opens the node configuration and renders services, chains and observers after a failed read', async () => {
    const pending = deferred()
    api.getNodeConfig.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(response({
      services: [{ name: 'forward', addr: ':80', handler: { type: 'tcp', chain: 'primary' }, forwarder: { nodes: [{}] } }, {}],
      chains: [{ name: 'primary', hops: [{}] }, { name: 'empty' }],
      observers: [{ name: 'journal', plugin: { type: 'grpc', addr: 'localhost:80' } }]
    }))
    const view = open()
    await flushPromises()
    await click(view, '配置')
    expect(router.currentRoute.value.query.node).toBeUndefined()
    const config = dialog(view, '节点配置 · 边缘节点')
    await config.get('#tab-gost').trigger('click')
    expect(config.text()).toContain('正在读取节点配置')
    pending.reject(new Error('node offline'))
    await flushPromises()
    expect(config.get('[role="alert"]').text()).toContain('无法读取该节点的配置')
    await click(config, '重新加载')
    expect(api.getNodeConfig).toHaveBeenLastCalledWith(primary.id)
    expect(config.text()).toContain(':80 · tcp · primary · 1 个目标')
    expect(config.text()).toContain('未命名')
    expect(config.text()).toContain('1 跳')
    expect(config.text()).toContain('0 跳')
    expect(config.text()).toContain('grpc · localhost:80')
    await config.get('#tab-chains').trigger('click')
    expect(config.get('#tab-chains').attributes('aria-selected')).toBe('true')
    await click(config, '关闭')
    expect(view.find('[aria-label="节点配置 · 边缘节点"]').exists()).toBe(false)
    api.getNodeConfig.mockResolvedValueOnce(response({}))
    await click(view, '配置')
    const emptyConfig = dialog(view, '节点配置 · 边缘节点')
    await emptyConfig.get('#tab-gost').trigger('click')
    await flushPromises()
    expect(emptyConfig.text()).toContain('暂无服务')
    await emptyConfig.get('[aria-label="关闭弹窗"]').trigger('click')
    expect(view.find('[aria-label="节点配置 · 边缘节点"]').exists()).toBe(false)
  })
})

describe('node registration and quota settings', () => {
  it('creates a node with supplementary IPs entered on separate lines', async () => {
    api.createNode.mockResolvedValue(response(newNode))
    const view = open()
    await flushPromises()
    await click(view, '添加节点')
    const form = editForm(view, '添加节点')
    await form.get('[placeholder="例如 华东入口"]').setValue(newNode.name)
    const field = form.get('#node-manual-addresses')
    expect(field.attributes('aria-describedby')).toBe('node-manual-addresses-help')
    expect(form.get('#node-manual-addresses-help').text()).toContain('不替代自动获取的主要 IP')
    await field.setValue(' 198.51.100.90 \n\n 2001:db8:1::9 \n')
    await click(dialog(view, '添加节点'), '添加节点')
    expect(api.createNode).toHaveBeenCalledWith({
      name: newNode.name, remark: '', traffic_limit_bytes: 0, traffic_reset_period: 'none',
      manual_addresses: ['198.51.100.90', '2001:db8:1::9']
    })
    expect(api.issueNodeRegistrationToken).toHaveBeenCalledWith(newNode.id)
  })

  it('retains invalid supplementary IP input after server validation, then saves and clears it', async () => {
    const view = open()
    await flushPromises()
    await click(view, '配置')
    const form = editForm(view, settingsTitle)
    expect(form.get('#node-manual-addresses').element.value).toBe(primary.manual_addresses.join('\n'))
    const logger = vi.spyOn(console, 'error').mockImplementation(() => {})
    const message = '补充 IP 必须是有效的 IPv4 或 IPv6 地址'
    api.updateNode.mockRejectedValueOnce({ response: { data: { message } } })
    await form.get('#node-manual-addresses').setValue('198.51.100.70:3333')
    await click(dialog(view, settingsTitle), '保存更改')
    expect(form.get('[role="alert"]').text()).toBe(message)
    expect(form.get('#node-manual-addresses').element.value).toBe('198.51.100.70:3333')
    expect(logger).toHaveBeenCalledOnce()

    const updated = { ...primary, manual_addresses: ['198.51.100.71', '2001:db8:1::8'] }
    api.updateNode.mockResolvedValue(response(updated))
    api.getNodeList.mockResolvedValue(list([updated]))
    await form.get('#node-manual-addresses').setValue(' 198.51.100.71 \n\n2001:db8:1::8\n')
    await click(dialog(view, settingsTitle), '保存更改')
    expect(api.updateNode).toHaveBeenLastCalledWith(primary.id, {
      name: primary.name, remark: primary.remark, manual_addresses: updated.manual_addresses
    })
    await click(view, '配置')
    const updatedForm = editForm(view, settingsTitle)
    expect(updatedForm.get('#node-manual-addresses').element.value).toBe(updated.manual_addresses.join('\n'))
    expect(updatedForm.find('[role="alert"]').exists()).toBe(false)
    await updatedForm.get('#node-manual-addresses').setValue(' \n ')
    await click(dialog(view, settingsTitle), '保存更改')
    expect(api.updateNode).toHaveBeenLastCalledWith(primary.id, {
      name: primary.name, remark: primary.remark, manual_addresses: []
    })
  })

  it('hides installation in both views and closes an open dialog once the node is online', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    api.getNodeList.mockResolvedValueOnce(list([offlinePrimary]))
    const view = open()
    await flushPromises()
    await click(view, '安装')
    const title = '安装节点 · 边缘节点'
    expect(dialog(view, title).get('code').text()).toBe('secret')

    await vi.advanceTimersByTimeAsync(5000)
    expect(view.find(`[aria-label="${title}"]`).exists()).toBe(false)
    expect(view.get('tbody tr .inventory-actions').text()).toBe('配置')
    expect(messages.warning).toHaveBeenCalledExactlyOnceWith('节点已在线，无法重新安装')
    await openDetail(view)
    expect(view.get('.detail-header .detail-buttons').text()).toBe('配置')
  })

  it('closes installation and refreshes the node status when the token API reports it is online', async () => {
    api.getNodeList.mockResolvedValueOnce(list([offlinePrimary]))
    const message = '节点已在线，无法重新安装'
    api.issueNodeRegistrationToken.mockRejectedValueOnce({ response: { status: 409, data: { code: 10008, message } } })
    const view = open()
    await flushPromises()
    await click(view, '安装')

    expect(api.issueNodeRegistrationToken).toHaveBeenCalledExactlyOnceWith(primary.id)
    expect(api.getNodeList).toHaveBeenCalledTimes(2)
    expect(messages.warning).toHaveBeenCalledExactlyOnceWith(message)
    expect(view.find('[aria-label="安装节点 · 边缘节点"]').exists()).toBe(false)
    expect(view.get('tbody tr .inventory-actions').text()).toBe('配置')
  })

  it('validates quota precision and opens installation after creating the node', async () => {
    api.createNode.mockResolvedValue(response(newNode))
    const view = open()
    await flushPromises()
    await click(view, '添加节点')
    const form = editForm(view, '添加节点')
    const name = form.get('[placeholder="例如 华东入口"]')
    await name.setValue(newNode.name)
    for (const invalid of ['1e2', '.00000000001', '999999999999']) {
      await form.get(quotaInput).setValue(invalid)
      await click(dialog(view, '添加节点'), '添加节点')
      expect(api.createNode).not.toHaveBeenCalled()
      await vi.waitFor(() => expect(form.text()).toContain('请输入 0 或至少 1 字节'))
    }
    await form.get(quotaInput).setValue(' .5 ')
    form.findComponent(ElSelect).vm.$emit('update:modelValue', 'weekly')
    await nextTick()
    await click(dialog(view, '添加节点'), '添加节点')
    expect(api.createNode).toHaveBeenCalledWith({
      name: newNode.name, remark: '', traffic_limit_bytes: GIB / 2, traffic_reset_period: 'weekly'
    })
    expect(api.issueNodeRegistrationToken).toHaveBeenCalledExactlyOnceWith(newNode.id)
    const install = dialog(view, '安装节点 · 新节点')
    expect(install.get('code').text()).toBe('secret')
    expect(install.get('textarea').element.value).toBe(
      "curl -fsSL 'https://raw.githubusercontent.com/hanzec/yagp/HEAD/scripts/install_node.sh' | bash -s -- " +
      "--host 'panel.example.com' --token 'secret'"
    )
    expect(messages.success).toHaveBeenCalledWith('节点已添加')
  })

  it('omits unchanged quota values during updates and sends explicit changes', async () => {
    api.updateNode.mockResolvedValue(response(primary))
    const view = open()
    await flushPromises()
    await openDetail(view)
    expect(view.text()).not.toContain('复制节点')
    await click(view, '配置')
    expect(editForm(view, settingsTitle).get(quotaInput).element.value).toBe('1.0000000009')
    await click(dialog(view, settingsTitle), '保存更改')
    expect(api.updateNode).toHaveBeenLastCalledWith(primary.id, { name: primary.name, remark: primary.remark })
    expect(messages.success).toHaveBeenCalledWith('节点已更新')

    await click(view, '配置')
    const form = editForm(view, settingsTitle)
    form.findComponent(ElSelect).vm.$emit('update:modelValue', 'monthly')
    await form.get(quotaInput).setValue('')
    await click(dialog(view, settingsTitle), '保存更改')
    expect(api.updateNode).toHaveBeenLastCalledWith(primary.id, {
      name: primary.name, remark: primary.remark, traffic_limit_bytes: 0, traffic_reset_period: 'monthly'
    })
  })

  it('uses the configured address to generate, retry, copy and expire a registration command', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    api.getNodeList.mockResolvedValue(list([offlinePrimary]))
    const writeText = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('clipboard denied'))
    Object.defineProperty(window.navigator, 'clipboard', { configurable: true, value: { writeText } })
    const execCommand = vi.fn().mockReturnValueOnce(true).mockReturnValueOnce(false)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })
    getPublicSystemConfig.mockResolvedValueOnce(response({ host: '' }))
      .mockResolvedValue(response({ host: '2001:db8::1', daemonVersion: '0.1.2-abcdef1' }))
    api.issueNodeRegistrationToken.mockRejectedValueOnce(new Error('retry'))
      .mockImplementationOnce(async () => token('expired', -1000))
      .mockImplementationOnce(async () => token("one'two", 1000))
    const view = open()
    await flushPromises()
    await click(view, '安装')
    const install = dialog(view, '安装节点 · 边缘节点')
    expect(install.find('input').exists()).toBe(false)
    expect(install.get('[role="alert"]').text()).toContain('host 和 report.listen')
    expect(api.issueNodeRegistrationToken).not.toHaveBeenCalled()
    await click(install, '重试生成')
    expect(install.get('[role="alert"]').text()).toContain('签发注册令牌失败')
    await click(install, '重试生成')
    expect(install.text()).toContain('令牌已过期')
    expect(install.find('textarea').exists()).toBe(false)
    await click(install, '重试生成')
    expect(install.get('code').text()).toBe("one'two")
    expect(install.text()).toContain('重新注册会使旧实例失去访问权限')
    const field = install.get('textarea').element
    const command = field.value
    expect(field.readOnly).toBe(true)
    expect(command).toContain("/refs/tags/0.1.2-abcdef1/scripts/install_node.sh' | YAGP_DAEMON_VERSION='0.1.2-abcdef1' bash")
    expect(command).toContain("--host '2001:db8::1'")
    expect(command).toContain("--token 'one'\"'\"'two'")
    await click(install, '复制安装命令')
    expect(writeText).toHaveBeenCalledWith(command)
    expect(messages.success).toHaveBeenCalledExactlyOnceWith('安装命令已复制')
    await click(install, '复制安装命令')
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(messages.success).toHaveBeenNthCalledWith(2, '安装命令已复制')
    expect(install.text()).not.toContain('安装命令已复制')
    delete window.navigator.clipboard
    await click(install, '复制安装命令')
    expect(field.selectionStart).toBe(0)
    expect(field.selectionEnd).toBe(command.length)
    expect(messages.warning).toHaveBeenCalledExactlyOnceWith('已选中安装命令，请使用系统复制功能。')
    await vi.advanceTimersByTimeAsync(1000)
    expect(install.text()).toContain('令牌已过期')
    expect(install.find('textarea').exists()).toBe(false)
    await click(install, '关闭')
    await click(view, '安装')
    expect(dialog(view, '安装节点 · 边缘节点').text()).not.toContain('令牌已过期')
  })

  it('discards a closed dialog address and shares a pending token on reopen', async () => {
    api.getNodeList.mockResolvedValue(list([offlinePrimary]))
    const config = deferred()
    const issue = deferred()
    getPublicSystemConfig.mockReturnValueOnce(config.promise)
    api.issueNodeRegistrationToken.mockReturnValueOnce(issue.promise)
    const view = open()
    await flushPromises()
    await click(view, '安装')
    const title = '安装节点 · 边缘节点'
    expect(dialog(view, title).text()).toContain('正在生成注册令牌')
    await click(dialog(view, title), '关闭')
    config.resolve(response({ host: 'stale.example.com' }))
    await flushPromises()
    expect(view.find(`[aria-label="${title}"]`).exists()).toBe(false)
    expect(api.issueNodeRegistrationToken).not.toHaveBeenCalled()
    await click(view, '安装')
    expect(button(dialog(view, title), '复制安装命令').element.disabled).toBe(true)
    await click(dialog(view, title), '关闭')
    await click(view, '安装')
    expect(api.issueNodeRegistrationToken).toHaveBeenCalledExactlyOnceWith(primary.id)
    issue.resolve(token('shared-token'))
    await flushPromises()
    expect(dialog(view, title).get('code').text()).toBe('shared-token')
    expect(dialog(view, title).get('textarea').element.value).toContain("--host 'panel.example.com'")
  })

  it('retains a failed edit, ignores a duplicate submission and lets the user retry', async () => {
    const failure = new Error('node name already in use')
    const logger = vi.spyOn(console, 'error').mockImplementation(() => {})
    const pending = deferred()
    api.updateNode.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(response(primary))
    const view = open()
    await flushPromises()
    await openDetail(view)
    await click(view, '配置')
    const edit = dialog(view, settingsTitle)
    const form = editForm(view, settingsTitle)
    await form.get('[placeholder="例如 华东入口"]').setValue('更名节点')
    await click(edit, '保存更改')
    await form.trigger('submit')
    expect(api.updateNode).toHaveBeenCalledTimes(1)
    pending.reject(failure)
    await flushPromises()
    expect(logger).toHaveBeenCalledWith('保存节点失败:', failure)
    expect(form.get('[placeholder="例如 华东入口"]').element.value).toBe('更名节点')
    await click(edit, '保存更改')
    expect(api.updateNode).toHaveBeenLastCalledWith(primary.id, { name: '更名节点', remark: primary.remark })
    expect(view.find(`[aria-label="${settingsTitle}"]`).exists()).toBe(false)
    await view.get('.node-detail-back').trigger('click')
    await flushPromises()
    await click(view, '添加节点')
    await click(dialog(view, '添加节点'), '取消')
    expect(view.find('[aria-label="添加节点"]').exists()).toBe(false)
  })
})

describe('node actions', () => {
  it('calibrates cumulative counters and the current quota independently', async () => {
    api.calibrateNodeTraffic.mockResolvedValue(response({}))
    const view = open()
    await flushPromises()
    await openDetail(view)
    await click(view, '配置')
    const settings = dialog(view, settingsTitle)
    await settings.get('#tab-traffic').trigger('click')
    await settings.get('#calibrate-input').setValue('-1')
    expect(button(settings, '保存累计流量').element.disabled).toBe(true)
    await settings.get('#calibrate-input').setValue('60')
    await settings.get('#calibrate-output').setValue('80')
    await settings.get('form[aria-labelledby="node-calibrate-totals"]').trigger('submit')
    await flushPromises()
    expect(api.calibrateNodeTraffic).toHaveBeenLastCalledWith(primary.id, { input_bytes: 60, output_bytes: 80 })
    await settings.get('#calibrate-quota').setValue('0')
    await settings.get('form[aria-labelledby="node-calibrate-quota"]').trigger('submit')
    await flushPromises()
    expect(api.calibrateNodeTraffic).toHaveBeenLastCalledWith(primary.id, { quota_used_bytes: 0 })
    expect(messages.success).toHaveBeenCalledTimes(2)
  })

  it('links directly to the inline Gost section and fetches logs when expanded', async () => {
    api.getGostLogs.mockResolvedValue(response({ entries: [{ message: '启动完成', priority: 6 }] }))
    const view = open()
    await flushPromises()
    expect(api.getGostService).not.toHaveBeenCalled()
    await view.get('.inventory-service-link').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.hash).toBe('#node-gost-service')
    expect(document.activeElement).toBe(view.get('#node-gost-title').element)
    const service = view.get('#node-gost-service')
    const overview = view.get('.detail-overview')
    expect(overview.get('h2').text()).toBe('IP 信息')
    expect(overview.findAll('h2')).toHaveLength(1)
    expect(service.get('h2').text()).toBe('Gost 服务')
    expect(overview.get('.detail-network').text()).toContain(primary.ipv4)
    expect(overview.get('.detail-network').text()).toContain(primary.ipv6)
    const addresses = Object.fromEntries(overview.findAll('.detail-endpoints > div').map(item => [item.get('dt').text(), item.get('dd').text()]))
    expect(addresses).toEqual({
      '节点 IP': primary.address, IPv4: primary.ipv4, IPv6: primary.ipv6, '补充 IP': primary.manual_addresses.join(' · ')
    })
    expect(view.get('[aria-labelledby="node-traffic-title"]').text()).toContain('流量统计与配额')
    expect(overview.find('[aria-labelledby="node-traffic-title"]').exists()).toBe(false)
    expect(service.classes()).toContain('ops-panel')
    expect(view.get('.detail-cards').findAll(':scope > .ops-panel').map(card => card.get('h2').text())).toEqual(['IP 信息', 'Gost 服务'])
    const traffic = view.get('[aria-labelledby="node-traffic-title"]')
    expect(traffic.element.previousElementSibling).toBe(view.get('.detail-cards').element)
    expect(overview.findAll('.ops-panel')).toHaveLength(0)
    expect(api.getGostService).toHaveBeenCalledWith(primary.id)
    expect(service.text()).toContain('运行中')
    expect(view.find('[role="dialog"]').exists()).toBe(false)
    expect(api.getGostLogs).not.toHaveBeenCalled()
    await service.get('button[aria-controls="node-gost-logs"]').trigger('click')
    await flushPromises()
    expect(api.getGostLogs).toHaveBeenCalledWith(primary.id)
    expect(service.get('#node-gost-logs').text()).toContain('启动完成')
  })

  it('opens upgrades separately and previews nft only from node configuration', async () => {
    api.getNodeNftConfig.mockResolvedValue(response({
      config: '{"nftables":[{"table":{"family":"inet","name":"yagp_quota"}}]}',
      live: true
    }))
    api.getNodeUpgradeStatus.mockResolvedValue(response({ available: false }))
    const view = open()
    await flushPromises()
    await openDetail(view)
    await click(view, '管理升级')
    await vi.dynamicImportSettled()
    await flushPromises()
    expect(api.getNodeUpgradeStatus).toHaveBeenCalledWith(primary.id)
    await click(dialog(view, `版本与升级 · ${primary.name}`), '关闭')

    expect(view.find('#tab-nft').exists()).toBe(false)
    await click(view, '配置')
    const settings = dialog(view, settingsTitle)
    expect(api.getNodeNftConfig).not.toHaveBeenCalled()
    await settings.get('#tab-nft').trigger('click')
    await vi.dynamicImportSettled()
    await flushPromises()
    expect(api.getNodeNftConfig).toHaveBeenCalledWith(primary.id)
    expect(view.findAll('[role="dialog"]')).toHaveLength(1)
    const preview = settings.get('#pane-nft')
    expect(preview.get('pre').text()).toContain('yagp_quota')
    expect(preview.find('textarea').exists()).toBe(false)
    expect(preview.text()).not.toContain('应用到节点')
  })

  it('cancels deletion, reports a failed delete and reloads after a successful one', async () => {
    const logger = vi.spyOn(console, 'error').mockImplementation(() => {})
    const failure = new Error('in use')
    api.deleteNode.mockRejectedValueOnce(failure).mockResolvedValueOnce(response({}))
    const view = open()
    await flushPromises()
    await openDetail(view)
    await click(view, '配置')
    const settings = dialog(view, settingsTitle)
    await settings.get('#tab-danger').trigger('click')
    messages.confirm.mockRejectedValueOnce('cancel')
    await click(settings, '删除节点')
    expect(api.deleteNode).not.toHaveBeenCalled()
    await click(settings, '删除节点')
    expect(logger).toHaveBeenCalledWith('删除节点失败:', failure)
    expect(view.text()).toContain(primary.name)
    api.getNodeList.mockResolvedValueOnce(list([]))
    await click(settings, '删除节点')
    expect(api.deleteNode).toHaveBeenLastCalledWith(primary.id)
    expect(messages.success).toHaveBeenCalledWith('节点已删除')
    expect(view.text()).toContain('尚未添加节点')
  })
})
