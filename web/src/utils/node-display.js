import { CircleCheck, CircleClose, Warning } from '@element-plus/icons-vue'

export const nodeStates = {
  online: { label: '在线', icon: CircleCheck, tone: 'success' },
  offline: { label: '离线', icon: CircleClose },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}

export const trafficResetPeriods = { none: '不重置', daily: '每日重置', weekly: '每周重置', monthly: '每月重置' }

const dateFormatter = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
export const formatTime = (value) => dateFormatter.format(new Date(value))

const reportedNodeAddresses = (node) => node.addresses?.length ? node.addresses : [node.ipv4, node.ipv6].filter(Boolean)

export const nodeAddresses = (node) => [...new Set([...reportedNodeAddresses(node), ...(node.manual_addresses || [])])]

export const nodeListenAddresses = (node) => node?.local_addresses ?? (node ? [...new Set(reportedNodeAddresses(node))] : [])

export const nodeIPs = (node, family) => reportedNodeAddresses(node).filter(address => address.includes(':') === (family === 6))

export const formatNodeTrafficUsage = (node) => {
  const used = formatGigabytes(node.traffic_current_period_used_bytes)
  const limit = node.traffic_limit_bytes > 0 ? formatGigabytes(node.traffic_limit_bytes) : 'Unlimited'
  return `${used} / ${limit}`
}

function formatGigabytes(bytes) {
  if (bytes == null) return '未统计'
  const gigabytes = bytes / 1e9
  if (gigabytes > 0 && gigabytes < 0.01) return '<0.01 GB'
  return gigabytes.toFixed(2) + ' GB'
}

export const formatBytes = (bytes) => {
  if (!bytes) return '0 B'
  const sizes = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizes.length - 1)
  return Number((bytes / 1024 ** index).toFixed(2)) + ' ' + sizes[index]
}
