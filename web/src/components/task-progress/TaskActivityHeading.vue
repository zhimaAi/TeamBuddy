<template>
  <p v-if="task.execution_mode === 'pipeline'" class="activity-heading"><FilterOutlined /><span>{{ t('workflows.task.detail.filteredActivity', { step: selectedStepName || t('workflows.task.common.currentStep') }) }}</span></p>
  <button v-else-if="task.execution_mode === 'cli'" type="button" class="activity-heading activity-toggle" :aria-expanded="open" @click="emit('update:open', !open)"><DownOutlined :class="{ collapsed: !open }" /><span>{{ t('workflows.task.detail.directExecutionActivity') }}</span></button>
  <p v-else-if="task.execution_mode === 'vibe_coding'" class="activity-heading"><FilterOutlined /><span>{{ t('workflows.task.workspace.vibeActivity', { tool: t(vibeToolLabelKey(task.execution_tool || '')) }) }}</span></p>
</template>

<script setup lang="ts">
import { DownOutlined, FilterOutlined } from '@ant-design/icons-vue'
import { useAppI18n } from '@/i18n'
import { vibeToolLabelKey } from '@/composables/useVibeCoding'
import type { TaskWithDetails } from '@/types/task-detail'
defineProps<{ task: TaskWithDetails; selectedStepName?: string; open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useAppI18n()
</script>

<style scoped>
.activity-heading { display: flex; align-items: center; gap: 8px; margin: 24px 0 16px; color: #8c8c8c; font-size: 12px; line-height: 20px; }
.activity-toggle { padding: 0; border: 0; background: transparent; cursor: pointer; }
.activity-toggle:hover { color: #262626; }
.activity-toggle:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.collapsed { transform: rotate(-90deg); }
</style>
