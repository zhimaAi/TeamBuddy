import { onScopeDispose, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { getCLIModelCatalog, type CLIModelsResponse } from '@/api/cli-models'
import { useAuthStore } from '@/stores/auth'

export interface CLIModelContext { taskUuid?: string; channelType?: string }
interface CatalogEntry {
  cliType: string
  catalog?: CLIModelsResponse
  fetchedAt: number
  error?: string
}

const freshnessWindow = 3 * 60 * 1000
const emptyCatalog = (): CLIModelsResponse => ({ models: [] })

// One owner for complete directories, shared by model pickers and thinking controls.
// Task/channel contexts are never borrowed from the user-level reference catalog.
export const useCLIModelCatalogStore = defineStore('cli-model-catalog', () => {
  const entries = ref<Record<string, CatalogEntry>>({})
  const loading = ref<Record<string, boolean>>({})
  const flights = new Map<string, { cliType: string; refresh: boolean; controller: AbortController; promise: Promise<CLIModelsResponse> }>()
  const generations = new Map<string, number>()
  let epoch = 0

  const keyFor = (cliType: string, context: CLIModelContext = {}) => JSON.stringify([cliType, context.taskUuid || '', context.channelType || ''])
  const catalog = (cliType: string, context: CLIModelContext = {}) => entries.value[keyFor(cliType, context)]?.catalog
  const error = (cliType: string, context: CLIModelContext = {}) => entries.value[keyFor(cliType, context)]?.error || ''
  const isLoading = (cliType: string, context: CLIModelContext = {}) => !!loading.value[keyFor(cliType, context)]

  function invalidate(cliType?: string) {
    if (cliType) generations.set(cliType, (generations.get(cliType) || 0) + 1)
    else epoch++
    for (const [key, flight] of flights) {
      if (!cliType || flight.cliType === cliType) {
        flight.controller.abort()
        flights.delete(key)
        delete loading.value[key]
      }
    }
    for (const [key, entry] of Object.entries(entries.value)) {
      if (!cliType || entry.cliType === cliType) delete entries.value[key]
    }
  }

  async function load(cliType: string, context: CLIModelContext = {}, refresh = false): Promise<CLIModelsResponse> {
    if (!cliType) return emptyCatalog()
    const key = keyFor(cliType, context)
    const existing = flights.get(key)
    if (existing) {
      if (!refresh || existing.refresh) return existing.promise
      const queuedEpoch = epoch
      const queuedGeneration = generations.get(cliType) || 0
      await existing.promise.catch(() => emptyCatalog())
      if (queuedEpoch !== epoch || queuedGeneration !== (generations.get(cliType) || 0)) return emptyCatalog()
      return load(cliType, context, true)
    }
    const cached = entries.value[key]
    if (!refresh && cached && Date.now() - cached.fetchedAt < freshnessWindow) return cached.catalog || emptyCatalog()

    const revision = epoch
    const generation = generations.get(cliType) || 0
    const current = () => revision === epoch && generation === (generations.get(cliType) || 0)
    const controller = new AbortController()
    loading.value[key] = true
    const promise = (async () => {
      try {
        // No model_name: each request reads one full CLI/context directory.
        const result = await getCLIModelCatalog(cliType, { ...context, refresh, signal: controller.signal })
        if (!current()) return emptyCatalog()
        const previous = entries.value[key]?.catalog
        const next = result.models?.length || !previous?.models.length ? result : staleCatalog(previous)
        entries.value[key] = { cliType, catalog: next, fetchedAt: Date.now() }
        // Publish the stale snapshot immediately, then wait for one shared actual
        // refresh in the background. A failed refresh never creates a retry loop.
        if (!refresh && next.catalog_meta?.state === 'stale') {
          void Promise.resolve().then(() => load(cliType, context, true)).catch(() => undefined)
        }
        return next
      } catch (cause) {
        if (!current()) return emptyCatalog()
        const previous = entries.value[key]?.catalog
        entries.value[key] = {
          cliType, fetchedAt: Date.now(),
          catalog: previous ? staleCatalog(previous) : undefined,
          error: cause instanceof Error ? cause.message : '',
        }
        throw cause
      } finally {
        if (flights.get(key)?.controller === controller) {
          flights.delete(key)
          delete loading.value[key]
        }
      }
    })()
    flights.set(key, { cliType, refresh, controller, promise })
    return promise
  }

  function staleCatalog(value: CLIModelsResponse): CLIModelsResponse {
    return {
      ...value,
      catalog_meta: { ...value.catalog_meta, state: 'stale' },
      model_options: value.model_options?.map(option => ({ ...option, thinking: { ...option.thinking, state: 'stale' } })),
    }
  }

  const auth = useAuthStore()
  watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => invalidate(), { flush: 'sync' })
  onScopeDispose(() => invalidate())

  return { entries, catalog, error, isLoading, load, invalidate }
})
