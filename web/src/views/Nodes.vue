<template>
  <div class="ops-page">
    <div v-if="detailId" ref="detailPage" class="ops-page">
      <nav class="node-detail-crumb" aria-label="节点详情导航">
        <RouterLink :to="nodesLocation" class="node-detail-back"><el-icon aria-hidden="true"><ArrowLeft /></el-icon>返回节点列表</RouterLink>
      </nav>
      <NodeDetails v-if="detailNode" :node="detailNode" @action="handleNodeAction($event, detailNode)" />
      <section v-else class="ops-panel inventory-state">
        <template v-if="detailError">
          <p role="alert">无法读取节点详情。</p>
          <el-button @click="fetchDetail()">重新加载</el-button>
        </template>
        <template v-else>
          <el-skeleton :rows="4" animated />
          <span class="ops-muted" role="status">正在读取节点详情</span>
        </template>
      </section>
    </div>

    <PageHeader v-else title="节点管理">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="fetchData()">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openDialog()">添加节点</el-button>
      </template>
    </PageHeader>

    <MetricStrip v-if="!detailId" label="节点统计" :items="nodeMetrics" />

    <section v-if="!detailId" ref="inventoryPanel" class="ops-panel inventory-panel" aria-labelledby="node-inventory-title">
      <div class="inventory-heading">
        <h2 id="node-inventory-title" tabindex="-1">节点清单</h2>
        <form class="inventory-filters" aria-label="筛选节点" @submit.prevent="handleSearch">
          <div class="inventory-field">
            <el-input id="node-keyword" v-model="searchKeyword" aria-label="节点名称" :prefix-icon="Search" clearable placeholder="搜索名称" @clear="handleSearch" />
          </div>
          <div class="inventory-field">
            <el-select v-model="searchStatus" aria-label="健康状态" placeholder="全部状态" clearable @change="handleSearch">
              <el-option label="在线" value="online" />
              <el-option label="离线" value="offline" />
              <el-option label="异常" value="error" />
            </el-select>
          </div>
          <el-button native-type="submit">筛选</el-button>
        </form>
      </div>

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
              <th scope="col" role="columnheader">Gost 服务</th>
              <th scope="col" role="columnheader">操作</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            <tr v-for="node in nodeList" :key="node.id" class="node-list-row" role="row" @click="router.push(nodeLocation(node.id))">
              <td class="inventory-resource" role="cell">
                <RouterLink
                  :id="'node-link-' + node.id"
                  :to="nodeLocation(node.id)"
                  class="inventory-row-name node-row-link"
                  :aria-label="'查看节点详情：' + node.name"
                  @click.stop
                >
                  <el-icon class="inventory-row-icon" aria-hidden="true"><Monitor /></el-icon>
                  <strong>{{ node.name }}</strong>
                  <el-icon class="node-row-arrow" aria-hidden="true"><ArrowRight /></el-icon>
                </RouterLink>
                <span class="inventory-secondary ops-mono ops-muted">#{{ node.id }}</span>
                <span v-if="node.region" class="inventory-secondary ops-muted">{{ formatRegion(node.region) }}</span>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">节点 IP</span>
                <strong class="ops-mono">{{ node.address || '待获取地址' }}</strong>
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
              <td class="inventory-service" role="cell">
                <span class="inventory-cell-label" aria-hidden="true">Gost 服务</span>
                <RouterLink
                  :to="{ ...nodeLocation(node.id), hash: '#node-gost-service' }"
                  class="inventory-service-link"
                  :aria-label="'查看 Gost 服务：' + node.name"
                  @click.stop
                >查看服务<el-icon aria-hidden="true"><ArrowRight /></el-icon></RouterLink>
              </td>
              <td class="inventory-actions-cell" role="cell">
                <div class="inventory-actions">
                  <el-button v-if="node.status !== 'online'" @click.stop="showInstallCommand(node)">安装</el-button>
                  <el-button @click.stop="openDialog(node)">配置</el-button>
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
            :pager-count="5"
            layout="prev, pager, next"
            @current-change="fetchData()"
          />
        </div>
      </div>
    </section>

    <PanelDialog
      v-model="dialogVisible"
      :title="isEdit ? '节点配置 · ' + editingNode.name : '添加节点'"
      :size="isEdit ? 'wide' : 'standard'"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitLoading"
      :show-close="!submitLoading"
      @closed="openCreatedNodeInstall"
    >
      <NodeSettingsForm v-if="!isEdit" ref="formRef" :form="form" :rules="rules" :has-traffic-limit="!!trafficLimitBytes" :error="submitError" @submit="handleSubmit" />
      <el-tabs v-else v-model="settingsTab" class="node-settings-tabs">
        <el-tab-pane label="基本设置" name="basic">
          <NodeSettingsForm ref="formRef" :form="form" :rules="rules" :has-traffic-limit="!!trafficLimitBytes" :error="submitError" @submit="handleSubmit" />
        </el-tab-pane>
        <el-tab-pane label="Gost 配置" name="gost" lazy>
          <div v-if="configLoading" class="inventory-state" role="status">
            <el-skeleton :rows="4" animated />
            <span class="ops-muted">正在读取节点配置</span>
          </div>
          <div v-else-if="configError" class="inventory-error" role="alert">
            <span>无法读取该节点的配置。</span>
            <el-button @click="fetchSettingsConfig">重新加载</el-button>
          </div>
          <el-tabs v-else v-model="configActiveTab">
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
        </el-tab-pane>
        <el-tab-pane label="nft 配置" name="nft" lazy>
          <NodeNftPreview v-if="dialogVisible && settingsTab === 'nft'" :key="editingNode.id" :node="liveEditingNode" />
        </el-tab-pane>
        <el-tab-pane label="流量校准" name="traffic" lazy>
          <NodeTrafficCalibration :key="editingNode.id" :node="liveEditingNode" @saved="refreshCurrentView()" />
        </el-tab-pane>
        <el-tab-pane label="危险操作" name="danger" lazy>
          <section class="ops-panel node-settings-danger">
            <h3>删除节点</h3>
            <p class="ops-muted">从面板移除此节点。删除后无法恢复。</p>
            <el-button type="danger" plain :loading="deleting" @click="handleDelete(editingNode)">删除节点</el-button>
          </section>
        </el-tab-pane>
      </el-tabs>
      <template #footer>
        <el-button :disabled="submitLoading || deleting" @click="dialogVisible = false">{{ isEdit ? '关闭' : '取消' }}</el-button>
        <el-button v-if="!isEdit || settingsTab === 'basic'" type="primary" :loading="submitLoading" @click="handleSubmit">{{ isEdit ? '保存更改' : '添加节点' }}</el-button>
      </template>
    </PanelDialog>

    <NodeUpgradeDialog v-if="upgradeNode" v-model="upgradeDialogVisible" :node="upgradeNode" @closed="upgradeNode = null" />

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
import { ref, reactive, computed, defineAsyncComponent, nextTick, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowLeft, ArrowRight, CopyDocument, Monitor, Plus, Refresh, Search } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import NodeDetails from '@/components/NodeDetails.vue'
import NodeSettingsForm from '@/components/NodeSettingsForm.vue'
import NodeTrafficCalibration from '@/components/NodeTrafficCalibration.vue'
import PageHeader from '@/components/PageHeader.vue'
import PanelDialog from '@/components/PanelDialog.vue'
import { getNode, getNodeList, createNode, updateNode, issueNodeRegistrationToken, deleteNode, getNodeConfig } from '@/api/node'
import { getPublicSystemConfig } from '@/api/system'
import { formatBytes, formatRegion, formatTime, nodeStates, trafficQuotaStates, trafficResetPeriods } from '@/utils/node-display'
import { confirmPanelAction } from '@/utils/panel-dialog'

