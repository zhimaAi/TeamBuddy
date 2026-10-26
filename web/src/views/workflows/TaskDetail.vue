<template>
  <div
    class="task-detail-page"
    :class="{ embedded }"
  >
    <TaskWorkspaceHeader
      :task="task"
      :status="taskStatus"
      :show-back="!embedded"
      :changing-status="changingStatus"
      :starting="starting"
      :deleting="deleting"
      :opening-terminal="openingTerminal"
      :original-loading="originalLoading"
      :has-requirement-update="hasRequirementUpdate"
      @back="returnToBoard"
      @status="changeStatus"
      @start="startTask"
      @original="openOriginalRequirement"
      @details="openTaskDetails(false)"
      @terminal="openTaskTerminal"
      @delete="deleteTask"
      @renamed="handleTitleRenamed"
      @changed="handleAssigned"
    />
    <TaskExecutionOverview
      v-if="task"
      :task="task"
      :selected-step-uuid="selectedStepUuid"
      :current-step-uuid="effectiveCurrentStepUuid"
      @select="selectStep"
      @assign="openExecutionModeModal(Boolean(task.execution_mode))"
    />

    <div
      v-if="task"
      class="detail-scroll"
    >
      <div class="detail-content">
        <template v-if="hasPipelineSnapshot || isVibeCoding || isExpertGroup || isCLI">
          <NextStepButton
            v-if="!isVibeCoding && !isExpertGroup && !isCLI && showNextStepCard"
            :next-step-name="nextStepName"
            :is-last-step="isLastStep"
            :disabled="!canComplete"
            :completing="completing"
            @confirm="completeStep"
          />
          <TaskDescriptionPreview :task="task" @edit="openTaskDetails(true)" @documents="openTaskDocuments" @preview-image="showImagePreview" />
          <TaskExecutionHistory
            :task-uuid="resolvedTaskUuid"
            :revision="task.updated_at"
            :file-roots="conversationFileRoots"
            @resync="load(true)"
          />
          <TaskActivityHeading v-model:open="cliActivityExpanded" :task="task" :selected-step-name="selectedStep?.name" />
          <StepMessageList
            ref="messageListRef"
            v-show="!isCLI || cliActivityExpanded"
            :items="displayedProgress"
            :steps="isVibeCoding || isCLI ? [] : sortedSteps"
            :highlight-uuid="highlightUuid"
            :loading="loading"
            :selected-step-name="
              isVibeCoding
                ? vibeCodingToolName
                : isCLI
                  ? cliRuntimeLabel
                  : isExpertGroup
                    ? task.expert_group_name_snapshot || t('agents.expertTeam')
                    : selectedStep?.name || ''
            "
            :fallback-actor-name="
              isVibeCoding
                ? vibeCodingToolName
                : isCLI
                  ? task.execution_tool || t('workflows.task.assign.cliMode')
                  : ''
            "
            :fallback-actor-logo="
              isVibeCoding ? vibeCodingActorLogo : isCLI ? cliExecutionLogo : ''
            "
            :task-uuid="resolvedTaskUuid"
            :file-roots="conversationFileRoots"
            @copy="copyResult"
            @resync="load(true)"
          />
        </template>
        <div
          v-else
          class="pipeline-empty"
        >
          <div class="pipeline-empty-icon">
            <img
              src="@/assets/icons/task-unassigned-pipeline.png"
              alt=""
              aria-hidden="true"
            />
          </div>
          <p>{{ t('workflows.task.common.unassignedExecution') }}</p>
          <span>{{ t('workflows.task.detail.unassignedDescription') }}</span>
        </div>
      </div>
    </div>
    <div
      v-else-if="loading"
      class="detail-loading"
    >
      <a-spin />
    </div>
    <a-empty
      v-else
      class="detail-empty"
      :description="t('workflows.task.detail.notFound')"
    />

    <VibeCodingConversationNotice
      v-if="task && isVibeCoding"
      :tool-name="vibeCodingToolName"
      :tool-id="task?.execution_tool || ''"
      :task-uuid="resolvedTaskUuid || task.uuid"
      :show-open-button="isVibeCoding"
      :open-label="t('workflows.task.detail.openInTool', { tool: vibeCodingToolName })"
      :opening="codexBusy"
      @open="handleOpenCodex"
    />

    <ChatComposer
      ref="composerRef"
      v-else-if="task && hasPipelineSnapshot"
      v-model="question"
      :can-ask="canAsk"
      :submitting="submitting || completing"
      :running="Boolean(activeSelectedProgress)"
      :stopping="stoppingSessionUuid === activeSelectedProgress?.session_uuid"
      :cli-type="composerCliType"
      :model-name="composerModelName"
      :start-mode="selectedStepAwaitingStart"
      :advance-action="composerAdvanceAction"
      :placeholder="composerPlaceholder"
      :context-text="composerContextText"
      :task-uuid="resolvedTaskUuid"
      :draft-key="composerDraftIdentity"
      :allow-draft="hasPipelineSnapshot"
      :notice="pipelineConversation.notice.value"
      :locate-step-uuid="hasPipelineSnapshot && activeTaskProgress?.task_step_uuid !== selectedStepUuid ? activeTaskProgress?.task_step_uuid : undefined"
      :current-step="selectedStep"
      :steps="sortedSteps"
      :executing-step-uuid="activeTaskProgress?.task_step_uuid || effectiveCurrentStepUuid"
      @submit="submitQuestion"
      @advance="completeStep"
      @stop="stopSelectedConversation"
      @prompt-saved="load"
      :execution-task="task"
      @switch-execution="openExecutionModeModal(Boolean(task.execution_mode))"
      @select-step="selectStep"
    />
    <ChatComposer
      ref="expertComposerRef"
      v-else-if="task && isExpertGroup"
      v-model="question"
      :can-ask="!activeExpertProgress"
      :submitting="submitting"
      :running="Boolean(activeExpertProgress)"
      :stopping="stoppingSessionUuid === activeExpertProgress?.session_uuid"
      :placeholder="
        activeExpertProgress
          ? t('workflows.task.feedback.agentRunning')
          : t('workflows.task.feedback.mentionPlaceholder')
      "
      :context-text="t('agents.expertTeam')"
      :task-uuid="resolvedTaskUuid"
      :expert-members="sortedSteps"
      :document-step="currentStep"
      :execution-task="task"
      @switch-execution="openExecutionModeModal(true)"
      @select-step="selectStep"
      @submit="submitExpertMessage"
      @stop="stopExpertConversation"
    />
    <ChatComposer
      v-else-if="task && isCLI"
      ref="composerRef"
      v-model="question"
      :can-ask="cliCanAsk"
      :submitting="submitting"
      :running="Boolean(activeCLIProgress)"
      :stopping="stoppingSessionUuid === activeCLIProgress?.session_uuid"
      :placeholder="
        activeCLIProgress
          ? t('workflows.task.feedback.agentRunning')
          : t('workflows.task.detail.cliMessagePlaceholder')
      "
      :context-text="cliRuntimeLabel"
      :task-uuid="resolvedTaskUuid"
      :current-step="currentStep"
      :document-step="currentStep"
      :hide-agent-prompt="true"
      :execution-task="task"
      @switch-execution="openExecutionModeModal(true)"
      @submit="submitCLIQuestion"
      @stop="stopCLIConversation"
    />

    <StopExecutionConfirmModal
      :open="stopConfirmOpen"
      :loading="Boolean(stoppingSessionUuid)"
      @close="closeStopConfirm"
      @confirm="confirmStopConversation"
    />

    <TaskDetailInfoModal
      v-model:open="detailModalOpen"
      :task="task"
      :status="taskStatus"
      :rendered-description="renderedDescription"
      :initial-editing="detailInitiallyEditing"
      @preview-image="showImagePreview"
      @saved="handleTaskSaved"
    />

    <WorkItemDetailModal
      v-model:open="originalOpen"
      :item="originalItem"
    />

    <TaskImagePreviewModal
      v-model:open="previewImageVisible"
      :image-url="previewImageUrl"
    />

    <!--
      任务详情标题旁的「产出文档」入口：有输入框的形态（流水线 / CLI / 专家团）
      复用 ChatComposer 内的抽屉，保留「引用到输入框」；Vibe Coding 没有输入框，
      这里挂一个独立实例承接入口。
    -->
    <TaskDocumentsDrawer
      v-model:open="standaloneDocumentsOpen"
      :task-uuid="resolvedTaskUuid"
      :current-step="currentStep"
      :reference-enabled="false"
    />

    <TaskExecutionModeDialog v-if="task" v-model:open="assignModalOpen" :task="task" :replace-existing="assignReplaceExisting" @assigned="handleAssigned" />
  </div>
