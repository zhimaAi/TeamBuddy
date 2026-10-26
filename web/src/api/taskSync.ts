import apiClient from './client'
import type { TaskSyncStatus } from '@/types/task-sync'

/** 读取任务级上报状态（任务投影 + 沟通会话） */
export function fetchTaskSyncStatus(taskUuid: string) {
  return apiClient.get<TaskSyncStatus>(`/tasks/${encodeURIComponent(taskUuid)}/sync-status`)
}

/**
 * 手动重新上报：后端会清掉退避等待并强制推进一次 revision。
 * 失败时仍返回 200，具体结果看返回的 status / last_error / retry_error。
 */
export function retryTaskSync(taskUuid: string) {
  return apiClient.post<TaskSyncStatus>(`/tasks/${encodeURIComponent(taskUuid)}/sync`, {})
}
