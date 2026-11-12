<template>
  <div class="ops-page">
    <PageHeader title="操作日志" description="按执行者、操作和资源追溯面板变更与自动升级，查看来源与详情。">
      <template #actions>
        <el-button :icon="Refresh" :loading="loading" @click="fetchData">刷新日志</el-button>
      </template>
    </PageHeader>

    <MetricStrip label="审计记录摘要" :items="summary" />

    <section class="ops-panel audit-panel" aria-labelledby="audit-title" :aria-busy="loading">
      <header class="audit-section-head">
        <div class="audit-section-heading">
          <span class="audit-mark"><el-icon aria-hidden="true"><Document /></el-icon></span>
          <div>
            <h2 id="audit-title">审计记录</h2>
            <p class="ops-muted">按操作时间倒序</p>
          </div>
        </div>
      </header>

      <form class="ops-toolbar audit-filters" aria-label="日志筛选" @submit.prevent="applyFilters">
        <div class="audit-filter">
          <label for="audit-user">执行者</label>
          <el-input id="audit-user" v-model="filters.username" :prefix-icon="Search" placeholder="输入用户名或系统" clearable />
        </div>
        <div class="audit-filter">
          <label for="audit-action">操作类型</label>
          <el-select id="audit-action" v-model="filters.action" aria-label="操作类型" placeholder="所有操作" clearable>
            <el-option v-for="(label, value) in actionLabels" :key="value" :label="label" :value="value" />
          </el-select>
        </div>
        <div class="audit-filter">
          <label for="audit-resource">资源类型</label>
          <el-select id="audit-resource" v-model="filters.resource_type" aria-label="资源类型" placeholder="所有资源" clearable>
            <el-option label="节点" value="node" />
            <el-option label="转发规则" value="rule" />
            <el-option label="隧道" value="tunnel" />
          </el-select>
        </div>
        <div class="audit-filter-actions">
          <el-button text @click="clearFilters">重置</el-button>
          <el-button native-type="submit" :icon="Search">应用筛选</el-button>
        </div>
      </form>

      <div v-if="listError" class="audit-error" role="alert">
        <div>
          <strong>日志加载失败</strong>
          <p>{{ logList.length ? '下方保留了上次获取的记录。' : '请检查连接后重试。' }}</p>
        </div>
        <el-button :loading="loading" @click="fetchData">重试</el-button>
      </div>

      <div v-if="!logList.length && loading" class="audit-placeholder" role="status">正在读取操作日志…</div>
      <div v-else-if="!logList.length && !listError" class="audit-placeholder">
        <el-icon aria-hidden="true"><Document /></el-icon>
        <strong>{{ hasFilters ? '没有匹配的记录' : '还没有操作记录' }}</strong>
        <p>{{ hasFilters ? '修改筛选条件，或清除筛选查看全部记录。' : '管理员变更与系统自动升级事件会记录在这里。' }}</p>
        <el-button v-if="hasFilters" @click="clearFilters">清除筛选</el-button>
      </div>

      <div
        v-if="logList.length"
        class="ops-table-scroll audit-table-scroll"
        tabindex="0"
        role="region"
        aria-label="操作日志列表"
      >
        <table class="audit-table" role="table">
          <thead role="rowgroup">
            <tr role="row">
              <th scope="col" role="columnheader">操作时间</th>
              <th scope="col" role="columnheader">执行者 / 来源</th>
              <th scope="col" role="columnheader">操作</th>
              <th scope="col" role="columnheader">资源</th>
              <th scope="col" role="columnheader">详情</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            <tr v-for="row in logList" :key="row.id" role="row">
              <th class="audit-when" scope="row" role="rowheader">
                <time class="ops-mono" :datetime="row.created_at">{{ formatDate(row.created_at) }}</time>
                <span class="audit-secondary ops-mono">#{{ row.id }}</span>
              </th>
              <td role="cell" data-label="执行者 / 来源">
                <strong>{{ row.username || '—' }}</strong>
                <span class="audit-secondary ops-mono">{{ row.ip_address || '—' }}</span>
              </td>
              <td role="cell" data-label="操作">
                <span class="ops-status" :class="actionClass[row.action] || 'is-muted'">
                  {{ actionLabels[row.action] || row.action || '—' }}
                </span>
              </td>
              <td role="cell" data-label="资源">
                {{ resourceLabel(row.resource_type) }}
                <span v-if="row.resource_id" class="audit-secondary ops-mono">#{{ row.resource_id }}</span>
              </td>
              <td class="audit-detail-cell" role="cell" data-label="详情">
                <span class="audit-detail-preview">{{ row.details || '—' }}</span>
                <el-button text :aria-label="'查看操作记录 ' + row.id" @click="openRecord(row)">查看详情</el-button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <footer v-if="logList.length || total > 0" class="ops-pagination audit-pagination">
        <span class="ops-muted">共 {{ total }} 条{{ hasFilters ? '匹配' : '记录' }}</span>
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50, 100]"
          :pager-count="5"
          layout="sizes, prev, pager, next"
          aria-label="日志列表分页"
        />
      </footer>
    </section>

    <PanelDialog
      v-model="recordVisible"
      :title="'操作记录 #' + (selectedRecord?.id || '')"
    >
      <template v-if="selectedRecord">
        <dl class="audit-record">
          <div>
            <dt>操作时间</dt>
            <dd class="ops-mono">{{ formatDate(selectedRecord.created_at) }}</dd>
          </div>
          <div>
            <dt>执行者</dt>
            <dd>{{ selectedRecord.username || '—' }}</dd>
          </div>
          <div>
            <dt>操作类型</dt>
            <dd>
              <span class="ops-status" :class="actionClass[selectedRecord.action] || 'is-muted'">
                {{ actionLabels[selectedRecord.action] || selectedRecord.action || '—' }}
              </span>
            </dd>
          </div>
          <div>
            <dt>资源</dt>
            <dd>
              {{ resourceLabel(selectedRecord.resource_type) }}
              <span v-if="selectedRecord.resource_id" class="ops-mono">#{{ selectedRecord.resource_id }}</span>
            </dd>
          </div>
          <div>
            <dt>来源 IP</dt>
            <dd class="ops-mono">{{ selectedRecord.ip_address || '—' }}</dd>
          </div>
          <div class="audit-record-wide">
            <dt>客户端 User-Agent</dt>
            <dd class="ops-mono">{{ selectedRecord.user_agent || '—' }}</dd>
          </div>
          <div class="audit-record-wide">
            <dt>操作详情</dt>
            <dd><pre>{{ selectedRecord.details || '—' }}</pre></dd>
          </div>
        </dl>
      </template>
      <template #footer>
        <el-button @click="recordVisible = false">关闭</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import PanelDialog from '@/components/PanelDialog.vue'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { Collection, Delete, Document, Refresh, Search, User } from '@element-plus/icons-vue'