</template>

<script setup lang="ts">
import { showVibeToolOpenResult } from '@/composables/useVibeToolFeedback'
import TaskActivityHeading from '@/components/task-progress/TaskActivityHeading.vue'
import TaskWorkspaceHeader from '@/components/task-progress/TaskWorkspaceHeader.vue'
import TaskExecutionOverview from '@/components/task-progress/TaskExecutionOverview.vue'
import TaskDescriptionPreview from '@/components/task-progress/TaskDescriptionPreview.vue'
import TaskDocumentsDrawer from '@/components/task-progress/TaskDocumentsDrawer.vue'
import TaskExecutionModeDialog from '@/components/task-progress/TaskExecutionModeDialog.vue'
import { useTaskWorkspaceControls } from '@/composables/useTaskWorkspaceControls'

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import apiClient from '@/api/client'
import cliExecutionLogo from '@/assets/icons/task-composer-cli.svg'
import MarkdownIt from 'markdown-it'
import ChatComposer from '@/components/task-progress/ChatComposer.vue'
import NextStepButton from '@/components/task-progress/NextStepButton.vue'
import StepMessageList from '@/components/task-progress/StepMessageList.vue'
import StopExecutionConfirmModal from '@/components/task-progress/StopExecutionConfirmModal.vue'
import TaskExecutionHistory from '@/components/task-progress/TaskExecutionHistory.vue'
import VibeCodingConversationNotice from '@/components/task-progress/VibeCodingConversationNotice.vue'
import { isUserMessage, resultText, userMessageText } from '@/components/task-progress/utils'
import { copyText } from '@/utils/clipboard'
import { taskFileRoots } from '@/utils/localFileLink'
import { isStopConfirmSuppressed, suppressStopConfirm } from '@/utils/stopConfirm'
import { useDocumentTitle } from '@/composables/useDocumentTitle'
import { useComposerDraft, readComposerDraft, writeComposerDraft } from '@/composables/useComposerDraft'
import { usePipelineStepConversation } from '@/composables/usePipelineStepConversation'
import { useLocalWS, useLocalWSStatus } from '@/composables/useLocalWebSocket'
import { openTaskVibeTool } from '@/composables/useTaskVibeTool'
import { vibeToolDisplayLabel, vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import type { TaskProgress } from '@/types/pipeline'
import type { ChatComposerSubmission } from '@/types/task-attachments'
import type { TaskWithDetails } from '@/types/task-detail'
import TaskDetailInfoModal from '@/views/workflows/components/TaskDetailInfoModal.vue'
import TaskImagePreviewModal from '@/views/workflows/components/TaskImagePreviewModal.vue'
import WorkItemDetailModal from '@/views/workflows/components/WorkItemDetailModal.vue'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

const route = useRoute()
const router = useRouter()
const props = withDefaults(
  defineProps<{ taskUuid?: string; embedded?: boolean; autoStart?: boolean }>(),
  { embedded: false, autoStart: false },
)
const emit = defineEmits<{ close: []; changed: [] }>()
const resolvedTaskUuid = computed(() => props.taskUuid || String(route.params.taskUuid || ''))
const task = ref<TaskWithDetails>()
const conversationFileRoots = computed(() => taskFileRoots(task.value))
const {
  changingStatus, starting, deleting, openingTerminal, originalLoading, originalOpen, originalItem,
  assignModalOpen, assignReplaceExisting, hasRequirementUpdate,
  changeStatus, startTask, openOriginalRequirement, openTaskTerminal, deleteTask, openExecutionModeModal,
} = useTaskWorkspaceControls(task, {
  refresh: async () => { await load(); emit('changed') },
  deleted: () => { emit('changed'); returnToBoard() },
})
function handleTitleRenamed(title: string) {
  if (task.value) task.value = { ...task.value, title }
  void load()
  emit('changed')
}

const codexBusy = ref(false)
const detailModalOpen = ref(false)
const detailInitiallyEditing = ref(false)
function openTaskDetails(edit = false) {
  detailInitiallyEditing.value = edit
  detailModalOpen.value = true
}

/**
 * 任务详情标题旁的「产出文档」入口。
 * 有输入框的形态交给 ChatComposer 打开它自己的抽屉（保留「引用到输入框」能力）；
 * Vibe Coding 没有输入框，改开页面级独立抽屉。
 */
function openTaskDocuments() {
  const composer = composerRef.value || expertComposerRef.value
  if (composer?.openDocuments()) return
  standaloneDocumentsOpen.value = true
}
const previewImageUrl = ref('')
const previewImageVisible = ref(false)
// 需求设计图：CLI 任务的动态区标题带折叠箭头。
const cliActivityExpanded = ref(true)
const taskTitle = computed(() => task.value?.title)
const taskStatus = computed(() => statusValue(task.value?.status))
const hasPipelineSnapshot = computed(() =>
  Boolean(
    task.value?.execution_mode === 'pipeline' &&
    (task.value?.pipeline_snapshot_uuid || task.value?.steps?.length),
  ),
)
const isVibeCoding = computed(() => task.value?.execution_mode === 'vibe_coding')
const vibeCodingToolName = computed(() => {
  const label = vibeToolDisplayLabel(task.value?.execution_tool || '', t(vibeToolLabelKey(task.value?.execution_tool || '')), task.value?.execution_tool_name)
  return task.value?.execution_model ? `${label} · ${task.value.execution_model}` : label
})
const vibeCodingActorLogo = computed(() => vibeToolLogo(task.value?.execution_tool || ''))
const isExpertGroup = computed(() => task.value?.execution_mode === 'expert_group')
// CLI 直接执行：任务只有一条隐式步骤，CLI 与模型来自用户指派时的选择。
const isCLI = computed(() => task.value?.execution_mode === 'cli')
const cliRuntimeLabel = computed(() => {
  if (!isCLI.value) return ''
  const cli = task.value?.execution_tool || ''
  const model = task.value?.execution_model || sortedSteps.value[0]?.model_name || ''
  return model ? `${cli} · ${model}` : cli
})
useDocumentTitle(taskTitle)

// 轮询、步骤选择和会话展示共享进度数据，由详情容器统一维护以避免重复请求。
const loading = ref(false)
const submitting = ref(false)
const completing = ref(false)
const stoppingSessionUuid = ref('')
const progress = ref<TaskProgress[]>([])
const selectedStepUuid = ref('')
const pipelineConversation = usePipelineStepConversation(task, progress, selectedStepUuid, resolvedTaskUuid)
const composerDraftIdentity = pipelineConversation.draftKey
const activeTaskProgress = pipelineConversation.activeTaskProgress
const question = useComposerDraft(composerDraftIdentity)
const highlightUuid = ref('')
const messageListRef = ref<InstanceType<typeof StepMessageList>>()
const stopConfirmOpen = ref(false)
const stopConfirmSessionUuid = ref('')
const composerRef = ref<InstanceType<typeof ChatComposer>>()
const expertComposerRef = ref<InstanceType<typeof ChatComposer>>()
// 任务详情标题旁的产出文档入口：只在没有输入框的形态（Vibe Coding）下使用独立抽屉
const standaloneDocumentsOpen = ref(false)
let refreshTimer: ReturnType<typeof setTimeout> | undefined
let highlightTimer: ReturnType<typeof setTimeout> | undefined
let loadVersion = 0
const wsConnected = useLocalWSStatus()

const sortedSteps = computed(() =>
  [...(task.value?.steps || [])].sort((a, b) => a.sort_order - b.sort_order),
)
const effectiveCurrentStepUuid = computed(
  () =>
    task.value?.current_step_uuid ||
    sortedSteps.value.find((step) => step.status === 'active')?.uuid ||
    sortedSteps.value[0]?.uuid ||
    '',
)
const currentStep = computed(() =>
  sortedSteps.value.find((step) => step.uuid === effectiveCurrentStepUuid.value),
)
const currentStepIndex = computed(() =>
  sortedSteps.value.findIndex((step) => step.uuid === effectiveCurrentStepUuid.value),
)
const selectedStep = computed(() =>
  sortedSteps.value.find((step) => step.uuid === selectedStepUuid.value),
)
const selectedStepIndex = computed(() =>
  sortedSteps.value.findIndex((step) => step.uuid === selectedStepUuid.value),
)
const selectedProgress = computed(() =>
  progress.value.filter((item) => item.task_step_uuid === selectedStepUuid.value),
)
const displayedProgress = computed(() =>
  isVibeCoding.value || isExpertGroup.value || isCLI.value
    ? progress.value
    : selectedProgress.value,
)
const activeExpertProgress = computed(() =>
  [...progress.value]
    .reverse()
    .find((item) => !isUserMessage(item) && ['created', 'running'].includes(item.status)),
)
// CLI 直接执行与专家团一致：动态不按步骤过滤，活动会话取最近一条未结束的记录。
const activeCLIProgress = computed(() =>
  [...progress.value]
    .reverse()
    .find((item) => !isUserMessage(item) && ['created', 'running'].includes(item.status)),
)
const cliCanAsk = computed(() => !activeCLIProgress.value)
const latestSelectedAgentProgress = computed(() =>
  [...selectedProgress.value].reverse().find((item) => !isUserMessage(item)),
)
const activeSelectedProgress = computed(() =>
  [...selectedProgress.value]
    .reverse()
    .find(
      (item) => !isUserMessage(item) && (item.status === 'created' || item.status === 'running'),
    ),
)
const composerCliType = computed(
  () => latestSelectedAgentProgress.value?.cli_type || selectedStep.value?.cli_type || '',
)
const composerModelName = computed(
  () =>
    latestSelectedAgentProgress.value?.model ||
    selectedStep.value?.model_name ||
    selectedStep.value?.model ||
    '',
)
const selectedIsCurrent = computed(() => selectedStepUuid.value === effectiveCurrentStepUuid.value)
const currentStepLocked = computed(
  () =>
    !task.value ||
    task.value.status === 'done' ||
    Boolean(task.value.current_step_completed) ||
    currentStep.value?.status === 'completed',
)
const canAsk = computed(() => pipelineConversation.canSend.value)
const selectedStepAwaitingStart = computed(
  () =>
    selectedIsCurrent.value &&
    !currentStepLocked.value &&
    selectedStep.value?.status === 'active' &&
    selectedProgress.value.length === 0,
)
const canComplete = computed(
  () =>
    selectedIsCurrent.value &&
    !currentStepLocked.value &&
    canAsk.value &&
    !submitting.value &&
    !completing.value &&
    selectedProgress.value.length > 0,
)
const lastProgressTerminal = computed(() => {
  const last = [...selectedProgress.value].reverse().find((item) => !isUserMessage(item))
  return Boolean(last && !['created', 'running'].includes(last.status))
})
const isLastStep = computed(() =>
  Boolean(currentStep.value && sortedSteps.value.at(-1)?.uuid === currentStep.value.uuid),
)
const nextStepName = computed(() =>
  isLastStep.value ? '' : sortedSteps.value[currentStepIndex.value + 1]?.name || '',
)
const showNextStepCard = computed(
  () => selectedIsCurrent.value && !currentStepLocked.value && lastProgressTerminal.value,
)
const composerAdvanceAction = computed(() => {
  if (!hasPipelineSnapshot.value || !showNextStepCard.value) return undefined
  return {
    label: t(isLastStep.value ? 'workflows.task.progress.completeTask' : 'workflows.task.progress.enterNext'),
    disabled: !canComplete.value,
    loading: completing.value,
  }
})
const composerPlaceholder = computed(() => {
  if (pipelineConversation.notice.value) return pipelineConversation.notice.value
  if (activeSelectedProgress.value) return t('workflows.task.detail.stepRunning')
  return t('workflows.task.detail.messagePlaceholder')
})
const composerContextText = computed(() =>
  t('workflows.task.detail.conversationContext', {
    step: selectedStep.value?.name || t('workflows.task.common.currentStep'),
    index: Math.max(selectedStepIndex.value + 1, 1),
  }),
)

function handleTaskSaved() {
  emit('changed')
  void load()
}

function returnToBoard() {
  if (props.embedded) emit('close')
  else if (route.query.from === 'team-work') void router.push('/board?tab=team')
  else void router.push('/board')
}

function statusValue(status?: string) {
  if (['todo', 'pending'].includes(status || '')) return 'pending'
  if (['active', 'running', 'developing', 'developed', 'in_progress'].includes(status || ''))
    return 'in_progress'
  if (['done', 'completed'].includes(status || '')) return 'done'
  return status || 'pending'
}

async function handleOpenCodex() {
  if (!task.value || codexBusy.value) return
  codexBusy.value = true
  try {
    const openResult = await openTaskVibeTool(resolvedTaskUuid.value)
    const toolName = vibeCodingToolName.value
    showVibeToolOpenResult(openResult, toolName)
    await load(true)
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.detail.codexOpenFailed'),
    )
  } finally {
    codexBusy.value = false
  }
}

