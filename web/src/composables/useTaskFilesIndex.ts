import { ref } from 'vue'
import apiClient from '@/api/client'
import type { MentionableTaskFile, TaskFileNode, TaskFilesResponse } from '@/types/task-files'

export type TaskFilesIndexStatus = 'idle' | 'loading' | 'ready' | 'error'

export interface TaskFilesIndexEntry {
  status: TaskFilesIndexStatus
  files: MentionableTaskFile[]
  error: string
  updatedAt: number
  pending: Promise<void> | null
}

/** 任务产出文件在会话内的缓存时长，超时后在下次唤出引用菜单时静默刷新 */
const CACHE_TTL = 60 * 1000

/** 列表为空时的重查间隔：任务刚建、产出还没生成时，不该把空结果缓存满一个 TTL */
const EMPTY_RETRY_DELAY = 5 * 1000

const entries = new Map<string, TaskFilesIndexEntry>()

/**
 * 清空产出文件索引缓存。
 * 登录 / 退出登录后必须调用：索引按 taskUuid 跨页面存活，不清会继续命中上一个账号的数据。
 */
export function clearTaskFilesIndexCache() {
  entries.clear()
}

function createEntry(): TaskFilesIndexEntry {
  return { status: 'idle', files: [], error: '', updatedAt: 0, pending: null }
}

function entryFor(taskUuid: string): TaskFilesIndexEntry {
  const existing = entries.get(taskUuid)
  if (existing) return existing
  const created = createEntry()
  entries.set(taskUuid, created)
  return created
}

/**
 * 任务产出文件索引。
 *
 * 输入框需要在按下一个字符的瞬间就判断「有没有匹配」，而文件列表接口是一次递归扫盘。
 * 因此这里按 taskUuid 缓存列表并在挂载时预加载，让过滤退化为纯内存计算：
 * 既消除了每次输入 # 都发请求带来的卡顿，也让菜单不必先弹出再消失。
 * 与任务无关的调用方（如会话切换）用同一个 entry 对象共享状态，避免重复请求。
 */
export function useTaskFilesIndex() {
  const taskUuid = ref('')
  const entry = ref<TaskFilesIndexEntry>(createEntry())

  function bind(nextTaskUuid: string) {
    if (taskUuid.value === nextTaskUuid) return
    taskUuid.value = nextTaskUuid
    entry.value = nextTaskUuid ? entryFor(nextTaskUuid) : createEntry()
  }

  async function load(force = false): Promise<void> {
    const target = taskUuid.value
    if (!target) return

    const current = entry.value
    if (current.pending) return current.pending
    if (!force && current.status === 'ready' && Date.now() - current.updatedAt < CACHE_TTL) return

    const request = (async () => {
      // 已有可用列表时保持 ready，让后台刷新不打断用户正在浏览的候选
      if (current.status !== 'ready') current.status = 'loading'
      current.error = ''
      try {
        const response = await apiClient.get<TaskFilesResponse>(
          `/tasks/${encodeURIComponent(target)}/files`,
        )
        if (entry.value !== current) return
        current.files = collectMentionableFiles(response.tree || [])
        current.status = 'ready'
        // 空列表多半是任务刚建、产出还没生成，缩短有效期让下次唤出立即重查
        current.updatedAt = current.files.length
          ? Date.now()
          : Date.now() - (CACHE_TTL - EMPTY_RETRY_DELAY)
      } catch (error) {
        if (entry.value !== current) return
        // 单次失败不清空已有列表，仅记录真实原因，兜底文案由展示层提供
        current.error = error instanceof Error ? error.message : ''
        current.status = current.files.length ? 'ready' : 'error'
      } finally {
        current.pending = null
      }
    })()

    current.pending = request
    return request
  }

  function invalidate(target = taskUuid.value) {
    if (!target) return
    entries.delete(target)
    if (taskUuid.value === target) entry.value = createEntry()
  }

  return { taskUuid, entry, bind, load, invalidate }
}

function collectMentionableFiles(nodes: TaskFileNode[]): MentionableTaskFile[] {
  const files: MentionableTaskFile[] = []
  for (const node of nodes) {
    if (node.is_dir) {
      files.push(...collectMentionableFiles(node.children || []))
      continue
    }
    // 不要求 owner_step_uuid：CLI 直接执行模式下 step_dir 为空，
    // 若以此为条件会把整份产出列表过滤成空，导致引用菜单永远打不开
    if (!node.absolute_path) continue
    files.push({ ...node, absolute_path: node.absolute_path })
  }
  return files
}
