<script setup lang="ts">
import { computed, ref } from 'vue'
import { message } from 'ant-design-vue'
import apiClient from '@/api/client'
import MenuIcon from '@/components/MenuIcon.vue'
import { useAppStore } from '@/stores/app'
import type { MenuConfigItem } from '@/stores/app'
import { useAppI18n } from '@/i18n'

/** Menu items managed by the tool center (one-to-one with sidebar keys; order is the sidebar display order) */
interface ToolItem {
  key: string
  label: string
  tag: string
  icon: string
  description: string
  detail: string
}

const { t } = useAppI18n()

const TOOL_ITEMS = computed<ToolItem[]>(() => [
  {
    key: 'workflows',
    label: t('tools.items.workflows.label'),
    tag: 'issue',
    icon: 'dashboard',
    description: t('tools.items.workflows.description'),
    detail: t('tools.items.workflows.detail'),
  },
  {
    key: 'tasks',
    label: t('tools.items.tasks.label'),
    tag: 'task',
    icon: 'task',
    description: t('tools.items.tasks.description'),
    detail: t('tools.items.tasks.detail'),
  },
  {
    key: 'agents',
    label: t('tools.items.agents.label'),
    tag: 'agent',
    icon: 'agent',
    description: t('tools.items.agents.description'),
    detail: t('tools.items.agents.detail'),
  },
  {
    key: 'projects',
    label: t('tools.items.projects.label'),
    tag: 'project',
    icon: 'project',
    description: t('tools.items.projects.description'),
    detail: t('tools.items.projects.detail'),
  },
  {
    key: 'commands',
    label: t('tools.items.commands.label'),
    tag: 'command',
    icon: 'command',
    description: t('tools.items.commands.description'),
    detail: t('tools.items.commands.detail'),
  },
  {
    key: 'knowledge',
    label: t('tools.items.knowledge.label'),
    tag: 'knowledge',
    icon: 'book',
    description: t('tools.items.knowledge.description'),
    detail: t('tools.items.knowledge.detail'),
  },
  {
    key: 'remote-channels',
    label: t('tools.items.remoteChannels.label'),
    tag: 'remote',
    icon: 'remote',
    description: t('tools.items.remoteChannels.description'),
    detail: t('tools.items.remoteChannels.detail'),
  },
  {
    key: 'skills',
    label: t('tools.items.skills.label'),
    tag: 'skill',
    icon: 'skills',
    description: t('tools.items.skills.description'),
    detail: t('tools.items.skills.detail'),
  },
  {
    key: 'apis',
    label: t('tools.items.apis.label'),
    tag: 'api',
    icon: 'api',
    description: t('tools.items.apis.description'),
    detail: t('tools.items.apis.detail'),
  },
  {
    key: 'settings',
    label: t('tools.items.settings.label'),
    tag: 'settings',
    icon: 'settings',
    description: t('tools.items.settings.description'),
    detail: t('tools.items.settings.detail'),
  },
])

/** Count of fixed, non-toggleable menus (tool center only) */
const FIXED_MENU_COUNT = 1

const appStore = useAppStore()
const selectedKey = ref('commands')
const draggedKey = ref<string | null>(null)
const dragOverKey = ref<string | null>(null)
const dragStartOrder = ref<string[]>([])
let dropHandled = false

const selected = computed(() => TOOL_ITEMS.value.find((i) => i.key === selectedKey.value) ?? TOOL_ITEMS.value[0])

const orderedItems = computed(() => {
  const itemMap = new Map(TOOL_ITEMS.value.map((item) => [item.key, item]))
  return appStore.menuOrder
    .map((key) => itemMap.get(key))
    .filter((item): item is ToolItem => item !== undefined)
})

const enabledMap = computed(() => {
  const map: Record<string, boolean> = {}
  for (const item of TOOL_ITEMS.value) {
    map[item.key] = appStore.isMenuVisible(item.key)
  }
  return map
})

/** Visible = enabled manageable items + fixed menus */
const visibleCount = computed(
  () => TOOL_ITEMS.value.filter((i) => appStore.isMenuVisible(i.key)).length + FIXED_MENU_COUNT,
)
/** Hidden = disabled manageable items */
const hiddenCount = computed(() => TOOL_ITEMS.value.filter((i) => !appStore.isMenuVisible(i.key)).length)

