<template>
  <aside
    v-show="!sidebarCollapsed"
    class="app-sidebar"
    :class="{
      'theme-dark': isDark,
      'mac-desktop': isMacDesktop,
      'windows-desktop': isWindowsDesktop,
      'is-resizing': sidebarResizing,
    }"
    :style="{ '--app-sidebar-width': `${sidebarWidth}px` }"
  >
    <div class="sidebar-header">
      <div class="sidebar-top-bar">
        <button
          type="button"
          class="sidebar-collapse-btn"
          :aria-label="t('layout.sidebar.collapse')"
          :title="t('layout.sidebar.collapse')"
          @click="collapseSidebar"
        >
          <img
            :src="sidebarToggleIcon"
            alt=""
            class="collapse-icon"
          />
        </button>
        <button type="button" class="sidebar-collapse-btn" :aria-label="t('layout.sidebar.searchConversations')" :title="t('layout.sidebar.searchConversations')" @click="searchOpen = true"><SidebarMenuIcon name="search" :dark="isDark" /></button>
      </div>

      <!-- 品牌行 -->
      <div class="sidebar-brand">
        <img
          class="brand-logo"
          :src="isDark ? logoDark : logo"
          alt="Logo"
        />
        <span class="brand-name">TeamBuddy</span>
      </div>
    </div>

    <!-- 顶部主导航菜单 -->
    <nav class="sidebar-primary-nav" :aria-label="t('layout.sidebar.navigation')">
      <!-- 新对话 -->
      <RouterLink
        to="/tasks/new"
        class="nav-item new-conversation-item"
        :class="{ active: isNewConversationActive }"
      >
        <SidebarMenuIcon class="nav-icon" name="newConversation" :dark="isDark" />
        <span class="nav-label">{{ t('layout.sidebar.newConversation') || '新对话' }}</span>
      </RouterLink>

      <!-- 看板 -->
      <RouterLink
        :to="boardReturnPath"
        class="nav-item"
        :class="{ active: activeKey === 'workflows' }"
      >
        <SidebarMenuIcon class="nav-icon" name="dashboard" :dark="isDark" />
        <span class="nav-label">{{ t('layout.menu.workflows') }}</span>
      </RouterLink>

      <!-- Agent员工 -->
      <RouterLink
        to="/agents"
        class="nav-item"
        :class="{ active: activeKey === 'agents' }"
      >
        <SidebarMenuIcon class="nav-icon" name="agent" :dark="isDark" />
        <span class="nav-label">{{ t('layout.menu.agents') }}</span>
      </RouterLink>

      <!-- 知识库 -->
      <RouterLink
        to="/knowledge"
        class="nav-item"
        :class="{ active: activeKey === 'knowledge' }"
      >
        <SidebarMenuIcon class="nav-icon" name="book" :dark="isDark" />
        <span class="nav-label">{{ t('layout.menu.knowledge') }}</span>
      </RouterLink>

      <!-- 远程通道 -->
      <RouterLink
        to="/remote-channels"
        class="nav-item"
        :class="{ active: activeKey === 'remote-channels' }"
      >
        <SidebarMenuIcon class="nav-icon" name="remote" :dark="isDark" />
        <span class="nav-label">{{ t('layout.menu.remoteChannels') }}</span>
      </RouterLink>

      <!-- 更多菜单（悬停或点击展示项目、配置中心、命令、接口管理） -->
      <a-dropdown
        v-model:open="moreMenuOpen"
        :trigger="['hover']"
        placement="bottomRight"
        overlay-class-name="sidebar-more-dropdown"
      >
        <button
          type="button"
          class="nav-item more-item"
          :class="{ active: isMoreMenuActive }"
          :aria-expanded="moreMenuOpen"
          aria-haspopup="menu"
          @click="moreMenuOpen = true"
          @keydown.esc="moreMenuOpen = false"
        >
          <span class="more-item__left">
            <SidebarMenuIcon class="nav-icon" name="more" :dark="isDark" />
            <span class="nav-label">{{ t('layout.sidebar.more') || '更多' }}</span>
          </span>
          <RightOutlined class="more-chevron" />
        </button>

        <template #overlay>
          <div class="more-menu-panel" role="menu" @click="moreMenuOpen = false" @keydown.esc="moreMenuOpen = false">
            <RouterLink
              to="/projects"
              class="more-menu-item"
              :class="{ active: activeKey === 'projects' }"
            >
              <SidebarMenuIcon class="item-icon" name="project" :dark="isDark" />
              <span>{{ t('layout.menu.projects') }}</span>
            </RouterLink>
            <RouterLink
              to="/settings"
              class="more-menu-item"
              :class="{ active: activeKey === 'settings' }"
            >
              <SidebarMenuIcon class="item-icon" name="settings" :dark="isDark" />
              <span>{{ t('layout.menu.settings') }}</span>
            </RouterLink>
            <RouterLink
              to="/commands"
              class="more-menu-item"
              :class="{ active: activeKey === 'commands' }"
            >
              <SidebarMenuIcon class="item-icon" name="command" :dark="isDark" />
              <span>{{ t('layout.menu.commands') }}</span>
            </RouterLink>
            <RouterLink
              to="/apis"
              class="more-menu-item"
              :class="{ active: activeKey === 'apis' }"
            >
              <SidebarMenuIcon class="item-icon" name="api" :dark="isDark" />
              <span>{{ t('layout.menu.apis') }}</span>
            </RouterLink>
          </div>
        </template>
      </a-dropdown>
    </nav>

    <div class="workspace-divider" />
    <div class="workspace-toolbar">
      <button type="button" class="workspace-toggle" :aria-expanded="!workspacesCollapsed" @click="workspacesCollapsed = !workspacesCollapsed">
        {{ t('layout.sidebar.workspaces', { count: conversationStore.activeGroups.length }) }}
        <SidebarMenuIcon name="conversationChevron" :dark="isDark" :class="{ 'workspace-expanded': !workspacesCollapsed }" />
      </button>
      <button type="button" class="workspace-add" :aria-label="t('layout.sidebar.addWorkspace')" @click="addWorkspaceOpen = true"><SidebarMenuIcon name="workspaceAdd" :dark="isDark" /></button>
    </div>
    <ConversationSearchModal v-model:open="searchOpen" />
    <AddWorkspaceModal v-model:open="addWorkspaceOpen" />

    <!-- 对话列表区域（按工作目录分组，自适应高度，单独滚动） -->
    <div v-show="!workspacesCollapsed" class="sidebar-conversations-scroll scrollbar--subtle">
      <div
        v-if="conversationStore.loading && !conversationStore.loaded"
        class="conversations-loading"
      >
        <a-spin size="small" />
      </div>

      <div
        v-else-if="conversationStore.error && !conversationStore.allConversations.length"
        class="conversations-error"
      >
        <span>{{ conversationStore.error }}</span>
        <button
          type="button"
          class="retry-btn"
          @click="conversationStore.loadConversations(true)"
        >
          {{ t('common.actions.retry') }}
        </button>
      </div>

      <!-- 未归档对话分组列表 -->
      <div
        v-for="group in conversationStore.activeGroups"
        :key="group.key"
        class="directory-group"
      >
        <WorkspaceHoverCard :group="group">
          <div
            class="group-header"
            role="button" tabindex="0" :aria-expanded="!conversationStore.isGroupCollapsed(group.key)" @keydown.enter.self.prevent="toggleGroup(group.key)" @keydown.space.self.prevent="toggleGroup(group.key)"
            @click="toggleGroup(group.key)"
          >
            <div class="group-header__main">
              <SidebarMenuIcon class="group-folder-icon" :name="conversationStore.isGroupCollapsed(group.key) ? 'conversationFolderClosed' : 'conversationFolderOpen'" :dark="isDark" />
              <span class="group-name">{{ group.displayName }}</span>
              <PushpinOutlined v-if="group.pinnedAt" class="group-pin" :aria-label="t('layout.sidebar.pinWorkspace')" />
            </div>

            <a-tooltip :title="t('layout.sidebar.addConversationInDir') || '在此目录新增对话'">
              <button
                type="button"
                class="group-add-btn"
                :aria-label="t('layout.sidebar.addConversationInDir')"
                @click.stop="goToNewConversationWithDir(group.workDir)"
              >
                <SidebarMenuIcon name="conversationAdd" :dark="isDark" />
              </button>
            </a-tooltip>
            <WorkspaceActions :group="group" :dark="isDark" />
          </div>
        </WorkspaceHoverCard>

        <div
          v-show="!conversationStore.isGroupCollapsed(group.key)"
          class="group-conversations"
        >
          <ConversationHoverCard
            v-for="conv in group.items"
            :key="conv.task_uuid"
            :conversation="conv"
          >
            <div
              class="conversation-entry"
              :class="{ active: isConversationActive(conv.task_uuid) }"
              role="button"
              tabindex="0"
              @keydown.enter.self.prevent="selectConversation(conv)"
              @keydown.space.self.prevent="selectConversation(conv)"
              @click="selectConversation(conv)"
            >
              <span
                class="conv-title"
              >{{ conv.title }}</span>

              <div class="conv-actions">
                <!-- 未读红点 -->
                <span
                  v-if="conv.unread"
                  class="unread-dot"
                  :title="t('workflows.task.common.unread')"
                />

                <!-- 运行状态与更多操作共用一个位置：默认显示 loading，悬浮时切换为更多。 -->
                <span class="conv-action-slot">
                  <LoadingOutlined
                    v-if="conv.running"
                    class="conversation-running"
                    spin
                    :title="t('layout.sidebar.conversationRunning')"
                  />
                  <a-dropdown
                    :trigger="['click']"
                    placement="bottomRight"
                  >
                    <button
                      type="button"
                      class="conv-more-btn"
                      :aria-label="t('layout.sidebar.more')"
                      @click.stop
                    >
                      <SidebarMenuIcon name="conversationMore" :dark="isDark" />
                    </button>
                    <template #overlay>
                      <ConversationActionsMenu
                        :unread="conv.unread"
                        :pending="taskActionPending"
                        @read="toggleReadState(conv)"
                        @archive="toggleArchiveState(conv)"
                        @rename="openRename(conv)"
                        @delete="confirmDelete(conv)"
                      />
                    </template>
                  </a-dropdown>
                </span>
              </div>
            </div>
          </ConversationHoverCard>
        </div>
      </div>

      <!-- 已归档折叠组（默认收起） -->
      <div
        v-if="conversationStore.archivedConversations.length > 0"
        class="archived-section"
      >
        <div
          class="archived-header"
          @click="conversationStore.toggleArchivedCollapse"
        >
          <div class="archived-header__left">
            <SidebarMenuIcon
              name="conversationChevron"
              :dark="isDark"
              class="group-chevron"
              :class="{ 'is-expanded': !conversationStore.archivedCollapsed }"
            />
            <span class="archived-label">{{ t('layout.sidebar.archived') || '已归档' }}</span>
          </div>
          <span class="archived-count">{{ conversationStore.archivedConversations.length }}</span>
        </div>

        <div
          v-show="!conversationStore.archivedCollapsed"
          class="archived-content"
        >
          <div
            v-for="group in conversationStore.archivedGroups"
            :key="`archived-${group.key}`"
            class="directory-group archived-group"
          >
            <WorkspaceHoverCard :group="group">
              <div
                class="group-header"
                role="button" tabindex="0" :aria-expanded="!conversationStore.isGroupCollapsed(`archived-${group.key}`)" @keydown.enter.self.prevent="toggleGroup(`archived-${group.key}`)" @keydown.space.self.prevent="toggleGroup(`archived-${group.key}`)"
                @click="toggleGroup(`archived-${group.key}`)"
              >
                <div class="group-header__main">
                  <SidebarMenuIcon class="group-folder-icon" :name="conversationStore.isGroupCollapsed(`archived-${group.key}`) ? 'conversationFolderClosed' : 'conversationFolderOpen'" :dark="isDark" />
                  <span class="group-name">{{ group.displayName }}</span>
                </div>
                <WorkspaceActions :group="group" :dark="isDark" />
              </div>
            </WorkspaceHoverCard>

            <div
              v-show="!conversationStore.isGroupCollapsed(`archived-${group.key}`)"
              class="group-conversations"
            >
              <ConversationHoverCard
                v-for="conv in group.items"
                :key="conv.task_uuid"
                :conversation="conv"
              >
                <div
                  class="conversation-entry archived-entry"
                  :class="{ active: isConversationActive(conv.task_uuid) }"
                  role="button"
                  tabindex="0"
                  @keydown.enter.self.prevent="selectConversation(conv)"
                  @keydown.space.self.prevent="selectConversation(conv)"
                  @click="selectConversation(conv)"
                >
                  <span
                    class="conv-title"
                  >{{ conv.title }}</span>

                  <div class="conv-actions">
                    <a-dropdown
                      :trigger="['click']"
                      placement="bottomRight"
                    >
                      <button
                        type="button"
                        class="conv-more-btn"
                        :aria-label="t('layout.sidebar.more')"
                        @click.stop
                      >
                        <SidebarMenuIcon name="conversationMore" :dark="isDark" />
                      </button>
                      <template #overlay>
                        <ConversationActionsMenu
                          :unread="conv.unread"
                          archived
                          :pending="taskActionPending"
                          @read="toggleReadState(conv)"
                          @archive="toggleArchiveState(conv)"
                          @rename="openRename(conv)"
                          @delete="confirmDelete(conv)"
                        />
                      </template>
                    </a-dropdown>
                  </div>
                </div>
              </ConversationHoverCard>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 底部：用户信息与设置上滑菜单 -->
    <footer class="sidebar-footer">
      <div
        class="user-info-card"
        @click="handleUserClick"
      >
        <div class="user-avatar">
          <UserOutlined />
        </div>
        <div class="user-text">
          <span class="user-title">{{ userNickname }}</span>
          <span class="user-sub">{{ authStore.cloudLoggedIn ? (authStore.cloudUser?.serverUrl || authStore.cloudUser?.username || 'GoTeams') : (t('layout.sidebar.guestLogin') || '本地 · 点击登录') }}</span>
        </div>
      </div>

      <!-- 设置按钮（点击上滑弹出菜单） -->
      <a-dropdown
        v-model:open="settingsMenuOpen"
        :trigger="['click']"
        placement="topLeft"
        overlay-class-name="sidebar-settings-popover"
      >
        <button
          type="button"
          class="settings-btn"
          :class="{ 'is-open': settingsMenuOpen }"
          :aria-label="t('layout.menu.settings')"
        >
          <SidebarMenuIcon name="footerSettings" :dark="isDark" />
        </button>

        <template #overlay>
          <div
            class="settings-up-menu"
            role="menu"
            @click.stop
          >
            <!-- 用户状态行 -->
            <button
              type="button"
              class="menu-user-row"
              @click="handleUserClick"
            >
              <div class="user-avatar small">
                <UserOutlined />
              </div>
              <div class="user-text">
                <span class="user-title">{{ userNickname }}</span>
                <span class="user-sub">{{ authStore.cloudLoggedIn ? (authStore.cloudUser?.serverUrl || authStore.cloudUser?.username || '') : (t('layout.sidebar.guestLogin') || '本地 · 点击登录') }}</span>
              </div>
            </button>

            <div class="menu-divider" />

            <!-- 外观切换菜单项 -->
            <a-dropdown
              placement="rightTop"
              :trigger="['hover', 'click']"
            >
              <div class="settings-menu-row">
                <div class="row-left">
                  <BulbOutlined class="row-icon" />
                  <span>{{ t('layout.sidebar.theme') || '外观' }}</span>
                </div>
                <div class="row-right">
                  <span class="current-val">{{ currentThemeLabel }}</span>
                  <RightOutlined class="chevron-right" />
                </div>
              </div>
              <template #overlay>
                <div class="sub-options-panel">
                  <button
                    type="button"
                    class="sub-option"
                    :class="{ 'is-selected': currentThemeConfig === 'light' }"
                    @click="chooseTheme('light')"
                  >
                    <span>{{ t('layout.sidebar.themeLight') || '浅色' }}</span>
                    <CheckOutlined v-if="currentThemeConfig === 'light'" class="check-icon" />
                  </button>
                  <button
                    type="button"
                    class="sub-option"
                    :class="{ 'is-selected': currentThemeConfig === 'dark' }"
                    @click="chooseTheme('dark')"
                  >
                    <span>{{ t('layout.sidebar.themeDark') || '深色' }}</span>
                    <CheckOutlined v-if="currentThemeConfig === 'dark'" class="check-icon" />
                  </button>
                  <button
                    type="button"
                    class="sub-option"
                    :class="{ 'is-selected': currentThemeConfig === 'auto' }"
                    @click="chooseTheme('auto')"
                  >
                    <span>{{ t('layout.sidebar.themeAuto') || '跟随系统' }}</span>
                    <CheckOutlined v-if="currentThemeConfig === 'auto'" class="check-icon" />
                  </button>
                </div>
              </template>
            </a-dropdown>

            <!-- 语言切换菜单项 -->
            <a-dropdown
              placement="rightTop"
              :trigger="['hover', 'click']"
            >
              <div class="settings-menu-row">
                <div class="row-left">
                  <GlobalOutlined class="row-icon" />
                  <span>{{ t('layout.sidebar.language') || '语言' }}</span>
                </div>
                <div class="row-right">
                  <span class="current-val">{{ currentLocaleLabel }}</span>
                  <RightOutlined class="chevron-right" />
                </div>
              </div>
              <template #overlay>
                <div class="sub-options-panel">
                  <button
                    type="button"
                    class="sub-option"
                    :class="{ 'is-selected': locale === 'zh-CN' }"
                    @click="chooseLocale('zh-CN')"
                  >
                    <span>{{ t('layout.sidebar.languageChinese') }}</span>
                    <CheckOutlined v-if="locale === 'zh-CN'" class="check-icon" />
                  </button>
                  <button
                    type="button"
                    class="sub-option"
                    :class="{ 'is-selected': locale === 'en-US' }"
                    @click="chooseLocale('en-US')"
                  >
                    <span>{{ t('layout.sidebar.languageEnglish') }}</span>
                    <CheckOutlined v-if="locale === 'en-US'" class="check-icon" />
                  </button>
                </div>
              </template>
            </a-dropdown>

            <button
              type="button"
              class="settings-menu-row"
              @click="reviewOnboarding"
            >
              <div class="row-left">
                <CompassOutlined class="row-icon" />
                <span>{{ t('layout.sidebar.onboarding') }}</span>
              </div>
            </button>

            <!-- 退出登录（如已登录） -->
            <template v-if="authStore.cloudLoggedIn">
              <div class="menu-divider" />
              <button
                type="button"
                class="settings-menu-row logout-row"
                @click="handleLogout"
              >
                <div class="row-left">
                  <LogoutOutlined class="row-icon" />
                  <span>{{ t('layout.sidebar.logout') || '退出登录' }}</span>
                </div>
              </button>
            </template>
          </div>
        </template>
      </a-dropdown>
    </footer>
    <a-modal
      :open="renameTaskUuid !== ''"
      :title="t('layout.sidebar.renameTask')"
      :ok-text="t('common.actions.save')"
      :cancel-text="t('common.actions.cancel')"
      :confirm-loading="taskActionPending"
      :ok-button-props="{ disabled: taskActionPending }"
      :cancel-button-props="{ disabled: taskActionPending }"
      :closable="!taskActionPending"
      :mask-closable="!taskActionPending"
      :keyboard="!taskActionPending"
      @ok="submitRename"
      @cancel="renameTaskUuid = ''"
    >
      <a-form layout="vertical">
        <a-form-item
          :label="t('layout.sidebar.taskName')"
          :validate-status="renameError ? 'error' : undefined"
          :help="renameError"
        >
          <a-input
            v-model:value="renameTitle"
            autofocus
            :disabled="taskActionPending"
            @press-enter="submitRename"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <div
      class="sidebar-resize-handle"
      role="separator"
      tabindex="0"
      aria-orientation="vertical"
      :aria-valuemin="SIDEBAR_MIN_WIDTH"
      :aria-valuemax="SIDEBAR_MAX_WIDTH"
      :aria-valuenow="sidebarWidth"
      @pointerdown="startSidebarResize"
      @keydown="handleSidebarResizeKeydown"
    />
  </aside>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'ant-design-vue'
