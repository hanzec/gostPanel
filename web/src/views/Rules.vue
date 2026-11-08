<template>
  <div class="ops-page">
    <PageHeader
      eyebrow="TRAFFIC POLICY"
      title="规则管理"
      description="配置转发入口、监听协议与目标，并查看每条规则的运行状态和累计流量。"
    >
      <template #actions>
        <el-button :icon="Upload" @click="showImportDialog">导入 JSON</el-button>
        <el-button :icon="Download" :loading="exportLoading" @click="handleExport">导出 JSON</el-button>
        <el-button type="primary" :icon="Plus" @click="openDialog()">新建规则</el-button>
      </template>
    </PageHeader>

    <MetricStrip label="转发规则摘要" :items="summary" />

    <section class="ops-panel rule-panel" aria-labelledby="rule-list-title" :aria-busy="loading">
      <header class="rule-section-head">
        <div>
          <h2 id="rule-list-title">转发规则</h2>
          <p class="ops-muted">按入口、类型与状态定位规则</p>
        </div>
        <div class="rule-section-actions">
          <span class="ops-status" :class="listError ? 'is-warning' : 'is-success'">
            {{ listError ? '更新暂停' : '每 5 秒更新' }}
          </span>
          <el-button :icon="Refresh" :loading="loading" @click="fetchData()">刷新</el-button>
        </div>
      </header>

      <form class="ops-toolbar rule-filters" aria-label="规则筛选" @submit.prevent="applyFilters">
        <div class="rule-filter">
          <label for="rule-keyword">规则名称</label>
          <el-input id="rule-keyword" v-model="filters.keyword" :prefix-icon="Search" placeholder="输入名称" clearable />
        </div>
        <div class="rule-filter">
          <label for="rule-node">入口节点</label>
          <el-select id="rule-node" v-model="filters.node_id" aria-label="入口节点" placeholder="所有节点" clearable>
            <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
          </el-select>
        </div>
        <div class="rule-filter">
          <label for="rule-type">转发类型</label>
          <el-select id="rule-type" v-model="filters.type" aria-label="转发类型" placeholder="所有类型" clearable>
            <el-option label="端口转发" value="forward" />
            <el-option label="隧道转发" value="tunnel" />
          </el-select>
        </div>
        <div class="rule-filter">
          <label for="rule-status">运行状态</label>
          <el-select id="rule-status" v-model="filters.status" aria-label="运行状态" placeholder="所有状态" clearable>
            <el-option label="运行中" value="running" />
            <el-option label="已停止" value="stopped" />
            <el-option label="异常" value="error" />
          </el-select>
        </div>
        <div class="rule-filter-actions">
          <el-button text @click="clearFilters">重置</el-button>
          <el-button native-type="submit" :icon="Search">应用筛选</el-button>
        </div>
      </form>

      <div v-if="listError" class="rule-error" role="alert">
        <div>
          <strong>规则加载失败</strong>
          <p>{{ ruleList.length ? '下方保留了上次获取的结果。' : '请检查连接后重试。' }}</p>
        </div>
        <el-button :loading="loading" @click="fetchData()">重试</el-button>
      </div>

      <div v-if="!ruleList.length && loading" class="rule-placeholder" role="status">正在读取转发规则…</div>
      <div v-else-if="!ruleList.length && !listError" class="rule-placeholder">
        <el-icon aria-hidden="true"><Switch /></el-icon>
        <strong>{{ hasFilters ? '没有匹配的规则' : '还没有转发规则' }}</strong>
        <p>{{ hasFilters ? '调整条件，或清除当前筛选查看全部规则。' : '创建规则以定义从监听端口到目标地址的转发路径。' }}</p>
        <el-button v-if="hasFilters" @click="clearFilters">清除筛选</el-button>
        <el-button v-else :icon="Plus" @click="openDialog()">新建规则</el-button>
      </div>

      <div
        v-if="ruleList.length"
        class="ops-table-scroll rule-table-scroll"
        tabindex="0"
        role="region"
        aria-label="转发规则列表"
      >
        <table class="rule-table" role="table">
          <thead role="rowgroup">
            <tr role="row">
              <th scope="col" role="columnheader">规则</th>
              <th scope="col" role="columnheader">转发路径</th>
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
                  <span>{{ row.type === 'tunnel' ? '隧道转发' : '端口转发' }}</span>
                </span>
              </th>
              <td class="rule-path" role="cell" data-label="转发路径">
                <span class="rule-path-entry">
                  {{ row.type === 'tunnel' ? row.tunnel?.entry_node?.name || '未知入口' : row.node?.name || '未知入口' }}
                  <span v-if="row.tunnel" class="ops-muted">· {{ row.tunnel.name }}</span>
                </span>
                <span class="rule-path-target ops-mono">
                  <el-icon aria-hidden="true"><Right /></el-icon>
                  {{ row.targets?.[0] || '—' }}
                </span>
                <details v-if="row.targets?.length > 1" class="rule-target-details">
                  <summary>另有 {{ row.targets.length - 1 }} 个目标</summary>
                  <ul class="ops-mono">
                    <li v-for="(target, index) in row.targets.slice(1)" :key="index">{{ target }}</li>
                  </ul>
                </details>
              </td>
              <td role="cell" data-label="监听">
                <span class="rule-protocol ops-mono">{{ (row.protocol || 'tcp').toUpperCase() }}</span>
                <span class="ops-mono">:{{ row.listen_port }}</span>
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
                    text
                    :type="row.status === 'running' ? 'warning' : 'success'"
                    :loading="pendingRuleIds.has(row.id)"
                    :aria-label="(row.status === 'running' ? '停止 ' : '启动 ') + row.name"
                    @click="toggleRule(row)"
                  >
                    {{ row.status === 'running' ? '停止' : '启动' }}
                  </el-button>
                  <el-button text :aria-label="'编辑 ' + row.name" @click="openDialog(row)">编辑</el-button>
                  <el-dropdown trigger="click" @command="handleRowCommand(row, $event)">
                    <el-button text :aria-label="row.name + '的更多操作'">
                      更多 <el-icon aria-hidden="true"><ArrowDown /></el-icon>
                    </el-button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="copy">复制规则</el-dropdown-item>
                        <el-dropdown-item command="delete" divided :disabled="pendingRuleIds.has(row.id)">删除规则</el-dropdown-item>
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
        <span class="ops-muted">共 {{ total }} 条{{ hasFilters ? '匹配' : '规则' }}</span>
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          :pager-count="5"
          layout="sizes, prev, pager, next"
          aria-label="规则列表分页"
        />
      </footer>
    </section>

    <PanelDialog
      v-model="dialogVisible"
      :title="isEdit ? '编辑转发规则' : '新建转发规则'"
      :close-on-click-modal="false"
      destroy-on-close
    >
      <el-form ref="formRef" class="rule-editor" :model="form" :rules="formRules" label-position="top" @submit.prevent="handleSubmit">
        <section class="rule-form-section" aria-labelledby="rule-entry-title">
          <header>
            <h3 id="rule-entry-title">入口与监听</h3>
            <p class="ops-muted">选择流量入口，指定对外监听的协议与端口。</p>
          </header>
          <div class="rule-form-grid">
            <el-form-item label="规则名称" prop="name" class="rule-form-wide">
              <el-input v-model.trim="form.name" placeholder="例如 办公网络入口" maxlength="100" />
            </el-form-item>
            <el-form-item label="转发类型" prop="type">
              <el-select v-model="form.type" :disabled="isEdit" @change="handleTypeChange">
                <el-option label="端口转发" value="forward" />
                <el-option label="隧道转发" value="tunnel" />
              </el-select>
            </el-form-item>
            <el-form-item v-if="form.type === 'forward'" label="入口节点" prop="node_id">
              <el-select v-model="form.node_id" placeholder="选择直接转发节点" :disabled="isEdit">
                <el-option v-for="node in nodeList" :key="node.id" :label="node.name" :value="node.id" />
              </el-select>
            </el-form-item>
            <el-form-item v-else label="转发隧道" prop="tunnel_id">
              <el-select v-model="form.tunnel_id" placeholder="选择隧道链路" :disabled="isEdit">
                <el-option
                  v-for="tunnel in tunnelList"
                  :key="tunnel.id"
                  :label="tunnel.name + '（' + (tunnel.entry_node?.name || '—') + ' → ' + (tunnel.exit_node?.name || '—') + '）'"
                  :value="tunnel.id"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="协议" prop="protocol">
              <el-select v-model="form.protocol">
                <el-option label="TCP" value="tcp" />
                <el-option label="UDP" value="udp" />
              </el-select>
            </el-form-item>
            <el-form-item label="监听端口" prop="listen_port">
              <el-input-number v-model="form.listen_port" :min="1" :max="65535" controls-position="right" />
            </el-form-item>
          </div>
        </section>

        <section class="rule-form-section" aria-labelledby="rule-target-title">
          <header>
            <h3 id="rule-target-title">目标与策略</h3>
            <p class="ops-muted">目标地址填写主机名或 IP 与端口。多目标可指定负载均衡策略。</p>
          </header>
          <div class="rule-form-grid">
            <el-form-item label="目标地址" class="rule-form-wide">
              <div class="rule-target-list">
                <div v-for="(item, index) in form.targetList" :key="index" class="rule-target-item">
                  <el-input
                    v-model.trim="item.address"
                    :aria-label="'目标地址 ' + (index + 1)"
                    placeholder="例如 192.168.1.10:8080"
                  />
                  <el-button
                    text
                    type="danger"
                    :icon="Remove"
                    :aria-label="'移除目标地址 ' + (index + 1)"
                    @click="removeTarget(index)"
                  />
                </div>
                <el-button :icon="Plus" @click="addTarget">添加目标地址</el-button>
              </div>
            </el-form-item>
            <el-form-item label="负载均衡" prop="strategy">
              <el-select v-model="form.strategy">
                <el-option label="轮询" value="round" />
                <el-option label="随机" value="rand" />
                <el-option label="先进先出" value="fifo" />
                <el-option label="哈希" value="hash" />
              </el-select>
            </el-form-item>
            <el-form-item label="备注" prop="remark" class="rule-form-wide">
              <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可选，记录用途或维护说明" />
            </el-form-item>
          </div>
        </section>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitLoading" @click="handleSubmit">
          {{ isEdit ? '保存规则' : '创建规则' }}
        </el-button>
      </template>
    </PanelDialog>

    <PanelDialog
      v-model="exportDialogVisible"
      title="导出规则"
      description="复制导出的规则配置，可在另一面板中通过 JSON 导入。"
    >
      <el-input v-model="exportJsonData" class="rule-json" type="textarea" :rows="12" aria-label="导出的 JSON 配置" readonly />
      <template #footer>
        <el-button @click="exportDialogVisible = false">关闭</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyExportData">复制 JSON</el-button>
      </template>
    </PanelDialog>

    <PanelDialog
      v-model="importDialogVisible"
      title="导入规则"
      description="粘贴 JSON 对象或数组以创建规则。导入前请检查入口与监听端口。"
    >
      <el-input
        v-model="importJsonData"
        class="rule-json"
        type="textarea"
        :rows="12"
        aria-label="要导入的 JSON 配置"
        :aria-describedby="importError ? 'rule-import-error' : undefined"
        placeholder="粘贴从规则页导出的 JSON"
      />
      <p v-if="importError" id="rule-import-error" class="rule-inline-error" role="alert">{{ importError }}</p>
      <template #footer>
        <el-button @click="importDialogVisible = false">取消</el-button>
        <el-button type="primary" :icon="Upload" :loading="importLoading" @click="handleImport">创建导入规则</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import PanelDialog from '@/components/PanelDialog.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { ArrowDown, CircleCheck, CopyDocument, Download, Plus, Refresh, Remove, Right, Search, Switch, Upload, VideoPause, Warning } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getNodeList } from '@/api/node'
