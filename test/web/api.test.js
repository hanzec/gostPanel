import { AxiosError } from 'axios'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ElMessage } from 'element-plus'
import request from '@/utils/request'
import * as node from '@/api/node'
import * as rule from '@/api/rule'
import * as tunnel from '@/api/tunnel'
import * as system from '@/api/system'
import { getUserInfo } from '@/api/auth'
import { getLogList } from '@/api/log'
import { getDashboardStats } from '@/api/stats'

vi.mock('element-plus', () => ({ ElMessage: { error: vi.fn() } }))

const response = (config, data) => ({
  status: 200, statusText: 'OK', headers: {}, config, data
})

const adapter = vi.fn()
beforeEach(() => {
  adapter.mockReset().mockImplementation(config => Promise.resolve(response(config, { code: 0, data: { ok: true } })))
  request.defaults.adapter = adapter
  ElMessage.error.mockClear()
})

describe('panel API contracts', () => {
  const filters = { page: 2, pageSize: 10, status: 'online' }
  const body = { name: 'edge' }
  const timeout = { timeout: 25000 }
  const silent = { silentError: true }
  const nodePolicy = { auto_upgrade_daemon: true }

  it.each([
    ['identity', getUserInfo, 'get', '/auth/info'],
    ['dashboard', getDashboardStats, 'get', '/dashboard/stats'],
    ['audit log', () => getLogList(filters), 'get', '/logs', { params: filters }],
    ['node list', () => node.getNodeList(filters), 'get', '/nodes', { params: filters }],
    ['node details', () => node.getNode(42), 'get', '/nodes/42', silent],
    ['create node', () => node.createNode(body), 'post', '/nodes', { data: body }],
    ['update node', () => node.updateNode(42, body), 'put', '/nodes/42', { data: body }],
    ['delete node', () => node.deleteNode(42), 'delete', '/nodes/42'],
    ['node config', () => node.getNodeConfig(42), 'get', '/nodes/42/config'],
    ['registration token', () => node.issueNodeRegistrationToken(42), 'post', '/nodes/42/registration-token', silent],
    ['Gost status', () => node.getGostService(42), 'get', '/nodes/42/gost/service', silent],
    ['restart Gost', () => node.restartGostService(42), 'post', '/nodes/42/gost/restart', { ...timeout, ...silent }],
    ['Gost logs', () => node.getGostLogs(42), 'get', '/nodes/42/gost/logs', silent],
    ['nft config preview', () => node.getNodeNftConfig(42), 'get', '/nodes/42/nft', silent],
    ['node upgrade status', () => node.getNodeUpgradeStatus(42), 'get', '/nodes/42/upgrade', silent],
    ['node upgrade policy', () => node.updateNodeUpgradePolicy(42, nodePolicy), 'put', '/nodes/42/upgrade', { ...silent, data: nodePolicy }],
    ['start upgrade', () => node.upgradeNodeComponent(42, 'daemon'), 'post', '/nodes/42/upgrade/daemon', { ...timeout, ...silent }],
    ['rule list', () => rule.getRuleList(filters), 'get', '/rules', { params: filters }],
    ['rule details', () => rule.getRule(42), 'get', '/rules/42'],
    ['create rule', () => rule.createRule(body), 'post', '/rules', { data: body }],
    ['check rule listener', () => rule.checkRuleListener(5), 'post', '/rules/5/listener-check', { silentError: true }],
    ['update rule', () => rule.updateRule(42, body), 'put', '/rules/42', { data: body }],
    ['delete rule', () => rule.deleteRule(42), 'delete', '/rules/42'],
    ['start rule', () => rule.startRule(42), 'post', '/rules/42/start'],
    ['stop rule', () => rule.stopRule(42), 'post', '/rules/42/stop'],
    ['tunnel list', () => tunnel.getTunnelList(filters), 'get', '/tunnels', { params: filters }],
    ['tunnel details', () => tunnel.getTunnel(42), 'get', '/tunnels/42'],
    ['create tunnel', () => tunnel.createTunnel(body), 'post', '/tunnels', { data: body }],
    ['update tunnel', () => tunnel.updateTunnel(42, body), 'put', '/tunnels/42', { data: body }],
    ['delete tunnel', () => tunnel.deleteTunnel(42), 'delete', '/tunnels/42'],
    ['start tunnel', () => tunnel.startTunnel(42), 'post', '/tunnels/42/start'],
    ['stop tunnel', () => tunnel.stopTunnel(42), 'post', '/tunnels/42/stop'],
    ['test tunnel', () => tunnel.testTunnel(42), 'post', '/tunnels/42/test', { timeout: 15000, ...silent }],
    ['settings', system.getSystemConfig, 'get', '/system/config'],
    ['public settings', system.getPublicSystemConfig, 'get', '/system/public-config'],
    ['update settings', () => system.updateSystemConfig(body), 'put', '/system/config', { data: body }],
    ['test email', () => system.sendTestEmail(body), 'post', '/system/email/test', { data: body }],
    ['test Bark', () => system.sendTestBark(body), 'post', '/system/bark/test', { ...silent, data: body }],
    ['backup', system.backupSystem, 'post', '/system/backup'],
    ['ruleset', system.getGostRuleset, 'get', '/system/gost-ruleset'],
    ['update ruleset', () => system.updateGostRuleset('https://example.test/rules.yaml', [42]), 'put', '/system/gost-ruleset',
      { ...timeout, data: { url: 'https://example.test/rules.yaml', node_ids: [42] } }]
  ])('%s uses the expected method, path and request options', async (_name, call, method, url, expected = {}) => {
    await expect(call()).resolves.toEqual({ code: 0, data: { ok: true } })
    expect(adapter).toHaveBeenCalledTimes(1)
    const [config] = adapter.mock.calls[0]
    const { data, ...options } = expected
    expect(config).toEqual(expect.objectContaining({ baseURL: '/api/v1', method, url, ...options }))
    if (data) expect(JSON.parse(config.data)).toEqual(data)
  })

  it('loads all routing nodes, including an incomplete final page', async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) => ({ id: index + 1 }))
    adapter.mockImplementation(config => Promise.resolve(response(config, {
      code: 0,
      data: { total: 120, list: config.params.page === 1 ? firstPage : [{ id: 101 }] }
    })))
    await expect(node.getRoutingNodes()).resolves.toEqual([...firstPage, { id: 101 }])
    expect(adapter.mock.calls.map(([config]) => config.params)).toEqual([
      { page: 1, pageSize: 100 }, { page: 2, pageSize: 100 }
    ])
  })

  it('handles an empty inventory response', async () => {
    adapter.mockImplementation(config => Promise.resolve(response(config, { code: 0, data: { total: 0 } })))
    await expect(node.getRoutingNodes()).resolves.toEqual([])
    expect(adapter).toHaveBeenCalledTimes(1)
  })
})

