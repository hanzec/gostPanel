<template>
  <div class="ops-page settings-page">
    <PageHeader eyebrow="CONFIGURATION" title="系统设置" description="管理面板品牌、通知渠道、数据库备份和 Gost 配置。">
      <template #actions>
        <el-button v-if="!activeTab.startsWith('gost-')" type="primary" :loading="saving" :disabled="!ready || backupRunning" @click="saveSettings">
          保存设置
        </el-button>
      </template>
    </PageHeader>

    <div v-if="loading" class="ops-panel settings-state" role="status" aria-label="正在加载系统设置">
      <el-skeleton :rows="5" animated />
    </div>
    <div v-else-if="loadError" class="ops-panel settings-state settings-feedback" role="alert">
      <div><strong>无法加载系统设置</strong><p>请重新获取当前配置后再编辑。</p></div>
      <el-button @click="loadSettings">重试</el-button>
    </div>

    <template v-else-if="ready">
      <div v-if="saveError" class="ops-panel settings-state settings-feedback" role="alert">
        <div><strong>设置保存失败</strong><p>输入内容仍在表单中，可以再次保存。</p></div>
      </div>

      <div class="settings-layout">
        <section class="ops-panel settings-workspace" aria-label="配置表单">
          <el-tabs v-model="activeTab" class="settings-tabs">
            <el-tab-pane name="config" label="面板品牌">
              <header class="settings-section-heading">
                <h2>品牌与显示</h2>
                <p>设置管理员看到的面板标识。</p>
              </header>
              <el-form :model="configForm" label-position="top" :disabled="saving" @submit.prevent="saveSettings">
                <div class="settings-fields">
                  <el-form-item label="站点标题" prop="siteTitle">
                    <el-input v-model="configForm.siteTitle" placeholder="yagp" />
                  </el-form-item>
                  <el-form-item label="Logo 地址" prop="logoUrl">
                    <el-input v-model.trim="configForm.logoUrl" inputmode="url" placeholder="https://example.com/logo.svg" />
                  </el-form-item>
                  <el-form-item class="settings-full-field" label="版权信息" prop="copyright">
                    <el-input v-model="configForm.copyright" placeholder="输入版权信息" />
                  </el-form-item>
                </div>
              </el-form>
            </el-tab-pane>

            <el-tab-pane name="email" label="通知管理">
              <header class="settings-section-heading">
                <h2>SMTP 邮件</h2>
                <p>填写邮件服务器和发件人信息，发送测试邮件验证连接。</p>
              </header>
              <el-form :model="emailForm" label-position="top" :disabled="saving" @submit.prevent="saveSettings">
                <div class="settings-fields">
                  <el-form-item label="SMTP 服务器" prop="host">
                    <el-input v-model.trim="emailForm.host" placeholder="smtp.example.com" />
                  </el-form-item>
                  <el-form-item label="SMTP 端口" prop="port">
                    <el-input-number v-model="emailForm.port" :min="1" :max="65535" :step="1" controls-position="right" placeholder="465 或 587" />
                  </el-form-item>
                  <el-form-item label="用户名" prop="username">
                    <el-input v-model="emailForm.username" autocomplete="off" />
                  </el-form-item>
                  <el-form-item label="密码" prop="password">
                    <el-input v-model="emailForm.password" type="password" show-password autocomplete="new-password" />
                  </el-form-item>
                  <el-form-item class="settings-full-field" label="发件人邮箱" prop="fromEmail">
                    <el-input v-model.trim="emailForm.fromEmail" type="email" placeholder="noreply@example.com" />
                  </el-form-item>
                </div>
              </el-form>
              <div class="settings-section-actions">
                <el-button :icon="Message" :disabled="saving" @click="openTestEmail">发送测试邮件</el-button>
              </div>

              <section class="settings-notification-channel" aria-labelledby="settings-bark-heading">
                <header class="settings-section-heading">
                  <h2 id="settings-bark-heading">Bark 推送</h2>
                  <p>使用 Bark App 中的服务器地址和设备密钥，支持自建服务器。</p>
                </header>
                <el-form :model="barkForm" label-position="top" :disabled="saving || testBarkSending" @submit.prevent="saveSettings">
                  <div class="settings-fields settings-bark-fields">
                    <el-form-item label="Bark 服务器地址" prop="serverUrl" :error="barkErrors.serverUrl">
                      <el-input
                        v-model.trim="barkForm.serverUrl"
                        inputmode="url"
                        placeholder="https://api.day.app"
                        :aria-invalid="Boolean(barkErrors.serverUrl)"
                        @input="clearBarkError('serverUrl')"
                        @blur="checkBarkServerUrl"
                      />
                    </el-form-item>
                    <el-form-item label="Bark 设备密钥" prop="deviceKey" :error="barkErrors.deviceKey">
                      <el-input
                        v-model.trim="barkForm.deviceKey"
                        :type="barkDeviceKeyVisible ? 'text' : 'password'"
                        autocomplete="new-password"
                        :aria-invalid="Boolean(barkErrors.deviceKey)"
                        @input="clearBarkError('deviceKey')"
                      >
                        <template #append>
                          <el-button
                            :aria-label="barkDeviceKeyVisible ? '隐藏 Bark 设备密钥' : '显示 Bark 设备密钥'"
                            @click="barkDeviceKeyVisible = !barkDeviceKeyVisible"
                          >{{ barkDeviceKeyVisible ? '隐藏' : '显示' }}</el-button>
                        </template>
                      </el-input>
                    </el-form-item>
                  </div>
                </el-form>
                <div class="settings-section-actions">
                  <el-button :icon="Bell" :disabled="saving" :loading="testBarkSending" @click="testBark">发送测试推送</el-button>
                  <p v-if="testBarkError" class="settings-inline-error" role="alert">{{ testBarkError }}</p>
                </div>
              </section>
            </el-tab-pane>

            <el-tab-pane name="backup" label="数据库备份">
              <header class="settings-section-heading">
                <h2>数据库备份</h2>
                <p>配置备份策略，或手动创建一份当前数据库的备份。</p>
              </header>
              <div v-if="!backupSupported" class="settings-notice">
                <el-icon aria-hidden="true"><InfoFilled /></el-icon>
                <div>
                  <strong>由数据库服务管理备份</strong>
                  <p>面板内的自动备份和手动备份适用于 SQLite。</p>
                </div>
              </div>
              <template v-else>
                <el-form :model="backupForm" label-position="top" :disabled="saving || backupRunning" @submit.prevent="saveSettings">
                  <div class="settings-fields backup-fields">
                    <el-form-item label="自动备份" prop="autoBackup">
                      <el-switch v-model="backupForm.autoBackup" aria-label="自动备份" />
                      <span class="settings-field-hint">启用后每天生成一份备份。</span>
                    </el-form-item>
                    <el-form-item label="保留份数" prop="retentionCount">
                      <el-input-number v-model="backupForm.retentionCount" :min="1" :max="100" :step="1" controls-position="right" />
                      <span class="settings-field-hint">保留最近生成的备份文件。</span>
                    </el-form-item>
                  </div>
                </el-form>
                <div class="settings-section-actions">
                  <el-button :icon="Coin" :loading="backupRunning" :disabled="saving" @click="createBackup">立即备份</el-button>
                </div>
              </template>
            </el-tab-pane>

            <el-tab-pane name="gost-allowlist" label="Gost IP 白名单" lazy>
              <header class="settings-section-heading">
                <h2>Gost Relay 来源 IP 白名单</h2>
                <p>统一设置面板管理的 Gost Relay 允许哪些节点连接。</p>
              </header>
              <GostAllowlistSettings />
            </el-tab-pane>

            <el-tab-pane name="gost-ruleset" label="Clash 规则集" lazy>
              <header class="settings-section-heading">
                <h2>透明代理规则集订阅</h2>
                <p>面板定期读取 Clash 规则集，转换为 Gost 分流白名单并同步到所选节点。</p>
              </header>
              <GostRulesetSettings />
            </el-tab-pane>
          </el-tabs>
        </section>

        <aside class="ops-panel settings-context" aria-label="当前配置说明">
          <div class="settings-context-icon" aria-hidden="true"><el-icon><component :is="context.icon" /></el-icon></div>
          <p class="settings-context-label ops-mono">{{ context.label }}</p>
          <h2>{{ context.title }}</h2>
          <p>{{ context.description }}</p>
          <dl>
            <div><dt>{{ context.factLabel }}</dt><dd>{{ context.factValue }}</dd></div>
          </dl>
        </aside>
      </div>
    </template>

    <PanelDialog
      v-model="testEmailVisible"
      title="发送测试邮件"
      description="使用当前邮件表单的连接参数发送测试邮件。"
      size="compact"
      destroy-on-close
    >
      <el-form
        id="settings-test-email"
        ref="testEmailFormRef"
        :model="testEmailForm"
        :rules="testEmailRules"
        label-position="top"
        :disabled="testEmailSending"
        @submit.prevent="confirmTestEmail"
      >
        <el-form-item label="收件人邮箱" prop="toEmail">
          <el-input v-model.trim="testEmailForm.toEmail" type="email" placeholder="admin@example.com" />
        </el-form-item>
        <p v-if="testEmailError" class="settings-inline-error" role="alert">发送失败，请核对 SMTP 连接和发件人配置。</p>
      </el-form>
      <template #footer>
        <el-button @click="testEmailVisible = false">取消</el-button>
        <el-button type="primary" native-type="submit" form="settings-test-email" :loading="testEmailSending">发送</el-button>
      </template>
    </PanelDialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { Bell, Coin, Connection, InfoFilled, Lock, Message, Monitor } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PanelDialog from '@/components/PanelDialog.vue'
