<template>
  <div class="ops-page">
    <PageHeader title="隧道管理" description="连接已创建的源端入口和目的端入口，统一管理两端的转发配置。">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="fetchData()">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openDialog()">添加隧道</el-button>
      </template>
    </PageHeader>

    <MetricStrip label="隧道统计" :items="tunnelMetrics" />

    <section class="ops-panel tunnel-panel" aria-labelledby="tunnel-inventory-title">
      <div class="inventory-heading">
        <div>
          <h2 id="tunnel-inventory-title">链路清单</h2>
          <p class="ops-muted">检查流量路径与出口 Relay 监听配置。</p>
        </div>
        <span class="inventory-badge ops-mono">RELAY</span>
      </div>

      <form class="ops-toolbar inventory-filters" aria-label="筛选隧道" @submit.prevent="handleSearch">
        <div class="inventory-field">
          <label for="tunnel-keyword">隧道名称</label>
          <el-input id="tunnel-keyword" v-model="searchKeyword" :prefix-icon="Search" clearable placeholder="搜索名称" @clear="handleSearch" />
        </div>
        <div class="inventory-field">
          <label for="tunnel-node">关联节点</label>
          <el-select id="tunnel-node" v-model="searchNodeId" placeholder="全部节点" :loading="nodeOptionsLoading" clearable @change="handleSearch">
            <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
          </el-select>
        </div>
        <div class="inventory-field">
          <label for="inventory-status">运行状态</label>
          <el-select id="inventory-status" v-model="searchStatus" placeholder="全部状态" clearable @change="handleSearch">
            <el-option label="运行中" value="running" />
            <el-option label="已停止" value="stopped" />
            <el-option label="异常" value="error" />
          </el-select>
        </div>
        <el-button native-type="submit" :icon="Search">筛选</el-button>
      </form>

      <div v-if="loadError" class="inventory-error" role="alert">
        <span>无法加载链路清单，请重试。</span>
        <el-button :loading="loading" @click="fetchData()">重新加载</el-button>
      </div>
      <div v-if="nodeOptionsError" class="inventory-error" role="alert">
        <span>无法加载节点选项。</span>
        <el-button :loading="nodeOptionsLoading" @click="fetchNodes">重新加载节点</el-button>
      </div>

      <div v-if="loading && !tunnelList.length && !loadError" class="inventory-state" role="status">
        <el-skeleton :rows="4" animated />
        <span class="ops-muted">正在加载隧道</span>
      </div>
      <div v-else-if="!tunnelList.length && !loadError" class="inventory-state">
        <el-icon class="inventory-state-icon" aria-hidden="true"><Connection /></el-icon>
        <p>{{ filtered ? '没有匹配的隧道' : '尚未建立隧道' }}</p>
        <span class="ops-muted">{{ filtered ? '调整搜索条件后重试。' : '先在入口管理创建两个端点，再在这里建立转发路径。' }}</span>
        <el-button v-if="filtered" @click="clearFilters">清除筛选</el-button>
        <el-button v-else type="primary" @click="openDialog()">添加隧道</el-button>
      </div>
      <div v-else-if="tunnelList.length" class="ops-table-scroll inventory-scroll" :aria-busy="loading">
        <table class="inventory-table" role="table" aria-labelledby="tunnel-inventory-title">
          <thead role="rowgroup">
            <tr role="row">
              <th scope="col" role="columnheader">隧道</th>
              <th scope="col" role="columnheader">转发路径</th>
              <th scope="col" role="columnheader">Relay</th>
              <th scope="col" role="columnheader">状态</th>
              <th scope="col" role="columnheader">操作</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            <tr v-for="tunnel in tunnelList" :key="tunnel.id" role="row">
              <td class="inventory-resource" role="cell">
                <span class="inventory-row-name">
                  <el-icon class="inventory-row-icon" aria-hidden="true"><Connection /></el-icon>
                  <strong>{{ tunnel.name }}</strong>
                </span>
                <span class="inventory-secondary ops-muted"><span class="ops-mono">#{{ tunnel.id }}</span><span v-if="tunnel.remark"> · {{ tunnel.remark }}</span></span>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">转发路径</span>
                <div class="tunnel-route">
                  <span>
                    <span class="tunnel-route-label">源端 · {{ tunnel.entry_node?.name || '节点 #' + tunnel.entry_node_id }}</span>
                    {{ tunnel.entry_rule?.name || tunnel.entry_node?.name || '节点 #' + tunnel.entry_node_id }}
                  </span>
                  <el-icon class="tunnel-route-arrow" aria-hidden="true"><Right /></el-icon>
                  <span>
                    <span class="tunnel-route-label">目的端 · {{ tunnel.exit_node?.name || '节点 #' + tunnel.exit_node_id }}</span>
                    {{ tunnel.exit_rule?.name || tunnel.exit_node?.name || '节点 #' + tunnel.exit_node_id }}
                  </span>
                </div>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">Relay</span>
                <strong class="ops-mono">{{ tunnel.protocol?.toUpperCase() || '—' }}</strong>
                <span class="inventory-secondary ops-mono ops-muted">{{ tunnel.exit_rule ? listenEndpoint(tunnel.exit_rule) : ':' + (tunnel.relay_port || '—') }}</span>
              </td>
              <td class="inventory-status" role="cell">
                <span class="ops-status" :class="'is-' + (tunnelStates[tunnel.status]?.tone || 'muted')">
                  {{ tunnelStates[tunnel.status]?.label || '未知' }}
                </span>
              </td>
              <td class="inventory-actions-cell" role="cell">
                <div class="inventory-actions">
                  <el-button
                    :type="tunnel.status === 'running' ? 'warning' : 'primary'"
                    plain
                    :loading="activeTunnelId === tunnel.id"
                    :disabled="activeTunnelId !== null && activeTunnelId !== tunnel.id"
                    @click="toggleTunnel(tunnel)"
                  >{{ tunnel.status === 'running' ? '停止' : '启动' }}</el-button>
                  <el-dropdown trigger="click" @command="handleTunnelAction($event, tunnel)">
                    <el-button :aria-label="'隧道 ' + tunnel.name + ' 的更多操作'">
                      更多<el-icon aria-hidden="true"><ArrowDown /></el-icon>
                    </el-button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="edit" :disabled="tunnel.status === 'running'">编辑隧道</el-dropdown-item>
                        <el-dropdown-item command="copy">复制隧道</el-dropdown-item>
                        <el-dropdown-item command="delete" divided class="inventory-danger">删除隧道</el-dropdown-item>
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
        <span class="ops-muted">共 {{ total }} 条隧道</span>
        <div class="inventory-page-controls">
          <el-select v-model="pageSize" class="inventory-page-size" aria-label="每页隧道数" @change="handleSearch">
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
      :title="isEdit ? '编辑隧道' : '添加隧道'"
      :close-on-click-modal="false"
    >
      <p v-if="legacyEdit" class="tunnel-dialog-hint ops-muted">此隧道直接关联节点，可调整协议和 Relay 端口。</p>
      <div v-else-if="entryOptionsError" class="inventory-error tunnel-dialog-error" role="alert">
        <span>无法加载入口选项。</span>
        <el-button :loading="entryOptionsLoading" @click="fetchEntrances">重新加载入口</el-button>
      </div>
      <p v-else class="tunnel-dialog-hint ops-muted">
        <template v-if="isEdit">端点变更将在启动时应用。</template>
        <template v-else-if="!entryOptionsLoading && (!sourceOptions.length || !destinationOptions.length)">源端或目的端缺少可用入口。</template>
        <template v-else>选定两端后，面板将配置源端与目的端并启动隧道。</template>
        <RouterLink to="/rules" class="tunnel-link" @click="dialogVisible = false">前往入口管理</RouterLink>
      </p>
      <el-form ref="formRef" :model="form" :rules="formRules" label-position="top" @submit.prevent="handleSubmit">
        <div class="inventory-form-grid">
          <el-form-item label="隧道名称" prop="name" class="inventory-form-wide">
            <el-input v-model="form.name" placeholder="例如 生产环境中继" />
          </el-form-item>
          <el-form-item v-if="!legacyEdit" label="源端入口" prop="entry_rule_id">
            <el-select v-model="form.entry_rule_id" placeholder="选择源端入口" :loading="entryOptionsLoading" filterable aria-describedby="source-entry-help">
              <el-option
                v-for="rule in sourceOptions"
                :key="rule.id"
                :label="entranceLabel(rule)"
                :value="rule.id"
                :disabled="rule.node_id === exitEntrance?.node_id"
              />
            </el-select>
            <span id="source-entry-help" class="tunnel-form-hint ops-muted">源端需要固定目标地址或 Clash 规则集。独立转发中的入口须先停止。</span>
          </el-form-item>
          <el-form-item v-if="!legacyEdit" label="目的端入口" prop="exit_rule_id">
            <el-select v-model="form.exit_rule_id" placeholder="选择目的端入口" :loading="entryOptionsLoading" filterable aria-describedby="destination-entry-help">
              <el-option
                v-for="rule in destinationOptions"
                :key="rule.id"
                :label="entranceLabel(rule)"
                :value="rule.id"
                :disabled="rule.node_id === entryEntrance?.node_id"
              />
            </el-select>
            <span id="destination-entry-help" class="tunnel-form-hint ops-muted">目的端须在另一节点、目标留空且监听可达。其协议和端口用于 Relay。</span>
          </el-form-item>
          <el-form-item v-if="legacyEdit" label="协议" prop="protocol">
            <el-select v-model="form.protocol">
              <el-option v-for="protocol in protocols" :key="protocol" :label="protocol.toUpperCase()" :value="protocol" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="legacyEdit" label="Relay 端口" prop="relay_port">
            <el-input-number v-model="form.relay_port" :min="1" :max="65535" controls-position="right" />
            <span class="tunnel-form-hint ops-muted">Relay 服务在出口节点监听。</span>
          </el-form-item>
          <el-form-item label="备注" prop="remark" class="inventory-form-wide">
            <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可记录链路用途" />
          </el-form-item>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitLoading" :disabled="!legacyEdit && (entryOptionsLoading || entryOptionsError)" @click="handleSubmit">{{ isEdit ? '保存更改' : '添加并启动' }}</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { computed, ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage } from 'element-plus'
