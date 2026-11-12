<template>
  <div class="page-actions">
    <slot />
  </div>
</template>

<style scoped>
.page-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}
.page-actions :deep(.el-button) { margin: 0; }

@media (max-width: 640px) {
  .page-actions :deep(.el-button) { min-height: 44px; }
}
</style>