import { confirmPanelAction } from '@/utils/panel-dialog'
import { backupSystem, getSystemConfig, sendTestBark, sendTestEmail, updateSystemConfig } from '@/api/system'
import GostAllowlistSettings from '@/components/GostAllowlistSettings.vue'
import GostRulesetSettings from '@/components/GostRulesetSettings.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useSystemStore } from '@/store/system'

const systemStore = useSystemStore()
const activeTab = ref('config')
const loading = ref(true)
const saving = ref(false)
const loadError = ref(false)
const saveError = ref(false)
const backupSupported = ref(false)
const backupRunning = ref(false)
const testEmailVisible = ref(false)
const testEmailSending = ref(false)
const testEmailError = ref(false)
const testEmailFormRef = ref()
const barkDeviceKeyVisible = ref(false)
const testBarkSending = ref(false)
const testBarkError = ref('')
const ready = computed(() => !loading.value && !loadError.value)

const configForm = reactive({ siteTitle: '', logoUrl: '', copyright: '' })
const emailForm = reactive({ host: '', port: undefined, username: '', password: '', fromEmail: '' })
const barkForm = reactive({ serverUrl: '', deviceKey: '' })
const barkErrors = reactive({ serverUrl: '', deviceKey: '' })
const backupForm = reactive({ autoBackup: false, retentionCount: 7 })
const testEmailForm = reactive({ toEmail: '' })
const testEmailRules = {
  toEmail: [
    { required: true, message: '请输入收件人邮箱', trigger: 'blur' },
    { type: 'email', message: '请输入有效的邮箱地址', trigger: 'blur' }
  ]
}
let savedLogSettings

