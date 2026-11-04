import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import apiClient from '@/api/client'
import { useCLIModelCatalogStore, type CLIModelContext } from '@/stores/cli-models'
import type { DiscoveredCLI } from '@/views/agents/components/agentPipeline'

export function useCliModelOptions(retainModelsOnEmpty = true, scope?: MaybeRefOrGetter<CLIModelContext>) {
  const directories = useCLIModelCatalogStore()
  const cliOptions = ref<DiscoveredCLI[]>([])
  const activeCli = ref('')
  const cliLoading = ref(false)
  const context = () => scope ? toValue(scope) : {}
  const modelCache = computed(() => {
    const cache: Record<string, string[]> = {}
    for (const cliType of new Set([...cliOptions.value.map(item => item.type), activeCli.value].filter(Boolean))) {
      const models = directories.catalog(cliType, context())?.models
      cache[cliType] = models?.length || !retainModelsOnEmpty ? models || []
        : cliOptions.value.find(item => item.type === cliType)?.models || []
    }
    return cache
  })
  const modelCounts = computed(() => Object.fromEntries(Object.entries(modelCache.value).map(([cli, models]) => [cli, models.length])))
  const modelOptions = computed(() => modelCache.value[activeCli.value] || [])
  const modelLoading = computed(() => !!activeCli.value && !modelOptions.value.length && directories.isLoading(activeCli.value, context()))

  async function loadCliOptions(refresh = false) {
    cliLoading.value = true
    try {
      const result = await apiClient.get<{ items: DiscoveredCLI[] }>('/tasks/cli-discovery', { refresh })
      for (const item of result.items || []) {
        const previous = cliOptions.value.find(cli => cli.type === item.type)
        if (!item.installed || (previous && (
          previous.installed !== item.installed || previous.exec_path !== item.exec_path ||
          (item.version && previous.version !== item.version)
        ))) directories.invalidate(item.type)
      }
      cliOptions.value = [...(result.items || [])].sort((left, right) => Number(right.installed) - Number(left.installed))
      return cliOptions.value
    } finally {
      cliLoading.value = false
    }
  }

  async function fetchModels(cliType: string, refresh = false): Promise<string[]> {
    try {
      return (await directories.load(cliType, context(), refresh)).models || []
    } catch (error) {
      const fallback = modelCache.value[cliType] || []
      if (!fallback.length) throw error
      return fallback
    }
  }

  function loadModelOptions(cliType: string, refresh = false) {
    activeCli.value = cliType
    return cliType ? fetchModels(cliType, refresh) : Promise.resolve([])
  }

  async function prefetchModelCounts(cliTypes: string[]) {
    await Promise.allSettled([...new Set(cliTypes.filter(Boolean))].map(cliType => fetchModels(cliType)))
  }

  watch(() => JSON.stringify(context()), () => {
    if (activeCli.value) void fetchModels(activeCli.value).catch(() => undefined)
  })

  return {
    cliLoading, cliOptions, loadCliOptions, loadModelOptions,
    modelCounts, modelLoading, modelOptions, prefetchModelCounts,
    resetModelOptions: () => { activeCli.value = '' },
  }
}
