import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import apiClient from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import type { Pipeline } from '@/types/pipeline'
import { filterPipelinesByCloudLogin } from '@/utils/pipeline'
import { t } from '@/i18n'

export const usePipelineStore = defineStore('pipeline', () => {
  const authStore = useAuthStore()
  const allPipelines = ref<Pipeline[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')
  let loadPromise: Promise<Pipeline[]> | undefined
  let revision = 0
  watch(() => authStore.cloudUser?.sessionGeneration || authStore.cloudUser?.accountScope || '', () => {
    revision += 1
    allPipelines.value = allPipelines.value.filter(item => item.source_type !== 'cloud')
    loaded.value = false
    loading.value = false
    error.value = ''
    loadPromise = undefined
  }, { flush: 'sync' })


  const pipelines = computed(() =>
    filterPipelinesByCloudLogin(allPipelines.value, authStore.cloudLoggedIn),
  )

  function normalisePipeline(pipeline: Pipeline): Pipeline {
    return { ...pipeline, steps: pipeline.steps || [] }
  }

  async function loadPipelines(force = false): Promise<Pipeline[]> {
    if (loadPromise) return loadPromise
    if (loaded.value && !force) return pipelines.value

    const requestRevision = revision
    loading.value = true
    error.value = ''
    loadPromise = apiClient
      .get<{ items: Pipeline[] }>('/pipelines')
      .then((result) => {
        if (requestRevision !== revision) return []
        allPipelines.value = (result.items || []).map(normalisePipeline)
        loaded.value = true
        return pipelines.value
      })
      .catch((loadError) => {
        if (requestRevision !== revision) return []
        error.value = loadError instanceof Error ? loadError.message : t('agents.loadFailed')
        throw loadError
      })
      .finally(() => {
        if (requestRevision !== revision) return
        loading.value = false
        loadPromise = undefined
      })

    return loadPromise
  }

  async function loadPipeline(uuid: string): Promise<Pipeline | undefined> {
    const requestRevision = revision
    try {
      const detail = await apiClient.get<Pipeline>(`/pipelines/${encodeURIComponent(uuid)}`)
      if (requestRevision !== revision) return undefined
      upsertPipeline(detail)
      return detail
    } catch (reason) {
      if (requestRevision !== revision) return undefined
      throw reason
    }
  }

  function upsertPipeline(pipeline: Pipeline) {
    const normalised = normalisePipeline(pipeline)
    const index = allPipelines.value.findIndex((item) => item.uuid === normalised.uuid)
    if (index >= 0) {
      allPipelines.value[index] = normalised
      return
    }
    allPipelines.value.push(normalised)
  }

  return {
    pipelines,
    loading,
    loaded,
    error,
    loadPipelines,
    loadPipeline,
    upsertPipeline,
  }
})
