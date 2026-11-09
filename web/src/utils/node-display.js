import { CircleCheck, CircleClose, Warning } from '@element-plus/icons-vue'

export const nodeStates = {
  online: { label: '在线', icon: CircleCheck, tone: 'success' },
  offline: { label: '离线', icon: CircleClose },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}

export const trafficResetPeriods = { none: '不重置', daily: '每日重置', weekly: '每周重置', monthly: '每月重置' }
export const trafficQuotaStates = {
  active: { label: '配额生效', tone: 'is-success' },
  blocked: { label: '已达上限', tone: 'is-danger' },
  pending: { label: '待同步', tone: 'is-muted' },
  unsupported: { label: '需升级', tone: 'is-warning' }
}

const dateFormatter = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
export const formatTime = (value) => dateFormatter.format(new Date(value))

export const formatBytes = (bytes) => {
  if (!bytes) return '0 B'
  const sizes = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizes.length - 1)
  return Number((bytes / 1024 ** index).toFixed(2)) + ' ' + sizes[index]
}
