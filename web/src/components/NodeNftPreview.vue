<template>
  <section class="nft-preview" aria-labelledby="nft-preview-title" :aria-busy="loading">
    <header class="nft-preview-header">
      <div>
        <h3 id="nft-preview-title">nftables 规则</h3>
        <p class="ops-muted">流量配额规则随节点设置自动生成。</p>
      </div>
      <el-button v-if="!loadError" :icon="Refresh" :loading="loading" @click="loadConfig">刷新</el-button>
    </header>
    <p v-if="loading && !config" class="ops-muted" role="status">正在读取 nftables 规则…</p>
    <div v-else-if="loadError" class="nft-preview-error">
      <p role="alert">{{ loadError }}</p>
      <el-button @click="loadConfig">重新加载</el-button>
    </div>
    <template v-else>
      <p v-if="!live" class="ops-muted">旧版守护程序仅提供上次保存的规则。升级后可预览节点当前的规则，包括流量配额。</p>
      <pre v-if="config" class="nft-preview-code ops-mono" tabindex="0" aria-label="nftables 配置 JSON"><code>{{ config }}</code></pre>
      <p v-else class="nft-preview-empty ops-muted">{{ live ? '此节点暂无 yagp 管理的 nftables 规则。' : '此节点暂无已保存的规则。' }}</p>
    </template>
  </section>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { getNodeNftConfig } from '@/api/node'

const props = defineProps({ node: { type: Object, required: true } })
const rules = ref('')
const live = ref(false)
const loading = ref(true)
const loadError = ref('')

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

onMounted(loadConfig)
</script>

<style scoped>
.nft-preview { display: grid; gap: 14px; }
.nft-preview h3, .nft-preview p, .nft-preview-code { margin: 0; }
.nft-preview-header, .nft-preview-error { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 12px; }
.nft-preview-header p { margin-top: 4px; }
.nft-preview-error p { color: var(--ops-danger); overflow-wrap: anywhere; }
.nft-preview-code, .nft-preview-empty {
  box-sizing: border-box;
  max-width: 100%;
  min-width: 0;
  padding: 14px;
  border: 1px solid var(--ops-border);
  border-radius: 8px;
  background: var(--ops-inset);
}
.nft-preview-code { max-height: min(45vh, 420px); overflow: auto; line-height: 1.55; }
</style>
