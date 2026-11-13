<template>
  <div class="ops-page">
    <MetricStrip label="端点摘要" :items="summary" />

    <section class="ops-panel rule-panel" aria-labelledby="rule-list-title" :aria-busy="loading">
      <InventoryToolbar filter-label="端点筛选" @submit="applyFilters">
        <template #title>
          <h2 id="rule-list-title">端点</h2>
        </template>
        <template #filters>
          <div class="inventory-toolbar-field">
            <label for="rule-keyword">端点名称</label>
            <el-input id="rule-keyword" v-model="filters.keyword" :prefix-icon="Search" placeholder="输入名称" clearable />
          </div>
          <div class="inventory-toolbar-field">
            <label for="rule-node">所在节点</label>
            <el-select id="rule-node" v-model="filters.node_id" aria-label="所在节点" placeholder="所有节点" :loading="nodeOptionsLoading" :disabled="nodeOptionsLoading || nodeOptionsError" clearable>
              <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
            </el-select>
          </div>
          <div class="inventory-toolbar-field">
            <label for="rule-status">运行状态</label>
            <el-select id="rule-status" v-model="filters.status" aria-label="运行状态" placeholder="所有状态" clearable>
              <el-option label="运行中" value="running" />
              <el-option label="已停止" value="stopped" />
              <el-option label="异常" value="error" />
            </el-select>
          </div>
          <el-button text @click="clearFilters">重置</el-button>
          <el-button native-type="submit" :icon="Search">应用筛选</el-button>
        </template>
        <template #actions>
          <el-button type="primary" :icon="Plus" @click="openDialog()">新建端点</el-button>
        </template>
      </InventoryToolbar>

      <p v-if="nodeOptionsLoading && !dialogVisible" class="rule-node-status ops-muted" role="status">正在加载节点选项…</p>
      <div v-else-if="nodeOptionsError && !dialogVisible" class="rule-error" role="alert">
        <span>无法加载节点选项，请重新加载后按节点筛选或创建端点。</span>
        <el-button @click="fetchNodes">重新加载节点</el-button>
      </div>

      <div v-if="listError" class="rule-error" role="alert">
        <div>
          <strong>端点加载失败</strong>
          <p>{{ ruleList.length ? '下方保留了上次获取的结果。' : '请检查连接后重试。' }}</p>
        </div>
        <el-button :loading="loading" @click="fetchData()">重试</el-button>
      </div>

      <div v-if="!ruleList.length && loading" class="rule-placeholder" role="status">正在读取端点…</div>
      <div v-else-if="!ruleList.length && !listError" class="rule-placeholder">
        <el-icon aria-hidden="true"><Switch /></el-icon>
        <strong>{{ hasFilters ? '没有匹配的端点' : '还没有端点' }}</strong>
        <p>{{ hasFilters ? '调整条件，或清除当前筛选查看全部端点。' : '创建端点以设置监听地址，再到隧道管理建立转发路径。' }}</p>
        <el-button v-if="hasFilters" @click="clearFilters">清除筛选</el-button>
      </div>

      <div
        v-if="ruleList.length"
        class="ops-table-scroll rule-table-scroll"
        tabindex="0"
        role="region"
        aria-label="端点列表"
      >
        <table class="rule-table" role="table">
          <thead role="rowgroup">
            <tr role="row">
              <th scope="col" role="columnheader">端点</th>
              <th scope="col" role="columnheader">所在节点</th>
              <th scope="col" role="columnheader">监听</th>
              <th scope="col" role="columnheader">累计流量</th>
              <th scope="col" role="columnheader">状态</th>
              <th scope="col" role="columnheader">操作</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            <tr v-for="row in ruleList" :key="row.id" role="row">
              <th class="rule-name" scope="row" role="rowheader">
                <strong>{{ row.name }}</strong>
                <span class="rule-secondary">
                  <span class="ops-mono">#{{ row.id }}</span>
                  <span>{{ endpointRoles[row.role] || '未指定' }}</span>
                  <span v-if="row.tunnel_role">{{ row.tunnel_role === 'entry' ? '隧道源端' : '隧道目的端' }}</span>
                  <span v-if="row.linked_tunnels?.length > 1">关联 {{ row.linked_tunnels.length }} 条隧道</span>
                </span>
              </th>
              <td class="rule-path" role="cell" data-label="所在节点">
                <span class="rule-path-entry">
                  {{ row.node?.name || row.tunnel?.entry_node?.name || '未知节点' }}
                </span>
              </td>
              <td role="cell" data-label="监听">
                <span class="rule-protocol" :class="{ 'ops-mono': row.protocol !== 'red' }">{{ row.protocol === 'red' ? '透明转发（HTTP/HTTPS）' : (row.protocol || 'tcp').toUpperCase() }}</span>
                <span class="ops-mono">{{ formatEndpoint(row.listen_ip, row.listen_port) }}</span>
                <span v-if="connectEndpoints[row.id]" class="rule-secondary">
                  对外连接 <span class="ops-mono">{{ connectEndpoints[row.id] }}</span>
                </span>
                <span v-if="listenerChecks.has(row.id) && row.status !== 'running'" class="rule-secondary" role="status">
                  {{ listenerChecks.get(row.id).label }}
                </span>
              </td>
              <td role="cell" data-label="累计流量">
                <strong class="ops-mono rule-traffic-total">{{ formatBytes(row.total_bytes) }}</strong>
                <span class="rule-secondary ops-mono">
                  <span>入 {{ formatBytes(row.input_bytes) }}</span>
                  <span>出 {{ formatBytes(row.output_bytes) }}</span>
                </span>
              </td>
              <td role="cell" data-label="状态">
                <span class="ops-status" :class="statusClass[row.status] || 'is-muted'">
                  {{ statusText[row.status] || row.status || '未知' }}
                </span>
              </td>
              <td class="rule-actions-cell" role="cell" data-label="操作">
                <div class="rule-row-actions">
                  <el-button
                    v-if="!row.role && !hasTunnelBindings(row) && !row.use_clash_ruleset && row.targets?.length"
                    text
                    :type="row.status === 'running' ? 'warning' : 'success'"
                    :loading="pendingRuleIds.has(row.id)"
                    :aria-label="(row.status === 'running' ? '停止 ' : '启动 ') + row.name"
                    @click="toggleRule(row)"
                  >
                    {{ row.status === 'running' ? '停止' : '启动' }}
                  </el-button>
                  <el-button v-if="canEditEndpoint(row)" text :aria-label="'编辑 ' + row.name" @click="openDialog(row)">编辑</el-button>
                  <RouterLink v-else class="rule-form-link" to="/tunnels">隧道管理</RouterLink>
                  <el-dropdown trigger="click" @command="handleRowCommand(row, $event)">
                    <el-button text :aria-label="row.name + '的更多操作'">
                      更多 <el-icon aria-hidden="true"><ArrowDown /></el-icon>
                    </el-button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="copy">复制端点</el-dropdown-item>
                        <el-dropdown-item
                          v-if="!row.tunnel_id && row.status !== 'running'"
                          command="listener-check"
                          :disabled="listenerChecks.get(row.id)?.pending"
                        >检查监听</el-dropdown-item>
                        <el-dropdown-item command="delete" divided :disabled="pendingRuleIds.has(row.id) || hasTunnelBindings(row)">删除端点</el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <footer v-if="ruleList.length || total > 0" class="ops-pagination rule-pagination">
        <span class="ops-muted">共 {{ total }} 个{{ hasFilters ? '匹配端点' : '端点' }}</span>
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          :pager-count="5"
          layout="sizes, prev, pager, next"
          aria-label="端点列表分页"
        />
      </footer>
    </section>

    <PanelDialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑端点' : '新建端点'"
      :close-on-click-modal="false"
      destroy-on-close
    >
      <p v-if="nodeOptionsLoading" class="rule-dialog-node-status ops-muted" role="status">正在加载节点选项…</p>
      <div v-else-if="nodeOptionsError" class="rule-error rule-dialog-node-status" role="alert">
        <span>无法加载节点选项，请重新加载后选择所在节点。</span>
        <el-button @click="fetchNodes">重新加载节点</el-button>
      </div>
      <el-form ref="formRef" class="rule-editor" :model="form" :rules="formRules" label-position="top" @submit.prevent="handleSubmit">
        <section class="rule-form-section" aria-labelledby="rule-entry-title">
          <header>
            <h3 id="rule-entry-title">端点与监听</h3>
            <p class="ops-muted">入口接收流量，出口配置目标与策略。到 <RouterLink class="rule-form-link" to="/tunnels">隧道管理</RouterLink> 连接入口与出口。</p>
            <p v-if="boundExitEdit" class="ops-muted">关联隧道已停止，可修改出口的目标与策略；监听地址保持不变。</p>
          </header>
          <div class="rule-form-grid">
            <el-form-item label="端点名称" prop="name" class="rule-form-wide">
              <el-input v-model.trim="form.name" placeholder="例如 办公网络端点" maxlength="100" />
            </el-form-item>
            <el-form-item label="端点方向" prop="role">
              <el-select v-model="form.role" :disabled="boundExitEdit">
                <el-option v-if="isEdit && !form.role" label="未指定（旧端点）" value="" disabled />
                <el-option label="入口" value="entry" />
                <el-option label="出口" value="exit" />
              </el-select>
            </el-form-item>
            <el-form-item label="所在节点" prop="node_id">
              <el-select v-model="form.node_id" placeholder="选择监听所在的节点" :loading="nodeOptionsLoading" :disabled="isEdit || nodeOptionsLoading || nodeOptionsError" no-data-text="请先在节点管理中添加节点" filterable>
                <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
              </el-select>
            </el-form-item>
            <el-form-item v-if="form.role !== 'exit'" :label="form.role === 'entry' ? '入口类型' : '协议'" prop="protocol">
              <el-select v-model="form.protocol" @change="handleEntryProtocolChange">
                <el-option label="TCP" value="tcp" />
                <el-option label="UDP" value="udp" />
                <el-option v-if="form.role === 'entry'" label="透明转发（HTTP/HTTPS）" value="red" />
              </el-select>
              <p v-if="transparentEntry" class="rule-form-hint ops-muted">透明接收 HTTP/HTTPS 流量并保留原始访问目标，监听 IP 由面板自动配置。</p>
            </el-form-item>
            <el-form-item v-if="!transparentEntry" label="监听 IP" prop="listen_ip">
              <el-select
                :model-value="form.listen_ip"
                :empty-values="[null, undefined]"
                :disabled="boundExitEdit"
                filterable
                allow-create
                default-first-option
                placeholder="选择或输入监听 IP"
                aria-describedby="rule-listen-ip-help"
                @update:model-value="form.listen_ip = $event.trim()"
              >
                <el-option v-for="[ip, label] in listenAddressOptions" :key="ip" :label="label" :value="ip" />
              </el-select>
              <p id="rule-listen-ip-help" class="rule-form-hint ops-muted">
                {{ form.listen_ip === '127.0.0.1' || form.listen_ip === '::1'
                  ? '回环地址仅接受本机连接。'
                  : '可输入自定义 IPv4 或 IPv6。探测地址经过 NAT 时，请选择所在节点本机的地址。' }}
              </p>
            </el-form-item>
            <el-form-item label="监听端口" prop="listen_port">
              <el-input-number v-model="form.listen_port" :disabled="boundExitEdit" :min="1" :max="65535" :step-strictly="true" placeholder="填写端口" controls-position="right" aria-describedby="rule-listen-endpoint" />
              <p v-if="form.listen_port" id="rule-listen-endpoint" class="rule-form-hint ops-muted">
                监听地址 <span class="ops-mono">{{ formatEndpoint(form.listen_ip, form.listen_port) }}</span>
                <span v-if="selectedConnectEndpoint" class="rule-connect-endpoint">
                  对外连接 <span class="ops-mono">{{ selectedConnectEndpoint }}</span>
                </span>
              </p>
            </el-form-item>
            <el-form-item label="备注" prop="remark" class="rule-form-wide">
              <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可选，记录用途或维护说明" />
            </el-form-item>
          </div>
        </section>
        <section v-if="form.role === 'exit'" class="rule-form-section" aria-labelledby="rule-target-title">
          <header>
            <h3 id="rule-target-title">目标与策略</h3>
            <p class="ops-muted">配置随出口保存，连接此出口的隧道会使用这些设置。</p>
          </header>
          <div class="rule-form-grid">
            <el-form-item label="隧道协议" prop="protocol" for="rule-tunnel-protocol">
              <el-select id="rule-tunnel-protocol" v-model="form.protocol" filterable aria-describedby="rule-protocol-help" @change="form.protocol_options = {}">
                <el-option v-for="protocol in tunnelProtocols" :key="protocol" :label="protocol.toUpperCase()" :value="protocol" />
              </el-select>
              <p id="rule-protocol-help" class="rule-form-hint ops-muted">用于入口节点到出口节点之间的 Gost 隧道传输。</p>
            </el-form-item>
            <TargetRoutingFields v-model:original-target="form.use_original_target" v-model:match-rules="form.match_rules" v-model:ruleset-ids="form.ruleset_ids" v-model:manual-domains-text="form.manualDomainsText" />
            <GostTransportOptions v-model="form.protocol_options" :protocol="form.protocol" :mptcp-supported="selectedNode?.mptcp_supported === true" class="rule-form-wide" />
            <el-form-item v-if="!form.use_original_target" label="目标地址" prop="targetList" class="rule-form-wide">
              <div class="rule-target-list">
                <div v-for="(item, index) in form.targetList" :key="index" class="rule-target-item">
                  <el-input v-model.trim="item.address" :aria-label="'目标地址 ' + (index + 1)" aria-describedby="rule-target-address-help" placeholder="例如 example.com:443 或 192.168.1.10:8080" />
                  <el-button text type="danger" :icon="Remove" :aria-label="'移除目标地址 ' + (index + 1)" @click="removeTarget(index)" />
                </div>
                <el-button :icon="Plus" @click="form.targetList.push({ address: '' })">添加目标地址</el-button>
                <p id="rule-target-address-help" class="rule-form-hint ops-muted">填写域名或 IP 与端口。HTTPS 会透传客户端发送的 SNI，无需额外开启。</p>
              </div>
            </el-form-item>
            <el-form-item v-if="!form.use_original_target" label="负载均衡" prop="strategy">
              <el-select v-model="form.strategy">
                <el-option v-for="(label, value) in strategies" :key="value" :label="label" :value="value" />
              </el-select>
            </el-form-item>
          </div>
        </section>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitLoading" :disabled="!isEdit && (nodeOptionsLoading || nodeOptionsError)" @click="handleSubmit">
          {{ isEdit ? '保存端点' : '创建端点' }}
        </el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import InventoryToolbar from '@/components/InventoryToolbar.vue'
