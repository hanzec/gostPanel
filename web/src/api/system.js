import request from '@/utils/request'

/**
 * 获取系统配置
 */
export function getSystemConfig() {
    return request({
        url: '/system/config',
        method: 'get'
    })
}

/**
 * 获取公开系统配置
 */
export function getPublicSystemConfig() {
    return request({
        url: '/system/public-config',
        method: 'get'
    })
}

/**
 * 更新系统配置
 */
export function updateSystemConfig(data) {
    return request({
        url: '/system/config',
        method: 'put',
        data
    })
}

export function sendTestEmail(data) {
    return request({
        url: '/system/email/test',
        method: 'post',
        data
    })
}

export function sendTestBark(data) {
    return request.post('/system/bark/test', data, { silentError: true })
}

export function backupSystem() {
    return request({
        url: '/system/backup',
        method: 'post'
    })
}

export function getGostRuleset() {
    return request.get('/system/gost-ruleset')
}

export function getGostRulesets() {
    return request.get('/system/gost-rulesets')
}

export function updateGostRuleset(url, nodeIds) {
    return request.put('/system/gost-ruleset', { url, node_ids: nodeIds }, { timeout: 25000 })
}

export function createGostRuleset(data) {
    return request.post('/system/gost-rulesets', data, { timeout: 25000, silentError: true })
}

export function updateNamedGostRuleset(id, data) {
    return request.put(`/system/gost-rulesets/${id}`, data, { timeout: 25000, silentError: true })
}

export function deleteGostRuleset(id) {
    return request.delete(`/system/gost-rulesets/${id}`, { silentError: true })
}
