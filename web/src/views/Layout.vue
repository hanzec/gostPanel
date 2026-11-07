<template>
  <div class="layout-container">
    <!-- 头部 (Top Header) -->
    <el-header class="header">
      <div class="header-left">
        <el-icon class="menu-toggle" @click="toggleMobileMenu"><Expand /></el-icon>
        <div class="logo">
          <img v-if="logoUrl" :src="logoUrl" alt="logo" class="logo-img" />
          <span>{{ siteTitle }}</span>
        </div>
      </div>
      <div class="header-right">
        <div v-if="username" class="user-group">
          <div class="username-box">
            {{ username }}
          </div>
          <a class="user-icon-box" href="/outpost.goauthentik.io/sign_out" title="退出 Authentik" aria-label="退出 Authentik">
            <el-icon><SwitchButton /></el-icon>
          </a>
        </div>
      </div>
    </el-header>

    <!-- 下方主体 (Body: Sidebar + Content) -->
    <el-container class="body-container">
      <!-- 侧边栏 (Floating Sidebar) -->
      <div v-if="mobileMenuVisible" class="mobile-overlay" @click="mobileMenuVisible = false"></div>
      <el-aside :width="isCollapse ? '64px' : '220px'" class="aside" :class="{ 'mobile-show': mobileMenuVisible }">
        <el-menu
          :default-active="currentRoute"
          :collapse="isCollapse"
          :collapse-transition="false"
          router
          class="sidebar-menu"
          text-color="#606266"
          active-text-color="#409eff"
        >
          <template v-for="item in menuItems" :key="item.path">
            <!-- 有子菜单 -->
            <el-sub-menu v-if="item.children" :index="item.path">
              <template #title>
                <el-icon><component :is="item.icon" /></el-icon>
                <span>{{ item.title }}</span>
              </template>
              <el-menu-item 
                v-for="child in item.children"
                :key="child.path"
                :index="child.path"
              >
                <el-icon><component :is="child.icon" /></el-icon>
                <template #title>{{ child.title }}</template>
              </el-menu-item>
            </el-sub-menu>

            <!-- 无子菜单 -->
            <el-menu-item v-else :index="item.path">
              <el-icon><component :is="item.icon" /></el-icon>
              <template #title>{{ item.title }}</template>
            </el-menu-item>
          </template>
        </el-menu>
      </el-aside>

      <!-- 主内容 (Content) -->
      <div class="main-wrapper">
        <el-main class="main">
          <router-view v-if="username" v-slot="{ Component }">
            <transition name="fade" mode="out-in">
              <component :is="Component" />
            </transition>
          </router-view>
          <el-result
            v-else-if="identityError"
            icon="error"
            title="无法确认管理员身份"
            sub-title="请通过启用了 Authentik 的面板地址访问。"
          />
        </el-main>
        
        <!-- 底部版权 (Footer) -->
        <div class="footer">
          <span>ycgp · 原作者 code-gopher · </span>
          <a href="https://github.com/code-gopher/gostPanel" target="_blank" rel="noopener noreferrer">
            上游 Gost Panel
          </a>
        </div>
      </div>
    </el-container>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { SwitchButton, Expand, Odometer, Monitor, Switch, Connection, Document, Setting } from '@element-plus/icons-vue'
import { useSystemStore } from '@/store/system'
import { getUserInfo } from '@/api/auth'

const route = useRoute()
const systemStore = useSystemStore()

const siteTitle = computed(() => systemStore.siteTitle)
const logoUrl = computed(() => systemStore.logoUrl)
const username = ref('')
const identityError = ref(false)

onMounted(async () => {
  try {
    const { data } = await getUserInfo()
    username.value = data.username
    await systemStore.fetchSystemConfig()
  } catch {
    identityError.value = true
  }
})

// 菜单配置
const menuItems = [
  { path: '/dashboard', title: '仪表盘', icon: Odometer },
  { path: '/nodes', title: '节点管理', icon: Monitor },
  { path: '/rules', title: '规则管理', icon: Switch },
  { path: '/tunnels', title: '隧道管理', icon: Connection },
  { path: '/logs', title: '操作日志', icon: Document },
  { path: '/system', title: '系统管理', icon: Setting }
]

