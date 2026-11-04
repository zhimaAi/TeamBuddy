<template>
  <div class="team-board">
    <ASpin v-if="props.viewMode === 'board'" :spinning="loading">
      <div class="lanes scrollbar--subtle">
        <section
          v-for="lane in lanes"
          :key="lane.key"
          class="lane"
          :style="{ '--lane-background': laneBackgrounds.backgroundOf(lane.key) }"
        >
          <BoardLaneHeader
            :title="lane.title"
            :color="lane.color"
            :count="grouped[lane.key]?.length || 0"
            :background="laneBackgrounds.backgroundOf(lane.key)"
            @set-background="laneBackgrounds.set(lane.key, $event)"
            @reset-background="laneBackgrounds.reset(lane.key)"
          />

          <div class="task-list scrollbar--subtle">
            <template
              v-for="card in grouped[lane.key] || []"
              :key="card.key"
            >
              <!-- 已绑定本地任务：沿用「看板」的任务卡片，点击进现有任务详情。 -->
              <TaskBoardCard
                v-if="card.task"
                :task="card.task"
                :draggable="false"
                @open="openTask"
              />
              <!-- 本地任务不在当前列表里（如超出分页）时，退化成工作项卡片，避免工作项消失。 -->
              <TeamWorkItemCard
                v-else
                :item="card.item"
                :configurable="lane.key === UNCONFIGURED_LANE_KEY"
                @open="openBoundOrWorkItem(card.item)"
                @configure="openConfigure(card.item)"
              />
            </template>

            <div
              v-if="(grouped[lane.key]?.length || 0) === 0"
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
      :search-keyword="props.searchKeyword"
      :loading="loading"
      row-clickable
      @action="handleListAction"
      @row-click="handleListRowClick"
    />

    <WorkItemDetailModal
      v-model:open="detailOpen"
      :item="detailItem"
    />

    <ConfigureTeamTaskModal
      v-model:open="configureOpen"
      :item="configureItem"
      @created="handleConfigured"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { useRouter } from 'vue-router'
import apiClient, { ApiError } from '@/api/client'
import { useLaneBackgrounds } from '@/composables/useLaneBackgrounds'
import { useLocalWS } from '@/composables/useLocalWebSocket'
import { useTaskExecutionDisplay } from '@/composables/useTaskExecutionDisplay'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { useAppI18n } from '@/i18n'
import { taskLaneKey } from '@/utils/taskLane'
import type {
  BoardLayoutMode,
  BoardListActionKind,
  BoardListFilter,
  BoardListRow,
  TaskBoardTask,
} from '@/types/task-board'
import type { MyWorkItem } from '@/types/workitem'
import BoardLaneHeader from './BoardLaneHeader.vue'
import ConfigureTeamTaskModal from './ConfigureTeamTaskModal.vue'
import EmptyIssueIcon from './EmptyIssueIcon.vue'
import TaskBoardCard from './TaskBoardCard.vue'
import TaskListView from './TaskListView.vue'
import TeamWorkItemCard from './TeamWorkItemCard.vue'
import WorkItemDetailModal from './WorkItemDetailModal.vue'

const { t } = useAppI18n()
const statusFilter = defineModel<string>('statusFilter', { default: 'active' })
const props = withDefaults(defineProps<{
  viewMode?: BoardLayoutMode
  searchKeyword?: string
}>(), {
  viewMode: 'board',
  searchKeyword: '',
})
const router = useRouter()
const authStore = useAuthStore()
const appStore = useAppStore()
const { executionName, executionAvatar } = useTaskExecutionDisplay()

/** 「待配置」是团队工作独有的虚拟泳道：GoTeams 待办但还没绑定本地任务的工作项。 */
const UNCONFIGURED_LANE_KEY = '__unconfigured__'
/** 需求固定五列，不跟本地看板的自定义泳道走。 */
const TEAM_LANE_DEFS = [
  { key: UNCONFIGURED_LANE_KEY, titleKey: 'teamwork.lane.pending', color: '#d8dde5' },
  { key: 'pending', titleKey: 'teamwork.lane.todo', color: '#8c8c8c' },
  { key: 'active', titleKey: 'teamwork.lane.active', color: '#3157e2' },
  { key: 'done', titleKey: 'teamwork.lane.done', color: '#52c41a' },
  { key: 'blocked', titleKey: 'teamwork.lane.blocked', color: '#f5222d' },
] as const