function handleAssigned() {
  void load()
  emit('changed')
}

const md = new MarkdownIt({ breaks: true, linkify: true })
const descriptionUsesHtml = computed(() => /<[a-z][\s\S]*>/i.test(task.value?.description || ''))

const renderedDescription = computed(() => {
  const text = task.value?.description
  if (!text) return ''
  // 兼容接口可能返回的历史 HTML 描述；纯文本和 Markdown 仍统一渲染。
  if (descriptionUsesHtml.value) return text
  return md.render(text)
})

async function load(silent = false) {
  const taskUuid = resolvedTaskUuid.value
  if (!taskUuid) return
  const requestVersion = ++loadVersion
  if (!silent) loading.value = true
  if (refreshTimer) clearTimeout(refreshTimer)
  try {
    const [loadedTask, loadedProgress] = await Promise.all([
      apiClient.get<TaskWithDetails>(`/tasks/${encodeURIComponent(taskUuid)}`),
      apiClient.get<{ items: TaskProgress[] } | TaskProgress[]>(
        `/tasks/${encodeURIComponent(taskUuid)}/progress`,
      ),
    ])
    if (requestVersion !== loadVersion || resolvedTaskUuid.value !== taskUuid) return
    task.value = loadedTask
    const progressItems = Array.isArray(loadedProgress)
      ? loadedProgress
      : loadedProgress.items || []
    progress.value = [...progressItems].sort((a, b) => a.created_at - b.created_at)
    if (
      !selectedStepUuid.value ||
      !loadedTask.steps.some((step) => step.uuid === selectedStepUuid.value)
    ) {
      const latestProgressStep = progress.value.at(-1)?.task_step_uuid || ''
      const validLatestProgressStep =
        latestProgressStep && loadedTask.steps.some((step) => step.uuid === latestProgressStep)
          ? latestProgressStep
          : ''
      selectedStepUuid.value =
        validLatestProgressStep ||
        loadedTask.current_step_uuid ||
        loadedTask.steps.find((step) => step.status === 'active')?.uuid ||
        loadedTask.steps[0]?.uuid ||
        ''
    }
  } catch (error) {
    if (!silent && requestVersion === loadVersion)
      message.error(
        error instanceof Error ? error.message : t('workflows.task.feedback.progressLoadFailed'),
      )
  } finally {
    if (requestVersion === loadVersion) {
      loading.value = false
      scheduleRefresh()
    }
  }
}

