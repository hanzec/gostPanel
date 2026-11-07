<template>
  <div class="console-shell">
    <a class="console-skip" href="#console-main" @click.prevent="mainContent?.focus()">跳到主要内容</a>

    <aside class="console-sidebar" aria-label="侧栏">
      <ConsoleNav :site-title="system.siteTitle" :logo-url="system.logoUrl" />
    </aside>

    <div class="console-workspace">
      <header class="console-topbar">
        <div class="console-topbar__inner">
          <div class="console-topbar__location">
            <button
              type="button"
              class="console-icon-button console-menu-button"
              aria-label="打开管理导航"
              aria-haspopup="dialog"
              :aria-expanded="mobileMenu"
              @click="mobileMenu = true"
            >
              <el-icon aria-hidden="true"><Menu /></el-icon>
            </button>
            <nav class="console-crumb" aria-label="面包屑">
              <RouterLink to="/dashboard">控制台</RouterLink>
              <span aria-hidden="true">/</span>
              <span aria-current="page">{{ route.meta.title }}</span>
            </nav>
          </div>

          <div v-if="username" class="console-identity">
            <span class="console-identity__avatar" aria-hidden="true"><el-icon><User /></el-icon></span>
            <span class="console-identity__text">
              <strong>{{ username }}</strong>
              <small>Authentik 管理员</small>
            </span>
            <a class="console-icon-button" href="/outpost.goauthentik.io/sign_out" aria-label="退出 Authentik" title="退出 Authentik">
              <el-icon aria-hidden="true"><SwitchButton /></el-icon>
            </a>
          </div>
        </div>
      </header>

      <main id="console-main" ref="mainContent" class="console-main" tabindex="-1">
        <RouterView v-if="identity === 'ready'" />
        <section v-else-if="identity === 'error'" class="ops-panel console-gate" role="alert">
          <el-icon aria-hidden="true"><Lock /></el-icon>
          <h1>无法确认管理员身份</h1>
          <p>请通过启用了 Authentik 的面板地址访问。</p>
          <el-button type="primary" @click="loadIdentity">重试身份验证</el-button>
        </section>
        <div v-else class="ops-panel console-gate" role="status" aria-live="polite">
          <el-icon class="is-loading" aria-hidden="true"><Loading /></el-icon>
          <p>正在确认管理员身份…</p>
        </div>
      </main>

      <footer class="console-footer">
        <span>ycgp · 原作者 code-gopher</span>
        <a href="https://github.com/code-gopher/gostPanel" target="_blank" rel="noopener noreferrer">上游 Gost Panel</a>
        <span v-if="system.copyright && system.copyright !== upstream">{{ system.copyright }}</span>
      </footer>
    </div>

    <el-drawer v-model="mobileMenu" direction="ltr" size="min(304px, 88vw)" title="管理导航" append-to-body destroy-on-close class="console-drawer">
      <ConsoleNav :site-title="system.siteTitle" :logo-url="system.logoUrl" @navigate="mobileMenu = false" />
    </el-drawer>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Loading, Lock, Menu, SwitchButton, User } from '@element-plus/icons-vue'
import { getUserInfo } from '@/api/auth'
import ConsoleNav from '@/components/ConsoleNav.vue'
import { useSystemStore } from '@/store/system'

const route = useRoute()
const system = useSystemStore()
const upstream = 'https://github.com/code-gopher/gostPanel'
const mainContent = ref(null)
const mobileMenu = ref(false)
const username = ref('')
const identity = ref('loading')

async function loadIdentity() {
  identity.value = 'loading'
  try {
    const { data } = await getUserInfo()
    username.value = data.username
    identity.value = 'ready'
    await system.fetchSystemConfig()
  } catch {
    identity.value = 'error'
  }
}

onMounted(loadIdentity)
</script>

<style scoped>
.console-shell {
  display: grid;
  min-height: 100dvh;
  grid-template-columns: 224px minmax(0, 1fr);
}

