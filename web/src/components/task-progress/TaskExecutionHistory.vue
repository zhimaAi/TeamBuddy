<template>
  <section
    v-if="items.length || loading || error"
    class="execution-history"
  >
    <button
      v-if="items.length"
      type="button"
      class="history-toggle"
      :aria-expanded="expanded"
      :aria-label="
        expanded
          ? t('workflows.task.executionHistory.collapseSection')
          : t('workflows.task.executionHistory.expandSection')
      "
      @click="toggleSection"
    >
      <HistoryOutlined aria-hidden="true" />
      <span>{{ t('workflows.task.executionHistory.title', { count: items.length }) }}</span>
      <DownOutlined
        aria-hidden="true"
        :class="{ rotated: !expanded }"
      />
    </button>

    <div
      v-if="loading && !items.length"
      class="history-state"
      role="status"
    >
      <a-spin size="small" />
    </div>
    <div
      v-else-if="error && !items.length"
      class="history-state history-state--error"
    >
      <span>{{ error }}</span>
      <button
        type="button"
        @click="loadSummaries"
      >
        <ReloadOutlined />{{ t('common.actions.retry') }}
      </button>
    </div>

    <div
      v-if="expanded && items.length"
      class="history-card"
    >
      <p class="history-notice">{{ t('workflows.task.executionHistory.readOnlyNotice') }}</p>
      <article
        v-for="item in items"
        :key="item.uuid"
        class="history-execution"
      >
        <button
          type="button"
          class="history-execution-head"
          :aria-expanded="isExecutionExpanded(item.uuid)"
          :aria-label="
            isExecutionExpanded(item.uuid)
              ? t('workflows.task.executionHistory.collapseExecution', { index: item.execution_no })
              : t('workflows.task.executionHistory.expandExecution', { index: item.execution_no })
          "
          @click="toggleExecution(item)"
        >
          <span class="history-index">{{ item.execution_no }}</span>
          <span class="history-copy">
            <strong>{{ executionTitle(item) }}</strong>
            <span>
              <time>{{ formatDateTime(item.started_at || item.archived_at) }}</time>
              <i>{{ t('workflows.task.executionHistory.archivedReadonly') }}</i>
            </span>
          </span>
          <MinusOutlined
            v-if="isExecutionExpanded(item.uuid)"
            aria-hidden="true"
            class="history-action"
          />
          <PlusOutlined
            v-else
            aria-hidden="true"
            class="history-action"
          />
        </button>

        <div
          v-if="isExecutionExpanded(item.uuid)"
          class="history-detail"
        >
          <div
            v-if="isDetailLoading(item.uuid)"
            class="history-state"
            role="status"
          >
            <a-spin size="small" />
          </div>
          <div
            v-else-if="detailError(item.uuid)"
            class="history-state history-state--error"
          >
            <span>{{ detailError(item.uuid) }}</span>
            <button
              type="button"
              @click.stop="loadDetail(item)"
            >
              <ReloadOutlined />{{ t('common.actions.retry') }}
            </button>
          </div>
          <StepMessageList
            v-else-if="detailFor(item.uuid)"
            :items="historyItems(detailFor(item.uuid))"
            :steps="historySteps(detailFor(item.uuid))"
            :selected-step-name="fallbackActorName(item)"
            :fallback-actor-name="fallbackActorName(item)"
            :fallback-actor-logo="fallbackActorLogo(item)"
            :task-uuid="taskUuid"
            :file-roots="fileRoots"
            :session-events="detailFor(item.uuid)?.session_events || {}"
            embedded
            read-only
            @copy="copyProgress"
            @resync="refreshDetail(item)"
          />
        </div>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import {
  DownOutlined,
  HistoryOutlined,
  MinusOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import cliExecutionLogo from '@/assets/icons/task-composer-cli.svg'
import codexLogo from '@/assets/icons/codex-logo.svg'
import vibeCodingLogo from '@/assets/icons/vibe-coding-logo.svg'
import { isVibeCodingTool, vibeToolLabelKey } from '@/composables/useVibeCoding'
import { copyText } from '@/utils/clipboard'
import type { PipelineStep, TaskProgress } from '@/types/pipeline'
import type {
  TaskExecutionHistoryDetail,
  TaskExecutionHistorySummary,
} from '@/types/task-execution-history'
import { useAppI18n } from '@/i18n'
import { useLocale } from '@/composables/useLocale'
import { cliDisplayName, isUserMessage, resultText, userMessageText } from './utils'
import StepMessageList from './StepMessageList.vue'

const props = withDefaults(
  defineProps<{
    taskUuid: string
    revision?: number | string
    fileRoots?: string[]
  }>(),
  { revision: 0, fileRoots: () => [] },
)

/** 历史执行里重报成功后通知页面：重报会把整条任务队列里未投递的执行一起推上去 */
const emit = defineEmits<{ resync: [] }>()

const { t } = useAppI18n()
const { locale } = useLocale()
const items = ref<TaskExecutionHistorySummary[]>([])
const loading = ref(false)
const error = ref('')
const expanded = ref(false)
const expandedUuids = ref(new Set<string>())
const details = ref(new Map<string, TaskExecutionHistoryDetail>())
const detailLoadingUuids = ref(new Set<string>())
const detailErrors = ref(new Map<string, string>())
let summaryRequestVersion = 0
let detailContextVersion = 0

async function loadSummaries() {
  const taskUuid = props.taskUuid.trim()
  const requestVersion = ++summaryRequestVersion
  if (!taskUuid) {
    items.value = []
    return
  }
  loading.value = true
  error.value = ''
  try {
    const result = await apiClient.get<
      { items: TaskExecutionHistorySummary[] } | TaskExecutionHistorySummary[]
    >(`/tasks/${encodeURIComponent(taskUuid)}/execution-history`)
    if (requestVersion !== summaryRequestVersion || taskUuid !== props.taskUuid.trim()) return
    const nextItems = Array.isArray(result) ? result : result.items || []
    items.value = [...nextItems].sort((a, b) => a.execution_no - b.execution_no)
    const validUuids = new Set(items.value.map((item) => item.uuid))
    expandedUuids.value = new Set([...expandedUuids.value].filter((uuid) => validUuids.has(uuid)))
    if (!items.value.length) expanded.value = false
  } catch (loadError) {
    if (requestVersion !== summaryRequestVersion) return
    error.value =
      loadError instanceof Error
        ? loadError.message
        : t('workflows.task.executionHistory.loadFailed')
  } finally {
    if (requestVersion === summaryRequestVersion) loading.value = false
  }
}

function isExecutionExpanded(uuid: string) {
  return expandedUuids.value.has(uuid)
}

function toggleSection() {
  expanded.value = !expanded.value
  if (!expanded.value) return
  expandedUuids.value = new Set(items.value.map((item) => item.uuid))
  for (const item of items.value) {
    if (!details.value.has(item.uuid) && !detailLoadingUuids.value.has(item.uuid)) {
      void loadDetail(item)
    }
  }
}

async function toggleExecution(item: TaskExecutionHistorySummary) {
  const next = new Set(expandedUuids.value)
  if (next.has(item.uuid)) {
    next.delete(item.uuid)
  } else {
    next.add(item.uuid)
    if (!details.value.has(item.uuid)) void loadDetail(item)
  }
  expandedUuids.value = next
}

async function loadDetail(item: TaskExecutionHistorySummary) {
  if (detailLoadingUuids.value.has(item.uuid)) return
  const taskUuid = props.taskUuid.trim()
  const contextVersion = detailContextVersion
  if (!taskUuid) return
  const nextLoading = new Set(detailLoadingUuids.value)
  nextLoading.add(item.uuid)
  detailLoadingUuids.value = nextLoading
  const nextErrors = new Map(detailErrors.value)
  nextErrors.delete(item.uuid)
  detailErrors.value = nextErrors
  try {
    const detail = await apiClient.get<TaskExecutionHistoryDetail>(
      `/tasks/${encodeURIComponent(taskUuid)}/execution-history/${encodeURIComponent(item.uuid)}`,
    )
    if (contextVersion !== detailContextVersion || taskUuid !== props.taskUuid.trim()) return
    const nextDetails = new Map(details.value)
    nextDetails.set(item.uuid, detail)
    details.value = nextDetails
  } catch (loadError) {
    if (contextVersion !== detailContextVersion || taskUuid !== props.taskUuid.trim()) return
    const errors = new Map(detailErrors.value)
    errors.set(
      item.uuid,
      loadError instanceof Error
        ? loadError.message
        : t('workflows.task.executionHistory.detailLoadFailed'),
    )
    detailErrors.value = errors
  } finally {
    if (contextVersion === detailContextVersion && taskUuid === props.taskUuid.trim()) {
      const loadingUuids = new Set(detailLoadingUuids.value)
      loadingUuids.delete(item.uuid)
      detailLoadingUuids.value = loadingUuids
    }
  }
}

function detailFor(uuid: string) {
  return details.value.get(uuid)
}

function isDetailLoading(uuid: string) {
  return detailLoadingUuids.value.has(uuid)
}

function detailError(uuid: string) {
  return detailErrors.value.get(uuid) || ''
}

function executionTitle(item: TaskExecutionHistorySummary) {
  if (item.execution_mode === 'pipeline') {
    return t('workflows.task.executionHistory.pipelineTitle', {
      name: item.target_name || t('workflows.task.common.pipeline'),
    })
  }
  if (item.execution_mode === 'expert_group') {
    return t('workflows.task.executionHistory.expertTitle', {
      name: item.target_name || t('agents.expertTeam'),
    })
  }
  if (item.execution_mode === 'cli') {
    const cli = cliDisplayName(item.execution_tool || item.target_name)
    return item.model_name
      ? t('workflows.task.executionHistory.cliTitle', { cli, model: item.model_name })
      : t('workflows.task.executionHistory.directTitle', { tool: cli })
  }
  const toolKey = item.execution_tool || ''
  const tool = item.target_name
    || (isVibeCodingTool(toolKey) ? t(vibeToolLabelKey(toolKey)) : toolKey)
    || t('workflows.task.create.vibeCodingMode')
  return t('workflows.task.executionHistory.directTitle', {
    tool: [tool.toLowerCase() === 'codex' ? 'Codex' : tool, item.model_name].filter(Boolean).join(' · '),
  })
}

function fallbackActorName(item: TaskExecutionHistorySummary) {
  if (item.execution_mode === 'cli') return cliDisplayName(item.execution_tool || item.target_name)
  if (item.execution_mode === 'vibe_coding') {
    const toolKey = item.execution_tool || ''
    return item.target_name || (isVibeCodingTool(toolKey) ? t(vibeToolLabelKey(toolKey)) : toolKey)
  }
  return item.target_name
}

function fallbackActorLogo(item: TaskExecutionHistorySummary) {
  if (item.execution_mode === 'cli') return cliExecutionLogo
  if (item.execution_mode === 'vibe_coding') {
    return item.execution_tool === 'codex' ? codexLogo : vibeCodingLogo
  }
  return ''
}

function historySteps(detail?: TaskExecutionHistoryDetail): PipelineStep[] {
  if (!detail || detail.execution_mode === 'cli' || detail.execution_mode === 'vibe_coding')
    return []
  return detail.steps || []
}

/**
 * 归档快照里的消息没有逐条上报字段：一次执行就是一次整体上报，
 * 用执行级状态给每条消息渲染「上报状态 + 上报时间」。
 */
function historyItems(detail?: TaskExecutionHistoryDetail): TaskProgress[] {
  const progress = detail?.progress || []
  if (!detail?.cloud_sync_status) return progress
  return progress.map((item) => ({
    ...item,
    cloud_sync_status: detail.cloud_sync_status,
    cloud_sync_error: detail.cloud_sync_error,
    cloud_synced_at: detail.cloud_synced_at,
  }))
}

/** 历史执行里点「重新上报」后，重新拉一次快照详情，拿到最新上报状态 */
async function refreshDetail(item: TaskExecutionHistorySummary) {
  await loadDetail(item)
  emit('resync')
}

function formatDateTime(timestamp: number) {
  if (!timestamp) return ''
  const milliseconds = timestamp < 1e12 ? timestamp * 1000 : timestamp
  return new Date(milliseconds).toLocaleString(locale.value, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

async function copyProgress(item: TaskProgress) {
  try {
    await copyText(isUserMessage(item) ? userMessageText(item) : resultText(item))
    message.success(t('components.feedback.copied'))
  } catch {
    message.warning(t('components.feedback.copyFailed'))
  }
}

watch(
  () => props.taskUuid,
  (taskUuid, previousTaskUuid) => {
    if (taskUuid === previousTaskUuid) return
    detailContextVersion += 1
    expanded.value = false
    expandedUuids.value = new Set()
    details.value = new Map()
    detailLoadingUuids.value = new Set()
    detailErrors.value = new Map()
  },
)

watch(
  () => [props.taskUuid, props.revision] as const,
  () => {
    void loadSummaries()
  },
  { immediate: true },
)
</script>

<style scoped>
.execution-history {
  margin: 20px 0;
}
.history-toggle {
  display: inline-flex;
  min-height: 32px;
  align-items: center;
  gap: 10px;
  padding: 0;
  border: 0;
  color: #8c8c8c;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
}
.history-toggle:hover,
.history-toggle:focus-visible {
  color: #262626;
}
.history-toggle:focus-visible,
.history-execution-head:focus-visible,
.history-state button:focus-visible {
  outline: 2px solid rgba(49, 87, 226, 0.35);
  outline-offset: 2px;
}
.history-toggle .anticon:last-child {
  font-size: 12px;
  transition: transform 0.18s ease;
}
.history-toggle .anticon.rotated {
  transform: rotate(-90deg);
}
.history-card {
  overflow: hidden;
  margin-top: 8px;
  border: 1px solid #e5e7eb;
  border-radius: 16px;
  background: #fff;
  box-shadow: 0 8px 28px rgba(15, 23, 42, 0.06);
}
.history-notice {
  margin: 0;
  padding: 14px 16px 4px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}
.history-execution + .history-execution {
  border-top: 1px solid #f0f0f0;
}
.history-execution-head {
  display: flex;
  width: 100%;
  min-height: 72px;
  align-items: center;
  gap: 14px;
  padding: 12px 16px;
  border: 0;
  color: #262626;
  background: transparent;
  cursor: pointer;
  text-align: left;
}
.history-execution-head:hover {
  background: #fafafa;
}
.history-index {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  color: #595959;
  background: #f3f4f6;
  font-size: 14px;
}
.history-copy {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 4px;
}
.history-copy strong {
  overflow: hidden;
  color: #262626;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.history-copy > span {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}
.history-copy i {
  font-style: normal;
}
.history-action {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 14px;
}
.history-detail {
  padding: 0 16px;
  border-top: 1px solid #f0f0f0;
}
.history-state {
  display: flex;
  min-height: 56px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: #8c8c8c;
  font-size: 13px;
}
.history-state--error {
  color: #fb363f;
}
.history-state button {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 0;
  border-radius: 6px;
  color: #3157e2;
  background: transparent;
  cursor: pointer;
}

@media (max-width: 760px) {
  .history-execution-head {
    padding-inline: 12px;
  }
  .history-detail {
    padding-inline: 12px;
  }
}
</style>
