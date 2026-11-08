<template>
  <div class="ops-page">
    <PageHeader eyebrow="NODE INVENTORY" title="节点管理" description="登记 Gost 实例，查看节点连接状态和流量，并管理节点服务。">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="fetchData()">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openDialog()">添加节点</el-button>
      </template>
    </PageHeader>

    <MetricStrip label="节点统计" :items="nodeMetrics" />

    <section class="ops-panel inventory-panel" aria-labelledby="node-inventory-title">
      <div class="inventory-heading">
        <div>
          <h2 id="node-inventory-title">节点清单</h2>
          <p class="ops-muted">查看节点健康状态、累计上下行流量及上限。</p>
        </div>
        <span class="inventory-badge ops-mono">gRPC / TLS</span>
      </div>

      <form class="ops-toolbar inventory-filters" aria-label="筛选节点" @submit.prevent="handleSearch">
        <div class="inventory-field">
          <label for="node-keyword">节点名称</label>
          <el-input id="node-keyword" v-model="searchKeyword" :prefix-icon="Search" clearable placeholder="搜索名称" @clear="handleSearch" />
        </div>
        <div class="inventory-field">
          <label for="node-status">健康状态</label>
          <el-select id="node-status" v-model="searchStatus" placeholder="全部状态" clearable @change="handleSearch">
            <el-option label="在线" value="online" />
            <el-option label="离线" value="offline" />
            <el-option label="异常" value="error" />
          </el-select>
        </div>
        <el-button native-type="submit" :icon="Search">筛选</el-button>
      </form>

      <div v-if="loadError" class="inventory-error" role="alert">
        <span>无法加载节点清单，请重试。</span>
        <el-button :loading="loading" @click="fetchData()">重新加载</el-button>
      </div>

      <div v-if="loading && !nodeList.length && !loadError" class="inventory-state" role="status">
        <el-skeleton :rows="4" animated />
        <span class="ops-muted">正在加载节点</span>
      </div>
      <div v-else-if="!nodeList.length && !loadError" class="inventory-state">
        <el-icon class="inventory-state-icon" aria-hidden="true"><Monitor /></el-icon>
        <p>{{ filtered ? '没有匹配的节点' : '尚未添加节点' }}</p>
        <span class="ops-muted">{{ filtered ? '调整关键字或状态后重试。' : '先登记一个 Gost 实例，再生成安装命令。' }}</span>
        <el-button v-if="filtered" @click="clearFilters">清除筛选</el-button>
        <el-button v-else type="primary" @click="openDialog()">添加节点</el-button>
      </div>
      <div v-else-if="nodeList.length" class="ops-table-scroll inventory-scroll" :aria-busy="loading">
        <table class="inventory-table" role="table" aria-labelledby="node-inventory-title">
          <thead role="rowgroup">
            <tr role="row">
              <th scope="col" role="columnheader">实例</th>
              <th scope="col" role="columnheader">节点 IP</th>
              <th scope="col" role="columnheader">状态</th>
              <th scope="col" role="columnheader">累计流量</th>
              <th scope="col" role="columnheader">操作</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            <tr v-for="node in nodeList" :key="node.id" role="row">
              <td class="inventory-resource" role="cell">
                <span class="inventory-row-name">
                  <el-icon class="inventory-row-icon" aria-hidden="true"><Monitor /></el-icon>
                  <strong>{{ node.name }}</strong>
                </span>
                <span class="inventory-secondary ops-mono ops-muted">#{{ node.id }}</span>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">节点 IP</span>
                <strong class="ops-mono">{{ node.address || '待获取地址' }}</strong>
                <span v-if="node.ipv4" class="inventory-secondary ops-muted">IPv4 · <span class="ops-mono">{{ node.ipv4 }}</span></span>
                <span v-if="node.ipv6" class="inventory-secondary ops-muted">IPv6 · <span class="ops-mono">{{ node.ipv6 }}</span></span>
                <span class="inventory-secondary ops-muted">检查 · {{ node.last_check_at ? formatTime(node.last_check_at) : '待检查' }}</span>
              </td>
              <td class="inventory-status" role="cell">
                <span class="ops-status" :class="'is-' + (nodeStates[node.status]?.tone || 'muted')">
                  {{ nodeStates[node.status]?.label || '未知' }}
                </span>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">累计流量</span>
                <strong class="ops-mono">{{ formatBytes(node.total_bytes) }}</strong>
                <span class="inventory-secondary ops-muted">
                  入 <span class="ops-mono">{{ formatBytes(node.input_bytes) }}</span>
                  <span aria-hidden="true"> · </span>
                  出 <span class="ops-mono">{{ formatBytes(node.output_bytes) }}</span>
                </span>
                <template v-if="node.traffic_limit_bytes > 0">
                  <span class="inventory-secondary ops-muted">
                    上限 <span class="ops-mono">{{ formatBytes(node.traffic_limit_bytes) }}</span>
                    · {{ trafficResetPeriods[node.traffic_reset_period] || trafficResetPeriods.none }}
                  </span>
                  <span v-if="node.traffic_quota_state === 'active' || node.traffic_quota_state === 'blocked'" class="inventory-secondary ops-muted">
                    {{ node.traffic_reset_period && node.traffic_reset_period !== 'none' ? '本期用量' : '额度用量' }}
                    <span class="ops-mono">{{ formatBytes(node.traffic_quota_used_bytes) }}</span>
                  </span>
                  <span class="ops-status node-traffic-status" :class="(trafficQuotaStates[node.traffic_quota_state] || trafficQuotaStates.pending).tone">
                    {{ (trafficQuotaStates[node.traffic_quota_state] || trafficQuotaStates.pending).label }}
                  </span>
                  <span v-if="node.traffic_quota_state === 'unsupported'" class="inventory-secondary ops-muted">
                    需要支持流量配额的 yagp-daemon 和 nftables。
                  </span>
                </template>
                <span v-else-if="node.traffic_quota_state" class="inventory-secondary ops-muted">解除限制待同步</span>
                <span v-else class="inventory-secondary ops-muted">未设上限</span>
              </td>
              <td class="inventory-actions-cell" role="cell">
                <div class="inventory-actions">
                  <el-button @click="showInstallCommand(node)">安装</el-button>
                  <el-button @click="handleViewConfig(node)">配置</el-button>
                  <el-dropdown trigger="click" @command="handleNodeAction($event, node)">
                    <el-button :aria-label="'节点 ' + node.name + ' 的更多操作'">
                      更多<el-icon aria-hidden="true"><ArrowDown /></el-icon>
                    </el-button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="gost">Gost 服务</el-dropdown-item>
                        <el-dropdown-item command="upgrade">版本与升级</el-dropdown-item>
                        <el-dropdown-item command="nft">nft 配置</el-dropdown-item>
                        <el-dropdown-item command="edit">编辑节点</el-dropdown-item>
                        <el-dropdown-item command="copy">复制节点</el-dropdown-item>
                        <el-dropdown-item command="delete" divided class="inventory-danger">删除节点</el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="total" class="ops-pagination inventory-pagination">
        <span class="ops-muted">共 {{ total }} 个节点</span>
        <div class="inventory-page-controls">
          <el-select v-model="pageSize" class="inventory-page-size" aria-label="每页节点数" @change="handleSearch">
            <el-option v-for="size in [10, 20, 50, 100]" :key="size" :label="size + ' 条/页'" :value="size" />
          </el-select>
          <el-pagination
            v-model:current-page="page"
            :page-size="pageSize"
            :total="total"
            :pager-count="3"
            layout="prev, pager, next"
            @current-change="fetchData()"
          />
        </div>
      </div>
    </section>

    <PanelDialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑节点' : '添加节点'"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitLoading"
      :show-close="!submitLoading"
      @closed="openCreatedNodeInstall"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="handleSubmit">
        <p class="node-address-help ops-muted">节点主动连接面板，注册时会自动上报 IP。</p>
        <div class="inventory-form-grid">
          <el-form-item label="节点名称" prop="name" class="inventory-form-wide">
            <el-input v-model="form.name" placeholder="例如 华东入口" />
          </el-form-item>
          <el-form-item label="流量上限（GiB，可选）" prop="trafficLimitGiB" class="inventory-form-wide">
            <el-input v-model.trim="form.trafficLimitGiB" inputmode="decimal" placeholder="例如 100.5" aria-describedby="node-traffic-limit-help" clearable />
            <p id="node-traffic-limit-help" class="node-traffic-help ops-muted">
              留空或输入 0 表示不限。达额后节点立即拦截转发，额度恢复后自动解除限制。在线节点约每 5 秒同步用量和周期。
            </p>
          </el-form-item>
          <el-form-item label="重置周期" prop="trafficResetPeriod" class="inventory-form-wide">
            <el-select v-model="form.trafficResetPeriod" :disabled="!trafficLimitBytes" aria-describedby="node-traffic-reset-help">
              <el-option v-for="(label, period) in trafficResetPeriods" :key="period" :label="label" :value="period" />
            </el-select>
            <p id="node-traffic-reset-help" class="node-traffic-help ops-muted">按北京时间零点重置，每周从周一开始。</p>
          </el-form-item>
          <el-form-item label="备注" prop="remark" class="inventory-form-wide">
            <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可记录节点用途或部署位置" />
          </el-form-item>
        </div>
      </el-form>
      <template #footer>
        <el-button :disabled="submitLoading" @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitLoading" @click="handleSubmit">{{ isEdit ? '保存更改' : '添加节点' }}</el-button>
      </template>
    </PanelDialog>

    <PanelDialog v-model="configDialogVisible" :title="'节点配置 · ' + (configNode?.name || '')" size="wide">
      <div v-if="configLoading" class="inventory-state" role="status">
        <el-skeleton :rows="4" animated />
        <span class="ops-muted">正在读取节点配置</span>
      </div>
      <div v-else-if="configError" class="inventory-error" role="alert">
        <span>无法读取该节点的配置。</span>
        <el-button @click="handleViewConfig(configNode)">重新加载</el-button>
      </div>
      <el-tabs v-else v-model="configActiveTab" stretch>
        <el-tab-pane v-for="tab in configTabs" :key="tab.name" :label="tab.label" :name="tab.name">
          <p v-if="!nodeConfig?.[tab.name]?.length" class="config-empty ops-muted">暂无{{ tab.label }}</p>
          <details v-for="entry in nodeConfig?.[tab.name] || []" :key="entry.name" class="config-entry">
            <summary>
              <strong>{{ entry.name || '未命名' }}</strong>
              <span class="ops-mono ops-muted">{{ configSummary(tab.name, entry) }}</span>
            </summary>
            <pre class="config-code">{{ JSON.stringify(entry, null, 2) }}</pre>
          </details>
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button @click="configDialogVisible = false">关闭</el-button>
      </template>
    </PanelDialog>

    <NodeGostDialog v-if="gostNode" v-model="gostDialogVisible" :node="gostNode" @closed="gostNode = null" />
    <NodeUpgradeDialog v-if="upgradeNode" v-model="upgradeDialogVisible" :node="upgradeNode" @closed="upgradeNode = null" />
    <NodeNftDialog v-if="nftNode" v-model="nftDialogVisible" :node="nftNode" @closed="nftNode = null" />

    <PanelDialog
      v-model="installDialogVisible"
      :title="'安装节点 · ' + (installNode?.name || '')"
      :description="'节点 #' + (installNode?.id || '') + ' · 在目标服务器上以 root 身份执行安装命令。'"
      :close-on-click-modal="false"
      @close="resetInstall"
    >
      <p v-if="installLoading" class="install-feedback ops-muted" role="status">正在生成注册令牌…</p>
      <p v-else-if="installError" class="install-feedback install-error" role="alert">{{ installError }}</p>
      <div v-else-if="registration" class="install-content">
        <section class="install-field" aria-labelledby="node-registration-token">
          <h3 id="node-registration-token">注册令牌</h3>
          <code class="install-token ops-mono">{{ registration.token }}</code>
          <p class="install-hint ops-muted">一次性使用 · <time :datetime="registration.expiresAt">{{ formatTime(registration.expiresAt) }}</time> 到期</p>
        </section>
        <div class="install-field">
          <label for="node-install-command">安装命令</label>
          <textarea
            id="node-install-command"
            ref="installCommandField"
            class="install-command ops-mono"
            :value="installCommand"
            readonly
            spellcheck="false"
            rows="3"
          ></textarea>
        </div>
        <p v-if="copyFeedback" class="install-copy-feedback" role="status">{{ copyFeedback }}</p>
        <p class="install-hint ops-muted">生成新令牌会撤销上一枚未使用的令牌。</p>
        <p v-if="installNode?.username" class="install-replace-note">重新注册会使旧实例失去访问权限。</p>
      </div>

      <template #footer>
        <el-button @click="installDialogVisible = false">关闭</el-button>
        <el-button v-if="installError" type="primary" @click="prepareInstall">重试生成</el-button>
        <el-button v-else type="primary" :icon="CopyDocument" :disabled="!installCommand" @click="copyInstallCommand">复制安装命令</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { ref, reactive, computed, defineAsyncComponent, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage } from 'element-plus'
