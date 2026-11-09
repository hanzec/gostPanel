import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElMessage, ElMessageBox, ElOption, ElSelect } from 'element-plus'
import { createPinia } from 'pinia'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '@/api/system'
import GostAllowlistSettings from '@/components/GostAllowlistSettings.vue'
import GostRulesetSettings from '@/components/GostRulesetSettings.vue'
import System from '@/views/System.vue'

vi.mock('element-plus', async (original) => ({
  ...await original(),
  ElMessage: { success: vi.fn(), warning: vi.fn() },
  ElMessageBox: { confirm: vi.fn() }
}))

vi.mock('@/api/system', () => ({
  backupSystem: vi.fn(),
  getSystemConfig: vi.fn(),
  getPublicSystemConfig: vi.fn(),
  sendTestBark: vi.fn(),
  sendTestEmail: vi.fn(),
  updateSystemConfig: vi.fn(),
  getGostAllowlist: vi.fn(),
  getGostRuleset: vi.fn(),
  getRoutingNodes: vi.fn(),
  updateGostAllowlist: vi.fn(),
  updateGostRuleset: vi.fn()
}))

const mounted = []
const settings = {
  config: { siteTitle: 'Relay Console', logoUrl: 'https://example.test/logo.svg', copyright: 'Test' },
  email: { host: 'smtp.example.test', port: 587, username: 'relay', password: 'test', fromEmail: 'relay@example.test' },
  log: { retentionDays: 14, level: 'info' },
  backup: { autoBackup: true, retentionCount: 8, supported: true }
}
const nodes = [
  { id: 1, name: 'Dual stack', ipv4: '192.0.2.1', ipv6: '2001:db8::1', address: '192.0.2.1:8080' },
  { id: 2, name: 'Lost IP', ipv4: '', ipv6: '' },
  { id: 3, name: 'Unregistered', ipv4: '', ipv6: '' },
  { id: 4, name: 'IPv4 only', ipv4: '192.0.2.4', ipv6: '', address: '192.0.2.4:8080' }
]
const originalTitle = document.title

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function mountPage(component) {
  const container = document.createElement('div')
  document.body.append(container)
  const wrapper = mount(component, {
    attachTo: container,
    global: { plugins: [ElementPlus, createPinia()] }
  })
  mounted.push({ wrapper, container })
  return wrapper
}

function button(wrapper, label) {
  const result = wrapper.findAll('button').find((item) => item.text() === label)
  expect(result, label).toBeDefined()
  return result
}

async function selectTab(wrapper, label) {
  const tab = wrapper.findAll('[role="tab"]').find((item) => item.text() === label)
  expect(tab, label).toBeDefined()
  await tab.trigger('click')
  await flushPromises()
}

function field(wrapper, label) {
  const item = wrapper.findAll('.el-form-item').find((entry) => entry.get('label').text() === label)
  expect(item, label).toBeDefined()
  return item.get('input')
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  localStorage.clear()
  api.getSystemConfig.mockResolvedValue({ data: settings })
  api.getPublicSystemConfig.mockResolvedValue({ data: settings.config })
  api.updateSystemConfig.mockResolvedValue({ data: {} })
  api.getRoutingNodes.mockResolvedValue(nodes)
  api.getGostAllowlist.mockResolvedValue({ data: { node_ids: [], addresses: [] } })
  api.getGostRuleset.mockResolvedValue({ data: {} })
})

afterEach(() => {
  for (const { wrapper, container } of mounted.splice(0)) {
    wrapper.unmount()
    container.remove()
  }
  document.title = originalTitle
  localStorage.clear()
  vi.unstubAllGlobals()
})

