import { readonly, ref } from 'vue'

type DisplayMode = 'hidden' | 'all' | 'partial'
const STORAGE_KEY = 'goteams.task.execution-display.v2'

function isDisplayMode(value: string | null): value is DisplayMode {
  return value === 'hidden' || value === 'all' || value === 'partial'
}

function readDisplayMode(): DisplayMode {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY)
    return isDisplayMode(saved) ? saved : 'all'
  } catch {
    return 'all'
  }
}

// 所有任务和详情入口共用；长期保留，v2 不读取旧的任务级偏好。
const displayMode = ref<DisplayMode>(readDisplayMode())

export function useTaskExecutionDisplayPreference() {
  function setDisplayMode(value: string) {
    if (!isDisplayMode(value)) return
    displayMode.value = value
    try {
      window.localStorage.setItem(STORAGE_KEY, value)
    } catch {
      // 存储不可用时，仍在当前客户端会话内共享展示状态。
    }
  }

  return { displayMode: readonly(displayMode), setDisplayMode }
}
