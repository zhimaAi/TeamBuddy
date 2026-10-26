<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter, type RouteLocationNormalizedLoaded } from 'vue-router'
import { notification } from 'ant-design-vue'
import apiClient from '@/api/client'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useConversationStore } from '@/stores/conversation'
import { removeDeletedTaskFromCache } from '@/utils/taskConversationCache'
import {
  isDesktopRuntime,
  isMacDesktopRuntime,
  isWindowsDesktopRuntime,
  onOpenTaskConversation,
  showTaskNotification,
  syncTrayTaskCount,
} from '@/composables/useDesktop'
import { useLocalWS } from '@/composables/useLocalWebSocket'
import { provideDocumentTitle } from '@/composables/useDocumentTitle'
import { useAppI18n } from '@/i18n'
import type { TaskWithDetails } from '@/types/task-detail'
import type { TaskNotification } from '@/types/pipeline'
import sidebarToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import AppSidebar from './components/AppSidebar.vue'

const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const authStore = useAuthStore()
const conversationStore = useConversationStore()
const isMacDesktop = isMacDesktopRuntime()
const isWindowsDesktop = isWindowsDesktopRuntime()
const { t } = useAppI18n()
const notificationPermission = ref<'default' | 'denied' | 'granted'>(
  typeof window !== 'undefined' && 'Notification' in window ? Notification.permission : 'denied',
)
const seenNotificationIds = new Set<string>()
const notificationFetchVersion = ref(0)
const MAX_REMEMBERED_NOTIFICATIONS = 500
const notifiedSessionUuids = new Set<string>()
const notifyingSessionUuids = new Set<string>()

const currentMenuTitle = computed(() =>
  route.meta.titleKey
    ? t(route.meta.titleKey)
    : typeof route.meta.title === 'string'
      ? route.meta.title
      : undefined,
)
provideDocumentTitle(currentMenuTitle)

function routeViewKey(currentRoute: RouteLocationNormalizedLoaded) {
  const key = currentRoute.name ?? currentRoute.path
  const hasTaskState = currentRoute.meta.menu === 'tasks' || currentRoute.params.taskUuid !== undefined
  return hasTaskState ? `${String(key)}:${authStore.cloudUser?.sessionGeneration || 'local'}` : key
}

const mainContentClasses = computed(() => {
  const layout = route.meta.contentLayout ?? 'default'
  const isScroll = route.meta.isScroll !== false
  const menu = route.meta.menu

  return {
    [`main-content-${layout}`]: true,
    'main-content-scroll': isScroll,
    'main-content-no-scroll': !isScroll,
    'api-content': menu === 'apis',
    'agents-content': menu === 'agents',
    'knowledge-content': menu === 'knowledge',
  }
})

// 侧边栏不再展示任务计数，但仍保持全局状态更新，供现有其他消费方继续使用。
useLocalWS('app.cli_count', (data: { count: number }) => {
  appStore.setCliTaskCount(data?.count ?? 0)
})

useLocalWS('app.notifications.unread', (data: { count: number }) => {
  const nextCount = data?.count ?? 0
  const previousCount = appStore.unreadTaskNotifications
  appStore.setUnreadTaskNotifications(nextCount)
  void conversationStore.loadConversations(true)
  void syncTaskNotifications(previousCount, nextCount)
})

useLocalWS('task.changed', (data: { task_uuid?: string; deleted?: boolean }) => {
  if (data.deleted && data.task_uuid) {
    void removeDeletedTaskFromCache(data.task_uuid).catch(() => {})
  }
  void conversationStore.loadConversations(true)
})

useLocalWS(
  'executor.completed',
  (data: { task_uuid?: string; step_key?: string; session_uuid?: string; status?: string }) => {
    if (!isDesktopRuntime() || data.status !== 'success' || !data.task_uuid || !data.session_uuid) {
      return
    }
    if (
      notifiedSessionUuids.has(data.session_uuid) ||
      notifyingSessionUuids.has(data.session_uuid)
    ) {
      return
    }
    notifyingSessionUuids.add(data.session_uuid)
    void notifyTaskReady(data.task_uuid, data.step_key || '', data.session_uuid)
  },
)

