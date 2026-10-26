<template>
  <a-modal
    :open="open"
    :width="730"
    :title="modalTitle"
    :confirm-loading="saving || Boolean(vibeModelResolving[selectedTarget || ''])"
    :ok-button-props="{ disabled: !canConfirm }"
    :cancel-button-props="{ disabled: saving }"
    :z-index="modalZIndex"
    :ok-text="confirmLabel"
    :cancel-text="t('workflows.task.assign.cancel')"
    :keyboard="!saving"
    :mask-closable="false"
    wrap-class-name="assign-execution-modal"
    @ok="confirm"
    @cancel="close"
  >
    <div class="assign-banner">
      <InfoCircleFilled aria-hidden="true" />
      <I18nT
        v-if="intent === 'assign' || selectOnly"
        tag="p"
        :keypath="
          selectOnly && !taskTitle.trim()
            ? 'workflows.task.assign.executionDescriptionSelect'
            : replaceExisting
              ? 'workflows.task.assign.executionDescriptionSwitch'
              : mode === 'board'
                ? 'workflows.task.assign.executionDescriptionBoard'
                : 'workflows.task.assign.executionDescriptionDetail'
        "
      >
        <template #title>
          <strong>{{ taskTitle }}</strong>
        </template>
      </I18nT>
      <p v-else>{{ t('workflows.task.assign.createConversationHint') }}</p>
    </div>
    <p v-if="replaceExisting" class="assign-switch-warning">
      <ExclamationCircleFilled aria-hidden="true" />
      <span>{{ t('workflows.task.assign.switchWarning') }}</span>
    </p>

    <fieldset class="assign-mode-fieldset">
      <legend class="visually-hidden">{{ t('workflows.task.assign.executionMode') }}</legend>

      <section class="assign-section">
        <div class="assign-section__head">
          <div>
            <p class="assign-section__title">
              <img class="assign-section__logo" :src="vibeCodingLogo" alt="" />
              {{ t('workflows.task.assign.vibeCodingMode') }}
            </p>
            <p class="assign-section__hint">{{ t('workflows.task.assign.vibeCodingCardHint') }}</p>
          </div>
          <button
            v-if="vibeTools.length > previewCount"
            type="button"
            class="assign-section__toggle"
            @click="vibeExpanded = !vibeExpanded"
          >
            {{ t(vibeExpanded ? 'workflows.task.assign.collapseAll' : 'workflows.task.assign.viewAll', { count: vibeTools.length }) }}
          </button>
        </div>
        <div class="assign-grid">
          <div v-for="tool in visibleVibeTools" :key="tool.id" class="assign-grid__cell">
          <a-tooltip
            :title="vibeToolDisabledReason(tool.id)"
          >
            <div
              class="assign-choice"
              :class="{
                'is-selected': selectedMode === 'vibe_coding' && selectedTarget === tool.id,
                'is-disabled': vibeToolDisabled(tool.id),
              }"
              @click="chooseVibeTool(tool.id)"
            >
              <label class="assign-choice__body">
                <input
                  class="assign-choice__input"
                  type="radio"
                  :name="executionModeRadioName"
                  :value="tool.id"
                  :checked="selectedMode === 'vibe_coding' && selectedTarget === tool.id"
                  :disabled="vibeToolDisabled(tool.id)"
                  @change="chooseVibeTool(tool.id)"
                />
                <img
                  class="assign-choice__logo"
                  :class="{ 'is-brand-icon': isManualVibeTool(tool.id) }"
                  :src="vibeToolLogo(tool.id)"
                  alt=""
                />
                <span class="assign-choice__name">{{ t(`workflows.task.assign.vibeTools.${tool.id}`) }}</span>
                <span v-if="tool.id === recentVibeTool" class="assign-choice__recent">{{ t('workflows.task.assign.recent') }}</span>
              </label>
              <VibeModelPicker
                v-if="supportsVibeModel(tool.id)"
                :tool="tool.id"
                :model-value="vibeModels[tool.id] || ''"
                :selected="selectedMode === 'vibe_coding' && selectedTarget === tool.id"
                :disabled="vibeToolDisabled(tool.id) || (!selectOnly && initialMode === 'vibe_coding' && initialVibeTool === tool.id)"
                @update:model-value="selectVibeModel(tool.id, $event)"
                @resolved="setVibeModel(tool.id, $event)"
              />
              <CheckCircleFilled
                v-if="selectedMode === 'vibe_coding' && selectedTarget === tool.id"
                class="assign-choice__check"
                aria-hidden="true"
              />
              <span v-else class="assign-choice__radio" aria-hidden="true" />
            </div>
          </a-tooltip>
          </div>
        </div>
      </section>

      <section class="assign-section">
        <p class="assign-section__title">
          <img class="assign-section__logo" :src="cliExecutionLogo" alt="" />
          {{ t('workflows.task.common.directExecution') }}
        </p>
        <p class="assign-section__hint">{{ t('workflows.task.assign.directExecutionHint') }}</p>
        <div class="assign-runtime" :class="{ 'is-disabled': cliModeLocked }">
          <a-select
            class="assign-runtime__select"
            :value="selectedMode === 'cli' ? selectedTarget : undefined"
            :placeholder="t('workflows.task.assign.chooseCli')"
            :options="cliSelectOptions"
            :loading="cliLoading"
            :disabled="saving || cliModeLocked"
            :dropdown-style="selectDropdownStyle"
            @change="chooseCLIType"
          >
            <template #option="{ label, value, modelCount }">
              <span class="assign-cli-option">
                <span class="assign-cli-option__name">{{ label ?? value }}</span>
                <span class="assign-cli-option__count">{{ modelCount ?? '…' }}</span>
              </span>
            </template>
          </a-select>
          <a-select
            class="assign-runtime__select"
            :value="selectedMode === 'cli' ? selectedModelName || undefined : undefined"
            :placeholder="selectedMode === 'cli' && selectedTarget ? t('workflows.task.assign.chooseModel') : t('workflows.task.assign.chooseCliFirst')"
            :options="modelSelectOptions"
            :loading="modelLoading"
            :disabled="saving || cliModeLocked || selectedMode !== 'cli' || !selectedTarget"
            :dropdown-style="selectDropdownStyle"
            @change="chooseCLIModel"
          >
            <template #option="{ label, value }">
              <a-tooltip
                :title="String(label ?? value ?? '')"
                placement="right"
                :mouse-enter-delay="0"
                :overlay-style="modelNameTooltipStyle"
              >
                <span class="assign-model-option">{{ label ?? value }}</span>
              </a-tooltip>
            </template>
          </a-select>
        </div>
        <p v-if="!cliLoading && !availableCliOptions.length" class="field-help">
          {{ t('workflows.task.assign.noCli') }}
        </p>
      </section>

      <section class="assign-section">
        <div class="assign-section__head">
          <div>
            <p class="assign-section__title">
              <DeploymentUnitOutlined class="assign-section__icon" aria-hidden="true" />
              {{ t('workflows.task.assign.pipelineField') }}
            </p>
            <p class="assign-section__hint">{{ t('workflows.task.assign.pipelineHint') }}</p>
          </div>
          <button
            v-if="pipelines.length > previewCount"
            type="button"
            class="assign-section__toggle"
            @click="pipelineExpanded = !pipelineExpanded"
          >
            {{ t(pipelineExpanded ? 'workflows.task.assign.collapseAll' : 'workflows.task.assign.viewAll', { count: pipelines.length }) }}
          </button>
        </div>
        <a-spin :spinning="pipelinesLoading">
          <div class="assign-grid">
            <div
              v-for="pipeline in visiblePipelines"
              :key="pipeline.uuid"
              class="assign-choice"
              :class="{
                'is-selected': selectedPipelineUuid === pipeline.uuid,
                'is-disabled': !isPipelineAvailable(pipeline),
              }"
            >
              <label class="assign-choice__body">
                <input
                  class="assign-choice__input"
                  type="radio"
                  :name="executionModeRadioName"
                  :checked="selectedPipelineUuid === pipeline.uuid"
                  :disabled="!isPipelineAvailable(pipeline)"
                  @change="choosePipeline(pipeline.uuid)"
                />
                <span class="assign-choice__avatar">
                  <img v-if="pipeline.avatar" :src="pipeline.avatar" alt="" />
                  <span v-else>{{ pipeline.name.slice(0, 1) }}</span>
                </span>
                <span class="assign-choice__name" :title="pipeline.name">{{ pipeline.name }}</span>
                <CheckCircleFilled
                  v-if="selectedPipelineUuid === pipeline.uuid"
                  class="assign-choice__check"
                  aria-hidden="true"
                />
                <span v-else class="assign-choice__radio" aria-hidden="true" />
              </label>
              <button
                v-if="!isPipelineConfigured(pipeline)"
                type="button"
                class="assign-choice__config"
                @click="goToPipelineConfig"
              >
                {{ t('workflows.task.assign.configure') }}
              </button>
            </div>
          </div>
          <div v-if="!pipelinesLoading && !configuredPipelines.length" class="field-help">
            <span>{{ t('workflows.task.assign.noConfiguredPipeline') }}</span>
            <button type="button" class="assign-section__toggle" @click="goToPipelineConfig">
              {{ t('workflows.task.assign.configure') }}
            </button>
          </div>
        </a-spin>
      </section>

      <section class="assign-section">
        <div class="assign-section__head">
          <div>
            <p class="assign-section__title">
              <TeamOutlined class="assign-section__icon" aria-hidden="true" />
              {{ t('agents.expertTeam') }}
            </p>
            <p class="assign-section__hint">{{ t('workflows.task.assign.expertHint') }}</p>
          </div>
          <button
            v-if="expertGroups.length > previewCount"
            type="button"
            class="assign-section__toggle"
            @click="expertExpanded = !expertExpanded"
          >
            {{ t(expertExpanded ? 'workflows.task.assign.collapseAll' : 'workflows.task.assign.viewAll', { count: expertGroups.length }) }}
          </button>
        </div>
        <a-spin :spinning="expertGroupsLoading">
          <div class="assign-grid">
            <div
              v-for="group in visibleExpertGroups"
              :key="group.uuid"
              class="assign-choice"
              :class="{
                'is-selected': selectedMode === 'expert_group' && selectedTarget === group.uuid,
                'is-disabled': expertModeLocked || !group.ready,
              }"
            >
              <label class="assign-choice__body">
                <input
                  class="assign-choice__input"
                  type="radio"
                  :name="executionModeRadioName"
                  :checked="selectedMode === 'expert_group' && selectedTarget === group.uuid"
                  :disabled="expertModeLocked || !group.ready"
                  @change="chooseExpertGroup(group.uuid)"
                />
                <span class="assign-choice__avatar">
                  <img v-if="group.avatar" :src="group.avatar" alt="" />
                  <span v-else>{{ group.name.slice(0, 1) }}</span>
                </span>
                <span class="assign-choice__name" :title="group.name">{{ group.name }}</span>
                <CheckCircleFilled
                  v-if="selectedMode === 'expert_group' && selectedTarget === group.uuid"
                  class="assign-choice__check"
                  aria-hidden="true"
                />
                <span v-else class="assign-choice__radio" aria-hidden="true" />
              </label>
              <button
                v-if="!group.ready"
                type="button"
                class="assign-choice__config"
                @click="goToExpertGroupConfig"
              >
                {{ t('workflows.task.assign.configure') }}
              </button>
            </div>
          </div>
          <div v-if="!expertGroupsLoading && !expertGroups.some((group) => group.ready)" class="field-help">
            <span>{{ t('expertGroups.notReady') }}</span>
            <button type="button" class="assign-section__toggle" @click="goToExpertGroupConfig">
              {{ t('expertGroups.listTitle') }}
            </button>
          </div>
        </a-spin>
      </section>
    </fieldset>
  </a-modal>
