<template>
  <AssignExecutionModeModal :open="open" :task-uuid="task.uuid" :task-title="task.title" :initial-mode="task.execution_mode || ''" :preferred-expert-group-uuid="task.selected_expert_group_uuid || ''" :preferred-pipeline-uuid="task.selected_pipeline_uuid || ''" :initial-cli-type="task.execution_mode === 'cli' ? task.execution_tool || '' : ''" :initial-vibe-tool="task.execution_mode === 'vibe_coding' ? task.execution_tool || '' : ''" :initial-model-name="task.execution_model || ''" :replace-existing="replaceExisting" mode="detail" @update:open="emit('update:open', $event)" @assigned="emit('assigned')" />
</template>

<script setup lang="ts">
import AssignExecutionModeModal from '@/components/AssignExecutionModeModal.vue'
import type { TaskWithDetails } from '@/types/task-detail'
defineProps<{ open: boolean; task: TaskWithDetails; replaceExisting: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; assigned: [] }>()
</script>