import PanelDialog from '@/components/PanelDialog.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { ArrowDown, Connection, Plus, Refresh, Right, Search, VideoPause, VideoPlay, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getTunnelList, createTunnel, updateTunnel, deleteTunnel, startTunnel, stopTunnel } from '@/api/tunnel'
import { getNodeList } from '@/api/node'
import { getRuleList } from '@/api/rule'

const tunnelStates = {
  running: { label: '运行中', icon: VideoPlay, tone: 'success' },
  stopped: { label: '已停止', icon: VideoPause },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}
const protocols = ['tcp', 'udp', 'tls', 'mtls', 'ws', 'mws', 'quic']

const nodeList = ref([])
const nodeOptionsLoading = ref(false)
const nodeOptionsError = ref(false)
const entryList = ref([])
const entryOptionsLoading = ref(false)
const entryOptionsError = ref(false)
const tunnelList = ref([])
const loading = ref(true)
const loadError = ref(false)
const page = ref(1)
const pageSize = ref(10)
const total = ref(null)
const searchKeyword = ref('')
const searchNodeId = ref('')
const searchStatus = ref('')
const filtered = computed(() => Boolean(searchKeyword.value.trim() || searchNodeId.value || searchStatus.value))
const tunnelMetrics = computed(() => [
  { label: '筛选结果', value: total.value, detail: '全部匹配隧道', icon: Connection },
  ...Object.entries(tunnelStates).map(([status, state]) => ({
    ...state,
    value: total.value === null ? null : tunnelList.value.filter(tunnel => tunnel.status === status).length,
    detail: '当前页'
  }))
])

