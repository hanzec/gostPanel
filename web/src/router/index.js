import { createRouter, createWebHashHistory } from 'vue-router'

// 路由配置
const routes = [
    {
        path: '/',
        name: 'Layout',
        component: () => import('@/views/Layout.vue'),
        redirect: '/dashboard',
        children: [
            {
                path: 'dashboard',
                name: 'Dashboard',
                component: () => import('@/views/Dashboard.vue'),
                meta: { title: '运行总览', icon: 'Odometer' }
            },
            {
                path: 'nodes',
                name: 'Nodes',
                component: () => import('@/views/Nodes.vue'),
                meta: { title: '节点管理', icon: 'Monitor' }
            },
            {
                path: 'rules',
                name: 'Rules',
                component: () => import('@/views/Rules.vue'),
                meta: { title: '规则管理', icon: 'Switch' }
            },
            {
                path: 'tunnels',
                name: 'Tunnels',
                component: () => import('@/views/Tunnels.vue'),
                meta: { title: '隧道管理', icon: 'Connection' }
            },
            {
                path: 'logs',
                name: 'Logs',
                component: () => import('@/views/Logs.vue'),
                meta: { title: '操作日志', icon: 'Document' }
            },
            {
                path: 'system',
                name: 'System',
                component: () => import('@/views/System.vue'),
                meta: { title: '系统设置', icon: 'Setting' }
            }
        ]
    },
    {
        path: '/:pathMatch(.*)*',
        redirect: '/'
    }
]

const router = createRouter({
    history: createWebHashHistory(),
    routes
})

export default router
