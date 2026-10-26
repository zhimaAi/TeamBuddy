<template>
  <NewConversation v-if="isNewConversation" />
  <div v-else class="task-page">
    <TaskWorkspaceHeader
      :task="task"
      :status="taskStatus"
      :changing-status="changingStatus"
      :starting="starting"
      :deleting="deleting"
      :opening-terminal="openingTerminal"
      :original-loading="originalLoading"
      :has-requirement-update="hasRequirementUpdate"
      @status="changeStatus"
      @start="startTask"
      @original="openOriginalRequirement"
      @details="openTaskDetails(false)"
      @terminal="openTaskTerminal"
      @delete="deleteTask"
      @renamed="handleTitleRenamed"
      @changed="handleExecutionAssigned"
    >
      <template #actions>
        <a-tooltip
          v-if="selectedTaskUuid"
          :title="workspaceOpen ? t('workflows.task.workspaceFiles.collapse') : t('workflows.task.workspaceFiles.open')"
          placement="bottomRight"
        >
          <button
            type="button"
            class="overview-toggle-button"
            :class="{ active: workspaceOpen }"
            :aria-label="workspaceOpen ? t('workflows.task.workspaceFiles.collapse') : t('workflows.task.workspaceFiles.open')"
            :aria-expanded="workspaceOpen"
            aria-controls="task-workspace-panel"
            @click="toggleWorkspace"
          >
            <FolderOpenOutlined />
          </button>
        </a-tooltip>
      </template>
    </TaskWorkspaceHeader>
    <TaskExecutionOverview
      v-if="task"
      :task="task"
      :selected-step-uuid="selectedStepUuid"
      :current-step-uuid="effectiveCurrentStepUuid"
      @select="selectStep"
      @assign="openExecutionModeModal(Boolean(task.execution_mode))"
    />

    <div
      ref="taskLayoutRef"
      class="task-layout"
      :class="{ 'is-resizing-workspace': workspaceResizing }"
    >
      <main
        class="conversation-main"
        :aria-busy="initialTaskLoading || taskRefreshing"
      >
        <!-- 已归档任务提示条 -->
        <div
          v-if="selectedConversation?.is_archived"
          class="archived-banner"
        >
          <div class="archived-banner-text">
            <span class="archived-badge">{{ t('layout.sidebar.archived') || '已归档' }}</span>
            <span>{{
              t('workflows.task.notifications.archivedTip') ||
              '可继续发送消息，发送后自动取消归档。'
            }}</span>
          </div>
          <button
            type="button"
            class="unarchive-action-btn"
            @click="unarchiveCurrentTask"
          >
            {{ t('layout.sidebar.unarchive') || '取消归档' }}
          </button>
        </div>

        <template v-if="selectedConversation || selectedTaskUuid">
          <div
            v-if="taskError && !task"
            class="main-state main-error"
          >
            <span>{{ taskError }}</span>
            <button
              type="button"
              @click="loadSelectedTask()"
            >
              <ReloadOutlined />{{ t('common.actions.retry') }}
            </button>
          </div>
          <div
            v-else-if="initialTaskLoading || !task"
            class="conversation-loading-shell"
            aria-hidden="true"
          >
            <div class="message-scroll message-loading-skeleton scrollbar--subtle">
              <a-skeleton
                active
                :title="false"
                :paragraph="{ rows: 5 }"
              />
            </div>
            <div class="composer-loading-skeleton">
              <div class="composer-loading-body" />
            </div>
          </div>
          <template
            v-else-if="task && (hasPipelineSnapshot || isVibeCoding || isExpertGroup || isCLI)"
          >
            <NextStepButton
              v-if="!isVibeCoding && !isExpertGroup && !isCLI && showNextStepCard"
              :next-step-name="nextStepName"
              :is-last-step="isLastStep"
              :disabled="!canComplete"
              :completing="completing"
              @confirm="completeStep"
            />
            <div
              class="message-scroll scrollbar--subtle"
              :aria-busy="taskRefreshing"
            >
              <div
                v-if="taskRefreshing"
                class="message-refresh-indicator"
                role="status"
                :aria-label="t('workflows.task.notifications.refreshing')"
              >
                <a-spin
                  :spinning="taskRefreshing"
                  :delay="150"
                  size="small"
                />
              </div>
              <TaskDescriptionPreview :task="task" @edit="openTaskDetails(true)" @documents="openTaskDocuments" @preview-image="showImagePreview" />
              <TaskExecutionHistory
                :task-uuid="selectedTaskUuid"
                :revision="task.updated_at"
                :file-roots="conversationFileRoots"
              />
              <TaskActivityHeading v-model:open="cliActivityExpanded" :task="task" :selected-step-name="selectedStep?.name" />
              <StepMessageList
                v-show="!isCLI || cliActivityExpanded"
				:items="isVibeCoding ? progress : selectedProgress"
				:steps="isVibeCoding ? [] : sortedSteps"
                :highlight-uuid="highlightUuid"
                :selected-step-name="isVibeCoding ? vibeCodingToolName : selectedStep?.name || ''"
                :fallback-actor-name="isVibeCoding ? vibeCodingToolName : ''"
                :fallback-actor-logo="isVibeCoding ? vibeCodingActorLogo : ''"
                :task-uuid="selectedTaskUuid"
                :file-roots="conversationFileRoots"
                @copy="copyResult"
                @resync="loadSelectedTask(true)"
              />
            </div>
            <VibeCodingConversationNotice
              v-if="isVibeCoding"
              :tool-name="vibeCodingToolName"
              :tool-id="task?.execution_tool || ''"
              :task-uuid="selectedTaskUuid"
              show-open-button
              :open-label="t('workflows.task.detail.openInTool', { tool: vibeCodingToolName })"
              :opening="codexBusy"
              @open="handleOpenVibeTool"
            />
            <ChatComposer
              v-else
              ref="composerRef"
              v-model="question"
              :can-ask="canAsk"
              :submitting="submitting || completing"
              :running="Boolean(activeSelectedProgress)"
              :stopping="stoppingSessionUuid === activeSelectedProgress?.session_uuid"
              :cli-type="composerCliType"
              :model-name="composerModelName"
              :start-mode="!isExpertGroup && !isCLI && selectedStepAwaitingStart"
              :advance-action="composerAdvanceAction"
              :placeholder="composerPlaceholder"
              :context-text="composerContextText"
              :task-uuid="selectedTaskUuid"
              :draft-key="composerDraftIdentity"
              :allow-draft="hasPipelineSnapshot"
              :notice="pipelineConversation.notice.value"
              :locate-step-uuid="hasPipelineSnapshot && activeTaskProgress?.task_step_uuid !== selectedStepUuid ? activeTaskProgress?.task_step_uuid : undefined"
			  :current-step="isExpertGroup ? undefined : selectedStep"
			  :document-step="isExpertGroup ? currentStep : selectedStep"
              :steps="sortedSteps"
			  :expert-members="isExpertGroup ? sortedSteps : []"
              :executing-step-uuid="activeTaskProgress?.task_step_uuid || effectiveCurrentStepUuid"
              :hide-agent-prompt="isCLI"
			  @submit="isExpertGroup ? submitExpertMessage($event) : submitQuestion($event)"
              @advance="completeStep"
              @stop="stopSelectedConversation"
              @prompt-saved="loadSelectedTask"
              :execution-task="task"
              @switch-execution="openExecutionModeModal(Boolean(task.execution_mode))"
              @select-step="selectStep"
            />
          </template>
          <div
            v-else-if="task"
            class="main-state"
            :aria-busy="taskRefreshing"
          >
            <div
              v-if="taskRefreshing"
              class="main-refresh-indicator"
              role="status"
              :aria-label="t('workflows.task.notifications.refreshing')"
            >
              <a-spin
                :spinning="taskRefreshing"
                :delay="150"
                size="small"
              />
            </div>
            <FolderOutlined />
            <h3>{{ t('workflows.task.common.unassignedExecution') }}</h3>
            <p>{{ t('workflows.task.notifications.unassignedDescription') }}</p>
          </div>
        </template>
        <div
          v-else
          class="main-state"
        >
          <CheckCircleOutlined />
          <h3>{{ t('workflows.task.notifications.emptyTitle') }}</h3>
          <p>{{ t('workflows.task.notifications.emptyDescription') }}</p>
        </div>
      </main>
      <aside
        v-if="workspaceOpen && selectedTaskUuid"
        id="task-workspace-panel"
        class="workspace-pane"
        :style="{ width: `${workspaceWidth}px`, flexBasis: `${workspaceWidth}px` }"
      >
        <div
          class="workspace-resize-handle"
          role="separator"
          tabindex="0"
          aria-orientation="vertical"
          :aria-label="t('workflows.task.workspaceFiles.resize')"
          :aria-valuemin="260"
          :aria-valuemax="getWorkspaceMaxWidth()"
          :aria-valuenow="workspaceWidth"
          @pointerdown="startWorkspaceResize"
          @keydown="resizeWorkspaceWithKeyboard"
        />
        <WorkspaceFilePanel
          :task-uuid="selectedTaskUuid"
          @close="closeWorkspace"
        />
      </aside>
    </div>

    <div
      v-if="contextTaskUuid"
      id="conversation-context-menu"
      class="context-menu"
      role="menu"
      :style="{ left: `${contextX}px`, top: `${contextY}px` }"
      @click.stop
    >
      <button
        type="button"
        role="menuitem"
        @click="openConversationDetail"
      >
        <img
          :src="contextDetailIcon"
          alt=""
          aria-hidden="true"
        />{{ t('workflows.task.common.details') }}
      </button>
      <button
        type="button"
        role="menuitem"
        @click="toggleRead"
      >
        <img
          :src="contextReadIcon"
          alt=""
          aria-hidden="true"
        />{{
          contextConversation?.unread
            ? t('workflows.task.common.markRead')
            : t('workflows.task.common.markUnread')
        }}
      </button>
      <button
        type="button"
        role="menuitem"
        @click="archiveConversation"
      >
        <img
          :src="contextArchiveIcon"
          alt=""
          aria-hidden="true"
        />{{ t('workflows.task.common.archive') }}
      </button>
    </div>

    <TaskDetailInfoModal
      v-model:open="detailModalOpen"
      :task="task"
      :status="taskStatus"
      :rendered-description="renderedDescription"
      :initial-editing="detailInitiallyEditing"
      @preview-image="showImagePreview"
      @saved="handleTaskSaved"
    />
    <WorkItemDetailModal v-model:open="originalOpen" :item="originalItem" />
    <TaskExecutionModeDialog v-if="task" v-model:open="assignModalOpen" :task="task" :replace-existing="assignReplaceExisting" @assigned="handleExecutionAssigned" />
    <StopExecutionConfirmModal
      :open="stopConfirmOpen"
      :loading="Boolean(stoppingSessionUuid)"
      @close="closeStopConfirm"
      @confirm="confirmStopConversation"
    />
    <TaskImagePreviewModal
      v-model:open="previewImageVisible"
      :image-url="previewImageUrl"
    />
    <!-- 任务详情标题旁的产出文档入口；Vibe Coding 没有输入框时用独立抽屉承接 -->
    <TaskDocumentsDrawer
      v-model:open="standaloneDocumentsOpen"
      :task-uuid="selectedTaskUuid"
      :current-step="currentStep"
      :reference-enabled="false"
    />
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
import WorkItemDetailModal from '@/views/workflows/components/WorkItemDetailModal.vue'
import { useTaskWorkspaceControls } from '@/composables/useTaskWorkspaceControls'