const context = computed(() => {
  if (activeTab.value === 'email') {
    return {
      icon: Bell, label: 'NOTIFICATIONS', title: '连接验证',
      description: '测试邮件和推送使用当前输入的连接参数。选择「保存设置」以应用修改。',
      factLabel: '通知渠道', factValue: 'SMTP 邮件、Bark 推送'
    }
  }
  if (activeTab.value === 'backup') {
    return {
      icon: Coin, label: 'BACKUP', title: '备份方式',
      description: backupSupported.value ? 'SQLite 备份文件存放在面板服务的 backups 目录。' : '请使用数据库服务提供的工具创建和管理备份。',
      factLabel: '管理方式', factValue: backupSupported.value ? '面板内备份' : '数据库服务'
    }
  }
  if (activeTab.value === 'gost-allowlist') {
    return {
      icon: Lock, label: 'ADMISSION', title: '白名单同步',
      description: '面板向在线 Relay 同步已保存的来源 IP。离线节点重新连接后会收到更新。首次启用时，已有 Relay 可能短暂重启。',
      factLabel: '作用范围', factValue: '面板管理的 Gost Relay'
    }
  }
  if (activeTab.value === 'gost-ruleset') {
    return {
      icon: Connection, label: 'BYPASS', title: '在 Gost 中使用规则集',
      description: '在现有透明代理转发链的第一跳添加下方配置。匹配的目标经代理转发，其余目标直连。',
      factLabel: 'Gost 第一跳配置', factValue: 'bypass: yagp-transparent-proxy'
    }
  }
  return {
    icon: Monitor, label: 'IDENTITY', title: '显示预览',
    description: '站点标题和 Logo 保存后会应用到控制台导航。',
    factLabel: '站点标题', factValue: configForm.siteTitle || systemStore.siteTitle
  }
})

