import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { skillApi, type Skill, type CreateSkillInput } from '@/api/skills'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

export const useSkillStore = defineStore('skill', () => {
  const items = ref<Skill[]>([])
  const total = ref(0)
  const page = ref(1)
  const size = ref(10)
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref('')
  const hasMore = ref(false)
  let currentName = ''
  let revision = 0
  let active: { key: string; promise: Promise<void> } | undefined
  const auth = useAuthStore()

  watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => {
    revision += 1
    active = undefined
    items.value = []
    total.value = 0
    page.value = 1
    loaded.value = false
    loading.value = false
    error.value = ''
    hasMore.value = false
    currentName = ''
  }, { flush: 'sync' })

  function load(nextPage = 1, name = '', nextSize = 10, append = false): Promise<void> {
    const key = JSON.stringify([nextPage, name.trim(), nextSize])
    if (active?.key === key) return active.promise
    const requestRevision = ++revision
    if (!append) {
      hasMore.value = false
      loaded.value = false
    }
    loading.value = true
    error.value = ''
    const promise = skillApi.list(nextPage, nextSize, name.trim())
      .then(result => {
        if (requestRevision !== revision) return
        const approved = result.list.filter(item => item.review_status === 'approved')
        if (append) {
          const ids = new Set(items.value.map(item => item.id))
          items.value = [...items.value, ...approved.filter(item => !ids.has(item.id))]
        } else {
          items.value = approved
        }
        total.value = result.total
        page.value = result.page
        size.value = result.size
        loaded.value = true
        currentName = name.trim()
        hasMore.value = result.page === nextPage && result.list.length > 0 && result.size > 0 && result.page * result.size < result.total
      })
      .catch(reason => {
        if (requestRevision === revision) error.value = reason instanceof Error ? reason.message : t('skills.loadFailed')
      })
      .finally(() => {
        if (requestRevision !== revision) return
        loading.value = false
        active = undefined
      })
    active = { key, promise }
    return promise
  }

  function loadMore(): Promise<void> {
    if (active || !loaded.value || !hasMore.value) return Promise.resolve()
    return load(page.value + 1, currentName, size.value, true)
  }

  async function create(input: CreateSkillInput) {
    const result = await skillApi.create(input)
    loaded.value = false
    return result
  }

  return { items, total, page, size, loading, loaded, error, hasMore, load, loadMore, create }
})
