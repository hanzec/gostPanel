import { flushPromises } from '@vue/test-utils'
import { ElSwitch } from 'element-plus'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NodeGostDialog from '@/components/NodeGostDialog.vue'
import NodeNftDialog from '@/components/NodeNftDialog.vue'
import NodeUpgradeDialog from '@/components/NodeUpgradeDialog.vue'
import * as api from '@/api/node'
import { apiError, button, click, deferred, mockNodeMessages, mountNodeComponent, response } from './node-test-support.js'

vi.mock('@/api/node', () => ({
  getGostService: vi.fn(),
  getGostLogs: vi.fn(),
  restartGostService: vi.fn(),
  getNodeNftConfig: vi.fn(),
  applyNodeNftConfig: vi.fn(),
  getNodeUpgradeStatus: vi.fn(),
  updateNodeUpgradePolicy: vi.fn(),
  upgradeNodeComponent: vi.fn()
}))

const node = { id: 7, name: '边缘节点' }
let wrapper
let messages

beforeEach(() => {
  vi.resetAllMocks()
  messages = mockNodeMessages()
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function open(component) {
  wrapper = mountNodeComponent(component, { node, modelValue: true })
  return wrapper
}

describe('Gost service dialog', () => {
  const active = { unit: 'gost.service', active_state: 'active', load_state: 'loaded', sub_state: 'running' }

  it('loads status and sorts service logs newest first', async () => {
    const status = deferred()
    api.getGostService.mockReturnValueOnce(status.promise)
    api.getGostLogs.mockResolvedValueOnce(response({ entries: [
      { message: 'older', priority: 6, timestamp_unix_micros: 1_000_000 },
      { message: 'newer', priority: 42, timestamp_unix_micros: 2_000_000 },
      { message: 'unknown', timestamp_unix_micros: 0 }
    ] }))
    const view = open(NodeGostDialog)
    expect(view.text()).toContain('正在读取服务状态')
    expect(button(view, '重启 Gost').element.disabled).toBe(true)
    status.resolve(response(active))
    await flushPromises()
    expect(api.getGostService).toHaveBeenCalledWith(node.id)
    expect(api.getGostLogs).toHaveBeenCalledWith(node.id)
    expect(view.text()).toContain('运行中')
    expect(view.findAll('.gost-log-message').map(item => item.text())).toEqual(['newer', 'older', 'unknown'])
    expect(view.text()).toContain('未知级别')
    expect(view.text()).toContain('未知时间')
    expect(view.text()).toContain('紧急')

    api.getGostService.mockResolvedValue(response({ ...active, active_state: 'activating', sub_state: '' }))
    api.getGostLogs.mockResolvedValue(response({}))
    await click(view, '刷新状态和日志')
    expect(view.get('.ops-status').classes()).toContain('is-warning')
    expect(view.text()).toContain('未知')
    expect(view.text()).toContain('暂无日志')
    await click(view, '关闭')
    expect(view.emitted('update:modelValue')).toEqual([[false]])
  })

  it('retries independent read failures and handles restart cancellation, failure and success', async () => {
    api.getGostService.mockRejectedValueOnce(apiError('节点离线')).mockResolvedValue(response(active))
    api.getGostLogs.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(response({ entries: [] }))
    const view = open(NodeGostDialog)
    await flushPromises()
    expect(view.text()).toContain('节点离线')
    expect(view.text()).toContain('无法读取 Gost 日志')
    await click(view, '刷新状态和日志')
    expect(view.findAll('[role="alert"]')).toHaveLength(0)

    messages.confirm.mockRejectedValueOnce('cancel')
    await click(view, '重启 Gost')
    expect(api.restartGostService).not.toHaveBeenCalled()

    api.restartGostService.mockRejectedValueOnce(apiError('缺少重启权限'))
    await click(view, '重启 Gost')
    expect(view.get('[role="alert"]').text()).toBe('缺少重启权限')
    api.restartGostService.mockRejectedValueOnce(new Error('lost'))
    await click(view, '重启 Gost')
    expect(view.get('[role="alert"]').text()).toContain('重启请求失败')
    api.restartGostService.mockResolvedValueOnce(response(active))
    await click(view, '重启 Gost')
    expect(api.restartGostService).toHaveBeenCalledWith(node.id)
    expect(api.getGostLogs).toHaveBeenCalledTimes(5)
    expect(messages.success).toHaveBeenCalledWith('Gost 重启操作已完成')
    expect(view.find('[role="alert"]').exists()).toBe(false)
  })

  it('uses the service state and fallback errors when the bridge does not supply descriptions', async () => {
    api.getGostService.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(response({ unit: 'gost.service', active_state: 'custom' }))
    api.getGostLogs.mockRejectedValueOnce(apiError('日志不可用')).mockResolvedValue(response({ entries: [] }))
    const view = open(NodeGostDialog)
    await flushPromises()
    expect(view.text()).toContain('无法读取 Gost 状态')
    expect(view.text()).toContain('日志不可用')
    await click(view, '刷新状态和日志')
    expect(view.get('.ops-status').text()).toBe('custom')
    expect(view.get('.ops-status').classes()).toContain('is-muted')
    expect(button(view, '重启 Gost').element.disabled).toBe(true)
  })
})

describe('nftables dialog', () => {
  const template = '{"nftables":[]}'
  const saved = '{"nftables":[{"saved":true}]}'
  const config = { table: 'yagp_7', config: saved, template }

  it('retries loading, protects an edited draft and validates JSON before asking to apply', async () => {
    api.getNodeNftConfig.mockRejectedValueOnce(apiError('没有已注册的节点'))
      .mockRejectedValueOnce(new Error('offline')).mockResolvedValue(response(config))
    const view = open(NodeNftDialog)
    expect(view.text()).toContain('正在从面板读取配置')
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toContain('没有已注册的节点')
    expect(button(view, '应用到节点').element.disabled).toBe(true)
    await click(view, '重新加载')
    expect(view.text()).toContain('无法读取 nft 配置')
    await click(view, '重新加载')
    expect(api.getNodeNftConfig).toHaveBeenCalledWith(node.id)
    expect(view.text()).toContain('inet yagp_7')
    expect(view.text()).toContain('最近一次成功应用')
    const input = view.get('textarea#node-nft-config')
    expect(input.element.value).toBe(saved)

    await input.setValue('{')
    await click(view, '应用到节点')
    expect(view.get('[role="alert"]').text()).toContain('JSON 格式有误')
    expect(document.activeElement).toBe(view.get('#nft-config-error').element)
    expect(messages.confirm).not.toHaveBeenCalled()
    expect(api.applyNodeNftConfig).not.toHaveBeenCalled()
    messages.confirm.mockRejectedValueOnce('cancel')
    await click(view, '载入面板默认模板')
    expect(input.element.value).toBe('{')
    await click(view, '载入面板默认模板')
    expect(input.element.value).toBe(template)
    expect(view.text()).toContain('当前显示面板默认模板')
    expect(view.find('#nft-config-error').exists()).toBe(false)
  })

  it('preserves the draft until the node acknowledges an applied config', async () => {
    api.getNodeNftConfig.mockResolvedValue(response(config))
    const view = open(NodeNftDialog)
    await flushPromises()
    await click(view, '载入面板默认模板')
    expect(messages.confirm).not.toHaveBeenCalled()
    expect(button(view, '载入面板默认模板').element.disabled).toBe(true)
    const input = view.get('textarea#node-nft-config')
    await input.setValue('   ')
    expect(button(view, '应用到节点').element.disabled).toBe(true)
    await input.setValue(template)
    messages.confirm.mockRejectedValueOnce('cancel')
    await click(view, '应用到节点')
    expect(api.applyNodeNftConfig).not.toHaveBeenCalled()
    api.applyNodeNftConfig.mockRejectedValueOnce(apiError('表被占用'))
    await click(view, '应用到节点')
    expect(view.text()).toContain('表被占用')
    api.applyNodeNftConfig.mockRejectedValueOnce(new Error('lost connection'))
    await click(view, '应用到节点')
    expect(view.text()).toContain('无法确认 nft 配置已应用')
    expect(input.element.value).toBe(template)

    const apply = deferred()
    api.applyNodeNftConfig.mockReturnValueOnce(apply.promise)
    await click(view, '应用到节点')
    expect(input.element.disabled).toBe(true)
    expect(button(view, '关闭').element.disabled).toBe(true)
    apply.resolve(response({ ...config, config: template }))
    await flushPromises()
    expect(api.applyNodeNftConfig).toHaveBeenLastCalledWith(node.id, template)
    expect(messages.success).toHaveBeenCalledWith('nft 配置已应用')
    expect(view.text()).toContain('最近一次成功应用')
    await click(view, '关闭')
    expect(view.emitted('update:modelValue')).toEqual([[false]])
  })

  it('shows the server template for an unconfigured node and disables applying an empty template', async () => {
    api.getNodeNftConfig.mockResolvedValueOnce(response({ table: 'yagp_7', template }))
    const view = open(NodeNftDialog)
    await flushPromises()
    expect(view.get('textarea').element.value).toBe(template)
    expect(view.text()).toContain('面板默认模板')
    view.unmount()
    wrapper = undefined
    api.getNodeNftConfig.mockResolvedValueOnce(response({ table: 'yagp_7' }))
    const noTemplate = open(NodeNftDialog)
    await flushPromises()
    expect(noTemplate.get('textarea').element.value).toBe('')
    expect(button(noTemplate, '应用到节点').element.disabled).toBe(true)
  })
})

describe('node upgrade dialog', () => {
  const available = {
    available: true,
    gost_version: '3.0', latest_gost_version: '3.1', gost_update_available: true,
    daemon_version: '1.0', latest_daemon_version: '1.1', daemon_update_available: true,
    auto_upgrade_gost: false, auto_upgrade_daemon: true
  }
  const manual = (view, component) => view.get(`button[aria-label="手动升级 ${component}"]`)

  it('keeps policy editable on an unavailable node and commits changes only on success', async () => {
    api.getNodeUpgradeStatus.mockRejectedValueOnce(apiError('节点维护中'))
      .mockResolvedValueOnce(response({ available: false, reason: '需要升级守护程序' }))
      .mockResolvedValueOnce(response({ ...available, gost_update_available: false, daemon_version: '', latest_daemon_version: '', daemon_update_available: false }))
    const view = open(NodeUpgradeDialog)
    await nextTick()
    expect(view.text()).toContain('正在读取版本与升级状态')
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toBe('节点维护中')
    await click(view, '刷新版本状态')
    expect(view.text()).toContain('需要升级守护程序')
    expect(view.text()).toContain('自动升级设置仍可编辑')
    expect(manual(view, 'Gost').element.disabled).toBe(true)

    const [gost, daemon] = view.findAllComponents(ElSwitch)
    api.updateNodeUpgradePolicy.mockRejectedValueOnce(new Error('权限不足')).mockResolvedValue(response({}))
    daemon.vm.$emit('change', true)
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toBe('权限不足')
    expect(daemon.props('modelValue')).toBe(false)
    daemon.vm.$emit('change', true)
    await flushPromises()
    expect(api.updateNodeUpgradePolicy).toHaveBeenLastCalledWith(node.id, { auto_upgrade_gost: false, auto_upgrade_daemon: true })
    expect(daemon.props('modelValue')).toBe(true)
    gost.vm.$emit('change', true)
    await flushPromises()
    expect(api.updateNodeUpgradePolicy).toHaveBeenLastCalledWith(node.id, { auto_upgrade_gost: true, auto_upgrade_daemon: true })
    expect(messages.success).toHaveBeenCalledWith('已保存自动升级设置')
    await click(view, '刷新版本状态')
    expect(view.text()).toContain('版本待确认')
    expect(view.text()).toContain('暂无更新')
    await click(view, '关闭')
    expect(view.emitted('update:modelValue')).toEqual([[false]])
  })

  it('preserves an upgrade job during restarts, pauses polling and can resume it manually', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    api.getNodeUpgradeStatus.mockResolvedValue(response(available))
    const view = open(NodeUpgradeDialog)
    await flushPromises()
    expect(view.text()).toContain('有可用更新')
    messages.confirm.mockRejectedValueOnce('cancel')
    await manual(view, 'Gost').trigger('click')
    await flushPromises()
    expect(messages.confirm).toHaveBeenCalledWith(expect.stringContaining('正在处理的转发连接'), '升级 Gost', expect.anything())
    expect(api.upgradeNodeComponent).not.toHaveBeenCalled()

    api.upgradeNodeComponent.mockRejectedValueOnce({ isAxiosError: true }).mockResolvedValueOnce(response({
      component: 'daemon', target_version: '1.1', state: 'queued'
    }))
    await manual(view, 'yagp-daemon').trigger('click')
    await flushPromises()
    expect(view.get('[role="alert"]').text()).toContain('无法确认升级请求已提交')
    await manual(view, 'yagp-daemon').trigger('click')
    await flushPromises()
    expect(messages.confirm).toHaveBeenLastCalledWith(expect.stringContaining('面板与节点之间的管理连接'), '升级 yagp-daemon', expect.anything())
    expect(api.upgradeNodeComponent).toHaveBeenLastCalledWith(node.id, 'daemon')
    expect(view.text()).toContain('等待执行')
    expect(messages.success).toHaveBeenCalledWith('升级请求已提交')

    api.getNodeUpgradeStatus.mockRejectedValueOnce(new Error('restarting'))
    await vi.advanceTimersByTimeAsync(4000)
    expect(view.text()).toContain('系统会继续查询')
    api.getNodeUpgradeStatus.mockResolvedValueOnce(response({ available: false }))
    await vi.advanceTimersByTimeAsync(4000)
    expect(view.text()).toContain('等待执行')
    expect(view.text()).toContain('节点当前无法执行手动升级')
    api.getNodeUpgradeStatus.mockResolvedValue(response({ ...available, component: 'daemon', target_version: '1.1', state: 'running' }))
    await vi.advanceTimersByTimeAsync(292000)
    expect(view.text()).toContain('已暂停自动查询')
    const callsAtDeadline = api.getNodeUpgradeStatus.mock.calls.length
    await vi.advanceTimersByTimeAsync(4000)
    expect(api.getNodeUpgradeStatus).toHaveBeenCalledTimes(callsAtDeadline)

    api.getNodeUpgradeStatus.mockResolvedValueOnce(response({ ...available, component: 'daemon', target_version: '1.1', state: 'failed', error: '磁盘已满' }))
    await click(view, '刷新版本状态')
    expect(view.get('.upgrade-job').attributes('role')).toBe('alert')
    expect(view.text()).toContain('磁盘已满')
    expect(view.text()).not.toContain('已暂停自动查询')
  })

  it('does not schedule another refresh after unmounting a pending poll', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    api.getNodeUpgradeStatus.mockResolvedValueOnce(response({ ...available, component: 'gost', state: 'queued' }))
    const view = open(NodeUpgradeDialog)
    await flushPromises()
    const request = deferred()
    api.getNodeUpgradeStatus.mockReturnValueOnce(request.promise)
    await vi.advanceTimersByTimeAsync(4000)
    expect(api.getNodeUpgradeStatus).toHaveBeenCalledTimes(2)
    view.unmount()
    wrapper = undefined
    request.resolve(response({ ...available, component: 'gost', state: 'running' }))
    await nextTick()
    await vi.advanceTimersByTimeAsync(8000)
    expect(api.getNodeUpgradeStatus).toHaveBeenCalledTimes(2)
  })
})