import PanelDialog from '@/components/PanelDialog.vue'
import GostTransportOptions from '@/components/GostTransportOptions.vue'
import TargetRoutingFields from '@/components/TargetRoutingFields.vue'
import { formatEndpoint, mappedConnectEndpoint } from '@/utils/node-address-mapping'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { supportedProtocolOptions, tunnelProtocols } from '@/utils/gost-protocols'
import { targetRoutingFields, targetRoutingPayload, usesOriginalTarget } from '@/utils/target-policy'
import { ArrowDown, CircleCheck, Plus, Remove, Search, Switch, VideoPause, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import { getRoutingNodes } from '@/api/node'
import { checkRuleListener, createRule, deleteRule, getRuleList, startRule, stopRule, updateRule } from '@/api/rule'

const nodeList = ref([])
const nodeOptionsLoading = ref(false)
const nodeOptionsError = ref(false)
const ruleList = ref([])
const connectEndpoints = computed(() => Object.fromEntries(ruleList.value.map(rule => [
  rule.id, mappedConnectEndpoint(rule.node, rule.listen_ip, rule.listen_port)
])))
const loading = ref(false)
const listError = ref(false)
const page = ref(1)
const pageSize = ref(10)
const total = ref(0)

const emptyFilters = { keyword: '', node_id: '', status: '' }
const filters = reactive({ ...emptyFilters })
const activeFilters = ref({ ...emptyFilters })
const hasFilters = computed(() => Object.values(activeFilters.value).some(Boolean))

const statusClass = { running: 'is-success', stopped: 'is-muted', error: 'is-danger' }
const statusText = { running: '运行中', stopped: '已停止', error: '异常' }
const pendingRuleIds = reactive(new Set())
const listenerChecks = reactive(new Map())
const listenerReasons = {
  occupied: '监听地址已被占用',
  address_unavailable: '所选 IP 无法在此节点绑定',
  permission_denied: '节点无权绑定此监听地址',
  bind_failed: '节点绑定监听地址失败'
}
const summary = computed(() => {
  const unavailable = (loading.value || listError.value) && !ruleList.value.length
  const count = status => unavailable ? null : ruleList.value.filter(rule => rule.status === status).length
  return [
    { label: '匹配端点', value: unavailable ? null : total.value, detail: '所有匹配页', icon: Switch },
    { label: '本页运行中', value: count('running'), icon: CircleCheck, tone: 'success' },
    { label: '本页已停止', value: count('stopped'), icon: VideoPause },
    { label: '本页异常', value: count('error'), icon: Warning, tone: 'danger' }
  ]
})

const dialogVisible = ref(false)
const isEdit = ref(false)
const boundExitEdit = ref(false)
const endpointRoles = { entry: '入口', exit: '出口' }
const hasTunnelBindings = rule => !!(rule.linked_tunnels?.length || rule.linked_tunnel_id)
const canEditEndpoint = rule => !hasTunnelBindings(rule) || (rule.role === 'exit' && (rule.linked_tunnels?.length
  ? rule.linked_tunnels.every(tunnel => tunnel.status === 'stopped')
  : rule.linked_tunnel_status === 'stopped'))
const strategies = { round: '轮询', rand: '随机', fifo: '先进先出', hash: '哈希' }
const editId = ref(null)
const submitLoading = ref(false)
const formRef = ref(null)
let savedProtocolOptions = false
let savedRelayConfig = null
let savedOriginalTarget = false
let savedLegacyClash = false
let savedRuleSelection = false
const form = reactive({
  node_id: '',
  name: '',
  role: 'entry',
  protocol: 'tcp',
  protocol_options: {},
  listen_ip: '',
  listen_port: null,
  remark: '',
  ...targetRoutingFields(),
  targetList: [{ address: '' }],
  strategy: 'round'
})
const transparentEntry = computed(() => form.role === 'entry' && form.protocol === 'red')

const editingNode = ref(null)
const selectedNode = computed(() => nodeList.value.find(item => item.id === form.node_id) ||
  (editingNode.value?.id === form.node_id ? editingNode.value : undefined))
const selectedConnectEndpoint = computed(() => mappedConnectEndpoint(selectedNode.value, form.listen_ip, form.listen_port))
const listenAddressOptions = computed(() => {
  const entry = selectedNode.value
  const options = new Map([
    ['', '所有接口'],
    ['127.0.0.1', 'IPv4 回环 · 127.0.0.1'],
    ['::1', 'IPv6 回环 · ::1']
  ])
  for (const { listen_ip, connect_ip } of entry?.address_mappings || []) {
    options.set(listen_ip, `本机 ${listen_ip} · 对外 ${connect_ip}`)
  }
  for (const ip of [entry?.ipv4, entry?.ipv6]) {
    if (ip && !options.has(ip)) options.set(ip, '节点探测地址 · ' + ip)
  }
  if (form.listen_ip && !options.has(form.listen_ip)) options.set(form.listen_ip, '自定义地址 · ' + form.listen_ip)
  return options
})

const formRules = {
  role: [{ validator: (_rule, value, callback) => callback(!isEdit.value && !value ? new Error('请选择端点方向') : undefined), trigger: 'change' }],
  node_id: [{ required: true, message: '请选择所在节点', trigger: 'change' }],
  name: [{ required: true, message: '请输入端点名称', trigger: 'blur' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  listen_port: [{ required: true, message: '请输入监听端口', trigger: 'change' }],
  listen_ip: [{ validator: (_rule, value, callback) => callback(form.role === 'exit' && (value === '::1' || value.startsWith('127.'))
    ? new Error('出口须使用可达的本机 IP，或选择所有接口') : undefined), trigger: 'change' }],
  targetList: [{ validator: (_rule, value, callback) => callback(form.role === 'exit' && !form.use_original_target && !value.some(item => item.address.trim())
    ? new Error('请至少填写一个目标地址') : undefined), trigger: 'blur' }]
}

watch(() => form.role, role => {
  if (role === 'entry') {
    if (!['tcp', 'udp', 'red'].includes(form.protocol)) form.protocol = 'tcp'
    form.protocol_options = {}
  } else if (role === 'exit' && form.protocol === 'red') {
    form.protocol = 'tcp'
    form.listen_ip = ''
  }
})

const handleEntryProtocolChange = protocol => {
  form.protocol_options = {}
  if (protocol === 'red') form.listen_ip = '127.0.0.1'
}

const removeTarget = index => {
  form.targetList.splice(index, 1)
  if (!form.targetList.length) form.targetList.push({ address: '' })
}

const byteUnits = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
const formatBytes = (bytes = 0) => {
  if (!bytes) return '0 B'
  const index = Math.min(Math.floor(Math.log10(bytes) / 3), byteUnits.length - 1)
  return Math.round((bytes / 1000 ** index) * 100) / 100 + ' ' + byteUnits[index]
}

const fetchNodes = async () => {
  if (nodeOptionsLoading.value) return
  nodeOptionsLoading.value = true
  nodeOptionsError.value = false
  try {
    nodeList.value = await getRoutingNodes()
  } catch {
    nodeOptionsError.value = true
  } finally {
    nodeOptionsLoading.value = false
  }
}

let latestRequest = 0
const fetchData = async (silent = false) => {
  const request = ++latestRequest
  if (!silent) loading.value = true
  try {
    const { data } = await getRuleList({
      page: page.value,
      pageSize: pageSize.value,
      ...activeFilters.value
    })
    if (request !== latestRequest) return
    ruleList.value = data.list || []
    total.value = data.total || 0
    listError.value = false
  } catch {
    if (request === latestRequest) listError.value = true
  } finally {
    if (request === latestRequest) loading.value = false
  }
}

const applyFilters = () => {
  activeFilters.value = { ...filters, keyword: filters.keyword.trim() }
  ruleList.value = []
  total.value = 0
  if (page.value === 1) fetchData()
  else page.value = 1
}

const clearFilters = () => {
  Object.assign(filters, emptyFilters)
  applyFilters()
}

const openDialog = (row = null, copy = false) => {
  isEdit.value = !!row && !copy
  editId.value = isEdit.value ? row.id : null
  boundExitEdit.value = isEdit.value && row.role === 'exit' && hasTunnelBindings(row)
  savedProtocolOptions = isEdit.value && !!Object.keys(row.protocol_options || {}).length
  savedRelayConfig = isEdit.value ? { nodeId: row.node_id, protocol: row.protocol, mptcp: row.protocol_options?.mptcp } : null
  editingNode.value = row?.node || null
  savedOriginalTarget = isEdit.value && usesOriginalTarget(row)
  savedLegacyClash = isEdit.value && !!row.use_clash_ruleset
  savedRuleSelection = isEdit.value && !!(row.use_clash_ruleset || row.ruleset_ids?.length || row.manual_domains?.length)

  Object.assign(form, {
    node_id: row?.node_id || row?.tunnel?.entry_node_id || '',
    name: row ? row.name + (copy ? '-copy' : '') : '',
    role: row?.role || (isEdit.value ? '' : 'entry'),
    protocol: row?.protocol || 'tcp',
    protocol_options: row?.role === 'exit' ? { ...row.protocol_options } : {},
    listen_ip: row?.role === 'entry' && row.protocol === 'red' ? '127.0.0.1' : row?.listen_ip || '',
    listen_port: isEdit.value ? row.listen_port : null,
    remark: row?.remark || '',
    ...targetRoutingFields(row?.role === 'exit' ? row : null),
    targetList: row?.role === 'exit' && row.targets?.length ? row.targets.map(address => ({ address })) : [{ address: '' }],
    strategy: row?.role === 'exit' ? row.strategy || 'round' : 'round'
  })
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value || (!isEdit.value && (nodeOptionsLoading.value || nodeOptionsError.value))) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) {
    await nextTick()
    formRef.value.$el.querySelector('.el-form-item.is-error input, .el-form-item.is-error textarea')?.focus()
    return
  }

  submitLoading.value = true
  try {
    const payload = {
      node_id: form.node_id,
      name: form.name,
      protocol: form.protocol,
      listen_ip: form.listen_ip,
      listen_port: form.listen_port,
      remark: form.remark
    }
    if (form.role) {
      Object.assign(payload, {
        role: form.role,
        targets: form.role === 'exit' && !form.use_original_target ? form.targetList.map(item => item.address.trim()).filter(Boolean) : [],
        strategy: form.role === 'exit' && !form.use_original_target ? form.strategy : 'round'
      })
      Object.assign(payload, targetRoutingPayload({ ...form, use_original_target: form.role === 'exit' && form.use_original_target }, {
        legacyClash: savedLegacyClash, originalTarget: savedOriginalTarget, ruleSelection: savedRuleSelection
      }))
    }
    if (savedProtocolOptions || Object.keys(form.protocol_options).length) {
      payload.protocol_options = supportedProtocolOptions(form.protocol_options, selectedNode.value?.mptcp_supported,
        savedRelayConfig?.nodeId === form.node_id && savedRelayConfig?.protocol === form.protocol ? savedRelayConfig.mptcp : undefined)
    }
    if (isEdit.value) {
      await updateRule(editId.value, payload)
      listenerChecks.delete(editId.value)
    } else {
      const result = await createRule(payload)
      if (result.data?.id) checkListener(result.data, true)
    }
    ElMessage.success(isEdit.value ? '端点已保存' : '端点已创建')
    dialogVisible.value = false
    fetchData()
  } catch {
    // The request interceptor displays the backend error.
  } finally {
    submitLoading.value = false
  }
}

const runRuleAction = async (row, action, successMessage) => {
  if (pendingRuleIds.has(row.id)) return
  pendingRuleIds.add(row.id)
  try {
    await action(row.id)
    listenerChecks.delete(row.id)
    ElMessage.success(successMessage)
    fetchData()
  } catch {
    // The request interceptor displays the backend error.
  } finally {
    pendingRuleIds.delete(row.id)
  }
}

const checkListener = async (row, notify = false) => {
  if (listenerChecks.get(row.id)?.pending) return
  const pending = { pending: true, label: '正在检查监听…' }
  listenerChecks.set(row.id, pending)
  let available = false
  let label
  try {
    const { data } = await checkRuleListener(row.id)
    available = data.available
    label = available ? '监听地址可用' : listenerReasons[data.reason] || '监听地址不可用'
  } catch (error) {
    label = error.response?.data?.message || '暂时无法检查监听地址'
  }
  if (!listenerChecks.get(row.id)?.pending) return
  listenerChecks.set(row.id, { label })
  if (notify) {
    if (available) ElMessage.success(label)
    else ElMessage.warning(label)
  }
}

const toggleRule = row => {
  const running = row.status === 'running'
  return runRuleAction(row, running ? stopRule : startRule, running ? '端点已停止' : '端点已启动')
}

const handleRowCommand = async (row, command) => {
  if (command === 'copy') {
    openDialog(row, true)
    return
  }
  if (command === 'listener-check') return checkListener(row)
  try {
    await confirmPanelAction('确定删除端点「' + row.name + '」？', '删除端点', {
      confirmButtonText: '删除端点',
      cancelButtonText: '取消',
      confirmButtonClass: 'el-button--danger',
      type: 'warning'
    })
  } catch {
    return
  }
  await runRuleAction(row, deleteRule, '端点已删除')
}

let refreshTimer
watch([page, pageSize], () => fetchData())
onMounted(() => {
  fetchNodes()
  fetchData()
  refreshTimer = setInterval(() => {
    if (!document.hidden && !loading.value && !listError.value) fetchData(true)
  }, 5000)
})
onBeforeUnmount(() => clearInterval(refreshTimer))
</script>

<style scoped>
.rule-target-list { display: grid; gap: 8px; width: 100%; }
.rule-target-item { display: flex; align-items: center; gap: 8px; }
.rule-target-item .el-input { min-width: 0; }
.rule-target-list > .el-button { justify-self: start; }
.rule-target-list p { margin-bottom: 0; }
.rule-panel {
  min-width: 0;
}

.rule-form-section header p {
  margin: 4px 0 0;
  font-size: var(--ops-text-body);
}

.rule-row-actions .el-button + .el-button {
  margin-left: 0;
}

.rule-table-scroll {
  border-top: 1px solid var(--ops-border);
}

.rule-table {
  width: 100%;
  min-width: 830px;
  border-collapse: collapse;
  text-align: left;
  font-size: var(--ops-text-body);
}

.rule-table th,
.rule-table td {
  padding: 14px 12px;
  vertical-align: middle;
}

.rule-table th:first-child,
.rule-table td:first-child {
  padding-left: var(--ops-card-padding);
}

.rule-table th:last-child,
.rule-table td:last-child {
  padding-right: var(--ops-card-padding);
}

.rule-table thead {
  color: var(--ops-muted);
  background: var(--ops-inset);
}

.rule-table thead th {
  font-size: var(--ops-text-xs);
  font-weight: var(--ops-weight-semibold);
  white-space: nowrap;
}

.rule-table tbody tr + tr {
  border-top: 1px solid var(--ops-border);
}

.rule-table tbody tr:hover,
.rule-table tbody tr:focus-within {
  background: var(--ops-raised);
}

.rule-name {
  min-width: 120px;
  overflow-wrap: anywhere;
}

.rule-name strong,
.rule-traffic-total {
  display: block;
  color: var(--ops-text);
  font-weight: var(--ops-weight-semibold);
}

.rule-secondary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-top: 4px;
  color: var(--ops-muted);
  font-size: var(--ops-text-xs);
  font-weight: var(--ops-weight-normal);
}

