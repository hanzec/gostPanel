<template>
  <div class="console-nav">
    <RouterLink class="console-brand" to="/dashboard" @click="$emit('navigate')">
      <span class="console-brand__mark" aria-hidden="true">
        <img v-if="logoUrl" :key="logoUrl" :src="logoUrl" :class="{ 'is-loaded': loadedLogo === logoUrl }" alt="" width="24" height="24" @load="loadedLogo = logoUrl" />
        <img v-if="!logoUrl || loadedLogo !== logoUrl" class="is-loaded" :src="defaultLogoUrl" alt="" width="24" height="24" />
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

    <div v-if="version" class="console-nav__foot">
      版本 <span class="ops-mono">{{ version }}</span>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { Connection, Document, Monitor, Odometer, Setting, Switch } from '@element-plus/icons-vue'
import { defaultLogoUrl } from '@/store/system'

defineProps({
  siteTitle: { type: String, required: true },
  logoUrl: { type: String, default: '' },
  version: { type: String, default: '' }
})
defineEmits(['navigate'])

const loadedLogo = ref('')
const groups = [
  { label: '监控', items: [{ to: '/dashboard', label: '运行总览', icon: Odometer }] },
  { label: '网络', items: [
    { to: '/nodes', label: '节点管理', icon: Monitor },
    { to: '/rules', label: '端点管理', icon: Switch },
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
  border-radius: var(--ops-radius-inset);
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
  border-radius: var(--ops-radius-inset);
  background: var(--ops-accent-bg);
  color: var(--ops-accent);
}

.console-brand__mark > * { grid-area: 1 / 1; }
.console-brand__mark img { opacity: 0; border-radius: 4px; object-fit: contain; }
.console-brand__mark img.is-loaded { opacity: 1; }
.console-brand__type { display: grid; min-width: 0; gap: 2px; }
.console-brand__type strong { overflow-wrap: anywhere; font-size: var(--ops-text-title); line-height: var(--ops-leading-heading); font-weight: var(--ops-weight-bold); }

.console-brand__type small {
  color: var(--ops-faint);
  font: var(--ops-weight-semibold) var(--ops-text-xs)/var(--ops-leading-body) var(--ops-mono);
  letter-spacing: .13em;
}

.console-nav__links, .console-nav__group { display: grid; gap: 4px; }
.console-nav__links { gap: 18px; }

.console-nav__label {
  padding: 0 11px 3px;
  color: var(--ops-faint);
  font-size: var(--ops-text-xs);
  font-weight: var(--ops-weight-semibold);
  letter-spacing: .1em;
}

.console-nav__link {
  display: flex;
  min-height: 42px;
  align-items: center;
  gap: 11px;
  padding: 8px 11px;
  border: 1px solid transparent;
  border-radius: var(--ops-radius-inset);
  color: var(--ops-muted);
  font-size: var(--ops-text-body);
  font-weight: var(--ops-weight-medium);
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
  margin-top: auto;
  padding: 13px 10px 2px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-faint);
  font-size: var(--ops-text-xs);
  overflow-wrap: anywhere;
}

@media (max-width: 900px) {
  .console-nav { padding: 12px; }
  .console-nav__link { min-height: 44px; }
}
</style>
