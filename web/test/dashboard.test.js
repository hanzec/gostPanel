import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import { getLogList } from '@/api/log'
import { getDashboardStats } from '@/api/stats'
import Dashboard from '@/views/Dashboard.vue'

vi.mock('@/api/log', () => ({ getLogList: vi.fn() }))
vi.mock('@/api/stats', () => ({ getDashboardStats: vi.fn() }))

const resources = {
  nodes: { total: 2500, online: 1250, offline: 1250 },
  rules: { total: 2, running: 3, stopped: 0, forward_type: 1, tunnel_type: 1 },
  tunnels: { total: 0, running: 0, stopped: 0 },
  traffic: {
    input_bytes: 1024,
    output_bytes: 2048,
    tunnels: [
      { id: 1, name: '东京 1', status: 'running', exit_node_id: 10, exit_node_name: '东京出口', exit_node_status: 'online', input_bytes: 1024, output_bytes: 2048, measured: true },
      { id: 2, name: '东京 2', status: 'running', exit_node_id: 10, exit_node_name: '东京出口', exit_node_status: 'online', input_bytes: 0, output_bytes: 0, measured: true },
      { id: 3, name: '伦敦', status: 'running', exit_node_id: 20, exit_node_name: '伦敦出口', exit_node_status: 'offline', input_bytes: 0, output_bytes: 0, measured: false },
      { id: 4, name: '香港', status: 'stopped', exit_node_id: 30, exit_node_name: '香港出口', exit_node_status: 'offline', input_bytes: 0, output_bytes: 0, measured: false }
    ]
  },
  database: 'sqlite',
  version: '1.0.0'
}
const logs = [
  { id: 1, created_at: '2026-11-08T02:00:00Z', username: 'alice', action: 'create', details: 'Added a node' },
  { id: 2, action: 'custom' }
]
let wrapper

async function mountOverview() {
  const placeholder = { template: '<span />' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/nodes', '/logs'].map((path) => ({ path, component: placeholder }))
  })
  await router.push('/')
  wrapper = mount(Dashboard, { global: { plugins: [ElementPlus, router] } })
  return wrapper
}

async function pollOverview() {
  await vi.advanceTimersByTimeAsync(5000)
  await flushPromises()
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.resetAllMocks()
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.useRealTimers()
})

