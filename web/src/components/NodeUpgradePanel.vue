<template>
  <div class="node-upgrade-panel">
    <p v-if="refreshError" :class="isUpgrading ? 'ops-muted' : 'upgrade-error'" :role="isUpgrading ? 'status' : 'alert'">
      {{ refreshError }}
    </p>
    <p v-if="!status && refreshing" class="ops-muted" role="status">正在读取版本与升级状态…</p>

    <template v-if="status">
      <p v-if="status.reason || !status.available" class="upgrade-availability" role="status">
        {{ status.reason || '节点当前无法执行手动升级。' }}
        <span v-if="component === 'daemon' && !status.available">守护程序的自动升级设置仍可编辑。</span>
      </p>
      <section class="upgrade-component" :aria-labelledby="'node-upgrade-' + component" :aria-busy="busy">
        <header class="upgrade-component-header">
          <h3 class="ops-subsection-title" :id="'node-upgrade-' + component">{{ componentNames[component] }}</h3>
          <span v-if="component === 'daemon'" class="ops-status" :class="status.available && status.daemon_update_available ? 'is-success' : 'is-muted'">
            {{ availabilityLabel }}
          </span>
        </header>
        <dl v-if="component === 'daemon'" class="upgrade-versions">
          <div><dt>当前版本</dt><dd class="ops-mono">{{ status.daemon_version || '未上报' }}</dd></div>
          <div><dt>面板版本</dt><dd class="ops-mono">{{ status.latest_daemon_version || '未知' }}</dd></div>
        </dl>
        <dl v-else class="upgrade-versions">
          <div><dt>当前版本</dt><dd class="ops-mono">{{ status.gost_version || node.gost_version || '未上报' }}</dd></div>
        </dl>
        <div class="upgrade-controls">
          <div v-if="component === 'daemon'" class="upgrade-policy">
            <label for="node-auto-upgrade-daemon">自动升级</label>
            <el-switch
              id="node-auto-upgrade-daemon"
              aria-label="yagp-daemon 自动升级"
              :model-value="status.auto_upgrade_daemon || false"
              :loading="savingPolicy"
              :disabled="busy"
              @change="savePolicy"
            />
          </div>
          <el-button
            :aria-label="component === 'daemon' ? '手动升级 yagp-daemon' : undefined"
            :loading="submitting"
            :disabled="!canStartUpgrade"
            @click="startUpgrade"
          >{{ component === 'daemon' ? '手动升级' : '升级 Gost 至最新稳定版' }}</el-button>
        </div>
      </section>

      <div v-if="jobStatus" class="upgrade-job" :role="status.state === 'failed' ? 'alert' : 'status'">
        <div class="upgrade-job-heading">
          <strong>{{ componentNames[status.component] || '节点' }}升级</strong>
          <span v-if="status.target_version" class="ops-mono ops-muted">{{ status.target_version }}</span>
          <span class="ops-status" :class="'is-' + jobStatus.tone">{{ jobStatus.label }}</span>
        </div>
        <p v-if="status.state === 'failed' && status.error" class="upgrade-error">{{ status.error }}</p>
        <p v-else-if="pollPaused" class="ops-muted">已暂停自动查询。请刷新版本状态继续检查，或查看节点守护程序是否在线。</p>
        <p v-else-if="isUpgrading" class="ops-muted">系统会继续查询升级状态。离开此处后任务仍会运行。</p>
      </div>
    </template>

    <p v-if="actionError" class="upgrade-error" role="alert">{{ actionError }}</p>
    <div class="upgrade-refresh">
      <el-button :loading="refreshing" :disabled="submitting || savingPolicy" @click="refreshManually">刷新版本状态</el-button>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { getNodeUpgradeStatus, updateNodeUpgradePolicy, upgradeNodeComponent } from '@/api/node'

const props = defineProps({
  node: { type: Object, required: true },
  component: { type: String, required: true }
})
const status = ref(null)
const refreshing = ref(false)
const submitting = ref(false)
const savingPolicy = ref(false)
const refreshError = ref('')
const actionError = ref('')
const pollPaused = ref(false)
defineExpose({ submitting })

const jobStates = {
  queued: { label: '等待执行', tone: 'warning' },
  running: { label: '正在升级', tone: 'warning' },
  succeeded: { label: '升级成功', tone: 'success' },
  failed: { label: '升级失败', tone: 'danger' }
}
const componentNames = { daemon: 'yagp-daemon', gost: 'Gost' }
const busy = computed(() => refreshing.value || submitting.value || savingPolicy.value)
const isUpgrading = computed(() => status.value?.state === 'queued' || status.value?.state === 'running')
const canStartUpgrade = computed(() => !busy.value && !isUpgrading.value && !refreshError.value && status.value?.available &&
  (props.component === 'daemon' ? status.value.daemon_update_available : !!status.value.gost_version))
const jobStatus = computed(() => {
  if (isUpgrading.value || status.value?.component === props.component) return jobStates[status.value.state]
})
const availabilityLabel = computed(() => {
  if (!status.value.available) return '暂不可升级'
  if (status.value.daemon_update_available) return '有可用更新'
  if (!status.value.daemon_version || !status.value.latest_daemon_version) return '版本待确认'
  return '暂无更新'
})

