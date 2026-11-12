import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getPublicSystemConfig } from '@/api/system'

export const defaultLogoUrl = '/favicon.svg'
const previousDefaultLogoUrl = 'https://gost.run/images/gost.png'

function logoOverride(url) {
    return url === previousDefaultLogoUrl ? '' : (url || '')
}

export const useSystemStore = defineStore('system', () => {
    // State
    const siteTitle = ref(localStorage.getItem('siteTitle') || 'yagp')
    const logoUrl = ref(logoOverride(localStorage.getItem('logoUrl')))
    const copyright = ref(localStorage.getItem('copyright') || '')

    // Actions
    async function fetchSystemConfig() {
        try {
            const res = await getPublicSystemConfig()
            if (res.data) {
                const config = res.data

                // Update state
                if (config.siteTitle) {
                    siteTitle.value = config.siteTitle
                    document.title = config.siteTitle
                    localStorage.setItem('siteTitle', config.siteTitle)
                }

                logoUrl.value = logoOverride(config.logoUrl)
                if (logoUrl.value) {
                    localStorage.setItem('logoUrl', logoUrl.value)
                } else {
                    localStorage.removeItem('logoUrl')
                }

                if (config.copyright !== undefined) {
                    copyright.value = config.copyright || ''
                    if (copyright.value) {
                        localStorage.setItem('copyright', copyright.value)
                    } else {
                        localStorage.removeItem('copyright')
                    }
                }
            }
        } catch (error) {
            console.error('获取系统配置失败:', error)
        }
    }

    // Initialize document title from storage immediately
    if (localStorage.getItem('siteTitle')) {
        document.title = localStorage.getItem('siteTitle')
    }

    return {
        siteTitle,
        logoUrl,
        copyright,
        fetchSystemConfig
    }
})
