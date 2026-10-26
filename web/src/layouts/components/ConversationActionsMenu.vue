<template>
  <a-menu class="conversation-actions-menu" :selectable="false" @click="handleAction">
    <a-menu-item key="read" :disabled="pending">
      <span class="conversation-action-content">
        <span class="conversation-action-icon"><img :src="readIcon" alt="" /></span>
        <span>{{ t(unread ? 'workflows.task.common.markRead' : 'workflows.task.common.markUnread') }}</span>
      </span>
    </a-menu-item>
    <a-menu-item key="archive" :disabled="pending">
      <span class="conversation-action-content">
        <span class="conversation-action-icon"><img :src="archiveIcon" alt="" /></span>
        <span>{{ t(archived ? 'layout.sidebar.unarchive' : 'workflows.task.common.archive') }}</span>
      </span>
    </a-menu-item>
    <a-menu-item key="rename" :disabled="pending">
      <span class="conversation-action-content">
        <span class="conversation-action-icon"><img :src="renameIcon" alt="" /></span>
        <span>{{ t('layout.sidebar.renameTask') }}</span>
      </span>
    </a-menu-item>
    <a-menu-item key="delete" :disabled="pending">
      <span class="conversation-action-content">
        <span class="conversation-action-icon"><img :src="deleteIcon" alt="" /></span>
        <span>{{ t('common.actions.delete') }}</span>
      </span>
    </a-menu-item>
  </a-menu>
</template>

<script setup lang="ts">
import { useAppI18n } from '@/i18n'
import readIcon from '@/assets/icons/conversation-action-read.svg'
import archiveIcon from '@/assets/icons/conversation-action-archive.svg'
import renameIcon from '@/assets/icons/conversation-action-rename.svg'
import deleteIcon from '@/assets/icons/conversation-action-delete.svg'

defineProps<{ unread: boolean; archived?: boolean; pending?: boolean }>()
const emit = defineEmits<{ read: []; archive: []; rename: []; delete: [] }>()
const { t } = useAppI18n()

function handleAction({ key }: { key: string | number }) {
  switch (key) {
    case 'read': emit('read'); break
    case 'archive': emit('archive'); break
    case 'rename': emit('rename'); break
    case 'delete': emit('delete'); break
  }
}
</script>

<style scoped>
.conversation-actions-menu {
  display: flex;
  width: max-content;
  min-width: 160px;
  box-sizing: border-box;
  flex-direction: column;
  gap: 2px;
  padding: 2px;
  border-radius: 6px;
  background: #fff;
  box-shadow: 0 2px 4px -1px #0000001f, 0 4px 5px #00000014, 0 1px 10px #0000000d;
}

.conversation-actions-menu :deep(.ant-dropdown-menu-item),
.conversation-actions-menu :deep(.ant-menu-item) {
  height: 32px;
  margin: 0;
  padding: 5px 16px;
  border-radius: 6px;
  color: #262626;
  font-size: 14px;
  font-weight: 400;
  line-height: 22px;
}

.conversation-action-content {
  display: flex;
  align-items: center;
  gap: 8px;
  white-space: nowrap;
  color: #262626;
}

.conversation-action-icon {
  display: inline-flex;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  align-items: center;
  justify-content: center;
}

.conversation-action-icon img {
  display: block;
  max-width: none;
}

.conversation-actions-menu :deep(.ant-dropdown-menu-item-disabled),
.conversation-actions-menu :deep(.ant-menu-item-disabled) {
  color: #bfbfbf;
}

.conversation-actions-menu :deep(.ant-dropdown-menu-item-disabled) .conversation-action-content,
.conversation-actions-menu :deep(.ant-menu-item-disabled) .conversation-action-content {
  color: #bfbfbf;
}

.conversation-actions-menu :deep(.ant-dropdown-menu-item-disabled) .conversation-action-icon,
.conversation-actions-menu :deep(.ant-menu-item-disabled) .conversation-action-icon {
  opacity: 0.4;
}
</style>
