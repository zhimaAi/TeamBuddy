import { ref, watch } from 'vue'
import { defineStore } from 'pinia'
import apiClient from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import type { MyWorkItem } from '@/types/workitem'

export const useWorkItemStore = defineStore('work-item', () => {
  const auth = useAuthStore()
  const items = ref<MyWorkItem[]>([])
  let loadedAt = 0
  let revision = 0
  let pending: Promise<void> | undefined
  watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => {
    revision += 1
    items.value = []
    loadedAt = 0
    pending = undefined
  }, { flush: 'sync' })
  function load(force = false): Promise<void> {
    if (pending) return pending
    if (!force && Date.now() - loadedAt < 30_000) return Promise.resolve()
    const requestRevision = revision
    const request = apiClient.get<{ items: MyWorkItem[] }>('/team/my-work').then(result => {
      if (requestRevision !== revision) return
      items.value = result.items || []
      loadedAt = Date.now()
    }).finally(() => { if (pending === request) pending = undefined })
    pending = request
    return request
  }
  return { items, load }
})
