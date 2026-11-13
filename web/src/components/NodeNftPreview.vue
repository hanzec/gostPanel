<template>
  <section class="nft-preview" aria-labelledby="nft-preview-title">
    <header class="nft-preview-header">
      <div>
        <h3 class="ops-subsection-title" id="nft-preview-title">nftables 规则</h3>
        <p class="ops-muted">流量配额规则随节点设置自动生成。</p>
      </div>
      <el-button v-if="!loadError" :icon="Refresh" :loading="loading || firewallLoading" @click="refresh">刷新</el-button>
    </header>
    <section class="nft-firewall" aria-labelledby="nft-firewall-title" :aria-busy="firewallLoading">
      <div class="nft-firewall-summary" role="status">
        <h4 class="ops-subsection-title" id="nft-firewall-title">防火墙同步</h4>
        <span class="ops-status" :class="firewallState.tone">{{ firewallState.label }}</span>
      </div>
      <p v-if="firewall?.managed" class="ops-muted">管理员在主机 inet input 链中接入 YAGP 放行链，状态仅反映节点规则同步。</p>
      <p v-if="firewall?.last_sync" class="ops-muted">最近成功同步 <time :datetime="firewall.last_sync">{{ formatTime(firewall.last_sync) }}</time></p>
      <p v-if="firewallError || firewall?.last_error" class="nft-firewall-error">{{ firewallError || firewall.last_error }}</p>
    </section>
    <p v-if="loading && !config" class="ops-muted" role="status">正在读取 nftables 规则…</p>
    <div v-else-if="loadError" class="nft-preview-error">
      <p role="alert">{{ loadError }}</p>
      <el-button :loading="firewallLoading" @click="refresh">重新加载</el-button>
    </div>
    <template v-else>
      <p v-if="!live" class="ops-muted">旧版守护程序仅提供上次保存的规则。升级后可预览节点当前的规则，包括流量配额。</p>
      <pre v-if="config" class="ops-inset nft-preview-code ops-mono" tabindex="0" aria-label="nftables 配置 JSON"><code>{{ config }}</code></pre>
      <p v-else class="ops-inset nft-preview-empty ops-muted">{{ live ? '此节点暂无 yagp 管理的 nftables 规则。' : '此节点暂无已保存的规则。' }}</p>
    </template>
  </section>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { getNodeFirewallStatus, getNodeNftConfig } from '@/api/node'
import { formatTime } from '@/utils/node-display'

const props = defineProps({ node: { type: Object, required: true } })
const rules = ref('')
const live = ref(false)
const loading = ref(true)
const loadError = ref('')
const firewall = ref(null)
const firewallLoading = ref(true)
const firewallError = ref('')
const legacyFirewall = ref(false)

const firewallState = computed(() => {
  if (firewallLoading.value) return { label: '正在读取', tone: 'is-muted' }
  if (legacyFirewall.value) return { label: '守护程序版本较旧', tone: 'is-muted' }
  if (firewallError.value) return { label: '读取失败', tone: 'is-danger' }
  if (!firewall.value?.supported) return { label: '环境不支持', tone: 'is-muted' }
  if (!firewall.value.managed) return { label: '未启用', tone: 'is-muted' }
  if (firewall.value.synchronized) return { label: '已同步', tone: 'is-success' }
  if (firewall.value.last_error) return { label: '同步异常', tone: 'is-danger' }
  return { label: '待同步', tone: 'is-warning' }
})

const config = computed(() => {
  if (!rules.value) return ''
  try {
    const parsed = JSON.parse(rules.value)
    return parsed.nftables?.length ? rules.value : ''
  } catch {
    return rules.value
  }
})

const loadConfig = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const res = await getNodeNftConfig(props.node.id)
    rules.value = res.data.config || ''
    live.value = Boolean(res.data.live)
  } catch (error) {
    loadError.value = error.response?.data?.message || '无法读取 nftables 规则，请重试。'
  } finally {
    loading.value = false
  }
}

const loadFirewall = async () => {
  firewallLoading.value = true
  firewall.value = null
  firewallError.value = ''
  legacyFirewall.value = false
  try {
    const res = await getNodeFirewallStatus(props.node.id)
    firewall.value = res.data
  } catch (error) {
    legacyFirewall.value = error.response?.status === 501
    if (!legacyFirewall.value) {
      firewallError.value = error.response?.data?.message || '无法读取防火墙状态，请重试。'
    }
  } finally {
    firewallLoading.value = false
  }
}

const refresh = () => {
  loadConfig()
  loadFirewall()
}

onMounted(refresh)
</script>

<style scoped>
.nft-preview { display: grid; gap: var(--ops-section-gap); }
.nft-preview h3, .nft-preview p, .nft-preview-code { margin: 0; }
.nft-preview-header, .nft-preview-error { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 12px; }
.nft-preview-header p { margin-top: 4px; font-size: var(--ops-text-sm); }
.nft-firewall { display: grid; gap: 6px; min-width: 0; }
.nft-firewall-summary { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.nft-firewall-summary h4 { margin: 0; }
.nft-firewall p { font-size: var(--ops-text-sm); }
.nft-firewall-error, .nft-preview-error p { color: var(--ops-danger); overflow-wrap: anywhere; }
.nft-firewall-error { white-space: pre-wrap; }
.nft-preview-code, .nft-preview-empty {
  box-sizing: border-box;
  max-width: 100%;
  min-width: 0;
}
.nft-preview-code { max-height: min(45vh, 420px); overflow: auto; font-size: var(--ops-text-sm); line-height: var(--ops-leading-body); }
</style>
