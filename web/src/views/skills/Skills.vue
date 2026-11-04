<template>
  <section class="skills-page">
    <header class="skills-header">
      <div>
        <h1>{{ t('skills.title') }}</h1>
        <p>{{ t('skills.subtitle') }}</p>
      </div>
      <div class="header-actions">
        <a-input v-model:value="search" allow-clear :maxlength="100" :placeholder="t('skills.search')" :aria-label="t('skills.search')" @press-enter="searchNow">
          <template #suffix><SearchOutlined /></template>
        </a-input>
        <a-button type="primary" @click="createOpen = true"><ShareAltOutlined />{{ t('skills.share') }}</a-button>
      </div>
    </header>
    <div ref="contentRef" class="skills-content" @scroll.passive="loadWhenNearBottom">
      <a-alert v-if="store.error" type="error" show-icon :message="store.error" class="load-error">
        <template #action><a-button size="small" :loading="store.loading" @click="reload">{{ t('skills.retry') }}</a-button></template>
      </a-alert>
      <a-spin :spinning="store.loading && !store.items.length">
        <div class="skills-list" :class="{ 'skills-list-empty': !store.items.length }" :aria-busy="store.loading">
          <a-empty v-if="!store.items.length && !store.loading && !store.error" :description="t('skills.empty')" />
          <article v-for="skill in store.items" :key="skill.id" class="skill-row">
            <SkillIcon :icon="skill.icon" :name="skill.name" />
            <div class="skill-details">
              <div class="skill-heading"><h2 :title="skill.name">{{ skill.name }}</h2><a-tag :class="{ 'skill-source-github': detectSkillSource(skill.source_url) === 'github' }" :color="skillSourceColor(detectSkillSource(skill.source_url))">{{ skillSourceLabel(detectSkillSource(skill.source_url)) }}</a-tag></div>
              <p v-if="skill.description" :title="skill.description">{{ skill.description }}</p>
            </div>
            <span v-if="skill.sharer" class="skill-sharer" :title="skill.sharer"><UserOutlined />{{ skill.sharer }}</span>
            <a v-if="safeSkillURL(skill.source_url)" class="source-link" :href="safeSkillURL(skill.source_url)" target="_blank" rel="noopener noreferrer" :aria-label="t('skills.openSource', { name: skill.name })" :title="t('skills.openSource', { name: skill.name })"><ExportOutlined /></a>
          </article>
        </div>
      </a-spin>
      <div v-if="store.loading && store.items.length" class="skills-loading" role="status" :aria-label="t('skills.loadingMore')"><a-spin size="small" /></div>
    </div>
    <CreateSkillModal v-model:open="createOpen" @saved="onCreated" />
  </section>
</template>

<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { SearchOutlined, ShareAltOutlined, UserOutlined, ExportOutlined } from '@ant-design/icons-vue'
import { useAppI18n } from '@/i18n'
import { detectSkillSource, skillSourceLabel, skillSourceColor } from '@/views/skills/source'
import { safeSkillURL } from '@/views/skills/icons'
import { useSkillStore } from '@/stores/skill'
import { useAuthStore } from '@/stores/auth'
import SkillIcon from './components/SkillIcon.vue'
import CreateSkillModal from './components/CreateSkillModal.vue'

