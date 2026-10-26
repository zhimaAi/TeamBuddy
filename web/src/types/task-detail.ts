import type { LocalTask } from '@/types/pipeline'

export interface TaskRelatedProject {
  uuid: string
  name: string
  icon?: string
  icon_type?: string
  main_dir?: string
  local_dir?: string
  relation_type?: string
  sort_order?: number
}

export type TaskWithDetails = LocalTask & {
  awaiting_message?: boolean
  execution_mode?: import('@/types/pipeline').TaskExecutionMode
  execution_tool?: string
  // CLI 直接执行来自隐式步骤；Vibe Coding 来自独立保存的启动模型配置。
  execution_model?: string
  selected_pipeline_uuid?: string
  pipeline_avatar_snapshot?: string
  selected_expert_group_uuid?: string
  expert_group_snapshot_uuid?: string
  expert_group_name_snapshot?: string
  expert_group_avatar_snapshot?: string
  project_name?: string
  project_uuid?: string
  project_icon?: string
  projects?: TaskRelatedProject[]
  priority?: string
  planned_start_date?: string | number
  planned_end_date?: string | number
}
