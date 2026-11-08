import { flushPromises } from '@vue/test-utils'
import { ElForm, ElMessage, ElMessageBox, ElSelect } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Tunnels from '@/views/Tunnels.vue'
import { getNodeList } from '@/api/node'
import { createTunnel, deleteTunnel, getTunnelList, startTunnel, stopTunnel, updateTunnel } from '@/api/tunnel'
import { button, changePage, choose, command, field, mountInventory, result, row, select } from './inventory-fixtures'

vi.mock('@/api/node', () => ({ getNodeList: vi.fn() }))
vi.mock('@/api/tunnel', () => ({
  createTunnel: vi.fn(), deleteTunnel: vi.fn(), getTunnelList: vi.fn(),
  startTunnel: vi.fn(), stopTunnel: vi.fn(), updateTunnel: vi.fn()
}))

const ingress = { id: 4, name: '入口 A' }
const egress = { id: 9, name: '出口 B' }
const relay = {
  id: 12, name: 'Office relay', status: 'running', protocol: 'tls', relay_port: 65535,
  entry_node: ingress, entry_node_id: ingress.id, exit_node: egress, exit_node_id: egress.id, remark: 'Private network'
}
const udpRelay = {
  id: 13, name: 'Media relay', status: 'stopped', protocol: 'udp', relay_port: 8443,
  entry_node_id: ingress.id, exit_node_id: egress.id
}
const missingRelay = { id: 14, name: 'Unknown relay', entry_node_id: 31, exit_node_id: 32 }
const tunnels = [relay, udpRelay, { ...missingRelay, id: 15, name: 'Broken relay', status: 'error' }, missingRelay]
const dialog = wrapper => wrapper.get('[role="dialog"]')
const selectField = (wrapper, prop) => field(wrapper, prop).getComponent(ElSelect)

beforeEach(() => {
  getNodeList.mockResolvedValue(result([ingress, egress]))
  getTunnelList.mockResolvedValue(result(tunnels, 32))
  for (const action of [createTunnel, updateTunnel, deleteTunnel, startTunnel, stopTunnel]) action.mockResolvedValue({})
})

describe('Relay tunnel inventory', () => {
  it('shows loading and offline states, retries both requests, and filters paginated results', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const list = Promise.withResolvers()
    getTunnelList.mockReturnValueOnce(list.promise)
    getNodeList.mockRejectedValueOnce(new Error('nodes offline'))
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
    expect(dialog(wrapper).text()).toContain('无法加载节点选项')
    await button(dialog(wrapper), '重新加载节点').trigger('click')
    await flushPromises()
    expect(getNodeList).toHaveBeenLastCalledWith({ pageSize: 100 })
    expect(dialog(wrapper).text()).not.toContain('无法加载节点选项')
    await button(dialog(wrapper), '取消').trigger('click')

    await button(wrapper, '重新加载').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['32', '1', '1', '1'])
    expect(row(wrapper, 'Office relay').text()).toContain('Private network')
    expect(row(wrapper, 'Office relay').text()).toContain('出口 B')
    expect(row(wrapper, 'Office relay').text()).toContain('TLS')
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

  it('links to node management when fewer than two nodes are available', async () => {
    getNodeList.mockResolvedValueOnce({ data: {} })
    getTunnelList.mockResolvedValueOnce(result())
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper.get('.inventory-state'), '添加隧道').trigger('click')
    expect(dialog(wrapper).text()).toContain('建立隧道至少需要两个节点')
    await dialog(wrapper).get('.tunnel-link').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

describe('Relay tunnel configuration', () => {
  it('validates a new tunnel and preserves its form after a failed save', async () => {
    const error = new Error('relay already listening')
    const log = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await button(wrapper, '添加隧道').trigger('click')
    const editor = dialog(wrapper)
    await field(editor, 'name').get('input').setValue('New UDP relay')
    await button(editor, '添加隧道').trigger('click')
    await flushPromises()
    expect(wrapper.getComponent(ElForm).emitted('validate')).toContainEqual(['entry_node_id', false, '请选择入口节点'])
    expect(createTunnel).not.toHaveBeenCalled()

    await choose(selectField(editor, 'entry_node_id'), 4)
    await choose(selectField(editor, 'exit_node_id'), 9)
    await choose(selectField(editor, 'protocol'), 'quic')
    await field(editor, 'relay_port').get('input').setValue('9553')
    await field(editor, 'remark').get('textarea').setValue('new service')
    createTunnel.mockRejectedValueOnce(error)
    await button(editor, '添加隧道').trigger('click')
    await flushPromises()
    expect(log).toHaveBeenCalledWith('保存隧道失败:', error)
    expect(field(dialog(wrapper), 'name').get('input').element.value).toBe('New UDP relay')
    await button(editor, '添加隧道').trigger('click')
    await flushPromises()
    expect(createTunnel).toHaveBeenLastCalledWith({
      name: 'New UDP relay', entry_node_id: 4, exit_node_id: 9,
      protocol: 'quic', relay_port: 9553, remark: 'new service'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已添加')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('keeps endpoints fixed on edit and adjusts the listening port on copies', async () => {
    const wrapper = mountInventory(Tunnels)
    await flushPromises()
    await command(row(wrapper, 'Office relay'), 'edit')
    const editor = dialog(wrapper)
    expect(editor.text()).toContain('入口和出口在创建时确定')
    expect(selectField(editor, 'entry_node_id').props('disabled')).toBe(true)
    expect(selectField(editor, 'exit_node_id').props('disabled')).toBe(true)
    await field(editor, 'name').get('input').setValue('Office TLS')
    await button(editor, '保存更改').trigger('click')
    await flushPromises()
    expect(updateTunnel).toHaveBeenLastCalledWith(12, {
      name: 'Office TLS', entry_node_id: 4, exit_node_id: 9, protocol: 'tls',
      relay_port: 65535, remark: 'Private network'
    })
    expect(ElMessage.success).toHaveBeenCalledWith('隧道已更新')

    await command(row(wrapper, 'Office relay'), 'copy')
    expect(field(dialog(wrapper), 'name').get('input').element.value).toBe('Office relay-copy')
    expect(field(dialog(wrapper), 'relay_port').get('input').element.value).toBe('8443')
    await button(dialog(wrapper), '取消').trigger('click')
    await command(row(wrapper, 'Media relay'), 'copy')
    expect(field(dialog(wrapper), 'relay_port').get('input').element.value).toBe('8444')
    await button(dialog(wrapper), '取消').trigger('click')
    await command(row(wrapper, 'Unknown relay'), 'edit')
    expect(selectField(dialog(wrapper), 'protocol').props('modelValue')).toBe('tcp')
    expect(field(dialog(wrapper), 'relay_port').get('input').element.value).toBe('8443')
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
