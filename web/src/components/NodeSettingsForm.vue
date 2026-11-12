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

defineProps({
  form: { type: Object, required: true },
  rules: { type: Object, required: true },
  hasTrafficLimit: Boolean,
  error: { type: String, default: '' }
})
defineEmits(['submit'])
const formRef = ref(null)
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
@media (max-width: 700px) {
  .node-settings-form :deep(.el-input__wrapper),
  .node-settings-form :deep(.el-select__wrapper) { min-height: 44px; }
}
</style>
