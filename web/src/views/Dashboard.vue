<template>
  <div class="ops-page overview">
    <PageHeader
      class="ops-panel overview-banner"
      eyebrow="OVERVIEW / CONTROL PLANE"
      title="运行总览"
      description="查看 Gost 节点、转发规则和隧道状态，追踪管理操作。"
    >
      <template #actions>
        <el-button :icon="Refresh" :loading="statsLoading || logsLoading" @click="refreshOverview">刷新</el-button>
        <RouterLink class="overview-primary" to="/nodes">
          查看节点
          <el-icon aria-hidden="true"><ArrowRight /></el-icon>
        </RouterLink>
      </template>
    </PageHeader>

    <div v-if="statsError" class="ops-panel overview-feedback" role="alert">
      <span>{{ stats ? '状态更新失败，当前显示上次获取的数据。' : '暂时无法获取运行状态。' }}</span>
      <el-button :loading="statsLoading" @click="loadStats">重试</el-button>
    </div>

    <section v-if="stats || statsLoading" class="overview-kpis" aria-label="资源概况" :aria-busy="statsLoading">
      <p v-if="!stats" class="overview-sr-only" role="status">正在加载资源统计</p>
      <article v-for="item in groups" :key="item.name" class="ops-panel kpi-card">
        <header class="kpi-header">
          <div>
            <p class="overview-overline">{{ item.eyebrow }}</p>
            <h2>{{ item.name }}</h2>
          </div>
          <div class="kpi-symbol" aria-hidden="true"><el-icon><component :is="item.icon" /></el-icon></div>
        </header>
        <div v-if="!stats" class="kpi-loading" aria-hidden="true">
          <el-skeleton-item variant="text" />
          <el-skeleton-item variant="text" />
        </div>
        <template v-else>
          <p class="kpi-value ops-mono">{{ formatCount(item.total) }}</p>
          <div class="distribution-track kpi-track" aria-hidden="true">
            <div class="distribution-fill" :style="{ width: activeShare(item) + '%' }"></div>
          </div>
          <div class="kpi-labels">
            <span class="ops-status is-success">{{ item.activeLabel }} <b class="ops-mono">{{ formatCount(item.active) }}</b></span>
            <span class="ops-status is-muted">{{ item.inactiveLabel }} <b class="ops-mono">{{ formatCount(item.inactive) }}</b></span>
          </div>
        </template>
      </article>
    </section>

    <div v-if="stats" class="overview-details">
      <section class="ops-panel overview-panel" aria-labelledby="overview-distribution-title">
        <header class="overview-panel-header">
          <div>
            <p class="overview-overline">DISTRIBUTION</p>
            <h2 id="overview-distribution-title">状态分布</h2>
          </div>
          <el-icon aria-hidden="true"><DataAnalysis /></el-icon>
        </header>
        <dl class="distribution-list">
          <div v-for="item in groups" :key="item.name" class="distribution-row">
            <dt>{{ item.name }}</dt>
            <dd>
              <div class="distribution-track" aria-hidden="true">
                <div class="distribution-fill" :style="{ width: activeShare(item) + '%' }"></div>
              </div>
              <span v-if="item.total" class="distribution-count ops-mono">
                {{ formatCount(item.active) }} / {{ formatCount(item.total) }}
                <span class="overview-sr-only">{{ item.activeLabel }}</span>
              </span>
              <span v-else class="distribution-count ops-muted">暂无资源</span>
            </dd>
          </div>
        </dl>
        <div class="distribution-types">
          <span>规则构成</span>
          <span>端口转发 <b class="ops-mono">{{ formatCount(stats.rules.forward_type) }}</b></span>
          <span>隧道转发 <b class="ops-mono">{{ formatCount(stats.rules.tunnel_type) }}</b></span>
        </div>
      </section>

      <section class="ops-panel overview-panel" aria-labelledby="overview-runtime-title">
        <header class="overview-panel-header">
          <div>
            <p class="overview-overline">RUNTIME</p>
            <h2 id="overview-runtime-title">运行环境</h2>
          </div>
          <el-icon aria-hidden="true"><Cpu /></el-icon>
        </header>
        <dl class="runtime-list">
          <div><dt>面板版本</dt><dd class="ops-mono">{{ stats.version || '—' }}</dd></div>
          <div><dt>数据库</dt><dd class="ops-mono">{{ databaseLabel }}</dd></div>
          <div><dt>状态获取时间</dt><dd class="ops-mono"><time :datetime="updatedAt.toISOString()">{{ timeFormat.format(updatedAt) }}</time></dd></div>
        </dl>
      </section>
    </div>

    <section class="ops-panel overview-activity" aria-labelledby="overview-activity-title" :aria-busy="logsLoading">
      <header class="overview-panel-header">
        <div>
          <p class="overview-overline">AUDIT LOG</p>
          <h2 id="overview-activity-title">最近操作</h2>
        </div>
        <RouterLink class="overview-link" to="/logs">查看全部 <el-icon aria-hidden="true"><ArrowRight /></el-icon></RouterLink>
      </header>
      <div v-if="logsError" class="overview-feedback" role="alert">
        <span>{{ recentLogs.length ? '日志更新失败，当前显示上次获取的记录。' : '暂时无法加载最近操作。' }}</span>
        <el-button :loading="logsLoading" @click="loadRecentLogs">重试</el-button>
      </div>
      <div v-if="recentLogs.length" class="ops-table-scroll overview-table" tabindex="0" role="region" aria-label="最近操作列表">
        <table>
          <thead><tr><th scope="col">时间</th><th scope="col">管理员</th><th scope="col">操作</th><th scope="col">详情</th></tr></thead>
          <tbody>
            <tr v-for="row in recentLogs" :key="row.id">
              <td class="ops-mono"><time :datetime="row.created_at">{{ formatTime(row.created_at) }}</time></td>
              <td>{{ row.username || '—' }}</td>
              <td><span class="ops-status" :class="actionLabels[row.action]?.[1] || 'is-muted'">{{ actionLabels[row.action]?.[0] || row.action }}</span></td>
              <td class="overview-log-detail">{{ row.details || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else-if="logsLoading" class="overview-activity-state" role="status" aria-label="正在加载最近操作">
        <el-skeleton :rows="2" animated />
      </div>
      <div v-else-if="!logsError" class="overview-activity-state overview-empty">
        <el-icon aria-hidden="true"><Document /></el-icon>
        <strong>暂无操作记录</strong>
        <span>节点、规则或隧道的管理活动会显示在这里。</span>
      </div>
    </section>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ArrowRight, Connection, Cpu, DataAnalysis, Document, Monitor, Refresh, Switch } from '@element-plus/icons-vue'
import { getLogList } from '@/api/log'
import { getDashboardStats } from '@/api/stats'
import PageHeader from '@/components/PageHeader.vue'

const stats = ref(null)
const updatedAt = ref(null)
const statsLoading = ref(true)
const statsError = ref(false)
const recentLogs = ref([])
const logsLoading = ref(true)
const logsError = ref(false)

const actionLabels = {
  create: ['创建', 'is-success'],
  update: ['更新', 'is-warning'],
  delete: ['删除', 'is-danger'],
  start: ['启动', 'is-success'],
  stop: ['停止', 'is-muted']
}
const countFormat = new Intl.NumberFormat('zh-CN')
const timeFormat = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', hour12: false })
const formatCount = (value) => value == null ? '—' : countFormat.format(value)
const formatTime = (value) => value ? timeFormat.format(new Date(value)) : '—'

const groups = computed(() => [
  {
    name: '节点', eyebrow: 'GOST NODES', icon: Monitor, total: stats.value?.nodes.total,
    active: stats.value?.nodes.online, inactive: stats.value?.nodes.offline,
    activeLabel: '在线', inactiveLabel: '离线'
  },
  {
    name: '规则', eyebrow: 'FORWARDING RULES', icon: Switch, total: stats.value?.rules.total,
    active: stats.value?.rules.running, inactive: stats.value?.rules.stopped,
    activeLabel: '运行', inactiveLabel: '未运行'
  },
  {
    name: '隧道', eyebrow: 'RELAY TUNNELS', icon: Connection, total: stats.value?.tunnels.total,
    active: stats.value?.tunnels.running, inactive: stats.value?.tunnels.stopped,
    activeLabel: '运行', inactiveLabel: '未运行'
  }
])
const databaseLabel = computed(() => ({ mysql: 'MySQL', sqlite: 'SQLite' })[stats.value?.database] || stats.value?.database || '—')
const activeShare = (item) => item.total ? Math.min(100, item.active / item.total * 100) : 0

async function loadStats() {
  statsLoading.value = true
  statsError.value = false
  try {
    const { data } = await getDashboardStats()
    stats.value = data
    updatedAt.value = new Date()
  } catch {
    statsError.value = true
  } finally {
    statsLoading.value = false
  }
}

async function loadRecentLogs() {
  logsLoading.value = true
  logsError.value = false
  try {
    const { data } = await getLogList({ page: 1, pageSize: 5 })
    recentLogs.value = data.list || []
  } catch {
    logsError.value = true
  } finally {
    logsLoading.value = false
  }
}

function refreshOverview() {
  loadStats()
  loadRecentLogs()
}

onMounted(refreshOverview)
</script>

<style scoped>
.overview-banner {
  align-items: center;
  padding: 24px;
  background-image: linear-gradient(90deg, var(--ops-panel) 50%, transparent),
    radial-gradient(circle, color-mix(in srgb, var(--ops-accent) 24%, transparent) 1px, transparent 1px);
  background-size: 100% 100%, 16px 16px;
}

.overview-primary,
.overview-link {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  font-weight: 650;
  text-decoration: none;
  white-space: nowrap;
}

.overview-primary {
  padding: 0 16px;
  border-radius: 8px;
  background: var(--ops-accent);
  color: var(--ops-bg);
}

.overview-primary:hover { filter: brightness(1.08); }
.overview-link { color: var(--ops-accent); }
.overview-link:hover { text-decoration: underline; }

.overview-kpis,
.overview-details {
  display: grid;
  gap: 16px;
  min-width: 0;
}

.overview-kpis { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.overview-details { grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); }

.kpi-card,
.overview-panel { padding: 20px; }

.kpi-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.kpi-header h2 { margin: 0; font-size: 15px; font-weight: 650; }

.kpi-symbol {
  display: grid;
  flex: none;
  width: 36px;
  height: 36px;
  place-items: center;
  border: 1px solid var(--ops-border);
  border-radius: 9px;
  background: var(--ops-inset);
  color: var(--ops-accent);
  font-size: 18px;
}

.kpi-value {
  margin: 16px 0 12px;
  color: var(--ops-text);
  font-size: clamp(2rem, 3vw, 2.75rem);
  font-weight: 650;
  line-height: 1;
  overflow-wrap: anywhere;
}

.kpi-labels { display: flex; flex-wrap: wrap; gap: 8px; }
.kpi-track { margin: 0 0 12px; }
.kpi-labels b { font-weight: 600; }
.kpi-loading { display: grid; gap: 12px; margin-top: 20px; }
.kpi-loading > :first-child { width: 35%; height: 32px; }
.kpi-loading > :last-child { width: 65%; }

.overview-panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
}

