<template>
  <PanelDialog
    v-model="visible"
    :title="'升级 Gost · ' + node.name"
    description="升级会重启 Gost，转发连接可能短暂中断。"
    :close-on-click-modal="false"
    :close-on-press-escape="!upgradePanel?.submitting"
    :show-close="!upgradePanel?.submitting"
    @closed="$emit('closed')"
  >
    <NodeUpgradePanel v-if="visible" ref="upgradePanel" :node="node" component="gost" />
    <template #footer>
      <el-button :disabled="upgradePanel?.submitting" @click="visible = false">关闭</el-button>
    </template>
  </PanelDialog>
</template>

<script setup>
import { useTemplateRef } from 'vue'
import NodeUpgradePanel from '@/components/NodeUpgradePanel.vue'
import PanelDialog from '@/components/PanelDialog.vue'

defineProps({ node: { type: Object, required: true } })
defineEmits(['closed'])
const visible = defineModel({ type: Boolean, required: true })
const upgradePanel = useTemplateRef('upgradePanel')
</script>
