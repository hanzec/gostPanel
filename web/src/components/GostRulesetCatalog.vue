<template>
  <section aria-label="订阅规则集" class="ruleset-catalog">
    <div class="catalog-heading">
      <h3>订阅规则集</h3>
      <el-button type="primary" plain :disabled="loading || busy" @click="edit()">添加规则集</el-button>
    </div>
    <p v-if="loading" role="status" class="ops-muted">正在读取规则集…</p>
    <div v-else-if="loadError" role="alert" class="catalog-error">
      <p>{{ loadError }}</p>
      <el-button @click="load">重试</el-button>
    </div>
    <p v-else-if="!rulesets.length" class="ops-muted">暂无其他规则集。</p>
    <ul v-else class="catalog-list">
      <li v-for="ruleset in rulesets" :key="ruleset.id">
        <div class="catalog-description">
          <strong>{{ ruleset.name }}</strong>
          <span class="ops-muted">{{ ruleset.matcher_count }} 条规则</span>
        </div>
        <div class="catalog-actions">
          <el-button :aria-label="`编辑 ${ruleset.name}`" :disabled="busy" text @click="edit(ruleset)">编辑</el-button>
          <el-button :aria-label="`删除 ${ruleset.name}`" :disabled="busy" type="danger" text @click="remove(ruleset)">删除</el-button>
        </div>
      </li>
    </ul>
    <el-form v-if="editor" label-position="top" :disabled="busy" @submit.prevent="save">
      <el-form-item label="名称" for="named-ruleset-name" required>
        <el-input id="named-ruleset-name" v-model.trim="editor.name" maxlength="100" required />
      </el-form-item>
      <el-form-item label="Clash 规则集链接" for="named-ruleset-url" required>
        <el-input id="named-ruleset-url" v-model.trim="editor.url" type="url" inputmode="url" placeholder="https://example.com/rules.yaml" required />
      </el-form-item>
      <el-button type="primary" native-type="submit" :loading="busy">保存订阅</el-button>
      <el-button @click="editor = null">取消</el-button>
    </el-form>
    <p v-if="operationError" role="alert" class="catalog-error">{{ operationError }}</p>
  </section>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { createGostRuleset, deleteGostRuleset, getGostRulesets, updateNamedGostRuleset } from '@/api/system'
import { confirmPanelAction } from '@/utils/panel-dialog'

const rulesets = ref([])
const editor = ref(null)
const loading = ref(false)
const busy = ref(false)
const loadError = ref('')
const operationError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const { data } = await getGostRulesets()
    rulesets.value = (data || []).filter(ruleset => ruleset.id !== 1)
  } catch (error) {
    loadError.value = error.response?.data?.message || '无法读取规则集。'
  } finally {
    loading.value = false
  }
}

function edit(ruleset = {}) {
  operationError.value = ''
  editor.value = { id: ruleset.id, name: ruleset.name || '', url: ruleset.url || '' }
}

async function save() {
  if (busy.value) return
  busy.value = true
  operationError.value = ''
  const { id, name, url } = editor.value
  try {
    const payload = { name, url }
    if (id) await updateNamedGostRuleset(id, payload)
    else await createGostRuleset(payload)
    editor.value = null
    await load()
    ElMessage.success('规则集已保存')
  } catch (error) {
    operationError.value = error.response?.data?.message || '无法保存规则集。'
  } finally {
    busy.value = false
  }
}

async function remove(ruleset) {
  try {
    await confirmPanelAction(`删除规则集「${ruleset.name}」？`, '删除规则集', { type: 'warning' })
  } catch {
    return
  }
  operationError.value = ''
  busy.value = true
  try {
    await deleteGostRuleset(ruleset.id)
    if (editor.value?.id === ruleset.id) editor.value = null
    await load()
    ElMessage.success('规则集已删除')
  } catch (error) {
    operationError.value = error.response?.data?.message || '无法删除规则集。'
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.ruleset-catalog { margin-bottom: var(--ops-section-gap); }
.catalog-heading, .catalog-list li { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.catalog-heading h3 { margin: 0; }
.catalog-list { list-style: none; padding: 0; margin: 12px 0; }
.catalog-list li { border-bottom: 1px solid var(--ops-border); padding: 12px 0; }
.catalog-description { display: grid; min-width: 0; overflow-wrap: anywhere; }
.catalog-actions { display: flex; flex: none; }
.catalog-error { color: var(--ops-danger); }
</style>