import { createRule, deleteRule, getRuleList, startRule, stopRule, updateRule } from '@/api/rule'
import { getTunnelList } from '@/api/tunnel'

const nodeList = ref([])
const tunnelList = ref([])
const ruleList = ref([])
const loading = ref(false)
const listError = ref(false)
const page = ref(1)
const pageSize = ref(10)
const total = ref(0)

const emptyFilters = { keyword: '', node_id: '', type: '', status: '' }
const filters = reactive({ ...emptyFilters })
const activeFilters = ref({ ...emptyFilters })
const hasFilters = computed(() => Object.values(activeFilters.value).some(Boolean))

const statusClass = { running: 'is-success', stopped: 'is-muted', error: 'is-danger' }
const statusText = { running: '运行中', stopped: '已停止', error: '异常' }
const pendingRuleIds = reactive(new Set())
const summary = computed(() => {
  const unavailable = (loading.value || listError.value) && !ruleList.value.length
  const count = status => unavailable ? null : ruleList.value.filter(rule => rule.status === status).length
  return [
    { label: '匹配规则', value: unavailable ? null : total.value, detail: '所有匹配页', icon: Switch },
    { label: '本页运行中', value: count('running'), icon: CircleCheck, tone: 'success' },
    { label: '本页已停止', value: count('stopped'), icon: VideoPause },
    { label: '本页异常', value: count('error'), icon: Warning, tone: 'danger' }
  ]
})