</template>

<script setup lang="ts">
import VibeModelPicker from '@/components/VibeModelPicker.vue'
import { useVibeModelPreferences } from '@/composables/useVibeModelPreferences'
import { showVibeToolOpenResult } from '@/composables/useVibeToolFeedback'
import { computed, ref, useId, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Translation as I18nT } from 'vue-i18n'
import {
  CheckCircleFilled,
  DeploymentUnitOutlined,
  ExclamationCircleFilled,
  InfoCircleFilled,
  TeamOutlined,
} from '@ant-design/icons-vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import apiClient from '@/api/client'
import cliExecutionLogo from '@/assets/icons/task-composer-cli.svg'
import vibeCodingLogo from '@/assets/icons/vibe-coding-logo.svg'
import {
  loadVibeToolsCapability,
  openTaskVibeTool,
  type VibeToolsCapability,
} from '@/composables/useTaskVibeTool'
import {
  VIBE_CODING_TOOLS,
  supportsVibeModel,
  vibeToolLogo,
  isManualVibeTool,
  isVibeCodingTool,
  readRecentVibeTool,
  writeRecentVibeTool,
  type VibeCodingToolId,
} from '@/composables/useVibeCoding'
import { useCliModelOptions } from '@/composables/useCliModelOptions'
import { useCliKickoffNavigation } from '@/composables/useCliKickoff'
import { useAppI18n } from '@/i18n'
import { usePipelineStore } from '@/stores/pipeline'
import { useExpertGroupStore } from '@/stores/expert-group'
import type { ExpertGroup, Pipeline, TaskExecutionMode } from '@/types/pipeline'

