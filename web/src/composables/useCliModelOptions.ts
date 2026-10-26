import { computed, ref } from 'vue'
import apiClient from '@/api/client'
import type { DiscoveredCLI } from '@/views/agents/components/agentPipeline'

export function useCliModelOptions(retainModelsOnEmpty = true) {
  const cliOptions = ref<DiscoveredCLI[]>([])
  const modelOptions = ref<string[]>([])
  const modelCache = ref<Record<string, string[]>>({})
  const cliLoading = ref(false)
  const modelLoading = ref(false)
  let modelRequestId = 0
  const inflightModels = new Map<string, { refresh: boolean; promise: Promise<string[]> }>()
  const modelGenerations = new Map<string, number>()

  const modelCounts = computed(() => {
    const counts: Record<string, number> = {}
    for (const [cliType, models] of Object.entries(modelCache.value)) {
      counts[cliType] = models.length
    }
    return counts
  })

  async function loadCliOptions(refresh = false) {
    cliLoading.value = true
    try {
      const result = await apiClient.get<{ items: DiscoveredCLI[] }>('/tasks/cli-discovery', { refresh })
      const nextCache = { ...modelCache.value }
      for (const item of result.items || []) {
        const previous = cliOptions.value.find(cli => cli.type === item.type)
        if (!item.installed || (previous && (
          previous.installed !== item.installed || previous.exec_path !== item.exec_path ||
          (item.version && previous.version !== item.version)
        ))) {
          delete nextCache[item.type]
          modelGenerations.set(item.type, (modelGenerations.get(item.type) || 0) + 1)
          inflightModels.delete(item.type)
        }
      }
      modelCache.value = nextCache
      cliOptions.value = [...(result.items || [])].sort(
        (left, right) => Number(right.installed) - Number(left.installed),
      )
      return cliOptions.value
    } finally {
      cliLoading.value = false
    }
  }

  async function fetchModels(cliType: string, refresh = false): Promise<string[]> {
    const existing = inflightModels.get(cliType)
    if (existing) {
      if (!refresh || existing.refresh) return existing.promise
      // 强制刷新不能直接复用可能只读取缓存的普通请求。
      await existing.promise.catch(() => [])
      return fetchModels(cliType, true)
    }

    const generation = modelGenerations.get(cliType) || 0
    const request = (async () => {
      try {
        const result = await apiClient.get<{ models: string[] }>('/tasks/cli-models', {
          cli_type: cliType,
          refresh,
        })
        if (generation !== (modelGenerations.get(cliType) || 0)) return modelCache.value[cliType] || []
        const nextModels = result.models?.length ? result.models
          : retainModelsOnEmpty ? modelCache.value[cliType] || [] : []
        modelCache.value = { ...modelCache.value, [cliType]: nextModels }
        return nextModels
      } catch (error) {
        if (generation !== (modelGenerations.get(cliType) || 0)) return modelCache.value[cliType] || []
        const cached = modelCache.value[cliType]
        const fallback = cached?.length ? cached : cliOptions.value.find(item => item.type === cliType)?.models || []
        if (!fallback.length) throw error
        modelCache.value = { ...modelCache.value, [cliType]: fallback }
        return fallback
      }
    })()

    inflightModels.set(cliType, { refresh, promise: request })
    try {
      return await request
    } finally {
      if (inflightModels.get(cliType)?.promise === request) inflightModels.delete(cliType)
    }
  }

  async function loadModelOptions(cliType: string, refresh = false) {
    const requestId = ++modelRequestId
    if (!cliType) {
      modelOptions.value = []
      modelLoading.value = false
      return []
    }

    const cached = modelCache.value[cliType]
    modelOptions.value = cached ? [...cached] : []
    modelLoading.value = !modelOptions.value.length

    try {
      const nextModels = await fetchModels(cliType, refresh)
      if (requestId === modelRequestId) modelOptions.value = nextModels
      return nextModels
    } finally {
      if (requestId === modelRequestId) modelLoading.value = false
    }
  }

  async function prefetchModelCounts(cliTypes: string[]) {
    const unique = [...new Set(cliTypes.filter(Boolean))]
    if (!unique.length) return
    await Promise.allSettled(unique.map(cliType => fetchModels(cliType)))
  }

  function resetModelOptions() {
    modelRequestId++
    modelOptions.value = []
    modelLoading.value = false
  }

  return {
    cliLoading,
    cliOptions,
    loadCliOptions,
    loadModelOptions,
    modelCounts,
    modelLoading,
    modelOptions,
    prefetchModelCounts,
    resetModelOptions,
  }
}