const errorMessage = (error, fallback) => error.response?.data?.message || (!error.isAxiosError && error.message) || fallback

let pollTimer
let pollDeadline = 0
let disposed = false
const schedulePoll = () => {
  clearTimeout(pollTimer)
  if (!isUpgrading.value) {
    pollDeadline = 0
    pollPaused.value = false
    return
  }
  if (disposed || pollPaused.value) return
  if (!pollDeadline) pollDeadline = Date.now() + 5 * 60 * 1000
  pollTimer = setTimeout(() => {
    if (Date.now() >= pollDeadline) {
      refreshError.value = ''
      pollPaused.value = true
    } else {
      refresh()
    }
  }, Math.min(4000, Math.max(0, pollDeadline - Date.now())))
}

const refresh = async () => {
  if (busy.value) return
  clearTimeout(pollTimer)
  refreshing.value = true
  try {
    const { data } = await getNodeUpgradeStatus(props.node.id)
    const previous = status.value
    // The panel still returns saved policy while a daemon restart hides its active job.
    const matchesJob = jobStates[data.state] && data.component === previous?.component && data.target_version === previous?.target_version
    status.value = isUpgrading.value && !matchesJob ? {
      ...data,
      component: previous.component,
      target_version: previous.target_version,
      state: previous.state,
      error: previous.error
    } : data
    refreshError.value = ''
  } catch (error) {
    refreshError.value = isUpgrading.value
      ? '暂未读到升级状态，系统会继续查询。'
      : errorMessage(error, '无法读取节点版本与升级状态，请重试。')
  } finally {
    refreshing.value = false
    schedulePoll()
  }
}

const refreshManually = () => {
  if (pollPaused.value) {
    pollDeadline = 0
    pollPaused.value = false
  }
  refresh()
}

const savePolicy = async (enabled) => {
  if (busy.value) return
  clearTimeout(pollTimer)
  savingPolicy.value = true
  actionError.value = ''
  const policy = { auto_upgrade_daemon: enabled }
  try {
    await updateNodeUpgradePolicy(props.node.id, policy)
    status.value = { ...status.value, ...policy }
    ElMessage.success('已保存自动升级设置')
  } catch (error) {
    actionError.value = errorMessage(error, '无法保存自动升级设置，请重试。')
  } finally {
    savingPolicy.value = false
    schedulePoll()
  }
}

const startUpgrade = async () => {
  if (!canStartUpgrade.value) return
  const { component } = props
  const target = component === 'gost' ? '最新稳定版' : `当前面板版本 ${status.value.latest_daemon_version}`
  const name = componentNames[component]
  const confirmed = await confirmPanelAction(
    `将节点「${props.node.name}」的 ${name} 更新至${target}。服务会重启，转发连接可能短暂中断。`,
    `升级 ${name}`,
    { confirmButtonText: '确认升级', cancelButtonText: '取消', type: 'warning' }
  ).catch(() => false)
  if (!confirmed || !canStartUpgrade.value) return

  submitting.value = true
  actionError.value = ''
  try {
    const { data } = await upgradeNodeComponent(props.node.id, component)
    status.value = { ...status.value, ...data, error: '' }
    ElMessage.success('升级请求已提交')
  } catch (error) {
    actionError.value = errorMessage(error, '无法确认升级请求已提交，请刷新状态。')
  } finally {
    submitting.value = false
    schedulePoll()
  }
}

onMounted(refresh)
onBeforeUnmount(() => {
  disposed = true
  clearTimeout(pollTimer)
})
</script>

<style scoped>
.node-upgrade-panel { display: grid; gap: 12px; min-width: 0; }
.upgrade-availability, .upgrade-job { padding: var(--ops-inset-padding); border: 1px solid var(--ops-border); border-radius: var(--ops-radius-inset); background: var(--ops-inset); }
.upgrade-availability { color: var(--ops-warning); overflow-wrap: anywhere; }
.upgrade-component { min-width: 0; padding: var(--ops-inset-padding); border: 1px solid var(--ops-border); border-radius: var(--ops-radius-inset); }
.upgrade-component-header, .upgrade-controls, .upgrade-policy, .upgrade-job-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.upgrade-component-header, .upgrade-controls { justify-content: space-between; }
.upgrade-component-header h3 { margin: 0; font-size: var(--ops-text-base); }
.upgrade-versions { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 10px 0; }
.upgrade-versions dt { color: var(--ops-muted); font-size: var(--ops-text-sm); }
.upgrade-versions dd { margin: 3px 0 0; overflow-wrap: anywhere; }
.upgrade-policy label { cursor: pointer; }
.upgrade-job p { margin: 6px 0 0; }
.upgrade-error { color: var(--ops-danger); white-space: pre-wrap; overflow-wrap: anywhere; }
.upgrade-refresh { display: flex; justify-content: flex-end; }

@media (max-width: 640px) {
  .upgrade-controls .el-button, .upgrade-policy, .upgrade-refresh .el-button { min-height: 44px; }
  .upgrade-policy :deep(.el-switch) { min-width: 44px; min-height: 44px; }
}
</style>
