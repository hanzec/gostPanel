<template>
  <PanelDialog
    v-model="visible"
    title="隧道转发测试"
    class="tunnel-test-dialog"
    :close-on-click-modal="false"
    :close-on-press-escape="!loading"
    :show-close="!loading"
  >
    <div v-if="tunnel" class="tunnel-test-body">
      <section class="ops-inset tunnel-test-route" aria-labelledby="tunnel-test-name">
        <h3 id="tunnel-test-name" class="tunnel-test-name">
          <el-icon aria-hidden="true"><Connection /></el-icon>
          {{ tunnel.name }}
        </h3>
        <div class="tunnel-test-endpoints">
          <dl>
            <dt>源端</dt>
            <dd>{{ tunnel.entry_node?.name || '节点 #' + tunnel.entry_node_id }}</dd>
          </dl>
          <el-icon class="tunnel-test-route-arrow" aria-hidden="true"><Right /></el-icon>
          <dl>
            <dt>目的端</dt>
            <dd>{{ tunnel.exit_node?.name || '节点 #' + tunnel.exit_node_id }}</dd>
          </dl>
        </div>
      </section>

      <div role="status" aria-atomic="true" :class="{ 'is-idle': !loading && !result && tunnel.status === 'running' }">
        <div v-if="loading" class="tunnel-test-result is-pending">
          <el-icon class="tunnel-test-result-icon is-loading" aria-hidden="true"><Loading /></el-icon>
          <div>
            <h3>正在测试隧道转发</h3>
            <p class="ops-muted">正在检查两端状态和 Relay 数据往返。</p>
          </div>
        </div>
        <div v-else-if="result" class="tunnel-test-result-card" :class="resultStatus.tone">
          <div class="tunnel-test-result">
            <el-icon class="tunnel-test-result-icon" aria-hidden="true">
              <component :is="resultStatus.icon" />
            </el-icon>
            <div>
              <h3>{{ resultStatus.title }}</h3>
              <p v-if="result.success" class="tunnel-test-latency ops-muted">往返耗时 <strong class="ops-mono">{{ result.latency_ms }} ms</strong></p>
              <p v-else-if="result.partial" class="ops-muted">{{ roundTripCheck?.message }}</p>
              <template v-else>
                <p v-if="stageLabels[result.failure_stage]" class="tunnel-test-stage">失败环节 · {{ stageLabels[result.failure_stage] }}</p>
                <p v-if="result.message" class="ops-muted">{{ result.message }}</p>
              </template>
            </div>
          </div>
          <section v-if="result.recommendation" class="tunnel-test-recommendation" aria-labelledby="tunnel-test-recommendation-title">
            <el-icon aria-hidden="true"><InfoFilled /></el-icon>
            <div>
              <h3 id="tunnel-test-recommendation-title">下一步检查</h3>
              <p>{{ result.recommendation }}</p>
            </div>
          </section>
        </div>
        <div v-else-if="tunnel.status !== 'running'" class="tunnel-test-result is-blocked">
          <el-icon class="tunnel-test-result-icon" aria-hidden="true"><WarningFilled /></el-icon>
          <div>
            <h3>隧道未运行</h3>
            <p class="ops-muted">请先启动隧道，再测试转发。</p>
          </div>
        </div>
      </div>

      <details v-if="result?.diagnostics?.length" class="tunnel-test-checks" aria-labelledby="tunnel-test-checks-title">
        <summary>
          <span id="tunnel-test-checks-title">检查明细</span>
          <span class="tunnel-test-check-counts">
            <span v-for="status in checkCounts" :key="status.label" class="ops-status" :class="status.tone">
              <span class="ops-mono">{{ status.count }}</span> 项{{ status.label }}
            </span>
          </span>
          <el-icon class="tunnel-test-check-chevron" aria-hidden="true"><ArrowDown /></el-icon>
        </summary>
        <ul role="list">
          <li v-for="check in result.diagnostics" :key="check.stage" :class="checkStatuses[check.status]?.tone">
            <div class="tunnel-test-check-heading">
              <strong>{{ stageLabels[check.stage] || '其他检查' }}</strong>
              <span class="ops-status" :class="checkStatuses[check.status]?.tone || 'is-muted'">
                {{ checkStatuses[check.status]?.label || '未知' }}
              </span>
            </div>
            <p class="ops-muted">{{ check.message }}</p>
          </li>
        </ul>
      </details>

      <p class="tunnel-test-scope ops-muted">仅验证 Gost Relay 在两端之间的数据往返。应用目标需单独验证。</p>
    </div>

    <template #footer>
      <el-button :disabled="loading" @click="visible = false">关闭</el-button>
      <el-button
        type="primary"
        :icon="result ? RefreshRight : VideoPlay"
        :loading="loading"
        :disabled="tunnel?.status !== 'running'"
        @click="$emit('run')"
      >{{ loading ? '测试中…' : result ? '重新测试' : '开始测试' }}</el-button>
    </template>
  </PanelDialog>
