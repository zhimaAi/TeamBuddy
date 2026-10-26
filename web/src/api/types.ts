/** * Basic type definition */

/** User information */
export interface UserInfo {
  id: string
  accountScope?: string
  sessionGeneration?: string
  serverUrl?: string
  serverType?: 'official' | 'custom'
  username: string
  displayName?: string
  avatar?: string
  role?: string
}

/** Login request parameters */
export interface LoginParams {
  username: string
  password: string
}

/** Login response */
export interface LoginResult {
  token: string
  user: UserInfo
}

/** Task status */
export type TaskStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled'

/** Task priority */
export type TaskPriority = 'low' | 'medium' | 'high'

/** Task information */
export interface Task {
  id: string
  title: string
  description?: string
  status: TaskStatus
  priority: TaskPriority
  createdAt: string
  updatedAt: string
}

/** Task list query parameters */
export interface TaskListParams {
  page?: number
  pageSize?: number
  status?: TaskStatus
  keyword?: string
}

/** Pagination results */
export interface PageResult<T> {
  list: T[]
  total: number
  page: number
  pageSize: number
}

/** Setting items */
export interface AppSettings {
  theme: 'light' | 'dark'
  language: string
  apiBaseUrl?: string
}