import { ArrowDown, CircleCheck, CircleClose, CopyDocument, Monitor, Plus, Refresh, Search, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import PanelDialog from '@/components/PanelDialog.vue'
import { getNodeList, createNode, updateNode, issueNodeRegistrationToken, deleteNode, getNodeConfig } from '@/api/node'
import { getPublicSystemConfig } from '@/api/system'
import { confirmPanelAction } from '@/utils/panel-dialog'

const NodeGostDialog = defineAsyncComponent(() => import('@/components/NodeGostDialog.vue'))
const NodeUpgradeDialog = defineAsyncComponent(() => import('@/components/NodeUpgradeDialog.vue'))
const NodeNftDialog = defineAsyncComponent(() => import('@/components/NodeNftDialog.vue'))
const INSTALL_SCRIPT_URL = 'https://raw.githubusercontent.com/hanzec/yagp/HEAD/scripts/install_node.sh'
const nodeStates = {
  online: { label: '在线', icon: CircleCheck, tone: 'success' },
  offline: { label: '离线', icon: CircleClose },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}
const trafficResetPeriods = { none: '不重置', daily: '每日重置', weekly: '每周重置', monthly: '每月重置' }
const trafficQuotaStates = {
  active: { label: '配额生效', tone: 'is-success' },
  blocked: { label: '已达上限', tone: 'is-danger' },
  pending: { label: '待同步', tone: 'is-muted' },
  unsupported: { label: '需升级', tone: 'is-warning' }
}
const configTabs = [
  { name: 'services', label: '服务' },
  { name: 'chains', label: '转发链' },
  { name: 'observers', label: '观察器' }
]
const GIB = 1024 ** 3
const dateFormatter = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' })

const nodeList = ref([])
const loading = ref(true)
const loadError = ref(false)
const page = ref(1)
const pageSize = ref(10)
const total = ref(null)
const searchKeyword = ref('')
const searchStatus = ref('')
const filtered = computed(() => Boolean(searchKeyword.value.trim() || searchStatus.value))
const nodeMetrics = computed(() => [
  { label: '筛选结果', value: total.value, detail: '全部匹配节点', icon: Monitor },
  ...Object.entries(nodeStates).map(([status, state]) => ({
    ...state,
    value: total.value === null ? null : nodeList.value.filter(node => node.status === status).length,
    detail: '当前页'
  }))
])

const dialogVisible = ref(false)
const editId = ref(null)
const isEdit = computed(() => editId.value !== null)
const submitLoading = ref(false)
const formRef = ref(null)
const form = reactive({ name: '', remark: '', trafficLimitGiB: '', trafficResetPeriod: 'none' })
const originalTrafficLimitBytes = ref(0)
let originalTrafficResetPeriod = 'none'
const originalTrafficLimitGiB = computed(() => {
  const bytes = originalTrafficLimitBytes.value
  return bytes > 0 ? (bytes / GIB).toFixed(10).replace(/\.?0+$/, '') : ''
})
const trafficLimitBytes = computed(() => {
  const value = form.trafficLimitGiB
  if (value === originalTrafficLimitGiB.value) return originalTrafficLimitBytes.value
  if (!value) return 0
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(value)) return null
  const bytes = Number(value) * GIB
  const rounded = Math.round(bytes)
  return Number.isSafeInteger(rounded) && (bytes === 0 || rounded > 0) ? rounded : null
})
const rules = {
  name: [{ required: true, message: '请输入节点名称', trigger: 'blur' }],
  trafficLimitGiB: [{
    validator: () => trafficLimitBytes.value !== null && (isEdit.value || Number.isSafeInteger(trafficLimitBytes.value)),
    message: '请输入 0 或至少 1 字节且小于 8 PiB 的 GiB 数值。',
    trigger: 'blur'
  }]
}