// WS 连接正常时进度刷新依赖 task.changed 推送，轮询仅在断线期间兜底，避免长任务持续请求 /progress。
function scheduleRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer)
  if (
    wsConnected.value ||
    !progress.value.some(
      (item) => !isUserMessage(item) && (item.status === 'created' || item.status === 'running'),
    )
  ) {
    return
  }
  refreshTimer = setTimeout(() => {
    void load(true)
  }, 2000)
}

useLocalWS('task.changed', (data: { task_uuid?: string; deleted?: boolean }) => {
  if (data.task_uuid === resolvedTaskUuid.value) {
    if (data.deleted) {
      returnToBoard()
      return
    }
    void load(true)
    emit('changed')
  }
})

watch(wsConnected, (connected) => {
  if (connected) {
    // 重连成功后补偿拉取一次，随后继续依赖推送刷新
    void load(true)
  } else {
    scheduleRefresh()
  }
})

async function locateTarget() {
  await nextTick()
  const last = progress.value
    .filter((item) => item.task_step_uuid === selectedStepUuid.value)
    .at(-1)
  if (last) messageListRef.value?.scrollToItem(last.uuid)
}

function selectStep(uuid: string) {
  selectedStepUuid.value = uuid
  const last = progress.value.filter((item) => item.task_step_uuid === uuid).at(-1)
  if (last) flashTarget(last.uuid)
}