const dialogVisible = ref(false)
const isEdit = ref(false)
const editId = ref(null)
const submitLoading = ref(false)
const formRef = ref(null)
const form = reactive({
  type: 'forward',
  node_id: '',
  tunnel_id: null,
  name: '',
  protocol: 'tcp',
  listen_port: 8000,
  targetList: [{ address: '' }],
  strategy: 'round',
  remark: ''
})

const validateEntry = (rule, value, callback) => {
  if (form.type === 'forward' && !form.node_id) {
    callback(new Error('请选择入口节点'))
  } else if (form.type === 'tunnel' && !form.tunnel_id) {
    callback(new Error('请选择转发隧道'))
  } else {
    callback()
  }
}

const formRules = {
  type: [{ required: true, message: '请选择转发类型', trigger: 'change' }],
  node_id: [{ validator: validateEntry, trigger: 'change' }],
  tunnel_id: [{ validator: validateEntry, trigger: 'change' }],
  name: [{ required: true, message: '请输入规则名称', trigger: 'blur' }],
  protocol: [{ required: true, message: '请选择协议', trigger: 'change' }],
  listen_port: [{ required: true, message: '请输入监听端口', trigger: 'change' }]
}

const exportDialogVisible = ref(false)
const exportJsonData = ref('')
const exportLoading = ref(false)
const importDialogVisible = ref(false)
const importJsonData = ref('')
const importLoading = ref(false)
const importError = ref('')