const NodeUpgradeDialog = defineAsyncComponent(() => import('@/components/NodeUpgradeDialog.vue'))
const NodeNftPreview = defineAsyncComponent(() => import('@/components/NodeNftPreview.vue'))
const INSTALL_SCRIPT_ROOT = 'https://raw.githubusercontent.com/hanzec/yagp/'
const NODE_ONLINE_CODE = 10008
const configTabs = [
  { name: 'services', label: '服务' },
  { name: 'chains', label: '转发链' },
  { name: 'observers', label: '观察器' }
]
const GIB = 1024 ** 3

const route = useRoute()
const router = useRouter()
const nodesLocation = { name: 'Nodes' }
const nodeLocation = (id) => ({ ...nodesLocation, query: { node: id } })
const detailId = computed(() => {
  const id = route.query.node
  return typeof id === 'string' && /^[1-9]\d{0,9}$/.test(id) ? Number(id) : null
})
const detailPage = ref(null)
const inventoryPanel = ref(null)
const detailNode = ref(null)
const detailLoading = ref(false)
const detailError = ref(false)

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
const editingNode = ref(null)
const isEdit = computed(() => editingNode.value !== null)
const liveEditingNode = computed(() => {
  const row = editingNode.value
  if (detailNode.value?.id === row?.id) return detailNode.value
  return nodeList.value.find(node => node.id === row?.id) || row
})
const settingsTab = ref('basic')
const submitLoading = ref(false)
const submitError = ref('')
const deleting = ref(false)
const formRef = ref(null)
const form = reactive({ name: '', remark: '', manualAddresses: '', trafficLimitGiB: '', trafficResetPeriod: 'none' })
const originalTrafficLimitBytes = ref(0)
let originalTrafficResetPeriod = 'none'
let originalManualAddresses = []
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

