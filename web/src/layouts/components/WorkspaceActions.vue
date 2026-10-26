<template>
  <span class="workspace-actions" :class="{ 'is-open': menuOpen }" @click.stop>
    <a-dropdown v-model:open="menuOpen" :trigger="['click']">
      <button type="button" class="workspace-menu" :disabled="pending || confirmationOpen" :aria-expanded="menuOpen" aria-haspopup="menu" :aria-label="t('layout.sidebar.workspaceActions', { name: group.displayName })">
        <SidebarMenuIcon name="conversationMore" :dark="dark" />
      </button>
      <template #overlay>
        <a-menu>
          <a-menu-item key="open" :disabled="pending || !group.workDir" @click="openWorkspaceDirectory">
            <FolderOpenOutlined /> {{ t('layout.sidebar.openWorkspaceDirectory') }}
          </a-menu-item>
          <a-menu-item key="rename" :disabled="pending || !group.workDir" @click="openRename">
            <EditOutlined /> {{ t('layout.sidebar.renameWorkspace') }}
          </a-menu-item>
          <a-menu-divider />
          <a-menu-item key="pin" :disabled="pending || !group.workDir" @click="togglePin">
            <PushpinOutlined /> {{ t(group.pinnedAt ? 'layout.sidebar.unpinWorkspace' : 'layout.sidebar.pinWorkspace') }}
          </a-menu-item>
          <a-menu-item key="delete" danger :disabled="pending || !group.workDir" @click="confirmDelete">
            <DeleteOutlined /> {{ t('common.actions.delete') }}
          </a-menu-item>
        </a-menu>
      </template>
    </a-dropdown>
    <a-modal
      :open="renaming"
      :title="t('layout.sidebar.renameWorkspace')"
      :ok-text="t('common.actions.save')"
      :cancel-text="t('common.actions.cancel')"
      :confirm-loading="pending"
      :closable="!pending"
      :mask-closable="!pending"
      :keyboard="!pending"
      :cancel-button-props="{ disabled: pending }"
      @ok="saveName"
      @cancel="renaming = false"
    >
      <a-form layout="vertical">
        <a-form-item :label="t('layout.sidebar.workspaceName')" :validate-status="renameError ? 'error' : undefined" :help="renameError || t('layout.sidebar.workspaceNameHint')">
          <a-input v-model:value="name" autofocus :disabled="pending" @press-enter="saveName" />
        </a-form-item>
      </a-form>
    </a-modal>
  </span>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'ant-design-vue'
import { FolderOpenOutlined, EditOutlined, PushpinOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import SidebarMenuIcon from './SidebarMenuIcon.vue'
import { useAppI18n } from '@/i18n'
import { useConversationStore, type ConversationGroup } from '@/stores/conversation'
import { isDesktopRuntime, openDirectory } from '@/composables/useDesktop'

const props = defineProps<{ group: ConversationGroup; dark?: boolean }>()
const store = useConversationStore()
const { t } = useAppI18n()
const route = useRoute()
const router = useRouter()
const pending = ref(false)
const menuOpen = ref(false)
const confirmationOpen = ref(false)
const renaming = ref(false)
const name = ref('')
const renameError = ref('')
let renameWorkDir = ''

async function openWorkspaceDirectory() {
  if (pending.value) return
  const workDir = props.group.workDir
  pending.value = true
  try {
    if (isDesktopRuntime()) await openDirectory(workDir)
    else {
      await navigator.clipboard.writeText(workDir)
      message.info(t('layout.sidebar.workspacePathCopied'))
    }
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed'))
  } finally { pending.value = false }
}

function openRename() {
  renameWorkDir = props.group.workDir
  name.value = props.group.displayName
  renameError.value = ''
  renaming.value = true
}

async function saveName() {
  if (pending.value) return
  const value = name.value.trim()
  if (/[\r\n]/.test(value) || Array.from(value).length > 200) {
    renameError.value = t('layout.sidebar.workspaceNameInvalid')
    return
  }
  pending.value = true
  try {
    await store.updateWorkspace(renameWorkDir, { display_name: value })
    renaming.value = false
    message.success(t('layout.sidebar.workspaceSaved'))
  } catch (err) {
    renameError.value = err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed')
  } finally { pending.value = false }
}

async function togglePin() {
  if (pending.value) return
  const group = props.group
  pending.value = true
  try {
    await store.updateWorkspace(group.workDir, { pinned: !group.pinnedAt })
    message.success(t('layout.sidebar.workspaceSaved'))
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed'))
  } finally { pending.value = false }
}

async function confirmDelete() {
  if (pending.value || confirmationOpen.value) return
  const { workDir, displayName } = props.group
  pending.value = true
  try {
    const preview = await store.previewWorkspaceDeletion(workDir)
    const taskUuids = preview.items.map((item) => item.uuid)
    const archived = preview.items.filter((item) => item.is_archived).length
    // 确认期间固定目录及预览 UUID，避免导航或新增任务改变删除范围。
    confirmationOpen.value = true
    Modal.confirm({
      afterClose: () => { confirmationOpen.value = false },
      title: t('layout.sidebar.deleteWorkspaceConfirm', { name: displayName }),
      content: taskUuids.length
        ? t('layout.sidebar.deleteWorkspaceWarning', { path: workDir, count: taskUuids.length, archived })
        : t('layout.sidebar.deleteEmptyWorkspaceWarning', { path: workDir }),
      okText: t('common.actions.delete'),
      okType: 'danger',
      cancelText: t('common.actions.cancel'),
      onOk: async () => {
        if (pending.value) return Promise.reject()
        pending.value = true
        try {
          const result = await store.deleteWorkspaceTasks(workDir, taskUuids)
          if (!result.warning) message.success(taskUuids.length
            ? t('layout.sidebar.workspaceTasksDeleted', { count: result.deleted_task_uuids.length })
            : t('layout.sidebar.workspaceDeleted'))
          if (result.deleted_task_uuids.includes(String(route.query.taskUuid || ''))) {
            await router.replace({ name: 'tasks-new' }).catch(() => {})
          }
        } catch (err) {
          message.error(err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed'))
          // 关闭失败的确认，下次操作重新预览，避免沿用过期范围。
        } finally { pending.value = false }
      },
    })
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed'))
  } finally { pending.value = false }
}
</script>

<style scoped>
.workspace-actions { display: inline-flex; }
.workspace-menu {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--sidebar-text);
  cursor: pointer;
}
.workspace-menu:hover { background: var(--sidebar-active); }
.workspace-menu:focus-visible { outline: 2px solid var(--ant-color-primary, #3157e2); }
.workspace-menu:disabled { opacity: 0.5; cursor: default; }
</style>
