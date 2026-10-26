<template>
  <div class="task-board">
    <button
      v-if="false"
      type="button"
      @click="openSettings"
    >
      {{ t('workflows.board.listSettings') }}
    </button>

    <ASpin v-if="props.viewMode === 'board'" :spinning="loading">
      <div class="lanes scrollbar--subtle">
        <section
          v-for="lane in visibleLanes"
          :key="lane.lane_key"
          class="lane"
          :class="{ 'drag-over': dragOverLane === lane.lane_key }"
          :style="{ '--lane-background': laneBackground(lane.lane_key) }"
          @dragover="onDragOver"
          @dragenter="onDragEnter(lane.lane_key)"
          @dragleave="onDragLeave($event, lane.lane_key)"
          @drop="onDrop($event, lane.lane_key)"
        >
          <BoardLaneHeader
            :title="taskLaneTitle(lane)"
            :color="lane.color"
            :count="grouped[lane.lane_key]?.length || 0"
            :background="laneBackground(lane.lane_key)"
            @set-background="setLaneBackground(lane.lane_key, $event)"
            @reset-background="resetLaneBackground(lane.lane_key)"
          />

          <div class="task-list scrollbar--subtle">
            <TaskBoardCard
              v-for="task in grouped[lane.lane_key]"
              :key="task.uuid"
              :task="task"
              :dragging="draggingUuid === task.uuid"
              :draggable="task.execution_mode !== 'vibe_coding'"
              @drag-start="onDragStart"
              @open="openTask"
            />

            <div
              v-if="(grouped[lane.lane_key]?.length || 0) === 0"
              class="lane-empty"
            >
              <EmptyIssueIcon
                class="empty-icon"
                aria-hidden="true"
              />
              <span class="empty-text">{{ props.searchKeyword.trim() ? t('workflows.board.noMatchingTasks') : t('common.states.noData') }}</span>
            </div>
          </div>
        </section>
      </div>
    </ASpin>

    <TaskListView
      v-else
      v-model:status-filter="statusFilter"
      :rows="listRows"
      :filters="listFilters"
      :filters-ready="lanesLoaded"
      :search-keyword="props.searchKeyword"
      :loading="loading || listLoading"
      :status-counts="listStatusCounts"
      :total="listTotal"
      :page="listPage"
      :page-size="LIST_PAGE_SIZE"
      row-clickable
      @action="handleListAction"
      @row-click="handleListRowClick"
      @page-change="changeListPage"
    />

    <TaskBoardSettingsModal
      v-model:open="settingsVisible"
      :lanes="allLanes"
      :task-view-mode="taskViewMode"
      @lanes-change="handleSettingsLanesChange"
      @saved="handleSettingsSaved"
    />

    <ADrawer
      v-model:open="taskDrawerOpen"
      :title="t('workflows.board.taskDetails')"
      placement="right"
      width="min(1100px, 92vw)"
      :body-style="{ padding: 0, overflow: 'hidden' }"
      :destroy-on-close="true"
      @close="closeTaskPreview"
    >
      <div
        v-if="selectedTaskUuid"
        class="task-preview-content"
      >
        <TaskDetail
          :task-uuid="selectedTaskUuid"
          embedded
          @close="closeTaskPreview"
        />
      </div>
    </ADrawer>

    <AModal
      v-model:open="taskModalOpen"
      :title="t('workflows.board.taskDetails')"
      width="min(1200px, calc(100vw - 48px))"
      :body-style="{ height: '85vh', padding: 0, overflow: 'hidden' }"
      :footer="null"
      :destroy-on-close="true"
      wrap-class-name="task-preview-modal"
      @cancel="closeTaskPreview"
    >
      <div
        v-if="selectedTaskUuid"
        class="task-preview-content"
      >
        <TaskDetail
          :task-uuid="selectedTaskUuid"
          embedded
          @close="closeTaskPreview"
        />
      </div>
    </AModal>

    <CreateLocalTaskModal
      v-model:open="createModalOpen"
      :initial-status="createStatus"
      @created="onTaskCreated"
    />

    <AssignExecutionModeModal
      v-model:open="assignModalOpen"
      :task-uuid="pendingAssignTask?.uuid || ''"
      :task-title="pendingAssignTask?.title || ''"
	  :initial-mode="pendingAssignTask?.execution_mode === 'pipeline' ? 'pipeline' : pendingAssignTask?.execution_mode === 'expert_group' ? 'expert_group' : pendingAssignTask?.execution_mode === 'cli' ? 'cli' : pendingAssignTask?.execution_mode === 'vibe_coding' ? 'vibe_coding' : ''"
      :preferred-pipeline-uuid="pendingAssignTask?.selected_pipeline_uuid || ''"
	  :preferred-expert-group-uuid="pendingAssignTask?.selected_expert_group_uuid || ''"
      :initial-cli-type="pendingAssignTask?.execution_mode === 'cli' ? pendingAssignTask?.execution_tool || '' : ''"
      :initial-vibe-tool="pendingAssignTask?.execution_mode === 'vibe_coding' ? pendingAssignTask?.execution_tool || '' : ''"
      :initial-model-name="pendingAssignTask?.execution_mode === 'cli' ? pendingAssignTask?.execution_model || '' : ''"
      mode="board"
      @assigned="load"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useRouter } from 'vue-router'