.rule-path {
  min-width: 154px;
  max-width: 220px;
  overflow-wrap: anywhere;
}

.rule-path-entry {
  font-weight: var(--ops-weight-medium);
}

.rule-protocol {
  display: inline-block;
  margin-right: 5px;
  padding: 2px 5px;
  border: 1px solid var(--ops-border);
  border-radius: 4px;
  color: var(--ops-muted);
  font-size: var(--ops-text-xs);
}

.rule-row-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.rule-row-actions .el-button {
  min-height: 32px;
  padding: 4px 7px;
}

.rule-pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px 16px;
  padding: 12px var(--ops-card-padding);
  border-top: 1px solid var(--ops-border);
  font-size: var(--ops-text-sm);
}

.rule-pagination :deep(.el-pagination) {
  max-width: 100%;
  flex-wrap: wrap;
  row-gap: 8px;
}

.rule-placeholder {
  display: grid;
  justify-items: center;
  gap: 8px;
  padding: 36px 16px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-muted);
  text-align: center;
}

.rule-placeholder .el-icon {
  color: var(--ops-accent);
  font-size: 24px;
}

.rule-placeholder strong {
  color: var(--ops-text);
}

.rule-placeholder p,
.rule-error p {
  margin: 0;
}

.rule-error {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 12px var(--ops-card-padding);
  border-top: 1px solid var(--ops-border);
  color: var(--ops-warning);
  font-size: var(--ops-text-body);
}