import MetricStrip from '@/components/MetricStrip.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getLogList } from '@/api/log'

const logList = ref([])
const loading = ref(false)
const listError = ref(false)
const page = ref(1)
const pageSize = ref(10)
const total = ref(0)

const emptyFilters = { username: '', action: '', resource_type: '' }
const filters = reactive({ ...emptyFilters })
const activeFilters = ref({ ...emptyFilters })
const hasFilters = computed(() => Object.values(activeFilters.value).some(Boolean))

const actionLabels = { create: '创建', update: '更新', delete: '删除', start: '启动', stop: '停止', auto_upgrade: '自动升级' }
const actionClass = { create: 'is-success', update: 'is-warning', delete: 'is-danger', start: 'is-success', stop: 'is-muted', auto_upgrade: 'is-muted' }
const resourceLabels = { node: '节点', rule: '转发规则', tunnel: '隧道' }
const resourceLabel = type => resourceLabels[type] || type || '—'
const formatDate = value => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'
const summary = computed(() => {
  const unavailable = (loading.value || listError.value) && !logList.value.length
  const actors = new Set(logList.value.map(log => log.username).filter(Boolean))
  const resources = new Set(logList.value.filter(log => log.resource_type && log.resource_id)
    .map(log => log.resource_type + '/' + log.resource_id))
  return [
    { label: '匹配记录', value: unavailable ? null : total.value, detail: '所有匹配页', icon: Document },
    { label: '本页执行者', value: unavailable ? null : actors.size, icon: User },
    { label: '本页涉及资源', value: unavailable ? null : resources.size, icon: Collection },
    { label: '本页删除操作', value: unavailable ? null : logList.value.filter(log => log.action === 'delete').length, icon: Delete, tone: 'warning' }
  ]
})