import {
  PushpinOutlined,
  RightOutlined,
  UserOutlined,
  BulbOutlined,
  GlobalOutlined,
  LogoutOutlined,
  CheckOutlined,
  CompassOutlined,
  LoadingOutlined,
} from '@ant-design/icons-vue'
import {
  SIDEBAR_DEFAULT_WIDTH,
  SIDEBAR_MAX_WIDTH,
  SIDEBAR_MIN_WIDTH,
  useAppStore,
} from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useConversationStore, type TaskConversation } from '@/stores/conversation'
import { useAppI18n } from '@/i18n'
import { useOnboardingGuide } from '@/composables/useOnboardingGuide'
import { useLocale } from '@/composables/useLocale'
import { applySession } from '@/composables/useCloudSession'
import { clearTaskFilesIndexCache } from '@/composables/useTaskFilesIndex'
import { isMacDesktopRuntime, isWindowsDesktopRuntime } from '@/composables/useDesktop'
import apiClient from '@/api/client'
import logo from '@/assets/logo.svg'
import logoDark from '@/assets/logo-dark.svg'
import sidebarToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import SidebarMenuIcon from './SidebarMenuIcon.vue'
import WorkspaceActions from './WorkspaceActions.vue'
import ConversationSearchModal from './ConversationSearchModal.vue'
import AddWorkspaceModal from './AddWorkspaceModal.vue'
import ConversationActionsMenu from './ConversationActionsMenu.vue'
import ConversationHoverCard from './ConversationHoverCard.vue'
import WorkspaceHoverCard from './WorkspaceHoverCard.vue'

