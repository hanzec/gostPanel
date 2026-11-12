<template>
  <div class="ops-page">
    <MetricStrip label="隧道统计" :items="tunnelMetrics" />

    <section class="ops-panel tunnel-panel" aria-labelledby="tunnel-inventory-title">
      <InventoryToolbar filter-label="筛选隧道" @submit="handleSearch">
        <template #title>
          <h2 id="tunnel-inventory-title">链路清单</h2>
        </template>
        <template #filters>
          <div class="inventory-toolbar-field">
            <label for="tunnel-keyword">隧道名称</label>
            <el-input id="tunnel-keyword" v-model="searchKeyword" aria-label="隧道名称" :prefix-icon="Search" clearable placeholder="搜索名称" @clear="handleSearch" />
          </div>
          <div class="inventory-toolbar-field">
            <label for="tunnel-node">关联节点</label>
            <el-select id="tunnel-node" v-model="searchNodeId" aria-label="关联节点" placeholder="全部节点" :loading="nodeOptionsLoading" clearable @change="handleSearch">
              <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
            </el-select>
          </div>
          <div class="inventory-toolbar-field">
            <label for="inventory-status">运行状态</label>
            <el-select id="inventory-status" v-model="searchStatus" aria-label="运行状态" placeholder="全部状态" clearable @change="handleSearch">
              <el-option label="运行中" value="running" />
              <el-option label="已停止" value="stopped" />
              <el-option label="异常" value="error" />
            </el-select>
          </div>
          <el-button native-type="submit" :icon="Search">筛选</el-button>
        </template>
        <template #actions>
          <el-button type="primary" :icon="Plus" @click="openDialog()">添加隧道</el-button>
        </template>
      </InventoryToolbar>

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
        <span class="ops-muted">{{ filtered ? '调整搜索条件后重试。' : '先在端点管理创建入口与出口，再在这里连接两个端点。' }}</span>
        <el-button v-if="filtered" @click="clearFilters">清除筛选</el-button>
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
              <td class="tunnel-route-cell" role="cell">
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
                <strong class="ops-mono">{{ (tunnel.exit_rule?.role === 'exit' ? tunnel.exit_rule.protocol : tunnel.protocol)?.toUpperCase() || '—' }}</strong>
                <span class="inventory-secondary ops-muted">
                  <template v-if="relayConnectEndpoints[tunnel.id]">本机监听 </template>
                  <span class="ops-mono">{{ tunnel.exit_rule ? listenEndpoint(tunnel.exit_rule) : ':' + (tunnel.relay_port || '—') }}</span>
                </span>
                <span v-if="relayConnectEndpoints[tunnel.id]" class="inventory-secondary ops-muted">
                  源端连接 <span class="ops-mono">{{ relayConnectEndpoints[tunnel.id] }}</span>
                </span>
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
                        <el-dropdown-item command="allowlist">Gost IP 白名单</el-dropdown-item>
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
            :pager-count="5"
            layout="prev, pager, next"
            @current-change="fetchData()"
          />
        </div>
      </div>
    </section>

    <PanelDialog
      v-model="dialogVisible"
      :title="dialogTitle"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitLoading"
      :show-close="!submitLoading"
    >
      <p v-if="allowlistOnly" class="tunnel-dialog-hint ops-muted">正在设置「{{ form.name }}」。运行中的隧道会在下一轮同步时应用变更，首次绑定白名单时会重载出口 Relay。</p>
      <p v-else-if="legacyEdit" class="tunnel-dialog-hint ops-muted">此隧道直接关联节点，可调整协议和 Relay 端口。</p>
      <div v-else-if="entryOptionsError" class="inventory-error tunnel-dialog-error" role="alert">
        <span>无法加载端点选项。</span>
        <el-button :loading="entryOptionsLoading" @click="fetchEntrances">重新加载端点</el-button>
      </div>
      <p v-else class="tunnel-dialog-hint ops-muted">
        <template v-if="isEdit">端点变更将在启动时应用。</template>
        <template v-else-if="!entryOptionsLoading && (!sourceOptions.length || !destinationOptions.length)">源端或目的端缺少可用端点。</template>
        <template v-else>选择入口和出口后，面板将按出口的目标与策略配置两端并启动隧道。</template>
        <RouterLink to="/rules" class="tunnel-link" @click="dialogVisible = false">前往端点管理</RouterLink>
      </p>
      <el-form ref="formRef" :model="form" :rules="formRules" label-position="top" :disabled="submitLoading" @submit.prevent="handleSubmit">
        <div v-if="!allowlistOnly" class="inventory-form-grid">
          <el-form-item label="隧道名称" prop="name" class="inventory-form-wide">
            <el-input v-model="form.name" placeholder="例如 生产环境中继" />
          </el-form-item>
          <el-form-item v-if="!legacyEdit" label="源端点" prop="entry_rule_id">
            <el-select v-model="form.entry_rule_id" placeholder="选择源端点" :loading="entryOptionsLoading" filterable fit-input-width popper-class="tunnel-endpoint-options" aria-describedby="source-entry-help">
              <el-option
                v-for="rule in sourceOptions"
                :key="rule.id"
                :label="entranceLabel(rule)"
                :value="rule.id"
                :disabled="rule.node_id === exitEntrance?.node_id"
              />
            </el-select>
            <span id="source-entry-help" class="tunnel-form-hint ops-muted">入口接收流量，按出口的目标与策略转发。独立转发中的旧端点须先停止。</span>
          </el-form-item>
          <el-form-item v-if="!legacyEdit" label="目的端点" prop="exit_rule_id">
            <el-select v-model="form.exit_rule_id" placeholder="选择目的端点" :loading="entryOptionsLoading" filterable fit-input-width popper-class="tunnel-endpoint-options" aria-describedby="destination-entry-help">
              <el-option
                v-for="rule in destinationOptions"
                :key="rule.id"
                :label="entranceLabel(rule)"
                :value="rule.id"
                :disabled="rule.node_id === entryEntrance?.node_id"
              />
            </el-select>
            <span id="destination-entry-help" class="tunnel-form-hint ops-muted">
              出口须在另一节点且监听可达，其隧道协议、目标与策略在端点管理中配置。
              <span v-if="selectedRelayConnectEndpoint" class="tunnel-connect-endpoint">
                源端连接 <span class="ops-mono">{{ selectedRelayConnectEndpoint }}</span>
              </span>
            </span>
          </el-form-item>
          <el-form-item v-if="legacyEdit" label="协议" prop="protocol">
            <el-select v-model="form.protocol" filterable @change="form.protocol_options = {}">
              <el-option v-for="protocol in tunnelProtocols" :key="protocol" :label="protocol.toUpperCase()" :value="protocol" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="legacyEdit" label="Relay 端口" prop="relay_port">
            <el-input-number v-model="form.relay_port" :min="1" :max="65535" controls-position="right" />
            <span class="tunnel-form-hint ops-muted">Relay 服务在出口节点监听。</span>
          </el-form-item>
          <GostTransportOptions v-if="legacyEdit" v-model="form.protocol_options" :protocol="form.protocol" class="inventory-form-wide" />
          <section v-if="exitOwnsTargets" class="tunnel-target-section inventory-form-wide" aria-labelledby="tunnel-exit-policy-title">
            <h3 id="tunnel-exit-policy-title">出口目标与策略</h3>
            <p class="tunnel-dialog-hint ops-muted">以下设置来自所选出口，在端点管理中修改。</p>
            <dl class="tunnel-policy-summary">
              <dt>隧道协议</dt><dd>{{ exitEntrance.protocol.toUpperCase() }}</dd>
              <dt>目标类型</dt><dd>{{ exitEntrance.use_clash_ruleset ? 'Clash 规则集（透明分流）' : '固定目标地址' }}</dd>
              <template v-if="!exitEntrance.use_clash_ruleset">
                <dt>目标地址</dt><dd><span v-for="target in exitEntrance.targets" :key="target" class="ops-mono">{{ target }}</span></dd>
                <dt>负载均衡</dt><dd>{{ strategies[exitEntrance.strategy] || '轮询' }}</dd>
              </template>
            </dl>
            <GostTransportOptions :model-value="exitEntrance.protocol_options || {}" :protocol="exitEntrance.protocol" readonly />
            <p v-if="exitEntrance.use_clash_ruleset" class="tunnel-form-hint ops-muted">Clash 分流要求入口使用 TCP 与 127.0.0.1。</p>
          </section>
          <p v-else-if="!legacyEdit && !exitEntrance" class="tunnel-form-hint ops-muted inventory-form-wide">选择出口后查看其目标与策略。</p>
          <section v-if="legacyTargetEdit" class="tunnel-target-section inventory-form-wide" aria-labelledby="tunnel-target-title">
            <h3 id="tunnel-target-title">目标与策略</h3>
            <p class="tunnel-dialog-hint ops-muted">此旧端点未指定方向，目标与策略仍随隧道保存。</p>
            <div class="inventory-form-grid">
              <el-form-item label="隧道协议" prop="protocol" class="inventory-form-wide" for="tunnel-protocol">
                <el-select id="tunnel-protocol" v-model="form.protocol" filterable aria-describedby="tunnel-protocol-help" @change="form.protocol_options = {}">
                  <el-option v-for="protocol in tunnelProtocols" :key="protocol" :label="protocol.toUpperCase()" :value="protocol" />
                </el-select>
                <p id="tunnel-protocol-help" class="tunnel-form-hint ops-muted">用于源节点到目的节点之间的 Gost 隧道传输。</p>
              </el-form-item>
              <GostTransportOptions v-model="form.protocol_options" :protocol="form.protocol" class="inventory-form-wide" />
              <el-form-item label="目标类型" prop="use_clash_ruleset" class="inventory-form-wide">
                <el-select v-model="form.use_clash_ruleset" aria-describedby="tunnel-target-mode-help">
                  <el-option label="固定目标地址" :value="false" />
                  <el-option label="Clash 规则集（透明分流）" :value="true" :disabled="!canUseClashRuleset" />
                </el-select>
                <p id="tunnel-target-mode-help" class="tunnel-form-hint ops-muted">
                  {{ canUseClashRuleset ? 'Clash 分流使用系统设置中的规则集，匹配流量经过隧道，其余流量直连。' : 'Clash 分流需要源端点使用 TCP 与 127.0.0.1。' }}
                </p>
              </el-form-item>
              <el-form-item v-if="!form.use_clash_ruleset" label="目标地址" prop="targetList" class="inventory-form-wide">
                <div class="tunnel-target-list">
                  <div v-for="(item, index) in form.targetList" :key="index" class="tunnel-target-item">
                    <el-input v-model.trim="item.address" :aria-label="'目标地址 ' + (index + 1)" aria-describedby="tunnel-target-address-help" placeholder="例如 example.com:443 或 192.168.1.10:8080" />
                    <el-button text type="danger" :icon="Remove" :aria-label="'移除目标地址 ' + (index + 1)" @click="removeTarget(index)" />
                  </div>
                  <el-button :icon="Plus" @click="form.targetList.push({ address: '' })">添加目标地址</el-button>
                  <p id="tunnel-target-address-help" class="tunnel-form-hint ops-muted">填写域名或 IP 与端口。HTTPS 会透传客户端发送的 SNI，无需额外开启。</p>
                </div>
              </el-form-item>
              <el-form-item v-if="!form.use_clash_ruleset" label="负载均衡" prop="strategy">
                <el-select v-model="form.strategy">
                  <el-option label="轮询" value="round" />
                  <el-option label="随机" value="rand" />
                  <el-option label="先进先出" value="fifo" />
                  <el-option label="哈希" value="hash" />
                </el-select>
              </el-form-item>
            </div>
          </section>
          <el-form-item label="备注" prop="remark" class="inventory-form-wide">
            <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可记录链路用途" />
          </el-form-item>
        </div>
        <el-form-item class="tunnel-allowlist-field" label="Gost IP 白名单" prop="allowlist_node_ids" for="tunnel-allowlist-nodes">
          <el-select
            id="tunnel-allowlist-nodes"
            v-model="form.allowlist_node_ids"
            aria-describedby="tunnel-allowlist-help"
            :loading="nodeOptionsLoading"
            :disabled="nodeOptionsLoading || nodeOptionsError"
            multiple
            filterable
            clearable
            collapse-tags
            collapse-tags-tooltip
            placeholder="选择有可用 IP 的来源节点"
            no-data-text="请先在节点管理中添加节点"
          >
            <el-option
              v-for="node in nodeList"
              :key="node.id"
              :label="`${node.name}（${nodeAddresses(node).join(' / ') || '未获取 IP'}）`"
              :value="node.id"
              :disabled="!nodeAddresses(node).length && !form.allowlist_node_ids.includes(node.id)"
            />
            <el-option v-for="id in missingAllowlistNodes" :key="id" :label="`节点 #${id}（未找到）`" :value="id" />
          </el-select>
          <span id="tunnel-allowlist-help" class="tunnel-form-hint ops-muted">来源 IP 预览中的地址可连接此隧道的出口 Relay。清空选择并保存可解除来源 IP 限制。</span>
        </el-form-item>
        <div v-if="nodeOptionsError" class="inventory-error tunnel-dialog-error" role="alert">
          <span>无法加载来源节点选项。</span>
          <el-button :loading="nodeOptionsLoading" @click="fetchNodes">重新加载来源节点</el-button>
        </div>
        <dl v-else-if="!nodeOptionsLoading && form.allowlist_node_ids.length" class="tunnel-allowlist-preview" aria-live="polite">
          <dt>来源 IP 预览</dt>
          <dd v-if="allowlistAddresses.length">
            <code v-for="address in allowlistAddresses" :key="address" class="ops-mono">{{ address }}</code>
          </dd>
          <dd v-else class="ops-muted">所选节点暂无可用 IP</dd>
        </dl>
        <p v-if="allowlistError" ref="allowlistErrorNotice" class="tunnel-allowlist-error" role="alert" tabindex="-1">{{ allowlistError }}</p>
      </el-form>
      <template #footer>
        <el-button :disabled="submitLoading" @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitLoading" :disabled="submitDisabled" @click="handleSubmit">{{ isEdit ? '保存更改' : '添加并启动' }}</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { computed, nextTick, ref, reactive, watch, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage } from 'element-plus'
