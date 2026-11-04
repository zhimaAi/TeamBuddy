export interface ChatRuntimeSelection {
  cli_type: string
  model_name: string
  thinking_level?: string
}

const STORAGE_KEY = 'goteams.chatComposer.lastRuntime'

function readStore(): Record<string, ChatRuntimeSelection> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as Record<string, Partial<ChatRuntimeSelection>>
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const store: Record<string, ChatRuntimeSelection> = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (!key || !value || typeof value !== 'object') continue
      store[key] = {
        cli_type: typeof value.cli_type === 'string' ? value.cli_type : '',
        model_name: typeof value.model_name === 'string' ? value.model_name : '',
        thinking_level: typeof value.thinking_level === 'string' ? value.thinking_level : undefined,
      }
    }
    return store
  } catch {
    return {}
  }
}

function selectionKey(taskUuid: string, stepUuid: string) {
  return `${taskUuid}:${stepUuid}`
}

export function useChatRuntimeDefaults() {
  function getChatRuntimeSelection(taskUuid: string, stepUuid: string): ChatRuntimeSelection | null {
    if (!taskUuid || !stepUuid) return null
    const stored = readStore()[selectionKey(taskUuid, stepUuid)]
    if (!stored?.cli_type) return null
    return stored
  }

  function saveChatRuntimeSelection(
    taskUuid: string,
    stepUuid: string,
    selection: ChatRuntimeSelection,
  ): void {
    if (!taskUuid || !stepUuid || !selection.cli_type) return
    try {
      const store = readStore()
      store[selectionKey(taskUuid, stepUuid)] = {
        cli_type: selection.cli_type,
        model_name: selection.model_name || '',
        thinking_level: selection.thinking_level,
      }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(store))
    } catch {
      /* Ignore persistence failures in scenarios such as privacy mode */
    }
  }

  return { getChatRuntimeSelection, saveChatRuntimeSelection }
}