import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRaw, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import NewConversation from './NewConversation.vue'
import { useConversationStore } from '@/stores/conversation'
import { useComposerDraft, readComposerDraft, writeComposerDraft } from '@/composables/useComposerDraft'
import { usePipelineStepConversation } from '@/composables/usePipelineStepConversation'
import {
  CheckCircleOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  ReloadOutlined,
} from '@ant-design/icons-vue'
import MarkdownIt from 'markdown-it'
import apiClient from '@/api/client'
import contextArchiveIcon from '@/assets/icons/task-context-archive.svg'
import contextDetailIcon from '@/assets/icons/task-context-detail.svg'
import contextReadIcon from '@/assets/icons/task-context-read.svg'
import { vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import ChatComposer from '@/components/task-progress/ChatComposer.vue'
import NextStepButton from '@/components/task-progress/NextStepButton.vue'
import StepMessageList from '@/components/task-progress/StepMessageList.vue'
import StopExecutionConfirmModal from '@/components/task-progress/StopExecutionConfirmModal.vue'
import TaskExecutionHistory from '@/components/task-progress/TaskExecutionHistory.vue'
import VibeCodingConversationNotice from '@/components/task-progress/VibeCodingConversationNotice.vue'
import WorkspaceFilePanel from '@/components/workspace/WorkspaceFilePanel.vue'
import { copyText } from '@/utils/clipboard'
import { taskFileRoots } from '@/utils/localFileLink'
import { isStopConfirmSuppressed, suppressStopConfirm } from '@/utils/stopConfirm'
import {
  openTaskVibeTool,
} from '@/composables/useTaskVibeTool'
import { useLocalWS, useLocalWSStatus } from '@/composables/useLocalWebSocket'
import { buildCliKickoffPrompt, pendingCliKickoffUuid } from '@/composables/useCliKickoff'
import { useAppStore } from '@/stores/app'
import type { CompleteStepResponse, TaskNotification, TaskProgress } from '@/types/pipeline'
import type { ChatComposerSubmission } from '@/types/task-attachments'
import { isUserMessage, resultText, userMessageText } from '@/components/task-progress/utils'
import type { TaskWithDetails } from '@/types/task-detail'
import {
  taskConversationCacheScope,
  deleteTaskView,
  deleteTaskViewsOutside,
  readTaskConversationCache,
  saveTaskConversationViewState,
  saveTaskNotificationSnapshot,
  saveTaskView,
} from '@/utils/taskConversationCache'
import TaskDetailInfoModal from '@/views/workflows/components/TaskDetailInfoModal.vue'
import TaskImagePreviewModal from '@/views/workflows/components/TaskImagePreviewModal.vue'
import { useAppI18n } from '@/i18n'

interface TaskConversation {
  task_uuid: string
  title: string
  items: TaskNotification[]
  latest: TaskNotification
  unread: boolean
  is_archived: boolean
}

const appStore = useAppStore()
const route = useRoute()
const router = useRouter()
const { t } = useAppI18n()
const conversationStore = useConversationStore()
const isNewConversation = computed(() => {
  return route.name === 'tasks-new' || !route.query.taskUuid
})
watch(isNewConversation, (isNew, wasNew) => {
  if (wasNew && !isNew) void initialize()
})

interface TaskViewCache {
  task: TaskWithDetails
  progress: TaskProgress[]
}

const notificationsLoading = ref(false)
const notificationsError = ref('')
const notifications = ref<TaskNotification[]>([])
const temporaryNotifications = ref<TaskNotification[]>([])
watch(
  () =>
    [...notifications.value, ...temporaryNotifications.value].filter((item) => !item.is_read)
      .length,
  (count) => appStore.setUnreadTaskNotifications(count),
)
const pendingReadTasks = new Map<string, { isRead: boolean; notificationUuids: Set<string> }>()
const selectedTaskUuid = ref('')
const selectedStepUuid = ref('')
const selectedProgressUuid = ref('')

const taskLoading = ref(false)
const taskError = ref('')
const task = ref<TaskWithDetails>()
const conversationFileRoots = computed(() => taskFileRoots(task.value))
const {
  changingStatus, starting, deleting, openingTerminal, originalLoading, originalOpen, originalItem,
  assignModalOpen, assignReplaceExisting, hasRequirementUpdate,
  changeStatus, startTask, openOriginalRequirement, openTaskTerminal, deleteTask, openExecutionModeModal,
} = useTaskWorkspaceControls(task, {
  refresh: async () => { await Promise.all([loadSelectedTask(), loadNotifications(true), conversationStore.loadConversations(true)]) },
  deleted: async () => {
    selectedTaskUuid.value = ''
    task.value = undefined
    await router.push({ name: 'tasks-new' })
  },
})
function handleTitleRenamed(title: string) {
  if (task.value) task.value = { ...task.value, title }
  void Promise.all([loadSelectedTask(), loadNotifications(true)])
}
async function handleExecutionAssigned() {
  await Promise.all([loadSelectedTask(), loadNotifications(true), conversationStore.loadConversations(true)])
}

const progress = ref<TaskProgress[]>([])
const taskViewCache = new Map<string, TaskViewCache>()
const submitting = ref(false)
const completing = ref(false)
const stoppingSessionUuid = ref('')
const stopConfirmOpen = ref(false)
const stopConfirmSessionUuid = ref('')
const pipelineConversation = usePipelineStepConversation(task, progress, selectedStepUuid, selectedTaskUuid)
const composerDraftIdentity = pipelineConversation.draftKey
const activeTaskProgress = pipelineConversation.activeTaskProgress
const question = useComposerDraft(composerDraftIdentity)
const composerRef = ref<InstanceType<typeof ChatComposer>>()
// 任务详情标题旁的产出文档入口：没有输入框（Vibe Coding）时用独立抽屉
const standaloneDocumentsOpen = ref(false)
const highlightUuid = ref('')
const pipelineExpanded = ref(true)
const cliActivityExpanded = ref(true)
const codexBusy = ref(false)
const taskLayoutRef = ref<HTMLElement>()
const workspaceOpen = ref(localStorage.getItem('teambuddy.workspace-panel.open') !== 'false')
const workspaceWidth = ref(
  clampWorkspaceWidth(Number(localStorage.getItem('teambuddy.workspace-panel.width')) || 300),
)
const workspaceResizing = ref(false)
let workspaceResizeStartX = 0
let workspaceResizeStartWidth = 300

const contextTaskUuid = ref('')
const contextX = ref(0)
const contextY = ref(0)
const detailModalOpen = ref(false)
const detailInitiallyEditing = ref(false)
function openTaskDetails(edit = false) {
  detailInitiallyEditing.value = edit
  detailModalOpen.value = true
}

/**
 * 任务详情标题旁的「产出文档」入口：有输入框时用 ChatComposer 的抽屉（保留引用能力），
 * Vibe Coding 等没有输入框的形态退回页面级独立抽屉。
 */
function openTaskDocuments() {
  if (composerRef.value?.openDocuments()) return
  standaloneDocumentsOpen.value = true
}
const previewImageUrl = ref('')
const previewImageVisible = ref(false)

let refreshTimer: ReturnType<typeof setTimeout> | undefined
let highlightTimer: ReturnType<typeof setTimeout> | undefined
let taskLoadVersion = 0
let notificationsLoadVersion = 0
const cacheScope = taskConversationCacheScope()
let disposed = false
let persistentCacheAvailable = true
let notificationsHydrated = false
let lastNotifiedTaskUuids = new Set<string>()

const wsConnected = useLocalWSStatus()

const conversations = computed<TaskConversation[]>(() => {
  const grouped = new Map<string, TaskNotification[]>()
  for (const item of [...temporaryNotifications.value, ...notifications.value]) {
    const items = grouped.get(item.task_uuid) || []
    items.push(item)
    grouped.set(item.task_uuid, items)
  }
  return [...grouped.entries()]
    .map(([taskUuid, items]) => {
      items.sort((a, b) => b.created_at - a.created_at)
      const latest = items[0]
      return {
        task_uuid: taskUuid,
        title:
          latest.task_title ||
          latest.title ||
          t('workflows.task.feedback.taskFallback', { id: taskUuid.slice(0, 8) }),
        items,
        latest,
        unread: items.some((item) => !item.is_read),
        is_archived: items.some((item) => item.is_archived),
      }
    })
    .sort((a, b) => b.latest.created_at - a.latest.created_at)
})

const selectedConversation = computed(() => {
  const conversation = conversations.value.find((item) => item.task_uuid === selectedTaskUuid.value)
  if (!conversation) return undefined
  const sharedConversation = conversationStore.allConversations.find(
    (item) => item.task_uuid === conversation.task_uuid,
  )
  return {
    ...conversation,
    is_archived: conversation.is_archived || sharedConversation?.is_archived || false,
  }
})
const contextConversation = computed(() =>
  conversations.value.find((item) => item.task_uuid === contextTaskUuid.value),
)
const sortedSteps = computed(() =>
  [...(task.value?.steps || [])].sort((a, b) => a.sort_order - b.sort_order),
)
const hasPipelineSnapshot = computed(() =>
  Boolean(
    task.value?.execution_mode === 'pipeline' &&
    (task.value?.pipeline_snapshot_uuid || sortedSteps.value.length),
  ),
)
const isVibeCoding = computed(() => task.value?.execution_mode === 'vibe_coding')
const vibeCodingToolName = computed(() => t(vibeToolLabelKey(task.value?.execution_tool || '')))
const vibeCodingActorLogo = computed(() => vibeToolLogo(task.value?.execution_tool || ''))
const isExpertGroup = computed(() => task.value?.execution_mode === 'expert_group')
// 需求 2204：CLI 直接执行没有流水线与多 Agent，对话页也要能展示该任务的动态与输入框。
// 需求 2202 评论 4：直接执行 CLI 时输入框不展示「Agent 提示词」入口。
const isCLI = computed(() => task.value?.execution_mode === 'cli')
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
  isExpertGroup.value
    ? progress.value
    : progress.value.filter((item) => item.task_step_uuid === selectedStepUuid.value),
)
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
const initialTaskLoading = computed(() => taskLoading.value && !task.value)
const taskRefreshing = computed(() => taskLoading.value && Boolean(task.value))
const selectedIsCurrent = computed(() => selectedStepUuid.value === effectiveCurrentStepUuid.value)
const currentStepLocked = computed(
  () =>
    !task.value ||
    task.value.status === 'done' ||
    Boolean(task.value.current_step_completed) ||
    currentStep.value?.status === 'completed',
)
const canAsk = computed(() => !taskLoading.value && (
  hasPipelineSnapshot.value ? pipelineConversation.canSend.value : Boolean(selectedStep.value || isExpertGroup.value) && !activeTaskProgress.value
))
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
  if (taskLoading.value) return t('workflows.task.feedback.refreshingWait')
  if (hasPipelineSnapshot.value) {
    if (pipelineConversation.notice.value) return pipelineConversation.notice.value
    return t(activeSelectedProgress.value ? 'workflows.task.detail.stepRunning' : 'workflows.task.detail.messagePlaceholder')
  }
  if (activeSelectedProgress.value) return t('workflows.task.feedback.agentRunning')
  // CLI 直接执行没有多 Agent，用指令式占位文案。
  if (isCLI.value) return t('workflows.task.detail.cliMessagePlaceholder')
  return t('workflows.task.feedback.mentionPlaceholder')
})
const composerContextText = computed(() =>
  t('workflows.task.detail.conversationContext', {
    step: selectedStep.value?.name || 'Agent',
    index: Math.max(selectedStepIndex.value + 1, 1),
  }),
)
const taskStatus = computed(() => statusValue(task.value?.status))
// 详情状态可能来自接口、内存缓存或持久化缓存；无论来源如何，都同步给侧栏，
// 避免任务已经 blocked，但旧的 running 通知仍让侧栏显示 loading。
watch(
  [selectedTaskUuid, () => task.value?.status],
  ([taskUuid, status]) => conversationStore.setTaskStatus(taskUuid, status),
  { immediate: true },
)