describe('Dashboard overview', () => {
  it('shows pending resources and displays distribution, runtime and the recent audit log', async () => {
    let resolveStats
    let resolveLogs
    getDashboardStats.mockReturnValue(new Promise((resolve) => { resolveStats = resolve }))
    getLogList.mockReturnValue(new Promise((resolve) => { resolveLogs = resolve }))
    const page = await mountOverview()

    expect(page.get('[aria-label="资源概况"]').attributes('aria-busy')).toBe('true')
    expect(page.get('[aria-label="正在加载最近操作"]').attributes('role')).toBe('status')
    expect(getLogList).toHaveBeenCalledExactlyOnceWith({ page: 1, pageSize: 5 }, { silentError: true })
    await pollOverview()
    expect(getDashboardStats).toHaveBeenCalledTimes(1)
    expect(getLogList).toHaveBeenCalledTimes(1)
    resolveStats({ data: resources })
    resolveLogs({ data: { list: logs } })
    await flushPromises()

    expect(page.get('[aria-label="资源概况"]').attributes('aria-busy')).toBe('false')
    expect(page.findAll('.kpi-value').map((item) => item.text())).toEqual(['2,500', '2', '0'])
    expect(page.findAll('.kpi-card .distribution-fill').map((item) => item.element.style.width)).toEqual(['50%', '100%', '0%'])
    expect(page.get('.distribution-list').text()).toContain('暂无资源')
    expect(page.get('.distribution-types').text()).toContain('端口转发 1')
    expect(page.get('.runtime-list').text()).toContain('1.0.0')
    expect(page.get('.runtime-list').text()).toContain('SQLite')
    expect(Date.parse(page.get('.runtime-list time').attributes('datetime'))).not.toBeNaN()
    const entries = page.findAll('.overview-activity tbody tr')
    expect(entries[0].text()).toContain('alice')
    expect(entries[0].text()).toContain('创建')
    expect(entries[0].text()).toContain('Added a node')
    expect(entries[0].get('time').attributes('datetime')).toBe(logs[0].created_at)
    expect(entries[1].findAll('td').map((item) => item.text())).toEqual(['—', '—', 'custom', '—'])
    expect(page.get('a[href="/nodes"]').text()).toContain('查看节点')
    expect(page.get('a[href="/logs"]').text()).toContain('查看全部')
  })

  it('includes system automatic upgrades in recent operations', async () => {
    getDashboardStats.mockResolvedValue({ data: resources })
    getLogList.mockResolvedValue({ data: { list: [{
      id: 40, username: '系统', action: 'auto_upgrade', resource_type: 'node', resource_id: 7,
      details: '节点 东京 (#7)：Gost 自动升级成功（v3.2.5 → v3.2.6）'
    }] } })
    const page = await mountOverview()
    await flushPromises()
    const activity = page.get('.overview-activity')
    expect(activity.get('thead').text()).toContain('执行者')
    expect(activity.get('tbody').text()).toContain('系统')
    expect(activity.get('tbody .ops-status').text()).toBe('自动升级')
    expect(activity.get('.overview-log-detail').text()).toContain('Gost 自动升级成功（v3.2.5 → v3.2.6）')
  })

  it('keeps the last successful stats and log entries on failure and retries on the next poll', async () => {
    getDashboardStats.mockResolvedValueOnce({ data: resources }).mockRejectedValueOnce(new Error('stats offline'))
      .mockResolvedValue({ data: { ...resources, database: 'mysql', version: '2.0.0' } })
    getLogList.mockResolvedValueOnce({ data: { list: logs } }).mockRejectedValueOnce(new Error('logs offline'))
      .mockResolvedValue({ data: {} })
    const page = await mountOverview()
    await flushPromises()
    await pollOverview()

    const [statsAlert, logsAlert] = page.findAll('[role="alert"]')
    expect(statsAlert.text()).toContain('状态更新失败，当前显示上次获取的数据')
    expect(logsAlert.text()).toContain('日志更新失败，当前显示上次获取的记录')
    expect(page.get('.runtime-list').text()).toContain('1.0.0')
    expect(page.get('.overview-activity tbody').text()).toContain('alice')

    await pollOverview()
    expect(getDashboardStats).toHaveBeenCalledTimes(3)
    expect(getLogList).toHaveBeenCalledTimes(3)
    expect(page.get('.runtime-list').text()).toContain('MySQL')
    expect(page.get('.runtime-list').text()).toContain('2.0.0')
    expect(page.get('.overview-empty').text()).toContain('暂无操作记录')
    expect(page.find('[role="alert"]').exists()).toBe(false)
  })

  it('shows the initial failure states and uses display defaults for absent and unfamiliar metadata', async () => {
    getDashboardStats.mockRejectedValueOnce(new Error('unavailable'))
      .mockResolvedValueOnce({ data: { ...resources, version: '', database: 'custom-db' } })
      .mockResolvedValue({ data: { ...resources, version: '', database: '', tunnels: { total: null, running: null, stopped: null } } })
    getLogList.mockRejectedValueOnce(new Error('unavailable')).mockResolvedValue({ data: { list: [] } })
    const page = await mountOverview()
    await flushPromises()
    const [statsAlert, logsAlert] = page.findAll('[role="alert"]')
    expect(statsAlert.text()).toContain('暂时无法获取运行状态')
    expect(logsAlert.text()).toContain('暂时无法加载最近操作')
    expect(page.find('[aria-label="资源概况"]').exists()).toBe(false)
    await pollOverview()
    expect(page.findAll('.runtime-list dd').slice(0, 2).map((item) => item.text())).toEqual(['—', 'custom-db'])
    await pollOverview()
    expect(page.findAll('.runtime-list dd').slice(0, 2).map((item) => item.text())).toEqual(['—', '—'])
    expect(page.findAll('.kpi-value').at(-1).text()).toBe('—')
  })

  it('updates total and per-tunnel rates automatically, even for a shared exit', async () => {
    const nextTraffic = {
      input_bytes: 4096,
      output_bytes: 5120,
      tunnels: [
        { ...resources.traffic.tunnels[0], input_bytes: 2048, output_bytes: 4096 },
        { ...resources.traffic.tunnels[1], input_bytes: 2048, output_bytes: 1024 },
        ...resources.traffic.tunnels.slice(2)
      ]
    }
    getDashboardStats.mockResolvedValueOnce({ data: resources })
      .mockResolvedValueOnce({ data: { ...resources, traffic: nextTraffic } })
      .mockResolvedValue({ data: { ...resources, traffic: { input_bytes: 0, output_bytes: 0, tunnels: [] } } })
    getLogList.mockResolvedValue({ data: { list: [] } })
    const page = await mountOverview()
    await flushPromises()

    expect(page.findAll('button')).toHaveLength(0)
    expect(page.findAll('.traffic-metric .ops-mono').map((item) => item.text())).toEqual(['3 KiB', '—', '—'])
    const rows = page.findAll('.traffic-table tbody tr')
    expect(rows.map((item) => item.get('strong').text())).toEqual(['东京 1', '东京 2', '伦敦', '香港'])
    for (const row of rows.slice(0, 2)) expect(row.text()).toContain('东京出口')
    expect(rows[2].text()).toContain('等待采集')
    expect(rows[3].text()).toContain('隧道启动后开始统计')

    await pollOverview()
    expect(page.findAll('.traffic-metric .ops-mono').map((item) => item.text())).toEqual(['9 KiB', '614.4 B/s', '614.4 B/s'])
    expect(rows[0].findAll('.ops-mono').map((item) => item.text())).toEqual(['204.8 B/s', '409.6 B/s'])
    expect(rows[1].findAll('.ops-mono').map((item) => item.text())).toEqual(['409.6 B/s', '204.8 B/s'])

    await pollOverview()
    expect(page.findAll('.traffic-metric .ops-mono').map((item) => item.text())).toEqual(['0 B', '—', '—'])
    expect(page.get('.traffic-exits').text()).toContain('暂无隧道')

    wrapper.unmount()
    wrapper = undefined
    await pollOverview()
    expect(getDashboardStats).toHaveBeenCalledTimes(3)
  })
})
