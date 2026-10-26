import { computed, onBeforeUnmount, ref, watch } from 'vue'

export type KnowledgeDraft = {
  uuid: string
  title: string
  ext: 'md' | 'txt'
  folderID: number
  tags: string[]
  content: string
  filePath: string
  contentHash: string
  wordCount: number
  createdAt: number
  updatedAt: number
}

export type KnowledgeTab = {
  draft: KnowledgeDraft
  savedSnapshot: string
  state: 'idle' | 'pending' | 'saving' | 'saved' | 'error'
  savedAt: number
  error: string
  saving: boolean
}

export function draftSnapshot(draft: KnowledgeDraft) {
  return JSON.stringify({
    title: draft.title,
    folderID: draft.folderID,
    tags: [...draft.tags].sort(),
    content: draft.content,
  })
}

export function tabDirty(tab: KnowledgeTab) {
  return draftSnapshot(tab.draft) !== tab.savedSnapshot
}

export function useKnowledgeTabs(save: (tab: KnowledgeTab) => Promise<void>) {
  const tabs = ref<KnowledgeTab[]>([])
  const activeUUID = ref('')
  const emptyDraft: KnowledgeDraft = {
    uuid: '', title: '', ext: 'md', folderID: 0, tags: [], content: '',
    filePath: '', contentHash: '', wordCount: 0, createdAt: 0, updatedAt: 0,
  }
  const activeTab = computed(() => tabs.value.find((tab) => tab.draft.uuid === activeUUID.value))
  const draft = computed(() => activeTab.value?.draft ?? emptyDraft)
  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  const stops = new Map<string, () => void>()

  function cancelTimer(uuid: string) {
    const timer = timers.get(uuid)
    if (timer) clearTimeout(timer)
    timers.delete(uuid)
  }

  function scheduleSave(tab: KnowledgeTab) {
    cancelTimer(tab.draft.uuid)
    if (!tabDirty(tab)) {
      tab.state = tab.savedAt ? 'saved' : 'idle'
      tab.error = ''
      return
    }
    tab.state = 'pending'
    timers.set(tab.draft.uuid, setTimeout(() => {
      timers.delete(tab.draft.uuid)
      void save(tab)
    }, 1500))
  }

  function addTab(value: KnowledgeDraft) {
    tabs.value.push({
      draft: value, savedSnapshot: draftSnapshot(value), state: 'idle',
      savedAt: 0, error: '', saving: false,
    })
    const tab = tabs.value[tabs.value.length - 1]
    stops.set(value.uuid, watch(() => draftSnapshot(tab.draft), () => {
      if (!tab.saving) scheduleSave(tab)
    }, { flush: 'sync' }))
    return tab
  }

  function removeTab(uuid: string) {
    cancelTimer(uuid)
    stops.get(uuid)?.()
    stops.delete(uuid)
    const index = tabs.value.findIndex((tab) => tab.draft.uuid === uuid)
    if (index < 0) return
    tabs.value.splice(index, 1)
    if (activeUUID.value === uuid) {
      activeUUID.value = tabs.value[Math.min(index, tabs.value.length - 1)]?.draft.uuid ?? ''
    }
  }

  onBeforeUnmount(() => {
    timers.forEach(clearTimeout)
    timers.clear()
    stops.forEach((stop) => stop())
    stops.clear()
  })

  return { tabs, activeUUID, activeTab, draft, addTab, removeTab, cancelTimer, scheduleSave }
}