interface TeamLane {
  key: string
  title: string
  color: string
}

interface TeamLaneCard {
  key: string
  item: MyWorkItem
  /** 命中本地任务列表时才带上，用于渲染看板卡片。 */
  task?: TaskBoardTask
}

const loading = ref(false)
const items = ref<MyWorkItem[]>([])
const tasks = ref<TaskBoardTask[]>([])
const detailOpen = ref(false)
const detailItem = ref<MyWorkItem>()
const configureOpen = ref(false)
const configureItem = ref<MyWorkItem>()
const laneBackgrounds = useLaneBackgrounds()

const lanes = computed<TeamLane[]>(() =>
  TEAM_LANE_DEFS.map((lane) => ({
    key: lane.key,
    title: t(lane.titleKey),
    color: lane.color,
  })),
)

const grouped = computed<Record<string, TeamLaneCard[]>>(() => {
  const buckets: Record<string, TeamLaneCard[]> = Object.fromEntries(
    lanes.value.map((lane) => [lane.key, [] as TeamLaneCard[]]),
  )
  const taskByUuid = new Map(tasks.value.map((task) => [task.uuid, task]))
  const keyword = props.searchKeyword.trim().toLowerCase()

  for (const item of items.value) {
    const task = item.local_task_uuid ? taskByUuid.get(item.local_task_uuid) : undefined
    if (keyword && !(task?.title || item.title).toLowerCase().includes(keyword)) continue
    // 待配置只放「我的待办」且还没绑定本地任务的需求/缺陷；已完成但未绑定的工作项不应出现。
    let desired = UNCONFIGURED_LANE_KEY
    if (item.local_task_uuid) {
      desired = taskLaneKey(item.local_task_status || '')
    } else if (isWorkItemDone(item)) {
      continue
    }
    const key = buckets[desired] ? desired : 'pending'
    if (!key) continue
    buckets[key].push({
      key: `${item.type}:${item.id}`,
      item,
      task,
    })
  }
  return buckets
})

const listFilters = computed<BoardListFilter[]>(() => {
  const active = lanes.value.find((lane) => lane.key === 'active')
  const ordered = active
    ? [active, ...lanes.value.filter((lane) => lane.key !== active.key)]
    : lanes.value
  return ordered.map((lane) => ({
    key: lane.key,
    label: lane.title,
    color: lane.color,
  }))
})

const listRows = computed<BoardListRow[]>(() => lanes.value.flatMap((lane) => (
  (grouped.value[lane.key] || []).map((card) => {
    const task = card.task
    const bound = Boolean(card.item.local_task_uuid)
    const assigneeName = card.item.assignee_names?.[0]?.trim()
      || card.item.assignee_usernames?.[0]?.trim()
      || ''
    const actions = bound
      ? [{ kind: 'detail' as const, label: t('workflows.board.taskDetails') }]
      : [{ kind: 'configure' as const, label: t('teamwork.board.configure') }]

    return {
      key: card.key,
      title: task?.title || card.item.title,
      status: lane.key,
      statusLabel: lane.title,
      statusColor: lane.color,
      priority: task?.priority || card.item.priority_name,
      executorName: task ? executionName(task) : assigneeName,
      executorAvatar: task
        ? executionAvatar(task)
        : card.item.assignee_avatars?.[0]?.trim() || '',
      updatedAt: task?.updated_at || card.item.updated_at,
      activity: task?.latest_activity,
      actions,
      taskUuid: card.item.local_task_uuid,
      workItemKey: card.key,
    }
  })
)).sort((a, b) => listRowTimestamp(b.updatedAt) - listRowTimestamp(a.updatedAt)))

function listRowTimestamp(value?: number | string): number {
  const timestamp = Number(value || 0)
  if (!Number.isFinite(timestamp) || timestamp <= 0) return 0
  // 与时间列保持相同的秒/毫秒口径，避免绑定本地任务后显示时间与顺序脱节。
  return timestamp < 1e12 ? timestamp * 1000 : timestamp
}