const configDialogVisible = ref(false)
const configActiveTab = ref('services')
const configNode = ref(null)
const nodeConfig = ref(null)
const configLoading = ref(false)
const configError = ref(false)

const gostNode = ref(null)
const gostDialogVisible = ref(false)
const upgradeNode = ref(null)
const upgradeDialogVisible = ref(false)
const nftNode = ref(null)
const nftDialogVisible = ref(false)

const installDialogVisible = ref(false)
const currentInstallNode = ref(null)
const installNode = computed(() => nodeList.value.find(node => node.id === currentInstallNode.value?.id) || currentInstallNode.value)
const registration = ref(null)
const installLoading = ref(false)
const installError = ref('')
const installCommandField = ref(null)
const copyFeedback = ref('')
const pendingInstallTokens = new Map()
let createdInstallNode
let installSession = 0
let registrationTimer

const formatBytes = (bytes) => {
  if (!bytes) return '0 B'
  const sizes = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizes.length - 1)
  return Number((bytes / 1024 ** index).toFixed(2)) + ' ' + sizes[index]
}

const formatTime = (value) => dateFormatter.format(new Date(value))

const configSummary = (tab, entry) => {
  if (tab === 'chains') return (entry.hops?.length || 0) + ' 跳'
  if (tab === 'observers') return [entry.plugin?.type, entry.plugin?.addr].filter(Boolean).join(' · ')
  return [entry.addr, entry.handler?.type, entry.handler?.chain, (entry.forwarder?.nodes?.length || 0) + ' 个目标']
    .filter(Boolean).join(' · ')
}