const md = new MarkdownIt({ breaks: true, linkify: true })
const renderedDescription = computed(() => {
  const text = task.value?.description
  if (!text) return ''
  // 兼容接口可能返回的历史 HTML 描述；纯文本和 Markdown 仍统一渲染。
  if (/<[a-z][\s\S]*>/i.test(text)) return text
  return md.render(text)
})

function handleTaskSaved() {
  void loadSelectedTask()
}

function statusValue(status?: string) {
  if (['todo', 'pending'].includes(status || '')) return 'pending'
  if (['active', 'running', 'developing', 'developed', 'in_progress'].includes(status || ''))
    return 'in_progress'
  if (['done', 'completed'].includes(status || '')) return 'done'
  return status || 'pending'
}

function routeQueryValue(value: unknown) {
  return typeof value === 'string' ? value : ''
}

// 需求 2204（评论 5 调整后口径）：指派给 CLI 后不再预填输入框，而是任务与隐式
// 步骤就绪后自动向 CLI 发送 task.md 引用与固定提示词。两个触发来源：
// - 对话内新建 CLI 对话：创建任务成功后置 autoKickoffTaskUuid；
// - 三条指派链路（新建任务 / 看板弹窗 / 详情指派）：openCliConversation 置
//   pendingCliKickoffUuid（模块级 ref；不能走路由 query，会被 syncConversationRoute 重写掉）。
// 发送复用 submitQuestion 的 runs 链路；意图只消费一次，失败不重试避免重复轰炸。
// 注意：本段引用的 selectedStep / activeSelectedProgress / canAsk 等都在上方定义，
// watch 源数组会立即求值，不能把本段提前到它们之前（会触发 TDZ 错误）。
const autoKickoffTaskUuid = ref('')
const cliKickoffSending = ref(false)
watch(() => task.value?.awaiting_message, waiting => {
  if (!waiting) return
  if (pendingCliKickoffUuid.value === selectedTaskUuid.value) pendingCliKickoffUuid.value = ''
  if (autoKickoffTaskUuid.value === selectedTaskUuid.value) autoKickoffTaskUuid.value = ''
})

const cliKickoffPending = computed(() => {
  const target = selectedTaskUuid.value
  if (!target || task.value?.awaiting_message) return false
  return autoKickoffTaskUuid.value === target || pendingCliKickoffUuid.value === target
})

// 就绪条件：目标任务是 CLI 直接执行、隐式步骤（cli-direct）已加载为当前步骤，
// 且可以发起提问（任务加载完、无进行中会话、非提交中）。
const cliKickoffTaskReady = computed(() => {
  const loaded = task.value
  if (!loaded || loaded.uuid !== selectedTaskUuid.value) return null
  if (loaded.execution_mode !== 'cli') return null
  const step = selectedStep.value
  if (!step || step.step_key !== 'cli-direct') return null
  return loaded
})

watch(
  [cliKickoffPending, cliKickoffTaskReady, canAsk, submitting, activeSelectedProgress],
  async ([pending, loaded, ask, busy, active]) => {
    if (!pending || cliKickoffSending.value) return
    if (!loaded || !ask || busy || active) return
    cliKickoffSending.value = true
    try {
      // 先消费意图再发送：状态回写触发重渲染也不会重复触发。
      autoKickoffTaskUuid.value = ''
      pendingCliKickoffUuid.value = ''
      const prompt = buildCliKickoffPrompt(
        loaded.task_dir || '',
        t('workflows.task.notifications.cliKickoffPrompt'),
      )
      if (!prompt) return
      await submitQuestion({
        content: prompt.content,
        display_content: prompt.content,
        config: {
          cli_type: loaded.execution_tool || '',
          model_name: loaded.execution_model || '',
        },
      })
    } finally {
      cliKickoffSending.value = false
    }
  },
  { immediate: true },
)

