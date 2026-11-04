<template>
  <div class="vibe-coding-conversation-notice">
    <div class="notice-bar">
      <span class="notice-icon">
        <img
          :src="vibeCodingLogo"
          alt=""
          aria-hidden="true"
        />
      </span>
      <div class="notice-body">
        <p class="notice-title">
          <span>{{ t('workflows.task.create.vibeCodingMode') }}</span>
          <span
            class="notice-sep"
            aria-hidden="true"
          ></span>
          <strong>{{
            t('workflows.task.detail.vibeCodingNoticeConversation', { tool: toolName })
          }}</strong>
        </p>
        <p class="notice-desc">
          {{ t(noticeKey, { tool: toolName }) }}
        </p>
      </div>
      <button
        v-if="showOpenButton"
        type="button"
        class="notice-open-button"
        :disabled="opening"
        :aria-label="buttonLabel"
        @click="emit('open')"
      >
        <CopyOutlined v-if="toolId === 'generic'" aria-hidden="true" />
        <ExportOutlined v-else aria-hidden="true" />
        <span>{{ buttonLabel }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CopyOutlined, ExportOutlined } from '@ant-design/icons-vue'
import vibeCodingLogo from '@/assets/icons/vibe-coding-logo-blue.svg'
import { useAppI18n, type MessageKey } from '@/i18n'

const props = withDefaults(
  defineProps<{
    toolName: string
    /** 执行工具枚举，不依赖展示名。 */
    toolId?: string
    showOpenButton?: boolean
    opening?: boolean
    openLabel?: string
  }>(),
  {
    toolId: '',
    showOpenButton: false,
    opening: false,
    openLabel: '',
  },
)
const emit = defineEmits<{ open: [] }>()

const { t } = useAppI18n()

const noticeKey = computed<MessageKey>(() => {
  if (props.toolId === 'generic') return 'workflows.task.detail.genericNotice'
  if (props.toolId === 'qoder_ide') return 'workflows.task.detail.qoderIDENotice'
  return 'workflows.task.detail.vibeCodingNoticeDescription'
})

const buttonLabel = computed(() => {
  if (props.toolId === 'generic') return t('workflows.task.detail.copyCodexPrompt')
  return props.openLabel || t('workflows.task.detail.openInCodex')
})
</script>

<style scoped>
.vibe-coding-conversation-notice {
  flex: 0 0 auto;
  padding: 12px 24px;
  background: #fff;
}

.notice-bar {
  display: flex;
  align-items: center;
  gap: 15px;
  border: 1px solid #c5d4f1;
  border-radius: 12px;
  padding: 12px 16px;
}

.notice-icon {
  display: inline-flex;
  width: 36px;
  height: 36px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 10px;
  background: #e5efff;
}

.notice-icon img {
  display: block;
  width: 16px;
  height: 16px;
}

.notice-body {
  min-width: 0;
}

.notice-title {
  margin: 0;
  color: #3157e2;
  font-size: 14px;
  font-weight: 400;
  line-height: 20px;
}

.notice-title strong {
  font-weight: 600;
}

.notice-sep::after {
  content: '·';
  padding: 0 4px;
}

.notice-desc {
  margin: 2px 0 0;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 18px;
}

.notice-open-button {
  display: inline-flex;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  gap: 4px;
  margin-left: auto;
  border: 1px solid #c5d4f1;
  border-radius: 8px;
  padding: 0 12px;
  background: #fff;
  color: #3157e2;
  font-size: 13px;
  line-height: 20px;
  cursor: pointer;
  transition:
    border-color 180ms ease,
    background 180ms ease;
}

.notice-open-button:hover:not(:disabled) {
  border-color: #3157e2;
  background: #e5efff;
}

.notice-open-button:focus-visible {
  outline: 2px solid rgba(49, 87, 226, 0.2);
  outline-offset: 2px;
}

.notice-open-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

@media (max-width: 720px) {
  .vibe-coding-conversation-notice {
    padding: 10px 16px;
  }

  .notice-bar {
    gap: 12px;
    padding: 10px 12px;
  }
}
</style>
