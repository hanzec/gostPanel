<template>
  <div class="ops-page">
    <PageHeader eyebrow="NODE INVENTORY" title="节点管理" description="登记 Gost 实例，检查 gRPC 连通性，并管理节点桥接服务。">
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
          <p class="ops-muted">查看节点健康状态与累计上下行流量。</p>
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
              <th scope="col" role="columnheader">连接地址</th>
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
                <span class="inventory-cell-label" aria-hidden="true">gRPC 地址</span>
                <strong class="ops-mono">{{ nodeEndpoint(node) }}</strong>
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
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="handleSubmit">
        <div class="inventory-form-grid">
          <el-form-item label="节点名称" prop="name" class="inventory-form-wide">
            <el-input v-model="form.name" placeholder="例如 华东入口" />
          </el-form-item>
          <el-form-item label="IP 或域名" prop="address">
            <el-input v-model="form.address" placeholder="gost.example.com" />
          </el-form-item>
          <el-form-item label="gRPC 端口" prop="port">
            <el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" />
          </el-form-item>
          <el-form-item label="备注" prop="remark" class="inventory-form-wide">
            <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可记录节点用途或部署位置" />
          </el-form-item>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
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

    <el-dialog
      v-model="installDialogVisible"
      :title="'安装节点 · ' + (currentInstallNode?.name || '')"
      width="min(680px, calc(100vw - 24px))"
      :close-on-click-modal="false"
      @close="clearRegistration"
    >
      <p class="install-intro ops-muted">在目标服务器上以 root 身份执行安装命令。节点首次通过 TLS 注册时会自动获取节点 ID 和凭据。</p>
      <dl v-if="currentInstallNode" class="install-meta">
        <div><dt>节点 ID</dt><dd class="ops-mono">#{{ currentInstallNode.id }}</dd></div>
        <div><dt>gRPC 地址</dt><dd class="ops-mono">{{ nodeEndpoint(currentInstallNode) }}</dd></div>
      </dl>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="面板注册地址" for="node-panel-address">
          <el-input id="node-panel-address" v-model.trim="panelAddress" placeholder="panel.example.com:39101" spellcheck="false" />
        </el-form-item>
      </el-form>

      <div class="install-registration">
        <p class="ops-muted" role="status">
          <template v-if="registrationToken">
            令牌到期时间 <time :datetime="registrationExpiresAt">{{ formatTime(registrationExpiresAt) }}</time>
          </template>
          <template v-else-if="registrationExpiresAt">令牌已过期，请重新签发。</template>
          <template v-else>令牌在注册成功后立即失效。</template>
        </p>
        <el-button type="primary" :loading="issuingToken" :disabled="!panelAddress" @click="issueRegistrationToken">
          {{ registrationExpiresAt ? '重新签发令牌' : '签发注册令牌' }}
        </el-button>
      </div>

      <section class="install-command" aria-labelledby="node-install-command">
        <div class="install-command-heading">
          <h3 id="node-install-command">安装命令</h3>
          <el-button :icon="CopyDocument" :disabled="!installCommand || issuingToken" @click="copyInstallCommand">复制命令</el-button>
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
const INSTALL_SCRIPT_URL = 'https://raw.githubusercontent.com/hanzec/gostPanel/master/scripts/install_node.sh'
const nodeStates = {
  online: { label: '在线', icon: CircleCheck, tone: 'success' },
  offline: { label: '离线', icon: CircleClose },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}
const configTabs = [
  { name: 'services', label: '服务' },
  { name: 'chains', label: '转发链' },
  { name: 'observers', label: '观察器' }
]
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
const form = reactive({ name: '', address: '', port: 39000, remark: '' })
const rules = {
  name: [{ required: true, message: '请输入节点名称', trigger: 'blur' }],
  address: [{ required: true, message: '请输入 IP 或域名', trigger: 'blur' }],
  port: [{ required: true, message: '请输入端口', trigger: 'blur' }]
}

const configDialogVisible = ref(false)
const configActiveTab = ref('services')
const configNode = ref(null)
const nodeConfig = ref(null)
const configLoading = ref(false)
const configError = ref(false)

const gostNode = ref(null)
const gostDialogVisible = ref(false)

const installDialogVisible = ref(false)
const currentInstallNode = ref(null)
const panelAddress = ref('')
const registrationToken = ref('')
const registrationExpiresAt = ref('')
const issuingToken = ref(false)
let registrationTimer

const formatBytes = (bytes) => {
  if (!bytes) return '0 B'
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizes.length - 1)
  return Number((bytes / 1024 ** index).toFixed(2)) + ' ' + sizes[index]
}

const formatTime = (value) => dateFormatter.format(new Date(value))

const nodeEndpoint = (node) => {
  const address = node.address.includes(':') && !node.address.startsWith('[') ? '[' + node.address + ']' : node.address
  return address + ':' + node.port
}

const configSummary = (tab, entry) => {
  if (tab === 'chains') return (entry.hops?.length || 0) + ' 跳'
  if (tab === 'observers') return [entry.plugin?.type, entry.plugin?.addr].filter(Boolean).join(' · ')
  return [entry.addr, entry.handler?.type, entry.handler?.chain, (entry.forwarder?.nodes?.length || 0) + ' 个目标']
    .filter(Boolean).join(' · ')
}

const shellQuote = (value) => "'" + String(value).replaceAll("'", "'\"'\"'") + "'"

const installCommand = computed(() => {
  if (!panelAddress.value || !registrationToken.value) return ''
  return 'bash <(curl -fsSL ' + shellQuote(INSTALL_SCRIPT_URL) + ') ' +
    '--panel-address ' + shellQuote(panelAddress.value) + ' --token ' + shellQuote(registrationToken.value)
})

const showInstallCommand = async (row) => {
  currentInstallNode.value = { id: row.id, name: row.name, address: row.address, port: row.port }
  installDialogVisible.value = true
  if (!panelAddress.value) {
    const res = await getPublicSystemConfig().catch(() => null)
    if (!panelAddress.value && res) panelAddress.value = (res.data.reportAddress || '').trim()
  }
}

const clearRegistration = () => {
  clearTimeout(registrationTimer)
  registrationToken.value = ''
  registrationExpiresAt.value = ''
}

const issueRegistrationToken = async () => {
  const node = currentInstallNode.value
  if (!node || issuingToken.value) return
  issuingToken.value = true
  const res = await issueNodeRegistrationToken(node.id).catch(() => null)
  issuingToken.value = false
  if (!res || !installDialogVisible.value || currentInstallNode.value !== node) return

  clearRegistration()
  registrationExpiresAt.value = res.data.expires_at
  const remaining = Date.parse(registrationExpiresAt.value) - Date.now()
  if (remaining <= 0) return
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
  Object.assign(form, row ? {
    name: row.name,
    address: row.address,
    port: row.port,
    remark: row.remark || ''
  } : {
    name: '',
    address: '127.0.0.1',
    port: 39000,
    remark: ''
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  submitLoading.value = true
  try {
    if (isEdit.value) {
      await updateNode(editId.value, form)
      ElMessage.success('节点已更新')
    } else {
      await createNode(form)
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
  clearRegistration()
})
</script>

<style scoped src="../inventory.css"></style>

<style scoped>
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
.install-command { border: 1px solid var(--ops-border); border-radius: 8px; padding: 12px; }
.install-command-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.install-command-heading h3 { font-size: 14px; margin: 0; }
.install-command > :not(:first-child) { margin-top: 8px; }
.install-hint { font-size: 12px; margin: 10px 0 0; }
</style>