async function loadSettings() {
  loading.value = true
  loadError.value = false
  try {
    const { data } = await getSystemConfig()
    Object.assign(configForm, data.config)
    Object.assign(emailForm, data.email, { port: data.email.port || undefined })
    Object.assign(barkForm, data.bark)
    Object.assign(backupForm, { autoBackup: data.backup.autoBackup, retentionCount: data.backup.retentionCount })
    savedLogSettings = data.log
    backupSupported.value = data.backup.supported
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!ready.value || saving.value || backupRunning.value) return
  if (backupSupported.value && backupForm.retentionCount == null) {
    activeTab.value = 'backup'
    ElMessage.warning('请填写备份保留份数')
    return
  }
  if (!validateBarkConfig()) {
    activeTab.value = 'email'
    return
  }

  saving.value = true
  saveError.value = false
  try {
    await updateSystemConfig({
      config: { ...configForm },
      email: { ...emailForm, port: emailForm.port ?? 0 },
      bark: { ...barkForm },
      log: savedLogSettings,
      backup: { ...backupForm }
    })
    await systemStore.fetchSystemConfig()
    ElMessage.success('设置已保存')
  } catch {
    saveError.value = true
  } finally {
    saving.value = false
  }
}

function openTestEmail() {
  if (!emailForm.host || !emailForm.port || !emailForm.fromEmail) {
    ElMessage.warning('请先填写 SMTP 服务器、端口和发件人邮箱')
    return
  }
  testEmailForm.toEmail = emailForm.fromEmail
  testEmailError.value = false
  testEmailVisible.value = true
}

async function confirmTestEmail() {
  if (testEmailSending.value || !await testEmailFormRef.value.validate().catch(() => false)) return
  testEmailSending.value = true
  testEmailError.value = false
  try {
    await sendTestEmail({ ...emailForm, toEmail: testEmailForm.toEmail })
    testEmailVisible.value = false
    ElMessage.success('测试邮件已发送')
  } catch {
    testEmailError.value = true
  } finally {
    testEmailSending.value = false
  }
}

function clearBarkError(field) {
  barkErrors[field] = ''
  testBarkError.value = ''
}

function checkBarkServerUrl() {
  const { serverUrl } = barkForm
  if (!serverUrl) return
  if (new TextEncoder().encode(serverUrl).length > 2048) {
    barkErrors.serverUrl = 'Bark 服务器地址过长（最多 2048 字节）'
    return
  }
  barkErrors.serverUrl = isBarkServerUrl(serverUrl) ? '' : '请输入不含账号信息、查询参数和片段的 HTTP(S) 地址'
}