const shellQuote = (value) => "'" + String(value).replaceAll("'", "'\"'\"'") + "'"

const installCommand = computed(() => {
  if (!registration.value) return ''
  const { address, token } = registration.value
  return 'curl -fsSL ' + shellQuote(INSTALL_SCRIPT_URL) + ' | bash -s -- ' +
    '--panel-address ' + shellQuote(address) + ' --token ' + shellQuote(token)
})

const showInstallCommand = (row) => {
  resetInstall()
  currentInstallNode.value = row
  installDialogVisible.value = true
  prepareInstall()
}

const resetInstall = () => {
  installSession++
  clearTimeout(registrationTimer)
  registration.value = null
  installLoading.value = false
  installError.value = ''
  copyFeedback.value = ''
}

const requestInstallToken = (nodeId) => {
  const pending = pendingInstallTokens.get(nodeId)
  if (pending) return pending

  // A reopened dialog shares the in-flight request. Another POST could invalidate the token it shows.
  const request = issueNodeRegistrationToken(nodeId).finally(() => pendingInstallTokens.delete(nodeId))
  pendingInstallTokens.set(nodeId, request)
  return request
}

const expireRegistration = () => {
  clearTimeout(registrationTimer)
  registration.value = null
  copyFeedback.value = ''
  installError.value = '令牌已过期，请重新生成。'
}

