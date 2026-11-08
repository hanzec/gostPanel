<template>
  <div v-if="loading" role="status" aria-label="正在读取 Gost IP 白名单">
    <el-skeleton :rows="3" animated />
  </div>
  <div v-else-if="loadError" role="alert">
    <p class="settings-error">{{ loadError }}</p>
    <el-button @click="load">重试</el-button>
  </div>
  <el-form v-else label-position="top" :disabled="saving" @submit.prevent="save">
    <el-form-item label="允许连接的来源节点" for="gost-allowlist-nodes">
      <el-select
        id="gost-allowlist-nodes"
        v-model="nodeIds"
        class="node-select"
        aria-describedby="gost-allowlist-help"
        multiple
        filterable
        clearable
        collapse-tags
        collapse-tags-tooltip
        placeholder="选择已获取 IP 的节点"
        no-data-text="请先在节点管理中添加节点"
      >
        <el-option
          v-for="node in nodes"
          :key="node.id"
          :label="`${node.name}（${[node.ipv4, node.ipv6].filter(Boolean).join(' / ') || '未获取 IP'}）`"
          :value="node.id"
          :disabled="!node.ipv4 && !node.ipv6 && !nodeIds.includes(node.id)"
        />
      </el-select>
      <p id="gost-allowlist-help" class="ops-muted field-help">面板加入节点注册时自动获取的 IPv4 和 IPv6。清空选择并保存可解除来源 IP 限制。</p>
    </el-form-item>
    <dl class="allowlist-preview" aria-live="polite">
      <dt>已保存的来源 IP</dt>
      <dd v-if="addresses.length">
        <code v-for="address in addresses" :key="address" class="ops-mono">{{ address }}</code>
      </dd>
      <dd v-else class="ops-muted">未限制来源 IP</dd>
    </dl>
    <p v-if="saveError" ref="errorNotice" class="settings-error" role="alert" tabindex="-1">{{ saveError }}</p>
    <el-button type="primary" native-type="submit" :loading="saving">保存 IP 白名单</el-button>
  </el-form>
</template>

<script setup>
import { nextTick, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getGostAllowlist, getRoutingNodes, updateGostAllowlist } from '@/api/system'

const nodes = ref([])
const nodeIds = ref([])
const addresses = ref([])
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const saveError = ref('')
const errorNotice = ref(null)

function showConfig(data) {
  nodeIds.value = data.node_ids || []
  addresses.value = data.addresses || []
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [{ data }, nodeList] = await Promise.all([getGostAllowlist(), getRoutingNodes()])
    nodes.value = nodeList
    showConfig(data)
  } catch (error) {
    loadError.value = error.response?.data?.message || '无法读取白名单或节点列表，请重试。'
  } finally {
    loading.value = false
  }
}

async function save() {
  if (saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    const { data } = await updateGostAllowlist(nodeIds.value)
    showConfig(data)
    ElMessage.success('IP 白名单已保存，面板将同步到 Gost Relay')
  } catch (error) {
    saveError.value = error.response?.data?.message || '无法保存 IP 白名单，请重试。'
    await nextTick()
    errorNotice.value?.focus()
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.node-select { width: 100%; }
.field-help { margin: 8px 0 0; line-height: 1.5; }
.allowlist-preview { margin: 0 0 16px; padding: 12px; background: var(--ops-inset); border-radius: 8px; }
.allowlist-preview dd { display: flex; flex-wrap: wrap; gap: 4px 12px; margin: 4px 0 0; overflow-wrap: anywhere; }
.settings-error { color: var(--ops-danger); overflow-wrap: anywhere; white-space: pre-wrap; }
</style>
