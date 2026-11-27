<template>
  <a-modal
    :open="open"
    class="copy-agent-modal"
    :title="multiple ? t('agents.chooseAgents') : t('agents.chooseAgent')"
    width="730px"
    centered
    :closable="!saving"
    :mask-closable="!saving"
    :keyboard="!saving"
    @update:open="handleOpenChange"
  >
    <a-spin :spinning="loading">
      <div class="agent-list">
        <div class="agent-list__scroll">
          <button
            v-for="(step, index) in deduplicatedSourceSteps"
            :key="`${step.pipelineName}-${step.uuid}`"
            type="button"
            class="agent-list-item"
            :disabled="saving"
            :class="{ active: selectedStepUuids.includes(step.uuid) }"
            :aria-pressed="selectedStepUuids.includes(step.uuid)"
            @click="toggleSelection(step.uuid)"
          >
            <img
              class="agent-list-item__avatar"
              :src="resolveAgentAvatar(step, index)"
              alt=""
            />
            <span class="agent-list-item__content">
              <strong>{{ step.name }}</strong>
              <span class="agent-list-item__description">
                {{
                  step.description ||
                  step.prompt ||
                  step.prompt_snapshot ||
                  t('agents.agentNoDescription')
                }}
              </span>
              <span class="agent-list-item__tags">
                <span>
                  <CodeOutlined />
                  {{ step.cli_type || t('agents.cliNotConfigured') }}
                </span>
                <span>
                  <DeploymentUnitOutlined />
                  {{ stepModel(step) === DEFAULT_CLI_MODEL ? t('components.cliSelect.modelDefault') : stepModel(step) || t('agents.modelNotConfigured') }}
                </span>
              </span>
            </span>
            <span
              v-if="selectedStepUuids.includes(step.uuid)"
              class="agent-list-item__selected"
              aria-hidden="true"
            >
              <CheckOutlined />
            </span>
          </button>

          <a-empty
            v-if="deduplicatedSourceSteps.length === 0"
            :description="t('agents.noAgentsToChoose')"
          />
        </div>
      </div>
    </a-spin>

    <template #footer>
      <div class="copy-agent-modal__footer-actions">
        <a-button
          :disabled="saving"
          @click="emit('update:open', false)"
        >
          {{ t('agents.cancel') }}
        </a-button>
        <a-button
          type="primary"
          :loading="saving"
          :disabled="!selectedStepUuids.length"
          @click="copy"
        >
          {{
            multiple
              ? t('agents.confirmSelected', { count: selectedStepUuids.length })
              : t('agents.confirm')
          }}
        </a-button>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { DEFAULT_CLI_MODEL } from '@/utils/cliModelOptions'
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CheckOutlined, CodeOutlined, DeploymentUnitOutlined } from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import type { ExpertMember, PipelineStep } from '@/types/pipeline'
import {
  deduplicateReusableSteps,
  resolveAgentAvatar,
  type ReusablePipelineStep,
  type StepInput,
  stepModel,
} from './agentPipeline'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

const props = defineProps<{
  open: boolean
  targetPipelineUuid?: string
  targetExpertGroupUuid?: string
  expertRole?: 'leader' | 'member'
  sourceSteps: ReusablePipelineStep[]
  loading?: boolean
  multiple?: boolean
}>()

const emit = defineEmits<{
  saved: []
  'update:open': [open: boolean]
}>()

const saving = ref(false)
const selectedStepUuids = ref<string[]>([])
const deduplicatedSourceSteps = computed(() => deduplicateReusableSteps(props.sourceSteps))

interface CopyTarget {
  pipelineUuid: string
  expertGroupUuid: string
  expertRole: 'leader' | 'member'
}

watch(
  () => props.open,
  (open) => {
    if (open) selectedStepUuids.value = []
  },
)

function toggleSelection(stepUuid: string) {
  if (saving.value) return
  if (!props.multiple) {
    selectedStepUuids.value = [stepUuid]
    return
  }
  selectedStepUuids.value = selectedStepUuids.value.includes(stepUuid)
    ? selectedStepUuids.value.filter((uuid) => uuid !== stepUuid)
    : [...selectedStepUuids.value, stepUuid]
}

function handleOpenChange(open: boolean) {
  if (saving.value && !open) return
  emit('update:open', open)
}

async function copySource(source: ReusablePipelineStep, target: CopyTarget) {
  const payload: StepInput = {
    name: source.name,
    description: source.description || '',
    avatar: source.avatar || '',
    prompt: source.prompt || source.prompt_snapshot || '',
    cli_type: source.cli_type || '',
    model_name: stepModel(source),
  }
  if (target.expertGroupUuid) {
    await apiClient.post<ExpertMember>(
      `/expert-groups/${encodeURIComponent(target.expertGroupUuid)}/members`,
      { ...payload, member_role: target.expertRole },
    )
    return
  }
  await apiClient.post<PipelineStep>(
    `/pipelines/${encodeURIComponent(target.pipelineUuid)}/steps`,
    payload,
  )
}