describe('System settings', () => {
  it('retries initial loading and saves brand changes with the loaded log settings', async () => {
    const loading = deferred()
    api.getSystemConfig.mockReturnValueOnce(loading.promise)
    const wrapper = mountPage(System)

    expect(wrapper.get('[aria-label="正在加载系统设置"]').attributes('role')).toBe('status')
    expect(button(wrapper, '保存设置').element.disabled).toBe(true)
    loading.reject(new Error('unavailable'))
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('无法加载系统设置')
    await button(wrapper, '重试').trigger('click')
    await flushPromises()

    expect(field(wrapper, '站点标题').element.value).toBe('Relay Console')
    expect(wrapper.get('aside').text()).toContain('显示预览')
    await field(wrapper, '站点标题').setValue('Updated Console')
    await field(wrapper, 'Logo 地址').setValue('  https://example.test/new.svg  ')
    expect(wrapper.get('aside').text()).toContain('Updated Console')
    api.getPublicSystemConfig.mockResolvedValue({ data: { ...settings.config, siteTitle: 'Updated Console' } })

    const saving = deferred()
    api.updateSystemConfig.mockReturnValueOnce(saving.promise)
    await button(wrapper, '保存设置').trigger('click')
    await wrapper.get('form').trigger('submit')
    expect(api.updateSystemConfig).toHaveBeenCalledExactlyOnceWith({
      config: { ...settings.config, siteTitle: 'Updated Console', logoUrl: 'https://example.test/new.svg' },
      email: settings.email,
      bark: { serverUrl: '', deviceKey: '' },
      log: settings.log,
      backup: { autoBackup: true, retentionCount: 8 }
    })
    saving.resolve({ data: {} })
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledWith('设置已保存')
    expect(document.title).toBe('Updated Console')
  })

  it('validates backup retention and keeps entered settings after a failed save', async () => {
    api.getSystemConfig.mockResolvedValue({
      data: { ...settings, config: { ...settings.config, siteTitle: '' }, email: { ...settings.email, port: 0 } }
    })
    api.updateSystemConfig.mockRejectedValueOnce(new Error('save failed'))
    const wrapper = mountPage(System)
    await flushPromises()
    expect(wrapper.get('aside').text()).toContain('yagp')

    await selectTab(wrapper, '数据库备份')
    const retention = field(wrapper, '保留份数')
    await retention.setValue('')
    await retention.trigger('change')
    await selectTab(wrapper, '面板品牌')
    await button(wrapper, '保存设置').trigger('click')
    expect(ElMessage.warning).toHaveBeenCalledWith('请填写备份保留份数')
    expect(wrapper.get('[aria-selected="true"]').text()).toContain('数据库备份')
    expect(api.updateSystemConfig).not.toHaveBeenCalled()

    await retention.setValue('12')
    await retention.trigger('change')
    await button(wrapper, '保存设置').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('设置保存失败')
    expect(retention.element.value).toBe('12')
    await button(wrapper, '保存设置').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(api.updateSystemConfig).toHaveBeenLastCalledWith(expect.objectContaining({
      email: expect.objectContaining({ port: 0 }),
      backup: { autoBackup: true, retentionCount: 12 }
    }))
  })

  it('checks SMTP inputs, validates the recipient, and sends with the current connection settings', async () => {
    api.getSystemConfig.mockResolvedValue({ data: { ...settings, email: { ...settings.email, host: '', port: 0, fromEmail: '' } } })
    const wrapper = mountPage(System)
    await flushPromises()
    await selectTab(wrapper, '通知管理')
    expect(wrapper.get('aside').text()).toContain('SMTP 邮件、Bark 推送')
    await button(wrapper, '发送测试邮件').trigger('click')
    expect(ElMessage.warning).toHaveBeenCalledWith('请先填写 SMTP 服务器、端口和发件人邮箱')

    await field(wrapper, 'SMTP 服务器').setValue(' smtp.local.test ')
    const port = field(wrapper, 'SMTP 端口')
    await port.setValue('465')
    await port.trigger('change')
    await field(wrapper, '发件人邮箱').setValue('sender@example.test')
    await button(wrapper, '发送测试邮件').trigger('click')
    await flushPromises()
    const email = wrapper.get('#settings-test-email input')
    expect(email.element.value).toBe('sender@example.test')
    await email.setValue('invalid')
    expect(email.element.validity.typeMismatch).toBe(true)
    await button(wrapper, '发送').trigger('click')
    await flushPromises()
    expect(api.sendTestEmail).not.toHaveBeenCalled()

    const sending = deferred()
    api.sendTestEmail.mockReturnValueOnce(sending.promise).mockResolvedValue({ data: {} })
    await email.setValue('admin@example.test')
    await wrapper.get('#settings-test-email').trigger('submit')
    await flushPromises()
    await wrapper.get('#settings-test-email').trigger('submit')
    expect(api.sendTestEmail).toHaveBeenCalledExactlyOnceWith({
      ...settings.email,
      host: 'smtp.local.test',
      port: 465,
      fromEmail: 'sender@example.test',
      toEmail: 'admin@example.test'
    })
    sending.reject(new Error('SMTP unavailable'))
    await flushPromises()
    expect(wrapper.get('#settings-test-email [role="alert"]').text()).toContain('发送失败')
    await wrapper.get('#settings-test-email').trigger('submit')
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledWith('测试邮件已发送')
    await button(wrapper, '发送测试邮件').trigger('click')
    await flushPromises()
    await wrapper.get('.el-dialog__headerbtn').trigger('click')
    await flushPromises()
    expect(wrapper.find('#settings-test-email').exists() && wrapper.get('#settings-test-email').isVisible()).toBe(false)
    await button(wrapper, '发送测试邮件').trigger('click')
    await flushPromises()
    await button(wrapper, '取消').trigger('click')
    await flushPromises()
    expect(wrapper.find('#settings-test-email').exists() && wrapper.get('#settings-test-email').isVisible()).toBe(false)
  })

  it('loads, tests, saves and clears Bark notification settings', async () => {
    const savedBark = { serverUrl: 'https://api.day.app', deviceKey: 'saved-device-key' }
    api.getSystemConfig.mockResolvedValue({ data: { ...settings, bark: savedBark } })
    const wrapper = mountPage(System)
    await flushPromises()
    await selectTab(wrapper, '通知管理')

    const serverUrl = field(wrapper, 'Bark 服务器地址')
    const deviceKey = field(wrapper, 'Bark 设备密钥')
    const testButton = button(wrapper, '发送测试推送')
    expect(serverUrl.element.value).toBe(savedBark.serverUrl)
    expect(deviceKey.element.value).toBe(savedBark.deviceKey)
    expect(deviceKey.attributes('type')).toBe('password')
    await wrapper.get('button[aria-label="显示 Bark 设备密钥"]').trigger('click')
    expect(deviceKey.attributes('type')).toBe('text')
    await wrapper.get('button[aria-label="隐藏 Bark 设备密钥"]').trigger('click')
    expect(deviceKey.attributes('type')).toBe('password')

    await serverUrl.setValue('https://bark.example.test?')
    await serverUrl.trigger('blur')
    expect(serverUrl.attributes('aria-invalid')).toBe('true')
    await testButton.trigger('click')
    expect(api.sendTestBark).not.toHaveBeenCalled()

    await serverUrl.setValue('  http://bark.local:8080/notifications  ')
    await deviceKey.setValue('')
    await button(wrapper, '保存设置').trigger('click')
    await vi.waitFor(() => expect(wrapper.text()).toContain('请输入 Bark 设备密钥'))
    expect(api.updateSystemConfig).not.toHaveBeenCalled()

    await deviceKey.setValue('  current-device-key  ')
    const sending = deferred()
    api.sendTestBark.mockReturnValueOnce(sending.promise).mockResolvedValue({ data: {} })
    await testButton.trigger('click')
    await testButton.trigger('click')
    const bark = { serverUrl: 'http://bark.local:8080/notifications', deviceKey: 'current-device-key' }
    expect(api.sendTestBark).toHaveBeenCalledExactlyOnceWith(bark)
    expect(api.updateSystemConfig).not.toHaveBeenCalled()
    sending.reject({ response: { data: { message: 'Bark 服务器拒绝推送' } } })
    await flushPromises()
    expect(wrapper.findAll('[role="alert"]').map((alert) => alert.text())).toContain('Bark 服务器拒绝推送')
    await testButton.trigger('click')
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledWith('Bark 测试推送已发送')
    await vi.waitFor(() => expect(wrapper.find('[role="alert"]').exists()).toBe(false))

    await button(wrapper, '保存设置').trigger('click')
    await flushPromises()
    expect(api.updateSystemConfig).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ bark, email: settings.email }))
    await serverUrl.setValue('')
    await deviceKey.setValue('')
    await button(wrapper, '保存设置').trigger('click')
    await flushPromises()
    expect(api.updateSystemConfig).toHaveBeenLastCalledWith(expect.objectContaining({ bark: { serverUrl: '', deviceKey: '' } }))
  })

  it('asks for confirmation before backing up and allows retry after failure', async () => {
    const wrapper = mountPage(System)
    await flushPromises()
    await selectTab(wrapper, '数据库备份')
    expect(wrapper.get('aside').text()).toContain('面板内备份')
    ElMessageBox.confirm.mockRejectedValueOnce('cancel').mockResolvedValue()
    await button(wrapper, '立即备份').trigger('click')
    await flushPromises()
    expect(api.backupSystem).not.toHaveBeenCalled()

    const backingUp = deferred()
    api.backupSystem.mockReturnValueOnce(backingUp.promise).mockResolvedValue({ data: {} })
    await button(wrapper, '立即备份').trigger('click')
    await flushPromises()
    expect(button(wrapper, '保存设置').element.disabled).toBe(true)
    backingUp.reject(new Error('backup failed'))
    await flushPromises()
    expect(button(wrapper, '保存设置').element.disabled).toBe(false)
    await button(wrapper, '立即备份').trigger('click')
    await flushPromises()
    expect(api.backupSystem).toHaveBeenCalledTimes(2)
    expect(ElMessage.success).toHaveBeenCalledWith('数据库备份已创建')
  })

  it('displays database-managed backups and switches to the Gost settings', async () => {
    api.getSystemConfig.mockResolvedValue({ data: { ...settings, backup: { ...settings.backup, supported: false } } })
    const wrapper = mountPage(System)
    await flushPromises()
    await selectTab(wrapper, '数据库备份')
    expect(wrapper.text()).toContain('由数据库服务管理备份')
    expect(wrapper.get('aside').text()).toContain('管理方式数据库服务')
    expect(wrapper.findAll('button').some((item) => item.text() === '立即备份')).toBe(false)

    await selectTab(wrapper, 'Gost IP 白名单')
    expect(wrapper.get('aside').text()).toContain('白名单同步')
    expect(api.getGostAllowlist).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('button').some((item) => item.text() === '保存设置')).toBe(false)
    await selectTab(wrapper, 'Clash 规则集')
    expect(wrapper.get('aside').text()).toContain('bypass: yagp-transparent-proxy')
    expect(api.getGostRuleset).toHaveBeenCalledTimes(1)
  })
})