.console-skip {
  position: fixed;
  z-index: 20;
  top: 8px;
  left: 8px;
  padding: 9px 13px;
  transform: translateY(-150%);
  border-radius: 7px;
  background: var(--ops-accent);
  color: var(--ops-on-accent);
  font-weight: 650;
}

.console-skip:focus { transform: none; }

.console-sidebar {
  position: sticky;
  top: 0;
  height: 100dvh;
  overflow-y: auto;
  border-right: 1px solid var(--ops-border);
  background: var(--ops-rail);
}

.console-workspace {
  display: flex;
  min-height: 100dvh;
  min-width: 0;
  flex-direction: column;
  --grid-line: color-mix(in srgb, var(--ops-border) 26%, transparent);
  background-image:
    linear-gradient(to right, var(--grid-line) 1px, transparent 1px),
    linear-gradient(var(--grid-line) 1px, transparent 1px),
    radial-gradient(ellipse 950px 430px at 91% -12%, var(--ops-inset), transparent 79%);
  background-size: 28px 28px, 28px 28px, auto;
}

.console-topbar {
  position: sticky;
  z-index: 10;
  top: 0;
  border-bottom: 1px solid var(--ops-border);
  background: var(--ops-rail);
}

.console-topbar__inner, .console-main, .console-footer {
  width: 100%;
  max-width: 1560px;
  margin-inline: auto;
  padding-inline: 28px;
}

.console-topbar__inner, .console-topbar__location, .console-crumb, .console-identity {
  display: flex;
  min-width: 0;
  align-items: center;
}

.console-topbar__inner { min-height: 56px; justify-content: space-between; gap: 10px; }
.console-topbar__location, .console-crumb { gap: 10px; }
.console-crumb { color: var(--ops-faint); font-size: 13px; }
.console-crumb a { color: var(--ops-muted); text-decoration: none; white-space: nowrap; }
.console-crumb a:hover { color: var(--ops-accent); }
.console-crumb [aria-current] { color: var(--ops-text); font-weight: 600; white-space: nowrap; }

.console-identity { gap: 9px; }
.console-identity__text { display: grid; min-width: 0; line-height: 1.4; }
.console-identity__text strong { overflow: hidden; max-width: 22ch; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.console-identity__text small { color: var(--ops-faint); font-size: 10px; white-space: nowrap; }

.console-identity__avatar, .console-icon-button {
  display: grid;
  width: 34px;
  height: 34px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-panel);
  color: var(--ops-muted);
  font-size: 17px;
}

.console-identity__avatar { color: var(--ops-accent); }
.console-icon-button:hover { border-color: var(--ops-accent-line); color: var(--ops-accent); }
.console-menu-button { display: none; }

.console-main { flex: 1; min-width: 0; padding-block: 28px 36px; }

.console-gate {
  display: grid;
  min-height: 215px;
  place-content: center;
  justify-items: center;
  gap: 12px;
  padding: 24px;
  color: var(--ops-muted);
  text-align: center;
}

.console-gate .el-icon { color: var(--ops-accent); font-size: 25px; }
.console-gate h1 { color: var(--ops-text); font-size: 22px; }

.console-footer {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px 16px;
  padding-block: 14px 20px;
  border-top: 1px solid var(--ops-border);
  color: var(--ops-faint);
  font-size: 12px;
  overflow-wrap: anywhere;
}

@media (max-width: 900px) {
  .console-shell { display: block; }
  .console-sidebar { display: none; }
  .console-menu-button { display: grid; }
  .console-icon-button { width: 44px; height: 44px; }
}

@media (max-width: 640px) {
  .console-topbar__inner, .console-main, .console-footer { padding-inline: 16px; }
  .console-main { padding-block: 20px 28px; }
  .console-topbar__location { gap: 8px; }
  .console-crumb a, .console-crumb [aria-hidden], .console-identity__avatar { display: none; }
  .console-identity__text strong { max-width: 12ch; }
}
</style>
