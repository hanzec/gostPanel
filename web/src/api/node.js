import request from '@/utils/request'

/**
 * 获取节点列表
 */
export function getNodeList(params) {
    return request({
        url: '/nodes',
        method: 'get',
        params
    })
}

/**
 * 获取节点详情
 */
export function getNode(id) {
    return request({
        url: `/nodes/${id}`,
        method: 'get',
        silentError: true
    })
}

/**
 * 创建节点
 */
export function createNode(data) {
    return request({
        url: '/nodes',
        method: 'post',
        data
    })
}

/**
 * 更新节点
 */
export function updateNode(id, data) {
    return request({
        url: `/nodes/${id}`,
        method: 'put',
        data
    })
}

/**
 * 签发一次性节点注册令牌
 */
export function issueNodeRegistrationToken(id) {
    return request({
        url: `/nodes/${id}/registration-token`,
        method: 'post',
        silentError: true
    })
}

/**
 * 删除节点
 */
export function deleteNode(id) {
    return request({
        url: `/nodes/${id}`,
        method: 'delete'
    })
}

/**
 * 获取节点配置
 */
export function getNodeConfig(id) {
    return request({
        url: `/nodes/${id}/config`,
        method: 'get'
    })
}

export function getGostService(id) {
    return request.get(`/nodes/${id}/gost/service`)
}

export function restartGostService(id) {
    return request.post(`/nodes/${id}/gost/restart`, null, { timeout: 25000 })
}

export function getGostLogs(id) {
    return request.get(`/nodes/${id}/gost/logs`)
}

export function getNodeNftConfig(id, loopbackPort) {
    if (loopbackPort) {
        return request.get(`/nodes/${id}/nft`, { params: { loopback_port: loopbackPort } })
    }
    return request.get(`/nodes/${id}/nft`)
}

export function applyNodeNftConfig(id, config) {
    return request.put(`/nodes/${id}/nft`, { config }, { timeout: 25000 })
}

export function getNodeUpgradeStatus(id) {
    return request.get(`/nodes/${id}/upgrade`, { silentError: true })
}

export function updateNodeUpgradePolicy(id, policy) {
    return request.put(`/nodes/${id}/upgrade`, policy, { silentError: true })
}

export function upgradeNodeComponent(id, component) {
    return request.post(`/nodes/${id}/upgrade/${component}`, null, { timeout: 25000, silentError: true })
}
