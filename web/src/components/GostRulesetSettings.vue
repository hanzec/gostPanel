<template>
  <div v-if="loading" role="status" aria-label="正在读取 Clash 规则集订阅">
    <el-skeleton :rows="3" animated />
  </div>
  <div v-else-if="loadError" role="alert">
    <p class="settings-error">{{ loadError }}</p>
    <el-button @click="load">重试</el-button>
  </div>
  <el-form v-else label-position="top" :disabled="saving" @submit.prevent="save">
    <el-form-item label="Clash 规则集链接" for="gost-ruleset-url">
      <el-input
        id="gost-ruleset-url"
        v-model.trim="url"
        type="url"
        inputmode="url"
        autocomplete="off"
        spellcheck="false"
        aria-describedby="gost-ruleset-url-help"
        placeholder="https://example.com/rules.yaml"
      />
      <p id="gost-ruleset-url-help" class="ops-muted field-help">填写 Clash YAML 或纯文本规则集的 HTTPS 链接。清空链接和节点后保存可停用订阅。</p>
    </el-form-item>
    <el-form-item label="同步到节点" for="gost-ruleset-nodes">
      <el-select
        id="gost-ruleset-nodes"
        v-model="nodeIds"
        class="node-select"
        aria-describedby="gost-ruleset-nodes-help"
        multiple
        filterable
        clearable
        collapse-tags
        collapse-tags-tooltip
        placeholder="选择运行 Gost 透明代理的节点"
        no-data-text="请先在节点管理中添加节点"
      >
        <el-option v-for="node in nodes" :key="node.id" :label="`${node.name}（${node.address || '待获取地址'}）`" :value="node.id" />
      </el-select>
      <p id="gost-ruleset-nodes-help" class="ops-muted field-help">取消选中并保存后，该节点按最后一次同步的规则分流。</p>
    </el-form-item>
    <div class="ruleset-preview">
      <p v-if="matchers.length" role="status">
        最近一次转换生成 {{ matchers.length }} 条 Gost 匹配规则<span v-if="updatedAt">，更新时间 {{ updatedAt }}</span>。
      </p>
      <p v-else class="ops-muted" role="status">暂无已转换的规则。</p>
      <details v-if="matchers.length">
        <summary>{{ matchers.length > 20 ? '预览前 20 条规则' : '预览匹配规则' }}</summary>
        <pre class="ops-mono">{{ matchers.slice(0, 20).join('\n') }}</pre>
      </details>
    </div>
    <p v-if="saveError" ref="errorNotice" class="settings-error" role="alert" tabindex="-1">{{ saveError }}</p>
    <el-button type="primary" native-type="submit" :loading="saving">保存规则集订阅</el-button>
  </el-form>
</template>

<script setup>
import { nextTick, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getGostRuleset, updateGostRuleset } from '@/api/system'
import { getRoutingNodes } from '@/api/node'

const nodes = ref([])
const nodeIds = ref([])
const url = ref('')
const matchers = ref([])
const updatedAt = ref('')
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const saveError = ref('')
const errorNotice = ref(null)

function showConfig(data) {
  nodeIds.value = data.node_ids || []
  url.value = data.url || ''
  matchers.value = data.matchers || []
  const timestamp = Date.parse(data.updated_at)
  updatedAt.value = timestamp > 0 ? new Date(timestamp).toLocaleString('zh-CN', { hour12: false }) : ''
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [{ data }, nodeList] = await Promise.all([getGostRuleset(), getRoutingNodes()])
    nodes.value = nodeList
    showConfig(data)
  } catch (error) {
    loadError.value = error.response?.data?.message || '无法读取规则集订阅或节点列表，请重试。'
  } finally {
    loading.value = false
  }
}

async function save() {
  if (saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    const { data } = await updateGostRuleset(url.value, nodeIds.value)
    showConfig(data)
    ElMessage.success('规则集订阅已保存')
  } catch (error) {
    saveError.value = error.response?.data?.message || '无法保存规则集订阅，请重试。'
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
.ruleset-preview { margin: 0 0 16px; padding: 12px; background: var(--ops-inset); border-radius: 8px; }
.ruleset-preview p { margin: 0; }
.ruleset-preview details { margin-top: 8px; }
.ruleset-preview summary { cursor: pointer; }
.ruleset-preview pre { margin: 8px 0 0; overflow-wrap: anywhere; white-space: pre-wrap; }
.settings-error { color: var(--ops-danger); overflow-wrap: anywhere; white-space: pre-wrap; }
</style>