function flashTarget(uuid: string) {
  highlightUuid.value = uuid
  if (highlightTimer) clearTimeout(highlightTimer)
  highlightTimer = setTimeout(() => {
    highlightUuid.value = ''
  }, 1200)
}

function stopSelectedConversation() {
  const sessionUuid = activeSelectedProgress.value?.session_uuid
  if (!sessionUuid || stoppingSessionUuid.value) {
    if (!sessionUuid) message.warning(t('workflows.task.feedback.sessionNotFound'))
    return
  }
  if (isStopConfirmSuppressed()) {
    void stopConversation(sessionUuid)
    return
  }
  stopConfirmSessionUuid.value = sessionUuid
  stopConfirmOpen.value = true
}

function closeStopConfirm() {
  stopConfirmOpen.value = false
  stopConfirmSessionUuid.value = ''
}

function confirmStopConversation(suppressFutureConfirm: boolean) {
  const sessionUuid = stopConfirmSessionUuid.value
  if (suppressFutureConfirm) suppressStopConfirm()
  if (sessionUuid) void stopConversation(sessionUuid)
}

async function stopConversation(sessionUuid: string) {
  stoppingSessionUuid.value = sessionUuid
  try {
    await apiClient.post(`/tasks/sessions/${encodeURIComponent(sessionUuid)}/stop`, {})
    message.success(t('workflows.task.feedback.stopped'))
    await load()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.feedback.stopFailed'))
  } finally {
    stoppingSessionUuid.value = ''
    closeStopConfirm()
  }
}

