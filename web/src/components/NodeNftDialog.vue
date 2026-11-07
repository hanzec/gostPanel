<template>
  <el-dialog
    v-model="visible"
    :title="'nft 配置 · ' + node.name"
    width="min(820px, calc(100vw - 24px))"
    :close-on-click-modal="false"
    :close-on-press-escape="!applying"
    :show-close="!applying"
    @closed="$emit('closed')"
  >
    <p v-if="loading" class="ops-muted" role="status">正在从面板读取配置与默认模板…</p>
    <div v-else-if="loadError" class="nft-toolbar" role="alert">
      <p class="nft-error">{{ loadError }}</p>
      <el-button @click="loadConfig">重新加载</el-button>
    </div>
    <template v-else>
      <p id="nft-config-help" class="ops-muted nft-description">
        编辑 nftables JSON。规则仅应用到节点专用表 <code class="ops-mono">inet {{ table }}</code>。
      </p>
      <div class="nft-toolbar">
        <p class="ops-muted" role="status">{{ draftStatus }}</p>
        <el-button :disabled="applying || !template || draft === template" @click="loadTemplate">载入面板默认模板</el-button>
      </div>
      <el-form label-position="top" @submit.prevent="confirmApply">
        <el-form-item label="nftables JSON" for="node-nft-config">
          <el-input
            id="node-nft-config"
            v-model="draft"
            class="nft-editor"
            type="textarea"
            :rows="12"
            :disabled="applying"
            :aria-invalid="Boolean(applyError)"
            :aria-describedby="applyError ? 'nft-config-help nft-config-error' : 'nft-config-help'"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            @input="applyError = ''"
          />
        </el-form-item>
        <p v-if="applyError" id="nft-config-error" ref="errorNotice" class="nft-error" role="alert" tabindex="-1">{{ applyError }}</p>
      </el-form>
    </template>
    <template #footer>
      <el-button :disabled="applying" @click="visible = false">关闭</el-button>
      <el-button type="primary" :loading="applying" :disabled="loading || !!loadError || !draft.trim()" @click="confirmApply">应用到节点</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { applyNodeNftConfig, getNodeNftConfig } from '@/api/node'

const props = defineProps({ node: { type: Object, required: true } })
defineEmits(['closed'])
const visible = defineModel({ type: Boolean, required: true })
const table = ref('')
const savedConfig = ref('')
const template = ref('')
const draft = ref('')
const loading = ref(true)
const applying = ref(false)
const loadError = ref('')
const applyError = ref('')
const errorNotice = ref(null)
const draftStatus = computed(() => {
  if (savedConfig.value && draft.value === savedConfig.value) return '当前显示最近一次成功应用的配置。'
  if (draft.value === template.value) return '当前显示面板默认模板。点击「应用到节点」生效。'
  return '当前草稿有待应用的更改。'
})

const showConfig = (data) => {
  table.value = data.table
  savedConfig.value = data.config || ''
  template.value = data.template || ''
  draft.value = savedConfig.value || template.value
}

const loadConfig = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const res = await getNodeNftConfig(props.node.id)
    showConfig(res.data)
  } catch (error) {
    loadError.value = error.response?.data?.message || '无法读取 nft 配置，请重试。'
  } finally {
    loading.value = false
  }
}

const loadTemplate = async () => {
  if (draft.value !== savedConfig.value) {
    const confirmed = await ElMessageBox.confirm('载入模板会替换当前编辑区的内容。', '载入面板默认模板', {
      confirmButtonText: '载入模板', cancelButtonText: '取消', type: 'warning'
    }).catch(() => false)
    if (!confirmed) return
  }
  draft.value = template.value
  applyError.value = ''
}

const showApplyError = async (message) => {
  applyError.value = message
  await nextTick()
  errorNotice.value?.focus()
}

const confirmApply = async () => {
  if (loading.value || applying.value || loadError.value || !draft.value.trim()) return
  try {
    JSON.parse(draft.value)
  } catch {
    await showApplyError('JSON 格式有误。请检查括号、引号和逗号。')
    return
  }

  const confirmed = await ElMessageBox.confirm(
    `应用后将替换节点「${props.node.name}」的专用 nft 表，转发和连通性可能受影响。`,
    '应用 nft 配置',
    { confirmButtonText: '应用到节点', cancelButtonText: '取消', type: 'warning' }
  ).catch(() => false)
  if (!confirmed) return

  applying.value = true
  applyError.value = ''
  try {
    const res = await applyNodeNftConfig(props.node.id, draft.value)
    showConfig(res.data)
    ElMessage.success('nft 配置已应用')
  } catch (error) {
    await showApplyError(error.response?.data?.message || '无法确认 nft 配置已应用。请核对节点状态后重试。')
  } finally {
    applying.value = false
  }
}

onMounted(loadConfig)
</script>

<style scoped>
.nft-description, .nft-toolbar p { margin: 0; }
.nft-description code { color: var(--ops-text); overflow-wrap: anywhere; }
.nft-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; margin: 12px 0; }
.nft-editor :deep(textarea) { font-family: var(--ops-mono); line-height: 1.6; }
.nft-error { color: var(--ops-danger); overflow-wrap: anywhere; white-space: pre-wrap; }
</style>
