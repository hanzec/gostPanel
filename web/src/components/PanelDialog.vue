<template>
  <el-dialog
    v-model="visible"
    :title="title"
    :width="widths[size]"
    align-center
    class="panel-dialog"
    modal-class="panel-dialog-overlay"
  >
    <template #header="{ titleId, titleClass }">
      <h2 :id="titleId" :class="titleClass">{{ title }}</h2>
      <p v-if="description" class="panel-dialog-description">{{ description }}</p>
    </template>

    <slot />

    <template v-if="$slots.footer" #footer>
      <slot name="footer" />
    </template>
  </el-dialog>
</template>

<script setup>
const visible = defineModel({ type: Boolean, required: true })

defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' },
  size: { type: String, default: 'standard' }
})

const widths = {
  compact: 440,
  standard: 640,
  wide: 820
}
</script>
