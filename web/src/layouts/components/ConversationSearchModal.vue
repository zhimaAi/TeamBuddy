<template>
  <a-modal class="conversation-search-modal" :open="open" :footer="null" :title="null" :closable="false" centered width="584px" @cancel="emit('update:open', false)" @after-open-change="focusInput">
    <div class="conversation-search">
      <div class="search-header">
        <a-input ref="inputRef" v-model:value="keyword" :maxlength="200" allow-clear :placeholder="t('layout.sidebar.searchConversations')" :aria-label="t('layout.sidebar.searchConversations')">
          <template #prefix><img :src="searchIcon" alt="" aria-hidden="true" /></template>
        </a-input>
        <a-button type="text" class="search-close" :aria-label="t('common.actions.close')" @click="emit('update:open', false)">
          <img :src="closeIcon" alt="" aria-hidden="true" />
        </a-button>
      </div>
      <div class="search-results" :aria-busy="displayLoading">
        <a-spin v-if="displayLoading && !displayItems.length" class="search-state" />
        <div v-else-if="displayError" class="search-state" role="alert"><p>{{ displayError }}</p><a-button @click="retry()">{{ t('common.actions.retry') }}</a-button></div>
        <a-empty v-else-if="!displayItems.length" class="search-state" :description="t(isSearching ? 'layout.sidebar.searchEmpty' : 'layout.sidebar.searchPrompt')" />
        <button v-for="item in displayItems" :key="item.task_uuid" type="button" class="search-result" @click="select(item.task_uuid)">
          <span class="result-title" :title="item.title"><template v-for="(part, index) in highlight(item.title)" :key="index"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
          <span v-if="item.snippet.trim()" class="result-snippet"><template v-for="(part, index) in highlight(item.snippet)" :key="index"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
        </button>
        <a-button v-if="isSearching && hasMore" class="search-more" :loading="loading" @click="search(true)">{{ t('layout.sidebar.searchMore') }}</a-button>
      </div>
    </div>
  </a-modal>