</template>

<script setup>
import { computed } from 'vue'
import { ArrowDown, CircleCloseFilled, Connection, InfoFilled, Loading, RefreshRight, Right, SuccessFilled, VideoPlay, WarningFilled } from '@element-plus/icons-vue'
import PanelDialog from '@/components/PanelDialog.vue'

const visible = defineModel({ type: Boolean, required: true })
const props = defineProps({
  tunnel: { type: Object, default: null },
  result: { type: Object, default: null },
  loading: { type: Boolean, default: false }
})
defineEmits(['run'])

const stageLabels = {
  tunnel: '隧道配置',
  entry_node: '入口节点状态',
  exit_node: '出口节点状态',
  exit_listener: '出口测试监听',
  entry_gost_api: '入口 Gost API',
  entry_chain: '入口转发链',
  entry_gost_process: '入口 Gost 进程',
  relay_round_trip: 'Relay 数据往返',
  relay_connection: '入口至 Relay 连通性',
  relay_handshake: 'Relay 握手或转发',
  exit_relay: '出口 Relay 服务',
  relay_policy: '出口 Relay 访问策略',
  relay_quota: '出口流量配额'
}

const checkStatuses = {
  failed: { label: '失败', tone: 'is-danger' },
  warning: { label: '需检查', tone: 'is-warning' },
  passed: { label: '通过', tone: 'is-success' },
  skipped: { label: '未验证', tone: 'is-muted' }
}

const resultStatus = computed(() => {
  if (props.result?.success) return { title: '隧道转发正常', tone: 'is-passed', icon: SuccessFilled }
  if (props.result?.partial) return { title: 'Relay 握手通过', tone: 'is-partial', icon: InfoFilled }
  return { title: '测试失败', tone: 'is-failed', icon: CircleCloseFilled }
})

const roundTripCheck = computed(() => props.result?.diagnostics?.find(check => check.stage === 'relay_round_trip'))

const checkCounts = computed(() => {
  const checks = props.result?.diagnostics ?? []
  return Object.entries(checkStatuses)
    .map(([status, style]) => ({ ...style, count: checks.filter(check => check.status === status).length }))
    .filter(status => status.count)
})
</script>

<style>
.tunnel-test-dialog.el-dialog { width: 560px; }
.tunnel-test-dialog.el-dialog .el-dialog__footer > .el-button { flex: 0 1 auto; }
</style>

<style scoped>
.tunnel-test-body { display: grid; gap: 16px; overflow-wrap: anywhere; }
.tunnel-test-body p { margin: 0; }
.tunnel-test-body .is-idle { position: absolute; }

.tunnel-test-route { padding: 0; overflow: hidden; }
.tunnel-test-name {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 12px 16px;
  font-size: var(--ops-text-body);
}
.tunnel-test-name .el-icon { align-self: start; flex: none; margin-top: 2px; color: var(--ops-accent); }
.tunnel-test-endpoints {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-top: 1px solid var(--ops-border);
}
.tunnel-test-endpoints dl { min-width: 0; margin: 0; }
.tunnel-test-endpoints dt { color: var(--ops-muted); font-size: var(--ops-text-sm); }
.tunnel-test-endpoints dd { margin: 2px 0 0; font-weight: var(--ops-weight-semibold); }
.tunnel-test-route-arrow { color: var(--ops-accent); }