async function notifyTaskReady(taskUuid: string, stepKey: string, sessionUuid: string) {
  try {
    const task = await apiClient.get<TaskWithDetails>(`/tasks/${encodeURIComponent(taskUuid)}`)
    const step = task.steps.find((item) => item.step_key === stepKey || item.uuid === stepKey)
    const result = await showTaskNotification({
      title: t('layout.notifications.readyTitle', { step: step?.name || 'Agent' }),
      body: t('layout.notifications.openTask', { task: task.title }),
      taskUuid,
      stepUuid: step?.uuid,
    })
    if (result.shown || result.reason === 'unsupported') {
      notifiedSessionUuids.add(sessionUuid)
      if (notifiedSessionUuids.size > MAX_REMEMBERED_NOTIFICATIONS) {
        const oldestSessionUuid = notifiedSessionUuids.values().next().value
        if (oldestSessionUuid) notifiedSessionUuids.delete(oldestSessionUuid)
      }
    } else {
      notification.warning({
        key: 'desktop-notification-unavailable',
        message: t('layout.notifications.unavailableTitle'),
        description: result.message || t('layout.notifications.unavailableHint'),
        duration: 0,
      })
    }
  } catch {
    notification.warning({
      key: 'desktop-notification-unavailable',
      message: t('layout.notifications.sendFailedTitle'),
      description: t('layout.notifications.sendFailedHint'),
      duration: 0,
    })
  } finally {
    notifyingSessionUuids.delete(sessionUuid)
  }
}

watch(() => appStore.cliTaskCount, syncTrayTaskCount, { immediate: true })

async function syncTaskNotifications(previousCount: number, nextCount: number) {
  if (typeof window === 'undefined' || !('Notification' in window)) return
  if (route.name === 'tasks') return
  if (notificationPermission.value === 'default') {
    notificationPermission.value = await Notification.requestPermission()
  }
  if (notificationPermission.value !== 'granted' || nextCount <= previousCount) {
    return
  }
  const requestVersion = ++notificationFetchVersion.value
  try {
    const result = await apiClient.get<{ items: TaskNotification[] }>('/notifications')
    if (requestVersion !== notificationFetchVersion.value) return
    const items = (result.items || []).filter((item) => !item.is_read)
    for (const item of items) {
      if (seenNotificationIds.has(item.uuid)) continue
      seenNotificationIds.add(item.uuid)
      const title = t('layout.notifications.readyTitle', { step: item.step_name || 'Agent' })
      const body = t('layout.notifications.openTask', {
        task: item.task_title || t('layout.notifications.details'),
      })
      const notification = new Notification(title, { body })
      notification.onclick = () => {
        window.focus()
        void router.push({
          name: 'tasks',
          query: { taskUuid: item.task_uuid, stepUuid: item.task_step_uuid },
        })
      }
    }
  } catch {
    // 通知弹窗失败不影响页面主流程
  }
}

async function seedNotificationHistory() {
  try {
    const result = await apiClient.get<{ items: TaskNotification[] }>('/notifications')
    appStore.setUnreadTaskNotifications((result.items || []).filter((item) => !item.is_read).length)
    for (const item of result.items || []) {
      seenNotificationIds.add(item.uuid)
    }
  } catch {
    // 初始化失败不影响页面使用，后续 unread 推送会再触发。
  }
  // 侧栏未读状态不依赖系统通知权限，避免等待授权时红点迟迟不显示。
  if (typeof window !== 'undefined' && 'Notification' in window && notificationPermission.value === 'default') {
    notificationPermission.value = await Notification.requestPermission()
  }
}

onMounted(() => {
  void seedNotificationHistory()
})

