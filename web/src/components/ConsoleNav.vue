<template>
  <div class="console-nav">
    <RouterLink class="console-brand" to="/dashboard" @click="$emit('navigate')">
      <span class="console-brand__mark" aria-hidden="true">
        <img v-if="logoUrl" :key="logoUrl" :src="logoUrl" :class="{ 'is-loaded': loadedLogo === logoUrl }" alt="" width="24" height="24" @load="loadedLogo = logoUrl" />
        <svg v-if="!logoUrl || loadedLogo !== logoUrl" viewBox="0 0 24 24" fill="none" width="23" height="23">
          <path d="M4 15h5l3-6h8M6 9h3m6 6h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
          <circle cx="4" cy="15" r="1.6" fill="currentColor" />
          <circle cx="20" cy="9" r="1.6" fill="currentColor" />
          <circle cx="20" cy="15" r="1.6" fill="currentColor" />
        </svg>
      </span>
      <span class="console-brand__type">
        <strong>{{ siteTitle }}</strong>
        <small>GOST CONTROL</small>
      </span>
    </RouterLink>

    <nav class="console-nav__links" aria-label="管理导航">
      <div v-for="group in groups" :key="group.label" class="console-nav__group">
        <span class="console-nav__label">{{ group.label }}</span>
        <RouterLink
          v-for="item in group.items"
          :key="item.to"
          :to="item.to"
          class="console-nav__link"
          @click="$emit('navigate')"
        >
          <el-icon aria-hidden="true"><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </RouterLink>
      </div>
    </nav>

    <div class="console-nav__foot">
      <el-icon aria-hidden="true"><Lock /></el-icon>
      <span>内部管理控制台</span>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { Connection, Document, Lock, Monitor, Odometer, Setting, Switch } from '@element-plus/icons-vue'

defineProps({
  siteTitle: { type: String, required: true },
  logoUrl: { type: String, default: '' }
})
defineEmits(['navigate'])

const loadedLogo = ref('')
const groups = [
  { label: '监控', items: [{ to: '/dashboard', label: '运行总览', icon: Odometer }] },
  { label: '网络', items: [
    { to: '/nodes', label: '节点管理', icon: Monitor },
    { to: '/rules', label: '规则管理', icon: Switch },
    { to: '/tunnels', label: '隧道管理', icon: Connection }
  ] },
  { label: '管理', items: [
    { to: '/logs', label: '操作日志', icon: Document },
    { to: '/system', label: '系统设置', icon: Setting }
  ] }
]
</script>

<style scoped>
.console-nav {
  display: flex;
  min-height: 100%;
  flex-direction: column;
  gap: 28px;
  padding: 24px 14px 16px;
}

.console-brand {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 11px;
  margin-inline: 4px;
  border-radius: 9px;
  color: var(--ops-text);
  text-decoration: none;
}

.console-brand__mark {
  display: grid;
  width: 39px;
  height: 39px;
  flex: 0 0 39px;
  place-items: center;
  border: 1px solid var(--ops-accent-line);
  border-radius: 10px;
  background: var(--ops-accent-bg);
  color: var(--ops-accent);
}

.console-brand__mark > * { grid-area: 1 / 1; }
.console-brand__mark img { opacity: 0; border-radius: 4px; object-fit: contain; }
.console-brand__mark img.is-loaded { opacity: 1; }
.console-brand__type { display: grid; min-width: 0; gap: 2px; }
.console-brand__type strong { overflow-wrap: anywhere; font-size: 21px; line-height: 1.15; }

.console-brand__type small {
  color: var(--ops-faint);
  font: 600 9px/1.5 var(--ops-mono);
  letter-spacing: .13em;
}

.console-nav__links, .console-nav__group { display: grid; gap: 4px; }
.console-nav__links { gap: 18px; }

.console-nav__label {
  padding: 0 11px 3px;
  color: var(--ops-faint);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: .1em;
}

.console-nav__link {
  display: flex;
  min-height: 42px;
  align-items: center;
  gap: 11px;
  padding: 8px 11px;
  border: 1px solid transparent;
  border-radius: 8px;
  color: var(--ops-muted);
  font-size: 14px;
  font-weight: 570;
  text-decoration: none;
  transition: color 150ms, background-color 150ms, border-color 150ms;
}

.console-nav__link .el-icon { font-size: 17px; }
.console-nav__link:hover { background: var(--ops-raised); color: var(--ops-text); }

.console-nav__link.router-link-exact-active {
  border-color: var(--ops-accent-line);
  background: var(--ops-accent-bg);
  box-shadow: inset 2px 0 var(--ops-accent);
  color: var(--ops-accent);
}

.console-nav__foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: auto;
  padding: 13px 10px 2px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-faint);
  font-size: 12px;
}

@media (max-width: 900px) {
  .console-nav { padding: 12px; }
  .console-nav__link { min-height: 44px; }
}
</style>
