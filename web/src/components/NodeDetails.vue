<template>
  <header class="detail-header">
    <div class="detail-identity">
      <div class="detail-title">
        <h2>{{ node.name }}</h2>
        <span class="ops-status" :class="'is-' + (nodeStates[node.status]?.tone || 'muted')">{{ nodeStates[node.status]?.label || '未知' }}</span>
        <span class="detail-chip ops-mono">#{{ node.id }}</span>
        <span v-if="node.region" class="detail-chip"><el-icon aria-hidden="true"><Location /></el-icon>{{ formatRegion(node.region) }}</span>
      </div>
      <p v-if="node.remark" class="ops-muted">{{ node.remark }}</p>
    </div>
    <div class="detail-buttons">
      <el-button :icon="Setting" @click="$emit('action', 'config')">配置</el-button>
      <el-button v-if="node.status !== 'online'" type="primary" :icon="Download" @click="$emit('action', 'install')">安装</el-button>
    </div>
  </header>

  <div class="detail-cards">
    <section class="ops-panel ops-panel-body detail-overview" aria-labelledby="node-network-title">
      <h2 class="ops-section-title" id="node-network-title"><el-icon aria-hidden="true"><Connection /></el-icon>IP 信息</h2>
      <div class="detail-network">
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
      </div>
    </section>
    <NodeGostService :key="node.id" :node="node" class="ops-panel ops-panel-body" />
  </div>

  <section class="ops-panel ops-panel-body detail-traffic" aria-labelledby="node-traffic-title">
    <div class="section-title">
      <h2 class="ops-section-title" id="node-traffic-title"><el-icon aria-hidden="true"><PieChart /></el-icon>流量统计与配额</h2>
      <span v-if="hasQuota" class="detail-chip">{{ trafficResetPeriods[node.traffic_reset_period] || trafficResetPeriods.none }}</span>
    </div>
    <div class="detail-quota">
      <dl class="quota-line">
        <div><dt>本期用量</dt><dd class="ops-mono">{{ formatGigabytes(currentUsage) }}</dd></div>
        <div v-if="hasQuota"><dt>流量上限</dt><dd class="ops-mono">{{ formatGigabytes(node.traffic_limit_bytes) }}</dd></div>
      </dl>
      <progress
        v-if="currentUsage != null"
        aria-label="流量配额用量"
        :class="{ 'quota-blocked': node.traffic_quota_state === 'blocked' }"
        :max="node.traffic_limit_bytes"
        :value="Math.min(currentUsage, node.traffic_limit_bytes)"
      ></progress>
      <span v-if="hasQuota" class="ops-status" :class="quotaState.tone">{{ quotaState.label }}</span>
      <span v-else-if="node.traffic_quota_state" class="ops-muted">解除限制待同步</span>
      <span v-else class="ops-muted">未设上限</span>
    </div>
  </section>

  <section class="ops-panel ops-panel-body detail-upgrade" aria-labelledby="node-upgrade-title">
    <h2 class="ops-section-title" id="node-upgrade-title"><el-icon aria-hidden="true"><Download /></el-icon>版本与升级</h2>
    <el-button @click="$emit('action', 'upgrade')">管理升级</el-button>
  </section>
</template>

<script setup>
import { computed } from 'vue'
import { Connection, Download, Location, PieChart, Setting } from '@element-plus/icons-vue'
import NodeGostService from '@/components/NodeGostService.vue'
import { formatGigabytes, formatRegion, formatTime, nodeIPs, nodeStates, nodeTrafficQuotaState, trafficResetPeriods } from '@/utils/node-display'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['action'])

const endpoints = computed(() => [
  { label: '节点 IP', value: props.node.address || '待获取地址' },
  { label: 'IPv4', value: nodeIPs(props.node, 4).join(' · ') || '未上报' },
  { label: 'IPv6', value: nodeIPs(props.node, 6).join(' · ') || '不支持' },
  ...(props.node.manual_addresses?.length ? [{ label: '补充 IP', value: props.node.manual_addresses.join(' · ') }] : [])
])
const hasQuota = computed(() => props.node.traffic_limit_bytes > 0)
const currentUsage = computed(() => props.node.traffic_current_period_used_bytes)
const quotaState = computed(() => nodeTrafficQuotaState(props.node))
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
.detail-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 7px;
  border: 1px solid var(--ops-border-strong);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-muted);
  font-size: var(--ops-text-xs);
}
.detail-buttons .el-button { min-height: 44px; margin: 0; }
.detail-cards { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: var(--ops-section-gap); }
.detail-overview { display: grid; min-width: 0; gap: var(--ops-section-gap); }
.detail-network, .detail-traffic { display: grid; min-width: 0; gap: var(--ops-section-gap); }
.section-title { justify-content: space-between; }
.detail-overview h2, .detail-traffic h2, .detail-upgrade h2 {
  display: flex;
  align-items: center;
  gap: 8px;
}
.detail-overview h2 .el-icon, .detail-traffic h2 .el-icon, .detail-upgrade h2 .el-icon { color: var(--ops-accent); }
.detail-network dl, .detail-traffic dl { margin: 0; min-width: 0; }
.detail-network dt, .detail-traffic dt { color: var(--ops-muted); font-size: var(--ops-text-sm); }
.detail-network dd, .detail-traffic dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
.detail-endpoints { display: grid; gap: 6px; }
.detail-endpoints > div, .detail-check, .quota-line {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.detail-endpoints > div {
  padding-block: 8px;
  border-bottom: 1px solid var(--ops-border);
}
.detail-endpoints dt, .detail-check dt { flex: none; }
.detail-endpoints dd, .detail-check dd { text-align: end; }
.detail-check { padding-top: 4px; }
.detail-quota { display: grid; justify-items: start; gap: 7px; font-size: var(--ops-text-sm); }
.quota-line { flex-wrap: wrap; width: 100%; }
.quota-line dd { margin-top: 4px; font-weight: var(--ops-weight-semibold); }
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
.detail-upgrade {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}

@media (max-width: 1100px) {
  .detail-cards { grid-template-columns: minmax(0, 1fr); }
  .detail-upgrade .el-button { min-height: 44px; }
}
</style>
