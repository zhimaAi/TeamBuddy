<template>
  <header class="workspace-header">
    <button v-if="showBack" type="button" class="icon-button" :aria-label="t('workflows.task.detail.back')" @click="emit('back')"><LeftOutlined /></button>
    <div class="workspace-heading">
      <form v-if="editing" class="title-form" @submit.prevent="saveTitle">
        <a-input ref="titleInput" v-model:value="draftTitle" :disabled="savingTitle" :aria-label="t('layout.sidebar.renameTask')" @keydown.esc.prevent="cancelEdit" />
        <a-button type="text" html-type="submit" :loading="savingTitle" :aria-label="t('common.actions.save')"><CheckOutlined /></a-button>
        <a-button type="text" :disabled="savingTitle" :aria-label="t('common.actions.cancel')" @click="cancelEdit"><CloseOutlined /></a-button>
      </form>
      <div v-else class="title-display">
        <h1 :title="task?.title">{{ task?.title || t('workflows.task.common.conversation') }}</h1>
        <button v-if="task" type="button" class="icon-button title-edit" :aria-label="t('layout.sidebar.renameTask')" @click="editTitle"><EditOutlined /></button>
      </div>
      <a-tooltip v-if="task?.execution_mode === 'vibe_coding'" :title="t('workflows.task.detail.awaitingToolStatus')">
        <span class="status-button status-readonly" :class="`status-${status}`" tabindex="0" aria-disabled="true" :aria-label="`${statusLabel}: ${t('workflows.task.detail.awaitingToolStatus')}`">
          <span class="status-dot" aria-hidden="true" />{{ statusLabel }}
        </span>
      </a-tooltip>
      <a-dropdown v-else-if="task" :disabled="changingStatus" :trigger="['click']">
        <button type="button" class="status-button" :disabled="changingStatus" :class="`status-${status}`" :aria-label="t('workflows.task.detail.taskStatus')" aria-haspopup="menu">
          <LoadingOutlined v-if="changingStatus" spin /><span v-else class="status-dot" aria-hidden="true" />
          {{ statusLabel }}<DownOutlined />
        </button>
        <template #overlay><a-menu :selected-keys="[status]" @click="selectStatus"><a-menu-item v-for="option in statusOptions" :key="option.value">{{ option.label }}</a-menu-item></a-menu></template>
      </a-dropdown>
      <span v-if="priorityLabel" class="priority-tag" :class="`priority-${task?.priority}`">{{ priorityLabel }}</span>
    </div>
    <div v-if="task" class="header-actions">
      <TaskVibeToolActions :task="task" @changed="emit('changed')" />
      <slot name="actions" />
      <a-button v-if="status === 'pending' && !task.awaiting_message && task.execution_mode !== 'vibe_coding' && task.execution_mode !== 'cli'" size="small" type="primary" :loading="starting" @click="emit('start')">{{ t('workflows.task.detail.start') }}</a-button>
      <a-tooltip v-if="showOriginal" :title="hasRequirementUpdate ? t('workflows.task.workspace.requirementUpdated') : t('workflows.task.workspace.originalRequirement')">
        <button type="button" class="icon-button original-button" :disabled="originalLoading" :aria-label="hasRequirementUpdate ? t('workflows.task.workspace.requirementUpdated') : t('workflows.task.workspace.originalRequirement')" @click="emit('original')">
          <LoadingOutlined v-if="originalLoading" spin /><img v-else class="header-action-icon" :src="originalRequirementIcon" alt="" aria-hidden="true" /><span v-if="hasRequirementUpdate" class="requirement-dot" aria-hidden="true" />
        </button>
      </a-tooltip>
      <a-tooltip v-if="!task.created_from_conversation" :title="t('workflows.task.detail.viewDetails')"><button type="button" class="icon-button" :aria-label="t('workflows.task.detail.viewDetails')" @click="emit('details')"><img class="header-action-icon" :src="detailsIcon" alt="" aria-hidden="true" /></button></a-tooltip>
      <a-dropdown v-model:open="moreMenuOpen" :trigger="['click']"><button type="button" class="icon-button" :class="{ 'is-open': moreMenuOpen }" :aria-label="t('workflows.task.common.moreActions')" :aria-expanded="moreMenuOpen" aria-haspopup="menu"><img class="header-action-icon" :src="moreIcon" alt="" aria-hidden="true" /></button>
        <template #overlay><a-menu>
          <a-menu-item key="export" :disabled="exporting" @click="exportRecords"><LoadingOutlined v-if="exporting" spin /><DownloadOutlined v-else /> {{ t('layout.sidebar.exportMarkdown') }}</a-menu-item>
          <a-menu-item key="directory" :disabled="!directory || openingDirectory" :title="directory || t('workflows.task.common.unsetDirectory')" @click="openTaskDirectory">
            <LoadingOutlined v-if="openingDirectory" spin /><FolderOutlined v-else />
            {{ t('layout.sidebar.openWorkspaceDirectory') }}
          </a-menu-item>
          <a-menu-item key="terminal" :disabled="!directory || openingTerminal" @click="emit('terminal')"><CodeOutlined /> {{ t('workflows.task.progress.openTerminal') }}</a-menu-item>
          <a-menu-item key="delete" danger :disabled="deleting" @click="emit('delete')"><DeleteOutlined /> {{ t('workflows.task.detail.delete') }}</a-menu-item>
        </a-menu></template>
      </a-dropdown>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CheckOutlined, CloseOutlined, CodeOutlined, DeleteOutlined, DownloadOutlined, DownOutlined, EditOutlined, FolderOutlined, LeftOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import originalRequirementIcon from '@/assets/icons/task-header-original-requirement.svg'