export interface ExecutionModeSelection {
  mode: 'pipeline' | 'expert_group' | 'vibe_coding' | 'cli'
  tool: string
  pipelineUuid: string
  expertGroupUuid: string
  modelName: string
}

const props = withDefaults(
  defineProps<{
    open: boolean
    taskUuid?: string
    taskTitle?: string
    mode?: 'detail' | 'board'
    initialMode?: Extract<TaskExecutionMode, 'pipeline' | 'expert_group' | 'vibe_coding' | 'cli'> | ''
    preferredPipelineUuid?: string
    preferredExpertGroupUuid?: string
    initialCliType?: string
    initialModelName?: string
    initialVibeTool?: string
    replaceExisting?: boolean
    intent?: 'assign' | 'create'
    /** 只回传选择，不调用指派接口。新建任务和配置团队需求用这个入口。 */
    selectOnly?: boolean
  }>(),
  {
    taskUuid: '',
    taskTitle: '',
    mode: 'detail',
    initialMode: '',
    preferredPipelineUuid: '',
    preferredExpertGroupUuid: '',
    initialCliType: '',
    initialModelName: '',
    initialVibeTool: '',
    replaceExisting: false,
    intent: 'assign',
    selectOnly: false,
  },
)

const emit = defineEmits<{
  'update:open': [value: boolean]
  assigned: []
  create: [value: ExecutionModeSelection]
  selected: [value: ExecutionModeSelection]
}>()