type ThemeMode = 'light' | 'dark' | 'auto'

const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const authStore = useAuthStore()
const conversationStore = useConversationStore()
const boardReturnPath = ref('/board')
const boardAccountScope = computed(() => {
  const user = authStore.cloudUser
  return authStore.cloudLoggedIn && user
    ? user.accountScope || `legacy:${user.id}:${user.username}`
    : 'local'
})
watch(boardAccountScope, () => {
  boardReturnPath.value = '/board'
})
watch(() => route.fullPath, (path) => {
  // 保留本次应用会话的看板返回位置；主动返回看板时自然替换任务详情位置。
  if (route.name === 'board' || route.name === 'board-task-detail' || route.name === 'workflows-task-detail') {
    boardReturnPath.value = path
  }
}, { immediate: true })
const { t } = useAppI18n()
const { replay: replayOnboarding } = useOnboardingGuide()
const { locale, setLocale } = useLocale()

const searchOpen = ref(false)
const addWorkspaceOpen = ref(false)
const workspacesCollapsed = ref(false)
const moreMenuOpen = ref(false)
const settingsMenuOpen = ref(false)
const renameTaskUuid = ref('')
const renameTitle = ref('')
const renameError = ref('')
const taskActionPending = ref(false)
const sidebarResizing = ref(false)
let sidebarResizeStartX = 0
let sidebarResizeStartWidth = 0
let previousBodyCursor = ''
let previousBodyUserSelect = ''