async function syncConversationRoute(taskUuid: string, stepUuid = '') {
  if (
    routeQueryValue(route.query.taskUuid) === taskUuid &&
    routeQueryValue(route.query.stepUuid) === stepUuid
  ) {
    return
  }
  await router.replace({
    name: 'tasks',
    query: {
      taskUuid: taskUuid || undefined,
      stepUuid: taskUuid && stepUuid ? stepUuid : undefined,
    },
  })
}

async function selectConversationFromRoute() {
  const routeTaskUuid = routeQueryValue(route.query.taskUuid)
  if (!routeTaskUuid) {
    selectedTaskUuid.value = ''
    selectedStepUuid.value = ''
    selectedProgressUuid.value = ''
    clearSelectedTask()
    return
  }
  // 切换执行方式后会清掉旧动态再跳进对话。此时内存和本地缓存里仍是上一次
  // 已完成的会话；若因为任务和步骤都没变就直接返回，头部会变成「进行中」，
  // 列表和消息却继续显示上一次「执行完成」。
  const kickoffThisTask = pendingCliKickoffUuid.value === routeTaskUuid
  if (kickoffThisTask) {
    taskViewCache.delete(routeTaskUuid)
    if (task.value?.uuid === routeTaskUuid) progress.value = []
    selectedProgressUuid.value = ''
    selectedStepUuid.value = routeQueryValue(route.query.stepUuid)
    await loadNotifications(true)
  }
  let conversation = conversations.value.find((item) => item.task_uuid === routeTaskUuid)
  if (!conversation && !kickoffThisTask) {
    await loadNotifications(true)
    conversation = conversations.value.find((item) => item.task_uuid === routeTaskUuid)
  }
  const routeStepUuid = routeQueryValue(route.query.stepUuid)
  // 指派给 CLI 后跳转过来的任务可能还没有通知记录：直接按路由选中，
  // 否则详情区会停在空白态。
  if (!conversation) {
    if (!kickoffThisTask && selectedTaskUuid.value === routeTaskUuid) return
    selectedTaskUuid.value = routeTaskUuid
    selectedStepUuid.value = routeStepUuid
    selectedProgressUuid.value = ''
    closeContextMenu()
    void persistViewState()
    await loadSelectedTask()
    return
  }
  const nextStepUuid = routeStepUuid || (kickoffThisTask ? '' : conversation.latest.task_step_uuid)
  if (
    !kickoffThisTask &&
    selectedTaskUuid.value === conversation.task_uuid &&
    selectedStepUuid.value === nextStepUuid
  ) {
    return
  }
  selectedTaskUuid.value = conversation.task_uuid
  selectedStepUuid.value = nextStepUuid
  selectedProgressUuid.value =
    kickoffThisTask || routeStepUuid ? '' : conversation.latest.progress_uuid || ''
  closeContextMenu()
  void persistViewState()
  await loadSelectedTask()
}

function showSystemNotification(conversation: TaskConversation) {
  if (typeof window === 'undefined' || typeof Notification === 'undefined') return
  if (Notification.permission !== 'granted') return

  const notification = new Notification(
    t('workflows.task.feedback.readyTitle', {
      step: conversation.latest.step_name || t('workflows.task.common.agentOrchestration'),
    }),
    {
      body: t('workflows.task.feedback.openTask', { task: conversation.title }),
      tag: conversation.task_uuid,
    },
  )
  notification.onclick = () => {
    window.focus()
    void syncConversationRoute(conversation.task_uuid)
  }
}

async function ensureNotificationPermission() {
  if (typeof window === 'undefined' || typeof Notification === 'undefined') return
  if (Notification.permission !== 'default') return
  try {
    await Notification.requestPermission()
  } catch {
    // 通知权限申请失败不影响任务页面主流程
  }
}

function clearRefreshTimer() {
  if (!refreshTimer) return
  clearTimeout(refreshTimer)
  refreshTimer = undefined
}

// WS 连接正常时进度刷新依赖 task.changed 推送，轮询仅在断线期间兜底，避免长任务持续请求 /progress。
function scheduleRefresh() {
  clearRefreshTimer()
  if (
    wsConnected.value ||
    !progress.value.some(
      (item) => !isUserMessage(item) && (item.status === 'created' || item.status === 'running'),
    )
  ) {
    return
  }
  refreshTimer = setTimeout(() => {
    void loadSelectedTask(true)
  }, 2000)
}

function clearSelectedTask() {
  taskLoadVersion += 1
  clearRefreshTimer()
  task.value = undefined
  progress.value = []
  selectedStepUuid.value = ''
  selectedProgressUuid.value = ''
  taskError.value = ''
  taskLoading.value = false
}

function resolveSelectedStepUuid(
  loadedTask: TaskWithDetails,
  loadedProgress: TaskProgress[],
  preferredStepUuid: string,
) {
  if (preferredStepUuid && loadedTask.steps.some((step) => step.uuid === preferredStepUuid)) {
    return preferredStepUuid
  }
  const latestProgressStep = loadedProgress.at(-1)?.task_step_uuid || ''
  const validLatestProgressStep =
    latestProgressStep && loadedTask.steps.some((step) => step.uuid === latestProgressStep)
      ? latestProgressStep
      : ''
  return (
    validLatestProgressStep ||
    loadedTask.current_step_uuid ||
    loadedTask.steps.find((step) => step.status === 'active')?.uuid ||
    loadedTask.steps[0]?.uuid ||
    ''
  )
}

function restoreCachedTaskView(taskUuid: string) {
  const cached = taskViewCache.get(taskUuid)
  if (!cached) {
    task.value = undefined
    progress.value = []
    return false
  }
  task.value = cached.task
  conversationStore.setTaskStatus(taskUuid, cached.task.status)
  progress.value = cached.progress
  selectedStepUuid.value = resolveSelectedStepUuid(
    cached.task,
    cached.progress,
    selectedStepUuid.value,
  )
  return true
}

function reportCacheFailure(action: string, error: unknown) {
  persistentCacheAvailable = false
  const reason = error instanceof Error ? error.message : String(error)
  console.warn(`[task-conversation-cache] ${action}失败: ${reason}`)
}

function currentNotificationSnapshot() {
  const uniqueItems = new Map<string, TaskNotification>()
  for (const item of [...toRaw(temporaryNotifications.value), ...toRaw(notifications.value)]) {
    uniqueItems.set(item.uuid, item)
  }
  return [...uniqueItems.values()]
}

async function persistNotificationSnapshot() {
  if (!persistentCacheAvailable || disposed) return
  try {
    await saveTaskNotificationSnapshot(currentNotificationSnapshot(), cacheScope)
  } catch (error) {
    reportCacheFailure('保存通知快照', error)
  }
}

async function persistViewState() {
  if (!persistentCacheAvailable || disposed) return
  try {
    await saveTaskConversationViewState(selectedTaskUuid.value, pipelineExpanded.value, cacheScope)
  } catch (error) {
    reportCacheFailure('保存页面状态', error)
  }
}

async function persistTaskView(
  taskUuid: string,
  loadedTask: TaskWithDetails,
  loadedProgress: TaskProgress[],
) {
  if (!persistentCacheAvailable || disposed) return
  try {
    await saveTaskView(taskUuid, loadedTask, loadedProgress, cacheScope)
  } catch (error) {
    reportCacheFailure('保存会话详情', error)
  }
}

async function removePersistentTaskView(taskUuid: string) {
  if (!persistentCacheAvailable || disposed) return
  try {
    await deleteTaskView(taskUuid, cacheScope)
  } catch (error) {
    reportCacheFailure('删除会话详情', error)
  }
}

async function reconcileTaskViewCache(validTaskUuids: Set<string>) {
  for (const taskUuid of taskViewCache.keys()) {
    if (!validTaskUuids.has(taskUuid)) taskViewCache.delete(taskUuid)
  }
  if (!persistentCacheAvailable || disposed) return
  try {
    await deleteTaskViewsOutside(validTaskUuids, cacheScope)
  } catch (error) {
    reportCacheFailure('清理失效会话', error)
  }
}

async function restorePersistentTaskConversationCache() {
  try {
    const cached = await readTaskConversationCache(cacheScope)
    if (disposed) return false
    notifications.value = cached.notifications
    temporaryNotifications.value = []
    taskViewCache.clear()
    cached.taskViews.forEach((item) => {
      taskViewCache.set(item.taskUuid, { task: item.task, progress: item.progress })
    })
    pipelineExpanded.value = cached.pipelineExpanded
    if (!conversations.value.length) return false

    const selectedConversationFromCache =
      conversations.value.find((item) => item.task_uuid === cached.lastSelectedTaskUuid) ||
      conversations.value[0]
    selectedTaskUuid.value = selectedConversationFromCache.task_uuid
    selectedStepUuid.value = selectedConversationFromCache.latest.task_step_uuid
    selectedProgressUuid.value = selectedConversationFromCache.latest.progress_uuid || ''
    restoreCachedTaskView(selectedConversationFromCache.task_uuid)
    void persistViewState()
    return true
  } catch (error) {
    reportCacheFailure('读取本地缓存', error)
    return false
  }
}

