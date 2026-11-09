<template>
  <header class="detail-header">
    <div class="detail-identity">
      <div class="detail-title">
        <h1>{{ node.name }}</h1>
        <span class="ops-status" :class="'is-' + (nodeStates[node.status]?.tone || 'muted')">{{ nodeStates[node.status]?.label || '未知' }}</span>
        <span class="detail-chip ops-mono">#{{ node.id }}</span>
        <span v-if="node.region" class="detail-chip"><el-icon aria-hidden="true"><Location /></el-icon>{{ formatRegion(node.region) }}</span>
      </div>
      <p v-if="node.remark" class="ops-muted">{{ node.remark }}</p>
    </div>
    <div class="detail-buttons">
      <el-button :icon="Setting" @click="$emit('action', 'config')">配置</el-button>
      <el-button type="primary" :icon="Download" @click="$emit('action', 'install')">安装</el-button>
    </div>
  </header>

  <div class="ops-panel detail-overview">
    <section class="overview-section" aria-labelledby="node-network-title">
      <h2 id="node-network-title"><el-icon aria-hidden="true"><Connection /></el-icon>网络与连接信息</h2>
      <dl class="detail-endpoints">
        <div v-for="item in endpoints" :key="item.label">
          <dt>{{ item.label }}</dt>
          <dd class="ops-mono">{{ item.value }}</dd>
        </div>
      </dl>
      <dl class="detail-check">
        <dt>最近检查</dt>
        <dd><time v-if="node.last_check_at" :datetime="node.last_check_at" class="ops-mono">{{ formatTime(node.last_check_at) }}</time><span v-else>待检查</span></dd>
      </dl>
    </section>

    <section class="overview-section" aria-labelledby="node-traffic-title">
      <div class="section-title">
        <h2 id="node-traffic-title"><el-icon aria-hidden="true"><PieChart /></el-icon>流量统计与配额</h2>
        <span v-if="hasQuota" class="detail-chip">{{ trafficResetPeriods[node.traffic_reset_period] || trafficResetPeriods.none }}</span>
      </div>
      <dl class="detail-transfer">
        <div><dt>累计流量</dt><dd class="ops-mono">{{ formatBytes(node.total_bytes) }}</dd></div>
        <div><dt>入站</dt><dd class="ops-mono">{{ formatBytes(node.input_bytes) }}</dd></div>
        <div><dt>出站</dt><dd class="ops-mono">{{ formatBytes(node.output_bytes) }}</dd></div>
      </dl>
      <div class="detail-quota">
        <div class="quota-line">
          <span class="ops-muted">{{ hasQuota && quotaSynced ? '配额用量' : '流量上限' }}</span>
          <span v-if="hasQuota" class="ops-mono">
            <template v-if="quotaSynced">{{ formatBytes(node.traffic_quota_used_bytes) }} / </template>{{ formatBytes(node.traffic_limit_bytes) }}
          </span>
          <span v-else>未设上限</span>
        </div>
        <progress
          v-if="hasQuota && quotaSynced"
          aria-label="流量配额用量"
          :class="{ 'quota-blocked': node.traffic_quota_state === 'blocked' }"
          :max="node.traffic_limit_bytes"
          :value="Math.min(node.traffic_quota_used_bytes || 0, node.traffic_limit_bytes)"
        ></progress>
        <span v-if="hasQuota" class="ops-status" :class="quotaState.tone">{{ quotaState.label }}</span>
        <span v-else-if="node.traffic_quota_state" class="ops-muted">解除限制待同步</span>
      </div>
    </section>
  </div>

  <section class="detail-maintenance" aria-labelledby="node-maintenance-title">
    <h2 id="node-maintenance-title">运维操作</h2>
    <div class="maintenance-grid">
      <button
        v-for="action in actions"
        :key="action.key"
        type="button"
        class="ops-panel maintenance-card"
        :aria-label="action.title"
        :aria-describedby="'node-action-hint-' + action.key"
        @click="$emit('action', action.key)"
      >
        <el-icon class="maintenance-icon" aria-hidden="true"><component :is="action.icon" /></el-icon>
        <span class="maintenance-copy"><strong>{{ action.title }}</strong><small :id="'node-action-hint-' + action.key">{{ action.description }}</small></span>
        <el-icon class="maintenance-arrow" aria-hidden="true"><ArrowRight /></el-icon>
      </button>
    </div>
  </section>

</template>

<script setup>
import { computed } from 'vue'
import { ArrowRight, Connection, Download, Location, Lock, Monitor, PieChart, Setting } from '@element-plus/icons-vue'
import { formatBytes, formatRegion, formatTime, nodeIPs, nodeStates, trafficQuotaStates, trafficResetPeriods } from '@/utils/node-display'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['action'])