// 侧边栏折叠
const isCollapse = ref(false)
const toggleCollapse = () => {
  isCollapse.value = !isCollapse.value
}

// 移动端菜单
const mobileMenuVisible = ref(false)
const toggleMobileMenu = () => {
  mobileMenuVisible.value = !mobileMenuVisible.value
}

// 当前路由
const currentRoute = computed(() => route.path)
const currentTitle = computed(() => {
  const item = menuItems.find(m => m.path === route.path)
  return item?.title || ''
})
</script>

<style scoped>
.layout-container {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.header {
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  box-shadow: 0 1px 4px rgba(0, 21, 41, 0.08);
  height: 64px;
  position: relative;
  z-index: 10;
}

.body-container {
  flex: 1;
  display: flex;
  background-color: #f0f2f5;
  overflow: hidden;
}

.aside {
  background-color: #fff !important;
  margin: 16px 0 16px 16px;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
  overflow: hidden;
  border-right: none;
  transition: width 0.3s;
}

.sidebar-menu {
  border-right: none;
  height: 100%;
}

.main-wrapper {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.main {
  flex: 1;
  background: transparent;
  padding: 0;
  margin: 16px;
  margin-bottom: 0;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
}

.logo {
  font-size: 20px;
  font-weight: 700;
  color: #333;
  margin-right: 32px;
  display: flex;
  align-items: center;
}

.logo-img {
  width: 32px;
  height: 32px;
  margin-right: 10px;
  border-radius: 4px;
}

.header-left {
  display: flex;
  align-items: center;
}

.header-right {
  display: flex;
  align-items: center;
}

.user-group {
  display: flex;
  align-items: center;
}

.username-box {
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 16px;
  min-width: 100px;
  border: 1px solid #d9d9d9;
  border-right: none;
  border-top-left-radius: 4px;
  border-bottom-left-radius: 4px;
  background: #fff;
  font-size: 13px;
  font-weight: 500;
  cursor: default;
  color: #606266;
}

.user-icon-box {
  height: 30px;
  width: 30px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid #d9d9d9;
  border-top-right-radius: 4px;
  border-bottom-right-radius: 4px;
  background: #fff;
  color: #606266;
  text-decoration: none;
  cursor: pointer;
  transition: all 0.3s;
  font-size: 14px;
  margin-left: -1px;
}

.user-icon-box:hover {
  border-color: #409eff;
  color: #409eff;
  position: relative;
  z-index: 1;
}

.footer {
  text-align: center;
  padding: 16px;
  color: #909399;
  font-size: 13px;
  background-color: #f0f2f5;
}

.footer a {
  color: #909399;
  text-decoration: none;
  transition: color 0.3s;
}

.footer a:hover {
  color: #409eff;
}

/* Transitions */
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

/* ========== 移动端适配 ========== */
@media screen and (max-width: 768px) {
  .header {
    padding: 0 12px;
    height: 56px;
  }
  
  .logo span {
    display: none;
  }
  
  .logo-img {
    width: 28px;
    height: 28px;
  }
  
  .username-box {
    display: none;
  }
  
  .user-icon-box {
    border-radius: 4px;
    margin-left: 0;
  }
  
  .aside {
    position: fixed;
    left: 0;
    top: 56px;
    bottom: 0;
    z-index: 1000;
    margin: 0;
    border-radius: 0;
    transform: translateX(-100%);
    transition: transform 0.3s ease;
  }
  
  .aside.mobile-show {
    transform: translateX(0);
  }
  
  .main {
    margin: 8px;
  }
  
  .footer {
    padding: 12px;
    font-size: 12px;
  }
  
  .footer span {
    display: block;
  }
}

/* 汉堡菜单默认隐藏 */
.menu-toggle {
  display: none;
}

/* 遮罩层默认隐藏 */
.mobile-overlay {
  display: none;
}

/* 移动端显示汉堡菜单和遮罩层 */
@media screen and (max-width: 768px) {
  .menu-toggle {
    display: flex !important;
    font-size: 28px;
    color: #303133;
    cursor: pointer;
    padding: 8px;
    margin-right: 4px;
  }
  
  .mobile-overlay {
    display: block;
    position: fixed;
    top: 56px;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 999;
  }
}
</style>