.rule-node-status { padding: 12px 20px; font-size: .875rem; }
.rule-dialog-node-status { margin: 0 0 14px; }
.rule-error.rule-dialog-node-status { border: 1px solid var(--ops-border); border-radius: 8px; background: var(--ops-inset); }

.rule-editor {
  display: grid;
  gap: 14px;
}

.rule-form-section {
  min-width: 0;
  padding: var(--ops-inset-padding) var(--ops-inset-padding) 2px;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
}

.rule-form-section h3 { font-size: var(--ops-text-base); }

.rule-form-section header {
  margin-bottom: 12px;
}

.rule-form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 14px;
}

.rule-form-wide {
  grid-column: 1 / -1;
}

.rule-form-hint {
  margin: 4px 0 0;
  line-height: 1.45;
  font-size: var(--ops-text-xs);
}

.rule-connect-endpoint { display: block; overflow-wrap: anywhere; }

.rule-form-link { color: var(--ops-accent); }

.rule-form-grid :deep(.el-select),
.rule-form-grid :deep(.el-input-number) {
  width: 100%;
}

@media (max-width: 700px) {
  .rule-pagination,
  .rule-node-status,
  .rule-error {
    padding-left: var(--ops-card-padding);
    padding-right: var(--ops-card-padding);
  }

  .rule-table {
    display: block;
    min-width: 0;
  }

  .rule-table thead {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .rule-table tbody {
    display: block;
  }

  .rule-table tbody tr {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px 16px;
    padding: 14px;
  }

  .rule-table tbody th,
  .rule-table tbody td {
    min-width: 0;
    max-width: none;
    padding: 0;
    overflow-wrap: anywhere;
  }

  .rule-table tbody th:first-child,
  .rule-table tbody td:last-child {
    padding: 0;
  }

  .rule-table td[data-label]::before {
    display: block;
    margin-bottom: 4px;
    color: var(--ops-muted);
    font-size: var(--ops-text-xs);
    content: attr(data-label);
  }

  .rule-name,
  .rule-path,
  .rule-actions-cell {
    grid-column: 1 / -1;
  }

  .rule-row-actions {
    flex-wrap: wrap;
    gap: 8px;
  }

  .rule-row-actions .el-button,
  .rule-editor :deep(.el-input__wrapper),
  .rule-editor :deep(.el-select__wrapper),
  .rule-pagination :deep(.el-pagination button),
  .rule-pagination :deep(.el-pager li) {
    min-height: 44px;
  }
}

@media (max-width: 480px) {
  .rule-form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