const configActiveTab = ref('services')
const nodeConfig = ref(null)
const configLoading = ref(false)
const configError = ref(false)

const upgradeNode = ref(null)
const upgradeDialogVisible = ref(false)

const installDialogVisible = ref(false)
const currentInstallNode = ref(null)
const installNode = computed(() => {
  const row = currentInstallNode.value
  if (detailNode.value?.id === row?.id) return detailNode.value
  return nodeList.value.find(node => node.id === row?.id) || row
})
const registration = ref(null)
const installLoading = ref(false)
const installError = ref('')
const installCommandField = ref(null)
const pendingInstallTokens = new Map()
let createdInstallNode
let installSession = 0
let registrationTimer

const configSummary = (tab, entry) => {
  if (tab === 'chains') return (entry.hops?.length || 0) + ' 跳'
  if (tab === 'observers') return [entry.plugin?.type, entry.plugin?.addr].filter(Boolean).join(' · ')
  return [entry.addr, entry.handler?.type, entry.handler?.chain, (entry.forwarder?.nodes?.length || 0) + ' 个目标']
    .filter(Boolean).join(' · ')
}

const shellQuote = (value) => "'" + String(value).replaceAll("'", "'\"'\"'") + "'"

const installCommand = computed(() => {
  if (!registration.value) return ''
  const { host, token, version } = registration.value
  const script = INSTALL_SCRIPT_ROOT + (version ? 'refs/tags/' + encodeURIComponent(version) : 'HEAD') + '/scripts/install_node.sh'
  const pin = version ? 'YAGP_DAEMON_VERSION=' + shellQuote(version) + ' ' : ''
  return 'curl -fsSL ' + shellQuote(script) + ' | ' + pin + 'bash -s -- ' +
    '--host ' + shellQuote(host) + ' --token ' + shellQuote(token)
})

const showInstallCommand = (row) => {
  if (row.status === 'online') return
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
  installError.value = '令牌已过期，请重新生成。'
}

