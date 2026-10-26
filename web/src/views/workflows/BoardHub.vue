<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { AppstoreOutlined, PlusOutlined, SearchOutlined, UnorderedListOutlined } from '@ant-design/icons-vue'
import { useRoute, useRouter } from 'vue-router'
import TaskBoard from './components/TaskBoard.vue'
import TeamBoard from './components/TeamBoard.vue'
import TeamWorkLoginCard from './components/TeamWorkLoginCard.vue'
import boardViewIcon from '@/assets/board-view.svg'
import teamViewIcon from '@/assets/team-view.svg'
import { useAuthStore } from '@/stores/auth'
import { useAppI18n } from '@/i18n'
import type { BoardLayoutMode } from '@/types/task-board'

const { t } = useAppI18n()

type BoardTab = 'local' | 'team'
const BOARD_TAB_STORAGE_KEY = 'goteams.workflows.board-tab'
const LEGACY_LAYOUT_KEYS: Record<BoardTab, string> = {
  local: 'goteams.workflows.local-board-layout',
  team: 'goteams.workflows.team-board-layout',
}

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const preferenceKey = computed(() => {
  const user = authStore.cloudUser
  const scope = authStore.cloudLoggedIn && user
    ? user.accountScope || `legacy:${user.id}:${user.username}`
    : 'local'
  return `goteams.workflows.board.v1:${encodeURIComponent(scope)}`
})

function readPreferences() {
  const defaults = {
    tab: 'local' as BoardTab,
    layout: 'board' as BoardLayoutMode,
    localStatus: 'active',
    teamStatus: 'active',
  }
  try {
    const raw = window.localStorage.getItem(preferenceKey.value)
    if (!raw) {
      // 只迁移旧版有限枚举偏好，后续按账号独立保存。
      defaults.tab = window.localStorage.getItem(BOARD_TAB_STORAGE_KEY) === 'team' ? 'team' : 'local'
      defaults.layout = window.localStorage.getItem(LEGACY_LAYOUT_KEYS[defaults.tab]) === 'list' ? 'list' : 'board'
      return defaults
    }
    const saved: Record<string, unknown> = JSON.parse(raw)
    const tab = saved.tab === 'team' ? 'team' as const : 'local' as const
    // 旧版分别保存两处布局，合并时沿用上次所在页签的选择。
    const layout = saved.layout === 'board' || saved.layout === 'list' ? saved.layout : saved[tab]
    return {
      tab,
      layout: layout === 'list' ? 'list' as const : 'board' as const,
      localStatus: typeof saved.localStatus === 'string' && saved.localStatus ? saved.localStatus : 'active',
      teamStatus: typeof saved.teamStatus === 'string' && saved.teamStatus ? saved.teamStatus : 'active',
    }
  } catch {
    return defaults
  }
}

const preferences = ref(readPreferences())
function savePreferences() {
  try {
    window.localStorage.setItem(preferenceKey.value, JSON.stringify(preferences.value))
  } catch {
    // 禁用或已满的存储不阻止本次页面操作。
  }
}
watch(preferenceKey, (_key, previousKey) => {
  preferences.value = readPreferences()
  // 退出后保留团队登录深链；已登录账号切换时仍清除旧账号的页面偏好。
  if (authStore.cloudLoggedIn && previousKey !== 'goteams.workflows.board.v1:local' && route.query.tab !== undefined) {
    const query = { ...route.query }
    delete query.tab
    void router.replace({ query })
  }
})
const activeTab = computed<BoardTab>({
  get: () => {
    // 显式深链优先，只有主动切换才改变下次进入时的默认偏好。
    const tab = Array.isArray(route.query.tab) ? route.query.tab[0] : route.query.tab
    if (tab === 'team' || tab === 'local') return tab
    return authStore.cloudLoggedIn ? preferences.value.tab : 'local'
  },
  set: (val) => {
    preferences.value.tab = val
    savePreferences()
    void router.replace({ query: { ...route.query, tab: val } })
  },
})
const searchKeywords = ref<Record<BoardTab, string>>({ local: '', team: '' })
const activeSearchKeyword = computed({
  get: () => searchKeywords.value[activeTab.value],
  set: (value: string) => {
    searchKeywords.value[activeTab.value] = value
  },
})
const activeLayoutMode = computed<BoardLayoutMode>({
  get: () => preferences.value.layout,
  set: (mode) => {
    preferences.value.layout = mode
    savePreferences()
  },
})
function setStatusFilter(tab: BoardTab, status: string) {
  preferences.value[tab === 'local' ? 'localStatus' : 'teamStatus'] = status
  savePreferences()
}

function setLayoutMode(mode: BoardLayoutMode) {
  activeLayoutMode.value = mode
}
const boardRef = ref<{ openCreate: () => void }>()

// 未登录时团队工作展示登录引导卡片；已登录才渲染团队工作内容。
const showLoginGuide = computed(() => activeTab.value === 'team' && !authStore.cloudLoggedIn)
</script>