async function loadNotifications(preserveSelection = true) {
  const requestVersion = ++notificationsLoadVersion
  const hadCachedNotifications = conversations.value.length > 0
  notificationsLoading.value = true
  notificationsError.value = ''
  try {
    const result = await apiClient.get<{ items: TaskNotification[] }>('/notifications', {
      include_archived: 1,
    })
    if (requestVersion !== notificationsLoadVersion) return
    const loadedNotifications = result.items || []
    // 读取列表可能与点击消红点并发，只覆盖本次操作时已存在的通知，
    // 新到达的通知仍保留服务端的未读状态。
    for (const item of loadedNotifications) {
      const pending = pendingReadTasks.get(item.task_uuid)
      if (pending?.notificationUuids.has(item.uuid)) item.is_read = pending.isRead
    }
    const previousTaskUuids = lastNotifiedTaskUuids
    notifications.value = loadedNotifications
    temporaryNotifications.value = temporaryNotifications.value.filter(
      (temporary) => !loadedNotifications.some((item) => item.task_uuid === temporary.task_uuid),
    )
    const currentTaskUuids = new Set(loadedNotifications.map((item) => item.task_uuid))
    if (notificationsHydrated) {
      for (const conversation of conversations.value) {
        if (
          conversation.unread &&
          !previousTaskUuids.has(conversation.task_uuid) &&
          currentTaskUuids.has(conversation.task_uuid)
        ) {
          showSystemNotification(conversation)
        }
      }
    }
    lastNotifiedTaskUuids = currentTaskUuids
    notificationsHydrated = true
    const previousSelectedTaskUuid = selectedTaskUuid.value
    const previousSelectedStepUuid = selectedStepUuid.value
    const routeTaskUuid = routeQueryValue(route.query.taskUuid)
    const routeStepUuid = routeQueryValue(route.query.stepUuid)
    const routeConversation = conversations.value.find((item) => item.task_uuid === routeTaskUuid)
    if (routeConversation) {
      selectedTaskUuid.value = routeConversation.task_uuid
      selectedStepUuid.value = routeStepUuid || routeConversation.latest.task_step_uuid
      selectedProgressUuid.value = routeStepUuid ? '' : routeConversation.latest.progress_uuid || ''
    } else if (
      !preserveSelection ||
      !conversations.value.some((item) => item.task_uuid === selectedTaskUuid.value)
    ) {
      if (routeTaskUuid) {
        const first = conversations.value[0]
        selectedTaskUuid.value = first?.task_uuid || ''
        selectedStepUuid.value = first?.latest.task_step_uuid || ''
        selectedProgressUuid.value = first?.latest.progress_uuid || ''
      } else {
        selectedTaskUuid.value = ''
        selectedStepUuid.value = ''
        selectedProgressUuid.value = ''
      }
    }
    const selectionChanged = previousSelectedTaskUuid !== selectedTaskUuid.value
    const selectedStepChanged = previousSelectedStepUuid !== selectedStepUuid.value
    if (!conversations.value.length) {
      selectedTaskUuid.value = ''
      clearSelectedTask()
    } else if (selectionChanged && selectedTaskUuid.value) {
      const requestedStepUuid = selectedStepUuid.value
      clearSelectedTask()
      selectedStepUuid.value = requestedStepUuid
      restoreCachedTaskView(selectedTaskUuid.value)
    }

    const validTaskUuids = new Set(conversations.value.map((item) => item.task_uuid))
    await Promise.all([
      persistNotificationSnapshot(),
      persistViewState(),
      reconcileTaskViewCache(validTaskUuids),
    ])
    if ((selectionChanged || selectedStepChanged) && selectedTaskUuid.value) {
      void loadSelectedTask()
    }
  } catch (error) {
    if (requestVersion !== notificationsLoadVersion) return
    if (hadCachedNotifications || conversations.value.length) {
      message.warning(t('workflows.task.feedback.cachedConversations'))
    } else {
      notificationsError.value =
        error instanceof Error ? error.message : t('workflows.task.feedback.conversationLoadFailed')
      message.error(notificationsError.value)
    }
  } finally {
    if (requestVersion === notificationsLoadVersion) notificationsLoading.value = false
  }
}

async function loadSelectedTask(silent = false) {
  const taskUuid = selectedTaskUuid.value
  if (!taskUuid) return
  const requestVersion = ++taskLoadVersion
  clearRefreshTimer()
  if (!silent) {
    if (task.value?.uuid !== taskUuid) {
      const restoredFromCache = restoreCachedTaskView(taskUuid)
      if (restoredFromCache) void locateTarget('auto')
    }
    taskLoading.value = true
    taskError.value = ''
  }
  try {
    const [loadedTask, loadedProgress] = await Promise.all([
      apiClient.get<TaskWithDetails>(`/tasks/${taskUuid}`),
      apiClient.get<{ items: TaskProgress[] } | TaskProgress[]>(`/tasks/${taskUuid}/progress`),
    ])
    if (requestVersion !== taskLoadVersion || taskUuid !== selectedTaskUuid.value) return
    const progressItems = Array.isArray(loadedProgress)
      ? loadedProgress
      : loadedProgress.items || []
    const sortedProgress = [...progressItems].sort((a, b) => a.created_at - b.created_at)
    const nextSelectedStepUuid = resolveSelectedStepUuid(
      loadedTask,
      sortedProgress,
      selectedStepUuid.value,
    )
    taskViewCache.set(taskUuid, { task: loadedTask, progress: sortedProgress })
    conversationStore.setTaskStatus(taskUuid, loadedTask.status)
    task.value = loadedTask
    progress.value = sortedProgress
    selectedStepUuid.value = nextSelectedStepUuid
    if (
      routeQueryValue(route.query.taskUuid) === taskUuid &&
      routeQueryValue(route.query.stepUuid) &&
      routeQueryValue(route.query.stepUuid) !== nextSelectedStepUuid
    ) {
      await syncConversationRoute(taskUuid, nextSelectedStepUuid)
    }
    taskError.value = ''
    void persistTaskView(taskUuid, loadedTask, sortedProgress)
    await locateTarget('auto')
  } catch (error) {
    if (requestVersion === taskLoadVersion && !silent) {
      if (task.value?.uuid === taskUuid) {
        taskError.value = ''
        message.warning(t('workflows.task.feedback.cachedProgress'))
      } else {
        taskError.value =
          error instanceof Error ? error.message : t('workflows.task.feedback.progressLoadFailed')
        message.error(taskError.value)
      }
    }
  } finally {
    if (requestVersion === taskLoadVersion) {
      taskLoading.value = false
      scheduleRefresh()
    }
  }
}

async function initialize() {
  notificationsLoading.value = true
  const restoredFromCache = await restorePersistentTaskConversationCache()
  if (disposed) return
  if (selectedTaskUuid.value) void loadSelectedTask()
  await loadNotifications(restoredFromCache)
  if (disposed) return
  // 直接打开带 taskUuid 的链接（指派 CLI 后的跳转就是这种）时路由 watcher 不会触发，
  // 这里补一次路由选中，避免停在空白态。
  await selectConversationFromRoute()
}

async function setTaskRead(taskUuid: string, isRead: boolean) {
  const conversation = conversations.value.find((item) => item.task_uuid === taskUuid)
  if (!conversation || pendingReadTasks.has(taskUuid)) return
  const previous = new Map(conversation.items.map((item) => [item.uuid, item.is_read]))
  pendingReadTasks.set(taskUuid, { isRead, notificationUuids: new Set(previous.keys()) })
  conversation.items.forEach((item) => {
    item.is_read = isRead
  })
  try {
    await conversationStore.setTaskRead(taskUuid, isRead)
  } catch (error) {
    for (const item of [...notifications.value, ...temporaryNotifications.value]) {
      if (previous.has(item.uuid)) item.is_read = previous.get(item.uuid)!
    }
    message.error(error instanceof Error ? error.message : t('workflows.task.feedback.readFailed'))
  } finally {
    pendingReadTasks.delete(taskUuid)
    // 完成写入后重新读取，同时使写入前发出的列表请求失效。
    await loadNotifications(true)
  }
}

async function selectConversation(conversation: TaskConversation, markRead = true) {
  if (markRead && conversation.unread) void setTaskRead(conversation.task_uuid, true)
  selectedTaskUuid.value = conversation.task_uuid
  selectedStepUuid.value = conversation.latest.task_step_uuid
  selectedProgressUuid.value = conversation.latest.progress_uuid || ''
  void syncConversationRoute(conversation.task_uuid, selectedStepUuid.value)
  closeContextMenu()
  void persistViewState()
  await loadSelectedTask()
}

async function selectStep(uuid: string) {
  selectedStepUuid.value = uuid
  selectedProgressUuid.value = ''
  void syncConversationRoute(selectedTaskUuid.value, uuid)
  await locateTarget('smooth')
  const last = progress.value.filter((item) => item.task_step_uuid === uuid).at(-1)
  if (last) flashTarget(last.uuid)
}