import InventoryToolbar from '@/components/InventoryToolbar.vue'
import PanelDialog from '@/components/PanelDialog.vue'
import GostTransportOptions from '@/components/GostTransportOptions.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { tunnelProtocols } from '@/utils/gost-protocols'
import { ArrowDown, Connection, Plus, Remove, Right, Search, VideoPause, VideoPlay, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import { getTunnelList, createTunnel, updateTunnel, updateTunnelAllowlist, deleteTunnel, startTunnel, stopTunnel } from '@/api/tunnel'
import { getRoutingNodes } from '@/api/node'
import { getRuleList } from '@/api/rule'
import { formatEndpoint, mappedConnectEndpoint } from '@/utils/node-address-mapping'
import { nodeAddresses } from '@/utils/node-display'

const tunnelStates = {
  running: { label: '运行中', icon: VideoPlay, tone: 'success' },
  stopped: { label: '已停止', icon: VideoPause },
  error: { label: '异常', icon: Warning, tone: 'danger' }
}
const strategies = { round: '轮询', rand: '随机', fifo: '先进先出', hash: '哈希' }

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
const allowlistOnly = ref(false)
const allowlistError = ref('')
const allowlistErrorNotice = ref(null)
const submitLoading = ref(false)
const submitDisabled = computed(() => allowlistOnly.value
  ? nodeOptionsLoading.value || nodeOptionsError.value
  : !legacyEdit.value && (entryOptionsLoading.value || entryOptionsError.value))
const dialogTitle = computed(() => allowlistOnly.value ? 'Gost IP 白名单' : (isEdit.value ? '编辑隧道' : '添加隧道'))
const formRef = ref(null)
const form = reactive({ name: '', entry_rule_id: null, exit_rule_id: null, protocol: 'tcp', protocol_options: {}, relay_port: null, remark: '', allowlist_node_ids: [], use_clash_ruleset: false, targetList: [{ address: '' }], strategy: 'round' })
let savedAllowlistNodeIds = []
let savedProtocolOptions = false
const missingAllowlistNodes = computed(() => form.allowlist_node_ids.filter(id => !nodeList.value.some(node => node.id === id)))
const allowlistAddresses = computed(() => [...new Set(nodeList.value
  .filter(node => form.allowlist_node_ids.includes(node.id))
  .flatMap(nodeAddresses))])
const listenEndpoint = rule => formatEndpoint(rule.listen_ip, rule.listen_port)
const entranceLabel = rule => `${rule.name} · ${rule.node?.name || '节点 #' + rule.node_id} · ${rule.protocol.toUpperCase()} ${listenEndpoint(rule)}`
const entryEntrance = computed(() => entryList.value.find(rule => rule.id === form.entry_rule_id))
const exitEntrance = computed(() => entryList.value.find(rule => rule.id === form.exit_rule_id))
const exitOwnsTargets = computed(() => !legacyEdit.value && exitEntrance.value?.role === 'exit')
const legacyTargetEdit = computed(() => !legacyEdit.value && !!exitEntrance.value && !exitOwnsTargets.value)
const selectedRelayConnectEndpoint = computed(() => mappedConnectEndpoint(exitEntrance.value?.node, exitEntrance.value?.listen_ip, exitEntrance.value?.listen_port))
const relayConnectEndpoints = computed(() => Object.fromEntries(tunnelList.value.map(tunnel => [
  tunnel.id, mappedConnectEndpoint(tunnel.exit_node, tunnel.exit_rule?.listen_ip, tunnel.relay_port)
])))
const availableEntrances = computed(() => entryList.value.filter(rule => rule.linked_tunnel_id
  ? rule.linked_tunnel_id === editId.value
  : !rule.tunnel_id && rule.status !== 'running'))
const sourceOptions = computed(() => availableEntrances.value.filter(rule => !rule.role || rule.role === 'entry'))
const destinationOptions = computed(() => availableEntrances.value.filter(rule =>
  (!rule.role || rule.role === 'exit') && !['0.0.0.0', '::', '::1'].includes(rule.listen_ip) && !rule.listen_ip?.startsWith('127.')))
const canUseClashRuleset = computed(() => entryEntrance.value?.protocol === 'tcp' && entryEntrance.value?.listen_ip === '127.0.0.1')
const validateEndpointNodes = (_rule, _value, callback) => callback(entryEntrance.value?.node_id &&
  entryEntrance.value.node_id === exitEntrance.value?.node_id ? new Error('源端和目的端须位于不同节点') : undefined)
const formRules = {
  name: [{ required: true, message: '请输入隧道名称', trigger: 'blur' }],
  entry_rule_id: [{ required: true, message: '请选择源端点', trigger: 'change' }, { validator: validateEndpointNodes, trigger: 'change' }],
  exit_rule_id: [{ required: true, message: '请选择目的端点', trigger: 'change' }, { validator: validateEndpointNodes, trigger: 'change' },
    { validator: (_rule, _value, callback) => callback(exitOwnsTargets.value && exitEntrance.value.use_clash_ruleset && !canUseClashRuleset.value
      ? new Error('此出口的 Clash 分流需要入口使用 TCP 与 127.0.0.1') : undefined), trigger: 'change' }],
  use_clash_ruleset: [{ validator: (_rule, value, callback) => callback(value && !canUseClashRuleset.value
    ? new Error('Clash 分流需要源端点使用 TCP 与 127.0.0.1') : undefined), trigger: 'change' }],
  targetList: [{ required: true, validator: (_rule, value, callback) => callback(!form.use_clash_ruleset && !value.some(item => item.address.trim())
    ? new Error('请至少填写一个目标地址') : undefined), trigger: 'blur' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  relay_port: [{ required: true, message: '请输入 Relay 端口', trigger: 'blur' }]
}

watch(canUseClashRuleset, () => {
  if (dialogVisible.value && legacyTargetEdit.value && form.use_clash_ruleset) formRef.value?.validateField('use_clash_ruleset').catch(() => {})
  if (dialogVisible.value && exitOwnsTargets.value) formRef.value?.validateField('exit_rule_id').catch(() => {})
})

const removeTarget = index => {
  form.targetList.splice(index, 1)
  if (!form.targetList.length) form.targetList.push({ address: '' })
}

const targetFields = row => {
  const config = row?.strategy ? row : row?.entry_rule
  return {
    use_clash_ruleset: config?.use_clash_ruleset || false,
    targetList: config?.targets?.length ? config.targets.map(address => ({ address })) : [{ address: '' }],
    strategy: config?.strategy || 'round'
  }
}

const fetchEntrances = async () => {
  if (entryOptionsLoading.value) return
  entryOptionsLoading.value = true
  try {
    const rows = []
    const size = 100
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
  if (nodeOptionsLoading.value) return
  nodeOptionsLoading.value = true
  try {
    nodeList.value = await getRoutingNodes()
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

const openDialog = (row = null, onlyAllowlist = false) => {
  editId.value = row?.id ?? null
  legacyEdit.value = !!row && !row.entry_rule_id
  allowlistOnly.value = onlyAllowlist
  allowlistError.value = ''
  savedAllowlistNodeIds = row?.allowlist_node_ids || []
  savedProtocolOptions = !!row && row.exit_rule?.role !== 'exit' && !!Object.keys(row.protocol_options || {}).length
  Object.assign(form, {
    name: row?.name || '',
    entry_rule_id: row?.entry_rule_id ?? null,
    exit_rule_id: row?.exit_rule_id ?? null,
    protocol: row?.protocol || 'tcp',
    protocol_options: row?.exit_rule?.role === 'exit' ? {} : { ...row?.protocol_options },
    relay_port: row?.relay_port ?? null,
    remark: row?.remark || '',
    allowlist_node_ids: [...savedAllowlistNodeIds],
    ...targetFields(row)
  })
  formRef.value?.clearValidate()
  dialogVisible.value = true
  if (!legacyEdit.value && !onlyAllowlist) fetchEntrances()
  if (!nodeOptionsError.value) fetchNodes()
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value || submitDisabled.value) return
  submitLoading.value = true
  allowlistError.value = ''
  try {
    const { name, remark, entry_rule_id, exit_rule_id, protocol, relay_port, allowlist_node_ids } = form
    if (allowlistOnly.value) {
      await updateTunnelAllowlist(editId.value, allowlist_node_ids)
      ElMessage.success('Gost IP 白名单已保存')
    } else {
      if (!await formRef.value.validate().catch(() => false)) {
        submitLoading.value = false
        await nextTick()
        formRef.value.$el.querySelector('.el-form-item.is-error input, .el-form-item.is-error textarea')?.focus()
        return
      }
      const payload = legacyEdit.value
        ? { name, remark, protocol, relay_port }
        : exitOwnsTargets.value
          ? { name, remark, entry_rule_id, exit_rule_id }
          : { name, remark, entry_rule_id, exit_rule_id, protocol, use_clash_ruleset: form.use_clash_ruleset,
            targets: form.use_clash_ruleset ? [] : form.targetList.map(item => item.address.trim()).filter(Boolean), strategy: form.strategy }
      if (!exitOwnsTargets.value && (savedProtocolOptions || Object.keys(form.protocol_options).length)) {
        payload.protocol_options = { ...form.protocol_options }
      }
      if (!isEdit.value || allowlist_node_ids.length !== savedAllowlistNodeIds.length ||
        allowlist_node_ids.some(id => !savedAllowlistNodeIds.includes(id))) {
        payload.allowlist_node_ids = allowlist_node_ids
      }
      if (isEdit.value) {
        await updateTunnel(editId.value, payload)
        ElMessage.success('隧道已更新')
      } else {
        await createTunnel(payload)
        ElMessage.success('隧道已添加并启动')
      }
    }
    dialogVisible.value = false
    fetchData()
  } catch (error) {
    if (allowlistOnly.value) {
      allowlistError.value = error.response?.data?.message || '无法保存 Gost IP 白名单，请重试。'
      await nextTick()
      allowlistErrorNotice.value?.focus()
      return
    }
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
    await confirmPanelAction('确定要删除隧道「' + row.name + '」吗？关联的端点配置仍可在端点管理中查看。', '删除隧道', {
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
  if (action === 'allowlist') openDialog(row, true)
  if (action === 'delete') handleDelete(row)
  if (action === 'copy') {
    openDialog()
    form.name = row.name.slice(0, 95) + '-copy'
    form.remark = row.remark || ''
    if (row.exit_rule?.role !== 'exit') {
      form.protocol = row.protocol || 'tcp'
      form.protocol_options = { ...row.protocol_options }
      Object.assign(form, targetFields(row))
    }
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

<style>
.tunnel-endpoint-options .el-select-dropdown__item { height: auto; padding-block: 8px; white-space: normal; line-height: 1.5; overflow-wrap: anywhere; }
</style>

<style scoped>
.tunnel-route { display: flex; align-items: center; gap: 8px; }
.tunnel-route-label { display: block; color: var(--ops-muted); font-size: var(--ops-text-xs); margin-bottom: 2px; }
.tunnel-route-arrow { color: var(--ops-accent); flex: none; }
.tunnel-dialog-error { margin: 0 0 12px; }
.tunnel-dialog-hint { margin: 0 0 12px; }
.tunnel-link { color: var(--ops-accent); }
.tunnel-form-hint { font-size: var(--ops-text-xs); line-height: 1.5; margin-top: 4px; }
.tunnel-connect-endpoint { display: block; overflow-wrap: anywhere; }
.tunnel-target-section { border-top: 1px solid var(--ops-border); padding-top: 16px; }
.tunnel-target-section h3 { margin: 0 0 8px; font-size: var(--ops-text-base); }
.tunnel-policy-summary { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 16px; margin: 0; }
.tunnel-policy-summary dt { color: var(--ops-muted); }
.tunnel-policy-summary dd { margin: 0; overflow-wrap: anywhere; }
.tunnel-policy-summary dd span { display: block; }
.tunnel-target-list { display: grid; gap: 8px; width: 100%; }
.tunnel-target-item { display: flex; align-items: center; gap: 8px; }
.tunnel-target-item .el-input { min-width: 0; }
.tunnel-target-list > .el-button { justify-self: start; }
.tunnel-allowlist-preview { margin: 0; padding: 12px; background: var(--ops-inset); border-radius: var(--ops-radius-inset); }
.tunnel-allowlist-preview dd { display: flex; flex-wrap: wrap; gap: 4px 12px; margin: 4px 0 0; overflow-wrap: anywhere; }
.tunnel-allowlist-error { color: var(--ops-danger); overflow-wrap: anywhere; }

@media (max-width: 700px) {
  .tunnel-route-cell { grid-column: 1 / -1; }
  .tunnel-route > span { flex: 1; min-width: 0; }
  .tunnel-route { gap: 6px; }
  .tunnel-allowlist-field :deep(.el-select__wrapper) { min-height: 44px; }
}
</style>