async function copy() {
  const sourceByUuid = new Map(deduplicatedSourceSteps.value.map((step) => [step.uuid, step]))
  const sources = selectedStepUuids.value
    .map((uuid) => sourceByUuid.get(uuid))
    .filter((source): source is ReusablePipelineStep => Boolean(source))
  const target: CopyTarget = {
    pipelineUuid: props.targetPipelineUuid || '',
    expertGroupUuid: props.targetExpertGroupUuid || '',
    expertRole: props.expertRole || 'member',
  }
  if (!sources.length || (!target.pipelineUuid && !target.expertGroupUuid)) {
    return message.warning(t('agents.chooseOneAgent'))
  }

  saving.value = true
  const copiedUuids: string[] = []
  try {
    for (const source of sources) {
      await copySource(source, target)
      copiedUuids.push(source.uuid)
    }
    emit('update:open', false)
    emit('saved')
    message.success(t('agents.agentsCopied', { count: copiedUuids.length }))
  } catch (error) {
    if (copiedUuids.length) {
      selectedStepUuids.value = selectedStepUuids.value.filter(
        (uuid) => !copiedUuids.includes(uuid),
      )
      emit('saved')
      message.error(t('agents.agentCopyPartial', { count: copiedUuids.length }))
    } else {
      message.error(error instanceof Error ? error.message : t('agents.agentCopyFailed'))
    }
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.agent-list {
  overflow: hidden;
  border: 1px solid #d9d9d9;
  border-radius: 18px;
  background: #fff;
  box-shadow: 0 2px 24px rgba(0, 0, 0, 0.08);
}

.agent-list__scroll {
  max-height: 627px;
  overflow-y: auto;
}

.agent-list-item {
  position: relative;
  display: flex;
  width: 100%;
  min-height: 125px;
  align-items: center;
  gap: 16px;
  padding: 24px 56px 24px 24px;
  border: 0;
  border-bottom: 1px solid #f0f0f0;
  color: #262626;
  background: #fff;
  cursor: pointer;
  text-align: left;
  transition: background-color 0.2s ease;
}

.agent-list-item:last-of-type {
  border-bottom: 0;
}

.agent-list-item:hover:not(:disabled),
.agent-list-item.active {
  background: #f7f9ff;
}

.agent-list-item:disabled {
  cursor: not-allowed;
}

.agent-list-item:focus-visible {
  z-index: 1;
  outline: 2px solid #3157e2;
  outline-offset: -2px;
}

.agent-list-item__avatar {
  display: block;
  width: 48px;
  height: 48px;
  flex: 0 0 auto;
  border-radius: 50%;
  object-fit: cover;
}

.agent-list-item__content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.agent-list-item__content strong {
  overflow: hidden;
  color: #1d1d1f;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-list-item__description {
  margin-top: 2px;
  overflow: hidden;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 26px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-list-item__tags {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 5px;
  overflow: hidden;
}

.agent-list-item__tags > span {
  display: inline-flex;
  max-width: 50%;
  height: 26px;
  align-items: center;
  gap: 5px;
  padding: 2px 10px;
  overflow: hidden;
  border-radius: 13px;
  color: #595959;
  background: #f5f5f5;
  font-size: 12px;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-list-item__tags :deep(.anticon) {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 14px;
}

.agent-list-item__selected {
  position: absolute;
  top: 50%;
  right: 24px;
  display: flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  color: #fff;
  background: #3157e2;
  font-size: 14px;
  transform: translateY(-50%);
}

.copy-agent-modal__footer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

:deep(.copy-agent-modal) {
  max-width: calc(100vw - 32px);
  padding-bottom: 0;
}

:deep(.copy-agent-modal .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border-radius: 24px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.16);
}

:deep(.copy-agent-modal .ant-modal-header) {
  margin-bottom: 0;
  padding: 24px;
  border-radius: 24px 24px 0 0;
  background: #fff;
}

:deep(.copy-agent-modal .ant-modal-title) {
  color: #262626;
  font-size: 24px;
  font-weight: 600;
  line-height: 32px;
}

:deep(.copy-agent-modal .ant-modal-body) {
  max-height: calc(100vh - 144px);
  overflow-y: auto;
  padding: 16px 48px 8px;
}

:deep(.copy-agent-modal .ant-modal-footer) {
  margin-top: 0;
  padding: 16px 48px;
  border-top: 0;
}

:deep(.copy-agent-modal .ant-modal-footer .ant-btn) {
  min-width: 65px;
  height: 32px;
  padding: 5px 16px;
  border-radius: 6px;
  font-size: 14px;
  line-height: 22px;
}

@media (max-width: 760px) {
  :deep(.copy-agent-modal .ant-modal-header) {
    padding: 20px 24px;
  }

  :deep(.copy-agent-modal .ant-modal-title) {
    font-size: 20px;
    line-height: 28px;
  }

  :deep(.copy-agent-modal .ant-modal-body) {
    padding: 16px 24px 24px;
  }

  :deep(.copy-agent-modal .ant-modal-footer) {
    padding: 16px 24px;
  }

  .agent-list-item {
    padding-right: 48px;
  }

  .agent-list-item__selected {
    right: 16px;
  }
}
</style>
