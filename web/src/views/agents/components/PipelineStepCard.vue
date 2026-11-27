<template>
  <article
    class="step-row"
    :class="{
      'step-row--batch': batchMode,
      'step-row--selected': batchMode && selected,
    }"
  >
    <a-checkbox
      v-if="batchMode"
      class="step-checkbox"
      :checked="selected"
      :aria-label="t('agents.selectAgent', { name: step.name })"
      @change="emit('toggle')"
    />
    <div
      class="step-timeline"
      aria-hidden="true"
    >
      <span class="step-number">{{ index + 1 }}</span>
      <img
        class="step-line"
        :src="pipelineStepLine"
        alt=""
      />
    </div>
    <div class="step-content">
      <div class="step-body">
        <div class="step-heading">
          <img
            :src="resolveAgentAvatar(step, index)"
            alt=""
          />
          <strong>{{ step.name }}</strong>
        </div>
        <p class="step-description">
          {{ step.description || step.prompt || step.prompt_snapshot || t('agents.missingDescription') }}
        </p>
        <div
          v-if="!step.cli_type || !stepModel(step)"
          class="missing"
        >
          <ExclamationOutlined />{{ missingConfigText() }}
        </div>
      </div>
      <footer>
        <div class="tag-list">
          <span
            v-if="step.cli_type"
            class="badge"
          >
            <img
              :src="pipelineAgentCliIcon"
              alt=""
              aria-hidden="true"
            />
            {{ step.cli_type }}
          </span>
          <span
            v-if="stepModel(step)"
            class="badge"
          >
            <img
              :src="pipelineAgentModelIcon"
              alt=""
              aria-hidden="true"
            />
            {{ stepModel(step) === DEFAULT_CLI_MODEL ? t('components.cliSelect.modelDefault') : stepModel(step) }}
          </span>
        </div>
        <div class="step-actions">
          <button
            v-if="!isCloudPipeline"
            type="button"
            class="action-btn"
            :aria-label="t('agents.moveUp')"
            :disabled="index === 0"
            @click="emit('move', -1)"
          >
            <ArrowUpOutlined />
          </button>
          <button
            v-if="!isCloudPipeline"
            type="button"
            class="action-btn"
            :aria-label="t('agents.moveDown')"
            :disabled="index === total - 1"
            @click="emit('move', 1)"
          >
            <ArrowDownOutlined />
          </button>
          <button
            type="button"
            class="action-btn action-btn-edit"
            :aria-label="isCloudPipeline ? t('agents.configureAgent') : t('agents.editAgent')"
            @click="emit('edit')"
          >
            <EditOutlined />
          </button>
          <button
            v-if="!isCloudPipeline"
            type="button"
            class="action-btn action-btn-danger"
            :aria-label="t('agents.removeAgent')"
            @click="emit('remove')"
          >
            <DeleteOutlined />
          </button>
        </div>
      </footer>
    </div>
  </article>
</template>

<script setup lang="ts">
import { DEFAULT_CLI_MODEL } from '@/utils/cliModelOptions'
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  EditOutlined,
  ExclamationOutlined,
} from '@ant-design/icons-vue'
import type { PipelineStep } from '@/types/pipeline'
import pipelineAgentCliIcon from '@/assets/icons/pipeline-agent-cli.svg'
import pipelineAgentModelIcon from '@/assets/icons/pipeline-agent-model.svg'
import pipelineStepLine from '@/assets/icons/pipeline-step-line.svg'
import { resolveAgentAvatar, stepModel } from './agentPipeline'
import { useAppI18n } from '@/i18n'

const props = defineProps<{
  step: PipelineStep
  index: number
  total: number
  isCloudPipeline: boolean
  batchMode?: boolean
  selected?: boolean
}>()

const { t } = useAppI18n()

function missingConfigText() {
  const action = props.isCloudPipeline ? t('agents.configure') : t('agents.edit')
  if (!props.step.cli_type && !stepModel(props.step)) return t('agents.missingBoth', { action })
  if (!props.step.cli_type) return t('agents.missingCli', { action })
  return t('agents.missingModel', { action })
}

const emit = defineEmits<{
  edit: []
  move: [offset: number]
  remove: []
  toggle: []
}>()
</script>

<style scoped>
.step-row {
  position: relative;
  display: flex;
  gap: 16px;
  overflow: hidden;
  padding: 24px;
  background: #fff;
}

.step-row--batch {
  align-items: flex-start;
  padding: 24px 8px;
  border-radius: 12px;
}

.step-row--selected {
  border-radius: 12px;
  background: #f5f9ff;
}

.step-row::after {
  position: absolute;
  right: 24px;
  bottom: 0;
  left: 64px;
  height: 1px;
  background: #f0f0f0;
  content: '';
}

.step-row--selected::after {
  display: none;
}

.step-checkbox {
  flex: 0 0 auto;
  margin-top: 16px;
}

.step-checkbox :deep(.ant-checkbox-inner) {
  width: 16px;
  height: 16px;
  border-radius: 2px;
  border-color: #d9d9d9;
}

.step-timeline {
  display: flex;
  width: 24px;
  flex: 0 0 24px;
  align-self: stretch;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding-top: 13px;
}

.step-number {
  display: flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border-radius: 16px;
  color: #595959;
  background: #f0f0f0;
  font-size: 14px;
  line-height: 24px;
}

.step-line {
  width: 8.19627px;
  min-height: 1px;
  flex: 1;
}

.step-content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 20px;
}

.step-body {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 16px;
}

.step-heading {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 12px;
}

.step-heading > img {
  width: 40px;
  height: 40px;
  flex: 0 0 40px;
  border-radius: 50%;
  object-fit: cover;
}

.step-heading strong {
  overflow: hidden;
  color: #1d1d1f;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.step-description {
  display: -webkit-box;
  overflow: hidden;
  margin: 0;
  color: #595959;
  font-size: 14px;
  line-height: 26px;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.step-row footer {
  display: flex;
  min-height: 26px;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.tag-list,
.step-actions {
  display: flex;
  align-items: center;
}

.tag-list {
  min-width: 0;
  gap: 8px;
}

.step-actions {
  flex: 0 0 auto;
  gap: 8px;
}

.badge {
  display: inline-flex;
  height: 26px;
  min-width: 0;
  align-items: center;
  gap: 5px;
  overflow: hidden;
  padding: 0 10px;
  border-radius: 999px;
  color: #6e6e73;
  background: rgba(0, 0, 0, 0.05);
  font-size: 12px;
  font-weight: 600;
  line-height: 26px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.badge img {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
}

.action-btn {
  display: inline-flex;
  width: 24px;
  min-width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #595959;
  background: transparent;
  transition: color 0.18s, background-color 0.18s;
  cursor: pointer;
}

.action-btn:hover:not(:disabled) {
  color: #262626;
  background: rgba(0, 0, 0, 0.06);
}

.action-btn:disabled {
  visibility: hidden;
}

.action-btn-edit {
  color: #3157e2;
}

.action-btn-edit:hover:not(:disabled) {
  color: #3157e2;
}

.action-btn-danger:hover:not(:disabled) {
  color: #ff4d4f;
  background: #fff1f0;
}

.action-btn:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.missing {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 5px 8px;
  border-radius: 6px;
  color: #b45309;
  background: #fff7e6;
  font-size: 12px;
  font-weight: 500;
}

@media (prefers-reduced-motion: reduce) {
  .action-btn {
    transition: none;
  }
}
</style>
