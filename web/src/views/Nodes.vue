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

    <el-dialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑节点' : '添加节点'"
      width="min(600px, calc(100vw - 24px))"
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
    </el-dialog>

    <el-dialog v-model="configDialogVisible" :title="'节点配置 · ' + (configNode?.name || '')" width="min(760px, calc(100vw - 24px))">
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
    </el-dialog>

    <NodeGostDialog v-if="gostNode" v-model="gostDialogVisible" :node="gostNode" @closed="gostNode = null" />
    <NodeUpgradeDialog v-if="upgradeNode" v-model="upgradeDialogVisible" :node="upgradeNode" @closed="upgradeNode = null" />
    <NodeNftDialog v-if="nftNode" v-model="nftDialogVisible" :node="nftNode" @closed="nftNode = null" />

    <el-dialog
      v-model="installDialogVisible"
      :title="'安装节点 · ' + (installNode?.name || '')"
      width="min(680px, calc(100vw - 24px))"
      :close-on-click-modal="false"
      @close="resetInstall"
    >
      <p class="install-intro ops-muted">在目标服务器上以 root 身份执行安装命令。节点首次通过 TLS 注册时会自动获取节点 ID 和凭据，并探测 IP。</p>
      <dl v-if="installNode" class="install-meta">
        <div><dt>节点 ID</dt><dd class="ops-mono">#{{ installNode.id }}</dd></div>
      </dl>
      <el-form label-position="top" @submit.prevent>
        <el-form-item
          label="面板注册地址"
          for="node-panel-address"
          :error="panelAddress && !panelAddressValid ? '请输入主机:端口，通过 Traefik 时加上 /api/v1/grpc。' : ''"
        >
          <el-input
            id="node-panel-address"
            v-model.trim="panelAddress"
            placeholder="panel.example.com:443/api/v1/grpc"
            :disabled="loadingPanelAddress"
            spellcheck="false"
          />
        </el-form-item>
      </el-form>

      <div class="install-registration">
        <p class="ops-muted" role="status">
          <template v-if="loadingPanelAddress">正在读取面板注册地址。</template>
          <template v-else-if="issuingToken">正在签发注册令牌。</template>
          <template v-else-if="registrationToken">
            令牌到期时间 <time :datetime="registrationExpiresAt">{{ formatTime(registrationExpiresAt) }}</time>
          </template>
          <template v-else-if="registrationExpiresAt">令牌已过期，请重新签发。</template>
          <template v-else>令牌在注册成功后立即失效。</template>
        </p>
        <el-button :type="installCommand ? '' : 'primary'" :loading="issuingToken" :disabled="!panelAddressValid" @click="issueRegistrationToken">
          {{ registrationExpiresAt ? '重新签发令牌' : '签发注册令牌' }}
        </el-button>
      </div>
      <p v-if="registrationError" class="install-error" role="alert">签发失败，节点已保存。请重试签发。</p>

      <section class="install-command" aria-labelledby="node-install-command">
        <div class="install-command-heading">
          <h3 id="node-install-command">安装命令</h3>
          <el-button :type="installCommand ? 'primary' : ''" :icon="CopyDocument" :disabled="!installCommand || issuingToken" @click="copyInstallCommand">复制命令</el-button>
        </div>
        <pre v-if="installCommand" class="config-code"><code>{{ installCommand }}</code></pre>
        <p v-else class="ops-muted">填写面板地址并签发令牌后即可复制命令。</p>
      </section>
      <p class="install-hint ops-muted">请妥善保管安装命令。重新签发会撤销该节点上一枚未使用的令牌。</p>
      <p class="install-hint ops-muted">已安装的节点使用新令牌注册后会更换凭据，旧实例将失去访问权限。</p>

      <template #footer>
        <el-button @click="installDialogVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, reactive, computed, defineAsyncComponent, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, CircleCheck, CircleClose, CopyDocument, Monitor, Plus, Refresh, Search, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getNodeList, createNode, updateNode, issueNodeRegistrationToken, deleteNode, getNodeConfig } from '@/api/node'