const activeTunnelId = ref(null)
const dialogVisible = ref(false)
const editId = ref(null)
const isEdit = computed(() => editId.value !== null)
const legacyEdit = ref(false)
const submitLoading = ref(false)
const formRef = ref(null)
const form = reactive({ name: '', entry_rule_id: null, exit_rule_id: null, protocol: 'tcp', relay_port: null, remark: '' })
const listenEndpoint = ({ listen_ip: ip, listen_port: port }) => (ip?.includes(':') ? '[' + ip + ']' : ip || '*') + ':' + port
const entranceLabel = rule => `${rule.name} · ${rule.node?.name || '节点 #' + rule.node_id} · ${rule.protocol.toUpperCase()} ${listenEndpoint(rule)}`
const entryEntrance = computed(() => entryList.value.find(rule => rule.id === form.entry_rule_id))
const exitEntrance = computed(() => entryList.value.find(rule => rule.id === form.exit_rule_id))
const availableEntrances = computed(() => entryList.value.filter(rule => rule.linked_tunnel_id
  ? rule.linked_tunnel_id === editId.value
  : !rule.tunnel_id && rule.status !== 'running'))
const sourceOptions = computed(() => availableEntrances.value.filter(rule => rule.targets?.length || rule.use_clash_ruleset))
const destinationOptions = computed(() => availableEntrances.value.filter(rule => !rule.targets?.length && !rule.use_clash_ruleset &&
  !['0.0.0.0', '::', '::1'].includes(rule.listen_ip) && !rule.listen_ip?.startsWith('127.')))