async function submitQuestion(submission: ChatComposerSubmission) {
  const text = submission.content.trim()
  const step = selectedStep.value
  if (!text || !step || !canAsk.value || submitting.value) return
  const startingStep = hasPipelineSnapshot.value ? pipelineConversation.firstRun.value : selectedStepAwaitingStart.value
  const taskUuid = resolvedTaskUuid.value
  const draftKey = composerDraftIdentity.value
  const draftText = question.value
  submitting.value = true
  try {
    const path = startingStep
      ? `/tasks/${encodeURIComponent(taskUuid)}/steps/${encodeURIComponent(step.uuid)}/runs`
      : `/tasks/${encodeURIComponent(taskUuid)}/steps/${encodeURIComponent(step.uuid)}/questions`
    await apiClient.post(path, {
      question: text,
      display_question: submission.display_content?.trim() || text,
      request_id: crypto.randomUUID(),
      cli_type: submission.config.cli_type,
      model_name: submission.config.model_name,
    })
    if (readComposerDraft(draftKey) === draftText) writeComposerDraft(draftKey, '')
    composerRef.value?.resetAfterSubmit(draftKey)
    message.success(
      startingStep
        ? t('workflows.task.feedback.agentStarted')
        : t('workflows.task.feedback.messageSent'),
    )
    if (resolvedTaskUuid.value === taskUuid) {
      await load()
      if (selectedStepUuid.value === step.uuid) await locateTarget()
    }
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.messageFailed'),
    )
  } finally {
    submitting.value = false
  }
}

