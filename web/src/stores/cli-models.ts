import { onScopeDispose, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { getCLIModelCatalog, type CLIModelsResponse } from '@/api/cli-models'
import { useAuthStore } from '@/stores/auth'

export interface CLIModelContext { taskUuid?: string; channelType?: string; modelName?: string }
interface CatalogEntry {
  cliType: string
  channelType?: string
  catalog?: CLIModelsResponse
  error?: string
}

const emptyCatalog = (): CLIModelsResponse => ({ models: [] })

// 只保存用户本次主动读取的结果；任务、通道与选中模型的能力互不借用。
export const useCLIModelCatalogStore = defineStore('cli-model-catalog', () => {
  const entries = ref<Record<string, CatalogEntry>>({})
  const loading = ref<Record<string, boolean>>({})
  const flights = new Map<string, { cliType: string; channelType?: string; controller: AbortController; promise: Promise<CLIModelsResponse> }>()
  const keyFor = (cliType: string, context: CLIModelContext = {}) => JSON.stringify([
    cliType, context.taskUuid || '', context.channelType || '', context.modelName || '',
  ])
  const catalog = (cliType: string, context: CLIModelContext = {}) => entries.value[keyFor(cliType, context)]?.catalog
  const error = (cliType: string, context: CLIModelContext = {}) => entries.value[keyFor(cliType, context)]?.error || ''
  const isLoading = (cliType: string, context: CLIModelContext = {}) => !!loading.value[keyFor(cliType, context)]

  function invalidate(cliType?: string, channelType?: string, authFailureKey?: string) {
    const matches = (entry: { cliType: string; channelType?: string }) => (!cliType || entry.cliType === cliType)
      && (!channelType || entry.channelType === channelType)
    for (const [key, flight] of flights) {
      if (key !== authFailureKey && matches(flight)) {
        flight.controller.abort()
        flights.delete(key)
        delete loading.value[key]
      }
    }
    for (const [key, entry] of Object.entries(entries.value)) {
      if (matches(entry) && (!authFailureKey || entry.catalog?.catalog_meta?.reason !== 'login_required')) {
        delete entries.value[key]
      }
    }
  }

  async function read(cliType: string, context: CLIModelContext = {}): Promise<CLIModelsResponse> {
    if (!cliType) return emptyCatalog()
    const key = keyFor(cliType, context)
    const existing = flights.get(key)
    if (existing) return existing.promise
    const controller = new AbortController()
    const channelType = context.channelType
    delete entries.value[key]
    loading.value[key] = true
    const promise = (async () => {
      try {
        const result = await getCLIModelCatalog(cliType, { ...context, signal: controller.signal })
        if (controller.signal.aborted) return emptyCatalog()
        // 本机登录状态由 CLI 共享；退出后清除同工具其他入口的旧能力，不改已保存的选择。
        if (result.catalog_meta?.reason === 'login_required') {
          invalidate(cliType, undefined, key)
        } else if (cliType === 'antigravity' && result.catalog_meta?.state === 'fresh') {
          // 成功重读后撤掉其他入口的旧登录提示，仍按入口单独读取能力。
          for (const [otherKey, entry] of Object.entries(entries.value)) {
            if (entry.cliType === cliType && entry.catalog?.catalog_meta?.reason === 'login_required') {
              delete entries.value[otherKey]
            }
          }
        }
        entries.value[key] = { cliType, channelType, catalog: result }
        return result
      } catch (cause) {
        if (controller.signal.aborted) return emptyCatalog()
        entries.value[key] = { cliType, channelType, error: cause instanceof Error ? cause.message : '' }
        throw cause
      } finally {
        if (flights.get(key)?.controller === controller) {
          flights.delete(key)
          delete loading.value[key]
        }
      }
    })()
    flights.set(key, { cliType, channelType, controller, promise })
    return promise
  }

  // 所有安装列表入口共用上一轮的安装身份，页面重新挂载也能识别变化。
  const installations = new Map<string, { installed: boolean; exec_path?: string; version?: string }>()
  function reconcileInstallations(items: { type: string; installed: boolean; exec_path?: string; version?: string }[]) {
    const remaining = new Set(installations.keys())
    for (const item of items) {
      const previous = installations.get(item.type)
      if (!item.installed || (previous && (
        previous.installed !== item.installed || previous.exec_path !== item.exec_path ||
        (item.version && previous.version !== item.version)
      ))) invalidate(item.type)
      installations.set(item.type, item)
      remaining.delete(item.type)
    }
    for (const type of remaining) {
      installations.delete(type)
      invalidate(type)
    }
  }

  const auth = useAuthStore()
  watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => invalidate(), { flush: 'sync' })
  onScopeDispose(() => invalidate())

  return { catalog, error, isLoading, read, invalidate, reconcileInstallations }
})