<template>
  <div class="board-hub">
    <header class="board-tabs">
      <div class="board-heading"><h1>{{ t('workflows.board.title') }}</h1></div>
      <div class="board-actions">
        <nav class="board-source-switch" :aria-label="t('workflows.board.viewSwitch')">
          <button
            :class="{ active: activeTab === 'local' }"
            :aria-current="activeTab === 'local' ? 'true' : undefined"
            @click="activeTab='local'"
          ><img :src="boardViewIcon" alt="" />{{ t('workflows.board.localTasks') }}</button>
          <button
            :class="{ active: activeTab === 'team' }"
            :aria-current="activeTab === 'team' ? 'true' : undefined"
            @click="activeTab='team'"
          ><img :src="teamViewIcon" alt="" />{{ t('workflows.board.teamWork') }}</button>
        </nav>
        <div class="board-action-end">
          <a-input
            v-if="!showLoginGuide"
            v-model:value="activeSearchKeyword"
            class="task-search"
            :placeholder="t('workflows.board.searchTasks')"
            :aria-label="t('workflows.board.searchTasks')"
            allow-clear
          >
            <template #suffix><SearchOutlined aria-hidden="true" /></template>
          </a-input>
          <nav class="layout-switch" :aria-label="t('workflows.board.layoutSwitch')">
            <a-tooltip :title="t('workflows.board.boardView')">
              <button
                type="button"
                :class="{ active: activeLayoutMode === 'board' }"
                :aria-label="t('workflows.board.boardView')"
                :aria-pressed="activeLayoutMode === 'board'"
                @click="setLayoutMode('board')"
              >
                <AppstoreOutlined aria-hidden="true" />
              </button>
            </a-tooltip>
            <a-tooltip :title="t('workflows.board.listView')">
              <button
                type="button"
                :class="{ active: activeLayoutMode === 'list' }"
                :aria-label="t('workflows.board.listView')"
                :aria-pressed="activeLayoutMode === 'list'"
                @click="setLayoutMode('list')"
              >
                <UnorderedListOutlined aria-hidden="true" />
              </button>
            </a-tooltip>
          </nav>
          <a-button v-if="activeTab === 'local'" class="create-task" type="primary" @click="boardRef?.openCreate()"><template #icon><PlusOutlined /></template>{{ t('workflows.board.newTask') }}</a-button>
        </div>
      </div>
    </header>
    <main
      class="board-content"
      :class="{ 'team-login-content': showLoginGuide }"
    >
      <TaskBoard
        v-if="activeTab === 'local'"
        ref="boardRef"
        :status-filter="preferences.localStatus"
        :view-mode="activeLayoutMode"
        :search-keyword="activeSearchKeyword"
        @update:status-filter="setStatusFilter('local', $event)"
      />
      <template v-else-if="showLoginGuide">
        <TeamWorkLoginCard @use-local="activeTab = 'local'" />
      </template>
      <TeamBoard
        v-else
        :key="authStore.cloudUser?.sessionGeneration || preferenceKey"
        :status-filter="preferences.teamStatus"
        :view-mode="activeLayoutMode"
        :search-keyword="activeSearchKeyword"
        @update:status-filter="setStatusFilter('team', $event)"
      />
    </main>
  </div>
</template>

<style scoped>
.board-hub { display: flex; height: 100%; min-height: 0; flex-direction: column; background: #fff; }
.board-tabs { display: flex; height: 96px; flex: 0 0 96px; flex-direction: column; margin: 0 0 20px; padding: 0 24px; background: #fff; }
.board-heading { display: flex; min-height: 44px; flex: 0 0 44px; align-items: center; margin: 0 -24px; padding: 0 24px; border-bottom: 1px solid #f0f0f0; }
.board-tabs h1 { margin: 0; color: #262626; font-size: 20px; font-weight: 600; line-height: 28px; }
.board-actions { display: flex; height: 52px; align-items: center; justify-content: space-between; gap: 20px; }
.board-source-switch,
.layout-switch { display: flex; align-items: center; gap: 2px; padding: 2px; border-radius: 32px; background: #edeff2; }
.board-source-switch button { display: flex; align-items: center; gap: 4px; padding: 3px 12px; border: 0; border-radius: 32px; color: #595959; background: transparent; cursor: pointer; font-size: 14px; font-weight: 400; line-height: 22px; }
.board-source-switch button img { width: 16px; height: 16px; }
.board-source-switch button:not(.active) img { opacity: .76; }
.board-source-switch button.active,
.layout-switch button.active { color: #262626; background: #fff; box-shadow: 0 1px 2px rgba(0,0,0,.08); font-weight: 600; }
.board-source-switch button.active img[src$="team-view.svg"] { filter: brightness(.43); opacity: 1; }
.board-action-end { display: flex; align-items: center; gap: 10px; }
.task-search { width: 240px; height: 32px; padding: 4px 11px; border-color: #dadde2; border-radius: 6px; box-shadow: none; font-size: 14px; }
.task-search :deep(.ant-input-suffix) { color: #555a61; font-size: 16px; }
.layout-switch button { display: inline-flex; width: 30px; height: 30px; align-items: center; justify-content: center; padding: 0; border: 0; border-radius: 50%; color: #595959; background: transparent; cursor: pointer; font-size: 16px; }
.board-source-switch button:focus-visible,
.layout-switch button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.create-task { min-width: 80px; }
.board-content { min-height: 0; flex: 1; }
.board-content:not(.team-login-content) { padding: 0 24px 24px; }
/* 设计稿把登录引导卡片在整列高度（含 96px 标题带）内居中，
   因此底部留白比顶部多 96px，卡片位置才与设计稿一致。 */
.team-login-content { display: flex; overflow-y: auto; padding: 24px 24px 120px; }
.team-login-content > * { margin: auto; }
@media (max-width: 900px) {
  .board-tabs { height: auto; flex-basis: auto; }
  .board-actions { height: auto; min-height: 52px; flex-wrap: wrap; gap: 12px; padding: 10px 0; }
  .board-action-end { min-width: 0; flex-wrap: wrap; }
  .task-search { width: 200px; max-width: 100%; }
}
@media (max-width: 640px) { .board-tabs { padding: 0 16px; } .board-heading { margin: 0 -16px; padding: 0 16px; } .board-content:not(.team-login-content) { padding: 0 16px 16px; } .team-login-content { padding: 16px 16px 112px; } }
</style>