const selectedRecord = ref(null)
const recordVisible = ref(false)

const openRecord = row => {
  selectedRecord.value = row
  recordVisible.value = true
}

let latestRequest = 0
const fetchData = async () => {
  const request = ++latestRequest
  loading.value = true
  try {
    const { data } = await getLogList({ page: page.value, pageSize: pageSize.value, ...activeFilters.value })
    if (request !== latestRequest) return
    logList.value = data.list || []
    total.value = data.total || 0
    listError.value = false
  } catch {
    if (request === latestRequest) listError.value = true
  } finally {
    if (request === latestRequest) loading.value = false
  }
}

const applyFilters = () => {
  activeFilters.value = { ...filters, username: filters.username.trim() }
  logList.value = []
  total.value = 0
  if (page.value === 1) fetchData()
  else page.value = 1
}

const clearFilters = () => {
  Object.assign(filters, emptyFilters)
  applyFilters()
}

watch([page, pageSize], fetchData)
onMounted(fetchData)
</script>

<style scoped>
.audit-panel {
  min-width: 0;
}

.audit-section-head,
.audit-section-heading,
.audit-filter-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.audit-section-head {
  justify-content: space-between;
  flex-wrap: wrap;
  padding: var(--ops-card-padding);
}

.audit-mark {
  display: grid;
  width: 40px;
  height: 40px;
  flex: none;
  place-items: center;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  color: var(--ops-accent);
  background: var(--ops-inset);
}

.audit-section-head h2 {
  margin: 0;
  font-size: var(--ops-text-title);
  font-weight: var(--ops-weight-semibold);
}

.audit-section-head p {
  margin: 4px 0 0;
  font-size: var(--ops-text-body);
}

.audit-filters {
  display: grid;
  grid-template-columns: minmax(155px, 1.4fr) repeat(2, minmax(120px, 1fr)) auto;
  gap: 12px;
  align-items: end;
  padding: var(--ops-inset-padding) var(--ops-card-padding);
  border-top: 1px solid var(--ops-border);
  border-bottom: 0;
}

.audit-filter {
  display: grid;
  min-width: 0;
  gap: 6px;
}

.audit-filter label {
  color: var(--ops-muted);
  font-size: var(--ops-text-sm);
  font-weight: var(--ops-weight-medium);
}

.audit-filter-actions {
  gap: 8px;
}

.audit-filter-actions .el-button + .el-button {
  margin-left: 0;
}

.audit-table-scroll {
  border-top: 1px solid var(--ops-border);
}

.audit-table {
  width: 100%;
  min-width: 670px;
  border-collapse: collapse;
  text-align: left;
  font-size: var(--ops-text-body);
}

.audit-table th,
.audit-table td {
  padding: 14px 12px;
  vertical-align: middle;
  overflow-wrap: anywhere;
}

.audit-table th:first-child {
  padding-left: var(--ops-card-padding);
}

.audit-table th:last-child,
.audit-table td:last-child {
  padding-right: var(--ops-card-padding);
}

.audit-table thead {
  color: var(--ops-muted);
  background: var(--ops-inset);
}

.audit-table thead th {
  font-size: var(--ops-text-xs);
  font-weight: var(--ops-weight-semibold);
  white-space: nowrap;
}