function buildMenuPayload(
  enabled: Record<string, boolean>,
  order: string[] = appStore.menuOrder,
): MenuConfigItem[] {
  return order.map((key, sortOrder) => ({
    key,
    enabled: enabled[key] !== false,
    sort_order: sortOrder,
  }))
}

async function saveMenuConfig(enabled: Record<string, boolean>, order = appStore.menuOrder) {
  await apiClient.put('/tools/menu', { items: buildMenuPayload(enabled, order) })
}

async function toggleItem(item: ToolItem, checked: boolean) {
  // Record the pre-switch state for rollback on failure
  const prev = { ...enabledMap.value }
  // Take effect locally first
  const next = { ...prev, [item.key]: checked }
  appStore.setMenuEnabled(next)
  try {
    await saveMenuConfig(next)
  } catch {
    message.error(t('tools.saveVisibilityFailed'))
    appStore.setMenuEnabled(prev)
  }
}

function startDrag(event: DragEvent, item: ToolItem) {
  draggedKey.value = item.key
  dragOverKey.value = item.key
  dragStartOrder.value = [...appStore.menuOrder]
  dropHandled = false
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', item.key)
  }
}

/** Adjust the Pinia order immediately when dragging over a target item, so the left menu gives synchronous feedback. */
function moveDraggedItem(targetKey: string) {
  const sourceKey = draggedKey.value
  dragOverKey.value = targetKey
  if (!sourceKey || sourceKey === targetKey) return

  const nextOrder = [...appStore.menuOrder]
  const sourceIndex = nextOrder.indexOf(sourceKey)
  const targetIndex = nextOrder.indexOf(targetKey)
  if (sourceIndex < 0 || targetIndex < 0) return
  nextOrder.splice(sourceIndex, 1)
  nextOrder.splice(targetIndex, 0, sourceKey)
  appStore.setMenuOrder(nextOrder)
}

async function finishDrag() {
  if (!draggedKey.value) return
  const previousOrder = [...dragStartOrder.value]
  const nextOrder = [...appStore.menuOrder]
  dropHandled = true
  draggedKey.value = null
  dragOverKey.value = null
  try {
    await saveMenuConfig(enabledMap.value, nextOrder)
  } catch {
    appStore.setMenuOrder(previousOrder)
    message.error(t('tools.saveOrderFailed'))
  }
}

function cancelDrag() {
  if (!dropHandled && dragStartOrder.value.length > 0) {
    appStore.setMenuOrder(dragStartOrder.value)
  }
  draggedKey.value = null
  dragOverKey.value = null
  dragStartOrder.value = []
}
</script>

<template>
  <div class="tools-center">
    <!-- Header -->
    <div class="tc-header">
      <div class="tc-header-left">
        <div class="tc-title">{{ t('tools.title') }}</div>
        <div class="tc-subtitle">{{ t('tools.subtitle') }}</div>
      </div>
      <div class="tc-header-right">
        <span class="tc-stat">{{ t('tools.visible', { count: visibleCount }) }}</span>
        <span class="tc-sep" />
        <span class="tc-stat">{{ t('tools.hidden', { count: hiddenCount }) }}</span>
      </div>
    </div>

    <div class="tc-body">
      <!-- Left tool card list -->
      <div class="tc-list">
        <template v-for="(item, idx) in orderedItems" :key="item.key">
          <div v-if="idx > 0" class="tc-divider" />
          <div
            class="tc-card"
            :class="{
              selected: selectedKey === item.key,
              dragging: draggedKey === item.key,
              'drag-over': dragOverKey === item.key && draggedKey !== item.key,
            }"
            @click="selectedKey = item.key"
            @dragover.prevent="moveDraggedItem(item.key)"
            @drop.prevent="finishDrag"
          >
            <div class="tc-card-left">
              <span
                class="tc-drag-handle"
                draggable="true"
                :title="t('tools.drag')"
                :aria-label="t('tools.drag')"
                @click.stop
                @dragstart.stop="startDrag($event, item)"
                @dragend="cancelDrag"
              >
                <i v-for="dot in 6" :key="dot" />
              </span>
              <div class="tc-icon"><MenuIcon :name="item.icon" /></div>
              <div class="tc-info">
                <div class="tc-name-row">
                  <span class="tc-name">{{ item.label }}</span>
                  <span class="tc-tag">{{ item.tag }}</span>
                </div>
                <div class="tc-desc">{{ item.description }}</div>
              </div>
            </div>
            <a-switch
              class="tc-switch"
              :checked="enabledMap[item.key]"
              @click.stop
              @change="(checked: boolean) => toggleItem(item, checked)"
            />
          </div>
        </template>
      </div>

      <!-- Right feature description panel -->
      <div class="tc-detail">
        <div class="tc-detail-head">
          <div class="tc-detail-icon"><MenuIcon :name="selected.icon" /></div>
          <div class="tc-detail-id">
            <div class="tc-detail-name">{{ selected.label }}</div>
            <div class="tc-detail-tag">{{ selected.tag }}</div>
          </div>
        </div>
        <div class="tc-detail-section">
          <div class="tc-detail-title">{{ t('tools.details') }}</div>
          <div class="tc-detail-text">{{ selected.detail }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tools-center {
  background: #fff;
  border-radius: 8px;
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* ===== Header ===== */
.tc-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 24px;
  border-bottom: 1px solid #d9d9d9;
}

.tc-header-left {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 8px;
}

.tc-title {
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
  color: #262626;
}

.tc-subtitle {
  font-size: 14px;
  line-height: 22px;
  color: #8c8c8c;
}

.tc-header-right {
  display: flex;
  align-items: center;
  gap: 12px;
}

.tc-stat {
  font-size: 14px;
  color: #595959;
}

.tc-sep {
  width: 1px;
  height: 12px;
  background: #f0f0f0;
}

/* ===== Body ===== */
.tc-body {
  flex: 1;
  display: flex;
  min-height: 0;
}

/* Left list */
.tc-list {
  flex: 1;
  min-width: 0;
  padding: 25px 0 25px 25px;
  overflow-y: auto;
}

.tc-divider {
  height: 1px;
  background: #f0f0f0;
  margin: 0 16px;
}

.tc-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-radius: 12px;
  cursor: pointer;
  transition: background 0.2s;
}

