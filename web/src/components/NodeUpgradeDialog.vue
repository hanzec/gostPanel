<template>
  <PanelDialog
    v-model="visible"
    :title="'版本与升级 · ' + node.name"
    description="分别管理 Gost 和 yagp-daemon 的升级。升级会重启对应服务，相关连接可能短暂中断。"
    :close-on-click-modal="false"
    :close-on-press-escape="!submitting"
    :show-close="!submitting"
    @closed="$emit('closed')"
  >

    <p v-if="refreshError" :class="isUpgrading ? 'ops-muted' : 'upgrade-error'" :role="isUpgrading ? 'status' : 'alert'">
      {{ refreshError }}
    </p>
    <p v-if="!status && refreshing" class="ops-muted" role="status">正在读取版本与升级状态…</p>

    <template v-if="status">
      <p v-if="status.reason || !status.available" class="upgrade-availability" role="status">
        {{ status.reason || '节点当前无法执行手动升级。' }}
        <span v-if="!status.available">自动升级设置仍可编辑。</span>
      </p>
      <div class="upgrade-components" :aria-busy="busy">
        <section v-for="item in components" :key="item.key" class="upgrade-component" :aria-labelledby="'node-upgrade-' + item.key">
          <header class="upgrade-component-header">
            <h3 :id="'node-upgrade-' + item.key">{{ item.name }}</h3>
            <span class="ops-status" :class="status.available && item.hasUpdate ? 'is-success' : 'is-muted'">
              {{ availabilityLabel(item) }}
            </span>
          </header>
          <dl class="upgrade-versions">
            <div><dt>当前版本</dt><dd class="ops-mono">{{ item.currentVersion || '未上报' }}</dd></div>
            <div><dt>{{ item.key === 'daemon' ? '面板版本' : '最新版本' }}</dt><dd class="ops-mono">{{ item.latestVersion || '未知' }}</dd></div>
          </dl>
          <div class="upgrade-controls">
            <div class="upgrade-policy">
              <label :for="'node-auto-upgrade-' + item.key">自动升级</label>
              <el-switch
                :id="'node-auto-upgrade-' + item.key"
                :aria-label="item.name + ' 自动升级'"
                :model-value="item.autoUpgrade"
                :loading="savingPolicy === item.key"
                :disabled="busy"
                @change="savePolicy(item, $event)"
              />
            </div>
            <el-button
              :aria-label="'手动升级 ' + item.name"
              :loading="submitting === item.key"
              :disabled="busy || isUpgrading || !!refreshError || !status.available || !item.hasUpdate"
              @click="startUpgrade(item)"
            >手动升级</el-button>
          </div>
        </section>
      </div>

      <div v-if="jobStatus" class="upgrade-job" :role="status.state === 'failed' ? 'alert' : 'status'">
        <div class="upgrade-job-heading">
          <strong>{{ componentNames[status.component] || '节点' }}升级</strong>
          <span v-if="status.target_version" class="ops-mono ops-muted">{{ status.target_version }}</span>
          <span class="ops-status" :class="'is-' + jobStatus.tone">{{ jobStatus.label }}</span>
        </div>
        <p v-if="status.state === 'failed' && status.error" class="upgrade-error">{{ status.error }}</p>
        <p v-else-if="pollPaused" class="ops-muted">已暂停自动查询。请刷新版本状态继续检查，或查看节点守护程序是否在线。</p>
        <p v-else-if="isUpgrading" class="ops-muted">系统会继续查询升级状态。关闭弹窗后任务仍会运行。</p>
      </div>
    </template>

    <p v-if="actionError" class="upgrade-error" role="alert">{{ actionError }}</p>
    <template #footer>
      <el-button :disabled="!!submitting" @click="visible = false">关闭</el-button>
      <el-button :loading="refreshing" :disabled="!!submitting || !!savingPolicy" @click="refreshManually">刷新版本状态</el-button>
    </template>
  </PanelDialog>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import PanelDialog from '@/components/PanelDialog.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { getNodeUpgradeStatus, updateNodeUpgradePolicy, upgradeNodeComponent } from '@/api/node'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['closed'])
const visible = defineModel({ type: Boolean, required: true })
const status = ref(null)
const refreshing = ref(false)
const submitting = ref('')
const savingPolicy = ref('')
const refreshError = ref('')
const actionError = ref('')
const pollPaused = ref(false)