.overview-panel-header > .el-icon { color: var(--ops-muted); font-size: 20px; }

.overview-overline {
  margin: 0 0 4px;
  color: var(--ops-muted);
  font-family: var(--ops-mono);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: .12em;
}

.overview-panel-header h2 { margin: 0; font-size: 18px; font-weight: 650; }
.distribution-list, .runtime-list { margin: 16px 0 0; }

.distribution-row {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  min-height: 36px;
  font-size: 14px;
}

.distribution-row dd {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  margin: 0;
}

.distribution-track {
  height: 6px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--ops-raised);
}

.distribution-fill { height: 100%; border-radius: inherit; background: var(--ops-accent); }
.distribution-count { min-width: 64px; text-align: end; font-size: 12px; }

.distribution-types {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-muted);
  font-size: 13px;
}

.distribution-types b { margin-inline-start: 4px; color: var(--ops-text); }

.runtime-list > div {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--ops-border);
  font-size: 14px;
}

.runtime-list > div:last-child { border-bottom: 0; }
.runtime-list dt { color: var(--ops-muted); white-space: nowrap; }
.runtime-list dd { margin: 0; text-align: end; overflow-wrap: anywhere; }

.overview-activity { min-width: 0; }
.overview-activity > .overview-panel-header { padding: 16px 20px; }

