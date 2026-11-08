import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { markRaw } from 'vue'
import ElementPlus, { ElMessage } from 'element-plus'
import { Monitor } from '@element-plus/icons-vue'
import App from '@/App.vue'
import MetricStrip from '@/components/MetricStrip.vue'
import router from '@/router'
import { useSystemStore } from '@/store/system'
import request from '@/utils/request'

const adapter = vi.fn()
const reply = (config, data, code = 0, message = '') => ({
  status: 200, statusText: 'OK', headers: {}, config, data: { code, data, message }
})
const branding = { siteTitle: 'Edge 管理台', logoUrl: '/edge-logo.svg', copyright: '网络运维' }
const stats = {
  nodes: { total: 2, online: 1, offline: 1 },
  rules: { total: 4, running: 3, stopped: 1 },
  tunnels: { total: 1, running: 1, stopped: 0 },
  database: 'sqlite', version: '1.0.0'
}
let wrapper

beforeEach(() => {
  localStorage.clear()
  document.title = 'yagp'
  setActivePinia(createPinia())
  adapter.mockReset().mockImplementation(config => {
    const payloads = {
      '/auth/info': { username: 'operator' },
      '/system/public-config': branding,
      '/dashboard/stats': stats,
      '/logs': { list: [], total: 0 }
    }
    if (!(config.url in payloads)) throw new Error('unexpected request: ' + config.url)
    return Promise.resolve(reply(config, payloads[config.url]))
  })
  request.defaults.adapter = adapter
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  document.querySelector('#app')?.__vue_app__?.unmount()
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('console routing and Authentik gate', () => {
  const drawer = {
    props: ['modelValue'],
    template: '<aside v-if="modelValue" aria-label="管理导航"><slot /></aside>'
  }

  it('waits for identity, recovers after a rejection, and then opens the requested page', async () => {
    await router.push('/')
    let answer
    adapter.mockImplementationOnce(config => new Promise(resolve => {
      answer = () => resolve(reply(config, null, 401, '需要登录'))
    }))
    vi.spyOn(ElMessage, 'error').mockImplementation(() => {})
    wrapper = mount(App, {
      attachTo: document.body,
      global: { plugins: [router, createPinia(), ElementPlus], stubs: { ElDrawer: drawer } }
    })

    expect(wrapper.get('[role="status"]').text()).toContain('正在确认管理员身份')
    expect(wrapper.get('.console-brand__mark img').attributes('src')).toBe('/favicon.svg')
    expect(adapter.mock.calls.map(([config]) => config.url)).toEqual(['/auth/info'])
    answer()
    await flushPromises()
    const identityError = wrapper.get('[role="alert"]')
    expect(identityError.text()).toContain('无法确认管理员身份')
    expect(identityError.get('img').attributes('src')).toBe('/favicon.svg')
    expect(wrapper.find('.overview-banner').exists()).toBe(false)

    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.get('.console-identity strong').text()).toBe('operator')
    expect(wrapper.get('a[aria-label="退出 Authentik"]').attributes('href')).toBe('/outpost.goauthentik.io/sign_out')
    expect(wrapper.get('h1').text()).toBe('运行总览')
    expect(wrapper.get('.console-footer').text()).toContain(branding.copyright)
    expect(document.title).toBe(branding.siteTitle)

    const logo = wrapper.get('.console-brand__mark')
    const customLogo = logo.get(`img[src="${branding.logoUrl}"]`)
    expect(logo.find('img[src="/favicon.svg"]').exists()).toBe(true)
    expect(customLogo.classes()).not.toContain('is-loaded')
    await customLogo.trigger('load')
    expect(customLogo.classes()).toContain('is-loaded')
    expect(logo.find('img[src="/favicon.svg"]').exists()).toBe(false)
    await wrapper.get('a.console-skip').trigger('click')
    expect(document.activeElement.id).toBe('console-main')

    const openMenu = wrapper.get('button[aria-label="打开管理导航"]')
    await openMenu.trigger('click')
    expect(openMenu.attributes('aria-expanded')).toBe('true')
    await wrapper.get('aside[aria-label="管理导航"] a.console-brand').trigger('click')
    expect(openMenu.attributes('aria-expanded')).toBe('false')
    await openMenu.trigger('click')
    await wrapper.get('aside[aria-label="管理导航"] a[href="#/logs"]').trigger('click')
    expect(openMenu.attributes('aria-expanded')).toBe('false')
    await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/logs'))
  })

  it('loads every navigation route and redirects unknown URLs to the overview', async () => {
    for (const [path, title] of [
      ['/nodes', '节点管理'], ['/rules', '规则管理'], ['/tunnels', '隧道管理'],
      ['/logs', '操作日志'], ['/system', '系统设置']
    ]) {
      await router.push(path)
      expect(router.currentRoute.value.fullPath).toBe(path)
      expect(router.currentRoute.value.meta.title).toBe(title)
      expect(router.currentRoute.value.matched.map(route => route.name)).toHaveLength(2)
    }
    await router.push('/unknown-page')
    expect(router.currentRoute.value.fullPath).toBe('/dashboard')
  })

  it('boots the real entry point with the stored title while identity is pending', async () => {
    window.history.replaceState(null, '', '/#/dashboard')
    document.body.innerHTML = '<div id="app"></div>'
    localStorage.setItem('siteTitle', '缓存中的标题')
    adapter.mockImplementationOnce(() => new Promise(() => {}))
    await import('@/main.js')
    await router.isReady()
    await flushPromises()
    expect(document.querySelector('#app [role="status"]').textContent).toContain('正在确认管理员身份')
    expect(document.title).toBe('缓存中的标题')
  })
})

describe('branding and summary components', () => {
  it('uses sensible defaults and persists server branding', async () => {
    const store = useSystemStore()
    expect(store.siteTitle).toBe('yagp')
    expect(store.logoUrl).toBe('')
    expect(store.copyright).toBe('')
    await store.fetchSystemConfig()
    expect(localStorage.getItem('siteTitle')).toBe(branding.siteTitle)
    expect(localStorage.getItem('logoUrl')).toBe(branding.logoUrl)
    expect(localStorage.getItem('copyright')).toBe(branding.copyright)
    expect(document.title).toBe(branding.siteTitle)
  })

  it('uses the bundled logo instead of the historical default from cache or server', async () => {
    const historicalLogo = 'https://gost.run/images/gost.png'
    localStorage.setItem('logoUrl', historicalLogo)
    const store = useSystemStore()
    expect(store.logoUrl).toBe('')

    await store.fetchSystemConfig()
    expect(store.logoUrl).toBe(branding.logoUrl)

    adapter.mockImplementationOnce(config => Promise.resolve(reply(config, {
      ...branding, logoUrl: historicalLogo
    })))
    await store.fetchSystemConfig()
    expect(store.logoUrl).toBe('')
    expect(localStorage.getItem('logoUrl')).toBeNull()
  })

  it('preserves cached branding on errors and removes an omitted logo on refresh', async () => {
    localStorage.setItem('siteTitle', 'Cached title')
    localStorage.setItem('logoUrl', '/cached.svg')
    localStorage.setItem('copyright', 'Cached owner')
    const store = useSystemStore()
    expect(document.title).toBe('Cached title')
    expect(store.logoUrl).toBe('/cached.svg')
    expect(store.copyright).toBe('Cached owner')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(ElMessage, 'error').mockImplementation(() => {})
    adapter.mockRejectedValueOnce(new Error('offline'))
      .mockImplementationOnce(config => Promise.resolve(reply(config, null)))
      .mockImplementationOnce(config => Promise.resolve(reply(config, {})))
    await store.fetchSystemConfig()
    await store.fetchSystemConfig()
    expect(store.logoUrl).toBe('/cached.svg')
    await store.fetchSystemConfig()
    expect(store.logoUrl).toBe('')
    expect(localStorage.getItem('logoUrl')).toBeNull()
    expect(store.siteTitle).toBe('Cached title')
    expect(store.copyright).toBe('Cached owner')
  })

  it('distinguishes a zero metric from an unavailable value', () => {
    wrapper = mount(MetricStrip, {
      props: { label: '节点指标', items: [
        { label: '在线', value: 0, detail: '暂时没有在线节点', tone: 'warning', icon: markRaw(Monitor) },
        { label: '总数', value: null }
      ] },
      global: { plugins: [ElementPlus] }
    })
    expect(wrapper.get('dl').attributes('aria-label')).toBe('节点指标')
    expect(wrapper.findAll('.metric-strip-value').map(item => item.text())).toEqual(['0', '—'])
    expect(wrapper.get('.metric-strip-detail').text()).toBe('暂时没有在线节点')
    expect(wrapper.get('.metric-strip-item').classes()).toContain('warning')
  })
})
