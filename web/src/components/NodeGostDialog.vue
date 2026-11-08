<template>
  <PanelDialog
    v-model="visible"
    :title="'Gost 服务 · ' + node.name"
    size="wide"
    :close-on-click-modal="false"
    :close-on-press-escape="!restarting"
    :show-close="!restarting"
    @closed="$emit('closed')"
  >
    <div class="gost-dialog-toolbar">
      <p class="ops-muted">检查节点上的 systemd 服务并查看最近 100 条日志。</p>
      <el-button :loading="refreshing" :disabled="restarting" @click="refresh">刷新状态和日志</el-button>
    </div>
    <section aria-labelledby="gost-status-title" :aria-busy="refreshing || restarting">
      <h3 id="gost-status-title">服务状态</h3>
      <p v-if="statusError" class="gost-error" role="alert">{{ statusError }}</p>
      <dl v-else-if="service" class="gost-service-info">
        <div><dt>服务单元</dt><dd class="ops-mono">{{ service.unit }}</dd></div>
        <div>
          <dt>运行状态</dt>
          <dd><span class="ops-status" :class="'is-' + statusTone">{{ statusLabel }}</span></dd>
        </div>
        <div><dt>加载状态</dt><dd class="ops-mono">{{ service.load_state || '未知' }}</dd></div>
        <div><dt>子状态</dt><dd class="ops-mono">{{ service.sub_state || '未知' }}</dd></div>
      </dl>
      <p v-else class="ops-muted" role="status">正在读取服务状态…</p>
    </section>
    <section aria-labelledby="gost-logs-title" :aria-busy="refreshing">
      <h3 id="gost-logs-title">服务日志</h3>
      <p v-if="logsError" class="gost-error" role="alert">{{ logsError }}</p>
      <p v-else-if="!logs.length" class="ops-muted" role="status">{{ refreshing ? '正在读取日志…' : '暂无日志' }}</p>
      <ol v-else class="gost-logs ops-mono" tabindex="0" aria-label="Gost 服务日志，最新记录在前">
        <li v-for="(entry, index) in logs" :key="index">
          <div class="gost-log-meta ops-muted">{{ formatTimestamp(entry.timestamp_unix_micros) }} · {{ priorityLabels[entry.priority ?? 0] || '未知级别' }}</div>
          <div class="gost-log-message">{{ entry.message }}</div>
        </li>
      </ol>
    </section>
    <p v-if="restartError" class="gost-error" role="alert">{{ restartError }}</p>
    <template #footer>
      <el-button :disabled="restarting" @click="visible = false">关闭</el-button>
      <el-button
        type="warning"
        :loading="restarting"
        :disabled="refreshing || !!statusError || service?.load_state !== 'loaded'"
        @click="restart"
      >重启 Gost</el-button>
    </template>
  </PanelDialog>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import PanelDialog from '@/components/PanelDialog.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { getGostLogs, getGostService, restartGostService } from '@/api/node'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['closed'])
const visible = defineModel({ type: Boolean, required: true })
const service = ref(null)
const logs = ref([])
const refreshing = ref(false)
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
  logsError.value = ''
  try {
    const res = await getGostLogs(props.node.id)
    logs.value = (res.data.entries || []).slice().sort((a, b) => b.timestamp_unix_micros - a.timestamp_unix_micros)
  } catch (error) {
    logsError.value = error.response?.data?.message || '无法读取 Gost 日志，请刷新重试。'
  }
}

const readStatus = async () => {
  statusError.value = ''
  try {
    const res = await getGostService(props.node.id)
    service.value = res.data
  } catch (error) {
    statusError.value = error.response?.data?.message || '无法读取 Gost 状态，请刷新重试。'
  }
}

const refresh = async () => {
  refreshing.value = true
  await Promise.all([readStatus(), readLogs()])
  refreshing.value = false
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
    restarting.value = false
  }
  await refresh()
}

onMounted(refresh)
</script>

<style scoped>
.gost-dialog-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
.gost-dialog-toolbar p, .gost-service-info { margin: 0; }
h3 { margin: 16px 0 8px; font-size: 14px; }
.gost-service-info {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
  gap: 12px;
  padding: 12px;
  border-radius: 8px;
  background: var(--ops-inset);
}
.gost-service-info dt { color: var(--ops-muted); }
.gost-service-info dd { margin: 4px 0 0; overflow-wrap: anywhere; }
.gost-error { color: var(--ops-danger); white-space: pre-wrap; overflow-wrap: anywhere; }
.gost-logs {
  max-height: min(40vh, 420px);
  margin: 0;
  padding: 0 12px;
  overflow-y: auto;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
  list-style: none;
}
.gost-logs li { padding: 8px 0; }
.gost-logs li + li { border-top: 1px solid var(--ops-border); }
.gost-log-meta { font-size: 12px; }
.gost-log-message { margin-top: 4px; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
