<template>
  <div class="vibe-coding-conversation-notice">
    <div class="notice-bar" :class="{ 'is-manual': manual }">
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
        v-if="showOpenButton || manual"
        type="button"
        class="notice-open-button"
        :disabled="opening"
        :aria-label="buttonLabel"
        @click="handleAction"
      >
        <CopyOutlined v-if="toolId === 'generic'" aria-hidden="true" />
        <ExportOutlined v-else aria-hidden="true" />
        <span>{{ buttonLabel }}</span>
      </button>
    </div>

    <div
      v-if="manual"
      class="manual-prompt"
    >
      <div class="manual-prompt__head">
        <span class="manual-prompt__label">
          {{ t('workflows.task.detail.manualPromptLabel') }}
        </span>
        <div class="manual-prompt__actions">
          <button
            v-if="promptError"
            type="button"
            class="manual-prompt__copy"
            @click="retryPrompt"
          >
            <ReloadOutlined aria-hidden="true" />
            <span>{{ t('workflows.task.detail.manualRetry') }}</span>
          </button>
          <button
            v-if="aibridgeAvailable"
            type="button"
            class="manual-prompt__copy is-primary"
            :disabled="!prompt || dispatching || promptLoading"
            @click="dispatchPrompt"
          >
            <span>{{ t('workflows.task.detail.manualDispatch') }}</span>
          </button>
          <button
            type="button"
            class="manual-prompt__copy"
            :disabled="!prompt || promptLoading"
            @click="copyPrompt"
          >
            <CopyOutlined aria-hidden="true" />
            <span>{{ t('workflows.task.detail.manualCopyPrompt') }}</span>
          </button>
        </div>
      </div>
      <pre
        class="manual-prompt__code"
        :class="{ 'is-error': !!promptError }"
      >{{ promptText }}</pre>
    </div>

    <TaskVibeManualPanel
      v-model:open="panelOpen"
      :task-uuid="taskUuid"
      :tool="toolId"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined, ExportOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import vibeCodingLogo from '@/assets/icons/vibe-coding-logo-blue.svg'
import TaskVibeManualPanel from '@/components/task-progress/TaskVibeManualPanel.vue'
import { injectVibeToolPrompt, loadTaskCodexContext } from '@/composables/useTaskCodex'
import { isManualVibeTool } from '@/composables/useVibeCoding'
import { copyText } from '@/utils/clipboard'
import { useAppI18n, type MessageKey } from '@/i18n'

const props = withDefaults(
  defineProps<{
    toolName: string
    /** 执行工具枚举；手动模式据此判断，不依赖展示名。 */
    toolId?: string
    taskUuid?: string
    showOpenButton?: boolean
    opening?: boolean
    openLabel?: string
  }>(),
  {
    toolId: '',
    taskUuid: '',
    showOpenButton: false,
    opening: false,
    openLabel: '',
  },
)
const emit = defineEmits<{ open: [] }>()

const { t } = useAppI18n()

// 手动模式没有可拉起的进程，详情页直接给出可复制的提示词，并把入口指向完整面板。
const manual = computed(() => isManualVibeTool(props.toolId))
const panelOpen = ref(false)
const prompt = ref('')
const promptLoading = ref(false)
const promptError = ref('')
const aibridgeAvailable = ref(false)
const dispatching = ref(false)
let loadedFor = ''

const noticeKey = computed<MessageKey>(() => {
  if (props.toolId === 'generic') return 'workflows.task.detail.genericNotice'
  if (props.toolId === 'qoder_ide') return 'workflows.task.detail.qoderIDENotice'
  if (manual.value) return 'workflows.task.detail.manualNoticeDescription'
  return 'workflows.task.detail.vibeCodingNoticeDescription'
})

const buttonLabel = computed(() => {
  if (props.toolId === 'generic') return t('workflows.task.detail.copyCodexPrompt')
  if (manual.value) return t('workflows.task.detail.manualConfigAction')
  return props.openLabel || t('workflows.task.detail.openInCodex')
})

const promptText = computed(() => {
  if (promptLoading.value) return t('workflows.task.detail.manualPromptLoading')
  return prompt.value || promptError.value
})

async function loadPrompt() {
  const uuid = props.taskUuid
  if (!manual.value || !uuid || loadedFor === uuid || promptLoading.value) return
  promptLoading.value = true
  promptError.value = ''
  try {
    const context = await loadTaskCodexContext(uuid)
    prompt.value = context.prompt
    aibridgeAvailable.value = Boolean(context.aibridge_available)
    loadedFor = uuid
  } catch (error) {
    promptError.value = error instanceof Error ? error.message : t('workflows.task.detail.manualPromptFailed')
  } finally {
    promptLoading.value = false
  }
}

async function copyPrompt() {
  if (!prompt.value) return
  try {
    await copyText(prompt.value)
    message.success(t('workflows.task.detail.manualPromptCopied'))
  } catch {
    message.error(t('workflows.task.detail.manualCopyFailed'))
  }
}

async function dispatchPrompt() {
  if (dispatching.value || !props.taskUuid) return
  dispatching.value = true
  try {
    const result = await injectVibeToolPrompt(props.taskUuid)
    message.success(t('workflows.task.detail.manualDispatched', { editor: result.editor || props.toolName }))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.detail.manualDispatchFailed'))
  } finally {
    dispatching.value = false
  }
}

function retryPrompt() {
  loadedFor = ''
  void loadPrompt()
}

function handleAction() {
  if (manual.value) {
    panelOpen.value = true
    return
  }
  emit('open')
}

watch(
  () => [props.taskUuid, manual.value] as const,
  () => { void loadPrompt() },
  { immediate: true },
)
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

.notice-bar.is-manual {
  align-items: flex-start;
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

.manual-prompt {
  margin-top: 10px;
  border: 1px solid #eef1f6;
  border-radius: 12px;
  padding: 10px 14px;
  background: #fafbfd;
}

.manual-prompt__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}

.manual-prompt__label {
  color: #3157e2;
  font-size: 12px;
  font-weight: 500;
  line-height: 18px;
}

.manual-prompt__copy {
  display: inline-flex;
  height: 26px;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  border: 1px solid #c5d4f1;
  border-radius: 6px;
  padding: 0 10px;
  background: #fff;
  color: #3157e2;
  font-size: 12px;
  cursor: pointer;
  transition: border-color 180ms ease;
}

.manual-prompt__copy:hover:not(:disabled) {
  border-color: #3157e2;
}

.manual-prompt__copy:disabled {
  border-color: #e5e7eb;
  color: #bfbfbf;
  cursor: not-allowed;
}

.manual-prompt__copy.is-primary {
  border-color: #3157e2;
  background: #3157e2;
  color: #fff;
}

.manual-prompt__copy.is-primary:hover:not(:disabled) {
  border-color: #1d4ed8;
  background: #1d4ed8;
}

.manual-prompt__actions {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
}

.manual-prompt__code.is-error {
  color: #d4380d;
}

.manual-prompt__code {
  max-height: 200px;
  margin: 0;
  overflow: auto;
  color: #262626;
  font-family: inherit;
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
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