const sidebarCollapsed = computed(() => appStore.sidebarCollapsed)
watch([() => route.fullPath, sidebarCollapsed], () => { moreMenuOpen.value = false })
const sidebarWidth = computed(() => appStore.sidebarWidth)
const isMacDesktop = isMacDesktopRuntime()
const isWindowsDesktop = isWindowsDesktopRuntime()
const userNickname = computed(() => {
  if (!authStore.cloudLoggedIn) return t('layout.sidebar.notLoggedIn') || '未登录'
  return authStore.cloudUser?.displayName?.trim()
    || '已登录'
})

function readThemePreference(): ThemeMode {
  try {
    const raw = localStorage.getItem('goteams.theme.mode')
    if (raw === 'light' || raw === 'dark' || raw === 'auto') return raw
    return 'light'
  } catch {
    return 'light'
  }
}

const currentThemeConfig = ref<ThemeMode>(readThemePreference())
const systemDark = ref(
  typeof window !== 'undefined' ? window.matchMedia('(prefers-color-scheme: dark)').matches : false,
)

let mediaQueryListener: ((e: MediaQueryListEvent) => void) | null = null

function openRename(conv: TaskConversation) {
  if (taskActionPending.value) return
  renameTaskUuid.value = conv.task_uuid
  renameTitle.value = conv.title
  renameError.value = ''
}