const { t } = useAppI18n()
// 新建任务弹窗是 1000。本弹窗要盖住它。antd 下拉层默认 1050，
// 不抬高的话 CLI / 模型菜单落在弹窗后面，选项点不到。
const modalZIndex = 1100
const selectDropdownStyle = { zIndex: modalZIndex + 100 }
const modelNameTooltipStyle = { zIndex: selectDropdownStyle.zIndex + 100 }
const { openCliConversation } = useCliKickoffNavigation()
const router = useRouter()
const pipelineStore = usePipelineStore()
const { pipelines, loading: pipelinesLoading } = storeToRefs(pipelineStore)
const expertGroupStore = useExpertGroupStore()
const { items: expertGroups, loading: expertGroupsLoading } = storeToRefs(expertGroupStore)
const saving = ref(false)
const previewCount = 2
const vibeExpanded = ref(false)
const pipelineExpanded = ref(false)
const expertExpanded = ref(false)
const recentVibeTool = ref('')
const codexCapability = ref<VibeToolsCapability>({ available: false })
const vibeTools = VIBE_CODING_TOOLS
const radioGroupId = useId()
const executionModeRadioName = `assign-execution-mode-${radioGroupId}`
const selectedMode = ref<Extract<TaskExecutionMode, 'pipeline' | 'expert_group' | 'vibe_coding' | 'cli'> | undefined>()
const selectedTarget = ref<string>()
// CLI 直接执行没有 Agent 编排，模型选择与 CLI 一起构成完整的执行目标。
const selectedModelName = ref('')
const { vibeModels, resolving: vibeModelResolving, resetVibeModels, setVibeModel, resolveVibeModel } = useVibeModelPreferences()

interface ExecutionModeAssignmentResult {
  switched?: boolean
  start_status?: 'started' | 'failed' | 'awaiting_message'
  start_error?: string
}

const {
  cliLoading,
  cliOptions,
  loadCliOptions,
  loadModelOptions,
  modelCounts,
  modelLoading,
  modelOptions,
  prefetchModelCounts,
} = useCliModelOptions()

const availableCliOptions = computed(() => cliOptions.value.filter((cli) => cli.installed))
const cliSelectOptions = computed(() => {
  const items = cliOptions.value.map((cli) => ({
    value: cli.type,
    label: cli.name,
    modelCount: modelCounts.value[cli.type],
    disabled: !cli.installed,
  }))
  // 已指派任务的 CLI 可能来自其他设备（本机未安装），保留可回显。
  const current = props.initialCliType
  if (current && !items.some((item) => item.value === current)) {
    items.unshift({
      value: current,
      label: current,
      modelCount: modelCounts.value[current],
      disabled: false,
    })
  }
  return items
})
const modelSelectOptions = computed(() =>
  modelOptions.value.map((model) => ({ value: model, label: model, title: '' })),
)

