import request from '@/utils/request'

/**
 * 获取当前 Authentik 管理员
 */
export function getUserInfo() {
    return request({
        url: '/auth/info',
        method: 'get'
    })
}