const byteUnits = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
const formatBytes = (bytes = 0) => {
  if (!bytes) return '0 B'
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), byteUnits.length - 1)
  return Math.round((bytes / 1024 ** index) * 100) / 100 + ' ' + byteUnits[index]
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

  Object.assign(form, {
    type: row?.type || 'forward',
    node_id: row?.node_id || '',
    tunnel_id: row?.tunnel_id || null,
    name: row ? row.name + (copy ? '-copy' : '') : '',
    protocol: row?.protocol || 'tcp',
    listen_port: copy ? Math.min(row.listen_port + 1, 65535) : row?.listen_port || 8000,
    targetList: row?.targets?.length ? row.targets.map(address => ({ address })) : [{ address: '' }],
    strategy: row?.strategy || 'round',
    remark: row?.remark || ''
  })
  dialogVisible.value = true
}

const handleSubmit = async () => {
  if (!formRef.value || submitLoading.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  submitLoading.value = true
  try {
    const payload = {
      type: form.type,
      node_id: form.type === 'forward' ? form.node_id : null,
      tunnel_id: form.type === 'tunnel' ? form.tunnel_id : null,
      name: form.name,
      protocol: form.protocol,
      listen_port: form.listen_port,
      targets: form.targetList.map(item => item.address).filter(Boolean),
      strategy: form.strategy,
      remark: form.remark
    }
    if (isEdit.value) {
      await updateRule(editId.value, payload)
    } else {
      await createRule(payload)
    }
    ElMessage.success(isEdit.value ? '规则已保存' : '规则已创建')
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
    ElMessage.success(successMessage)
    fetchData()
  } catch {
    // The request interceptor displays the backend error.
  } finally {
    pendingRuleIds.delete(row.id)
  }
}

const toggleRule = row => {
  const running = row.status === 'running'
  return runRuleAction(row, running ? stopRule : startRule, running ? '规则已停止' : '规则已启动')
}

const handleRowCommand = async (row, command) => {
  if (command === 'copy') {
    openDialog(row, true)
    return
  }
  try {
    await confirmPanelAction('确定删除规则「' + row.name + '」？', '删除规则', {
      confirmButtonText: '删除规则',
      cancelButtonText: '取消',
      confirmButtonClass: 'el-button--danger',
      type: 'warning'
    })
  } catch {
    return
  }
  await runRuleAction(row, deleteRule, '规则已删除')
}

const handleExport = async () => {
  exportLoading.value = true
  try {
    const rows = []
    const batchSize = 100
    for (let exportPage = 1; ; exportPage++) {
      const { data } = await getRuleList({ page: exportPage, pageSize: batchSize })
      const batch = data.list || []
      rows.push(...batch)
      if (rows.length >= data.total || batch.length < batchSize) break
    }
    if (!rows.length) {
      ElMessage.warning('没有可导出的规则')
      return
    }

    exportJsonData.value = JSON.stringify(rows.map(rule => ({
      name: rule.name,
      type: rule.type,
      node_id: rule.node_id,
      tunnel_id: rule.tunnel_id,
      protocol: rule.protocol,
      listen_port: rule.listen_port,
      targets: rule.targets,
      strategy: rule.strategy,
      remark: rule.remark || ''
    })), null, 2)
    exportDialogVisible.value = true
  } catch {
    // The request interceptor displays the backend error.
  } finally {
    exportLoading.value = false
  }
}

const copyExportData = async () => {
  try {
    await navigator.clipboard.writeText(exportJsonData.value)
    ElMessage.success('已复制 JSON')
  } catch {
    ElMessage.warning('剪贴板不可用，请选中文本手动复制。')
  }
}

const showImportDialog = () => {
  importJsonData.value = ''
  importError.value = ''
  importDialogVisible.value = true
}

const handleImport = async () => {
  importError.value = ''
  if (!importJsonData.value.trim()) {
    importError.value = '请粘贴规则配置。'
    return
  }

  let rows
  try {
    const parsed = JSON.parse(importJsonData.value)
    rows = Array.isArray(parsed) ? parsed : [parsed]
  } catch {
    importError.value = 'JSON 格式有误，请检查括号和引号。'
    return
  }
  if (!rows.length || rows.some(rule => !rule || typeof rule !== 'object' || Array.isArray(rule))) {
    importError.value = '请输入规则对象或非空的规则数组。'
    return
  }

  importLoading.value = true
  let successCount = 0
  for (const rule of rows) {
    try {
      await createRule({
        name: rule.name || '导入规则',
        type: rule.type || 'forward',
        node_id: rule.node_id,
        tunnel_id: rule.tunnel_id,
        protocol: rule.protocol || 'tcp',
        listen_port: rule.listen_port || 0,
        targets: rule.targets || [],
        strategy: rule.strategy || 'round',
        remark: rule.remark || ''
      })
      successCount++
    } catch {
      // Keep importing the remaining records.
    }
  }
  importLoading.value = false

  if (!successCount) {
    importError.value = '导入失败。请核对入口节点、隧道和监听端口。'
    return
  }
  importDialogVisible.value = false
  const failedCount = rows.length - successCount
  if (failedCount) {
    ElMessage.warning('已导入 ' + successCount + ' 条，失败 ' + failedCount + ' 条')
  } else {
    ElMessage.success('已导入 ' + successCount + ' 条规则')
  }
  fetchData()
}

const addTarget = () => form.targetList.push({ address: '' })
const removeTarget = index => form.targetList.splice(index, 1)
const handleTypeChange = type => {
  if (type === 'forward') {
    form.tunnel_id = null
  } else {
    form.node_id = ''
  }
}

let refreshTimer
watch([page, pageSize], () => fetchData())
onMounted(() => {
  getNodeList({ pageSize: 100 }).then(({ data }) => { nodeList.value = data.list || [] }).catch(() => {})
  getTunnelList({ pageSize: 100 }).then(({ data }) => { tunnelList.value = data.list || [] }).catch(() => {})
  fetchData()
  refreshTimer = setInterval(() => {
    if (!document.hidden && !loading.value && !listError.value) fetchData(true)
  }, 5000)
})
onBeforeUnmount(() => clearInterval(refreshTimer))
</script>

<style scoped>
.rule-panel {
  min-width: 0;
}

.rule-section-head,
.rule-section-actions,
.rule-filter-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.rule-section-head {
  justify-content: space-between;
  flex-wrap: wrap;
  padding: 20px 20px 16px;
}

.rule-section-head h2,
.rule-form-section h3 {
  margin: 0;
  color: var(--ops-text);
  font-size: 1rem;
  font-weight: 650;
}

.rule-section-head p,
.rule-form-section header p {
  margin: 4px 0 0;
  font-size: .875rem;
}

.rule-filters {
  display: grid;
  grid-template-columns: minmax(140px, 1.5fr) repeat(3, minmax(110px, 1fr)) auto;
  gap: 12px;
  align-items: end;
  padding: 16px 20px;
  border-top: 1px solid var(--ops-border);
  border-bottom: 0;
}

.rule-filter {
  display: grid;
  min-width: 0;
  gap: 6px;
}

.rule-filter label {
  color: var(--ops-muted);
  font-size: .8125rem;
  font-weight: 550;
}

.rule-filter-actions {
  gap: 8px;
}

.rule-filter-actions .el-button + .el-button,
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
  font-size: .875rem;
}