const configuredPipelines = computed(() => pipelines.value.filter(isPipelineConfigured))
const selectedPipelineUuid = computed(() =>
  selectedMode.value === 'pipeline' ? selectedTarget.value || '' : '',
)
const visibleVibeTools = computed(() =>
  vibeExpanded.value ? vibeTools : vibeTools.slice(0, previewCount),
)
const visiblePipelines = computed(() =>
  pipelineExpanded.value ? pipelines.value : pipelines.value.slice(0, previewCount),
)
const visibleExpertGroups = computed(() =>
  expertExpanded.value ? expertGroups.value : expertGroups.value.slice(0, previewCount),
)
const modalTitle = computed(() =>
  t(
    props.selectOnly
      ? 'workflows.task.assign.executionTitle'
      : props.intent === 'create'
        ? 'workflows.task.assign.createConversationTitle'
        : props.replaceExisting
          ? 'workflows.task.assign.switchTitle'
          : 'workflows.task.assign.executionTitle',
  ),
)
const confirmLabel = computed(() =>
  t(
    props.selectOnly
      ? 'workflows.task.assign.confirm'
      : props.intent === 'create'
        ? 'workflows.task.assign.createConversationAction'
        : props.replaceExisting
          ? 'workflows.task.assign.confirmSwitch'
          : 'workflows.task.assign.confirmAssign',
  ),
)
// 看板仍会用本组件补全已预选但尚未冻结的流水线；该入口不是执行方式切换，
// 只能继续配置当前方式。详情页显式切换时 replaceExisting=true，才解锁其他方式。
const modeLocked = computed(
  () => props.intent === 'assign' && !props.selectOnly && !props.replaceExisting && Boolean(props.initialMode),
)
const pipelineLocked = computed(() => modeLocked.value && props.initialMode !== 'pipeline')
const directModeLocked = computed(() => modeLocked.value && props.initialMode !== 'vibe_coding')
const cliModeLocked = computed(() => modeLocked.value && props.initialMode !== 'cli')
const expertModeLocked = computed(() => modeLocked.value && props.initialMode !== 'expert_group')
const selectionChanged = computed(() => {
  if (!props.replaceExisting) return true
  if (selectedMode.value !== props.initialMode) return true
  if (selectedMode.value === 'pipeline') return selectedTarget.value !== props.preferredPipelineUuid
  if (selectedMode.value === 'expert_group') return selectedTarget.value !== props.preferredExpertGroupUuid
  if (selectedMode.value === 'cli') {
    return selectedTarget.value !== props.initialCliType || selectedModelName.value !== props.initialModelName
  }
  if (selectedMode.value === 'vibe_coding') return selectedTarget.value !== props.initialVibeTool
  return false
})
const canConfirm = computed(() => {
  if (!selectionChanged.value) return false
  if (!selectedMode.value || !selectedTarget.value) return false
  if (selectedMode.value === 'cli') {
    return Boolean(selectedModelName.value)
  }
  if (selectedMode.value === 'vibe_coding') {
    return isVibeCodingTool(selectedTarget.value) && !vibeToolDisabled(selectedTarget.value) && !vibeModelResolving.value[selectedTarget.value]
  }
  if (selectedMode.value === 'expert_group') {
    return expertGroups.value.some((group: ExpertGroup) => group.ready && group.uuid === selectedTarget.value)
  }
  return configuredPipelines.value.some((pipeline) => pipeline.uuid === selectedTarget.value)
})

function isPipelineConfigured(pipeline: Pipeline) {
  const steps = pipeline.steps || []
  return steps.length > 0 && steps.every((step) => Boolean(step.cli_type && (step.model_name || step.model)))
}

function isPipelineAvailable(pipeline: Pipeline) {
  return !pipelineLocked.value && isPipelineConfigured(pipeline)
}

function close() {
  if (!saving.value) emit('update:open', false)
}

function choosePipeline(uuid: string) {
  selectedMode.value = 'pipeline'
  selectedTarget.value = uuid
}

function chooseCLIType(cliType: string) {
  selectedMode.value = 'cli'
  selectedTarget.value = cliType
  selectedModelName.value = ''
  void loadModelOptions(cliType)
}

function vibeCapability(tool: string) {
  return codexCapability.value.tools?.[tool]
}

function vibeToolDisabled(tool: string) {
  if (directModeLocked.value || !isVibeCodingTool(tool)) return true
  const capability = vibeCapability(tool)
  if (!capability) return tool !== 'codex' || !codexCapability.value.available
  if (!capability.available) return true
  // 手动模式不启动本机进程，没有可执行文件可探测，只要能力可用就允许选择。
  if (isManualVibeTool(tool)) return false
  return tool !== 'codex' && capability.installed === false
}

function vibeToolDisabledReason(tool: string) {
  if (!vibeToolDisabled(tool)) return ''
  return vibeCapability(tool)?.message || codexCapability.value.message || t('workflows.task.codex.capabilityUnavailable')
}

function selectVibeModel(tool: VibeCodingToolId, model: string) {
  setVibeModel(tool, model)
  chooseVibeTool(tool)
}

function chooseVibeTool(tool: VibeCodingToolId) {
  if (vibeToolDisabled(tool)) return
  selectedMode.value = 'vibe_coding'
  selectedTarget.value = tool
}

function chooseCLIModel(model: string) {
  selectedModelName.value = model
}

function chooseExpertGroup(uuid: string) {
  selectedMode.value = 'expert_group'
  selectedTarget.value = uuid
}

function goToPipelineConfig() {
  close()
  void router.push({ name: 'agents' })
}

