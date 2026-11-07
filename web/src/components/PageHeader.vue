<template>
  <header class="page-heading">
    <div class="page-heading__intro">
      <span class="page-heading__eyebrow">{{ eyebrow }}</span>
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
  eyebrow: { type: String, required: true },
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

.page-heading__eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--ops-accent);
  font: 600 11px/1.4 var(--ops-mono);
  letter-spacing: .13em;
}

.page-heading__eyebrow::before {
  width: 16px;
  height: 1px;
  background: currentColor;
  content: '';
}

.page-heading h1 {
  margin-top: 6px;
  color: var(--ops-text);
  font-size: clamp(27px, 3vw, 38px);
  font-weight: 730;
  line-height: 1.22;
  text-wrap: balance;
}

.page-heading p {
  max-width: 56ch;
  margin-top: 9px;
  color: var(--ops-muted);
  font-size: 14px;
}

.page-heading__actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

@media (max-width: 640px) {
  .page-heading { align-items: flex-start; flex-direction: column; gap: 12px; }
  .page-heading p { font-size: 16px; }
  .page-heading__actions { width: 100%; }
  .page-heading__actions :deep(.el-button) { min-height: 44px; }
}
</style>
