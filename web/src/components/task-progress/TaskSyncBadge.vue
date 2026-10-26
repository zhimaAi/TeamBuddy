<template>
  <!-- 整个绑定（任务投影 + 全部沟通会话）是否已上报线上 -->
  <div
    v-if="visible"
    class="task-sync"
    :class="`is-${state}`"
  >
    <a-tooltip :title="tooltip">
      <span class="sync-chip">
        <CheckCircleFilled v-if="state === 'success'" />
        <ExclamationCircleFilled v-else-if="state === 'failed'" />
        <CloudUploadOutlined v-else />
        <span class="sync-text">{{ label }}</span>
        <span
          v-if="timeText"
          class="sync-time"
          >{{ timeText }}</span
        >
      </span>
    </a-tooltip>
    <button
      v-if="showRetry"
      type="button"
      class="sync-retry"
      :disabled="retrying"
      @click="resync"
    >
      {{ retrying ? t('workflows.task.sync.retrying') : t('workflows.task.sync.reupload') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  CheckCircleFilled,
  CloudUploadOutlined,
  ExclamationCircleFilled,
} from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { fetchTaskSyncStatus, retryTaskSync } from '@/api/taskSync'
import { useAppI18n } from '@/i18n'
import type { CloudSyncState, TaskSyncStatus } from '@/types/task-sync'

const props = withDefaults(
  defineProps<{
    taskUuid: string
    sourceType?: 'cloud' | 'local'
    /** 任务更新时（WS 推送后）变化，用于立即刷新上报状态 */
    refreshKey?: number
  }>(),
  { sourceType: 'local', refreshKey: 0 },
)

const emit = defineEmits<{ resync: [] }>()
const { t } = useAppI18n()

const status = ref<TaskSyncStatus>()
const retrying = ref(false)
// 最近一次手动重报的真实失败原因：接口下一次轮询不会带 retry_error_detail，
// 这里缓存住，直到重报成功，避免用户刚看到原因就消失。
const retryDetail = ref('')
let timer: ReturnType<typeof setTimeout> | undefined

// 只有云端来源的任务才有上报动作，本地任务不展示（避免误报「待上报」）
const visible = computed(
  () => props.sourceType === 'cloud' && Boolean(status.value?.enabled ?? true),
)
const state = computed<CloudSyncState>(() => status.value?.status || 'pending')
const timeText = computed(() => formatTime(status.value?.last_success_at))
// 账号不匹配时后端必然拒绝推送，不给可点的重报按钮，避免点了才知道做不了
const canRetry = computed(
  () =>
    Boolean(status.value?.logged_in) &&
    status.value?.reason !== 'not_owned' &&
    status.value?.reason !== 'not_cloud',
)
const showRetry = computed(
  () =>
    canRetry.value &&
    (state.value !== 'success' ||
      (status.value?.pending_sessions || 0) > 0 ||
      (status.value?.pending_executions || 0) > 0),
)

const label = computed(() => {
  if (status.value?.reason === 'not_owned') return t('workflows.task.sync.unowned')
  if (!status.value?.logged_in) return t('workflows.task.sync.pending')
  if (state.value === 'success') return t('workflows.task.sync.synced')
  if (state.value === 'failed') return t('workflows.task.sync.failed')
  const pending = status.value?.pending_sessions || 0
  return pending > 0
    ? t('workflows.task.sync.pendingCount', { count: pending })
    : t('workflows.task.sync.pending')
})

const tooltip = computed(() => {
  if (status.value?.reason === 'not_owned') return t('workflows.task.sync.unownedTitle')
  if (!status.value?.logged_in) return t('workflows.task.sync.notLoggedIn')
  if (retryDetail.value) return retryDetail.value
  if (state.value === 'failed') {
    return t('workflows.task.sync.failedTitle', {
      error: status.value?.last_error || t('workflows.task.sync.unknownError'),
    })
  }
  if (state.value === 'success') return t('workflows.task.sync.syncedTitle')
  const executions = status.value?.pending_executions || 0
  if (executions > 0) {
    return t('workflows.task.sync.pendingExecutionsTitle', { count: executions })
  }
  return t('workflows.task.sync.pendingTitle')
})

function formatTime(timestamp?: number) {
  if (!timestamp) return ''
  const date = new Date(timestamp)
  const pad = (value: number) => String(value).padStart(2, '0')
  const clock = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  if (date.toDateString() === new Date().toDateString()) return clock
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock}`
}

async function refresh() {
  if (!props.taskUuid || props.sourceType !== 'cloud') return
  try {
    status.value = await fetchTaskSyncStatus(props.taskUuid)
    if (
      status.value.status === 'success' &&
      (status.value.pending_sessions || 0) === 0 &&
      (status.value.pending_executions || 0) === 0
    ) {
      retryDetail.value = ''
    }
  } catch {
    // 状态查询失败不打扰用户：保留上次状态，下一轮继续
  }
}

// 有未投递/失败的变更时短周期轮询，追平后停止；从未产生过快照的空跑任务不轮询。
function schedule() {
  if (timer) clearTimeout(timer)
  if (!visible.value) return
  const current = status.value
  const active =
    state.value === 'failed' ||
    (current?.pending_sessions || 0) > 0 ||
    (current?.pending_executions || 0) > 0 ||
    (Boolean(current?.revision) && (current?.delivered_revision || 0) < (current?.revision || 0))
  if (!active) return
  timer = setTimeout(async () => {
    await refresh()
    schedule()
  }, 5000)
}

async function resync() {
  if (retrying.value) return
  retrying.value = true
  try {
    status.value = await retryTaskSync(props.taskUuid)
    const detail = status.value?.retry_error_detail || status.value?.retry_error || ''
    if (detail) {
      retryDetail.value = detail
      message.error(detail)
    } else {
      retryDetail.value = ''
      message.success(t('workflows.task.sync.resynced'))
    }
    emit('resync')
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.sync.resyncFailed'),
    )
  } finally {
    retrying.value = false
    schedule()
  }
}

watch(
  () => props.taskUuid,
  async () => {
    status.value = undefined
    await refresh()
    schedule()
  },
  { immediate: true },
)

watch(
  () => props.refreshKey,
  async () => {
    await refresh()
    schedule()
  },
)

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<style scoped>
.task-sync {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  line-height: 20px;
}
.sync-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 8px;
  border-radius: 10px;
  color: #8c8c8c;
  background: #f5f6f8;
  cursor: default;
}
.task-sync.is-success .sync-chip {
  color: #15803d;
  background: #ecfdf3;
}
.task-sync.is-failed .sync-chip {
  color: #b42318;
  background: #fef2f2;
}
.sync-chip :deep(svg) {
  width: 12px;
  height: 12px;
}
.sync-time {
  color: inherit;
  opacity: 0.72;
}
.sync-retry {
  padding: 0 6px;
  border: 0;
  border-radius: 6px;
  color: #3157e2;
  background: transparent;
  font-size: 12px;
  line-height: 20px;
  cursor: pointer;
}
.sync-retry:hover {
  background: #e5efff;
}
.sync-retry:disabled {
  color: #bfbfbf;
  cursor: not-allowed;
}
.task-sync.is-failed .sync-retry {
  color: #b42318;
}
.task-sync.is-failed .sync-retry:hover {
  background: #fee4e2;
}
.sync-retry:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}
</style>