async function submitExpertMessage(submission: ChatComposerSubmission) {
  const text = submission.content.trim()
  if (!text || submitting.value || activeExpertProgress.value) return
  submitting.value = true
  try {
    await apiClient.post(`/tasks/${resolvedTaskUuid.value}/expert-messages`, {
      content: text,
      display_content: submission.display_content?.trim() || text,
      member_uuid: submission.member_uuid,
      request_id: crypto.randomUUID(),
    })
    question.value = ''
    expertComposerRef.value?.resetAfterSubmit()
    message.success(t('workflows.task.feedback.messageSent'))
    await load()
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.messageFailed'),
    )
  } finally {
    submitting.value = false
  }
}

function stopExpertConversation() {
  const sessionUUID = activeExpertProgress.value?.session_uuid
  if (sessionUUID) stopSelectedConversationFor(sessionUUID)
}

// CLI 直接执行：首条消息触发 initial_run，后续消息走 questions 续聊。
async function submitCLIQuestion(submission: ChatComposerSubmission) {
  const text = submission.content.trim()
  if (!text || submitting.value || activeCLIProgress.value) return
  const step = sortedSteps.value[0]
  if (!step) return
  const hasConversation = progress.value.some((item) => !isUserMessage(item))
  submitting.value = true
  try {
    const path = hasConversation
      ? `/tasks/${resolvedTaskUuid.value}/steps/${step.uuid}/questions`
      : `/tasks/${resolvedTaskUuid.value}/steps/${step.uuid}/runs`
    await apiClient.post(path, {
      question: text,
      display_question: submission.display_content?.trim() || text,
      request_id: crypto.randomUUID(),
    })
    question.value = ''
    composerRef.value?.resetAfterSubmit()
    message.success(t('workflows.task.feedback.messageSent'))
    await load()
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.messageFailed'),
    )
  } finally {
    submitting.value = false
  }
}