.tc-card:hover {
  background: #f5f9ff;
}

.tc-card.selected {
  background: #e5efff;
}

.tc-card.dragging {
  opacity: 0.55;
}

.tc-card.drag-over {
  box-shadow: inset 0 2px 0 #3157e2;
}

.tc-card-left {
  display: flex;
  align-items: center;
  gap: 16px;
  min-width: 0;
}

.tc-drag-handle {
  width: 14px;
  min-width: 14px;
  display: grid;
  grid-template-columns: repeat(2, 3px);
  grid-auto-rows: 3px;
  gap: 3px;
  padding: 8px 2px;
  color: #b8c0cc;
  cursor: grab;
}

.tc-drag-handle:active {
  cursor: grabbing;
}

.tc-drag-handle i {
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: currentColor;
}

.tc-icon {
  width: 42px;
  height: 42px;
  min-width: 42px;
  border-radius: 12px;
  background: #d1e3ff;
  color: #3157e2;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.tc-icon svg {
  width: 18px;
  height: 18px;
}

.tc-info {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.tc-name-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.tc-name {
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  color: #262626;
  white-space: nowrap;
}

.tc-tag {
  padding: 1px 8px;
  background: #f1f5fa;
  border-radius: 6px;
  font-size: 12px;
  line-height: 20px;
  color: #3a4559;
  white-space: nowrap;
}

.tc-desc {
  font-size: 14px;
  line-height: 22px;
  color: #8c8c8c;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tc-switch {
  flex-shrink: 0;
  margin-left: 24px;
}

/* Right detail panel */
.tc-detail {
  width: 379px;
  min-width: 379px;
  border-left: 1px solid #d9d9d9;
  padding: 24px;
  overflow-y: auto;
}

.tc-detail-head {
  display: flex;
  align-items: center;
  gap: 16px;
}

.tc-detail-icon {
  width: 42px;
  height: 42px;
  min-width: 42px;
  border-radius: 12px;
  background: #e6f0ff;
  color: #3157e2;
  display: flex;
  align-items: center;
  justify-content: center;
}

.tc-detail-icon svg {
  width: 20px;
  height: 20px;
}

.tc-detail-id {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.tc-detail-name {
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  color: #262626;
}

.tc-detail-tag {
  font-size: 12px;
  line-height: 20px;
  color: #7a8699;
}

.tc-detail-section {
  margin-top: 40px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tc-detail-title {
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
  color: #1b2a42;
}

.tc-detail-text {
  font-size: 14px;
  line-height: 22px;
  color: #595959;
}
</style>
