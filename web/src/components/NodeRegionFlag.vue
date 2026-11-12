<template>
  <span v-if="region" class="node-region-flag" role="img" :aria-label="name" :title="name">{{ flag }}</span>
  <span v-else class="ops-muted">—</span>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({ region: { type: String, default: '' } })
const names = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
const name = computed(() => names.of(props.region))
const flag = computed(() =>
  String.fromCodePoint(...Array.from(props.region, letter => 0x1F1E6 + letter.charCodeAt(0) - 65))
)
</script>

<style scoped>
.node-region-flag {
  font-family: "Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", sans-serif;
  font-size: var(--ops-text-base);
}
</style>
