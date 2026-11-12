<template>
  <template v-if="readonly">
    <dl class="target-routing-summary">
      <dt>分流策略</dt><dd>{{ matchRules ? '匹配规则后走隧道' : '全部走隧道' }}</dd>
      <template v-if="matchRules">
        <dt v-if="rulesetIds.length">订阅规则集</dt>
        <dd v-if="rulesetIds.length">{{ rulesetIds.map(id => rulesets.find(item => item.id === id)?.name || '订阅 #' + id).join('、') }}</dd>
        <dt v-if="manualDomainsText">手动域名</dt><dd v-if="manualDomainsText" class="target-domain-summary ops-mono">{{ manualDomainsText }}</dd>
      </template>
    </dl>
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
      <el-form-item ref="rulesetField" label="订阅规则集" prop="ruleset_ids" class="target-routing-wide" :rules="[{ validator: validateSelection, trigger: 'change' }]">
        <el-select :model-value="rulesetIds" :loading="loading" multiple filterable placeholder="选择订阅规则集，可与手动域名同时使用" @update:model-value="$emit('update:rulesetIds', $event)">
          <el-option v-for="ruleset in rulesets" :key="ruleset.id" :label="ruleset.name" :value="ruleset.id" />
          <el-option v-for="id in missingRulesetIds" :key="id" :label="'订阅 #' + id + '（未找到）'" :value="id" />
        </el-select>
        <div v-if="loadError" class="target-ruleset-error" role="alert">
          <span>无法加载订阅规则集。</span><el-button text :loading="loading" @click="fetchRulesets">重新加载</el-button>
        </div>
        <p v-else class="target-routing-hint ops-muted">订阅可在系统设置中管理。</p>
      </el-form-item>
      <el-form-item label="手动域名" prop="manualDomainsText" class="target-routing-wide">
        <el-input :model-value="manualDomainsText" type="textarea" :rows="3" :aria-describedby="domainsHelpId" placeholder="每行一个域名，例如 example.com" @update:model-value="$emit('update:manualDomainsText', $event)" />
        <p :id="domainsHelpId" class="target-routing-hint ops-muted">每行一个域名，匹配该域名及其子域。至少选择一个订阅规则集或填写一个域名。</p>
      </el-form-item>
    </template>
  </template>
</template>

<script setup>
import { computed, ref, useId, watch } from 'vue'
import { getGostRulesets } from '@/api/system'
import { parseManualDomains } from '@/utils/target-policy'

const props = defineProps({
  originalTarget: Boolean,
  matchRules: Boolean,
  rulesetIds: { type: Array, default: () => [] },
  manualDomainsText: { type: String, default: '' },
  readonly: Boolean
})
defineEmits(['update:originalTarget', 'update:matchRules', 'update:rulesetIds', 'update:manualDomainsText'])
const helpId = `target-routing-${useId()}`
const domainsHelpId = `${helpId}-domains`
const rulesets = ref([])
const loading = ref(false)
const loaded = ref(false)
const loadError = ref(false)
const rulesetField = ref(null)
const missingRulesetIds = computed(() => props.rulesetIds.filter(id => !rulesets.value.some(item => item.id === id)))

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

const validateSelection = (_rule, _value, callback) => callback(props.originalTarget && props.matchRules && !props.rulesetIds.length && !parseManualDomains(props.manualDomainsText).length
  ? new Error('请选择订阅规则集或填写手动域名') : undefined)

watch(() => props.originalTarget && props.matchRules, needed => {
  if (needed && !loaded.value && !loadError.value) fetchRulesets()
}, { immediate: true })
watch(() => props.manualDomainsText, () => rulesetField.value?.validate('change').catch(() => {}))
</script>

<style scoped>
.target-routing-wide { grid-column: 1 / -1; }
.target-routing-hint { margin: 4px 0 0; font-size: var(--ops-text-xs); line-height: 1.5; }
.target-ruleset-error { display: flex; align-items: center; gap: 8px; color: var(--ops-danger); }
.target-routing-summary { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 16px; margin: 8px 0 0; }
.target-routing-summary dt { color: var(--ops-muted); }
.target-routing-summary dd { margin: 0; overflow-wrap: anywhere; }
.target-domain-summary { white-space: pre-wrap; }
</style>
