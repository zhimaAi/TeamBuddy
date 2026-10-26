export interface TaskBoardLane {
  id: number
  lane_key: string
  title: string
  color: string
  sort_order: number
  is_hidden: number
  task_count: number
}

export type BoardLayoutMode = 'board' | 'list'

export interface TaskBoardListResponse {
  items: TaskBoardTask[]
  total: number
  page: number
  page_size: number
  status_counts: Record<string, number>
}

export type BoardListActionKind = 'configure' | 'detail'

export interface BoardListAction {
  kind: BoardListActionKind
  label: string
}

export interface BoardListFilter {
  key: string
  label: string
  color: string
}

export interface BoardListRow {
  key: string
  title: string
  status: string
  statusLabel: string
  statusColor: string
  priority?: string | number
  executorName: string
  executorAvatar?: string
  updatedAt?: number | string
  activity?: TaskLatestActivity
  actions: BoardListAction[]
  taskUuid?: string
  workItemKey?: string
}

export interface TaskBoardTask {
  uuid: string
  title: string
  status: string
  execution_status: string
  agent_id: string
  work_item_type: string
  work_item_id: string
  created_at: number
  updated_at: number
  content_snapshot?: string
  agent_name_snapshot?: string
  description?: string
  pipeline_snapshot_uuid?: string
  selected_pipeline_uuid?: string
  execution_mode?: import('@/types/pipeline').TaskExecutionMode
  execution_tool?: string
  execution_tool_name?: string
  execution_bound?: boolean
  execution_index?: number
  // CLI 直接执行：模型由看板接口透出（若后端返回），缺省回退隐式步骤。
  execution_model?: string
  pipeline_name_snapshot?: string
  pipeline_avatar_snapshot?: string
  selected_expert_group_uuid?: string
  expert_group_snapshot_uuid?: string
  expert_group_name_snapshot?: string
  expert_group_avatar_snapshot?: string
  priority?: string
  planned_end_date?: number | string
  blocked_reason?: string
  latest_activity?: TaskLatestActivity
}

export type TaskActivityStatus = 'running' | 'completed' | 'error'
export type TaskActivityActor = 'user' | 'tool' | 'system'

export interface TaskLatestActivity {
  id: string
  status: TaskActivityStatus
  actor: TaskActivityActor
  kind: string
  preview: string
  occurred_at: number
}

export interface TaskActivityDetail extends Omit<TaskLatestActivity, 'preview'> {
  content: string
}

export type TaskViewMode = 'drawer' | 'modal' | 'new-window'
