import { ref } from 'vue'
import { supportsVibeModel, VIBE_CODING_TOOLS } from '@/composables/useVibeCoding'

const STORAGE_KEY = 'goteams.vibe-coding.recent-models'

// 只保存用户的选择，不保存或用探测结果改写模型偏好。
export function readRecentVibeModels(): Record<string, string> {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}')
    if (!stored || typeof stored !== 'object' || Array.isArray(stored)) return {}
    return Object.fromEntries(VIBE_CODING_TOOLS.filter(tool => supportsVibeModel(tool.id)).flatMap(tool => {
      const value = Object.getOwnPropertyDescriptor(stored, tool.id)?.value
      return typeof value === 'string' && value.length <= 256 ? [[tool.id, value]] : []
    }))
  } catch {
    return {}
  }
}

export function rememberVibeModel(tool: string, model: string) {
  if (!supportsVibeModel(tool)) return
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...readRecentVibeModels(), [tool]: model }))
  } catch {
    // 偏好保存失败不影响本次选择。
  }
}

export function useVibeModelPreferences() {
  const vibeModels = ref<Record<string, string>>(readRecentVibeModels())

  function resetVibeModels(tool = '', model = '') {
    vibeModels.value = readRecentVibeModels()
    if (supportsVibeModel(tool)) vibeModels.value[tool] = model
  }

  function setVibeModel(tool: string, model: string) {
    vibeModels.value = { ...vibeModels.value, [tool]: model }
    rememberVibeModel(tool, model)
  }

  function selectedVibeModel(tool: string): string {
    return supportsVibeModel(tool) ? vibeModels.value[tool] || '' : ''
  }

  return { vibeModels, resetVibeModels, setVibeModel, selectedVibeModel }
}
