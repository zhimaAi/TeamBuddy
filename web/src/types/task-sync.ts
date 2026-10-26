/** 执行记录上报线上（云端）的状态 */
export type CloudSyncState = 'success' | 'pending' | 'failed'

/**
 * 任务级上报状态：任务投影 + 全部步骤会话是否已投递到云端。
 * 对应 GET /api/local/tasks/:uuid/sync-status。
 */
export interface TaskSyncStatus {
  /** 该任务是否参与上报（只有云端来源的任务参与） */
  enabled: boolean
  /** 当前是否已登录云端账号 */
  logged_in: boolean
  /** 该任务是否归属当前登录账号；false 时后端会拒绝推送 */
  owned: boolean
  /** 不可上报的原因，可上报时为空 */
  reason?: 'not_cloud' | 'logged_out' | 'not_owned'
  source_type?: string
  status: CloudSyncState
  /** 连续失败次数，成功后清零 */
  attempts: number
  /** 最近一次失败原因 */
  last_error: string
  /** 最近一次上报成功的实际时间（毫秒），0 表示从未成功 */
  last_success_at: number
  revision: number
  delivered_revision: number
  /** 尚未上报成功的沟通会话条数 */
  pending_sessions: number
  /** 尚未投递成功的执行序号数量（含历史序号） */
  pending_executions: number
  /** 手动重报失败时的本地化提示 */
  retry_error?: string
  /** 手动重报失败的真实原因，用于定位（账号不匹配 / 工作项缺失 / 云端拒绝等） */
  retry_error_detail?: string
}