function stopCLIConversation() {
  const sessionUUID = activeCLIProgress.value?.session_uuid
  if (sessionUUID) stopSelectedConversationFor(sessionUUID)
}

function stopSelectedConversationFor(sessionUUID: string) {
  if (isStopConfirmSuppressed()) {
    void stopConversation(sessionUUID)
    return
  }
  stopConfirmSessionUuid.value = sessionUUID
  stopConfirmOpen.value = true
}

async function completeStep() {
  const step = currentStep.value
  if (!step || !canComplete.value || completing.value) return
  completing.value = true
  try {
    await apiClient.post(`/tasks/${resolvedTaskUuid.value}/steps/${step.uuid}/complete`, {
      auto_start: false,
    })
    const wasLastStep = sortedSteps.value.at(-1)?.uuid === step.uuid
    if (wasLastStep) {
      message.success(t('workflows.task.feedback.taskCompleted'))
    } else {
      message.success(t('workflows.task.feedback.nextStepManual'))
    }
    await load()
    const nextCurrent = task.value?.current_step_uuid
    if (nextCurrent && nextCurrent !== selectedStepUuid.value) {
      selectedStepUuid.value = nextCurrent
      await locateTarget()
    }
    if (wasLastStep) emit('changed')
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.nextStepFailed'),
    )
  } finally {
    completing.value = false
  }
}

async function copyResult(item: TaskProgress) {
  try {
    await copyText(isUserMessage(item) ? userMessageText(item) : resultText(item))
    message.success(t('components.feedback.copied'))
  } catch {
    message.warning(t('components.feedback.copyFailed'))
  }
}

function showImagePreview(url: string) {
  previewImageUrl.value = url
  previewImageVisible.value = true
}

watch(
  resolvedTaskUuid,
  () => {
    selectedStepUuid.value = ''
    task.value = undefined
    progress.value = []
    detailModalOpen.value = false
    void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  loadVersion += 1
  if (refreshTimer) clearTimeout(refreshTimer)
  if (highlightTimer) clearTimeout(highlightTimer)
})
</script>

<style scoped>
.task-detail-page {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  background: #fff;
}

.detail-scroll {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  overflow-y: auto;
}

.detail-content {
  flex: 1 0 auto;
  padding: 0 24px 24px;
}

.pipeline-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  padding: 80px 0;
  color: #8c8c8c;
}

.pipeline-empty-icon {
  width: 164px;
  height: 45px;
}

.pipeline-empty-icon img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.pipeline-empty p {
  margin: 16px 0 8px;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.pipeline-empty span {
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.detail-loading,
.detail-empty {
  display: flex;
  min-height: 0;
  flex: 1;
  align-items: center;
  justify-content: center;
}

@media (max-width: 720px) {
  .pipeline-card {
    padding: 12px 16px 8px;
  }

  .detail-content {
    padding: 0 16px 20px;
  }

  .task-title {
    margin-top: 20px;
    font-size: 24px;
    line-height: 32px;
  }
}
</style>