describe('Gost routing settings', () => {
  it('renders saved IPs, disables new nodes without addresses and clears the allowlist', async () => {
    const initial = deferred()
    api.getGostAllowlist.mockReturnValueOnce(initial.promise)
    const wrapper = mountPage(GostAllowlistSettings)
    expect(wrapper.get('[aria-label="正在读取 Gost IP 白名单"]').attributes('role')).toBe('status')
    initial.resolve({ data: { node_ids: [1, 2], addresses: ['192.0.2.1', '2001:db8::1'] } })
    await flushPromises()
    expect(wrapper.findAll('code').map((item) => item.text())).toEqual(['192.0.2.1', '2001:db8::1'])
    const options = wrapper.findAllComponents(ElOption)
    expect(options.map((option) => [option.props('value'), option.props('label'), option.props('disabled')])).toEqual([
      [1, 'Dual stack（192.0.2.1 / 2001:db8::1）', false],
      [2, 'Lost IP（未获取 IP）', false],
      [3, 'Unregistered（未获取 IP）', true],
      [4, 'IPv4 only（192.0.2.4）', false]
    ])
    wrapper.getComponent(ElSelect).vm.$emit('update:modelValue', [])
    await nextTick()
    const saving = deferred()
    api.updateGostAllowlist.mockReturnValueOnce(saving.promise)
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(api.updateGostAllowlist).toHaveBeenCalledExactlyOnceWith([])
    saving.resolve({ data: {} })
    await flushPromises()
    expect(wrapper.text()).toContain('未限制来源 IP')
    expect(ElMessage.success).toHaveBeenCalledWith('IP 白名单已保存，面板将同步到 Gost Relay')
  })

  it('retries allowlist loading and focuses a server error after submission', async () => {
    api.getGostAllowlist.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountPage(GostAllowlistSettings)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('无法读取白名单或节点列表')
    await button(wrapper, '重试').trigger('click')
    await flushPromises()
    wrapper.getComponent(ElSelect).vm.$emit('update:modelValue', [4])
    await nextTick()
    api.updateGostAllowlist
      .mockRejectedValueOnce({ response: { data: { message: 'IP 地址已失效' } } })
      .mockResolvedValueOnce({ data: { node_ids: [4], addresses: ['192.0.2.4'] } })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const alert = wrapper.get('[role="alert"]')
    expect(alert.text()).toContain('IP 地址已失效')
    expect(document.activeElement).toBe(alert.element)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateGostAllowlist).toHaveBeenLastCalledWith([4])
    expect(wrapper.get('code').text()).toBe('192.0.2.4')
  })

  it('caps the Clash preview and saves the trimmed URL and selected nodes', async () => {
    const matchers = Array.from({ length: 22 }, (_, index) => `domain-${index}.test`)
    api.getGostRuleset.mockResolvedValueOnce({
      data: { node_ids: [1], url: 'https://example.test/old.yaml', matchers, updated_at: '2026-11-08T02:00:00Z' }
    })
    const wrapper = mountPage(GostRulesetSettings)
    expect(wrapper.get('[aria-label="正在读取 Clash 规则集订阅"]').attributes('role')).toBe('status')
    await flushPromises()
    expect(wrapper.get('summary').text()).toBe('预览前 20 条规则')
    expect(wrapper.get('pre').text().split('\n')).toEqual(matchers.slice(0, 20))
    expect(wrapper.get('.ruleset-preview').text()).toContain('22 条 Gost 匹配规则，更新时间')
    expect(wrapper.findAllComponents(ElOption).map((option) => option.props('label'))).toContain('Lost IP（待获取地址）')

    await wrapper.get('#gost-ruleset-url').setValue('  https://example.test/new.yaml  ')
    wrapper.getComponent(ElSelect).vm.$emit('update:modelValue', [4])
    await nextTick()
    const saving = deferred()
    api.updateGostRuleset.mockReturnValueOnce(saving.promise)
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(api.updateGostRuleset).toHaveBeenCalledExactlyOnceWith('https://example.test/new.yaml', [4])
    saving.resolve({ data: { node_ids: [4], url: 'https://example.test/new.yaml', matchers: ['new.test'] } })
    await flushPromises()
    expect(wrapper.get('summary').text()).toBe('预览匹配规则')
    expect(wrapper.get('pre').text()).toBe('new.test')
    expect(ElMessage.success).toHaveBeenCalledWith('规则集订阅已保存')
  })

  it('reports rule subscription load and save failures and permits disabling it', async () => {
    api.getGostRuleset.mockRejectedValueOnce({ response: { data: { message: '订阅服务不可用' } } })
    const wrapper = mountPage(GostRulesetSettings)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('订阅服务不可用')
    await button(wrapper, '重试').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('暂无已转换的规则')
    api.updateGostRuleset.mockRejectedValueOnce(new Error('network offline')).mockResolvedValue({ data: {} })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const alert = wrapper.get('[role="alert"]')
    expect(alert.text()).toContain('无法保存规则集订阅')
    expect(document.activeElement).toBe(alert.element)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateGostRuleset).toHaveBeenLastCalledWith('', [])
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })
})
