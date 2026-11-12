<template>
  <el-collapse v-if="fields.length" v-model="expanded" class="gost-transport-options">
    <el-collapse-item name="options">
      <template #title>
        <span>高级参数<span v-if="configuredCount"> · {{ configuredCount }} 项已设置</span></span>
      </template>
      <p class="gost-options-help ops-muted">{{ readonly ? '以下参数随出口保存。未设置的参数使用 Gost 默认值。' : '按需配置当前通道。留空使用 Gost 默认值，切换协议会清除当前高级参数。' }}</p>
      <template v-if="readonly">
        <dl v-if="configuredCount" class="gost-options-summary">
          <template v-for="field in configuredFields" :key="field.id">
            <dt>{{ field.label }}</dt>
            <dd>{{ displayValue(field) }}</dd>
          </template>
        </dl>
        <p v-else class="ops-muted">使用 Gost 默认参数。</p>
      </template>
      <div v-else class="gost-options-sections">
        <section v-for="group in groups" :key="group.title" class="gost-option-section" :aria-label="group.title">
          <h4>{{ group.title }}</h4>
          <div class="gost-options-grid">
            <el-form-item
              v-for="field in group.fields"
              :key="field.id"
              :label="field.label"
              :for="inputId(field)"
              :prop="['protocol_options', field.id]"
              :rules="[{ validator: (_rule, value, callback) => validateOption(field, value, callback), trigger: 'blur' }]"
              :data-protocol-option="field.id"
              :class="{ 'gost-option-wide': field.kind === 'headers' }"
            >
              <el-select
                v-if="field.kind === 'boolean' || field.kind === 'select'"
                :id="inputId(field)"
                :model-value="modelValue[field.id] || ''"
                :aria-describedby="inputId(field) + '-help'"
                @update:model-value="updateOption(field.id, $event)"
              >
                <el-option label="使用 Gost 默认值" value="" />
                <template v-if="field.kind === 'boolean'">
                  <el-option label="启用" value="true" />
                  <el-option label="禁用" value="false" />
                </template>
                <template v-else>
                  <el-option v-for="choice in field.choices" :key="choice" :label="choice" :value="choice" />
                </template>
              </el-select>
              <el-input
                v-else
                :id="inputId(field)"
                :model-value="modelValue[field.id] || ''"
                :type="field.kind === 'headers' ? 'textarea' : field.secret ? 'password' : 'text'"
                :show-password="field.secret"
                :rows="3"
                :inputmode="field.kind === 'integer' ? 'numeric' : undefined"
                :placeholder="field.placeholder || '留空使用 Gost 默认值'"
                :aria-describedby="inputId(field) + '-help'"
                @update:model-value="updateOption(field.id, $event)"
              />
              <p :id="inputId(field) + '-help'" class="gost-option-hint ops-muted">
                <span>{{ optionScope(field) }}。</span>
                <span v-if="field.help">{{ field.help }}</span>
                <span v-else-if="field.kind === 'duration'">使用时间单位，例如 30s、1m。</span>
                <span v-else-if="field.kind === 'integer'">填写整数<span v-if="field.min !== undefined">，最小 {{ field.min }}</span><span v-if="field.max !== undefined">，最大 {{ field.max }}</span>。</span>
                <span v-else-if="field.kind === 'csv'">多个值用英文逗号分隔。</span>
                <span v-else-if="field.kind === 'file'">填写对应节点上的文件路径。</span>
                <span v-else-if="field.kind === 'headers'">填写 JSON 对象，例如 {"X-Example":"value"}。</span>
              </p>
            </el-form-item>
          </div>
        </section>
      </div>
    </el-collapse-item>
  </el-collapse>
</template>

<script setup>
import { computed, ref, useId } from 'vue'
import { protocolOptionFields, protocolOptionGroups } from '@/utils/gost-protocols'

const props = defineProps({
  protocol: { type: String, required: true },
  modelValue: { type: Object, default: () => ({}) },
  mptcpSupported: Boolean,
  readonly: Boolean
})
const emit = defineEmits(['update:modelValue'])
const supportedField = field => field.id !== 'mptcp' || props.mptcpSupported === true
const fields = computed(() => protocolOptionFields(props.protocol).filter(supportedField))
const groups = computed(() => protocolOptionGroups(props.protocol)
  .map(group => ({ ...group, fields: group.fields.filter(supportedField) }))
  .filter(group => group.fields.length))
const configuredFields = computed(() => fields.value.filter(field => props.modelValue[field.id]?.trim()))
const configuredCount = computed(() => configuredFields.value.length)
const expanded = ref(configuredCount.value ? ['options'] : [])
const componentId = useId()
const inputId = field => `gost-option-${componentId}-${field.id}`
const optionScope = field => field.listener && field.dialer ? '入口与出口' : field.listener ? '出口监听' : '入口连接'
const displayValue = field => field.secret || field.kind === 'headers' ? '已设置' : field.kind === 'boolean'
  ? props.modelValue[field.id] === 'true' ? '启用' : '禁用'
  : props.modelValue[field.id]

const updateOption = (id, value) => {
  const options = { ...props.modelValue }
  if (value === '') delete options[id]
  else options[id] = value
  emit('update:modelValue', options)
}

const validateOption = (field, value, callback) => {
  const input = value?.trim()
  let message = ''
  if (input && field.kind === 'integer') {
    const number = Number(input)
    if (!/^-?\d+$/.test(input) || !Number.isSafeInteger(number)) message = '请输入整数'
    else if (field.min !== undefined && number < field.min) message = `不能小于 ${field.min}`
    else if (field.max !== undefined && number > field.max) message = `不能大于 ${field.max}`
  }
  if (input && field.kind === 'path' && !input.startsWith('/')) message = '路径须以 / 开头'
  if (input && field.kind === 'headers') {
    try {
      const headers = JSON.parse(input)
      if (!headers || Array.isArray(headers) || typeof headers !== 'object' || Object.values(headers).some(item => typeof item !== 'string')) {
        message = '请输入值为字符串的 JSON 对象'
      }
    } catch {
      message = '请输入有效的 JSON 对象'
    }
  }
  if (message) expanded.value = ['options']
  callback(message ? new Error(message) : undefined)
}
</script>

<style scoped>
.gost-transport-options { width: 100%; }
.gost-options-help { margin: 0 0 16px; }
.gost-option-section + .gost-option-section { border-top: 1px solid var(--ops-border); padding-top: 16px; }
.gost-option-section h4 { margin: 0 0 12px; font-size: var(--ops-text-base); }
.gost-options-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; }
.gost-option-wide { grid-column: 1 / -1; }
.gost-option-hint { width: 100%; margin: 4px 0 0; font-size: var(--ops-text-xs); line-height: 1.5; }
.gost-options-summary { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 16px; }
.gost-options-summary dt { color: var(--ops-muted); }
.gost-options-summary dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 700px) {
  .gost-options-grid { grid-template-columns: minmax(0, 1fr); }
  .gost-options-summary { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  .gost-options-summary dd { margin-bottom: 8px; }
}
</style>
