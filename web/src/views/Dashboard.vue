<template>
  <div class="ops-page overview">
    <PageHeader
      class="ops-panel overview-banner"
      title="运行总览"
      description="实时查看隧道出口流量、节点和转发状态。"
    >
      <template #actions>
        <RouterLink class="overview-primary" to="/nodes">
          查看节点
          <el-icon aria-hidden="true"><ArrowRight /></el-icon>
        </RouterLink>
      </template>
    </PageHeader>

    <div v-if="statsError" class="ops-panel overview-feedback" role="alert">
      <span>{{ stats ? '状态更新失败，当前显示上次获取的数据。' : '暂时无法获取运行状态。' }}</span>
    </div>

    <section v-if="traffic" class="ops-panel overview-panel" aria-labelledby="overview-traffic-title">
      <header class="overview-panel-header">
        <div>
          <p class="overview-overline">LIVE TRAFFIC</p>
          <h2 id="overview-traffic-title">实时流量</h2>
        </div>
        <span class="ops-status" :class="statsError ? 'is-warning' : 'is-success'">
          {{ statsError ? '更新暂缓' : '每 5 秒更新' }}
        </span>
      </header>
      <dl class="traffic-totals">
        <div class="traffic-metric">
          <dt>累计总流量</dt>
          <dd class="ops-mono">{{ totalTrafficLabel(traffic.input_bytes + traffic.output_bytes) }}</dd>
          <dd class="traffic-note">{{ hasPendingExits ? '有运行中的隧道等待采集' : '各隧道出口累计流量之和' }}</dd>
        </div>
        <div v-for="direction in trafficDirections" :key="direction.key" class="traffic-metric">
          <dt>{{ direction.label }}速率</dt>
          <dd class="ops-mono">{{ formatRate(totalTrafficRate(direction.key)) }}</dd>
          <dd class="traffic-note">累计 {{ totalTrafficLabel(traffic[direction.key]) }}</dd>
        </div>
      </dl>
      <div class="traffic-exits">
        <h3 id="tunnel-traffic-title">隧道出口流量</h3>
        <p class="traffic-note">每条隧道由其 Gost Relay 独立采集。</p>
        <p v-if="!traffic.tunnels.length" class="traffic-note">暂无隧道。</p>
        <div v-else class="ops-table-scroll overview-table traffic-table" tabindex="0" role="region" aria-labelledby="tunnel-traffic-title">
          <table>
            <thead>
              <tr>
                <th scope="col">隧道</th>
                <th scope="col">出口节点</th>
                <th v-for="direction in trafficDirections" :key="direction.key" scope="col">{{ direction.label }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="tunnel in traffic.tunnels" :key="tunnel.id">
                <td>
                  <strong>{{ tunnel.name }}</strong>
                  <p v-if="!tunnel.measured" class="traffic-note">
                    {{ tunnel.status === 'running' ? '等待采集。旧隧道在下次启动后开始统计。' : '隧道启动后开始统计。' }}
                  </p>
                </td>
                <td>
                  <span>{{ tunnel.exit_node_name }}</span>
                  <span class="ops-status" :class="tunnel.exit_node_status === 'online' ? 'is-success' : 'is-muted'">
                    {{ tunnel.exit_node_status === 'online' ? '在线' : '离线' }}
                  </span>
                </td>
                <td v-for="direction in trafficDirections" :key="direction.key">
                  <span class="ops-mono">{{ formatRate(tunnelRate(tunnel, direction.key)) }}</span>
                  <p class="traffic-note">累计 {{ tunnel.measured ? formatTraffic(tunnel[direction.key]) : '—' }}</p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>

    <section v-if="stats || statsLoading" class="overview-kpis" aria-label="资源概况" :aria-busy="!stats && statsLoading">
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

    <section class="ops-panel overview-activity" aria-labelledby="overview-activity-title" :aria-busy="!recentLogs && logsLoading">
      <header class="overview-panel-header">
        <div>
          <p class="overview-overline">AUDIT LOG</p>
          <h2 id="overview-activity-title">最近操作</h2>
        </div>
        <RouterLink class="overview-link" to="/logs">查看全部 <el-icon aria-hidden="true"><ArrowRight /></el-icon></RouterLink>
      </header>
      <div v-if="logsError" class="overview-feedback" role="alert">
        <span>{{ recentLogs ? '日志更新失败，当前显示上次获取的记录。' : '暂时无法加载最近操作。' }}</span>
      </div>
      <div v-if="recentLogs?.length" class="ops-table-scroll overview-table" tabindex="0" role="region" aria-label="最近操作列表">
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
      <div v-else-if="!recentLogs && logsLoading" class="overview-activity-state" role="status" aria-label="正在加载最近操作">
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
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowRight, Connection, Cpu, DataAnalysis, Document, Monitor, Switch } from '@element-plus/icons-vue'
import { getLogList } from '@/api/log'
import { getDashboardStats } from '@/api/stats'
import PageHeader from '@/components/PageHeader.vue'

