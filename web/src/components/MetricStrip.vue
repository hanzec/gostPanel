<template>
  <dl class="metric-strip" :aria-label="label">
    <div v-for="item in items" :key="item.label" class="ops-panel ops-panel-body metric-strip-item" :class="item.tone">
      <dt>
        <span>{{ item.label }}</span>
        <el-icon v-if="item.icon" aria-hidden="true"><component :is="item.icon" /></el-icon>
      </dt>
      <dd class="ops-mono metric-strip-value">{{ item.value ?? '—' }}</dd>
      <dd v-if="item.detail" class="metric-strip-detail">{{ item.detail }}</dd>
    </div>
  </dl>
</template>

<script setup>
defineProps({
  label: { type: String, required: true },
  items: { type: Array, required: true }
})
</script>

<style scoped>
.metric-strip {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
  gap: var(--ops-section-gap);
  margin: 0;
}

.metric-strip-item { min-width: 0; }
.metric-strip-item dt { display: flex; align-items: center; justify-content: space-between; gap: 6px; color: var(--ops-muted); font-size: var(--ops-text-sm); font-weight: var(--ops-weight-semibold); }
.metric-strip-item dt .el-icon { color: var(--metric-ink, var(--ops-accent)); font-size: 16px; flex: none; }
.metric-strip-value { margin: 6px 0 0; color: var(--metric-ink, var(--ops-text)); font-size: var(--ops-text-metric); font-weight: var(--ops-weight-bold); line-height: 1.2; overflow-wrap: anywhere; }
.metric-strip-item.success { --metric-ink: var(--ops-success); }
.metric-strip-item.warning { --metric-ink: var(--ops-warning); }
.metric-strip-item.danger { --metric-ink: var(--ops-danger); }
.metric-strip-detail { margin: 5px 0 0; color: var(--ops-muted); font-size: var(--ops-text-sm); }

@media (max-width: 700px) {
  .metric-strip { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
}
</style>