function goToExpertGroupConfig() {
  close()
  void router.push({ name: 'agents', query: { mode: 'expert_group' } })
}

function currentSelection(): ExecutionModeSelection | null {
  if (!selectedMode.value) return null
  return {
    mode: selectedMode.value,
    tool: selectedMode.value === 'cli' || selectedMode.value === 'vibe_coding' ? selectedTarget.value || '' : '',
    pipelineUuid: selectedMode.value === 'pipeline' ? selectedTarget.value || '' : '',
    expertGroupUuid: selectedMode.value === 'expert_group' ? selectedTarget.value || '' : '',
    modelName: selectedMode.value === 'cli' ? selectedModelName.value
      : selectedMode.value === 'vibe_coding' ? vibeModels.value[selectedTarget.value || ''] || '' : '',
  }
}

async function confirm() {
  if (!canConfirm.value || saving.value || !selectedMode.value) return
  if (selectedMode.value === 'vibe_coding' && selectedTarget.value && supportsVibeModel(selectedTarget.value)
    && (props.selectOnly || props.initialMode !== 'vibe_coding' || props.initialVibeTool !== selectedTarget.value)) {
    const tool = selectedTarget.value
    saving.value = true
    try {
      await resolveVibeModel(tool)
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('workflows.task.assign.vibeModelLoadFailed'))
      return
    } finally {
      saving.value = false
    }
    if (!props.open || selectedMode.value !== 'vibe_coding' || selectedTarget.value !== tool) return
  }
  if (props.intent === 'create' || props.selectOnly) {
    if (selectedMode.value === 'vibe_coding' && selectedTarget.value) writeRecentVibeTool(selectedTarget.value)
    const selection = currentSelection()
    if (!selection) return
    if (props.selectOnly) emit('selected', selection)
    else emit('create', selection)
    emit('update:open', false)
    return
  }
  if (!props.taskUuid) return
  saving.value = true
  let shouldOpenVibe = false
  // 需求 2204（评论 5）：指派给 CLI 后跳到对话界面，由对话页自动向 CLI
  // 发送 task.md 引用与起始提示词，不再预填输入框。
  let shouldOpenCliConversation = false
  let result: ExecutionModeAssignmentResult | undefined
  let successKey: string
  try {
    if (selectedMode.value === 'pipeline') {
      result = await apiClient.put<ExecutionModeAssignmentResult>(`/tasks/${encodeURIComponent(props.taskUuid)}/execution-mode`, {
        mode: 'pipeline',
        pipeline_uuid: selectedTarget.value,
        start: !props.replaceExisting,
        replace_existing: props.replaceExisting,
      })
      successKey = props.replaceExisting ? 'workflows.task.assign.switchSuccess' : 'workflows.task.assign.pipelineAssigned'
    } else if (selectedMode.value === 'expert_group') {
      result = await apiClient.put<ExecutionModeAssignmentResult>(`/tasks/${encodeURIComponent(props.taskUuid)}/execution-mode`, {
        mode: 'expert_group',
        expert_group_uuid: selectedTarget.value,
        start: !props.replaceExisting,
        replace_existing: props.replaceExisting,
      })
      successKey = props.replaceExisting ? 'workflows.task.assign.switchSuccess' : 'expertGroups.ready'
    } else if (selectedMode.value === 'cli') {
      result = await apiClient.put<ExecutionModeAssignmentResult>(`/tasks/${encodeURIComponent(props.taskUuid)}/execution-mode`, {
        mode: 'cli',
        tool: selectedTarget.value,
        model_name: selectedModelName.value,
        start: !props.replaceExisting,
        replace_existing: props.replaceExisting,
      })
      successKey = props.replaceExisting ? 'workflows.task.assign.switchSuccess' : 'workflows.task.assign.cliAssigned'
      shouldOpenCliConversation = true
    } else {
      result = await apiClient.put<ExecutionModeAssignmentResult>(`/tasks/${encodeURIComponent(props.taskUuid)}/execution-mode`, {
        mode: 'vibe_coding',
        tool: selectedTarget.value,
        model_name: vibeModels.value[selectedTarget.value || ''] || '',
        start: props.replaceExisting || props.mode === 'board',
        replace_existing: props.replaceExisting,
      })
      shouldOpenVibe = true
      if (selectedTarget.value) writeRecentVibeTool(selectedTarget.value)
      successKey = props.replaceExisting ? 'workflows.task.assign.switchSuccess' : 'workflows.task.assign.codexAssigned'
    }
    if (result?.start_status === 'failed') {
      message.warning(result.start_error || t('workflows.task.assign.switchStartFailed'))
    } else {
      message.success(t(result?.start_status === 'awaiting_message' ? 'layout.sidebar.switchAwaitingMessage' : successKey))
    }
    emit('assigned')
    emit('update:open', false)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.assign.executionFailed'))
  } finally {
    saving.value = false
  }
  if (shouldOpenCliConversation) {
    if (props.replaceExisting) await router.push({ name: 'tasks', query: { taskUuid: props.taskUuid } })
    else await openCliConversation(props.taskUuid)
    return
  }
  if (!shouldOpenVibe) return

  const toolName = t(`workflows.task.assign.vibeTools.${selectedTarget.value || 'codex'}`)
  const openResult = await openTaskVibeTool(props.taskUuid)
  showVibeToolOpenResult(openResult, toolName)
}