const stats = ref(null)
const updatedAt = ref(null)
const statsLoading = ref(false)
const statsError = ref(false)
const recentLogs = ref(null)
const logsLoading = ref(false)
const logsError = ref(false)
const previousTraffic = ref(null)
const sampleSeconds = ref(0)
let pollTimer

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
const trafficDirections = [{ key: 'input_bytes', label: '入站' }, { key: 'output_bytes', label: '出站' }]
const trafficUnits = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']
const trafficFormat = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 })

function formatTraffic(bytes) {
  const index = Math.min(Math.floor(Math.log2(Math.max(1, bytes)) / 10), trafficUnits.length - 1)
  return `${trafficFormat.format(bytes / 1024 ** index)} ${trafficUnits[index]}`
}

const formatRate = (bytes) => bytes == null ? '—' : `${formatTraffic(bytes)}/s`
const traffic = computed(() => stats.value?.traffic)
const previousTunnels = computed(() => new Map(previousTraffic.value?.tunnels.map((tunnel) => [tunnel.id, tunnel])))
const hasPendingExits = computed(() => traffic.value?.tunnels.some((tunnel) => tunnel.status === 'running' && !tunnel.measured))
const hasMeasuredExits = computed(() => traffic.value?.tunnels.some((tunnel) => tunnel.measured))
const totalTrafficLabel = (bytes) => traffic.value.tunnels.length && !hasMeasuredExits.value ? '—' : formatTraffic(bytes)

function trafficRate(current, previous) {
  return previous == null || current < previous || sampleSeconds.value <= 0 || statsError.value
    ? null
    : (current - previous) / sampleSeconds.value
}

function tunnelRate(tunnel, key) {
  const previous = previousTunnels.value.get(tunnel.id)
  if (!tunnel.measured || !previous?.measured || tunnel.exit_node_id !== previous.exit_node_id ||
      tunnel.status !== 'running' || tunnel.exit_node_status !== 'online' || previous.exit_node_status !== 'online') return null
  return trafficRate(tunnel[key], previous[key])
}

function totalTrafficRate(key) {
  const rates = traffic.value.tunnels.map((tunnel) => tunnelRate(tunnel, key)).filter((rate) => rate != null)
  return rates.length ? rates.reduce((total, rate) => total + rate, 0) : null
}

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
  if (statsLoading.value) return
  statsLoading.value = true
  try {
    const { data } = await getDashboardStats()
    const now = new Date()
    previousTraffic.value = traffic.value
    sampleSeconds.value = updatedAt.value ? (now - updatedAt.value) / 1000 : 0
    stats.value = data
    updatedAt.value = now
    statsError.value = false
  } catch {
    statsError.value = true
  } finally {
    statsLoading.value = false
  }
}

async function loadRecentLogs() {
  if (logsLoading.value) return
  logsLoading.value = true
  try {
    const { data } = await getLogList({ page: 1, pageSize: 5 }, { silentError: true })
    recentLogs.value = data.list || []
    logsError.value = false
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

onMounted(() => {
  refreshOverview()
  pollTimer = window.setInterval(refreshOverview, 5000)
})
onBeforeUnmount(() => window.clearInterval(pollTimer))
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
.traffic-totals {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 180px), 1fr));
  gap: 12px;
  margin: 16px 0 0;
}

.traffic-metric {
  min-width: 0;
  padding: 14px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
}

.traffic-metric dt, .traffic-note {
  color: var(--ops-muted);
  font-size: 13px;
}

.traffic-metric .traffic-note { margin: 0; }

.traffic-metric dd:not(.traffic-note) {
  margin: 6px 0;
  color: var(--ops-text);
  font-size: clamp(1.25rem, 3vw, 1.75rem);
  font-weight: 650;
  overflow-wrap: anywhere;
}

.traffic-exits { margin-top: 20px; }
.traffic-exits h3 { margin: 0; font-size: 15px; }
.traffic-exits p { margin: 6px 0 0; }
.traffic-table {
  margin: 12px 0 0;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
}

.traffic-table .ops-status { margin-inline-start: 8px; }
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
.traffic-table th { border-top: 0; }
.traffic-table td:first-child { white-space: normal; }
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
