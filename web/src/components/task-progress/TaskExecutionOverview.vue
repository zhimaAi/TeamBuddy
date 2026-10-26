<template>
  <section class="execution-overview">
    <div class="execution-heading">
      <span class="execution-type"><PipelineFlowIcon v-if="task.execution_mode === 'pipeline'" /><TeamOutlined v-else-if="isExpertGroup" /><img v-else :src="modeLogo" alt="" aria-hidden="true" />{{ modeLabel }}</span>
      <span class="heading-separator" aria-hidden="true" />
      <button type="button" class="execution-target" :title="executionLabel" @click="emit('assign')"><img v-if="executionAvatar" :src="executionAvatar" alt="" /><span class="target-name">{{ executionLabel }}</span></button>
      <!-- 整个绑定是否已上报线上：状态 + 上报时间 + 重新上报 -->
      <TaskSyncBadge :task-uuid="task.uuid" :source-type="task.source_type" :refresh-key="task.updated_at" />
      <div v-if="displayMode === 'partial' && showMembers" class="member-stack">
        <button v-for="(step, index) in steps" :key="step.uuid" type="button" :title="step.name" :aria-label="step.name" @click="emit('select', step.uuid)"><img :src="resolveAgentAvatar(step, index)" alt="" /></button>
      </div>
      <a-dropdown v-if="showMembers" v-model:open="settingsOpen" :trigger="['click']">
        <button type="button" class="settings-button" :class="{ 'settings-button--compact': displayMode === 'partial' }" :title="t('workflows.task.workspace.displaySettings')" :aria-label="t('workflows.task.workspace.displaySettings')" :aria-expanded="settingsOpen" aria-haspopup="menu"><SettingOutlined /></button>
        <template #overlay><a-menu :selected-keys="[displayMode]" @click="selectDisplayMode"><a-menu-item v-for="option in displayOptions" :key="option.value"><span>{{ option.label }}</span><CheckOutlined v-if="displayMode === option.value" class="option-check" /></a-menu-item></a-menu></template>
      </a-dropdown>
    </div>
    <AgentStepStrip v-if="showMembers && displayMode === 'all'" class="overview-steps" :steps="steps" :current-step-index="currentStepIndex" :current-step-uuid="currentStepUuid" :selected-step-uuid="selectedStepUuid" :show-arrows="!isExpertGroup" @select="emit('select', $event)" />
    <button v-if="!task.execution_mode || (task.execution_mode === 'pipeline' && !steps.length)" type="button" class="assign-button" @click="emit('assign')"><PlusOutlined />{{ t('workflows.task.workspace.quickAssign') }}</button>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CheckOutlined, PlusOutlined, SettingOutlined, TeamOutlined } from '@ant-design/icons-vue'
import AgentStepStrip from './AgentStepStrip.vue'
import PipelineFlowIcon from './PipelineFlowIcon.vue'
import TaskSyncBadge from './TaskSyncBadge.vue'
import cliLogo from '@/assets/icons/task-composer-cli.svg'
import vibeLogo from '@/assets/icons/vibe-coding-logo.svg'
import { resolveAgentAvatar } from '@/config/agentAvatars'
import { useAppI18n } from '@/i18n'
import { useCliNames } from '@/composables/useCliNames'
import { useTaskExecutionDisplayPreference } from '@/composables/useTaskExecutionDisplayPreference'
import { vibeToolDisplayLabel, vibeToolLabelKey } from '@/composables/useVibeCoding'
import type { TaskWithDetails } from '@/types/task-detail'

