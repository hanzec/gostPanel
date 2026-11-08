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

function button(container, label) {
  const result = container.findAll('button').find((item) => item.text() === label)
  expect(result, label).toBeDefined()
  return result
}

beforeEach(() => {
  vi.resetAllMocks()
})

afterEach(() => {
  wrapper?.unmount()
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
    expect(getLogList).toHaveBeenCalledExactlyOnceWith({ page: 1, pageSize: 5 })
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
    const entries = page.findAll('tbody tr')
    expect(entries[0].text()).toContain('alice')
    expect(entries[0].text()).toContain('创建')
    expect(entries[0].text()).toContain('Added a node')
    expect(entries[0].get('time').attributes('datetime')).toBe(logs[0].created_at)
    expect(entries[1].findAll('td').map((item) => item.text())).toEqual(['—', '—', 'custom', '—'])
    expect(page.get('a[href="/nodes"]').text()).toContain('查看节点')
    expect(page.get('a[href="/logs"]').text()).toContain('查看全部')
  })

  it('keeps the last successful stats and log entries during refresh failures and retries independently', async () => {
    getDashboardStats.mockResolvedValueOnce({ data: resources }).mockRejectedValueOnce(new Error('stats offline'))
      .mockResolvedValue({ data: { ...resources, database: 'mysql', version: '2.0.0' } })
    getLogList.mockResolvedValueOnce({ data: { list: logs } }).mockRejectedValueOnce(new Error('logs offline'))
      .mockResolvedValue({ data: {} })
    const page = await mountOverview()
    await flushPromises()
    await button(page, '刷新').trigger('click')
    await flushPromises()

    const [statsAlert, logsAlert] = page.findAll('[role="alert"]')
    expect(statsAlert.text()).toContain('状态更新失败，当前显示上次获取的数据')
    expect(logsAlert.text()).toContain('日志更新失败，当前显示上次获取的记录')
    expect(page.get('.runtime-list').text()).toContain('1.0.0')
    expect(page.get('tbody').text()).toContain('alice')

    await button(statsAlert, '重试').trigger('click')
    await flushPromises()
    expect(getDashboardStats).toHaveBeenCalledTimes(3)
    expect(getLogList).toHaveBeenCalledTimes(2)
    expect(page.get('.runtime-list').text()).toContain('MySQL')
    expect(page.get('.runtime-list').text()).toContain('2.0.0')
    await button(logsAlert, '重试').trigger('click')
    await flushPromises()
    expect(getDashboardStats).toHaveBeenCalledTimes(3)
    expect(getLogList).toHaveBeenCalledTimes(3)
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
    await button(page, '刷新').trigger('click')
    await flushPromises()
    expect(page.findAll('.runtime-list dd').slice(0, 2).map((item) => item.text())).toEqual(['—', 'custom-db'])
    await button(page, '刷新').trigger('click')
    await flushPromises()
    expect(page.findAll('.runtime-list dd').slice(0, 2).map((item) => item.text())).toEqual(['—', '—'])
    expect(page.findAll('.kpi-value').at(-1).text()).toBe('—')
  })
})
