<template>
  <header class="detail-header">
    <div class="detail-identity">
      <div class="detail-meta">
        <span class="ops-status" :class="'is-' + (nodeStates[node.status]?.tone || 'muted')">{{ nodeStates[node.status]?.label || '未知' }}</span>
        <span class="detail-chip ops-mono">#{{ node.id }}</span>
        <NodeRegionFlag v-if="node.region" :region="node.region" />
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
      <header class="ops-panel-header">
        <h2 class="ops-section-title" id="node-network-title"><el-icon aria-hidden="true"><Connection /></el-icon>IP 信息</h2>
      </header>
      <dl class="ops-panel-details">
        <div v-for="item in endpoints" :key="item.label">
          <dt>{{ item.label }}</dt>
          <dd class="ops-mono">{{ item.value }}</dd>
        </div>
        <div v-if="node.address_mappings?.length" class="detail-address-list">
          <dt>本机监听 IP → 对外连接 IP</dt>
          <dd v-for="mapping in node.address_mappings" :key="mapping.listen_ip" class="ops-mono">
            {{ mapping.listen_ip }} → {{ mapping.connect_ip }}
          </dd>
        </div>
      </dl>
    </section>
    <NodeGostService :key="node.id" :node="node" class="ops-panel ops-panel-body" @upgrade="$emit('action', 'upgrade-gost')" />
  </div>

  <section class="ops-panel ops-panel-body detail-traffic" aria-labelledby="node-traffic-title">
    <h2 class="ops-section-title" id="node-traffic-title"><el-icon aria-hidden="true"><PieChart /></el-icon>流量统计与配额</h2>
    <p class="ops-mono">{{ formatNodeTrafficUsage(node) }}</p>
  </section>
</template>

<script setup>
import { computed } from 'vue'
import { Connection, Download, PieChart, Setting } from '@element-plus/icons-vue'
import NodeGostService from '@/components/NodeGostService.vue'
import NodeRegionFlag from '@/components/NodeRegionFlag.vue'
import { formatNodeTrafficUsage, nodeIPs, nodeStates } from '@/utils/node-display'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['action'])

const endpoints = computed(() => [
  { label: '节点 IP', value: props.node.address || '待获取地址' },
  { label: 'IPv4', value: nodeIPs(props.node, 4).join(' · ') || '不支持' },
  { label: 'IPv6', value: nodeIPs(props.node, 6).join(' · ') || '不支持' }
])
</script>

<style scoped>
.detail-header, .detail-meta, .detail-buttons {
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
.detail-meta { gap: 8px 12px; }
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
.detail-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 440px), 1fr));
  gap: var(--ops-section-gap);
}
.detail-cards > .ops-panel { display: grid; align-content: start; gap: var(--ops-section-gap); }
.detail-address-list { grid-column: 1 / -1; }
.detail-traffic { display: grid; min-width: 0; gap: var(--ops-section-gap); }
.detail-traffic h2 {
  display: flex;
  align-items: center;
  gap: 8px;
}
.detail-traffic h2 .el-icon { color: var(--ops-accent); }
</style>