function isBarkServerUrl(value) {
  if (!/^https?:\/\/[^/@]+(?:\/|$)/i.test(value) || /[\s\\?#]/.test(value)) return false
  try {
    return Boolean(new URL(value).hostname)
  } catch {
    return false
  }
}

function validateBarkConfig(required = false) {
  const configured = required || barkForm.serverUrl || barkForm.deviceKey
  barkErrors.serverUrl = configured && !barkForm.serverUrl ? '请输入 Bark 服务器地址' : ''
  barkErrors.deviceKey = configured && !barkForm.deviceKey ? '请输入 Bark 设备密钥' : ''
  checkBarkServerUrl()
  if (new TextEncoder().encode(barkForm.deviceKey).length > 255) {
    barkErrors.deviceKey = 'Bark 设备密钥过长（最多 255 字节）'
  }
  return !barkErrors.serverUrl && !barkErrors.deviceKey
}

async function testBark() {
  if (saving.value || testBarkSending.value || !validateBarkConfig(true)) return
  testBarkSending.value = true
  testBarkError.value = ''
  try {
    await sendTestBark({ ...barkForm })
    ElMessage.success('Bark 测试推送已发送')
  } catch (error) {
    testBarkError.value = error.response?.data?.message || '发送失败，请核对 Bark 服务器地址和设备密钥。'
  } finally {
    testBarkSending.value = false
  }
}

async function createBackup() {
  try {
    await confirmPanelAction('将创建一份当前数据库的备份。', '立即备份', {
      confirmButtonText: '创建备份',
      cancelButtonText: '取消',
      type: 'warning'
    })
  } catch {
    return
  }

  backupRunning.value = true
  try {
    await backupSystem()
    ElMessage.success('数据库备份已创建')
  } catch {
    // The request layer displays the server error.
  } finally {
    backupRunning.value = false
  }
}

onMounted(loadSettings)
</script>

<style scoped>
.settings-layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(220px, 280px);
  align-items: start;
  gap: 16px;
  min-width: 0;
}

.settings-workspace { min-width: 0; padding: 4px 24px 24px; }
.settings-tabs :deep(.el-tabs__header) { margin-bottom: 24px; }
.settings-tabs :deep(.el-tabs__item) { min-height: 48px; font-weight: 600; }

.settings-section-heading { margin-bottom: 24px; }
.settings-section-heading h2 { margin: 0 0 8px; font-size: 20px; font-weight: 650; }
.settings-section-heading p { margin: 0; color: var(--ops-muted); font-size: 14px; }

.settings-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 4px 20px; }
.settings-full-field { grid-column: 1 / -1; }
.settings-fields :deep(.el-input-number) { width: 100%; }
.settings-bark-fields :deep(.el-form-item__error) { position: static; }
.backup-fields :deep(.el-form-item__content) { align-items: flex-start; flex-direction: column; }
.settings-field-hint { color: var(--ops-muted); line-height: 1.5; }
.settings-section-actions { margin-top: 4px; padding-top: 16px; border-top: 1px solid var(--ops-border); }
.settings-section-actions .settings-inline-error { margin-top: 12px; }
.settings-notification-channel { margin-top: 24px; padding-top: 24px; border-top: 1px solid var(--ops-border); }

.settings-context { padding: 20px; }

.settings-context-icon {
  display: grid;
  width: 40px;
  height: 40px;
  place-items: center;
  border: 1px solid var(--ops-border);
  border-radius: 10px;
  background: var(--ops-inset);
  color: var(--ops-accent);
  font-size: 18px;
}

.settings-context .settings-context-label {
  margin: 16px 0 4px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: .12em;
}

.settings-context h2 { margin: 0; font-size: 18px; font-weight: 650; }
.settings-context p { margin: 8px 0 0; color: var(--ops-muted); font-size: 14px; }
.settings-context dl { margin: 20px 0 0; padding-top: 16px; border-top: 1px solid var(--ops-border); }
.settings-context dt { color: var(--ops-muted); font-size: 12px; }
.settings-context dd { margin: 4px 0 0; font-weight: 600; overflow-wrap: anywhere; }

.settings-state { padding: 20px 24px; }

.settings-feedback {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--ops-warning);
}

.settings-feedback p { margin: 4px 0 0; color: var(--ops-muted); }

.settings-notice {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
}

.settings-notice > .el-icon { margin-top: 3px; color: var(--ops-accent); }
.settings-notice p { margin: 4px 0 0; color: var(--ops-muted); }
.settings-inline-error { margin: 0; color: var(--ops-danger); }

@media (max-width: 960px) {
  .settings-layout { grid-template-columns: minmax(0, 1fr); }
}

@media (max-width: 600px) {
  .settings-fields { grid-template-columns: minmax(0, 1fr); }
  .settings-fields :deep(.el-input__wrapper) { min-height: 44px; }
  .settings-notification-channel .el-button { min-height: 44px; }
  .settings-workspace { padding-inline: 16px; }
  .settings-tabs :deep(.el-tabs__item) { padding-inline: 8px; }
  .settings-context { padding: 16px; }
}
</style>