const { t } = useAppI18n()
const store = useSkillStore()
const auth = useAuthStore()
const search = ref('')
const createOpen = ref(false)
const contentRef = ref<HTMLElement>()
let activeView = false
let resizeObserver: ResizeObserver | undefined
let appliedSearch = ''
let searchTimer: ReturnType<typeof setTimeout> | undefined
function searchNow() {
  clearTimeout(searchTimer)
  appliedSearch = search.value.trim()
  if (contentRef.value) contentRef.value.scrollTop = 0
  void store.load(1, appliedSearch)
}
function reload() {
  if (store.loaded && store.hasMore && search.value.trim() === appliedSearch) void store.loadMore()
  else searchNow()
}
function loadWhenNearBottom() {
  const content = contentRef.value
  if (!activeView || !content || store.loading || store.error || search.value.trim() !== appliedSearch) return
  if (content.scrollHeight - content.scrollTop - content.clientHeight <= 80) void store.loadMore()
}
function onCreated() { searchNow() }
watch(search, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(searchNow, 300)
})
watch(() => auth.cloudUser?.sessionGeneration || auth.cloudUser?.accountScope || '', () => {
  createOpen.value = false
  searchNow()
})
watch(() => store.loading, async loading => {
  if (!loading) {
    await nextTick()
    loadWhenNearBottom()
  }
}, { flush: 'post' })
onMounted(() => {
  activeView = true
  resizeObserver = new ResizeObserver(loadWhenNearBottom)
  if (contentRef.value) resizeObserver.observe(contentRef.value)
  searchNow()
})
onUnmounted(() => {
  activeView = false
  clearTimeout(searchTimer)
  resizeObserver?.disconnect()
})
</script>

<style scoped>
.skills-page { height: 100%; min-height: 0; display: flex; flex-direction: column; container-type: inline-size; background: #FFFFFF; color: #262626; font-size: 14px; line-height: 22px; }
.skills-header { padding: 16px 24px; flex-shrink: 0; display: flex; align-items: center; justify-content: space-between; gap: 24px; }
h1 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.skills-header p { margin: 4px 0 0; color: #8C8C8C; font-size: 14px; line-height: 22px; }
.header-actions { display: flex; align-items: center; gap: 8px; }
.header-actions :deep(.ant-input-affix-wrapper) { width: 240px; height: 32px; padding: 4px 12px; border-radius: 12px; font-size: 14px; line-height: 22px; }
.header-actions :deep(.ant-input-affix-wrapper .ant-input) { min-height: 0; padding: 0; font-size: 14px; line-height: 22px; }
.header-actions :deep(.ant-btn) { flex-shrink: 0; }
.skills-content { padding: 0 24px 24px; min-height: 0; overflow-y: auto; flex: 1; }
.load-error { margin-bottom: 16px; }
.skills-list { border: 1px solid #D9D9D9; border-radius: 12px; overflow: hidden; }
.skills-list-empty { min-height: 160px; }
.skills-list :deep(.ant-empty) { margin: 48px 0; }
.skill-row { display: flex; align-items: center; gap: 16px; padding: 20px; border-bottom: 1px solid #F0F0F0; }
.skill-row:last-child { border-bottom: 0; }
.skill-row:hover { background: #FBFBFC; }
.skill-details { flex: 1; min-width: 0; }
.skill-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
h2 { margin: 0; max-width: min(100%, 20cqw); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 16px; line-height: 24px; font-weight: 600; }
.skill-heading :deep(.ant-tag) { border: 0; border-radius: 999px; }
.skill-heading :deep(.skill-source-github) { background: #EDEFF2; }
.skill-details p { margin: 6px 0 0; max-width: min(100%, 50cqw); color: #8C8C8C; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; white-space: pre-wrap; font-size: 14px; line-height: 22px; }
.skill-sharer { display: flex; align-items: center; gap: 6px; color: #8C8C8C; max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; line-height: 20px; }
.source-link { display: inline-flex; align-items: center; justify-content: center; width: 32px; height: 32px; flex-shrink: 0; border-radius: 6px; color: #8C8C8C; font-size: 18px; }
.source-link:hover { color: #3157E2; background: #E5EFFF; }
.source-link:focus-visible { outline: 2px solid #3157E2; outline-offset: 2px; }
.skills-loading { display: flex; justify-content: center; padding: 16px 0; }
@media (max-width: 900px) { .skills-header { align-items: flex-start; flex-direction: column; gap: 16px; } .header-actions { width: 100%; } .header-actions :deep(.ant-input-affix-wrapper) { flex: 1; width: auto; } }
@media (max-width: 640px) { .skills-header { padding: 16px; } .skills-content { padding: 0 16px 16px; } .skill-row { padding: 16px; gap: 12px; flex-wrap: wrap; } .skill-details { flex-basis: calc(100% - 76px); } .skill-sharer { margin-left: 64px; flex: 1; max-width: none; } }
</style>
