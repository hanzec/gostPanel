<template>
  <header class="page-heading">
    <div class="page-heading__intro">
      <h1>{{ title }}</h1>
      <p v-if="description">{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="page-heading__actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<script setup>
defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' }
})
</script>

<style scoped>
.page-heading {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 20px;
  padding-block: 4px 8px;
}

.page-heading__intro { min-width: 0; }

.page-heading h1 {
  color: var(--ops-text);
  font-size: var(--ops-text-page);
  font-weight: var(--ops-weight-bold);
  line-height: var(--ops-leading-heading);
  text-wrap: balance;
}

.page-heading p {
  max-width: 56ch;
  margin-top: 9px;
  color: var(--ops-muted);
  font-size: var(--ops-text-base);
}

.page-heading__actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.page-heading__actions :deep(.el-button) { margin: 0; }

@media (max-width: 640px) {
  .page-heading { align-items: flex-start; flex-direction: column; gap: 12px; }
  .page-heading__actions { width: 100%; }
  .page-heading__actions :deep(.el-button) { min-height: 44px; }
}
</style>