async function locateTarget(behavior: 'auto' | 'smooth' = 'auto') {
  await nextTick()
  const requested = selectedProgressUuid.value
    ? progress.value.find((item) => item.uuid === selectedProgressUuid.value)
    : undefined
  const target =
    requested ||
    progress.value.filter((item) => item.task_step_uuid === selectedStepUuid.value).at(-1)
  if (!target) return
  const container = document.querySelector<HTMLElement>('.message-scroll')
  const targetElement = document.getElementById(`progress-${target.uuid}`)
  if (!container || !targetElement || !container.contains(targetElement)) return
  const containerRect = container.getBoundingClientRect()
  const targetRect = targetElement.getBoundingClientRect()
  const centeredOffset = Math.max((container.clientHeight - targetRect.height) / 2, 0)
  const top = container.scrollTop + targetRect.top - containerRect.top - centeredOffset
  container.scrollTo({ top: Math.max(top, 0), behavior })
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
    await Promise.all([loadNotifications(true), loadSelectedTask()])
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
  const taskUuid = selectedTaskUuid.value
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
    if (selectedTaskUuid.value === taskUuid && selectedStepUuid.value === step.uuid) selectedProgressUuid.value = ''
    message.success(
      startingStep
        ? t('workflows.task.feedback.agentStarted')
        : t('workflows.task.feedback.messageSent'),
    )
    await unarchiveConversationIfNeeded(taskUuid)
    await setTaskRead(taskUuid, true)
    await Promise.all([loadNotifications(true), loadSelectedTask()])
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.messageFailed'),
    )
  } finally {
    submitting.value = false
  }
}

async function completeStep() {
  const step = currentStep.value
  if (!step || !canComplete.value || completing.value) return
  completing.value = true
  try {
    const result = await apiClient.post<CompleteStepResponse>(
      `/tasks/${selectedTaskUuid.value}/steps/${step.uuid}/complete`,
      { auto_start: false },
    )
    const wasLastStep = sortedSteps.value.at(-1)?.uuid === step.uuid
    selectedProgressUuid.value = ''
    if (!wasLastStep && result.next_step_uuid) {
      selectedStepUuid.value = result.next_step_uuid
      await syncConversationRoute(selectedTaskUuid.value, result.next_step_uuid)
    }
    if (wasLastStep) {
      message.success(t('workflows.task.feedback.taskCompleted'))
    } else {
      message.success(t('workflows.task.feedback.nextStepManual'))
    }
    await setTaskRead(selectedTaskUuid.value, true)
    await Promise.all([loadNotifications(true), loadSelectedTask()])
    const nextCurrent = task.value?.current_step_uuid
    if (nextCurrent && nextCurrent !== selectedStepUuid.value) {
      selectedStepUuid.value = nextCurrent
      await syncConversationRoute(selectedTaskUuid.value, nextCurrent)
      await locateTarget()
    }
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

function closeContextMenu() {
  contextTaskUuid.value = ''
}

function getWorkspaceMaxWidth() {
  const availableWidth = taskLayoutRef.value?.clientWidth || window.innerWidth
  return Math.max(260, Math.floor(availableWidth / 2))
}

function clampWorkspaceWidth(width: number) {
  return Math.min(getWorkspaceMaxWidth(), Math.max(260, Math.round(width)))
}

function toggleWorkspace() {
  workspaceOpen.value = !workspaceOpen.value
  localStorage.setItem('teambuddy.workspace-panel.open', String(workspaceOpen.value))
}

function closeWorkspace() {
  workspaceOpen.value = false
  localStorage.setItem('teambuddy.workspace-panel.open', 'false')
}

function startWorkspaceResize(event: PointerEvent) {
  if (event.button !== 0 || window.matchMedia('(max-width: 1000px)').matches) return
  event.preventDefault()
  workspaceResizing.value = true
  workspaceResizeStartX = event.clientX
  workspaceResizeStartWidth = workspaceWidth.value
  window.addEventListener('pointermove', moveWorkspaceResize)
  window.addEventListener('pointerup', finishWorkspaceResize, { once: true })
}

function moveWorkspaceResize(event: PointerEvent) {
  if (!workspaceResizing.value) return
  workspaceWidth.value = clampWorkspaceWidth(
    workspaceResizeStartWidth + workspaceResizeStartX - event.clientX,
  )
}

function finishWorkspaceResize() {
  if (!workspaceResizing.value) return
  workspaceResizing.value = false
  window.removeEventListener('pointermove', moveWorkspaceResize)
  localStorage.setItem('teambuddy.workspace-panel.width', String(workspaceWidth.value))
}

function resizeWorkspaceWithKeyboard(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return
  event.preventDefault()
  const delta = event.key === 'ArrowLeft' ? 16 : -16
  workspaceWidth.value = clampWorkspaceWidth(workspaceWidth.value + delta)
  localStorage.setItem('teambuddy.workspace-panel.width', String(workspaceWidth.value))
}

async function handleOpenVibeTool() {
  if (!selectedTaskUuid.value || !isVibeCoding.value || codexBusy.value) return
  const toolName = vibeCodingToolName.value
  codexBusy.value = true
  try {
    await unarchiveConversationIfNeeded(selectedTaskUuid.value)
    const openResult = await openTaskVibeTool(selectedTaskUuid.value)
    showVibeToolOpenResult(openResult, toolName)
  } catch (error) {
    message.error(
      error instanceof Error
        ? error.message
        : t('workflows.task.detail.toolOpenFailed', { tool: toolName }),
    )
  } finally {
    codexBusy.value = false
  }
}

async function openConversationDetail() {
  const conversation = contextConversation.value
  if (!conversation) return
  if (selectedTaskUuid.value !== conversation.task_uuid) {
    await selectConversation(conversation)
  } else {
    closeContextMenu()
    if (task.value?.uuid !== conversation.task_uuid) await loadSelectedTask()
  }
  if (task.value?.uuid === conversation.task_uuid) openTaskDetails(false)
}

async function toggleRead() {
  const conversation = contextConversation.value
  if (!conversation) return
  await setTaskRead(conversation.task_uuid, conversation.unread)
  closeContextMenu()
}

async function unarchiveConversationIfNeeded(taskUuid: string) {
  const isArchived =
    selectedConversation.value?.task_uuid === taskUuid
      ? selectedConversation.value.is_archived
      : conversationStore.allConversations.find((item) => item.task_uuid === taskUuid)?.is_archived
  if (!isArchived) return
  await conversationStore.archiveTask(taskUuid, false)
  for (const item of [...notifications.value, ...temporaryNotifications.value]) {
    if (item.task_uuid === taskUuid) item.is_archived = false
  }
}

async function unarchiveCurrentTask() {
  if (!selectedTaskUuid.value) return
  try {
    await unarchiveConversationIfNeeded(selectedTaskUuid.value)
    message.success(t('layout.sidebar.unarchive') || '已取消归档')
    await Promise.all([
      conversationStore.loadConversations(true),
      loadNotifications(true),
      loadSelectedTask(),
    ])
  } catch (err) {
    message.error(err instanceof Error ? err.message : '操作失败')
  }
}

async function archiveConversation() {
  const conversation = contextConversation.value
  if (!conversation) return
  const taskUuid = conversation.task_uuid
  try {
    await conversationStore.archiveTask(taskUuid, true)
    taskViewCache.delete(taskUuid)
    await Promise.all([
      conversationStore.loadConversations(true),
      loadNotifications(true),
      removePersistentTaskView(taskUuid),
      persistNotificationSnapshot(),
    ])
    if (selectedTaskUuid.value === taskUuid) {
      selectedTaskUuid.value = ''
      clearSelectedTask()
      await syncConversationRoute('')
      await persistViewState()
    }
    message.success(t('workflows.task.feedback.archived'))
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.archiveFailed'),
    )
  } finally {
    closeContextMenu()
  }
}

async function submitExpertMessage(submission: ChatComposerSubmission) {
  const text = submission.content.trim()
  if (!text || submitting.value || activeSelectedProgress.value) return
  submitting.value = true
  try {
    await apiClient.post(`/tasks/${selectedTaskUuid.value}/expert-messages`, {
      content: text,
      display_content: submission.display_content?.trim() || text,
      member_uuid: submission.member_uuid,
      request_id: crypto.randomUUID(),
    })
    await unarchiveConversationIfNeeded(selectedTaskUuid.value)
    question.value = ''
    composerRef.value?.resetAfterSubmit()
    message.success(t('workflows.task.feedback.messageSent'))
    await Promise.all([loadNotifications(true), loadSelectedTask()])
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.feedback.messageFailed'),
    )
  } finally {
    submitting.value = false
  }
}

function showImagePreview(url: string) {
  previewImageUrl.value = url
  previewImageVisible.value = true
}

function handleDocumentKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  closeContextMenu()
}

