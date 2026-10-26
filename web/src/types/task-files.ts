/** 产出文档上报状态（与后端 gt_task_document_uploads.status 对应） */
export type TaskFileUploadStatus =
  | 'pending'
  | 'uploading'
  | 'synced'
  | 'failed'
  | 'skipped'
  | 'deleted'

/** 单个文件的产出文档上报状态 */
export interface TaskFileUploadState {
  status: TaskFileUploadStatus
  attempts?: number
  last_error?: string
  uploaded_at?: number
  doc_key?: string
  remote_revision?: number
  /** 是否可手动重报（命中上报规则的文件才可重报） */
  reuploadable?: boolean
}

/** 目录节点下所有文件的上报状态聚合 */
export interface TaskFileUploadSummary {
  total: number
  synced: number
  failed: number
  pending: number
  skipped: number
}

export interface TaskFileNode {
  name: string
  path: string
  absolute_path: string
  is_dir: boolean
  file_type?: string
  size?: number
  modified_at?: number
  owner_step_uuid?: string
  owner_step_name?: string
  children?: TaskFileNode[]
  /** 上报开关关闭时为 null */
  upload?: TaskFileUploadState | null
  upload_summary?: TaskFileUploadSummary
}

/**
 * 可被 # 引用的任务产出文件。
 * 只要求绝对路径可序列化；CLI 直接执行等模式不在步骤子目录下工作，
 * 这类文件没有 owner_step_uuid，但同样是任务产出，必须可被引用。
 */
export interface MentionableTaskFile extends TaskFileNode {
  absolute_path: string
}

export interface TaskFilesResponse {
  task_dir: string
  tree: TaskFileNode[]
  /** 产出文档上报是否启用；关闭时节点不带 upload 字段 */
  upload_enabled?: boolean
  /** 任务级「同步 Agent 产出」开关；false 表示该任务默认不上报文件 */
  auto_upload_enabled?: boolean
}

export interface TaskFileContentResponse {
  path: string
  file_type: string
  size: number
  encoding?: string
  content: string
}

/** 单文件上报状态轮询返回项 */
export interface TaskFileUploadStatusItem extends TaskFileUploadState {
  path: string
}

export interface TaskFileUploadStatusResponse {
  items: TaskFileUploadStatusItem[]
  enabled?: boolean
}

export interface TaskFileReuploadResponse {
  queued: boolean
  paths?: string[]
  doc_key?: string
}

/** 任务级手动上报（关闭「同步agent产出」时使用） */
export interface TaskFileReportResponse {
  queued: boolean
  count: number
}
