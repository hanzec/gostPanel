<template>
  <section id="node-gost-service" aria-labelledby="node-gost-title">
    <header class="ops-panel-header">
      <div class="gost-title">
        <h2 class="ops-section-title" id="node-gost-title" tabindex="-1"><el-icon aria-hidden="true"><Monitor /></el-icon>Gost 服务</h2>
        <span v-if="service && !statusError" class="ops-status" :class="'is-' + statusTone">{{ statusLabel }}</span>
      </div>
      <div class="gost-actions">
        <el-button :icon="Refresh" :loading="statusLoading || logsLoading" :disabled="restarting" @click="refresh">刷新</el-button>
        <el-button
          :icon="SwitchButton"
          type="warning"
          plain
          :loading="restarting"
          :disabled="statusLoading || logsLoading || !!statusError || service?.load_state !== 'loaded'"
          @click="restart"
        >重启 Gost</el-button>
      </div>
    </header>

    <div class="gost-status" :aria-busy="statusLoading || restarting">
      <p v-if="statusError" class="gost-error" role="alert">{{ statusError }}</p>
      <dl v-else-if="service" class="ops-panel-details">
        <div><dt>服务单元</dt><dd class="ops-mono">{{ service.unit || '未知' }}</dd></div>
        <div><dt>加载状态</dt><dd class="ops-mono">{{ service.load_state || '未知' }}</dd></div>
        <div><dt>子状态</dt><dd class="ops-mono">{{ service.sub_state || '未知' }}</dd></div>
      </dl>
      <p v-else class="ops-muted" role="status">
        {{ statusLoading ? '正在读取服务状态…' : '节点当前未在线。可手动刷新以查询服务状态。' }}
      </p>
      <p v-if="restartError" class="gost-error" role="alert">{{ restartError }}</p>
    </div>

    <div class="gost-log-section">
      <button
        type="button"
        class="gost-log-toggle"
        :aria-expanded="logsOpen"
        aria-controls="node-gost-logs"
        @click="toggleLogs"
      >
        <el-icon aria-hidden="true"><Document /></el-icon>
        <span>服务日志</span>
        <small class="ops-muted">最近 100 条</small>
        <el-icon class="gost-log-chevron" aria-hidden="true"><ArrowDown /></el-icon>
      </button>
      <div v-show="logsOpen" id="node-gost-logs" class="gost-log-content" :aria-busy="logsLoading || restarting">
        <p v-if="logsError" class="gost-error" role="alert">{{ logsError }}</p>
        <p v-else-if="!logs.length" class="ops-muted" role="status">{{ logsLoading || restarting ? '正在读取日志…' : '暂无日志' }}</p>
        <ol v-else class="gost-logs ops-mono" tabindex="0" aria-label="Gost 服务日志，最新记录在前">
          <li v-for="(entry, index) in logs" :key="index">
            <div class="gost-log-meta ops-muted">{{ formatTimestamp(entry.timestamp_unix_micros) }} · {{ priorityLabels[entry.priority ?? 0] || '未知级别' }}</div>
            <div class="gost-log-message">{{ entry.message }}</div>
          </li>
        </ol>
      </div>
    </div>
  </section>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { ArrowDown, Document, Monitor, Refresh, SwitchButton } from '@element-plus/icons-vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { getGostLogs, getGostService, restartGostService } from '@/api/node'

const props = defineProps({ node: { type: Object, required: true } })
const service = ref(null)
const logs = ref([])
const logsOpen = ref(false)
const statusLoading = ref(false)
const logsLoading = ref(false)
const restarting = ref(false)
const statusError = ref('')
const logsError = ref('')
const restartError = ref('')

const statusLabels = {
  active: '运行中', inactive: '未运行', failed: '失败',
  activating: '正在启动', deactivating: '正在停止', reloading: '正在重新加载'
}
const statusLabel = computed(() => statusLabels[service.value?.active_state] || service.value?.active_state || '未知')
const statusTone = computed(() => ({ active: 'success', failed: 'danger', activating: 'warning' }[service.value?.active_state] || 'muted'))
const priorityLabels = ['紧急', '警报', '严重', '错误', '警告', '通知', '信息', '调试']
const dateFormatter = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' })
const formatTimestamp = (microseconds) => microseconds ? dateFormatter.format(Number(microseconds) / 1000) : '未知时间'

const readLogs = async () => {
  if (logsLoading.value) return
  logsLoading.value = true
  logsError.value = ''
  try {
    const res = await getGostLogs(props.node.id)
    logs.value = (res.data.entries || []).slice().sort((a, b) => b.timestamp_unix_micros - a.timestamp_unix_micros)
  } catch (error) {
    logsError.value = error.response?.data?.message || '无法读取 Gost 日志，请刷新重试。'
  } finally {
    logsLoading.value = false
  }
}

const readStatus = async () => {
  if (statusLoading.value) return
  statusLoading.value = true
  statusError.value = ''
  try {
    const res = await getGostService(props.node.id)
    service.value = res.data
  } catch (error) {
    statusError.value = error.response?.data?.message || '无法读取 Gost 状态，请刷新重试。'
  } finally {
    statusLoading.value = false
  }
}

const refresh = () => Promise.all([readStatus(), logsOpen.value && readLogs()])

const toggleLogs = () => {
  logsOpen.value = !logsOpen.value
  if (logsOpen.value && !restarting.value) readLogs()
}

const restart = async () => {
  const confirmed = await confirmPanelAction('重启 Gost 会短暂中断该节点正在处理的连接。', '重启 Gost', {
    confirmButtonText: '重启 Gost', cancelButtonText: '取消', type: 'warning'
  }).catch(() => false)
  if (!confirmed) return

  restarting.value = true
  restartError.value = ''
  try {
    const res = await restartGostService(props.node.id)
    service.value = res.data
    ElMessage.success('Gost 重启操作已完成')
  } catch (error) {
    restartError.value = error.response?.data?.message || '重启请求失败，请核对服务状态和日志。'
  } finally {
    await refresh()
    restarting.value = false
  }
}

watch(() => props.node.status, (status) => {
  if (status === 'online') readStatus()
}, { immediate: true })
</script>

<style scoped>
.gost-title, .gost-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.gost-actions .el-button { min-height: 44px; margin: 0; }
.gost-status { display: grid; gap: 8px; min-width: 0; }
.gost-error { color: var(--ops-danger); white-space: pre-wrap; overflow-wrap: anywhere; }

.gost-log-section { min-width: 0; border-top: 1px solid var(--ops-border); }
.gost-log-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 44px;
  padding: 6px 0;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--ops-text);
  text-align: start;
  font-weight: var(--ops-weight-semibold);
}
.gost-log-toggle:hover { color: var(--ops-accent); }
.gost-log-toggle small { font-size: var(--ops-text-sm); font-weight: var(--ops-weight-normal); }
.gost-log-chevron { margin-inline-start: auto; }
.gost-log-toggle[aria-expanded="true"] .gost-log-chevron { transform: rotate(180deg); }
.gost-log-content { padding-top: 4px; }
.gost-logs {
  max-height: min(40vh, 420px);
  margin: 0;
  padding: 0 12px;
  overflow-y: auto;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  list-style: none;
}
.gost-logs li { padding: 8px 0; }
.gost-logs li + li { border-top: 1px solid var(--ops-border); }
.gost-log-meta { font-size: var(--ops-text-xs); }
.gost-log-message { font-size: var(--ops-text-sm); margin-top: 4px; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
