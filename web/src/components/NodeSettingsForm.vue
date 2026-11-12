<template>
  <el-form ref="formRef" class="node-settings-form" :model="form" :rules="rules" label-position="top" @submit.prevent="$emit('submit')">
    <p class="node-settings-help ops-muted">节点主动连接面板，注册时会自动上报 IP。</p>
    <el-form-item label="节点名称" prop="name">
      <el-input v-model="form.name" placeholder="例如 华东入口" />
    </el-form-item>
    <el-form-item label="补充 IP（可选）" prop="manualAddresses" for="node-manual-addresses">
      <el-input
        id="node-manual-addresses"
        v-model="form.manualAddresses"
        type="textarea"
        :rows="3"
        placeholder="每行一个 IPv4 或 IPv6"
        aria-describedby="node-manual-addresses-help"
        spellcheck="false"
      />
      <p id="node-manual-addresses-help" class="node-settings-help ops-muted">可填写服务商映射的公网 IP，每行一个。补充 IP 可用于 Gost 白名单，不替代自动获取的主要 IP，也不会加入本机监听选项。</p>
    </el-form-item>
    <fieldset class="node-address-mappings" aria-describedby="node-address-mappings-help">
      <legend>监听 IP 映射（可选）</legend>
      <p id="node-address-mappings-help" class="node-settings-help ops-muted">隧道目的端绑定本机监听 IP，源端 Gost 使用配对的对外连接 IP 和同一个端口。</p>
      <div v-for="(mapping, index) in form.addressMappings" :key="mapping.key" class="node-address-mapping">
        <el-form-item label="本机监听 IP" :prop="`addressMappings.${index}.listen_ip`" :rules="listenIPRules">
          <el-input v-model.trim="mapping.listen_ip" placeholder="例如 10.0.0.2" spellcheck="false" />
        </el-form-item>
        <el-form-item label="对外连接 IP" :prop="`addressMappings.${index}.connect_ip`" :rules="connectIPRules">
          <el-input v-model.trim="mapping.connect_ip" placeholder="例如 203.0.113.20" spellcheck="false" />
        </el-form-item>
        <el-button :aria-label="`移除第 ${index + 1} 组映射`" @click="removeMapping(index)">移除</el-button>
      </div>
      <el-button ref="addMappingButton" @click="addMapping">添加映射</el-button>
    </fieldset>
    <el-form-item label="流量上限（GiB，可选）" prop="trafficLimitGiB">
      <el-input v-model.trim="form.trafficLimitGiB" inputmode="decimal" placeholder="例如 100.5" aria-describedby="node-traffic-limit-help" clearable />
      <p id="node-traffic-limit-help" class="node-settings-help ops-muted">留空或输入 0 表示不限。达额后节点拦截转发，额度恢复后解除限制。</p>
    </el-form-item>
    <el-form-item label="重置周期" prop="trafficResetPeriod">
      <el-select v-model="form.trafficResetPeriod" :disabled="!hasTrafficLimit" aria-describedby="node-traffic-reset-help">
        <el-option v-for="(label, period) in trafficResetPeriods" :key="period" :label="label" :value="period" />
      </el-select>
      <p id="node-traffic-reset-help" class="node-settings-help ops-muted">按北京时间零点重置，每周从周一开始。</p>
    </el-form-item>
    <el-form-item label="备注" prop="remark">
      <el-input v-model="form.remark" type="textarea" :rows="2" placeholder="可记录节点用途或部署位置" />
    </el-form-item>
    <p v-if="error" class="node-settings-error" role="alert">{{ error }}</p>
  </el-form>
</template>

<script setup>
import { nextTick, ref } from 'vue'
import { trafficResetPeriods } from '@/utils/node-display'

const props = defineProps({
  form: { type: Object, required: true },
  rules: { type: Object, required: true },
  hasTrafficLimit: Boolean,
  error: { type: String, default: '' }
})
defineEmits(['submit'])
const formRef = ref(null)
const addMappingButton = ref(null)
const listenIPRules = [{ required: true, message: '请输入本机监听 IP', trigger: 'blur' }]
const connectIPRules = [{ required: true, message: '请输入对外连接 IP', trigger: 'blur' }]

const addMapping = async () => {
  props.form.addressMappings.push({ key: Symbol(), listen_ip: '', connect_ip: '' })
  await nextTick()
  const rows = formRef.value.$el.querySelectorAll('.node-address-mapping')
  rows[rows.length - 1]?.querySelector('input')?.focus()
}

const removeMapping = async (index) => {
  props.form.addressMappings.splice(index, 1)
  await nextTick()
  addMappingButton.value?.$el.focus()
}

defineExpose({
  validate: async () => {
    try {
      return await formRef.value.validate()
    } catch (errors) {
      await nextTick()
      formRef.value.$el.querySelector('.is-error input, .is-error textarea')?.focus()
      throw errors
    }
  },
  clearValidate: () => formRef.value.clearValidate()
})
</script>

<style scoped>
.node-settings-help { margin: 4px 0 8px; font-size: var(--ops-text-sm); line-height: var(--ops-leading-body); }
.node-settings-error { color: var(--ops-danger); overflow-wrap: anywhere; }
.node-address-mappings { min-width: 0; margin: 0 0 18px; padding: 0; border: 0; }
.node-address-mappings legend { padding: 0; color: var(--el-text-color-regular); font-size: var(--ops-text-sm); }
.node-address-mapping { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)) auto; gap: 8px; align-items: start; }
.node-address-mapping > .el-button { margin-top: 30px; }
@media (max-width: 700px) {
  .node-settings-form :deep(.el-input__wrapper),
  .node-settings-form :deep(.el-select__wrapper) { min-height: 44px; }
  .node-address-mappings .el-button { min-height: 44px; }
}
@media (max-width: 480px) {
  .node-address-mapping { grid-template-columns: minmax(0, 1fr) auto; margin-bottom: 12px; }
  .node-address-mapping > .el-form-item { grid-column: 1 / -1; }
  .node-address-mapping > .el-button { grid-column: 2; margin-top: 0; }
}
</style>