.tunnel-test-result-card,
.tunnel-test-result.is-pending,
.tunnel-test-result.is-blocked {
  --result-ink: var(--ops-accent);
  padding: 14px 16px;
  border: 1px solid color-mix(in srgb, var(--result-ink) 30%, var(--ops-border));
  border-radius: var(--ops-radius-inset);
  background: color-mix(in srgb, var(--result-ink) 6%, var(--ops-panel));
}
.tunnel-test-result-card.is-passed { --result-ink: var(--ops-success); }
.tunnel-test-result-card.is-failed { --result-ink: var(--ops-danger); }
.tunnel-test-result-card.is-partial,
.tunnel-test-result.is-blocked { --result-ink: var(--ops-warning); }
.tunnel-test-result {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: start;
  gap: 12px;
}
.tunnel-test-result-icon {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--result-ink) 12%, transparent);
  color: var(--result-ink);
  font-size: 21px;
}
.tunnel-test-result h3 { margin-bottom: 4px; font-size: var(--ops-text-title); }
.tunnel-test-stage { color: var(--result-ink); font-weight: var(--ops-weight-semibold); }
.tunnel-test-result .tunnel-test-stage + p { margin-top: 2px; }
.tunnel-test-latency strong { margin-inline-start: 4px; color: var(--result-ink); }
.tunnel-test-recommendation {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: start;
  gap: 8px;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-muted);
}
.tunnel-test-recommendation .el-icon { margin-top: 3px; color: var(--ops-accent); }
.tunnel-test-recommendation h3 { margin-bottom: 3px; font-size: var(--ops-text-body); }

.tunnel-test-checks { border: 1px solid var(--ops-border); border-radius: var(--ops-radius-inset); }
.tunnel-test-checks summary {
  display: grid;
  min-height: 44px;
  grid-template-columns: minmax(max-content, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-text);
  font-weight: var(--ops-weight-semibold);
  list-style: none;
  cursor: pointer;
}
.tunnel-test-checks summary::-webkit-details-marker { display: none; }
.tunnel-test-checks summary:hover { background: var(--ops-raised); }
.tunnel-test-checks summary:focus-visible { outline-offset: -2px; }
.tunnel-test-checks[open] summary { border-end-start-radius: 0; border-end-end-radius: 0; }
.tunnel-test-checks[open] .tunnel-test-check-chevron { transform: rotate(180deg); }
.tunnel-test-check-chevron { color: var(--ops-muted); }
.tunnel-test-check-counts { display: flex; flex-wrap: wrap; justify-content: end; gap: 4px; }
.tunnel-test-check-counts .ops-status { gap: 3px; padding-inline: 6px; }
.tunnel-test-check-counts .ops-status::before { content: none; }
.tunnel-test-checks ul { margin: 0; padding: 0; list-style: none; }
.tunnel-test-checks li { padding: 7px 12px; border-top: 1px solid var(--ops-border); }
.tunnel-test-checks li:last-child { border-end-start-radius: var(--ops-radius-inset); border-end-end-radius: var(--ops-radius-inset); }
.tunnel-test-checks li.is-danger { background: color-mix(in srgb, var(--ops-danger) 6%, var(--ops-panel)); }
.tunnel-test-checks li.is-warning { background: color-mix(in srgb, var(--ops-warning) 5%, var(--ops-panel)); }
.tunnel-test-check-heading { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 4px 8px; margin-bottom: 2px; }
.tunnel-test-scope { font-size: var(--ops-text-sm); }

@media (max-width: 480px) {
  .tunnel-test-body { gap: 12px; }
  .tunnel-test-name, .tunnel-test-endpoints, .tunnel-test-result-card,
  .tunnel-test-result.is-pending, .tunnel-test-result.is-blocked { padding-inline: 12px; }
  .tunnel-test-checks summary { grid-template-columns: minmax(0, 1fr) auto; }
  .tunnel-test-check-counts { grid-area: 2 / 1 / auto / -1; justify-content: start; }
}
</style>
