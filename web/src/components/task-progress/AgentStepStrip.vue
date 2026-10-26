<template>
  <nav
    class="step-strip scrollbar--subtle"
    :aria-label="t('workflows.task.progress.orchestrationSteps')"
  >
    <Tooltip
      v-for="(step, stepIndex) in steps"
      :key="step.uuid"
      :title="stepTooltip(step)"
      :mouse-enter-delay="0"
      :trigger="['hover', 'focus']"
    >
      <span class="step-tooltip-trigger">
        <button
          type="button"
          class="step-card"
          :aria-label="stepTooltip(step)"
          :aria-pressed="selectedStepUuid === step.uuid"
          :aria-current="step.uuid === currentStepUuid ? 'step' : undefined"
          :class="[`step-card--${stepState(step)}`, { selected: selectedStepUuid === step.uuid }]"
          :disabled="readOnly"
          @click="emit('select', step.uuid)"
        >
          <span
            class="step-avatar-wrap"
            :class="{ spinning: stepState(step) === 'process' }"
          >
            <span
              class="step-avatar has-image"
            >
              <img
                :src="resolveAgentAvatar(step, stepIndex)"
                alt=""
              />
            </span>
            <span class="step-state-marker" :class="`marker-${stepState(step)}`" aria-hidden="true"><CheckOutlined v-if="stepState(step) === 'finish'" /><CloseOutlined v-else-if="stepState(step) === 'error'" /><LoadingOutlined v-else-if="stepState(step) === 'process'" spin /><MinusOutlined v-else /></span>
          </span>
          <span class="step-copy">{{ step.name }}</span>
        </button>
      </span>
    </Tooltip>
  </nav>
</template>

<script setup lang="ts">
import {CheckOutlined, CloseOutlined, LoadingOutlined, MinusOutlined } from '@ant-design/icons-vue'
import { Tooltip } from 'ant-design-vue'
import { resolveAgentAvatar } from '@/config/agentAvatars'
import type { PipelineStep } from '@/types/pipeline'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

withDefaults(
  defineProps<{
    steps: PipelineStep[]
    currentStepIndex: number
    currentStepUuid: string
    selectedStepUuid: string
    readOnly?: boolean
    showArrows?: boolean
  }>(),
  { readOnly: false, showArrows: true },
)

const emit = defineEmits<{
  select: [uuid: string]
}>()

function stepState(step: PipelineStep) {
  if (step.execution_status === 'created' || step.execution_status === 'running') return 'process'
  if (step.status === 'completed') return 'finish'
  if (step.execution_status === 'failed') return 'error'
  return 'wait'
}

function stepTooltip(step: PipelineStep) {
  const status = step.execution_status && step.execution_status !== 'idle'
    ? step.execution_status
    : step.status === 'completed' ? 'success' : 'waiting'
  return `${step.name} · ${t(`workflows.task.execution.${status}`)}`
}

</script>

<style scoped>
.step-strip { display: flex; align-items: flex-start; gap: 4px; overflow-x: auto; overflow-y: hidden; padding: 4px 0; }
.step-tooltip-trigger { display: inline-flex; flex: 0 0 auto; }
.step-card { display: flex; width: 96px; min-height: 64px; flex: 0 0 auto; flex-direction: column; align-items: center; gap: 4px; padding: 6px 4px; border: 1px solid transparent; border-radius: 12px; color: #595959; background: transparent; cursor: pointer; }
.step-card:not(:disabled):hover { background: #f0f0f0; }
.step-card.selected { color: #3157e2; background: #e5efff; }
.step-card.selected:not(:disabled):hover { border-color: #3157e2; background: #e5efff; }
.step-card:disabled { cursor: default; pointer-events: none; }
.step-card:focus-visible { outline: 2px solid #3157e2; outline-offset: -2px; }
.step-avatar-wrap { position: relative; display: flex; flex: 0 0 auto; }
.step-avatar { display: flex; overflow: hidden; width: 28px; height: 28px; border-radius: 50%; background: #f0f0f0; }
.step-avatar img { width: 100%; height: 100%; object-fit: cover; }
.step-state-marker { position: absolute; right: -2px; bottom: -2px; display: flex; width: 14px; height: 14px; align-items: center; justify-content: center; border: 1px solid #fff; border-radius: 50%; color: #fff; background: #bfbfbf; font-size: 8px; }
.marker-finish { background: #16a34a; }
.marker-process { background: #3157e2; }
.marker-error { background: #fb363f; }
.step-copy { overflow: hidden; max-width: 100%; font-size: 12px; line-height: 20px; text-overflow: ellipsis; white-space: nowrap; }
.step-card.selected .step-copy { font-weight: 600; }
.step-link { display: inline-flex; width: 16px; height: 16px; flex: 0 0 auto; align-items: center; justify-content: center; margin-top: 12px; color: #d9d9d9; font-size: 12px; }
.step-avatar-wrap.spinning { border-radius: 50%; outline: 1px solid #3157e2; outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) { .step-state-marker :deep(.anticon-spin) { animation: none; } }
</style>