async function submitRename() {
  if (taskActionPending.value || !renameTaskUuid.value) return
  const title = renameTitle.value.trim()
  if (!title || /[\r\n]/.test(title) || Array.from(title).length > 200) {
    renameError.value = t('layout.sidebar.taskNameInvalid')
    return
  }
  taskActionPending.value = true
  try {
    await conversationStore.renameTask(renameTaskUuid.value, title)
    renameTaskUuid.value = ''
    message.success(t('layout.sidebar.taskRenamed'))
  } catch (err) {
    renameError.value = err instanceof Error ? err.message : t('layout.sidebar.taskRenameFailed')
  } finally {
    taskActionPending.value = false
  }
}

function confirmDelete(conv: TaskConversation) {
  if (taskActionPending.value) return
  // Capture the task, so navigation while the confirmation is open cannot
  // change which task is deleted.
  const taskUuid = conv.task_uuid
  Modal.confirm({
    title: t('layout.sidebar.deleteTaskConfirm', { title: conv.title }),
    content: t('layout.sidebar.deleteTaskWarning'),
    okText: t('common.actions.delete'),
    okType: 'danger',
    cancelText: t('common.actions.cancel'),
    onOk: async () => {
      if (taskActionPending.value) return Promise.reject()
      taskActionPending.value = true
      try {
        await conversationStore.deleteTask(taskUuid)
        message.success(t('workflows.task.detail.deleted'))
        if (route.query.taskUuid === taskUuid) {
          await router.replace({ name: 'tasks-new' })
        }
      } catch (err) {
        message.error(err instanceof Error ? err.message : t('workflows.task.detail.deleteFailed'))
        throw err
      } finally {
        taskActionPending.value = false
      }
    },
  })
}

onMounted(() => {
  if (typeof window !== 'undefined') {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    mediaQueryListener = (e) => {
      systemDark.value = e.matches
    }
    mq.addEventListener('change', mediaQueryListener)
  }
})

onUnmounted(() => {
  if (mediaQueryListener && typeof window !== 'undefined') {
    window.matchMedia('(prefers-color-scheme: dark)').removeEventListener('change', mediaQueryListener)
  }
  finishSidebarResize()
})

const isDark = computed(() => {
  if (currentThemeConfig.value === 'dark') return true
  if (currentThemeConfig.value === 'light') return false
  return systemDark.value
})


const currentThemeLabel = computed(() => {
  if (currentThemeConfig.value === 'light') return t('layout.sidebar.themeLight') || '浅色'
  if (currentThemeConfig.value === 'dark') return t('layout.sidebar.themeDark') || '深色'
  return t('layout.sidebar.themeAuto') || '跟随系统'
})

const currentLocaleLabel = computed(() => (locale.value === 'zh-CN' ? '中文' : 'English'))

const activeKey = computed(() => {
  const name = route.name as string | undefined
  if (!name) return ''
  if (name.startsWith('workflows') || name.startsWith('board')) return 'workflows'
  if (name.startsWith('apis')) return 'apis'
  if (name === 'remote-channels') return 'remote-channels'
  if (name === 'agents') return 'agents'
  if (name === 'projects') return 'projects'
  if (name === 'commands') return 'commands'
  if (name === 'knowledge') return 'knowledge'
  if (name === 'settings') return 'settings'
  return name
})

const isNewConversationActive = computed(() => {
  return route.name === 'tasks-new' || (route.path === '/tasks' && !route.query.taskUuid)
})

const isMoreMenuActive = computed(() => {
  return ['projects', 'settings', 'commands', 'apis'].includes(activeKey.value)
})

