import { computed, ref, toValue, type MaybeRefOrGetter } from 'vue'
import apiClient from '@/api/client'
import { useAppI18n } from '@/i18n'
import { useCLIModelCatalogStore, type CLIModelContext } from '@/stores/cli-models'
import type { DiscoveredCLI } from '@/views/agents/components/agentPipeline'
import { cliModelChoices, cliModelFamily } from '@/utils/cliModelOptions'

export function useCliModelOptions(activeCli: MaybeRefOrGetter<string>, scope?: MaybeRefOrGetter<CLIModelContext>) {
  const { t } = useAppI18n()
  const directories = useCLIModelCatalogStore()
  const cliOptions = ref<DiscoveredCLI[]>([])
  const cliLoading = ref(false)
  const context = () => scope ? toValue(scope) : {}
  const modelOptions = computed(() => directories.catalog(toValue(activeCli), context())?.models || [])
  const defaultLabel = () => t('components.cliSelect.modelDefault')
  const modelChoices = (selected = '') => cliModelChoices(toValue(activeCli), modelOptions.value, selected, defaultLabel())
  const modelLabel = (model: string) => cliModelFamily(toValue(activeCli), model, defaultLabel())
  const modelLoading = computed(() => directories.isLoading(toValue(activeCli), context()))

  async function loadCliOptions(refresh = false) {
    cliLoading.value = true
    try {
      const result = await apiClient.get<{ items: DiscoveredCLI[] }>('/tasks/cli-discovery', { refresh })
      directories.reconcileInstallations(result.items || [])
      cliOptions.value = [...(result.items || [])].sort((left, right) => Number(right.installed) - Number(left.installed))
      return cliOptions.value
    } finally {
      cliLoading.value = false
    }
  }

  return { cliLoading, cliOptions, loadCliOptions, modelLoading, modelOptions, modelChoices, modelLabel }
}