import detailsIcon from '@/assets/icons/task-header-details.svg'
import moreIcon from '@/assets/icons/task-header-more.svg'
import { useAppI18n } from '@/i18n'
import { useConversationStore } from '@/stores/conversation'
import { openDirectory } from '@/composables/useDesktop'
import type { TaskWithDetails } from '@/types/task-detail'
import TaskVibeToolActions from './TaskVibeToolActions.vue'
import { exportConversationMarkdown } from '@/utils/conversationMarkdown'

const props = defineProps<{
  task?: TaskWithDetails
  status: string
  showBack?: boolean
  changingStatus?: boolean
  starting?: boolean
  deleting?: boolean
  openingTerminal?: boolean
  originalLoading?: boolean
  hasRequirementUpdate?: boolean
}>()
const emit = defineEmits<{ back: []; status: [value: string]; start: []; original: []; details: []; terminal: []; delete: []; renamed: [title: string]; changed: [] }>()
const { t } = useAppI18n()
const conversationStore = useConversationStore()
const exporting = ref(false)
async function exportRecords() {
  if (!props.task || exporting.value) return
  exporting.value = true
  try { await exportConversationMarkdown(props.task.uuid); message.success(t('layout.sidebar.exportSuccess')) }
  catch (error) { message.error(error instanceof Error ? error.message : t('layout.sidebar.exportFailed')) }
  finally { exporting.value = false }
}
const editing = ref(false)
const savingTitle = ref(false)
const draftTitle = ref('')
const titleInput = ref<{ focus: () => void }>()
const openingDirectory = ref(false)
const moreMenuOpen = ref(false)
const directory = computed(() => props.task?.work_dir || props.task?.task_dir || '')
const showOriginal = computed(() => props.task?.source_type === 'cloud' && Boolean(props.task.work_item_id))
const statusOptions = computed(() => [
  { value: 'pending', label: t('workflows.task.status.pending') },
  { value: 'in_progress', label: t('workflows.task.status.inProgress') },
  { value: 'blocked', label: t('workflows.task.status.blocked') },
  { value: 'done', label: t('workflows.task.status.done') },
])
const vibeBound = computed(() => props.task?.execution_bound === true)
const statusLabel = computed(() => {
  if (['pending', 'in_progress'].includes(props.status) && props.task?.execution_mode === 'vibe_coding'
      && props.task.execution_tool === 'generic' && !vibeBound.value) return t('workflows.task.detail.genericAwaitingBinding')
  if (props.status === 'in_progress' && props.task?.execution_mode === 'vibe_coding'
      && props.task.execution_tool === 'qoder_ide' && !vibeBound.value) return t('workflows.task.detail.qoderIDEAwaitingStart')
  return statusOptions.value.find(option => option.value === props.status)?.label || props.status
})
function selectStatus(event: { key: string | number }) {
  if (props.task?.execution_mode !== 'vibe_coding') emit('status', String(event.key))
}
const priorityLabel = computed(() => ['urgent', 'high', 'medium', 'low'].includes(props.task?.priority || '') ? t(`workflows.task.priority.${props.task?.priority}`) : '')
watch(() => props.task?.uuid, () => { editing.value = false; moreMenuOpen.value = false })
async function editTitle() {
  if (!props.task || savingTitle.value) return
  draftTitle.value = props.task.title
  editing.value = true
  await nextTick()
  titleInput.value?.focus()
}
function cancelEdit() { if (!savingTitle.value) editing.value = false }
async function saveTitle() {
  const task = props.task
  const title = draftTitle.value.trim()
  if (!task || savingTitle.value) return
  if (!title) { message.warning(t('workflows.task.create.titleRequired')); return }
  if (title === task.title) { editing.value = false; return }
  savingTitle.value = true
  try {
    const result = await conversationStore.renameTask(task.uuid, title)
    if (props.task?.uuid === task.uuid) { editing.value = false; emit('renamed', result.title) }
  } catch (error) { message.error(error instanceof Error ? error.message : t('workflows.task.workspace.renameFailed')) }
  finally { savingTitle.value = false }
}
async function openTaskDirectory() {
  if (!directory.value || openingDirectory.value) return
  openingDirectory.value = true
  try { await openDirectory(directory.value) }
  catch (error) { message.error(error instanceof Error ? error.message : t('workflows.task.workspace.openDirectoryFailed')) }
  finally { openingDirectory.value = false }
}
</script>