useLocalWS('task.changed', (data: { task_uuid?: string; deleted?: boolean }) => {
  if (isNewConversation.value) return
  if (data.deleted && data.task_uuid) {
    const taskUuid = data.task_uuid
    notificationsLoadVersion += 1
    notifications.value = notifications.value.filter((item) => item.task_uuid !== taskUuid)
    temporaryNotifications.value = temporaryNotifications.value.filter(
      (item) => item.task_uuid !== taskUuid,
    )
    taskViewCache.delete(taskUuid)
    void removePersistentTaskView(taskUuid)
    if (selectedTaskUuid.value === taskUuid) {
      selectedTaskUuid.value = ''
      clearSelectedTask()
      void router.replace({ name: 'tasks-new' })
      void persistViewState()
    }
  }
  void loadNotifications(true)
  if (!data.deleted && data.task_uuid === selectedTaskUuid.value) void loadSelectedTask(true)
})

watch(wsConnected, (connected) => {
  if (connected) {
    // 重连成功后补偿拉取一次，随后继续依赖推送刷新
    if (selectedTaskUuid.value) void loadSelectedTask(true)
  } else {
    scheduleRefresh()
  }
})

watch(
  () => [routeQueryValue(route.query.taskUuid), routeQueryValue(route.query.stepUuid)] as const,
  () => {
    void selectConversationFromRoute()
  },
)

// 已经停在同一个对话路由时，router.push 不会触发上面的监听。
// 切换执行方式后再次进来，仍要丢掉旧会话并拉起这一次执行。
watch(pendingCliKickoffUuid, (taskUuid) => {
  if (!taskUuid || routeQueryValue(route.query.taskUuid) !== taskUuid) return
  void selectConversationFromRoute()
})

onMounted(() => {
  document.addEventListener('click', closeContextMenu)
  document.addEventListener('keydown', handleDocumentKeydown)
  void ensureNotificationPermission()
  if (!isNewConversation.value) void initialize()
})

onBeforeUnmount(() => {
  disposed = true
  finishWorkspaceResize()
  window.removeEventListener('pointerup', finishWorkspaceResize)
  taskLoadVersion += 1
  notificationsLoadVersion += 1
  clearRefreshTimer()
  if (highlightTimer) clearTimeout(highlightTimer)
  document.removeEventListener('click', closeContextMenu)
  document.removeEventListener('keydown', handleDocumentKeydown)
})
</script>

<style scoped>
.task-page {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  background: #fff;
}

.page-titlebar {
  display: flex;
  min-height: 44px;
  flex: 0 0 44px;
  align-items: center;
  justify-content: flex-start;
  gap: 12px;
  padding: 0 24px;
  border-bottom: 1px solid #f0f0f0;
  background: #fff;
}

.page-titlebar-copy {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 12px;
}

.page-titlebar-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  margin-left: auto;
  gap: 8px;
}

.page-titlebar strong {
  color: #262626;
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
}

.page-titlebar p {
  overflow: hidden;
  margin: 0;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.overview-toggle-button {
  display: inline-flex;
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 6px;
  background: transparent;
  cursor: pointer;
  transition: background-color 180ms ease;
}

.overview-toggle-button:hover {
  background: #e4e6eb;
}

.overview-toggle-button:active,
.overview-toggle-button.active {
  color: #3157e2;
  background: #eaf0ff;
}

.overview-toggle-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.overview-toggle-button img {
  display: block;
  width: 16px;
  height: 16px;
}

.task-overview {
  position: relative;
  flex: 0 0 auto;
  height: 140px;
  padding: 24px 24px 0 24px;
  overflow: hidden;
  box-sizing: border-box;
  border-bottom: 1px solid #d9d9d9;
  background: #fff;
}

.task-overview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 22px;
}

