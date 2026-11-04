import apiClient from '@/api/client'

export interface ModelThinking {
  status: 'supported' | 'unsupported' | 'unknown'
  kind?: 'effort' | 'variant' | 'cli' | 'toggle'
  parameter?: string
  levels?: { value: string; description?: string }[]
  default_level?: string
  source: string
  state: 'fresh' | 'stale' | 'unknown'
  updated_at?: string
}

export interface CLIModelsResponse {
  models: string[]
  model_options?: { id: string; aliases?: string[]; thinking: ModelThinking }[]
  catalog_meta?: { state: 'fresh' | 'stale' | 'unknown'; cli_version?: string; updated_at?: string }
}

export function getCLIModelCatalog(cliType: string, options: { modelName?: string; taskUuid?: string; channelType?: string; refresh?: boolean; signal?: AbortSignal } = {}) {
  return apiClient.get<CLIModelsResponse>('/tasks/cli-models', {
    cli_type: cliType,
    model_name: options.modelName,
    task_uuid: options.taskUuid,
    channel_type: options.channelType,
    refresh: options.refresh ?? false,
  }, options.signal)
}