import { getPublicSystemConfig } from '@/api/system'

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
const panelAddress = ref('')
const loadingPanelAddress = ref(false)
const registrationToken = ref('')
const registrationExpiresAt = ref('')
const issuingToken = ref(false)
const registrationError = ref(false)
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

const panelAddressValid = computed(() => {
  const address = panelAddress.value.replace(/\/api\/v1\/grpc$/, '')
  if (!address || /[\s/@?#\\]/.test(address)) return false
  try {
    const { hostname, port } = new URL('tcp://' + address)
    return Boolean(hostname && Number(port) > 0)
  } catch {
    return false
  }
})

const installCommand = computed(() => {
  if (!panelAddressValid.value || !registrationToken.value) return ''
  return 'curl -fsSL ' + shellQuote(INSTALL_SCRIPT_URL) + ' | bash -s -- ' +
    '--panel-address ' + shellQuote(panelAddress.value) + ' --token ' + shellQuote(registrationToken.value)
})

const showInstallCommand = async (row, autoIssue = false) => {
  resetInstall()
  const session = installSession
  currentInstallNode.value = row
  panelAddress.value = ''
  loadingPanelAddress.value = true
  installDialogVisible.value = true

  const res = await getPublicSystemConfig().catch(() => null)
  if (session !== installSession || !installDialogVisible.value) return
  panelAddress.value = res?.data?.reportAddress?.trim() || ''
  loadingPanelAddress.value = false
  if (autoIssue && panelAddressValid.value) await issueRegistrationToken()
}

const clearRegistration = () => {
  clearTimeout(registrationTimer)
  registrationToken.value = ''
  registrationExpiresAt.value = ''
}

const resetInstall = () => {
  installSession++
  loadingPanelAddress.value = false
  issuingToken.value = false
  registrationError.value = false
  clearRegistration()
}

const issueRegistrationToken = async () => {
  const node = currentInstallNode.value
  if (!node || !panelAddressValid.value || issuingToken.value) return
  const session = installSession
  issuingToken.value = true
  registrationError.value = false
  const res = await issueNodeRegistrationToken(node.id).catch(() => null)
  if (session !== installSession || !installDialogVisible.value) return
  issuingToken.value = false
  if (!res) {
    registrationError.value = true
    return
  }

  clearRegistration()
  registrationExpiresAt.value = res.data.expires_at
  const remaining = Date.parse(registrationExpiresAt.value) - Date.now()
  if (!(remaining > 0)) return
  registrationToken.value = res.data.token
  registrationTimer = setTimeout(() => { registrationToken.value = '' }, remaining)
}

const copyInstallCommand = async () => {
  if (!installCommand.value || Date.parse(registrationExpiresAt.value) <= Date.now()) return
  try {
    await navigator.clipboard.writeText(installCommand.value)
    ElMessage.success('安装命令已复制')
  } catch {
    ElMessage.error('复制失败，请手动选择命令')
  }
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
  showInstallCommand(row, true)
}

const handleDelete = async (row) => {
  try {
    await ElMessageBox.confirm('确定要删除节点「' + row.name + '」吗？', '删除节点', {
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
.install-intro { margin: 0 0 12px; }
.install-meta {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 2fr);
  gap: 12px;
  border-radius: 8px;
  background: var(--ops-inset);
  padding: 12px;
  margin: 0 0 14px;
}
.install-meta dt { font-size: 12px; color: var(--ops-muted); }
.install-meta dd { margin: 3px 0 0; overflow-wrap: anywhere; }
.install-registration { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 12px; }
.install-registration p { margin: 0; }
.install-error { color: var(--ops-danger); margin: 0 0 12px; }
.install-command { border: 1px solid var(--ops-border); border-radius: 8px; padding: 12px; }
.install-command-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.install-command-heading h3 { font-size: 14px; margin: 0; }
.install-command > :not(:first-child) { margin-top: 8px; }
.install-hint { font-size: 12px; margin: 10px 0 0; }
</style>