onMounted(load)

let active = true
let activatedOnce = false
let loadVersion = 0
onActivated(() => {
  active = true
  if (!activatedOnce) {
    activatedOnce = true
    return
  }
  void load()
})

function deactivate() {
  active = false
  loadVersion += 1
  loading.value = false
  detailOpen.value = false
  detailItem.value = undefined
  configureOpen.value = false
  configureItem.value = undefined
}
onDeactivated(deactivate)
onUnmounted(deactivate)

let refreshQueued = false
useLocalWS('task.changed', () => {
  if (!active || refreshQueued) return
  refreshQueued = true
  queueMicrotask(() => {
    refreshQueued = false
    void load()
  })
})

async function load() {
  if (!active) return
  const version = ++loadVersion
  loading.value = true
  try {
    const response = await apiClient.get<{ items: MyWorkItem[] }>('/team/my-work')
    if (!active || version !== loadVersion) return
    const workItems = response.items || []
    const taskUuids = [...new Set(workItems.map((item) => item.local_task_uuid).filter((value): value is string => Boolean(value)))]
    const boundTasks: TaskBoardTask[] = []
    try {
      // 只加载当前账号工作项绑定的任务；每批不超过接口分页上限。
      for (let index = 0; index < taskUuids.length; index += 100) {
        const batch = await apiClient.get<{ items: TaskBoardTask[] }>('/tasks', {
          uuids: taskUuids.slice(index, index + 100).join(','),
          page_size: 100,
        })
        if (!active || version !== loadVersion) return
        boundTasks.push(...batch.items)
      }
      tasks.value = boundTasks
    } catch (error) {
      if (!active || version !== loadVersion) return
      // 补齐失败时只保留当前工作项仍引用的缓存，不让上一账号的数据进入展示。
      const currentUuids = new Set(taskUuids)
      tasks.value = tasks.value.filter((task) => currentUuids.has(task.uuid))
      message.error(error instanceof Error ? error.message : t('teamwork.board.loadFailed'))
    }
    if (!active || version !== loadVersion) return
    items.value = workItems
  } catch (error) {
    if (!active || version !== loadVersion) return
    if (error instanceof ApiError && error.status === 401) {
      authStore.clearCloudAuth()
      appStore.setCloudStatus('auth-expired')
    } else {
      message.error(
        error instanceof Error
          ? error.message
          : t('teamwork.board.loadFailed'),
      )
    }
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function openTask(taskUuid: string) {
  void router.push(`/board/task/${taskUuid}?from=team-work`)
}

function openBoundOrWorkItem(item: MyWorkItem) {
  if (item.local_task_uuid) {
    openTask(item.local_task_uuid)
    return
  }
  openWorkItem(item)
}

function openWorkItem(item: MyWorkItem) {
  detailItem.value = item
  detailOpen.value = true
}

function openConfigure(item: MyWorkItem) {
  configureItem.value = item
  configureOpen.value = true
}

function handleListAction(row: BoardListRow, action: BoardListActionKind) {
  const item = items.value.find((candidate) => `${candidate.type}:${candidate.id}` === row.workItemKey)
  if (!item) return
  if (action === 'configure') {
    openConfigure(item)
    return
  }
  if (row.taskUuid) openTask(row.taskUuid)
}

function handleListRowClick(row: BoardListRow) {
  const item = items.value.find((candidate) => `${candidate.type}:${candidate.id}` === row.workItemKey)
  if (!item) return
  openBoundOrWorkItem(item)
}

function handleConfigured() {
  void load()
}

function isWorkItemDone(item: MyWorkItem) {
  const value = item.is_done
  return value === true || value === 1 || value === '1' || value === 'true' || value === 't'
}
</script>

<style scoped>
.team-board {
  display: flex;
  height: 100%;
  flex-direction: column;
  background: #fff;
}

.team-board :deep(.ant-spin-nested-loading) {
  min-height: 0;
  flex: 1;
}

.team-board :deep(.ant-spin-container) {
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
  color: rgba(0, 0, 0, 0.25);
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
</style>