import apiClient from '@/api/client'
import AssignExecutionModeModal from '@/components/AssignExecutionModeModal.vue'
import CreateLocalTaskModal from '@/components/CreateLocalTaskModal.vue'
import { useLaneBackgrounds } from '@/composables/useLaneBackgrounds'
import { useTaskExecutionDisplay } from '@/composables/useTaskExecutionDisplay'
import { taskLaneKey, taskLaneTitle } from '@/utils/taskLane'
import type {
  BoardLayoutMode,
  BoardListActionKind,
  BoardListFilter,
  BoardListRow,
  TaskBoardLane,
  TaskBoardTask,
  TaskBoardListResponse,
  TaskLatestActivity,
  TaskViewMode,
} from '@/types/task-board'
import TaskDetail from '@/views/workflows/TaskDetail.vue'
import BoardLaneHeader from './BoardLaneHeader.vue'
import EmptyIssueIcon from './EmptyIssueIcon.vue'
import TaskBoardCard from './TaskBoardCard.vue'
import TaskListView from './TaskListView.vue'
import TaskBoardSettingsModal from './TaskBoardSettingsModal.vue'
import { useAppI18n } from '@/i18n'
import { useLocalWS } from '@/composables/useLocalWebSocket'

const { t } = useAppI18n()
const statusFilter = defineModel<string>('statusFilter', { default: 'active' })
const props = withDefaults(defineProps<{
  viewMode?: BoardLayoutMode
  searchKeyword?: string
}>(), {
  viewMode: 'board',
  searchKeyword: '',
})
const { executionName, executionAvatar } = useTaskExecutionDisplay()

const TASK_VIEW_MODE_STORAGE_KEY = 'goteams.workflows.task-view-mode'

function readTaskViewMode(): TaskViewMode {
  try {
    const storedMode = window.localStorage.getItem(TASK_VIEW_MODE_STORAGE_KEY)
    if (storedMode === 'drawer' || storedMode === 'modal' || storedMode === 'new-window') {
      return storedMode
    }
  } catch {
    // 浏览器禁用本地存储时，仍使用默认查看模式。
  }
  return 'new-window'
}

const router = useRouter()
const loading = ref(false)
const tasks = ref<TaskBoardTask[]>([])
const LIST_PAGE_SIZE = 20
const listTasks = ref<TaskBoardTask[]>([])
const listStatusCounts = ref<Record<string, number>>({})
const listTotal = ref(0)
const listPage = ref(1)
const listLoading = ref(false)
let listRequestVersion = 0
let listSearchTimer: ReturnType<typeof setTimeout> | undefined
let boardSearchTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false
const updating = ref<string>()
const allLanes = ref<TaskBoardLane[]>([])
const lanesLoaded = ref(false)
const taskViewMode = ref<TaskViewMode>(readTaskViewMode())
const selectedTaskUuid = ref('')
const taskDrawerOpen = ref(false)
const taskModalOpen = ref(false)
const assignModalOpen = ref(false)
const pendingAssignTask = ref<TaskBoardTask>()
const draggingUuid = ref<string | null>(null)
const dragOverLane = ref<string | null>(null)
const createModalOpen = ref(false)
const createStatus = ref('')
const settingsVisible = ref(false)
const laneBackgrounds = useLaneBackgrounds()

