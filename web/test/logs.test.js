import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import Logs from '@/views/Logs.vue'
import { getLogList } from '@/api/log'
import { button, changePage, choose, mountInventory, result, select } from './inventory-fixtures'

vi.mock('@/api/log', () => ({ getLogList: vi.fn() }))

const createdAt = '2026-10-15T04:23:00Z'
const auditRecords = [
  {
    id: 9, created_at: createdAt, username: 'alice', ip_address: '192.0.2.5',
    action: 'delete', resource_type: 'node', resource_id: 7,
    user_agent: 'Fixture browser', details: 'Removed a decommissioned node'
  },
  { id: 8, username: 'alice', action: 'update', resource_type: 'node', resource_id: 7 },
  { id: 6, username: 'bob', action: 'custom', resource_type: 'other' },
  { id: 5 }
]

describe('audit logs', () => {
  it('loads summary counts and opens the complete audit details', async () => {
    const response = Promise.withResolvers()
    getLogList.mockReturnValueOnce(response.promise)
    const wrapper = mountInventory(Logs)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('正在读取操作日志…')
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['—', '—', '—', '—'])

    response.resolve(result(auditRecords, 15))
    await flushPromises()
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['15', '2', '1', '1'])
    expect(wrapper.get('time').attributes('datetime')).toBe(createdAt)
    expect(wrapper.get('time').text()).toBe(new Date(createdAt).toLocaleString('zh-CN', { hour12: false }))
    expect(wrapper.get('tbody').text()).toContain('custom')
    expect(wrapper.get('tbody').text()).toContain('other')
    expect(wrapper.findAll('tbody tr').at(-1).text()).toContain('—')

    await wrapper.get('[aria-label="查看操作记录 9"]').trigger('click')
    const details = wrapper.get('[role="dialog"]')
    expect(details.attributes('aria-label')).toBe('操作记录 #9')
    expect(details.get('pre').text()).toBe('Removed a decommissioned node')
    expect(details.text()).toContain('Fixture browser')
    expect(details.text()).toContain('192.0.2.5')
    await button(details, '关闭').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    await wrapper.get('[aria-label="查看操作记录 5"]').trigger('click')
    expect(wrapper.get('[role="dialog"] pre').text()).toBe('—')
  })

  it('keeps the last page on failure, retries, and filters from page one', async () => {
    getLogList.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(result(auditRecords, 15))
    const wrapper = mountInventory(Logs)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('请检查连接后重试。')
    await button(wrapper, '重试').trigger('click')
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('alice')

    await changePage(wrapper, 2)
    expect(getLogList).toHaveBeenLastCalledWith({
      page: 2, pageSize: 10, username: '', action: '', resource_type: ''
    })
    getLogList.mockRejectedValueOnce(new Error('offline'))
    await button(wrapper, '刷新日志').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('下方保留了上次获取的记录。')
    expect(wrapper.get('tbody').text()).toContain('alice')

    getLogList.mockResolvedValueOnce({ data: {} })
    await wrapper.get('#audit-user').setValue('  alice  ')
    await choose(select(wrapper, 'audit-action'), 'delete')
    await choose(select(wrapper, 'audit-resource'), 'node')
    await wrapper.get('form[aria-label="日志筛选"]').trigger('submit')
    await flushPromises()
    expect(getLogList).toHaveBeenLastCalledWith({
      page: 1, pageSize: 10, username: 'alice', action: 'delete', resource_type: 'node'
    })
    expect(wrapper.get('.audit-placeholder').text()).toContain('没有匹配的记录')
    getLogList.mockResolvedValueOnce(result())
    await button(wrapper, '清除筛选').trigger('click')
    await flushPromises()
    expect(getLogList).toHaveBeenLastCalledWith({
      page: 1, pageSize: 10, username: '', action: '', resource_type: ''
    })
    expect(wrapper.get('.audit-placeholder').text()).toContain('还没有操作记录')
  })

  it.each(['resolve', 'reject'])('ignores an obsolete request that later %ss', async finish => {
    getLogList.mockResolvedValue(result(auditRecords, 40))
    const wrapper = mountInventory(Logs)
    await flushPromises()

    const older = Promise.withResolvers()
    getLogList.mockReturnValueOnce(older.promise).mockResolvedValueOnce(result([{ id: 77, username: 'latest' }], 40))
    await changePage(wrapper, 2)
    await changePage(wrapper, 3)
    older[finish](finish === 'resolve' ? result([{ id: 88, username: 'obsolete' }]) : new Error('obsolete'))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('latest')
    expect(wrapper.get('tbody').text()).not.toContain('obsolete')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })
})
