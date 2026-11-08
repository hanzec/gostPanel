import request from '@/utils/request'
import { getNodeList } from '@/api/node'

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

export function backupSystem() {
    return request({
        url: '/system/backup',
        method: 'post'
    })
}

export function getGostAllowlist() {
    return request.get('/system/gost-allowlist')
}

export function updateGostAllowlist(nodeIds) {
    return request.put('/system/gost-allowlist', { node_ids: nodeIds })
}

export async function getRoutingNodes() {
    const nodes = []
    const pageSize = 100
    for (let page = 1; ; page++) {
        const { data } = await getNodeList({ page, pageSize })
        const list = data.list || []
        nodes.push(...list)
        if (nodes.length >= data.total || list.length < pageSize) return nodes
    }
}
