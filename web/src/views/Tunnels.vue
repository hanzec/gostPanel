<template>
  <div class="ops-page">
    <PageHeader eyebrow="RELAY TUNNELS" title="隧道管理" description="在入口与出口节点之间建立 Relay 链路，管理隧道协议和运行状态。">
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
        <span class="ops-muted">{{ filtered ? '调整搜索条件后重试。' : '选择两个节点并配置 Relay 端口，即可建立转发路径。' }}</span>
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
                  <span><span class="tunnel-route-label">入口</span>{{ tunnel.entry_node?.name || '节点 #' + tunnel.entry_node_id }}</span>
                  <el-icon class="tunnel-route-arrow" aria-hidden="true"><Right /></el-icon>
                  <span><span class="tunnel-route-label">出口</span>{{ tunnel.exit_node?.name || '节点 #' + tunnel.exit_node_id }}</span>
                </div>
              </td>
              <td role="cell">
                <span class="inventory-cell-label" aria-hidden="true">Relay</span>
                <strong class="ops-mono">{{ tunnel.protocol?.toUpperCase() || '—' }}</strong>
                <span class="inventory-secondary ops-mono ops-muted">:{{ tunnel.relay_port }}</span>
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
                        <el-dropdown-item command="edit">编辑隧道</el-dropdown-item>
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

    <el-dialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑隧道' : '添加隧道'"
      width="min(600px, calc(100vw - 24px))"
      :close-on-click-modal="false"
    >
      <p v-if="isEdit" class="tunnel-dialog-hint ops-muted">入口和出口在创建时确定，可以在这里调整名称、协议和 Relay 端口。</p>
      <div v-else-if="nodeOptionsError" class="inventory-error tunnel-dialog-error" role="alert">
        <span>无法加载节点选项。</span>
        <el-button :loading="nodeOptionsLoading" @click="fetchNodes">重新加载节点</el-button>
      </div>
      <p v-else-if="!nodeOptionsLoading && nodeList.length < 2" class="tunnel-dialog-hint ops-muted">
        建立隧道至少需要两个节点。<RouterLink to="/nodes" class="tunnel-link" @click="dialogVisible = false">前往节点管理</RouterLink>
      </p>
      <el-form ref="formRef" :model="form" :rules="formRules" label-position="top" @submit.prevent="handleSubmit">
        <div class="inventory-form-grid">
          <el-form-item label="隧道名称" prop="name" class="inventory-form-wide">
            <el-input v-model="form.name" placeholder="例如 生产环境中继" />
          </el-form-item>
          <el-form-item label="入口节点" prop="entry_node_id">
            <el-select v-model="form.entry_node_id" placeholder="选择入口节点" :loading="nodeOptionsLoading" :disabled="isEdit">
              <el-option
                v-for="node in nodeList"
                :key="node.id"
                :label="node.name"
                :value="node.id"
                :disabled="node.id === form.exit_node_id"
              />
            </el-select>
          </el-form-item>
          <el-form-item label="出口节点" prop="exit_node_id">
            <el-select v-model="form.exit_node_id" placeholder="选择出口节点" :loading="nodeOptionsLoading" :disabled="isEdit">
              <el-option
                v-for="node in nodeList"
                :key="node.id"
                :label="node.name"
                :value="node.id"
                :disabled="node.id === form.entry_node_id"
              />
            </el-select>
          </el-form-item>
          <el-form-item label="协议" prop="protocol">
            <el-select v-model="form.protocol">
              <el-option v-for="protocol in protocols" :key="protocol" :label="protocol.toUpperCase()" :value="protocol" />
            </el-select>
          </el-form-item>
          <el-form-item label="Relay 端口" prop="relay_port">
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
        <el-button type="primary" :loading="submitLoading" @click="handleSubmit">{{ isEdit ? '保存更改' : '添加隧道' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, Connection, Plus, Refresh, Right, Search, VideoPause, VideoPlay, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getTunnelList, createTunnel, updateTunnel, deleteTunnel, startTunnel, stopTunnel } from '@/api/tunnel'
import { getNodeList } from '@/api/node'

const tunnelStates = {
  running: { label: '运行中', icon: VideoPlay, tone: 'success' },
  stopped: { label: '已停止', icon: VideoPause },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}
const protocols = ['tcp', 'udp', 'tls', 'mtls', 'ws', 'mws', 'quic']

const nodeList = ref([])
const nodeOptionsLoading = ref(false)
const nodeOptionsError = ref(false)
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
const submitLoading = ref(false)
const formRef = ref(null)
const form = reactive({ name: '', entry_node_id: '', exit_node_id: '', protocol: 'tcp', relay_port: 8443, remark: '' })
const formRules = {
  name: [{ required: true, message: '请输入隧道名称', trigger: 'blur' }],
  entry_node_id: [{ required: true, message: '请选择入口节点', trigger: 'change' }],
  exit_node_id: [{ required: true, message: '请选择出口节点', trigger: 'change' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  relay_port: [{ required: true, message: '请输入 Relay 端口', trigger: 'blur' }]
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
  Object.assign(form, row ? {
    name: row.name,
    entry_node_id: row.entry_node_id,
    exit_node_id: row.exit_node_id,
    protocol: row.protocol || 'tcp',
    relay_port: row.relay_port || 8443,
    remark: row.remark || ''
  } : {
    name: '',
    entry_node_id: '',
    exit_node_id: '',
    protocol: 'tcp',
    relay_port: 8443,
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
      await updateTunnel(editId.value, form)
      ElMessage.success('隧道已更新')
    } else {
      await createTunnel(form)
      ElMessage.success('隧道已添加')
    }
    dialogVisible.value = false
    fetchData()
  } catch (error) {
    console.error('保存隧道失败:', error)
  } finally {
    submitLoading.value = false
  }
}

const handleDelete = async (row) => {
  try {
    await ElMessageBox.confirm('确定要删除隧道「' + row.name + '」吗？规则占用中的隧道无法删除。', '删除隧道', {
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
  if (action === 'edit') openDialog(row)
  if (action === 'delete') handleDelete(row)
  if (action === 'copy') {
    openDialog(row)
    editId.value = null
    form.name = row.name.slice(0, 95) + '-copy'
    form.relay_port = row.relay_port < 65535 ? row.relay_port + 1 : 8443
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