const actions = [
  { key: 'gost', title: 'Gost 服务', description: '查看运行状态、日志和重启服务', icon: Monitor },
  { key: 'upgrade', title: '版本与升级', description: '检查稳定版本并管理自动升级', icon: Download },
  { key: 'nft', title: 'nft 配置', description: '编辑并应用 nftables 规则', icon: Lock }
]
const endpoints = computed(() => [
  { label: '节点 IP', value: props.node.address || '待获取地址' },
  { label: 'IPv4', value: nodeIPs(props.node, 4).join(' · ') || '未上报' },
  { label: 'IPv6', value: nodeIPs(props.node, 6).join(' · ') || '不支持' }
])
const hasQuota = computed(() => props.node.traffic_limit_bytes > 0)
const quotaSynced = computed(() => ['active', 'blocked'].includes(props.node.traffic_quota_state))
const quotaState = computed(() => trafficQuotaStates[props.node.traffic_quota_state] || trafficQuotaStates.pending)
</script>

<style scoped>
.detail-header, .detail-title, .detail-buttons, .section-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.detail-header {
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--ops-border);
}
.detail-identity { min-width: 0; }
.detail-identity p { margin-top: 6px; overflow-wrap: anywhere; }
.detail-title { gap: 8px 12px; }
.detail-title h1 { font-size: clamp(25px, 3vw, 36px); line-height: 1.2; overflow-wrap: anywhere; }
.detail-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 7px;
  border: 1px solid var(--ops-border-strong);
  border-radius: 5px;
  background: var(--ops-inset);
  color: var(--ops-muted);
  font-size: 12px;
}
.detail-buttons .el-button { min-height: 44px; margin: 0; }
.detail-overview { display: grid; grid-template-columns: 7fr 5fr; padding: 20px; }
.overview-section { min-width: 0; display: flex; flex-direction: column; gap: 12px; }
.overview-section:first-child { padding-inline-end: 20px; }
.overview-section + .overview-section { padding-inline-start: 20px; border-inline-start: 1px solid var(--ops-border); }
.section-title { justify-content: space-between; }
.detail-overview h2, .detail-maintenance h2 {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 1rem;
}
.overview-section h2 .el-icon { color: var(--ops-accent); }
.detail-overview dl { margin: 0; min-width: 0; }
.detail-overview dt { color: var(--ops-muted); font-size: 13px; }
.detail-overview dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
.detail-endpoints { display: grid; gap: 6px; }
.detail-endpoints > div, .detail-check, .quota-line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.detail-endpoints > div {
  border: 1px solid var(--ops-border);
  border-radius: 7px;
  background: var(--ops-inset);
  padding: 7px 10px;
}
.detail-endpoints dt, .detail-check dt { flex: none; }
.detail-endpoints dd, .detail-check dd { text-align: end; }
.detail-check { padding: 4px 10px 0; }
.detail-transfer {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
}
.detail-transfer dd { margin-top: 4px; font-weight: 650; }
.detail-quota { display: grid; justify-items: start; gap: 7px; font-size: 13px; }
.quota-line { flex-wrap: wrap; width: 100%; }
.detail-quota progress {
  display: block;
  width: 100%;
  height: 8px;
  overflow: hidden;
  appearance: none;
  border: 0;
  border-radius: 4px;
  background: var(--ops-inset);
  color: var(--ops-accent);
}
.detail-quota progress.quota-blocked { color: var(--ops-danger); }
.detail-quota progress::-webkit-progress-bar { background: var(--ops-inset); }
.detail-quota progress::-webkit-progress-value { background: currentColor; }
.detail-quota progress::-moz-progress-bar { background: currentColor; }
.detail-maintenance { display: grid; gap: 10px; }
.maintenance-grid { display: grid; gap: 12px; grid-template-columns: repeat(3, minmax(0, 1fr)); }
.maintenance-card {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 78px;
  padding: 12px 14px;
  color: var(--ops-text);
  text-align: start;
}
.maintenance-card:hover, .maintenance-card:focus-visible { border-color: var(--ops-accent-line); background: var(--ops-raised); }
.maintenance-card:active { background: var(--ops-accent-bg); }
.maintenance-icon {
  width: 38px;
  height: 38px;
  flex: 0 0 38px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
  color: var(--ops-accent);
  font-size: 18px;
}
.maintenance-copy { display: grid; min-width: 0; flex: 1; gap: 2px; }
.maintenance-copy small { color: var(--ops-muted); font-size: 13px; }
.maintenance-arrow { color: var(--ops-muted); }

@media (max-width: 760px) {
  .detail-overview, .maintenance-grid { grid-template-columns: minmax(0, 1fr); }
  .detail-overview { gap: 14px; padding: 14px; }
  .overview-section:first-child { padding-inline-end: 0; }
  .overview-section + .overview-section { padding: 14px 0 0; border-inline-start: 0; border-top: 1px solid var(--ops-border); }
  .maintenance-card { min-height: 64px; }
}
</style>
