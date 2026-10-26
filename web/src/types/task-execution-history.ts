import type { PipelineStep, SessionEventItem, TaskExecutionMode, TaskProgress } from './pipeline'
import type { CloudSyncState } from './task-sync'

export interface TaskExecutionHistorySummary {
  uuid: string
  task_uuid: string
  execution_no: number
  execution_mode: TaskExecutionMode
  execution_tool: string
  target_name: string
  model_name: string
  started_at: number
  archived_at: number
}

export interface TaskExecutionHistoryDetail extends TaskExecutionHistorySummary {
  version: number
  steps: PipelineStep[]
  progress: TaskProgress[]
  session_events: Record<string, SessionEventItem[]>
  /** 这次执行整体是否已上报线上：由后端按上报队列实时推导，一次执行就是一个上报快照 */
  cloud_sync_status?: CloudSyncState
  cloud_sync_error?: string
  cloud_synced_at?: number
}
