<template>
  <div class="calibration">
    <p class="ops-inset calibration-note ops-muted">输入校准后的字节数。后续上报会从新值继续累计。</p>
    <form class="ops-inset calibration-card" aria-labelledby="node-calibrate-totals" @submit.prevent="saveTotals">
      <h3 class="ops-subsection-title" id="node-calibrate-totals">累计流量</h3>
      <div class="calibration-grid">
        <div class="calibration-field">
          <label for="calibrate-input">累计入站（字节）</label>
          <el-input id="calibrate-input" v-model.trim="input" inputmode="numeric" />
          <small class="ops-muted">当前 {{ formatBytes(node.input_bytes) }}</small>
        </div>
        <div class="calibration-field">
          <label for="calibrate-output">累计出站（字节）</label>
          <el-input id="calibrate-output" v-model.trim="output" inputmode="numeric" />
          <small class="ops-muted">当前 {{ formatBytes(node.output_bytes) }}</small>
        </div>
      </div>
      <el-button native-type="submit" type="primary" :loading="saving === 'totals'" :disabled="!totalsValid || !!saving">保存累计流量</el-button>
    </form>
    <form class="ops-inset calibration-card" aria-labelledby="node-calibrate-quota" @submit.prevent="saveQuota">
      <h3 class="ops-subsection-title" id="node-calibrate-quota">当前周期配额用量</h3>
      <p class="ops-muted">节点会从新用量继续计量，并按流量上限执行限流。</p>
      <div class="calibration-field">
        <label for="calibrate-quota">配额用量（字节）</label>
        <el-input id="calibrate-quota" v-model.trim="quota" inputmode="numeric" :disabled="!quotaAvailable" />
        <small class="ops-muted">
          {{ quotaAvailable ? '当前 ' + formatBytes(node.traffic_quota_used_bytes) + ' / ' + formatBytes(node.traffic_limit_bytes) : '需要在线且已设置流量上限的节点。' }}
        </small>
      </div>
      <el-button native-type="submit" :loading="saving === 'quota'" :disabled="!quotaAvailable || !isBytes(quota) || !!saving">保存配额用量</el-button>
    </form>
    <p v-if="error" class="calibration-error" role="alert">{{ error }}</p>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { calibrateNodeTraffic } from '@/api/node'
import { formatBytes } from '@/utils/node-display'

const props = defineProps({ node: { type: Object, required: true } })
const emit = defineEmits(['saved'])
const input = ref(String(props.node.input_bytes || 0))
const output = ref(String(props.node.output_bytes || 0))
const quota = ref(String(props.node.traffic_quota_used_bytes || 0))
const saving = ref('')
const error = ref('')

const isBytes = value => /^\d+$/.test(value) && Number.isSafeInteger(Number(value))
const totalsValid = computed(() => isBytes(input.value) && isBytes(output.value) && Number.isSafeInteger(Number(input.value) + Number(output.value)))
const quotaAvailable = computed(() => props.node.status === 'online' && props.node.traffic_limit_bytes > 0)

const save = async (kind, payload) => {
  if (saving.value) return
  saving.value = kind
  error.value = ''
  try {
    await calibrateNodeTraffic(props.node.id, payload)
    ElMessage.success('流量已校准')
    emit('saved')
  } catch (err) {
    error.value = err.response?.data?.message || err.message || '无法校准流量'
  } finally {
    saving.value = ''
  }
}

const saveTotals = () => totalsValid.value && save('totals', {
  input_bytes: Number(input.value), output_bytes: Number(output.value)
})
const saveQuota = () => quotaAvailable.value && isBytes(quota.value) && save('quota', {
  quota_used_bytes: Number(quota.value)
})
</script>

<style scoped>
.calibration, .calibration-card, .calibration-grid, .calibration-field { display: grid; gap: 12px; }
.calibration-card { justify-items: start; }
.calibration-field label { font-size: var(--ops-text-body); font-weight: var(--ops-weight-medium); }
.calibration-field small { font-size: var(--ops-text-sm); }
.calibration-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); width: 100%; }
.calibration-field { gap: 5px; width: 100%; }
.calibration-card .el-button { min-height: 44px; }
.calibration-error { color: var(--ops-danger); }
@media (max-width: 560px) { .calibration-grid { grid-template-columns: 1fr; } }
</style>