const props = defineProps<{ task: TaskWithDetails; selectedStepUuid: string; currentStepUuid: string }>()
const emit = defineEmits<{ select: [uuid: string]; assign: [] }>()
const { t } = useAppI18n()
const { cliDisplayName, ensureLoaded } = useCliNames()
void ensureLoaded()
const { displayMode, setDisplayMode } = useTaskExecutionDisplayPreference()
const settingsOpen = ref(false)
const displayOptions = computed(() => [
  { value: 'hidden', label: t('workflows.task.workspace.hideAll') },
  { value: 'all', label: t('workflows.task.workspace.showAll') },
  { value: 'partial', label: t('workflows.task.workspace.showPartial') },
])
watch(() => props.task.uuid, () => { settingsOpen.value = false })
function selectDisplayMode(event: { key: string | number }) {
  setDisplayMode(String(event.key))
  settingsOpen.value = false
}
const steps = computed(() => [...props.task.steps].sort((a, b) => a.sort_order - b.sort_order))
const isExpertGroup = computed(() => props.task.execution_mode === 'expert_group')
const showMembers = computed(() => ['pipeline', 'expert_group'].includes(props.task.execution_mode || '') && steps.value.length > 0)
const currentStepIndex = computed(() => steps.value.findIndex(step => step.uuid === props.currentStepUuid))
const modeLogo = computed(() => props.task.execution_mode === 'vibe_coding' ? vibeLogo : cliLogo)
const modeLabel = computed(() => props.task.execution_mode === 'pipeline' ? t('workflows.task.common.pipeline') : isExpertGroup.value ? t('agents.expertTeam') : props.task.execution_mode === 'vibe_coding' ? t('workflows.task.assign.vibeCodingMode') : t('workflows.task.common.directExecution'))
const executionAvatar = computed(() => isExpertGroup.value ? props.task.expert_group_avatar_snapshot : props.task.execution_mode === 'pipeline' ? props.task.pipeline_avatar_snapshot : '')
const executionLabel = computed(() => {
  if (isExpertGroup.value) return props.task.expert_group_name_snapshot || t('workflows.task.common.unassignedExecution')
  if (props.task.execution_mode === 'pipeline') return props.task.pipeline_name_snapshot || t('workflows.task.common.unassignedPipeline')
  if (props.task.execution_mode === 'vibe_coding') {
    const label = vibeToolDisplayLabel(props.task.execution_tool || '', t(vibeToolLabelKey(props.task.execution_tool || '')), props.task.execution_tool_name)
    return props.task.execution_model ? `${label} · ${props.task.execution_model}` : label
  }
  if (props.task.execution_mode === 'cli') {
    const cli = cliDisplayName(props.task.execution_tool || '')
    const model = props.task.execution_model || steps.value[0]?.model_name
    return model ? `${cli} · ${model}` : cli
  }
  return t('workflows.task.common.unassignedExecution')
})
</script>

<style scoped>
.execution-overview { position: relative; z-index: 1; flex: 0 0 auto; padding: 12px 24px 8px; background: #fff; box-shadow: 0 4px 12px rgba(34, 52, 79, 0.07); }
.execution-heading { display: flex; min-width: 0; align-items: center; gap: 12px; }
.execution-type { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 8px; color: #8c8c8c; font-size: 14px; line-height: 22px; }
.execution-type img, .execution-type :deep(svg) { width: 16px; height: 16px; }
.heading-separator { width: 1px; height: 12px; background: #f0f0f0; }
.execution-target { display: inline-flex; overflow: hidden; min-width: 0; align-items: center; gap: 8px; padding: 4px 8px; border: 0; border-radius: 6px; color: #262626; background: transparent; font-size: 14px; line-height: 22px; cursor: pointer; text-align: left; }
.execution-target:hover { background: #f0f0f0; }
.execution-target img { width: 20px; height: 20px; border-radius: 50%; object-fit: cover; }
.target-name { overflow: hidden; min-width: 0; text-overflow: ellipsis; white-space: nowrap; }
.settings-button { width: 28px; height: 28px; flex: 0 0 auto; margin-left: auto; border: 0; border-radius: 6px; color: #8c8c8c; background: transparent; font-size: 16px; cursor: pointer; }
.settings-button:hover { color: #3157e2; background: #e5efff; }
.settings-button--compact { margin-left: 0; }
.member-stack { display: flex; overflow: hidden; min-width: 0; margin-left: auto; padding-left: 8px; }
.member-stack button { width: 32px; height: 32px; flex: 0 0 auto; margin-left: -8px; padding: 0; border: 2px solid #fff; border-radius: 50%; background: #f0f0f0; cursor: pointer; }
.member-stack img { width: 100%; height: 100%; border-radius: 50%; object-fit: cover; }
.overview-steps { margin-top: 8px; }
.option-check { margin-left: 32px; color: #3157e2; }
.assign-button { display: flex; align-items: center; gap: 4px; margin-top: 8px; padding: 2px 12px; border: 1px solid #d9d9d9; border-radius: 6px; color: #8c8c8c; background: #fff; cursor: pointer; font-size: 12px; line-height: 20px; }
button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
@media (max-width: 640px) { .execution-overview { padding: 12px 16px 8px; } .member-stack { max-width: 112px; } }
</style>