.rule-table th,
.rule-table td {
  padding: 14px 12px;
  vertical-align: middle;
}

.rule-table th:first-child,
.rule-table td:first-child {
  padding-left: 20px;
}

.rule-table th:last-child,
.rule-table td:last-child {
  padding-right: 20px;
}

.rule-table thead {
  color: var(--ops-muted);
  background: var(--ops-inset);
}

.rule-table thead th {
  font-size: .75rem;
  font-weight: 650;
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
  font-weight: 650;
}

.rule-secondary {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-top: 4px;
  color: var(--ops-muted);
  font-size: .75rem;
  font-weight: 450;
}

.rule-path {
  min-width: 154px;
  max-width: 220px;
  overflow-wrap: anywhere;
}

.rule-path-entry {
  font-weight: 550;
}

.rule-path-target {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin-top: 3px;
  color: var(--ops-muted);
}

.rule-path-target .el-icon {
  flex: none;
  align-self: center;
}

.rule-target-details {
  margin-top: 4px;
  color: var(--ops-muted);
  font-size: .75rem;
}

.rule-target-details summary {
  width: fit-content;
  min-height: 24px;
  color: var(--ops-accent);
  cursor: pointer;
}

.rule-target-details ul {
  margin: 0;
  padding-left: 18px;
}

