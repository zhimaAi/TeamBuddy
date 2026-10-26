import type { CloudSyncState } from './task-sync'

export type PipelineSource = 'local' | 'cloud'
export type StepBusinessStatus = 'pending' | 'active' | 'completed'
export type ExecutionStatus =
  'idle' | 'created' | 'running' | 'success' | 'failed' | 'stopped' | 'interrupted' | ''

export interface ConversationRuntimeConfig {
  cli_type: string
  model_name: string
}

export interface PipelineStep {
  uuid: string
  pipeline_uuid?: string
  source_step_id?: string | number
  local_step_uuid?: string
  cloud_step_id?: string | number
  step_key?: string
  name: string
  description?: string
  avatar?: string
  prompt?: string
  prompt_snapshot?: string
  cli_type: string
  model?: string
  model_name?: string
  sort_order: number
  status?: StepBusinessStatus
  execution_status?: ExecutionStatus
  step_dir?: string
  member_role?: 'leader' | 'member' | ''
}

export interface ExpertMember {
  uuid: string
  expert_group_uuid: string
  member_role: 'leader' | 'member'
  name: string
  description?: string
  avatar?: string
  prompt: string
  cli_type: string
  model_name: string
  created_at?: number
  updated_at?: number
}

export interface ExpertGroup {
  uuid: string
  name: string
  description?: string
  avatar?: string
  ready: boolean
  /** 云端团队资源标记；未传视为本地私有资源。 */
  source_type?: 'cloud' | 'local'
  leader?: ExpertMember
  members: ExpertMember[]
  created_at?: number
  updated_at?: number
}

export interface ReusableAgent {
  uuid: string
  source: 'pipeline' | 'expert_group'
  source_name: string
  name: string
  description?: string
  avatar?: string
  prompt: string
  cli_type: string
  model_name: string
}

export interface Pipeline {
  uuid: string
  name: string
  description?: string
  source?: PipelineSource
  source_type?: PipelineSource
  avatar?: string
  enabled?: boolean | number
  steps?: PipelineStep[]
  step_count?: number
  created_at?: number
  updated_at?: number
}

export interface TaskProgress {
  uuid: string
  task_uuid: string
  task_step_uuid: string
  record_type?: string
  user_prompt?: string
  question?: string
  status: ExecutionStatus
  final_result?: string
  result?: string
  cli_type?: string
  model?: string
  session_uuid?: string
  execution_mode?: TaskExecutionMode
  external_session_id?: string
  dispatch_status?: string
  dispatch_message?: string
  latest_event_type?: string
  latest_event_content?: string
  latest_event_at?: number
  /** 会话 token 用量。部分 CLI 不上报（如 copilot/kimi），此时为 0 */
  input_tokens?: number
  output_tokens?: number
  total_tokens?: number
  /** 会话实际执行耗时（毫秒），由后端按 started_at / finished_at 计算 */
  duration_ms?: number
  /** 该会话沟通记录上报线上（云端）的状态 */
  cloud_sync_status?: CloudSyncState
  /** 上报失败原因，仅 failed 时有值 */
  cloud_sync_error?: string
  /** 最近一次上报成功的实际时间（毫秒），0 表示从未成功上报 */
  cloud_synced_at?: number
  created_at: number
  started_at?: number
  finished_at?: number
}

export type TaskExecutionMode = '' | 'pipeline' | 'expert_group' | 'vibe_coding' | 'cli'

/** CLI 执行过程事件（思考 / 中间输出 / 工具调用 / 权限请求），与 WS executor.event 的 event 字段同构 */
export interface ExecutionEvent {
  type: string
  content?: string
  session_id?: string
  /** 协议侧工具调用 ID，用于把 tool_result 精确归属到 tool_call；协议不提供时为空 */
  tool_use_id?: string
  /** 非交互模式下的工具授权请求项，用于「执行过程」里的等待授权行 */
  permission_requests?: { tool_name?: string; tool_input?: string }[]
  timestamp?: number
  error?: string
  tool_result_interrupted?: boolean
  interrupted_attempt?: number
  activity?: { kind?: string; attempt?: number; max?: number; detail?: string; escalated?: boolean }
}

/** GET /tasks/sessions/:uuid/events 返回的执行过程事件条目 */
export interface SessionEventItem {
  session_uuid: string
  sequence: number
  event_type: string
  event?: ExecutionEvent
  created_at: number
}

export interface CompleteStepResponse {
  task_done: boolean
  next_step_uuid?: string
  session_uuid?: string
  start_error?: string
  auto_started: boolean
}

export interface LocalTask {
  uuid: string
  created_from_conversation?: boolean
  title: string
  description?: string
  status: string
  execution_status?: ExecutionStatus
  execution_mode?: TaskExecutionMode
  execution_tool?: string
  execution_tool_name?: string
  execution_bound?: boolean
  execution_index?: number
  source_type?: PipelineSource
  work_item_type?: string
  work_item_id?: string | number
  workspace_id?: number | string
  task_dir?: string
  work_dir?: string
  work_dirs?: string[]
  current_step_uuid?: string
  current_step_completed?: boolean | number
  pipeline_snapshot_uuid?: string
  pipeline_name_snapshot?: string
  selected_expert_group_uuid?: string
  expert_group_snapshot_uuid?: string
  expert_group_name_snapshot?: string
  expert_group_avatar_snapshot?: string
  created_at: number
  updated_at: number
  steps: PipelineStep[]
}

export interface TaskNotification {
  uuid: string
  task_uuid: string
  task_title?: string
  task_status?: 'pending' | 'active' | 'done' | 'blocked'
  title?: string
  task_step_uuid: string
  step_name?: string
  step_sort_order?: number
  step_order?: number
  progress_uuid?: string
  status?: ExecutionStatus
  terminal_status?: ExecutionStatus
  execution_mode?: TaskExecutionMode
  execution_tool?: string
  summary?: string
  is_read?: boolean | number
  is_archived?: boolean
  work_dir?: string
  project_uuid?: string
  created_at: number
}
