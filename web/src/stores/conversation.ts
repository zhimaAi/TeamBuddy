import { computed, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { defineStore } from 'pinia'
import apiClient from '@/api/client'
import type { TaskNotification } from '@/types/pipeline'
import type { LocalProject } from '@/types/project'
import { t } from '@/i18n'
import { removeDeletedTaskFromCache } from '@/utils/taskConversationCache'

export interface TaskConversation {
  task_uuid: string
  title: string
  work_dir: string
  project_uuid: string
  items: TaskNotification[]
  latest: TaskNotification
  running: boolean
  unread: boolean
  is_archived: boolean
}

export interface ConversationGroup {
  key: string
  workDir: string
  displayName: string
  fullPath: string
  projectName?: string
  pinnedAt: number
  items: TaskConversation[]
}

interface SidebarWorkspace {
  directory_key: string
  work_dir: string
  display_name: string
  pinned_at: number
}

export interface WorkspaceDeletionPreview {
  items: { uuid: string; title: string; is_archived: boolean }[]
}

type TaskStatus = NonNullable<TaskNotification['task_status']>

const ARCHIVED_COLLAPSED_KEY = 'goteams.conversation.archivedCollapsed'
const WORK_DIR_HISTORY_KEY = 'goteams.workDirectories.history'

function normalizePath(p: string): string {
  let value = (p || '').trim()
  // \\?\ 与 \\?\UNC\ 只是 Windows 扩展路径前缀，指向的仍是同一目录。
  if (/^\\\\\?\\unc\\/i.test(value)) {
    value = `\\\\${value.slice(8)}`
  } else if (/^\\\\\?\\/i.test(value)) {
    value = value.slice(4)
  }
  if (!value) return ''
  value = value.replace(/\\/g, '/')
  const windowsPath = /^[a-z]:\//i.test(value) || value.startsWith('//')
  if (value !== '/' && !/^[a-z]:\/$/i.test(value)) value = value.replace(/\/+$/, '')
  return windowsPath ? value.toLowerCase() : value
}

export function getLastDirSegment(p: string): string {
  const clean = (p || '').replace(/\\/g, '/').replace(/\/+$/, '').trim()
  if (!clean) return ''
  const parts = clean.split('/')
  return parts[parts.length - 1] || clean
}

export const useConversationStore = defineStore('conversation', () => {
  const rawNotifications = ref<TaskNotification[]>([])
  const projects = ref<LocalProject[]>([])
  const workspacePreferences = ref<SidebarWorkspace[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')
  const searchQuery = ref('')
  const archivedCollapsed = ref(
    typeof window !== 'undefined' ? localStorage.getItem(ARCHIVED_COLLAPSED_KEY) !== 'false' : true,
  )
  const collapsedGroupKeys = ref<Set<string>>(new Set())
  const dirHistory = ref<string[]>([])
  const taskStatuses = ref<Record<string, TaskStatus>>({})
  let loadVersion = 0
  let workspaceLoadVersion = 0
  const auth = useAuthStore()
  watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => {
    loadVersion += 1
    rawNotifications.value = []
    taskStatuses.value = {}
    loaded.value = false
    loading.value = false
    void loadConversations(true)
  }, { flush: 'sync' })

  function normalizeTaskStatus(status?: string): TaskStatus | undefined {
    if (status === 'pending' || status === 'active' || status === 'done' || status === 'blocked') {
      return status
    }
    return undefined
  }

  function setTaskStatus(taskUuid: string, status?: string) {
    const normalized = normalizeTaskStatus(status)
    if (!taskUuid || !normalized) return
    taskStatuses.value[taskUuid] = normalized
    for (const item of rawNotifications.value) {
      if (item.task_uuid === taskUuid) item.task_status = normalized
    }
  }

  function loadDirHistory() {
    try {
      const raw = localStorage.getItem(WORK_DIR_HISTORY_KEY)
      if (raw) {
        dirHistory.value = JSON.parse(raw) as string[]
      }
    } catch {
      dirHistory.value = []
    }
  }

  function recordWorkingDir(dir: string) {
    if (!dir || !dir.trim()) return
    const trimmed = dir.trim()
    const normalized = normalizePath(trimmed)
    const next = [trimmed, ...dirHistory.value.filter((d) => normalizePath(d) !== normalized)].slice(0, 50)
    dirHistory.value = next
    try {
      localStorage.setItem(WORK_DIR_HISTORY_KEY, JSON.stringify(next))
    } catch {
      // 忽略存储失败
    }
  }

  loadDirHistory()

  const allKnownWorkDirs = computed(() => {
    const set = new Set<string>()
    for (const d of dirHistory.value) {
      if (d) set.add(d)
    }
    for (const p of projects.value) {
      if (p.local_dir) set.add(p.local_dir)
    }
    for (const n of rawNotifications.value) {
      if (n.work_dir) set.add(n.work_dir)
    }
    return Array.from(set)
  })

  // 解析工作目录展示名称与匹配的项目
  function resolveWorkDirInfo(workDir: string): { displayName: string; fullPath: string; projectName?: string } {
    const trimmed = (workDir || '').trim()
    if (!trimmed) {
      return {
        displayName: t('workflows.task.common.defaultDirectory') || '默认目录',
        fullPath: '',
      }
    }
    const norm = normalizePath(trimmed)
    const matchedProject = projects.value.find((p) => p.local_dir && normalizePath(p.local_dir) === norm)
    const preference = workspacePreferences.value.find((item) => item.directory_key === norm)
    return {
      displayName: preference?.display_name || matchedProject?.name || getLastDirSegment(trimmed),
      fullPath: trimmed,
      projectName: matchedProject?.name,
    }
  }

  const allConversations = computed<TaskConversation[]>(() => {
    const grouped = new Map<string, TaskNotification[]>()
    for (const item of rawNotifications.value) {
      const items = grouped.get(item.task_uuid) || []
      items.push(item)
      grouped.set(item.task_uuid, items)
    }

    return [...grouped.entries()]
      .map(([taskUuid, items]) => {
        items.sort((a, b) => b.created_at - a.created_at)
        const latest = items[0]
        const taskStatus = latest.task_status || taskStatuses.value[taskUuid]
        const workDir = items.find((i) => i.work_dir)?.work_dir || ''
        const projectUuid = items.find((i) => i.project_uuid)?.project_uuid || ''
        const isArchived = items.some((i) => i.is_archived)

        return {
          task_uuid: taskUuid,
          title:
            latest.task_title ||
            latest.title ||
            t('workflows.task.feedback.taskFallback', { id: taskUuid.slice(0, 8) }),
          work_dir: workDir,
          project_uuid: projectUuid,
          items,
          latest,
          running:
            latest.execution_mode !== 'vibe_coding' &&
            taskStatus === 'active' &&
            (latest.status || latest.terminal_status) === 'running',
          unread: items.some((i) => !i.is_read),
          is_archived: isArchived,
        }
      })
      .sort((a, b) => b.latest.created_at - a.latest.created_at)
  })

  function filterBySearch(conv: TaskConversation): boolean {
    const q = searchQuery.value.trim().toLowerCase()
    if (!q) return true
    return (
      conv.title.toLowerCase().includes(q) ||
      conv.work_dir.toLowerCase().includes(q) ||
      resolveWorkDirInfo(conv.work_dir).displayName.toLowerCase().includes(q) ||
      (resolveWorkDirInfo(conv.work_dir).projectName || '').toLowerCase().includes(q)
    )
  }

  const activeConversations = computed(() =>
    allConversations.value.filter((c) => !c.is_archived && filterBySearch(c)),
  )

  const archivedConversations = computed(() =>
    allConversations.value.filter((c) => c.is_archived && filterBySearch(c)),
  )

  const unreadCount = computed(
    () => allConversations.value.filter((c) => !c.is_archived && c.unread).length,
  )

  function groupList(convs: TaskConversation[], includeEmptyWorkspaces = false): ConversationGroup[] {
    const groupsMap = new Map<string, TaskConversation[]>()
    for (const conv of convs) {
      // 斜杠、盘符大小写、尾部分隔符和 \\?\ 前缀不同，仍是同一目录。
      const dirKey = normalizePath(conv.work_dir)
      const list = groupsMap.get(dirKey) || []
      list.push(conv)
      groupsMap.set(dirKey, list)
    }

    if (includeEmptyWorkspaces) {
      const query = searchQuery.value.trim().toLowerCase()
      for (const preference of workspacePreferences.value) {
        const info = resolveWorkDirInfo(preference.work_dir)
        if (!groupsMap.has(preference.directory_key) &&
          (!query || [info.displayName, info.projectName || '', preference.work_dir].some((value) => value.toLowerCase().includes(query)))) {
          groupsMap.set(preference.directory_key, [])
        }
      }
    }
    const groups: ConversationGroup[] = []
    for (const [dirKey, items] of groupsMap.entries()) {
      const project = dirKey
        ? projects.value.find((item) => item.local_dir && normalizePath(item.local_dir) === dirKey)
        : undefined
      const preference = workspacePreferences.value.find((item) => item.directory_key === dirKey)
      const rawDir = preference?.work_dir || project?.local_dir
        || items.map((item) => item.work_dir.trim()).find(Boolean)
        || ''
      const info = resolveWorkDirInfo(rawDir)
      groups.push({
        key: dirKey || '__empty__',
        workDir: rawDir,
        displayName: info.displayName,
        fullPath: info.fullPath,
        projectName: info.projectName,
        pinnedAt: preference?.pinned_at || 0,
        items,
      })
    }

    return groups.sort((a, b) => b.pinnedAt - a.pinnedAt)
  }

  const activeGroups = computed(() => groupList(activeConversations.value, true))
  const archivedGroups = computed(() => groupList(archivedConversations.value))

  async function loadWorkspacePreferences() {
    const version = ++workspaceLoadVersion
    const res = await apiClient.get<{ items: SidebarWorkspace[] }>('/tasks/workspaces')
    if (version === workspaceLoadVersion) workspacePreferences.value = res.items || []
  }

  async function updateWorkspace(workDir: string, changes: { display_name?: string; pinned?: boolean; create_only?: boolean }) {
    const item = await apiClient.put<SidebarWorkspace>('/tasks/workspaces', { work_dir: workDir, ...changes })
    workspaceLoadVersion += 1
    const index = workspacePreferences.value.findIndex((entry) => entry.directory_key === item.directory_key)
    if (index < 0) workspacePreferences.value.push(item)
    else workspacePreferences.value[index] = item
  }

  async function previewWorkspaceDeletion(workDir: string) {
    return apiClient.post<WorkspaceDeletionPreview>('/tasks/workspaces/delete-preview', { work_dir: workDir })
  }

  async function deleteWorkspaceTasks(workDir: string, taskUuids: string[]) {
    const request = taskUuids.length ? apiClient.post : apiClient.delete
    const result = await request<{ deleted_task_uuids: string[]; cleanup_failed_task_uuids: string[]; warning?: string }>(
      taskUuids.length ? '/tasks/workspaces/delete' : '/tasks/workspaces',
      { work_dir: workDir, task_uuids: taskUuids },
    )
    loadVersion += 1
    loading.value = false
    workspaceLoadVersion += 1
    const deleted = new Set(result.deleted_task_uuids)
    rawNotifications.value = rawNotifications.value.filter((item) => !deleted.has(item.task_uuid))
    workspacePreferences.value = workspacePreferences.value.filter((item) => item.directory_key !== normalizePath(workDir))
    collapsedGroupKeys.value.delete(normalizePath(workDir))
    dirHistory.value = dirHistory.value.filter((dir) => normalizePath(dir) !== normalizePath(workDir))
    try {
      localStorage.setItem(WORK_DIR_HISTORY_KEY, JSON.stringify(dirHistory.value))
    } catch {
      // 存储失败不改变已完成的删除结果。
    }
    await Promise.allSettled(result.deleted_task_uuids.map(taskUuid => removeDeletedTaskFromCache(taskUuid)))
    // 再加载可见数据，保留预览后新建的任务；刷新失败不误报删除失败。
    void loadConversations(true)
    return result
  }

  async function loadProjects() {
    try {
      const res = await apiClient.get<{ items: LocalProject[] }>('/projects')
      projects.value = res.items || []
    } catch {
      // 容错保留空数组
    }
  }

  async function loadConversations(force = false) {
    if (loading.value && !force) return
    const requestVersion = ++loadVersion
    loading.value = true
    error.value = ''
    try {
      const [notificationResult, , workspaceResult] = await Promise.allSettled([
        apiClient.get<{ items: TaskNotification[] }>('/notifications', {
          include_archived: 1,
        }),
        loadProjects(),
        loadWorkspacePreferences(),
      ])
      if (requestVersion !== loadVersion) return
      if (notificationResult.status === 'rejected') throw notificationResult.reason
      const nextNotifications = notificationResult.value.items || []
      for (const item of nextNotifications) {
        const responseStatus = normalizeTaskStatus(item.task_status)
        if (responseStatus) {
          taskStatuses.value[item.task_uuid] = responseStatus
        } else if (taskStatuses.value[item.task_uuid]) {
          item.task_status = taskStatuses.value[item.task_uuid]
        }
      }
      rawNotifications.value = nextNotifications
      loaded.value = true
      if (workspaceResult.status === 'rejected') {
        error.value = workspaceResult.reason instanceof Error ? workspaceResult.reason.message : t('layout.sidebar.workspaceActionFailed')
      }
    } catch (err) {
      if (requestVersion === loadVersion) {
        error.value = err instanceof Error ? err.message : '加载失败'
      }
    } finally {
      if (requestVersion === loadVersion) loading.value = false
    }
  }

  async function setTaskRead(taskUuid: string, isRead: boolean) {
    await apiClient.put(`/notifications/tasks/${encodeURIComponent(taskUuid)}/read`, {
      is_read: isRead,
    })
    for (const item of rawNotifications.value) {
      if (item.task_uuid === taskUuid) {
        item.is_read = isRead
      }
    }
  }

  async function archiveTask(taskUuid: string, isArchived: boolean) {
    await apiClient.put(`/notifications/tasks/${encodeURIComponent(taskUuid)}/archive`, {
      is_archived: isArchived,
    })
    for (const item of rawNotifications.value) {
      if (item.task_uuid === taskUuid) {
        item.is_archived = isArchived
      }
    }
  }

  function toggleGroupCollapse(key: string) {
    if (collapsedGroupKeys.value.has(key)) {
      collapsedGroupKeys.value.delete(key)
    } else {
      collapsedGroupKeys.value.add(key)
    }
  }

  async function renameTask(taskUuid: string, title: string) {
    const result = await apiClient.put<{ uuid: string; title: string }>(
      `/tasks/${encodeURIComponent(taskUuid)}/title`, { title },
    )
    // Discard list responses started before this mutation.
    loadVersion += 1
    loading.value = false
    for (const item of rawNotifications.value) {
      if (item.task_uuid === taskUuid) item.task_title = result.title
    }
    return result
  }

  async function deleteTask(taskUuid: string) {
    await apiClient.delete<{ uuid: string; deleted: boolean; warning?: string }>(
      `/tasks/${encodeURIComponent(taskUuid)}`,
    )
    loadVersion += 1
    loading.value = false
    rawNotifications.value = rawNotifications.value.filter((item) => item.task_uuid !== taskUuid)
    // Cache failures must not turn a completed server deletion into an error.
    await removeDeletedTaskFromCache(taskUuid).catch(() => {})
  }

  function isGroupCollapsed(key: string): boolean {
    return collapsedGroupKeys.value.has(key)
  }

  function toggleArchivedCollapse() {
    archivedCollapsed.value = !archivedCollapsed.value
    try {
      localStorage.setItem(ARCHIVED_COLLAPSED_KEY, String(archivedCollapsed.value))
    } catch {
      // 忽略
    }
  }

  function setSearchQuery(q: string) {
    searchQuery.value = q
  }

  function upsertNotification(item: TaskNotification) {
    const idx = rawNotifications.value.findIndex((n) => n.uuid === item.uuid)
    if (idx >= 0) {
      rawNotifications.value[idx] = { ...rawNotifications.value[idx], ...item }
    } else {
      rawNotifications.value.unshift(item)
    }
  }

  return {
    rawNotifications,
    projects,
    loading,
    loaded,
    error,
    searchQuery,
    archivedCollapsed,
    dirHistory,
    allKnownWorkDirs,
    allConversations,
    activeConversations,
    archivedConversations,
    activeGroups,
    archivedGroups,
    unreadCount,
    resolveWorkDirInfo,
    loadConversations,
    loadProjects,
    setTaskRead,
    archiveTask,
    renameTask,
    updateWorkspace,
    previewWorkspaceDeletion,
    deleteWorkspaceTasks,
    deleteTask,
    toggleGroupCollapse,
    isGroupCollapsed,
    toggleArchivedCollapse,
    setSearchQuery,
    recordWorkingDir,
    upsertNotification,
    setTaskStatus,
  }
})