let trayStatusHeartbeat: ReturnType<typeof setInterval> | null = null
let removeOpenTaskListener: (() => void) | null = null
onMounted(() => {
  removeOpenTaskListener = onOpenTaskConversation(({ taskUuid, stepUuid }) => {
    void router.push({
      name: 'tasks',
      query: stepUuid ? { taskUuid, stepUuid } : { taskUuid },
    })
  })
  trayStatusHeartbeat = setInterval(() => {
    syncTrayTaskCount(appStore.cliTaskCount)
  }, 20_000)
})
onUnmounted(() => {
  removeOpenTaskListener?.()
  removeOpenTaskListener = null
  if (trayStatusHeartbeat) clearInterval(trayStatusHeartbeat)
})
</script>

<template>
  <div class="app-layout" :class="{ 'mac-desktop': isMacDesktop, 'windows-desktop': isWindowsDesktop }">
    <AppSidebar />
    <div class="main-wrapper">
      <!-- 桌面标题栏盖住内容区：侧栏收起时展开条本身就是拖动区；展开时主区域另留等高拖动条。 -->
      <div
        v-if="appStore.sidebarCollapsed"
        class="collapsed-bar"
      >
        <button
          type="button"
          class="sidebar-expand-btn"
          :title="t('layout.sidebar.expand')"
          :aria-label="t('layout.sidebar.expand')"
          @click="appStore.setSidebarCollapsed(false)"
        >
          <img
            :src="sidebarToggleIcon"
            alt=""
            class="expand-icon"
          />
        </button>
        <span class="collapsed-title">TeamBuddy</span>
      </div>
      <div
        v-else-if="isMacDesktop"
        class="mac-titlebar-spacer"
      />
      <div
        v-else-if="isWindowsDesktop"
        class="windows-titlebar-spacer"
      />

      <main
        class="main-content"
        :class="mainContentClasses"
      >
        <RouterView v-slot="{ Component, route: currentRoute }">
          <KeepAlive>
            <component
              :is="Component"
              v-if="currentRoute.meta.keepAlive"
              :key="currentRoute.name ?? currentRoute.path"
            />
          </KeepAlive>
          <component
            :is="Component"
            v-if="!currentRoute.meta.keepAlive"
            :key="routeViewKey(currentRoute)"
          />
        </RouterView>
      </main>
    </div>
  </div>
</template>

<style scoped>
.app-layout {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

.main-wrapper {
  flex: 1;
  min-width: 0;
  min-height: 0;
  height: 100vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.collapsed-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 38px;
  padding: 0 16px;
  background: transparent;
  flex-shrink: 0;
  user-select: none;
}

.mac-desktop .collapsed-bar {
  -webkit-app-region: drag;
  padding-left: 80px;
}

.windows-desktop .collapsed-bar {
  -webkit-app-region: drag;
  height: env(titlebar-area-height, 32px);
  padding-right: calc(100vw - env(titlebar-area-x, 0px) - env(titlebar-area-width, 100vw));
  border-bottom: 1px solid #e5e7eb;
  background: #f9f9f9;
}

.mac-titlebar-spacer {
  height: 38px;
  flex-shrink: 0;
  -webkit-app-region: drag;
  background: #f9f9f9;
}

.windows-titlebar-spacer {
  height: env(titlebar-area-height, 32px);
  flex-shrink: 0;
  -webkit-app-region: drag;
  border-bottom: 1px solid #e5e7eb;
  background: #f9f9f9;
}

.sidebar-expand-btn {
  -webkit-app-region: no-drag;
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

.sidebar-expand-btn:hover {
  background: #e5e7eb;
}

.expand-icon {
  display: block;
  width: 20px;
  height: 20px;
  transform: rotate(180deg);
}

.collapsed-title {
  font-size: 14px;
  font-weight: 600;
  color: #1d1d1f;
}

.main-content {
  flex: 1;
  min-width: 0;
  min-height: 0;
  height: 100%;
}

.main-content-default {
  padding: 24px;
  background: #f5f6f8;
}

.main-content-custom {
  padding: 0;
  background: transparent;
}

.main-content-scroll {
  overflow-y: auto;
}

.main-content-no-scroll {
  overflow: hidden;
}

.main-content.api-content,
.main-content.agents-content,
.main-content.knowledge-content {
  overflow: hidden;
  padding: 0;
}

.main-content.api-content,
.main-content.knowledge-content {
  background: #fff;
}
</style>