.task-overview-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.task-overview-title h2 {
  overflow: hidden;
  margin: 0;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-status,
.task-priority {
  flex: 0 0 auto;
  border: 1px solid currentColor;
  border-radius: 6px;
  padding: 0px 6px;
  font-size: 12px;
  line-height: 16px;
}

.task-status {
  color: #d97706;
}

.task-status.status-done {
  color: #16a34a;
}

.task-status.status-blocked {
  color: #ef4444;
}

.task-priority {
  color: #ef4444;
}

.sidebar-add-button:focus-visible,
.conversation-select-button:focus-visible,
.conversation-menu-button:focus-visible,
.context-menu button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.task-step-region {
  overflow: hidden;
  height: auto;
  box-sizing: border-box;
}

.task-step-region :deep(.step-strip) {
  padding-bottom: 5px;
}

.step-region-empty {
  padding: 16px 0 20px;
  color: #8c8c8c;
  font-size: 14px;
}

/* 需求设计图：CLI 任务的执行目标是固定文案「直接执行：CLI · 模型」 */
.direct-execution-summary {
  display: flex;
  padding: 8px 0 12px;
  align-items: center;
  gap: 8px;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.direct-execution-summary__logo {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
  object-fit: contain;
}

.direct-execution-summary__label::after {
  content: ':';
}

.direct-execution-summary strong {
  color: #262626;
  font-weight: 500;
}
.expert-overview {
  display: flex;
  align-items: center;
  gap: 16px;
  min-height: 44px;
  padding: 0 24px;
  color: #595959;
}
.expert-overview strong {
  color: #262626;
}
.expert-overview span {
  font-size: 12px;
}

.task-overview-skeleton {
  min-height: 60px;
}

.overview-skeleton-head {
  display: flex;
  min-height: 60px;
  box-sizing: border-box;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 8px 24px 0;
}

.overview-skeleton-title,
.overview-skeleton-steps span,
.composer-loading-body {
  border-radius: 6px;
  background: #f0f1f3;
}

.overview-skeleton-title {
  width: min(360px, 42%);
  height: 28px;
}

.overview-skeleton-steps {
  display: flex;
  min-height: 72px;
  box-sizing: border-box;
  align-items: center;
  gap: 36px;
  padding: 0 24px 8px;
}

.overview-skeleton-steps span {
  width: 155px;
  height: 40px;
}

.task-layout {
  position: relative;
  display: flex;
  min-height: 0;
  flex: 1;
  container-name: task-layout;
  container-type: inline-size;
}

.task-layout.is-resizing-workspace {
  cursor: col-resize;
  user-select: none;
}

.workspace-pane {
  position: relative;
  min-width: 260px;
  max-width: 50%;
  min-height: 0;
  flex: 0 0 300px;
  background: #fff;
}

.workspace-resize-handle {
  position: absolute;
  z-index: 2;
  top: 0;
  bottom: 0;
  left: -6px;
  width: 12px;
  cursor: col-resize;
  touch-action: none;
}

.workspace-resize-handle::after {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 50%;
  width: 1px;
  background: transparent;
  content: '';
  transform: translateX(-50%);
}

.workspace-resize-handle:hover::after,
.workspace-resize-handle:focus-visible::after {
  background: #3157e2;
}

.workspace-resize-handle:focus-visible {
  outline: none;
}

@container task-layout (max-width: 760px) {
  .workspace-pane {
    position: absolute;
    z-index: 20;
    top: 0;
    right: 0;
    bottom: 0;
    width: min(380px, 88cqw) !important;
    max-width: 88cqw;
    box-shadow: -12px 0 32px rgba(15, 23, 42, 0.14);
  }

  .workspace-resize-handle {
    display: none;
  }
}

.task-layout.is-resizing-sidebar {
  cursor: col-resize;
  user-select: none;
}

.conversation-sidebar {
  position: relative;
  display: flex;
  width: 300px;
  min-width: 140px;
  max-width: 500px;
  min-height: 0;
  flex: 0 0 300px;
  flex-direction: column;
  border-right: 1px solid #f0f0f0;
  background: #fff;
}

.sidebar-resize-handle {
  position: absolute;
  z-index: 1;
  top: 0;
  right: -6px;
  bottom: 0;
  width: 12px;
  cursor: col-resize;
  touch-action: none;
}

.sidebar-resize-handle::after {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 50%;
  width: 1px;
  background: transparent;
  content: '';
  transform: translateX(-50%);
}

.sidebar-resize-handle:hover::after,
.sidebar-resize-handle:focus-visible::after,
.is-resizing-sidebar .sidebar-resize-handle::after {
  width: 2px;
  background: #3157e2;
}

.sidebar-resize-handle:focus-visible {
  outline: none;
}

.sidebar-heading {
  position: relative;
  display: flex;
  min-height: 56px;
  flex: 0 0 56px;
  align-items: center;
  gap: 8px;
  padding: 16px 24px;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.sidebar-heading small {
  display: inline-flex;
  width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  border-radius: 11px;
  padding: 0;
  color: #8c8c8c;
  background: #edeff2;
  font-size: 12px;
  font-weight: 400;
  line-height: 14px;
}

.sidebar-add-button {
  appearance: none;
  position: absolute;
  top: 16px;
  right: 24px;
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 0;
  color: #2475fc;
  background: transparent;
  cursor: pointer;
}

.sidebar-add-button:hover,
.sidebar-add-button[aria-expanded='true'] {
  background: rgba(49, 87, 226, 0.08);
}

.pipeline-menu {
  width: 208px;
  overflow: hidden;
  border-radius: 6px;
  padding: 2px;
  background: #fff;
  box-shadow:
    0 8px 10px -5px rgba(0, 0, 0, 0.08),
    0 16px 24px 2px rgba(0, 0, 0, 0.04),
    0 6px 30px 5px rgba(0, 0, 0, 0.05);
}

.pipeline-menu-heading {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px 4px;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.pipeline-menu-list {
  max-height: 280px;
  overflow-y: auto;
  padding-bottom: 2px;
}

.pipeline-menu-item {
  display: flex;
  width: 100%;
  min-height: 32px;
  align-items: center;
  gap: 8px;
  border: 0;
  border-radius: 6px;
  padding: 5px 16px;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
  text-align: left;
}

.pipeline-menu-item:hover:not(:disabled),
.pipeline-menu-item:focus-visible {
  background: #f5f5f5;
}

.pipeline-menu-item:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: -2px;
}

.pipeline-menu-item:disabled {
  color: #bfbfbf;
  cursor: not-allowed;
}

.pipeline-menu-tooltip {
  display: block;
}

.pipeline-menu-item img,
.pipeline-avatar-fallback {
  display: inline-flex;
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  object-fit: cover;
}

.pipeline-avatar-fallback {
  color: #3157e2;
  background: #eef2ff;
  font-size: 10px;
  font-weight: 600;
}

.pipeline-avatar-fallback--cli img {
  width: 14px;
  height: 14px;
}

/* CLI 直接执行的新建对话状态：CLI 与模型两个下拉并排。 */
/* 需求设计图：直接执行分组的 CLI/模型下拉与「创建对话」按钮直接内嵌在菜单里 */
.direct-cli {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 2px 12px 6px;
}

.direct-cli__select {
  width: 100%;
}

.direct-cli__submit {
  margin-top: 2px;
}

.pipeline-menu-heading-logo {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
  object-fit: contain;
}

.cli-runtime-hint--selected {
  margin-top: 4px;
  color: #262626;
}

.cli-runtime-hint {
  margin: 0;
  color: #8c8c8c;
  font-size: 12px;
}

.pipeline-menu-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pipeline-menu-state {
  display: flex;
  min-height: 72px;
  align-items: center;
  justify-content: center;
  padding: 12px;
  color: #8c8c8c;
  font-size: 12px;
  text-align: center;
}

.pipeline-menu-error {
  flex-direction: column;
  gap: 8px;
}

.pipeline-menu-error button,
.sidebar-error button,
.main-error button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 0;
  border-radius: 6px;
  padding: 4px 8px;
  color: #3157e2;
  background: #eef2ff;
  cursor: pointer;
}

.conversation-list {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: 4px;
  overflow-y: scroll;
  padding: 8px 24px;
  scrollbar-gutter: stable;
}

.conversation-item {
  position: relative;
  display: flex;
  width: 100%;
  min-height: 70px;
  align-items: center;
  gap: 10px;
  margin: 0;
  border: 0;
  border-radius: 6px;
  padding: 12px;
  color: inherit;
  background: #fff;
  text-align: left;
}

.conversation-item:hover {
  background: #f2f4f7;
}

.conversation-item.active {
  background: #e5efff;
}

.new-conversation-item {
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  cursor: default;
}

.conversation-select-button {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  border: 0;
  padding: 0;
  color: inherit;
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.conversation-menu-button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 4px;
  color: #000;
  background: transparent;
  cursor: pointer;
  opacity: 0;
  pointer-events: none;
  transition:
    opacity 180ms ease,
    background-color 180ms ease;
}

.conversation-item:hover .conversation-menu-button,
.conversation-item:focus-within .conversation-menu-button,
.conversation-menu-button[aria-expanded='true'] {
  opacity: 1;
  pointer-events: auto;
}

.conversation-menu-button:hover {
  background: #e4e6eb;
}

.conversation-menu-button:active {
  background: #d9dce2;
}

.conversation-menu-button img {
  display: block;
  width: 16px;
  height: 16px;
}

@media (prefers-reduced-motion: reduce) {
  .conversation-menu-button {
    transition: none;
  }
}

.conversation-title {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.conversation-title strong {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: #262626;
  font-size: 14px;
  font-weight: 500;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.conversation-title i {
  width: 8px;
  height: 8px;
  flex: 0 0 8px;
  border-radius: 50%;
  background: #fb363f;
}

.conversation-subtitle {
  width: 100%;
  overflow: hidden;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sidebar-loading,
.sidebar-error,
.sidebar-empty {
  display: flex;
  min-height: 220px;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 8px;
  color: #8c8c8c;
  font-size: 12px;
  text-align: center;
}

.sidebar-empty :deep(svg) {
  color: #cbd5e1;
  font-size: 24px;
}

.conversation-main {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  background: #fff;
}

.conversation-loading-shell {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.message-scroll {
  position: relative;
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0 24px 24px;
}

.message-loading-skeleton {
  padding-top: 32px;
}

.message-loading-skeleton :deep(.ant-skeleton-paragraph) {
  margin: 0;
}

.message-loading-skeleton :deep(.ant-skeleton-paragraph > li) {
  height: 18px;
  margin-top: 22px;
}

.composer-loading-skeleton {
  min-height: 136px;
  flex: 0 0 136px;
  box-sizing: border-box;
  padding: 8px 22px 24px;
  background: #fff;
}

.composer-loading-body {
  height: 94px;
  border: 1px solid #e5e7eb;
  background: #f7f8fa;
}

.message-refresh-indicator {
  position: sticky;
  z-index: 2;
  top: 0;
  display: flex;
  height: 0;
  justify-content: center;
  pointer-events: none;
}

.main-refresh-indicator {
  position: absolute;
  z-index: 2;
  top: 16px;
  left: 50%;
  pointer-events: none;
  transform: translateX(-50%);
}

.message-refresh-indicator :deep(.ant-spin) {
  margin-top: 4px;
  border-radius: 999px;
  padding: 5px 9px;
  background: rgba(255, 255, 255, 0.92);
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.08);
}

.message-scroll :deep(.message-list-card) {
  overflow: visible;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}

.message-scroll :deep(.message-list) {
  padding: 0;
}

.main-state {
  position: relative;
  display: flex;
  min-height: 0;
  flex: 1;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 8px;
  color: #8c8c8c;
  text-align: center;
}

.main-state > :deep(svg) {
  color: #cbd5e1;
  font-size: 34px;
}

.main-state h3 {
  margin: 4px 0 0;
  color: #595959;
  font-size: 14px;
}

.main-state p {
  margin: 0;
  font-size: 12px;
}

.new-conversation-state :deep(.pipeline-flow-icon) {
  width: 34px;
  height: 34px;
  color: #3157e2;
}

.context-menu {
  position: fixed;
  z-index: 1000;
  width: 174px;
  padding: 5px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.14);
}

.context-menu button {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 8px 9px;
  border: 0;
  border-radius: 5px;
  color: #4b5563;
  background: transparent;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
}

.context-menu button:hover {
  background: #f5f6f8;
}

.context-menu button > img {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
}

@media (max-width: 900px) {
  .conversation-sidebar {
    width: 240px;
    flex-basis: 240px;
  }

  .conversation-list,
  .sidebar-heading {
    padding-right: 12px;
    padding-left: 12px;
  }

  .message-scroll {
    padding-right: 20px;
    padding-left: 20px;
  }
}

@media (max-width: 700px) {
  .page-titlebar p {
    display: none;
  }

  .task-overview-head {
    align-items: flex-start;
    flex-direction: column;
    padding-bottom: 8px;
  }

  .task-overview-skeleton,
  .overview-skeleton-head {
    min-height: 88px;
  }

  .task-overview {
    min-height: 160px;
    max-height: 160px;
  }

  .task-overview-enter-from,
  .task-overview-leave-to {
    min-height: 0;
    max-height: 0;
  }

  .overview-skeleton-head {
    align-items: flex-start;
    justify-content: center;
    flex-direction: column;
    gap: 12px;
    padding-bottom: 8px;
  }

  .overview-skeleton-title {
    width: min(320px, 72%);
    height: 24px;
  }

  .task-overview-title h2 {
    font-size: 16px;
    line-height: 24px;
  }

  .task-step-region {
    padding-right: 16px;
    padding-left: 16px;
  }

  .composer-loading-skeleton {
    min-height: 176px;
    flex-basis: 176px;
    padding-right: 16px;
    padding-left: 16px;
  }

  .composer-loading-body {
    height: 134px;
  }

  .conversation-sidebar {
    width: 210px;
    flex-basis: 210px;
  }

  .conversation-item time {
    display: none;
  }

  .conversation-title,
  .conversation-subtitle {
    padding-right: 0;
  }
}

.archived-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 24px;
  background: #fdf6ec;
  border-bottom: 1px solid #faecd8;
  color: #e6a23c;
  font-size: 13px;
  flex-shrink: 0;
}

.archived-banner-text {
  display: flex;
  align-items: center;
  gap: 8px;
}

.archived-badge {
  padding: 1px 6px;
  background: #e6a23c;
  color: #ffffff;
  border-radius: 4px;
  font-size: 11px;
}

.unarchive-action-btn {
  border: none;
  background: transparent;
  color: #3157e2;
  font-size: 13px;
  cursor: pointer;
}

.unarchive-action-btn:hover {
  text-decoration: underline;
}
</style>
