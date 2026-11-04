<template>
  <a-space-compact
    class="message-send-button"
    :class="{ 'is-send-disabled': disabled }"
    size="small"
  >
    <a-button
      type="primary"
      :disabled="disabled"
      :loading="loading"
      :title="`${label} (${shortcutLabel})`"
      @click="emit('submit')"
    >
      {{ label }}
    </a-button>
    <a-dropdown :trigger="['click']" placement="topRight">
      <a-button
        type="primary"
        class="shortcut-trigger"
        :title="t('workflows.task.progress.sendShortcut')"
        :aria-label="t('workflows.task.progress.sendShortcut')"
      >
        <DownOutlined aria-hidden="true" />
      </a-button>
      <template #overlay>
        <a-menu
          selectable
          :selected-keys="[preferences.sendShortcut]"
          @click="handleShortcutClick"
        >
          <a-menu-item key="enter">{{ t('workflows.task.progress.sendWithEnter') }}</a-menu-item>
          <a-menu-item key="ctrl+enter">{{ t('workflows.task.progress.sendWithCtrlEnter') }}</a-menu-item>
        </a-menu>
      </template>
    </a-dropdown>
  </a-space-compact>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { DownOutlined } from '@ant-design/icons-vue'
import { useComposerPreferencesStore } from '@/stores/composer-preferences'

defineProps<{ label: string; disabled?: boolean; loading?: boolean }>()
const emit = defineEmits<{ submit: [] }>()
const { t } = useI18n()
const preferences = useComposerPreferencesStore()
const shortcutLabel = computed(() => preferences.sendShortcut === 'enter' ? 'Enter' : 'Ctrl+Enter')

function handleShortcutClick({ key }: { key: string | number }) {
  preferences.setSendShortcut(key)
}
</script>

<style scoped>
.message-send-button {
  flex: 0 0 auto;
}

.message-send-button :deep(.ant-btn-primary) {
  color: #ffffff;
  background: #262626;
  border-color: #262626;
  box-shadow: none;
}

.message-send-button :deep(.ant-btn-primary:not(:disabled):hover) {
  background: #434343;
  border-color: #434343;
}

.message-send-button :deep(.ant-btn-primary:not(:disabled):active) {
  background: #141414;
  border-color: #141414;
}

.message-send-button :deep(.ant-btn-primary:disabled) {
  color: #aeb4be;
  background: #eceef2;
  border-color: #eceef2;
}

.message-send-button.is-send-disabled :deep(.ant-btn-primary.shortcut-trigger),
.message-send-button.is-send-disabled :deep(.ant-btn-primary.shortcut-trigger:hover),
.message-send-button.is-send-disabled :deep(.ant-btn-primary.shortcut-trigger:active) {
  color: #aeb4be;
  background: #eceef2;
  border-color: #eceef2;
}
</style>
