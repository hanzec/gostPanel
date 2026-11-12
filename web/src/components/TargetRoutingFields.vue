<template>
  <template v-if="readonly">
    <div class="target-routing-summary">
      <dl><dt>分流策略</dt><dd>{{ matchRules ? '匹配规则后走隧道' : '全部走隧道' }}</dd></dl>
      <table v-if="matchRules" class="target-rule-table" aria-label="分流规则">
        <thead><tr><th scope="col" class="target-rule-type">类型</th><th scope="col">规则内容</th></tr></thead>
        <tbody>
          <tr v-for="row in rows" :key="row.key" :data-rule-type="row.type">
            <td data-label="类型">{{ row.type === 'subscription' ? '订阅规则集' : '手动域名' }}</td>
            <td data-label="规则内容" :class="{ 'ops-mono': row.type === 'domain' }">{{ row.type === 'subscription' ? rulesetName(row.value) : row.value }}</td>
          </tr>
          <tr v-if="!rows.length"><td colspan="2" class="target-rules-empty">尚未添加匹配规则</td></tr>
        </tbody>
      </table>
    </div>
  </template>
  <template v-else>
    <el-form-item label="目标类型" prop="use_original_target" class="target-routing-wide">
      <el-select :model-value="originalTarget" :aria-describedby="helpId" @update:model-value="$emit('update:originalTarget', $event)">
        <el-option label="固定目标地址" :value="false" />
        <el-option label="按原始目标" :value="true" />
      </el-select>
      <p :id="helpId" class="target-routing-hint ops-muted">按原始目标需要连接“透明转发（HTTP/HTTPS）”入口。</p>
    </el-form-item>
    <el-form-item v-if="originalTarget" label="分流策略" prop="match_rules" class="target-routing-wide">
      <el-select :model-value="matchRules" @update:model-value="$emit('update:matchRules', $event)">
        <el-option label="全部走隧道" :value="false" />
        <el-option label="匹配规则后走隧道" :value="true" />
      </el-select>
      <p class="target-routing-hint ops-muted">{{ matchRules ? '匹配所选订阅或手动域名的请求经过隧道，其余直连。' : 'HTTP/HTTPS 请求按原始访问目标经过隧道。' }}</p>
    </el-form-item>
    <template v-if="originalTarget && matchRules">
      <el-form-item ref="rulesetField" label="匹配规则" prop="ruleset_ids" class="target-routing-wide" :rules="[{ validator: validateSelection, trigger: 'change' }]">
        <div class="target-rules-editor">
          <table ref="ruleTable" class="target-rule-table" aria-label="分流规则" :aria-describedby="domainsHelpId">
            <thead><tr><th scope="col" class="target-rule-type">类型</th><th scope="col">规则内容</th><th scope="col" class="target-rule-actions">操作</th></tr></thead>
            <tbody>
              <tr v-for="(row, index) in rows" :key="row.key" :data-rule-key="row.key" :data-rule-type="row.type">
                <td data-label="类型">{{ row.type === 'subscription' ? '订阅规则集' : '手动域名' }}</td>
                <td data-label="规则内容" class="target-rule-content">
                  <el-select v-if="row.type === 'subscription'" :model-value="row.value" :loading="loading" filterable clearable :aria-label="'订阅规则集 ' + (index + 1)" placeholder="选择订阅规则集" no-data-text="请先在系统设置中添加订阅" @update:model-value="updateRow(row, $event)">
                    <el-option v-for="ruleset in rulesets" :key="ruleset.id" :label="ruleset.name" :value="ruleset.id" :disabled="selectedRulesetIds.includes(ruleset.id) && row.value !== ruleset.id" />
                    <el-option v-for="id in missingRulesetIds" :key="id" :label="rulesetName(id)" :value="id" :disabled="selectedRulesetIds.includes(id) && row.value !== id" />
                  </el-select>
                  <el-input v-else :model-value="row.value" :aria-label="'手动域名 ' + (index + 1)" :aria-describedby="domainsHelpId" placeholder="例如 example.com" @update:model-value="updateRow(row, $event)" />
                </td>
                <td class="target-rule-actions"><el-button text type="danger" :aria-label="'删除规则 ' + (index + 1)" @click="removeRow(index)">删除</el-button></td>
              </tr>
              <tr v-if="!rows.length"><td colspan="3" class="target-rules-empty">尚未添加匹配规则，请添加订阅规则集或手动域名。</td></tr>
            </tbody>
          </table>
          <div class="target-rule-add-actions">
            <el-button ref="addSubscriptionButton" @click="addRow('subscription')">添加订阅规则集</el-button>
            <el-button @click="addRow('domain')">添加手动域名</el-button>
          </div>
          <div v-if="loadError" class="target-ruleset-error" role="alert">
            <span>无法加载订阅规则集。</span><el-button text :loading="loading" @click="fetchRulesets">重新加载</el-button>
          </div>
          <p :id="domainsHelpId" class="target-routing-hint ops-muted">域名匹配自身和子域。订阅可在系统设置中管理。</p>
        </div>
      </el-form-item>
    </template>
  </template>
</template>

<script setup>
import { computed, nextTick, ref, toRaw, useId, watch } from 'vue'
import { getGostRulesets } from '@/api/system'
import { parseManualDomains } from '@/utils/target-policy'