const componentNames = { gost: 'Gost', daemon: 'yagp-daemon' }
const jobStates = {
  queued: { label: '等待执行', tone: 'warning' },
  running: { label: '正在升级', tone: 'warning' },
  succeeded: { label: '升级成功', tone: 'success' },
  failed: { label: '升级失败', tone: 'danger' }
}
const components = computed(() => Object.entries(componentNames).map(([key, name]) => ({
  key,
  name,
  currentVersion: status.value?.[`${key}_version`],
  latestVersion: status.value?.[`latest_${key}_version`],
  hasUpdate: status.value?.[`${key}_update_available`] || false,
  autoUpgrade: status.value?.[`auto_upgrade_${key}`] || false
})))
const busy = computed(() => refreshing.value || !!submitting.value || !!savingPolicy.value)
const isUpgrading = computed(() => status.value?.state === 'queued' || status.value?.state === 'running')
const jobStatus = computed(() => jobStates[status.value?.state])
const availabilityLabel = (item) => {
  if (!status.value.available) return '暂不可升级'
  if (item.hasUpdate) return '有可用更新'
  if (!item.currentVersion || !item.latestVersion) return '版本待确认'
  return '暂无更新'
}

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
  if (disposed || !visible.value || pollPaused.value) return
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

const savePolicy = async (item, enabled) => {
  if (busy.value) return
  clearTimeout(pollTimer)
  savingPolicy.value = item.key
  actionError.value = ''
  const policy = {
    auto_upgrade_gost: status.value.auto_upgrade_gost || false,
    auto_upgrade_daemon: status.value.auto_upgrade_daemon || false,
    [`auto_upgrade_${item.key}`]: enabled
  }
  try {
    await updateNodeUpgradePolicy(props.node.id, policy)
    status.value = { ...status.value, ...policy }
    ElMessage.success('已保存自动升级设置')
  } catch (error) {
    actionError.value = errorMessage(error, '无法保存自动升级设置，请重试。')
  } finally {
    savingPolicy.value = ''
    schedulePoll()
  }
}

const startUpgrade = async (item) => {
  if (busy.value || isUpgrading.value || refreshError.value || !status.value.available || !item.hasUpdate) return
  const target = item.key === 'gost' ? '最新稳定版' : `当前面板版本 ${item.latestVersion}`
  const confirmed = await confirmPanelAction(
    `将节点「${props.node.name}」的 ${item.name} 更新至${target}。服务会重启，转发连接可能短暂中断。`,
    `升级 ${item.name}`,
    { confirmButtonText: '确认升级', cancelButtonText: '取消', type: 'warning' }
  ).catch(() => false)
  if (!confirmed) return

  submitting.value = item.key
  actionError.value = ''
  try {
    const { data } = await upgradeNodeComponent(props.node.id, item.key)
    status.value = { ...status.value, ...data, error: '' }
    ElMessage.success('升级请求已提交')
  } catch (error) {
    actionError.value = errorMessage(error, '无法确认升级请求已提交，请刷新状态。')
  } finally {
    submitting.value = ''
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
.upgrade-availability, .upgrade-job { padding: 10px 12px; border: 1px solid var(--ops-border); border-radius: 8px; background: var(--ops-inset); }
.upgrade-availability { margin: 0 0 12px; color: var(--ops-warning); overflow-wrap: anywhere; }
.upgrade-components { display: grid; gap: 12px; }
.upgrade-component { min-width: 0; padding: 12px; border: 1px solid var(--ops-border); border-radius: 8px; }
.upgrade-component-header, .upgrade-controls, .upgrade-policy, .upgrade-job-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.upgrade-component-header, .upgrade-controls { justify-content: space-between; }
.upgrade-component-header h3 { margin: 0; font-size: 15px; }
.upgrade-versions { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 10px 0; }
.upgrade-versions dt { color: var(--ops-muted); font-size: 12px; }
.upgrade-versions dd { margin: 3px 0 0; overflow-wrap: anywhere; }
.upgrade-policy label { cursor: pointer; }
.upgrade-job { margin-top: 12px; }
.upgrade-job p { margin: 6px 0 0; }
.upgrade-error { color: var(--ops-danger); white-space: pre-wrap; overflow-wrap: anywhere; }

@media (max-width: 640px) {
  .upgrade-controls .el-button, .upgrade-policy { min-height: 44px; }
  .upgrade-policy :deep(.el-switch) { min-width: 44px; min-height: 44px; }
}
</style>
