<template>
  <section class="step-pane">
    <AgentDetailHeader :title="pipeline?.name || t('agents.selectPipeline')">
      <template
        v-if="pipeline"
        #actions
      >
        <a-button
          v-if="sortedSteps.length"
          size="small"
          class="batch-config-button"
          :class="{ 'batch-cancel-button': batchMode }"
          :disabled="batchSaving"
          @click="toggleBatchMode"
        >
          <template #icon>
            <img
              :class="batchMode ? 'batch-cancel-icon' : 'batch-config-icon'"
              :src="batchMode ? batchCancelIcon : batchConfigIcon"
              alt=""
              aria-hidden="true"
            />
          </template>
          {{ batchMode ? t('agents.cancelBatch') : t('agents.batch') }}
        </a-button>
        <a-button
          v-if="batchMode"
          size="small"
          :disabled="batchSaving || selectedStepUuids.length === sortedSteps.length"
          @click="selectAllSteps"
        >
          {{ t('agents.selectAll') }}
        </a-button>
        <AgentAddDropdown
          v-if="!isCloudPipeline && !batchMode"
          ref="addDropdownRef"
          @create="emit('create-agent')"
          @copy="emit('copy-agent')"
        />
      </template>
    </AgentDetailHeader>
    <div
      class="step-list"
      :class="{ 'step-list--batch': batchMode }"
    >
      <template
        v-for="(step, index) in sortedSteps"
        :key="step.uuid"
      >
        <PipelineStepCard
          :step="step"
          :index="index"
          :total="sortedSteps.length"
          :is-cloud-pipeline="isCloudPipeline"
          :batch-mode="batchMode"
          :selected="selectedStepUuids.includes(step.uuid)"
          @edit="emit('edit-agent', step)"
          @move="emit('move-agent', step, $event)"
          @remove="emit('remove-agent', step)"
          @toggle="toggleStepSelection(step.uuid)"
        />
      </template>
      <a-empty
        v-if="pipeline && !sortedSteps.length"
        :description="t('agents.emptyAgents')"
      />
      <a-empty
        v-else-if="!pipeline"
        :description="t('agents.selectLeftPipeline')"
      />
    </div>
    <AgentBatchConfigBar
      v-if="pipeline && batchMode"
      :selected-count="selectedStepUuids.length"
      :saving="batchSaving"
      @apply="applyBatchConfiguration"
      @cancel="clearBatchSelection"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onDeactivated, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import apiClient from '@/api/client'
import type { Pipeline, PipelineStep } from '@/types/pipeline'
import batchCancelIcon from '@/assets/icons/batch-cancel.svg'
import batchConfigIcon from '@/assets/icons/batch-config.svg'
import { isPipelineCloud as isCloudPipelineSource, sortPipelineSteps } from './agentPipeline'
import AgentBatchConfigBar from './AgentBatchConfigBar.vue'
import PipelineStepCard from './PipelineStepCard.vue'
import AgentAddDropdown from './AgentAddDropdown.vue'
import AgentDetailHeader from './AgentDetailHeader.vue'
import { useAppI18n } from '@/i18n'

const props = defineProps<{
  pipeline?: Pipeline
}>()
const { t } = useAppI18n()

const emit = defineEmits<{
  'create-agent': []
  'copy-agent': []
  'edit-agent': [step: PipelineStep]
  'move-agent': [step: PipelineStep, offset: number]
  'remove-agent': [step: PipelineStep]
  updated: [pipeline: Pipeline]
}>()

const isCloudPipeline = computed(() => isCloudPipelineSource(props.pipeline))
const sortedSteps = computed(() => sortPipelineSteps(props.pipeline?.steps || []))
const addDropdownRef = ref<InstanceType<typeof AgentAddDropdown>>()
const batchMode = ref(false)
const batchSaving = ref(false)
const selectedStepUuids = ref<string[]>([])

watch(
  () => props.pipeline?.uuid,
  () => resetBatchState(),
)
watch(
  () => sortedSteps.value.map((step) => step.uuid).join(','),
  () => {
    const available = new Set(sortedSteps.value.map((step) => step.uuid))
    selectedStepUuids.value = selectedStepUuids.value.filter((stepUuid) => available.has(stepUuid))
  },
)

function toggleBatchMode() {
  if (batchMode.value) {
    resetBatchState()
    return
  }
  addDropdownRef.value?.close()
  batchMode.value = true
}

function resetBatchState() {
  batchMode.value = false
  batchSaving.value = false
  selectedStepUuids.value = []
}

function clearBatchSelection() {
  if (batchSaving.value) return
  selectedStepUuids.value = []
}

function selectAllSteps() {
  if (batchSaving.value) return
  selectedStepUuids.value = sortedSteps.value.map((step) => step.uuid)
}

function toggleStepSelection(stepUuid: string) {
  if (batchSaving.value) return
  selectedStepUuids.value = selectedStepUuids.value.includes(stepUuid)
    ? selectedStepUuids.value.filter((item) => item !== stepUuid)
    : [...selectedStepUuids.value, stepUuid]
}

async function applyBatchConfiguration(payload: { cliType: string; modelName: string; thinkingLevel?: string }) {
  if (!props.pipeline || !selectedStepUuids.value.length || batchSaving.value) return
  batchSaving.value = true
  try {
    const updated = await apiClient.put<Pipeline>(
      `/pipelines/${encodeURIComponent(props.pipeline.uuid)}/steps/execution-config`,
      {
        step_uuids: selectedStepUuids.value,
        cli_type: payload.cliType,
        model_name: payload.modelName,
        thinking_level: payload.thinkingLevel,
      },
    )
    emit('updated', updated)
    message.success(t('agents.batchUpdated', { count: selectedStepUuids.value.length }))
    resetBatchState()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.batchFailed'))
  } finally {
    batchSaving.value = false
  }
}

onDeactivated(() => {
  addDropdownRef.value?.close()
})
</script>

<style scoped>
.step-pane {
  position: relative;
  display: flex;
  height: 100%;
  min-width: 0;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  overflow: hidden;
  background: #fff;
}

.step-list {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0 0 24px;
}

.step-list--batch {
  padding: 0 0 96px;
}

@media (max-width: 900px) {
  .step-pane {
    height: auto;
    min-height: 420px;
  }
}
</style>