<style scoped>
.workspace-header { display: flex; min-height: 44px; flex: 0 0 auto; align-items: center; gap: 8px; padding: 8px 24px; border-bottom: 1px solid #f0f0f0; background: #fff; }
.workspace-heading { display: flex; min-width: 0; flex: 1; align-items: center; gap: 8px; }
.title-display { display: flex; min-width: 0; align-items: center; }
h1 { overflow: hidden; margin: 0; color: #262626; font-size: 20px; font-weight: 600; line-height: 28px; text-overflow: ellipsis; white-space: nowrap; }
.icon-button { display: inline-flex; width: 28px; height: 28px; flex: 0 0 auto; align-items: center; justify-content: center; padding: 0; border: 0; border-radius: 6px; color: #8c8c8c; background: transparent; cursor: pointer; font-size: 16px; }
.icon-button:hover:not(:disabled) { color: #262626; background: #f0f0f0; }
.icon-button:disabled { color: #bfbfbf; cursor: not-allowed; }
button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.title-edit { opacity: 0; }
.title-display:hover .title-edit, .title-display:focus-within .title-edit { opacity: 1; }
.title-form { display: flex; min-width: 0; flex: 1; align-items: center; }
.status-button { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 4px; padding: 2px 4px; border: 0; border-radius: 6px; color: #8c8c8c; background: transparent; cursor: pointer; font-size: 14px; line-height: 22px; }
.status-button:hover { background: #f0f0f0; }
.status-button:disabled { cursor: wait; }
.status-readonly { cursor: default; }
.status-readonly:hover { background: transparent; }
.status-readonly:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.status-dot { width: 6px; height: 6px; border-radius: 50%; background: #8c8c8c; }
.status-in_progress .status-dot { background: #ed744a; }
.status-done .status-dot { background: #16a34a; }
.status-blocked .status-dot { background: #fb363f; }
.priority-tag { flex: 0 0 auto; padding: 0 8px; border: 1px solid #d9d9d9; border-radius: 999px; color: #595959; font-size: 12px; line-height: 20px; }
.priority-high, .priority-urgent { color: #fb363f; border-color: #fb363f; }
.header-actions { display: flex; flex: 0 0 auto; align-items: center; gap: 4px; }
.header-action-icon { display: block; flex: 0 0 auto; }
.header-actions .icon-button.is-open { background: #f0f0f0; }
.header-actions .icon-button:disabled .header-action-icon { opacity: 0.4; }
.original-button { position: relative; }
.requirement-dot { position: absolute; top: 5px; right: 5px; width: 6px; height: 6px; border-radius: 50%; background: #fb363f; }
@media (max-width: 640px) { .workspace-header { flex-wrap: wrap; padding: 8px 16px; } .priority-tag { display: none; } .header-actions { margin-left: auto; } }
@media (hover: none) { .title-edit { opacity: 1; } }
</style>