const props = defineProps({
  originalTarget: Boolean,
  matchRules: Boolean,
  rulesetIds: { type: Array, default: () => [] },
  manualDomainsText: { type: String, default: '' },
  readonly: Boolean
})
const emit = defineEmits(['update:originalTarget', 'update:matchRules', 'update:rulesetIds', 'update:manualDomainsText'])
const helpId = `target-routing-${useId()}`
const domainsHelpId = `${helpId}-domains`
const rulesets = ref([])
const loading = ref(false)
const loaded = ref(false)
const loadError = ref(false)
const rulesetField = ref(null)
const ruleTable = ref(null)
const addSubscriptionButton = ref(null)
let nextRowKey = 0
const makeRows = () => [
  ...props.rulesetIds.map(value => ({ key: nextRowKey++, type: 'subscription', value })),
  ...parseManualDomains(props.manualDomainsText).map(value => ({ key: nextRowKey++, type: 'domain', value }))
]
const rows = ref(makeRows())
const selectedRulesetIds = computed(() => rows.value.filter(row => row.type === 'subscription' && row.value).map(row => row.value))
const missingRulesetIds = computed(() => selectedRulesetIds.value.filter(id => !rulesets.value.some(item => item.id === id)))
const rulesetName = id => rulesets.value.find(item => item.id === id)?.name || '订阅 #' + id + (loaded.value ? '（未找到）' : '')
let emittedValues = JSON.stringify([props.rulesetIds, props.manualDomainsText])
let emittedRuleIds = toRaw(props.rulesetIds)

const publishRows = () => {
  const ids = [...selectedRulesetIds.value]
  const domains = rows.value.filter(row => row.type === 'domain').map(row => row.value.trim()).filter(Boolean).join('\n')
  emittedValues = JSON.stringify([ids, domains])
  emittedRuleIds = ids
  emit('update:rulesetIds', ids)
  emit('update:manualDomainsText', domains)
  rulesetField.value?.validate('change').catch(() => {})
}

const updateRow = (row, value) => {
  row.value = value ?? ''
  publishRows()
}

const addRow = async type => {
  const row = { key: nextRowKey++, type, value: type === 'subscription' ? null : '' }
  rows.value.push(row)
  await nextTick()
  ruleTable.value?.querySelector(`[data-rule-key="${row.key}"] input`)?.focus()
}

const removeRow = async index => {
  rows.value.splice(index, 1)
  publishRows()
  await nextTick()
  const next = rows.value[index] || rows.value[index - 1]
  if (next) ruleTable.value?.querySelector(`[data-rule-key="${next.key}"] input`)?.focus()
  else addSubscriptionButton.value?.$el.focus()
}

const fetchRulesets = async () => {
  if (loading.value) return
  loading.value = true
  try {
    const { data } = await getGostRulesets()
    rulesets.value = data || []
    loaded.value = true
    loadError.value = false
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

const validateSelection = (_rule, _value, callback) => {
  if (!props.originalTarget || !props.matchRules) return callback()
  if (!rows.value.length) return callback(new Error('请选择订阅规则集或填写手动域名'))
  if (rows.value.some(row => row.type === 'subscription' ? !row.value : !row.value.trim())) return callback(new Error('请填写每一项规则，或删除空白项'))
  callback()
}

watch(() => props.originalTarget && props.matchRules, needed => {
  if (needed && !loaded.value && !loadError.value) fetchRulesets()
}, { immediate: true })
watch([() => props.rulesetIds, () => props.manualDomainsText], ([ids, domains]) => {
  const value = JSON.stringify([ids, domains])
  // Keep blank draft rows and focused inputs when our own updates return through v-model.
  if (toRaw(ids) !== emittedRuleIds || value !== emittedValues) {
    emittedValues = value
    emittedRuleIds = toRaw(ids)
    rows.value = makeRows()
  }
})
</script>

<style scoped>
.target-routing-wide { grid-column: 1 / -1; }
.target-routing-hint { margin: 4px 0 0; font-size: var(--ops-text-xs); line-height: 1.5; }
.target-ruleset-error { display: flex; align-items: center; gap: 8px; color: var(--ops-danger); }
.target-routing-summary > dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 16px; margin: 8px 0; }
.target-routing-summary dt { color: var(--ops-muted); }
.target-routing-summary dd { margin: 0; overflow-wrap: anywhere; }
.target-rules-editor { width: 100%; }
.target-rule-table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: var(--ops-text-body); line-height: 1.5; }
.target-rule-table th, .target-rule-table td { padding: 10px; text-align: left; vertical-align: top; border-bottom: 1px solid var(--ops-border); overflow-wrap: anywhere; }
.target-rule-table th { background: var(--ops-inset); color: var(--ops-muted); font-weight: var(--ops-weight-semibold); }
.target-rule-table .target-rule-type { width: 110px; }
.target-rule-table .target-rule-actions { width: 72px; text-align: right; }
.target-rule-content :deep(.el-select) { width: 100%; }
.target-rule-table .target-rules-empty { padding: 20px 12px; text-align: center; color: var(--ops-muted); }
.target-rule-add-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.target-rule-add-actions .el-button + .el-button { margin-left: 0; }
@media (max-width: 600px) {
  .target-rule-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .target-rule-table tbody { display: block; }
  .target-rule-table tr { display: grid; grid-template-columns: minmax(0, 1fr) auto; padding: 8px 0; border-bottom: 1px solid var(--ops-border); }
  .target-rule-table td { border: 0; min-width: 0; padding: 4px 8px; }
  .target-rule-table .target-rule-content, .target-rule-table td[data-label="规则内容"], .target-rule-table .target-rules-empty { grid-column: 1 / -1; }
  .target-rule-table .target-rule-actions { grid-column: 2; grid-row: 1; width: auto; }
  .target-rule-table td[data-label="规则内容"]::before { content: attr(data-label); display: block; margin-bottom: 4px; font-size: var(--ops-text-xs); color: var(--ops-muted); }
  .target-rule-add-actions .el-button { flex: 1 1 100%; }
}
</style>
