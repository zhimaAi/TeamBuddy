<template>
  <a-popover
    v-model:open="open"
    placement="rightTop"
    :trigger="['hover', 'focus']"
    :mouse-enter-delay="0.3"
    :arrow="false"
    :overlay-inner-style="overlayStyle"
  >
    <template #content>
      <div class="conversation-preview">
        <div class="conversation-preview__title">{{ conversation.title }}</div>
        <div class="conversation-preview__status" :aria-label="statusLabel">
          <span class="conversation-preview__icon" role="img" :aria-label="statusLabel">
            <img
              v-if="statusIcon"
              :src="statusIcon"
              :class="{ 'conversation-preview__running-icon': state === 'running' && open }"
              alt=""
              aria-hidden="true"
            >
            <PauseCircleOutlined v-else-if="state === 'stopped'" aria-hidden="true" />
            <ClockCircleOutlined v-else aria-hidden="true" />
          </span>
          <span class="conversation-preview__summary">{{ summary || statusLabel }}</span>
        </div>
      </div>
    </template>
    <div
      class="conversation-preview-trigger"
      @click.capture="open = false"
      @focusin="open = true"
      @focusout="open = false"
      @keydown.esc="open = false"
      @keydown.enter="open = false"
      @keydown.space="open = false"
    >
      <slot />
    </div>
  </a-popover>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ClockCircleOutlined, PauseCircleOutlined } from '@ant-design/icons-vue'
import { useAppI18n } from '@/i18n'
import type { TaskConversation } from '@/stores/conversation'
import runningIcon from '@/assets/icons/conversation-status-running.svg'
import successIcon from '@/assets/icons/conversation-status-success.svg'
import errorIcon from '@/assets/icons/conversation-status-error.svg'

const props = defineProps<{ conversation: TaskConversation }>()
const { t } = useAppI18n()
const route = useRoute()
const open = ref(false)
const overlayStyle = {
  padding: '12px',
  borderRadius: '6px',
  background: '#fff',
  boxShadow: '0 1px 10px rgba(0,0,0,.05), 0 4px 5px rgba(0,0,0,.08), 0 2px 4px -1px rgba(0,0,0,.12)',
}

const state = computed(() => {
  const { running, latest } = props.conversation
  const status = latest.status || latest.terminal_status
  if (running) return 'running'
  if (latest.task_status === 'blocked' || status === 'failed' || status === 'interrupted') return 'error'
  if (status === 'stopped') return 'stopped'
  if (status === 'success' || latest.task_status === 'done') return 'success'
  return 'pending'
})
const statusIcon = computed(() => ({ running: runningIcon, success: successIcon, error: errorIcon, stopped: '', pending: '' })[state.value])
const statusLabel = computed(() => t(`layout.sidebar.conversationState.${state.value}`))
const summary = computed(() => (props.conversation.latest.summary || '').replace(/\s+/g, ' ').trim())

watch(() => route.fullPath, () => { open.value = false })
</script>

<style scoped>
.conversation-preview-trigger {
  min-width: 0;
}

.conversation-preview {
  width: 206px;
  max-width: calc(100vw - 48px);
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.conversation-preview__title {
  color: #262626;
  font-size: 14px;
  font-weight: 500;
  line-height: 22px;
  overflow-wrap: anywhere;
}

.conversation-preview__status {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.conversation-preview__icon {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 16px;
  height: 16px;
  font-size: 14px;
}

.conversation-preview__running-icon {
  animation: conversation-preview-spin 1s linear infinite;
}

@keyframes conversation-preview-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .conversation-preview__running-icon {
    animation: none;
  }
}

.conversation-preview__summary {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