</template>
<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import searchIcon from '@/assets/icons/conversation-search-input.svg'
import closeIcon from '@/assets/icons/conversation-search-close.svg'
import apiClient from '@/api/client'
import { useAppI18n } from '@/i18n'
import { useConversationStore } from '@/stores/conversation'
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
interface SearchItem { task_uuid: string; title: string; snippet: string }
const { t } = useAppI18n()
const router = useRouter()
const conversationStore = useConversationStore()
const keyword = ref('')
const items = ref<SearchItem[]>([])
const loading = ref(false)
const error = ref('')
const hasMore = ref(false)
const inputRef = ref<{ focus: () => void }>()
const isSearching = computed(() => Boolean(keyword.value.trim()))
const displayItems = computed<SearchItem[]>(() => isSearching.value ? items.value : conversationStore.allConversations.map(conversation => ({
  task_uuid: conversation.task_uuid,
  title: conversation.title,
  snippet: (conversation.latest.summary || '').replace(/\s+/g, ' ').trim(),
})))
const displayLoading = computed(() => isSearching.value ? loading.value : conversationStore.loading)
const displayError = computed(() => isSearching.value ? error.value : (displayItems.value.length ? '' : conversationStore.error))
let version = 0
let timer: ReturnType<typeof setTimeout> | undefined
function focusInput(open: boolean) { if (open) inputRef.value?.focus() }
function retry() {
  if (isSearching.value) void search()
  else void conversationStore.loadConversations(true)
}
async function search(more = false) {
  const query = keyword.value.trim()
  const request = ++version
  if (!query || !props.open) { items.value = []; hasMore.value = false; loading.value = false; return }
  loading.value = true; error.value = ''
  try {
    const result = await apiClient.get<{ items: SearchItem[]; has_more: boolean }>('/tasks/conversations/search', { q: query, offset: more ? items.value.length : 0 })
    if (request !== version) return
    items.value = more ? [...items.value, ...result.items] : result.items
    hasMore.value = result.has_more
  } catch (err) { if (request === version) error.value = err instanceof Error ? err.message : t('layout.sidebar.searchFailed') }
  finally { if (request === version) loading.value = false }
}
watch(keyword, () => {
  version++; items.value = []; hasMore.value = false; error.value = ''; loading.value = Boolean(keyword.value.trim())
  clearTimeout(timer); timer = setTimeout(() => void search(), 250)
})
watch(() => props.open, open => {
  if (!open) { version++; clearTimeout(timer); loading.value = false }
  else {
    if (!conversationStore.loaded && !conversationStore.loading) void conversationStore.loadConversations()
    void search()
  }
})
function highlight(text: string) {
  const query = keyword.value.trim().toLocaleLowerCase()
  if (!query) return [{ text, match: false }]
  const parts: { text: string; match: boolean }[] = []
  let offset = 0
  let found: number
  while ((found = text.toLocaleLowerCase().indexOf(query, offset)) >= 0) {
    if (found > offset) parts.push({ text: text.slice(offset, found), match: false })
    parts.push({ text: text.slice(found, found + query.length), match: true })
    offset = found + query.length
  }
  if (offset < text.length) parts.push({ text: text.slice(offset), match: false })
  return parts
}
function select(taskUuid: string) { emit('update:open', false); void router.push({ name: 'tasks', query: { taskUuid } }) }
onBeforeUnmount(() => { version++; clearTimeout(timer) })
</script>
<style scoped>
:global(.ant-modal.conversation-search-modal .ant-modal-content) { display: flex; height: min(512px, calc(100dvh - 32px)); flex-direction: column; padding: 11.5px 16px 16px; border-radius: 24px; }
:global(.ant-modal.conversation-search-modal .ant-modal-body) { display: flex; flex: 1; min-height: 0; }
.conversation-search { display: flex; width: 100%; min-height: 0; flex-direction: column; gap: 8px; }
.search-header { display: flex; flex-shrink: 0; align-items: center; gap: 9px; }
.search-header :deep(.ant-input-affix-wrapper) { flex: 1; min-width: 0; height: 32px; padding: 0 10px; border: 0; border-radius: 10px; background: #f5f5f5; }
.search-header :deep(.ant-input) { min-width: 0; height: 100%; padding: 0; background: transparent; color: #262626; font-size: 12px; line-height: 20px; }
.search-header :deep(.ant-input-prefix) { margin-inline-end: 7px; }
.search-header :deep(.ant-input-prefix img) { display: block; }
.search-close.ant-btn { display: flex; width: 32px; min-width: 32px; height: 32px; flex-shrink: 0; align-items: center; justify-content: center; padding: 8px; border: 0; border-radius: 6px; }
.search-close img { display: block; }
.search-results { display: flex; flex: 1; min-height: 0; flex-direction: column; gap: 8px; overflow-y: auto; }
.search-state { display: block; margin: 0; padding: 24px; text-align: center; }
.search-result { display: flex; width: 100%; flex-shrink: 0; flex-direction: column; gap: 4px; padding: 8px 12px; border: 0; border-radius: 12px; background: transparent; font-family: inherit; font-weight: 400; text-align: left; cursor: pointer; }
.search-result:hover, .search-result:focus-visible { background: #f5f5f5; }
.search-result:focus-visible { outline: 2px solid #3157e2; }
.result-title { overflow: hidden; color: #262626; font-size: 14px; line-height: 22px; text-overflow: ellipsis; white-space: nowrap; }
.result-snippet { display: -webkit-box; overflow: hidden; -webkit-line-clamp: 2; -webkit-box-orient: vertical; color: #8c8c8c; font-size: 12px; line-height: 20px; overflow-wrap: anywhere; }
.search-more { align-self: center; flex-shrink: 0; }
mark { color: #fb363f; background: transparent; }
</style>