.audit-table tbody tr + tr {
  border-top: 1px solid var(--ops-border);
}

.audit-table tbody tr:hover,
.audit-table tbody tr:focus-within {
  background: var(--ops-raised);
}

.audit-when {
  min-width: 144px;
  font-weight: var(--ops-weight-medium);
}

.audit-secondary {
  display: block;
  margin-top: 4px;
  color: var(--ops-muted);
  font-size: var(--ops-text-xs);
  font-weight: var(--ops-weight-normal);
}

.audit-detail-cell {
  min-width: 140px;
  max-width: 350px;
}

.audit-detail-preview {
  display: -webkit-box;
  overflow: hidden;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  line-height: 1.45;
}

.audit-detail-cell .el-button {
  min-height: 28px;
  padding: 2px 0;
  color: var(--ops-accent);
}

.audit-pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px 16px;
  padding: 12px var(--ops-card-padding);
  border-top: 1px solid var(--ops-border);
  font-size: var(--ops-text-sm);
}

.audit-pagination :deep(.el-pagination) {
  max-width: 100%;
  flex-wrap: wrap;
  row-gap: 8px;
}

.audit-placeholder {
  display: grid;
  justify-items: center;
  gap: 8px;
  padding: 36px 16px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-muted);
  text-align: center;
}

.audit-placeholder .el-icon {
  color: var(--ops-accent);
  font-size: 24px;
}

.audit-placeholder strong {
  color: var(--ops-text);
}

.audit-placeholder p,
.audit-error p {
  margin: 0;
}

.audit-error {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 12px var(--ops-card-padding);
  border-top: 1px solid var(--ops-border);
  color: var(--ops-warning);
  font-size: var(--ops-text-body);
}

.audit-record {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin: 0;
}

.audit-record-wide {
  grid-column: 1 / -1;
}

.audit-record dt {
  color: var(--ops-muted);
  font-size: var(--ops-text-sm);
}

.audit-record dd {
  margin: 4px 0 0;
  color: var(--ops-text);
  overflow-wrap: anywhere;
}

.audit-record pre {
  margin: 0;
  padding: 12px;
  border: 1px solid var(--ops-border);
  border-radius: var(--ops-radius-inset);
  background: var(--ops-inset);
  font: inherit;
  white-space: pre-wrap;
}

@media (max-width: 1040px) {
  .audit-filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .audit-filter:first-child {
    grid-column: 1 / -1;
  }

  .audit-filter-actions {
    grid-column: 1 / -1;
    justify-content: flex-end;
  }
}

@media (max-width: 700px) {
  .audit-section-head,
  .audit-filters,
  .audit-pagination,
  .audit-error {
    padding-left: var(--ops-card-padding);
    padding-right: var(--ops-card-padding);
  }

  .audit-table {
    display: block;
    min-width: 0;
  }

  .audit-table thead {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .audit-table tbody {
    display: block;
  }

  .audit-table tbody tr {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px 16px;
    padding: 14px;
  }

  .audit-table tbody th,
  .audit-table tbody td {
    min-width: 0;
    max-width: none;
    padding: 0;
  }

  .audit-table tbody th:first-child,
  .audit-table tbody td:last-child {
    padding: 0;
  }

  .audit-table td[data-label]::before {
    display: block;
    margin-bottom: 4px;
    color: var(--ops-muted);
    font-size: var(--ops-text-xs);
    content: attr(data-label);
  }

  .audit-when,
  .audit-detail-cell {
    grid-column: 1 / -1;
  }

  .audit-detail-cell .el-button,
  .audit-filter-actions .el-button,
  .audit-filters :deep(.el-input__wrapper),
  .audit-filters :deep(.el-select__wrapper),
  .audit-pagination :deep(.el-pagination button),
  .audit-pagination :deep(.el-pager li) {
    min-height: 44px;
  }
}

@media (max-width: 430px) {
  .audit-record {
    grid-template-columns: 1fr;
  }
}
</style>