const prepareInstall = async () => {
  const node = currentInstallNode.value
  if (!node || installLoading.value) return
  const session = installSession
  const current = () => session === installSession && installDialogVisible.value
  installLoading.value = true
  installError.value = ''

  try {
    const config = await getPublicSystemConfig().catch(() => null)
    if (!current()) return
    if (!config) {
      installError.value = '无法读取面板注册地址，请重试。'
      return
    }
    const address = config.data?.reportAddress?.trim()
    if (!address) {
      installError.value = '请在面板配置中设置 report.address 后重试。'
      return
    }

    const result = await requestInstallToken(node.id).catch(() => null)
    if (!current()) return
    const token = result?.data?.token
    const expiresAt = result?.data?.expires_at
    if (!token || !expiresAt) {
      installError.value = '签发注册令牌失败，请重试。'
      return
    }
    const remaining = Date.parse(expiresAt) - Date.now()
    if (!(remaining > 0)) {
      expireRegistration()
      return
    }
    registration.value = { address, token, expiresAt }
    registrationTimer = setTimeout(expireRegistration, remaining)
  } finally {
    if (current()) installLoading.value = false
  }
}

const copyInstallCommand = async () => {
  const command = installCommand.value
  const field = installCommandField.value
  if (!command || !field) return
  if (Date.parse(registration.value.expiresAt) <= Date.now()) {
    expireRegistration()
    return
  }

  const session = installSession
  let copied = false
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(command)
      copied = true
    } catch {}
  }
  if (session !== installSession || !installDialogVisible.value || installCommand.value !== command) return
  if (!copied) {
    field.focus({ preventScroll: true })
    field.select()
    try {
      copied = document.execCommand?.('copy') === true
    } catch {}
  }
  copyFeedback.value = copied ? '安装命令已复制' : '已选中安装命令，请使用系统复制功能。'
}