watch(
  [selectedMode, selectedTarget],
  ([mode, tool]) => {
    if (!props.open || mode !== 'vibe_coding' || !tool || !supportsVibeModel(tool)) return
    if (!props.selectOnly && props.initialMode === 'vibe_coding' && props.initialVibeTool === tool) return
    void resolveVibeModel(tool).catch(() => {
      // 展开模型菜单或确认时展示加载错误，保留偏好以便重试。
    })
  },
)

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    vibeExpanded.value = false
    pipelineExpanded.value = false
    expertExpanded.value = false
    recentVibeTool.value = readRecentVibeTool()
    selectedMode.value = props.initialMode || undefined
    selectedTarget.value =
      props.initialMode === 'vibe_coding'
        ? (isVibeCodingTool(props.initialVibeTool) ? props.initialVibeTool : 'codex')
        : props.initialMode === 'cli'
          ? props.initialCliType || undefined
          : undefined
    selectedModelName.value = props.initialMode === 'cli' ? props.initialModelName : ''
    resetVibeModels(props.initialMode === 'vibe_coding' ? props.initialVibeTool : '', props.initialModelName)
    if (selectedMode.value === 'vibe_coding' && selectedTarget.value
      && (props.selectOnly || props.initialMode !== 'vibe_coding' || props.initialVibeTool !== selectedTarget.value)) {
      void resolveVibeModel(selectedTarget.value).catch(() => {
        // 确认前会再次校验，加载失败时允许从模型菜单重试。
      })
    }
    const [pipelineResult, expertResult, codexResult, cliResult] = await Promise.allSettled([
      pipelineStore.loadPipelines(true),
      expertGroupStore.load(true),
      loadVibeToolsCapability(),
      loadCliOptions(),
    ])
    if (cliResult.status === 'fulfilled' && selectedMode.value === 'cli' && selectedTarget.value) {
      void loadModelOptions(selectedTarget.value)
    }
    if (cliResult.status === 'fulfilled') {
      const cliTypes = cliOptions.value.map((cli) => cli.type)
      if (props.initialCliType && !cliTypes.includes(props.initialCliType)) {
        cliTypes.unshift(props.initialCliType)
      }
      void prefetchModelCounts(cliTypes)
    }
    codexCapability.value = codexResult.status === 'fulfilled'
      ? codexResult.value
      : { available: false, message: t('workflows.task.codex.capabilityUnavailable') }
    if (selectedMode.value === 'vibe_coding' && selectedTarget.value && vibeToolDisabled(selectedTarget.value)) {
      selectedTarget.value = undefined
    }
    if (pipelineResult.status === 'fulfilled') {
      if (selectedMode.value === 'pipeline') {
        selectedTarget.value = configuredPipelines.value.some(
          (pipeline) => pipeline.uuid === props.preferredPipelineUuid,
        ) ? props.preferredPipelineUuid : configuredPipelines.value[0]?.uuid
      }
    } else {
      const error = pipelineResult.reason
      message.warning(error instanceof Error ? error.message : t('workflows.task.assign.loadFailed'))
    }
    if (expertResult.status === 'fulfilled' && selectedMode.value === 'expert_group') {
      selectedTarget.value = expertGroups.value.some(
        (group) => group.ready && group.uuid === props.preferredExpertGroupUuid,
      )
        ? props.preferredExpertGroupUuid
        : expertGroups.value.find((group) => group.ready)?.uuid
    }
    const vibeIndex = vibeTools.findIndex((tool) => tool.id === selectedTarget.value)
    if (selectedMode.value === 'vibe_coding' && vibeIndex >= previewCount) vibeExpanded.value = true
    if (selectedMode.value === 'pipeline') {
      const pipelineIndex = pipelines.value.findIndex((pipeline) => pipeline.uuid === selectedTarget.value)
      if (pipelineIndex >= previewCount) pipelineExpanded.value = true
    }
    if (selectedMode.value === 'expert_group') {
      const expertIndex = expertGroups.value.findIndex((group) => group.uuid === selectedTarget.value)
      if (expertIndex >= previewCount) expertExpanded.value = true
    }
  },
)
</script>

