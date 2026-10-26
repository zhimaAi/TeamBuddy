import { ref } from 'vue'
import { useCliModelOptions } from '@/composables/useCliModelOptions'
import { supportsVibeModel, vibeToolDefinition, VIBE_CODING_TOOLS } from '@/composables/useVibeCoding'

const STORAGE_KEY = 'goteams.vibe-coding.recent-models'

// 模型是按 CLI 保存的用户偏好；每次使用前校验目录，失效时回退默认配置。
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

export function availableVibeModel(tool: string, model: string, models: string[]) {
  return model && !(tool === 'pi' && model === 'auto') && models.includes(model) ? model : ''
}

export function useVibeModelPreferences() {
  const vibeModels = ref<Record<string, string>>(readRecentVibeModels())
  const resolving = ref<Record<string, boolean>>({})
  const { loadModelOptions } = useCliModelOptions(false)
  let generation = 0

  function resetVibeModels(tool = '', model = '') {
    generation++
    resolving.value = {}
    vibeModels.value = readRecentVibeModels()
    if (supportsVibeModel(tool)) vibeModels.value[tool] = model
  }

  function setVibeModel(tool: string, model: string) {
    vibeModels.value = { ...vibeModels.value, [tool]: model }
    rememberVibeModel(tool, model)
  }

  async function resolveVibeModel(tool: string): Promise<string> {
    const candidate = vibeModels.value[tool] || ''
    const cliType = vibeToolDefinition(tool)?.cliType
    if (!supportsVibeModel(tool) || !cliType) return ''
    if (!candidate) {
      rememberVibeModel(tool, '')
      return ''
    }
    const currentGeneration = generation
    const remembered = readRecentVibeModels()[tool]
    resolving.value = { ...resolving.value, [tool]: true }
    try {
      const models = await loadModelOptions(cliType)
      const resolved = availableVibeModel(tool, candidate, models)
      if (currentGeneration === generation && vibeModels.value[tool] === candidate) {
        vibeModels.value = { ...vibeModels.value, [tool]: resolved }
        if (readRecentVibeModels()[tool] === remembered) rememberVibeModel(tool, resolved)
      }
      return resolved
    } finally {
      if (currentGeneration === generation) resolving.value = { ...resolving.value, [tool]: false }
    }
  }

  return { vibeModels, resolving, resetVibeModels, setVibeModel, resolveVibeModel }
}