const fetchData = async (isSilent = false) => {
  if (isSilent && (loading.value || loadError.value)) return
  if (!isSilent) loading.value = true
  try {
    const res = await getNodeList({
      page: page.value,
      pageSize: pageSize.value,
      keyword: searchKeyword.value,
      status: searchStatus.value
    })
    nodeList.value = res.data.list || []
    total.value = res.data.total || 0
    loadError.value = false
  } catch {
    loadError.value = true
  } finally {
    if (!isSilent) loading.value = false
  }
}

const handleSearch = () => {
  page.value = 1
  fetchData()
}

const clearFilters = () => {
  searchKeyword.value = ''
  searchStatus.value = ''
  handleSearch()
}

const openDialog = (row = null) => {
  editId.value = row?.id ?? null
  originalTrafficLimitBytes.value = row?.traffic_limit_bytes ?? 0
  originalTrafficResetPeriod = row?.traffic_reset_period || 'none'
  Object.assign(form, {
    name: row?.name ?? '',
    remark: row?.remark || '',
    trafficLimitGiB: originalTrafficLimitGiB.value,
    trafficResetPeriod: originalTrafficResetPeriod
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value) return
  submitLoading.value = true
  try {
    const valid = await formRef.value.validate().catch(() => false)
    if (!valid) return

    const node = { name: form.name, remark: form.remark }
    if (!isEdit.value || trafficLimitBytes.value !== originalTrafficLimitBytes.value) {
      node.traffic_limit_bytes = trafficLimitBytes.value
    }
    if (!isEdit.value || form.trafficResetPeriod !== originalTrafficResetPeriod) {
      node.traffic_reset_period = form.trafficResetPeriod
    }
    if (isEdit.value) {
      await updateNode(editId.value, node)
      ElMessage.success('节点已更新')
    } else {
      const res = await createNode(node)
      createdInstallNode = res.data
      ElMessage.success('节点已添加')
    }
    dialogVisible.value = false
    fetchData()
  } catch (error) {
    console.error('保存节点失败:', error)
  } finally {
    submitLoading.value = false
  }
}

const openCreatedNodeInstall = () => {
  if (!createdInstallNode) return
  const row = createdInstallNode
  createdInstallNode = null
  showInstallCommand(row)
}

const handleDelete = async (row) => {
  try {
    await confirmPanelAction('确定要删除节点「' + row.name + '」吗？', '删除节点', {
      confirmButtonText: '删除节点',
      confirmButtonClass: 'el-button--danger',
      cancelButtonText: '取消',
      type: 'warning'
    })
    await deleteNode(row.id)
    ElMessage.success('节点已删除')
    fetchData()
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') console.error('删除节点失败:', error)
  }
}

const handleNodeAction = (action, row) => {
  if (action === 'gost') {
    gostNode.value = row
    gostDialogVisible.value = true
  }
  if (action === 'upgrade') {
    upgradeNode.value = row
    upgradeDialogVisible.value = true
  }
  if (action === 'nft') {
    nftNode.value = row
    nftDialogVisible.value = true
  }
  if (action === 'edit') openDialog(row)
  if (action === 'delete') handleDelete(row)
  if (action === 'copy') {
    openDialog(row)
    editId.value = null
    form.name = row.name.slice(0, 95) + '-copy'
  }
}

const handleViewConfig = async (row) => {
  configNode.value = row
  configActiveTab.value = 'services'
  configLoading.value = true
  configError.value = false
  configDialogVisible.value = true
  try {
    const res = await getNodeConfig(row.id)
    nodeConfig.value = res.data
  } catch {
    configError.value = true
  } finally {
    configLoading.value = false
  }
}

let refreshTimer
onMounted(() => {
  fetchData()
  refreshTimer = setInterval(() => fetchData(true), 5000)
})
onBeforeUnmount(() => {
  clearInterval(refreshTimer)
  installDialogVisible.value = false
  resetInstall()
})
</script>

<style scoped src="../inventory.css"></style>

<style scoped>
.node-address-help { margin: 0 0 12px; line-height: 1.5; }
.node-traffic-help { margin: 6px 0 0; font-size: 12px; line-height: 1.5; }
.node-traffic-status { margin-top: 4px; }
.config-empty { text-align: center; padding: 24px 12px; }
.config-entry { border: 1px solid var(--ops-border); border-radius: 8px; margin-bottom: 8px; min-width: 0; }
.config-entry summary { padding: 10px 12px; cursor: pointer; overflow-wrap: anywhere; }
.config-entry summary > span { margin-left: 8px; font-size: 12px; }
.config-code {
  border-radius: 6px;
  background: var(--ops-inset);
  color: var(--ops-text);
  margin: 0;
  padding: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: 12px/1.6 var(--ops-mono);
}
.config-entry .config-code { margin: 0 8px 8px; }
.install-content { display: grid; gap: 16px; min-width: 0; }
.install-field { display: grid; min-width: 0; gap: 8px; }
.install-field h3, .install-field label { margin: 0; color: var(--ops-text); font-size: 14px; font-weight: 650; }
.install-token, .install-command {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
  color: var(--ops-text);
  overflow-wrap: anywhere;
  font: 13px/1.6 var(--ops-mono);
}
.install-token { color: var(--ops-accent); user-select: all; }
.install-command { resize: vertical; white-space: pre-wrap; }
.install-feedback { margin: 0; padding: 12px 0; }
.install-error { color: var(--ops-danger); }
.install-copy-feedback { margin: 0; color: var(--ops-accent); font-size: 13px; }
.install-hint { margin: 0; font-size: 12px; }
.install-replace-note {
  margin: 0;
  padding: 8px 12px;
  border-inline-start: 2px solid var(--ops-warning);
  border-radius: 6px;
  background: var(--ops-inset);
  color: var(--ops-warning);
  font-size: 13px;
}
</style>