const prepareInstall = async () => {
  const node = installNode.value
  if (!node || node.status === 'online' || installLoading.value) return
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
    const host = config.data?.host?.trim()
    if (!host) {
      installError.value = '请配置面板 host 和 report.listen 后重试。'
      return
    }

    const result = await requestInstallToken(node.id).catch(error => {
      if (current() && error.response?.data?.code === NODE_ONLINE_CODE) {
        closeOnlineInstall(error.response.data.message)
        refreshCurrentView(true)
      }
      return null
    })
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
    registration.value = { host, token, expiresAt, version: config.data?.daemonVersion }
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
  if (copied) ElMessage.success('安装命令已复制')
  else ElMessage.warning('已选中安装命令，请使用系统复制功能。')
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

const fetchDetail = async (isSilent = false) => {
  if (isSilent && (detailLoading.value || detailError.value)) return
  const id = detailId.value
  detailLoading.value = true
  detailError.value = false
  try {
    const res = await getNode(id)
    if (detailId.value === id) detailNode.value = res.data
  } catch (error) {
    if (detailId.value === id && (!isSilent || error.response?.status === 404)) {
      detailNode.value = null
      detailError.value = true
    }
  } finally {
    if (detailId.value === id) detailLoading.value = false
  }
}

const refreshCurrentView = (isSilent = false) => detailId.value ? fetchDetail(isSilent) : fetchData(isSilent)

const closeOnlineInstall = (message) => {
  installDialogVisible.value = false
  ElMessage.warning(message || '节点已在线，无法重新安装')
}

watch([installDialogVisible, () => installNode.value?.status], ([visible, status]) => {
  if (visible && status === 'online') closeOnlineInstall()
})

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
  editingNode.value = row
  settingsTab.value = 'basic'
  configActiveTab.value = 'services'
  nodeConfig.value = null
  configLoading.value = false
  configError.value = false
  originalTrafficLimitBytes.value = row?.traffic_limit_bytes ?? 0
  originalTrafficResetPeriod = row?.traffic_reset_period || 'none'
  originalManualAddresses = row?.manual_addresses || []
  submitError.value = ''
  Object.assign(form, {
    name: row?.name ?? '',
    remark: row?.remark || '',
    manualAddresses: originalManualAddresses.join('\n'),
    trafficLimitGiB: originalTrafficLimitGiB.value,
    trafficResetPeriod: originalTrafficResetPeriod
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value) return
  submitLoading.value = true
  submitError.value = ''
  try {
    const valid = await formRef.value.validate().catch(() => false)
    if (!valid) return

    const node = { name: form.name, remark: form.remark }
    const manualAddresses = form.manualAddresses.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
    if (manualAddresses.length !== originalManualAddresses.length ||
      manualAddresses.some((value, index) => value !== originalManualAddresses[index])) {
      node.manual_addresses = manualAddresses
    }
    if (!isEdit.value || trafficLimitBytes.value !== originalTrafficLimitBytes.value) {
      node.traffic_limit_bytes = trafficLimitBytes.value
    }
    if (!isEdit.value || form.trafficResetPeriod !== originalTrafficResetPeriod) {
      node.traffic_reset_period = form.trafficResetPeriod
    }
    if (isEdit.value) {
      await updateNode(editingNode.value.id, node)
      ElMessage.success('节点已更新')
    } else {
      const res = await createNode(node)
      createdInstallNode = res.data
      ElMessage.success('节点已添加')
    }
    dialogVisible.value = false
    refreshCurrentView()
  } catch (error) {
    submitError.value = error.response?.data?.message || error.message || '无法保存节点，请重试。'
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
  if (deleting.value) return
  deleting.value = true
  try {
    await confirmPanelAction('确定要删除节点「' + row.name + '」吗？', '删除节点', {
      confirmButtonText: '删除节点',
      confirmButtonClass: 'el-button--danger',
      cancelButtonText: '取消',
      type: 'warning'
    })
    await deleteNode(row.id)
    ElMessage.success('节点已删除')
    dialogVisible.value = false
    nodeList.value = nodeList.value.filter(node => node.id !== row.id)
    if (detailId.value === row.id) router.replace(nodesLocation)
    else if (!detailId.value) fetchData()
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') console.error('删除节点失败:', error)
  } finally {
    deleting.value = false
  }
}

const handleNodeAction = (action, row) => {
  if (action === 'install') showInstallCommand(row)
  if (action === 'config') openDialog(row)
  if (action === 'upgrade') {
    upgradeNode.value = row
    upgradeDialogVisible.value = true
  }
}

const fetchSettingsConfig = async () => {
  const row = editingNode.value
  configLoading.value = true
  configError.value = false
  try {
    const res = await getNodeConfig(row.id)
    if (editingNode.value === row) nodeConfig.value = res.data
  } catch {
    if (editingNode.value === row) configError.value = true
  } finally {
    if (editingNode.value === row) configLoading.value = false
  }
}

watch(settingsTab, tab => {
  if (tab === 'gost' && !nodeConfig.value && !configLoading.value) fetchSettingsConfig()
})

watch(detailId, async (id, previous) => {
  if (previous !== undefined) {
    createdInstallNode = null
    dialogVisible.value = false
    installDialogVisible.value = false
    upgradeDialogVisible.value = false
    upgradeNode.value = null
  }
  detailNode.value = nodeList.value.find(node => node.id === id) || null
  detailLoading.value = detailError.value = false
  refreshCurrentView()
  if (previous === undefined) return
  if (id && route.hash === '#node-gost-service') return
  await nextTick()
  const focusTarget = id
    ? detailPage.value?.querySelector('.node-detail-back')
    : inventoryPanel.value?.querySelector('#node-link-' + previous) || inventoryPanel.value?.querySelector('#node-inventory-title')
  focusTarget?.focus()
}, { immediate: true })

watch([() => detailNode.value?.id, () => route.hash], ([id, hash]) => {
  if (!id || hash !== '#node-gost-service') return
  detailPage.value?.querySelector('#node-gost-title')?.focus()
}, { flush: 'post' })

let refreshTimer
onMounted(() => {
  refreshTimer = setInterval(() => refreshCurrentView(true), 5000)
})
onBeforeUnmount(() => {
  clearInterval(refreshTimer)
  installDialogVisible.value = false
  resetInstall()
})
</script>

<style scoped src="../inventory.css"></style>

<style scoped>
.inventory-heading { align-items: center; flex-wrap: wrap; }
.inventory-heading h2 { margin: 0; }
.inventory-filters { width: min(100%, 380px); gap: 8px; padding: 0; }
.inventory-filters .inventory-field { flex-basis: 180px; }
.inventory-filters .inventory-field + .inventory-field { flex-basis: 120px; }
.inventory-scroll { container-type: inline-size; }
.node-list-row { cursor: pointer; }
.inventory-table .node-list-row:focus-within { background: var(--ops-raised); }
.node-row-link { width: fit-content; min-height: 30px; color: var(--ops-text); text-decoration: none; }
.node-row-link:hover, .node-row-link:focus-visible { color: var(--ops-accent); text-decoration: underline; }
.node-row-arrow { color: var(--ops-accent); flex: none; }
.inventory-service-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 36px;
  font-weight: var(--ops-weight-semibold);
  text-decoration: none;
  white-space: nowrap;
}
.inventory-service-link:hover, .inventory-service-link:focus-visible { text-decoration: underline; }
.node-detail-crumb, .node-detail-back { display: flex; align-items: center; gap: 8px; }
.node-detail-crumb { color: var(--ops-muted); font-size: var(--ops-text-sm); }
.node-detail-back { min-height: 44px; color: var(--ops-text); text-decoration: none; }
.node-detail-back:hover { color: var(--ops-accent); text-decoration: underline; }
@media (min-width: 701px) {
  @container (max-width: 720px) {
    .inventory-table th, .inventory-table td { padding-inline: 8px; }
    .inventory-table :is(th, td):first-child { padding-inline-start: 14px; }
    .inventory-table :is(th, td):last-child { padding-inline-end: 14px; }
    .inventory-table :is(th, td):nth-child(2) { width: 27%; }
    .node-row-link .el-icon { display: none; }
  }
}
@media (max-width: 700px) {
  .inventory-heading .inventory-filters { width: 100%; padding: 0; }
  .inventory-filters .inventory-field { flex: 2 1 108px; }
  .inventory-filters .inventory-field + .inventory-field { flex: 1 1 94px; }
  .node-row-link, .inventory-service-link { min-height: 44px; }
  .inventory-status { align-self: center; }
  .inventory-actions-cell { grid-column: 2; align-self: end; }
  .inventory-actions { justify-content: flex-end; }
}
.node-settings-tabs :deep(.el-tabs__item) { min-height: 44px; }
@media (max-width: 480px) {
  .node-settings-tabs > :deep(.el-tabs__header .el-tabs__item) { padding-inline: 6px; font-size: var(--ops-text-sm); }
}
.node-settings-danger { display: grid; justify-items: start; gap: 12px; padding: 16px; }
.node-settings-danger h3 { color: var(--ops-danger); font-size: var(--ops-text-base); }
.node-settings-danger .el-button { min-height: 44px; }
.node-traffic-status { margin-top: 4px; }
.config-empty { text-align: center; padding: 24px 12px; }
.config-entry { border: 1px solid var(--ops-border); border-radius: var(--ops-radius-inset); margin-bottom: 8px; min-width: 0; }
.config-entry summary { padding: 10px 12px; cursor: pointer; overflow-wrap: anywhere; }
.config-entry summary > span { margin-left: 8px; font-size: var(--ops-text-xs); }
.config-code {
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-text);
  margin: 0;
  padding: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: var(--ops-text-sm)/var(--ops-leading-body) var(--ops-mono);
}
.config-entry .config-code { margin: 0 8px 8px; }
.install-content { display: grid; gap: 16px; min-width: 0; }
.install-field { display: grid; min-width: 0; gap: 8px; }
.install-field h3, .install-field label { margin: 0; color: var(--ops-text); font-size: var(--ops-text-body); font-weight: var(--ops-weight-semibold); }
.install-token, .install-command {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-text);
  overflow-wrap: anywhere;
  font: var(--ops-text-sm)/var(--ops-leading-body) var(--ops-mono);
}
.install-token { color: var(--ops-accent); user-select: all; }
.install-command { resize: vertical; white-space: pre-wrap; }
.install-feedback { margin: 0; padding: 12px 0; }
.install-error { color: var(--ops-danger); }
.install-hint { margin: 0; font-size: var(--ops-text-xs); }
.install-replace-note {
  margin: 0;
  padding: 8px 12px;
  border-inline-start: 2px solid var(--ops-warning);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  color: var(--ops-warning);
  font-size: var(--ops-text-sm);
}
</style>