function isConversationActive(taskUuid: string): boolean {
  return route.name === 'tasks' && route.query.taskUuid === taskUuid
}

function collapseSidebar() {
  appStore.setSidebarCollapsed(true)
}

function updateSidebarWidth(event: PointerEvent) {
  appStore.setSidebarWidth(
    sidebarResizeStartWidth + event.clientX - sidebarResizeStartX,
    false,
  )
}

function finishSidebarResize() {
  if (!sidebarResizing.value) return
  sidebarResizing.value = false
  window.removeEventListener('pointermove', updateSidebarWidth)
  window.removeEventListener('pointerup', finishSidebarResize)
  window.removeEventListener('pointercancel', finishSidebarResize)
  document.body.style.cursor = previousBodyCursor
  document.body.style.userSelect = previousBodyUserSelect
  appStore.setSidebarWidth(sidebarWidth.value)
}

function startSidebarResize(event: PointerEvent) {
  if (event.button !== 0 || sidebarResizing.value) return
  event.preventDefault()
  sidebarResizing.value = true
  sidebarResizeStartX = event.clientX
  sidebarResizeStartWidth = sidebarWidth.value
  previousBodyCursor = document.body.style.cursor
  previousBodyUserSelect = document.body.style.userSelect
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  window.addEventListener('pointermove', updateSidebarWidth)
  window.addEventListener('pointerup', finishSidebarResize)
  window.addEventListener('pointercancel', finishSidebarResize)
}

function handleSidebarResizeKeydown(event: KeyboardEvent) {
  const step = event.shiftKey ? 20 : 10
  let nextWidth = sidebarWidth.value
  if (event.key === 'ArrowLeft') nextWidth -= step
  else if (event.key === 'ArrowRight') nextWidth += step
  else if (event.key === 'Home') nextWidth = SIDEBAR_MIN_WIDTH
  else if (event.key === 'End') nextWidth = SIDEBAR_MAX_WIDTH
  else if (event.key === 'Escape') nextWidth = SIDEBAR_DEFAULT_WIDTH
  else return
  event.preventDefault()
  appStore.setSidebarWidth(nextWidth)
}

function chooseTheme(mode: ThemeMode) {
  currentThemeConfig.value = mode
  try {
    localStorage.setItem('goteams.theme.mode', mode)
    localStorage.setItem('goteams.sidebar.theme', isDark.value ? 'dark' : 'light')
  } catch {
    // ignore
  }
}

function chooseLocale(targetLocale: 'zh-CN' | 'en-US') {
  setLocale(targetLocale)
}

function reviewOnboarding() {
  replayOnboarding()
  settingsMenuOpen.value = false
  if (route.name !== 'tasks-new') {
    void router.push({ name: 'tasks-new' })
  }
}

function handleUserClick() {
  settingsMenuOpen.value = false
  // 账号入口只定位到团队工作，由用户在页面中选择登录方式。
  void router.push({ path: '/board', query: { tab: 'team' } })
}

async function handleLogout() {
  try {
    await apiClient.post('/auth/logout')
    applySession({ local_authenticated: true, cloud_logged_in: false })
    clearTaskFilesIndexCache()
    void conversationStore.loadConversations(true).catch(() => {})
    settingsMenuOpen.value = false
    await router.replace({ path: '/board', query: { tab: 'team' } })
    message.success(t('layout.sidebar.logoutSuccess') || '已退出登录')
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('teamwork.login.failed'))
  }
}

function toggleGroup(key: string) {
  conversationStore.toggleGroupCollapse(key)
}

function selectConversation(conv: TaskConversation) {
  if (conv.unread) {
    void conversationStore.setTaskRead(conv.task_uuid, true)
  }
  void router.push({ name: 'tasks', query: { taskUuid: conv.task_uuid } })
}

function goToNewConversationWithDir(dir: string) {
  void router.push({ name: 'tasks-new', query: dir ? { workDir: dir } : undefined })
}

async function toggleReadState(conv: TaskConversation) {
  try {
    await conversationStore.setTaskRead(conv.task_uuid, conv.unread)
    message.success(conv.unread ? (t('workflows.task.common.markRead') || '已标为已读') : (t('workflows.task.common.markUnread') || '已设为未读'))
  } catch (err) {
    message.error(err instanceof Error ? err.message : '操作失败')
  }
}

async function toggleArchiveState(conv: TaskConversation) {
  try {
    await conversationStore.archiveTask(conv.task_uuid, !conv.is_archived)
    message.success(conv.is_archived ? (t('layout.sidebar.unarchive') || '已取消归档') : (t('workflows.task.feedback.archived') || '已归档'))
  } catch (err) {
    message.error(err instanceof Error ? err.message : '操作失败')
  }
}

onMounted(() => {
  void conversationStore.loadConversations()
})
</script>