.overview-table table {
  width: 100%;
  min-width: 580px;
  border-collapse: collapse;
  text-align: start;
  font-size: 14px;
}

.overview-table th,
.overview-table td {
  padding: 12px 16px;
  border-top: 1px solid var(--ops-border);
  text-align: start;
}

.overview-table th { color: var(--ops-muted); font-size: 13px; font-weight: 600; background: var(--ops-inset); }
.overview-table th:first-child, .overview-table td:first-child { padding-inline-start: 20px; white-space: nowrap; }
.overview-table td:nth-child(3) { white-space: nowrap; }
.overview-log-detail { overflow-wrap: anywhere; }

.overview-feedback {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 20px;
  color: var(--ops-warning);
}

.overview-activity-state { padding: 24px 20px; }
.overview-empty { display: grid; justify-items: center; gap: 8px; color: var(--ops-muted); text-align: center; }
.overview-empty > .el-icon { color: var(--ops-accent); font-size: 22px; }
.overview-empty strong { color: var(--ops-text); }
.overview-empty span { font-size: 14px; }

.overview-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

@media (max-width: 900px) {
  .overview-details { grid-template-columns: minmax(0, 1fr); }
}

@media (max-width: 700px) {
  .overview-banner { padding: 16px; }
  .overview-kpis { grid-template-columns: minmax(0, 1fr); gap: 12px; }
  .kpi-card { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px 12px; }
  .kpi-value { margin: 0; text-align: end; }
  .kpi-labels { grid-column: 1 / -1; }
  .kpi-track { grid-column: 1 / -1; margin: 4px 0; }
  .kpi-symbol { display: none; }
  .kpi-loading { min-width: 72px; margin: 0; }
  .kpi-loading > :first-child { width: 100%; }
  .kpi-card, .overview-panel { padding: 16px; }
  .overview-activity > .overview-panel-header { padding-inline: 16px; }
}
</style>