.rule-protocol {
  display: inline-block;
  margin-right: 5px;
  padding: 2px 5px;
  border: 1px solid var(--ops-border);
  border-radius: 4px;
  color: var(--ops-muted);
  font-size: .6875rem;
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
  padding: 12px 20px;
  border-top: 1px solid var(--ops-border);
  font-size: .8125rem;
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
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 12px 20px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-warning);
  font-size: .875rem;
}

.rule-editor {
  display: grid;
  gap: 14px;
}

.rule-form-section {
  min-width: 0;
  padding: 14px 16px 2px;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius);
  background: var(--ops-inset);
}

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

.rule-form-grid :deep(.el-select),
.rule-form-grid :deep(.el-input-number),
.rule-target-list {
  width: 100%;
}

.rule-target-list {
  display: grid;
  gap: 8px;
}

.rule-target-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.rule-target-list > .el-button {
  justify-self: start;
}

.rule-json :deep(textarea) {
  font-family: var(--ops-mono);
}

.rule-inline-error {
  margin: 8px 0 0;
  color: var(--ops-danger);
}

@media (max-width: 1160px) {
  .rule-filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .rule-filter-actions {
    grid-column: 1 / -1;
    justify-content: flex-end;
  }
}

@media (max-width: 700px) {
  .rule-section-head,
  .rule-filters,
  .rule-pagination,
  .rule-error {
    padding-left: 14px;
    padding-right: 14px;
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
    font-size: .75rem;
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
  .rule-filter-actions .el-button,
  .rule-filters :deep(.el-input__wrapper),
  .rule-filters :deep(.el-select__wrapper),
  .rule-section-actions .el-button,
  .rule-editor :deep(.el-input__wrapper),
  .rule-editor :deep(.el-select__wrapper),
  .rule-target-item .el-button,
  .rule-target-details summary,
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