<style scoped>
.workspace-divider { height: 1px; margin: 12px; background: #f0f0f0; }
.theme-dark .workspace-divider { background: rgba(255,255,255,.1); }
.workspace-toolbar { display: flex; align-items: center; justify-content: space-between; margin: 0 12px 4px; border-radius: 6px; color: #8c8c8c; }
.workspace-toggle, .workspace-add { display: flex; align-items: center; gap: 6px; min-height: 28px; padding: 4px; border: 0; border-radius: 6px; background: transparent; color: inherit; cursor: pointer; font-size: 12px; }
.workspace-toggle { flex: 1; min-width: 0; }
.workspace-add { flex-shrink: 0; width: 28px; justify-content: center; opacity: 0; pointer-events: none; }
.workspace-toolbar:hover .workspace-add,
.workspace-toolbar:has(:focus-visible) .workspace-add { opacity: 1; pointer-events: auto; }
.workspace-toolbar:hover { background: var(--sidebar-hover); }
.workspace-add:hover { background: var(--sidebar-active); }
.workspace-toggle:focus-visible, .workspace-add:focus-visible { outline: 2px solid #3157e2; }
.workspace-expanded { transform: rotate(90deg); }

.app-sidebar {
  --sidebar-bg: #f9f9f9;
  --sidebar-border: #f0f0f0;
  --sidebar-text: #262626;
  --sidebar-subtext: #8c8c8c;
  --sidebar-hover: #f0f0f0;
  --sidebar-active: #e9e9eb;
  --sidebar-item-radius: 8px;
  position: relative;
  width: var(--app-sidebar-width, 280px);
  min-width: var(--app-sidebar-width, 280px);
  flex-basis: var(--app-sidebar-width, 280px);
  height: 100vh;
  background: var(--sidebar-bg);
  border-right: 1px solid var(--sidebar-border);
  display: flex;
  flex-direction: column;
  user-select: none;
  overflow: hidden;
  transition: background-color 0.2s;
}

.sidebar-resize-handle {
  -webkit-app-region: no-drag;
  position: absolute;
  z-index: 10;
  top: 0;
  right: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  outline: none;
}

.sidebar-resize-handle::after {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 2px;
  background: transparent;
  content: '';
  transition: background 0.15s;
}

.sidebar-resize-handle:hover::after,
.sidebar-resize-handle:focus-visible::after,
.app-sidebar.is-resizing .sidebar-resize-handle::after {
  background: #3157e2;
}

.app-sidebar.theme-dark {
  --sidebar-bg: #0f172a;
  --sidebar-border: #1e293b;
  --sidebar-text: #f1f5f9;
  --sidebar-subtext: #94a3b8;
  --sidebar-hover: #1e293b;
  --sidebar-active: #334155;
}

.sidebar-header {
  flex-shrink: 0;
}

.sidebar-top-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  justify-content: flex-end;
  height: 38px;
  flex-shrink: 0;
  padding: 0 12px;
}

.mac-desktop .sidebar-top-bar {
  -webkit-app-region: drag;
}

.windows-desktop .sidebar-header {
  -webkit-app-region: drag;
  display: flex;
  align-items: center;
  gap: 8px;
  height: env(titlebar-area-height, 32px);
  padding: 0 12px 0 14px;
  border-bottom: 1px solid #e5e7eb;
}

.windows-desktop .sidebar-top-bar {
  order: 1;
  height: 100%;
  padding: 0;
}

.windows-desktop .sidebar-brand {
  flex: 1;
  min-width: 0;
  height: 100%;
  padding: 0;
}

.windows-desktop .brand-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sidebar-collapse-btn {
  -webkit-app-region: no-drag;
  color: var(--sidebar-text);
  font-size: 16px;
  width: 24px;
  height: 24px;
  border: none;
  background: transparent;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: background 0.15s;
}

.sidebar-collapse-btn:hover {
  background: var(--sidebar-hover);
}

.collapse-icon {
  display: block;
  width: 20px;
  height: 20px;
}

.app-sidebar.theme-dark .collapse-icon {
  filter: brightness(0) invert(1);
}

.sidebar-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 44px;
  padding: 0 14px;
}

.brand-logo {
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
}

.brand-name {
  font-size: 17px;
  font-weight: 600;
  color: var(--sidebar-text);
  letter-spacing: -0.5px;
}

.sidebar-primary-nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 4px 10px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 32px;
  padding: 0 10px;
  border-radius: var(--sidebar-item-radius);
  color: var(--sidebar-text);
  text-decoration: none;
  font-size: 14px;
  transition: background 0.15s, color 0.15s;
  cursor: pointer;
}

.nav-item:hover {
  background: var(--sidebar-hover);
}

.nav-item.active {
  background: var(--sidebar-active);
  font-weight: 500;
}

.nav-icon {
  display: inline-flex;
  width: 20px;
  height: 20px;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  font-size: 18px;
}