const visibleLanes = computed(() => allLanes.value.filter((lane) => !lane.is_hidden))

const listFilters = computed<BoardListFilter[]>(() => {
  const active = visibleLanes.value.find((lane) => taskLaneKey(lane.lane_key) === 'active')
  const ordered = active
    ? [active, ...visibleLanes.value.filter((lane) => lane.id !== active.id)]
    : visibleLanes.value
  return ordered.map((lane) => ({
    key: lane.lane_key,
    label: taskLaneTitle(lane),
    color: lane.color,
  }))
})

const listRows = computed<BoardListRow[]>(() => {
  const laneByKey = new Map(visibleLanes.value.map((lane) => [lane.lane_key, lane]))
  return listTasks.value.flatMap((task) => {
    const lane = laneByKey.get(taskLaneKey(task.status))
    if (!lane) return []
    const needsConfiguration = !task.execution_mode
    return [{
      key: task.uuid,
      title: task.title,
      status: lane.lane_key,
      statusLabel: taskLaneTitle(lane),
      statusColor: lane.color,
      priority: task.priority,
      executorName: executionName(task),
      executorAvatar: executionAvatar(task),
      updatedAt: task.updated_at,
      activity: task.latest_activity,
      actions: [{
        kind: needsConfiguration ? 'configure' : 'detail',
        label: needsConfiguration
          ? t('teamwork.board.configure')
          : t('workflows.board.taskDetails'),
      }],
      taskUuid: task.uuid,
    }]
  })
})

const grouped = computed(() => Object.fromEntries(
  visibleLanes.value.map((lane) => [
    lane.lane_key,
    tasks.value.filter((task) => taskLaneKey(task.status) === lane.lane_key),
  ]),
))

watch([() => props.viewMode, () => props.searchKeyword.trim()], (values, previous) => {
  clearTimeout(boardSearchTimer)
  loadRequestVersion++
  if (props.viewMode !== 'board') {
    loading.value = false
    return
  }
  loading.value = true
  boardSearchTimer = setTimeout(() => { void load() }, values[1] !== previous?.[1] ? 250 : 0)
})

watch(
  [() => props.viewMode, () => props.searchKeyword.trim(), statusFilter, () => visibleLanes.value.map((lane) => lane.lane_key).join(',')],
  (values, previous) => {
    listPage.value = 1
    scheduleListLoad(values[1] !== previous?.[1] ? 250 : 0)
  },
)

function changeListPage(page: number) {
  listPage.value = page
  scheduleListLoad(0)
}

function scheduleListLoad(delay: number) {
  if (disposed) return
  clearTimeout(listSearchTimer)
  listRequestVersion++
  if (props.viewMode !== 'list') {
    listLoading.value = false
    return
  }
  listLoading.value = true
  listSearchTimer = setTimeout(() => { void loadList() }, delay)
}

async function loadList() {
  clearTimeout(listSearchTimer)
  const version = ++listRequestVersion
  const revisionSnapshot = new Map([...realtimeActivities].map(([key, entry]) => [key, entry.revision]))
  listLoading.value = true
  try {
    const response = await apiClient.get<TaskBoardListResponse>('/tasks', {
      source_type: 'local',
      list_view: true,
      keyword: props.searchKeyword.trim(),
      status: statusFilter.value === 'all' ? undefined : statusFilter.value,
      page: listPage.value,
      page_size: LIST_PAGE_SIZE,
    })
    if (version !== listRequestVersion) return
    const lastPage = Math.max(1, Math.ceil(response.total / LIST_PAGE_SIZE))
    if (listPage.value > lastPage) {
      changeListPage(lastPage)
      return
    }
    for (const task of response.items) {
      const realtime = realtimeActivities.get(task.uuid)
      if (realtime && realtime.revision > (revisionSnapshot.get(task.uuid) || 0)) {
        task.latest_activity = latestActivity(task.latest_activity, realtime.activity)
      }
    }
    listTasks.value = response.items
    listStatusCounts.value = response.status_counts
    listTotal.value = response.total
  } catch (error) {
    if (version === listRequestVersion) {
      message.error(error instanceof Error ? error.message : t('workflows.board.loadFailed'))
    }
  } finally {
    if (version === listRequestVersion) listLoading.value = false
  }
}

