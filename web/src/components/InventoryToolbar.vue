<template>
  <header ref="toolbar" class="inventory-toolbar" :class="{ 'inventory-toolbar--compact': compact }">
    <slot name="title" />
    <form v-if="!compact" class="inventory-toolbar__inline" :aria-label="filterLabel" @submit.prevent="submitFilters">
      <slot name="filters" />
    </form>
    <el-button
      v-else
      class="inventory-toolbar__filter-button"
      :aria-label="filterLabel"
      aria-haspopup="dialog"
      :aria-expanded="filtersOpen"
      @click="filtersOpen = true"
    >筛选</el-button>
    <slot name="actions" />
  </header>

  <PanelDialog v-if="compact" v-model="filtersOpen" :title="filterLabel" size="compact">
    <form class="inventory-toolbar__dialog" :aria-label="filterLabel" @submit.prevent="submitFilters">
      <slot name="filters" />
    </form>
  </PanelDialog>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import PanelDialog from '@/components/PanelDialog.vue'

defineProps({ filterLabel: { type: String, required: true } })
const emit = defineEmits(['submit'])
const toolbar = ref(null)
const compact = ref(false)
const filtersOpen = ref(false)
let observer

onMounted(() => {
  if (typeof ResizeObserver === 'undefined') return
  observer = new ResizeObserver(([entry]) => {
    compact.value = entry.contentRect.width < 680
    if (!compact.value) filtersOpen.value = false
  })
  observer.observe(toolbar.value)
})

onBeforeUnmount(() => observer?.disconnect())

function submitFilters() {
  emit('submit')
  filtersOpen.value = false
}
</script>

<style scoped>
.inventory-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: var(--ops-card-padding);
}

.inventory-toolbar :deep(h2) { margin: 0; font-size: var(--ops-text-title); white-space: nowrap; }
.inventory-toolbar > :deep(.el-button), .inventory-toolbar :deep(h2) { flex: none; }
.inventory-toolbar > :deep(.el-button + .el-button) { margin-left: 0; }

.inventory-toolbar__inline {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: flex-end;
  min-width: 0;
  gap: 8px;
}

.inventory-toolbar__inline :deep(.inventory-toolbar-field) {
  flex: 0 1 144px;
  min-width: 92px;
}

.inventory-toolbar__inline :deep(.inventory-toolbar-field:first-child) { flex-basis: 180px; }
.inventory-toolbar__inline :deep(.inventory-toolbar-field > label) {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

.inventory-toolbar__inline > :deep(.el-button),
.inventory-toolbar__dialog > :deep(.el-button) { flex: none; margin-left: 0; }

.inventory-toolbar--compact { gap: 8px; }
.inventory-toolbar--compact .inventory-toolbar__filter-button { margin-left: auto; }
.inventory-toolbar--compact > :deep(.el-button) { min-height: 44px; padding-inline: 10px; }

.inventory-toolbar__dialog {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 12px;
}

.inventory-toolbar__dialog :deep(.inventory-toolbar-field) {
  display: grid;
  flex: 1 1 100%;
  min-width: 0;
  gap: 6px;
}

.inventory-toolbar__dialog :deep(.inventory-toolbar-field > label) {
  color: var(--ops-muted);
  font-size: var(--ops-text-sm);
  font-weight: var(--ops-weight-medium);
}

.inventory-toolbar__dialog :deep(.el-input__wrapper),
.inventory-toolbar__dialog :deep(.el-select__wrapper),
.inventory-toolbar__dialog :deep(.el-button) { min-height: 44px; }
</style>