const validateEndpointNodes = (_rule, _value, callback) => callback(entryEntrance.value?.node_id &&
  entryEntrance.value.node_id === exitEntrance.value?.node_id ? new Error('源端和目的端须位于不同节点') : undefined)
const formRules = {
  name: [{ required: true, message: '请输入隧道名称', trigger: 'blur' }],
  entry_rule_id: [{ required: true, message: '请选择源端入口', trigger: 'change' }, { validator: validateEndpointNodes, trigger: 'change' }],
  exit_rule_id: [{ required: true, message: '请选择目的端入口', trigger: 'change' }, { validator: validateEndpointNodes, trigger: 'change' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  relay_port: [{ required: true, message: '请输入 Relay 端口', trigger: 'blur' }]
}

const fetchEntrances = async () => {
  if (entryOptionsLoading.value) return
  entryOptionsLoading.value = true
  try {
    const rows = []
    const size = 1000
    for (let page = 1; ; page++) {
      const { data } = await getRuleList({ page, pageSize: size })
      const batch = data.list || []
      rows.push(...batch)
      if (rows.length >= data.total || batch.length < size) break
    }
    entryList.value = rows
    if (form.entry_rule_id && !sourceOptions.value.some(rule => rule.id === form.entry_rule_id)) form.entry_rule_id = null
    if (form.exit_rule_id && !destinationOptions.value.some(rule => rule.id === form.exit_rule_id)) form.exit_rule_id = null
    entryOptionsError.value = false
  } catch {
    entryOptionsError.value = true
  } finally {
    entryOptionsLoading.value = false
  }
}

const fetchNodes = async () => {
  nodeOptionsLoading.value = true
  try {
    const res = await getNodeList({ pageSize: 100 })
    nodeList.value = res.data.list || []
    nodeOptionsError.value = false
  } catch {
    nodeOptionsError.value = true
  } finally {
    nodeOptionsLoading.value = false
  }
}

const fetchData = async (isSilent = false) => {
  if (isSilent && (loading.value || loadError.value)) return
  if (!isSilent) loading.value = true
  try {
    const res = await getTunnelList({
      page: page.value,
      pageSize: pageSize.value,
      node_id: searchNodeId.value,
      status: searchStatus.value,
      keyword: searchKeyword.value
    })
    tunnelList.value = res.data.list || []
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
  searchNodeId.value = ''
  searchStatus.value = ''
  handleSearch()
}

const openDialog = (row = null) => {
  editId.value = row?.id ?? null
  legacyEdit.value = !!row && !row.entry_rule_id
  Object.assign(form, {
    name: row?.name || '',
    entry_rule_id: row?.entry_rule_id ?? null,
    exit_rule_id: row?.exit_rule_id ?? null,
    protocol: row?.protocol || 'tcp',
    relay_port: row?.relay_port ?? null,
    remark: row?.remark || ''
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
  if (!legacyEdit.value) fetchEntrances()
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  submitLoading.value = true
  try {
    const { name, remark, entry_rule_id, exit_rule_id, protocol, relay_port } = form
    const payload = legacyEdit.value ? { name, remark, protocol, relay_port } : { name, remark, entry_rule_id, exit_rule_id }
    if (isEdit.value) {
      await updateTunnel(editId.value, payload)
      ElMessage.success('隧道已更新')
    } else {
      await createTunnel(payload)
      ElMessage.success('隧道已添加并启动')
    }
    dialogVisible.value = false
    fetchData()
  } catch (error) {
    console.error('保存隧道失败:', error)
    if (!isEdit.value) {
      fetchData()
      fetchEntrances()
    }
  } finally {
    submitLoading.value = false
  }
}

const handleDelete = async (row) => {
  try {
    await confirmPanelAction('确定要删除隧道「' + row.name + '」吗？关联的入口配置仍可在入口管理中查看。', '删除隧道', {
      confirmButtonText: '删除隧道',
      confirmButtonClass: 'el-button--danger',
      cancelButtonText: '取消',
      type: 'warning'
    })
    await deleteTunnel(row.id)
    ElMessage.success('隧道已删除')
    fetchData()
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') console.error('删除隧道失败:', error)
  }
}

const toggleTunnel = async (row) => {
  activeTunnelId.value = row.id
  try {
    if (row.status === 'running') {
      await stopTunnel(row.id)
      ElMessage.success('隧道已停止')
    } else {
      await startTunnel(row.id)
      ElMessage.success('隧道已启动')
    }
    await fetchData()
  } catch (error) {
    console.error('更新隧道状态失败:', error)
  } finally {
    activeTunnelId.value = null
  }
}

const handleTunnelAction = (action, row) => {
  if (action === 'edit' && row.status !== 'running') openDialog(row)
  if (action === 'delete') handleDelete(row)
  if (action === 'copy') {
    openDialog()
    form.name = row.name.slice(0, 95) + '-copy'
    form.remark = row.remark || ''
  }
}

let refreshTimer
onMounted(() => {
  fetchNodes()
  fetchData()
  refreshTimer = setInterval(() => fetchData(true), 5000)
})
onBeforeUnmount(() => clearInterval(refreshTimer))
</script>

<style scoped src="../inventory.css"></style>

<style scoped>
.tunnel-panel .inventory-field { flex-basis: 145px; }
.tunnel-panel .inventory-field:first-child { flex-basis: 200px; }
.tunnel-route { display: flex; align-items: center; gap: 8px; }
.tunnel-route-label { display: block; color: var(--ops-muted); font-size: 11px; margin-bottom: 2px; }
.tunnel-route-arrow { color: var(--ops-accent); flex: none; }
.tunnel-dialog-error { margin: 0 0 12px; }
.tunnel-dialog-hint { margin: 0 0 12px; }
.tunnel-link { color: var(--ops-accent); }
.tunnel-form-hint { font-size: 12px; line-height: 1.5; margin-top: 4px; }

@media (max-width: 700px) {
  .tunnel-panel .inventory-field { flex-basis: 118px; }
  .tunnel-panel .inventory-field:first-child { flex-basis: 100%; }
  .tunnel-route { gap: 6px; }
}
</style>