onUnmounted(() => {
  disposed = true
  clearTimeout(listSearchTimer)
  clearTimeout(boardSearchTimer)
  listRequestVersion++
  loadRequestVersion++
})

interface RealtimeActivityEntry {
  activity: TaskLatestActivity
  revision: number
}

const realtimeActivities = new Map<string, RealtimeActivityEntry>()
let loadRequestVersion = 0

function eventActivityRef(activity: TaskLatestActivity) {
  const match = /^event:([^:]+):(\d+)$/.exec(activity.id)
  if (!match) return undefined
  return { sessionUuid: match[1], sequence: Number(match[2]) }
}

function latestActivity(
  serverActivity: TaskLatestActivity | undefined,
  realtimeActivity: TaskLatestActivity | undefined,
) {
  if (!serverActivity) return realtimeActivity
  if (!realtimeActivity) return serverActivity
  if (serverActivity.occurred_at !== realtimeActivity.occurred_at) {
    return serverActivity.occurred_at > realtimeActivity.occurred_at
      ? serverActivity
      : realtimeActivity
  }

  const serverTerminal = serverActivity.status !== 'running'
  const realtimeTerminal = realtimeActivity.status !== 'running'
  if (serverTerminal !== realtimeTerminal) return serverTerminal ? serverActivity : realtimeActivity

  const serverRef = eventActivityRef(serverActivity)
  const realtimeRef = eventActivityRef(realtimeActivity)
  if (serverRef && realtimeRef && serverRef.sessionUuid === realtimeRef.sessionUuid) {
    if (serverRef.sequence !== realtimeRef.sequence) {
      return serverRef.sequence > realtimeRef.sequence ? serverActivity : realtimeActivity
    }
  }

  return realtimeActivity
}

onMounted(load)

let refreshQueued = false
useLocalWS('task.changed', () => {
  if (refreshQueued) return
  refreshQueued = true
  queueMicrotask(() => {
    refreshQueued = false
    void load()
  })
})

useLocalWS('executor.activity', (data: {
  task_uuid?: string
  activity_id?: string
  status?: 'running'
  actor?: 'tool'
  kind?: string
  event_type?: string
  preview?: string
  content?: string
  at?: number
}) => {
  const taskUuid = String(data.task_uuid || '').trim()
  const occurredAt = Number(data.at || 0)
  const preview = String(data.preview || data.content || '').trim()
  const kind = data.kind || data.event_type
  if (!taskUuid || !data.activity_id || !kind || occurredAt <= 0) return
  const activity: TaskLatestActivity = {
    id: data.activity_id,
    status: data.status || 'running',
    actor: data.actor || 'tool',
    kind,
    preview,
    occurred_at: occurredAt,
  }
  const cached = realtimeActivities.get(taskUuid)
  const cachedActivity = latestActivity(cached?.activity, activity) || activity
  realtimeActivities.set(taskUuid, {
    activity: cachedActivity,
    revision: (cached?.revision || 0) + 1,
  })

  const task = tasks.value.find((item) => item.uuid === taskUuid)
  if (task) task.latest_activity = latestActivity(task.latest_activity, activity)
  const listTask = listTasks.value.find((item) => item.uuid === taskUuid)
  if (listTask) listTask.latest_activity = latestActivity(listTask.latest_activity, activity)
})