describe('panel request errors', () => {
  it.each([
    [401, undefined, '请重新打开面板并通过 Authentik 登录'],
    [403, '此操作需要管理员权限', '此操作需要管理员权限'],
    [403, undefined, '拒绝访问'],
    [404, undefined, '请求地址不存在'],
    [503, '维护中', '维护中'],
    [500, undefined, '请求失败'],
    [undefined, undefined, '网络错误']
  ])('reports HTTP %s errors using the panel message when appropriate', async (status, serverMessage, message) => {
    adapter.mockImplementation(config => Promise.reject(new AxiosError('transport failure', undefined, config, undefined,
      status ? { status, data: serverMessage ? { message: serverMessage } : undefined } : undefined)))
    await expect(getUserInfo()).rejects.toThrow('transport failure')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
  })

  it('rejects business errors, preserving the message and handling an absent message', async () => {
    adapter.mockImplementationOnce(config => Promise.resolve(response(config, { code: 100, message: '端口已占用' })))
      .mockImplementationOnce(config => Promise.resolve(response(config, { code: 100 })))
    await expect(node.createNode({ name: 'duplicate' })).rejects.toThrow('端口已占用')
    await expect(node.createNode({ name: 'duplicate' })).rejects.toThrow('请求失败')
    expect(ElMessage.error.mock.calls).toEqual([['端口已占用'], ['请求失败']])
  })

  it('lets silent dialogs handle transport and business failures inline', async () => {
    adapter.mockImplementationOnce(config => Promise.reject(new AxiosError('unreachable', undefined, config)))
      .mockImplementationOnce(config => Promise.resolve(response(config, { code: 403, message: '令牌已失效' })))
      .mockRejectedValueOnce(new Error('no request config'))
    await expect(node.getNodeUpgradeStatus(42)).rejects.toThrow('unreachable')
    await expect(node.issueNodeRegistrationToken(42)).rejects.toThrow('令牌已失效')
    expect(ElMessage.error).not.toHaveBeenCalled()
    await expect(getUserInfo()).rejects.toThrow('no request config')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('网络错误')
  })
})