<style scoped>
.assign-banner {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 16px;
  padding: 10px 12px;
  border-radius: 8px;
  background: #f4f6ff;
  color: #3a4559;
  font-size: 14px;
  line-height: 22px;
}

.assign-banner p {
  margin: 0;
}

.assign-banner :deep(.anticon) {
  margin-top: 3px;
  color: #3157e2;
}

.assign-banner strong {
  font-weight: 600;
}

.assign-switch-warning {
  display: flex;
  margin: -4px 0 16px;
  align-items: flex-start;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 8px;
  color: #ad6800;
  background: #fffbe6;
  font-size: 13px;
  line-height: 20px;
}

.assign-mode-fieldset {
  min-width: 0;
  margin: 0;
  border: 0;
  padding: 0;
}

.assign-section + .assign-section {
  margin-top: 20px;
}

.assign-section__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.assign-section__title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.assign-section__logo {
  width: 20px;
  height: 20px;
}

.assign-section__icon {
  color: #595959;
  font-size: 16px;
}

.assign-section__hint {
  margin: 2px 0 0;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.assign-section__toggle {
  flex: 0 0 auto;
  border: 0;
  padding: 0;
  background: transparent;
  color: #3157e2;
  font-size: 14px;
  line-height: 22px;
  cursor: pointer;
}

.assign-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-top: 12px;
}

.assign-grid__cell,
.assign-choice {
  min-width: 0;
}

.assign-grid__cell .assign-choice {
  width: 100%;
}

.assign-choice {
  display: flex;
  min-width: 0;
  min-height: 40px;
  align-items: center;
  gap: 8px;
  border: 1px solid #d9d9d9;
  border-radius: 8px;
  padding: 8px 12px;
  background: #fff;
  cursor: pointer;
}

.assign-choice__body {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.assign-choice.is-selected {
  border-color: #3157e2;
  background: #f4f7ff;
}

.assign-choice.is-disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.assign-choice__input {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
}

.assign-choice__logo,
.assign-choice__avatar,
.assign-choice__avatar img {
  width: 20px;
  height: 20px;
  flex: 0 0 auto;
  border-radius: 50%;
  object-fit: cover;
}

/* 手动工具直接用官方应用图标（自带圆角方形或透明底），不能再被裁成圆形。 */
.assign-choice__logo.is-brand-icon {
  border-radius: 5px;
}

.assign-choice__avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  background: #f5f5f5;
  color: #595959;
  font-size: 12px;
}

.assign-choice__name {
  min-width: 0;
  overflow: hidden;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.assign-choice__recent {
  flex: 0 0 auto;
  border-radius: 4px;
  padding: 0 6px;
  background: #f0f0f0;
  color: #3a4559;
  font-size: 12px;
  line-height: 20px;
}

.assign-choice__check {
  margin-left: auto;
  color: #3157e2;
  font-size: 16px;
}

.assign-choice__radio {
  width: 16px;
  height: 16px;
  margin-left: auto;
  flex: 0 0 auto;
  border: 1px solid #d9d9d9;
  border-radius: 50%;
  background: #fff;
}

.assign-choice__config {
  flex: 0 0 auto;
  border: 0;
  padding: 0;
  background: transparent;
  color: #3157e2;
  cursor: pointer;
}

.assign-runtime {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-top: 12px;
}

.assign-cli-option {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.assign-cli-option__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.assign-cli-option__count {
  flex: 0 0 auto;
  color: #7a8699;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 20px;
}

.assign-model-option {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.field-help {
  display: flex;
  margin: 8px 0 0;
  align-items: center;
  gap: 8px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
}

@media (max-width: 640px) {
  .assign-grid,
  .assign-runtime {
    grid-template-columns: 1fr;
  }
}
</style>

<style>
.assign-execution-modal .ant-modal {
  max-width: calc(100vw - 24px);
}

.assign-execution-modal .ant-modal-content {
  border-radius: 24px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.16);
  padding: 0;
}

.assign-execution-modal .ant-modal-header {
  margin: 0;
  border-bottom: 0;
  padding: 24px 24px 0;
}

.assign-execution-modal .ant-modal-title {
  color: #262626;
  font-size: 24px;
  font-weight: 600;
  line-height: 32px;
}

.assign-execution-modal .ant-modal-body {
  max-height: calc(100vh - 180px);
  overflow: auto;
  padding: 16px 24px 24px;
}

.assign-execution-modal .ant-modal-footer {
  margin: 0;
  border-top: 1px solid #d9d9d9;
  padding: 16px 24px;
}
</style>