async function load() {
  if (disposed) return
  clearTimeout(boardSearchTimer)
  if (props.viewMode === 'list') scheduleListLoad(0)
  const requestVersion = ++loadRequestVersion
  const keyword = props.viewMode === 'board' ? props.searchKeyword.trim() : ''
  const revisionSnapshot = new Map(
    [...realtimeActivities].map(([taskUuid, entry]) => [taskUuid, entry.revision]),
  )
  loading.value = true
  try {
    const [taskResponse, laneResponse] = await Promise.all([
      apiClient.get<TaskBoardListResponse>('/tasks', { page_size: 100, source_type: 'local', keyword }),
      apiClient.get<{ items: TaskBoardLane[] }>('/tasks/task-lanes'),
    ])
    if (requestVersion !== loadRequestVersion) return

    // 搜索覆盖全部匹配任务；空关键词保留看板原有的前 100 条展示范围。
    const matchedTasks = [...(taskResponse.items || [])]
    const pageCount = keyword ? Math.ceil(taskResponse.total / 100) : 1
    for (let page = 2; page <= pageCount; page++) {
      if (requestVersion !== loadRequestVersion) return
      const response = await apiClient.get<TaskBoardListResponse>('/tasks', {
        page, page_size: 100, source_type: 'local', keyword,
      })
      if (requestVersion !== loadRequestVersion) return
      if (!response.items.length) break
      matchedTasks.push(...response.items)
    }

    // 团队工作配置后生成的任务带 work_item_id。本地任务页只保留本地新建的任务。
    const loadedTasks = matchedTasks.filter((task) => !String(task.work_item_id || '').trim())
    for (const task of loadedTasks) {
      const realtime = realtimeActivities.get(task.uuid)
      if (realtime && realtime.revision > (revisionSnapshot.get(task.uuid) || 0)) {
        task.latest_activity = latestActivity(task.latest_activity, realtime.activity)
      }
    }
    tasks.value = loadedTasks
    allLanes.value = laneResponse.items || []
    lanesLoaded.value = true
    const loadedTaskUuids = new Set(loadedTasks.map((task) => task.uuid))
    for (const taskUuid of realtimeActivities.keys()) {
      if (!loadedTaskUuids.has(taskUuid) && !listTasks.value.some((task) => task.uuid === taskUuid)) realtimeActivities.delete(taskUuid)
    }
  } catch (error) {
    if (requestVersion === loadRequestVersion) {
      message.error(error instanceof Error ? error.message : t('workflows.board.loadFailed'))
    }
  } finally {
    if (requestVersion === loadRequestVersion) loading.value = false
  }
}

function onDragStart(event: DragEvent, task: TaskBoardTask) {
  draggingUuid.value = task.uuid
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', task.uuid)
  }
}

function onDragEnter(laneKey: string) {
  dragOverLane.value = laneKey
}

function onDragLeave(event: DragEvent, laneKey: string) {
  const relatedTarget = event.relatedTarget as Node | null
  const currentTarget = event.currentTarget as HTMLElement
  if (!relatedTarget || !currentTarget.contains(relatedTarget)) {
    if (dragOverLane.value === laneKey) dragOverLane.value = null
  }
}

async function onDrop(event: DragEvent, targetStatus: string) {
  event.preventDefault()
  dragOverLane.value = null
  const taskUuid = event.dataTransfer?.getData('text/plain') || draggingUuid.value
  draggingUuid.value = null
  if (!taskUuid) return

  const task = tasks.value.find((item) => item.uuid === taskUuid)
  if (!task || task.status === targetStatus) return
  await changeStatus(task, targetStatus)
}

function onDragOver(event: DragEvent) {
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
}