.nav-label {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.more-item {
  justify-content: space-between;
  border: 0;
  background: transparent;
  font-family: inherit;
  text-align: left;
}

.more-item__left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.more-chevron {
  font-size: 11px;
  color: var(--sidebar-subtext);
}

.more-menu-panel {
  width: 160px;
  background: #ffffff;
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
  padding: 4px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid #f0f0f0;
}

.more-menu-item {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 10px;
  border-radius: 6px;
  color: #262626;
  text-decoration: none;
  font-size: 13px;
  transition: background 0.15s;
}

.more-menu-item:hover,
.more-menu-item.active {
  background: #f5f5f5;
}

.more-menu-item .item-icon {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
}

.more-menu-item .item-icon :deep(img) {
  width: 14px;
  height: 14px;
  object-fit: contain;
  transform: translate(-50%, -50%);
}

.sidebar-conversations-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 10px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.conversations-loading {
  padding: 24px;
  text-align: center;
}

.conversations-error {
  padding: 16px;
  font-size: 12px;
  color: #ef4444;
  text-align: center;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.retry-btn {
  border: none;
  background: transparent;
  color: #2563eb;
  cursor: pointer;
  text-decoration: underline;
}

.directory-group {
  display: flex;
  flex-direction: column;
}

.group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 28px;
  padding: 0 8px;
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.15s;
}

.group-header:hover {
  background: var(--sidebar-hover);
}

.group-header :deep(.workspace-actions) {
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.18s;
}

.group-header:hover :deep(.workspace-actions),
.group-header:focus-visible :deep(.workspace-actions),
.group-header:has(:focus-visible) :deep(.workspace-actions),
.group-header :deep(.workspace-actions.is-open) {
  opacity: 1;
  pointer-events: auto;
}

@media (hover: none) {
  .workspace-add,
  .group-header :deep(.workspace-actions) {
    opacity: 1;
    pointer-events: auto;
  }
}

.group-header__main {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  flex: 1;
}

.group-chevron {
  transform: rotate(0deg);
  transition: transform 0.2s;
}

.group-chevron.is-expanded {
  transform: rotate(90deg);
}

.group-folder-icon {
  font-size: 14px;
  color: #595959;
}

.group-name {
  font-size: 14px;
  font-weight: 500;
  color: var(--sidebar-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-pin {
  font-size: 12px;
  flex-shrink: 0;
}

.group-add-btn {
  opacity: 0;
  width: 24px;
  height: 24px;
  padding: 0;
  border: none;
  background: transparent;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--sidebar-text);
  cursor: pointer;
  transition: opacity 0.15s, background 0.15s;
}

.group-header:hover .group-add-btn,
.group-header:focus-visible .group-add-btn,
.group-header:has(:focus-visible) .group-add-btn {
  opacity: 1;
}

.group-add-btn:hover {
  background: var(--sidebar-active);
}

.group-conversations {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding-left: 12px;
}

.conversation-entry {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 30px;
  padding: 0 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s;
  color: var(--sidebar-text);
}

.conversation-entry:hover {
  background: var(--sidebar-hover);
}

.conversation-entry.active {
  background: var(--sidebar-active);
  font-weight: 500;
}

.conv-title {
  flex: 1;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.conv-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.conv-action-slot {
  position: relative;
  display: inline-flex;
  width: 18px;
  height: 18px;
  align-items: center;
  justify-content: center;
}

.conversation-running {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #3157e2;
  font-size: 13px;
  pointer-events: none;
  transition: opacity 0.15s;
}

.unread-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #fb363f;
}

.conv-more-btn {
  position: absolute;
  inset: 0;
  opacity: 0;
  width: 18px;
  height: 18px;
  border: none;
  background: transparent;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--sidebar-subtext);
  cursor: pointer;
  transition: opacity 0.15s;
}

.conversation-entry:hover .conv-more-btn {
  opacity: 1;
}

.conversation-entry:hover .conversation-running {
  opacity: 0;
}

.conv-more-btn:hover {
  color: var(--sidebar-text);
}

.archived-entry .conv-more-btn {
  position: relative;
  inset: auto;
}

.archived-section {
  margin-top: 6px;
  border-top: 1px solid var(--sidebar-border);
  padding-top: 6px;
}

.archived-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 28px;
  padding: 0 6px;
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.15s;
}

.archived-header:hover {
  background: var(--sidebar-hover);
}

.archived-header__left {
  display: flex;
  align-items: center;
  gap: 6px;
}

.archived-label {
  font-size: 12px;
  color: var(--sidebar-subtext);
}

.archived-count {
  font-size: 11px;
  color: var(--sidebar-subtext);
}

.archived-content {
  padding-top: 4px;
}

.sidebar-footer {
  height: 52px;
  flex-shrink: 0;
  margin-top: auto;
  border-top: 1px solid var(--sidebar-border);
  padding: 0 10px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.user-info-card {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  padding: 4px 6px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s;
}

.user-info-card:hover {
  background: var(--sidebar-hover);
}

.user-avatar {
  width: 26px;
  height: 26px;
  border-radius: 50%;
  background: #e5e7eb;
  color: #4b5563;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  flex-shrink: 0;
}

.user-avatar.small {
  width: 24px;
  height: 24px;
  font-size: 12px;
}

.user-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.user-title {
  font-size: 13px;
  font-weight: 500;
  color: var(--sidebar-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  line-height: 16px;
}

.user-sub {
  font-size: 11px;
  color: var(--sidebar-subtext);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  line-height: 14px;
}

.settings-btn {
  width: 28px;
  height: 28px;
  border: none;
  background: transparent;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #595959;
  font-size: 16px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}

.settings-btn:hover,
.settings-btn.is-open {
  background: var(--sidebar-hover);
  color: var(--sidebar-text);
}

.settings-up-menu {
  width: 220px;
  background: #ffffff;
  border-radius: 10px;
  box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.1);
  padding: 8px;
  border: 1px solid #f0f0f0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.menu-user-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px 8px;
  width: 100%;
  border: none;
  border-radius: 6px;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s;
}

.menu-user-row:hover,
.menu-user-row:focus-visible {
  background: #f5f5f5;
  outline: none;
}

.menu-user-row:disabled {
  cursor: wait;
  opacity: 0.72;
}

.menu-divider {
  height: 1px;
  background: #f0f0f0;
  margin: 4px 0;
}

.settings-menu-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 32px;
  padding: 0 8px;
  border-radius: 6px;
  color: #262626;
  font-size: 13px;
  cursor: pointer;
  transition: background 0.15s;
  border: none;
  background: transparent;
  width: 100%;
}

.settings-menu-row:hover {
  background: #f5f5f5;
}

.row-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.row-icon {
  font-size: 14px;
}

.row-right {
  display: flex;
  align-items: center;
  gap: 4px;
}

.current-val {
  font-size: 12px;
  color: #8c8c8c;
}

.chevron-right {
  font-size: 10px;
  color: #8c8c8c;
}

.logout-row {
  color: #ef4444;
}

.sub-options-panel {
  width: 140px;
  background: #ffffff;
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
  padding: 4px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid #f0f0f0;
}

.sub-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 30px;
  padding: 0 8px;
  border: none;
  background: transparent;
  border-radius: 4px;
  color: #262626;
  font-size: 13px;
  cursor: pointer;
  transition: background 0.15s;
}

.sub-option:hover {
  background: #f5f5f5;
}

.sub-option.is-selected {
  background: #f0f7ff;
  color: #2563eb;
}

.check-icon {
  color: #2563eb;
  font-size: 12px;
}
</style>