async function changeStatus(task: TaskBoardTask, status: string) {
  if (task.execution_mode === 'vibe_coding') return
  if (task.status === status) return
  const shouldStart = ['active', 'in_progress', 'developing'].includes(status)
  if (shouldStart && !task.execution_mode) {
    pendingAssignTask.value = task
    assignModalOpen.value = true
    return
  }
  if (shouldStart && task.execution_mode === 'pipeline' && !task.pipeline_snapshot_uuid) {
    pendingAssignTask.value = task
    assignModalOpen.value = true
    return
  }
  // 流水线步骤缺少提示词/CLI/模型时，弹窗补全
  if (shouldStart && task.execution_mode === 'pipeline' && task.pipeline_snapshot_uuid) {
    try {
      const detail = await apiClient.get<{ steps: Array<{ prompt_snapshot?: string; cli_type?: string; model_name?: string }> }>(`/tasks/${task.uuid}`)
      if (detail.steps?.some(s => !s.prompt_snapshot || !s.cli_type || !s.model_name)) {
        pendingAssignTask.value = task
        assignModalOpen.value = true
        return
      }
    } catch { /* 加载失败时直接调用 API */ }
  }
  updating.value = task.uuid
  try {
    await apiClient.put(`/tasks/${task.uuid}/status`, { status })
    task.status = status
    if (shouldStart) message.success(t('workflows.board.started'))
    else message.success(t('workflows.board.statusUpdated'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.board.statusUpdateFailed'))
  } finally {
    updating.value = undefined
  }
}

function openCreate(status?: string) {
  createStatus.value = status || ''
  createModalOpen.value = true
}

function laneBackground(laneKey: string) {
  return laneBackgrounds.backgroundOf(laneKey)
}

function setLaneBackground(laneKey: string, color: string) {
  laneBackgrounds.set(laneKey, color)
}

function resetLaneBackground(laneKey: string) {
  laneBackgrounds.reset(laneKey)
}

function onTaskCreated(taskUuid: string, options?: { openChat?: boolean }) {
  void load()
  // CLI 任务由创建弹窗跳到对话页。这里再打开任务详情会把那次跳转取消掉。
  if (options?.openChat) return
  void router.push(`/board/task/${taskUuid}`)
}

defineExpose({ openCreate })

function openTask(taskUuid: string) {
  if (taskViewMode.value === 'new-window') {
    void router.push(`/board/task/${taskUuid}`)
    return
  }

  selectedTaskUuid.value = taskUuid
  if (taskViewMode.value === 'drawer') taskDrawerOpen.value = true
  else taskModalOpen.value = true
}

function handleListAction(row: BoardListRow, action: BoardListActionKind) {
  if (!row.taskUuid) return
  if (action === 'configure') {
    const task = listTasks.value.find((item) => item.uuid === row.taskUuid)
    if (!task) return
    pendingAssignTask.value = task
    assignModalOpen.value = true
    return
  }
  openTask(row.taskUuid)
}

function handleListRowClick(row: BoardListRow) {
  if (row.taskUuid) openTask(row.taskUuid)
}

function closeTaskPreview() {
  taskDrawerOpen.value = false
  taskModalOpen.value = false
  selectedTaskUuid.value = ''
  void load()
}

function openSettings() {
  settingsVisible.value = true
}

function handleSettingsLanesChange(lanes: TaskBoardLane[]) {
  allLanes.value = lanes
}

function handleSettingsSaved(viewMode: TaskViewMode) {
  taskViewMode.value = viewMode
  try {
    window.localStorage.setItem(TASK_VIEW_MODE_STORAGE_KEY, taskViewMode.value)
  } catch {
    // 本地存储不可用时，当前页面内的设置仍然生效。
  }
  void load()
}
</script>

<style scoped>
.task-board {
  display: flex;
  height: 100%;
  flex-direction: column;
  background: #fff;
}

.task-board :deep(.ant-spin-nested-loading) {
  min-height: 0;
  flex: 1;
}

.task-board :deep(.ant-spin-container) {
  height: 100%;
}

.lanes {
  display: flex;
  height: 100%;
  overflow-x: auto;
  overflow-y: hidden;
  align-items: stretch;
  gap: 14px;
  padding-bottom: 4px;
}

.lane {
  display: flex;
  width: 280px;
  min-width: 280px;
  box-sizing: border-box;
  overflow: hidden;
  flex-shrink: 0;
  flex-direction: column;
  border: 0;
  border-radius: 16px;
  background: var(--lane-background, #fbfbfc);
  transition: border-color 0.2s, background 0.2s;
}

.lane.drag-over {
  border-color: #3157e2;
  box-shadow: inset 0 0 0 1px #3157e2;
}

.task-list {
  display: flex;
  overflow-y: auto;
  flex: 1;
  flex-direction: column;
  gap: 10px;
  padding: 0 12px 12px;
}

.lane-empty {
  display: flex;
  width: 100%;
  height: 144px;
  box-sizing: border-box;
  flex-direction: column;
  align-items: center;
  padding-top: 60px;
  color: rgba(0,0,0,.25);
}

.empty-icon {
  width: 64px;
  height: 40px;
}

.empty-text {
  margin-top: 8px;
  font-size: 14px;
  line-height: 22px;
}

.task-preview-content {
  width: 100%;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

:global(.task-preview-modal .ant-modal) {
  top: 24px;
  max-width: calc(100vw - 48px);
  padding-bottom: 0;
}

@media (max-width: 640px) {
  :global(.task-preview-modal .ant-modal) {
    top: 12px;
    width: calc(100vw - 24px) !important;
    max-width: calc(100vw - 24px);
  }

  :global(.task-preview-modal .ant-modal-body) {
    height: calc(100vh - 92px) !important;
  }
}
</style>
