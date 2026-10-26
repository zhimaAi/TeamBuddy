<template>
  <a-tooltip v-if="visible" :title="capability.available ? undefined : capability.message || t('workflows.task.codex.capabilityUnavailable')">
    <span :tabindex="capability.available ? undefined : 0">
      <a-dropdown :disabled="busy || !capability.available" :trigger="['click']">
        <button type="button" class="codex-button" :disabled="busy || !capability.available" :aria-label="t('workflows.task.detail.codexActions')" aria-haspopup="menu"><img :src="toolLogo" alt="" aria-hidden="true" /><span>{{ toolLabel }}</span></button>
        <template #overlay><a-menu><a-menu-item key="open" @click="openTool"><DesktopOutlined /> {{ openLabel }}</a-menu-item><a-menu-item key="copy" @click="copyPrompt"><CopyOutlined /> {{ t('workflows.task.detail.copyCodexPrompt') }}</a-menu-item></a-menu></template>
      </a-dropdown>
    </span>
  </a-tooltip>

  <TaskVibeManualPanel
    v-model:open="panelOpen"
    :task-uuid="task.uuid"
    :tool="task.execution_tool || ''"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined, DesktopOutlined } from '@ant-design/icons-vue'
import codexLogo from '@/assets/icons/codex-logo.svg'
import TaskVibeManualPanel from '@/components/task-progress/TaskVibeManualPanel.vue'
import { assignTaskToCodex, copyTaskCodexPrompt, loadCodexCapability, openTaskInCodex, type CodexCapability } from '@/composables/useTaskCodex'
import { isManualVibeTool, vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import { useAppI18n } from '@/i18n'
import type { TaskWithDetails } from '@/types/task-detail'

const props = defineProps<{ task: TaskWithDetails }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useAppI18n()
// 未指派时保留 Codex 快捷入口；已指派为 Vibe Coding 时跟随实际工具展示。
const visible = computed(() => !props.task.execution_mode || props.task.execution_mode === 'vibe_coding')
// 手动模式没有可拉起的进程，打开与复制都改为展示可复制面板。
const manual = computed(() => props.task.execution_mode === 'vibe_coding' && isManualVibeTool(props.task.execution_tool || ''))
const toolLabel = computed(() => (props.task.execution_mode === 'vibe_coding'
  ? t(vibeToolLabelKey(props.task.execution_tool || ''))
  : t('workflows.task.assign.codex')))
const toolLogo = computed(() => (props.task.execution_mode === 'vibe_coding' ? vibeToolLogo(props.task.execution_tool || '') : codexLogo))
const openLabel = computed(() => (manual.value
  ? t('workflows.task.detail.openManualPanel')
  : t('workflows.task.detail.openCodex')))
const capability = ref<CodexCapability>({ available: false })
const busy = ref(false)
const panelOpen = ref(false)
let revision = 0
watch(() => [props.task.uuid, visible.value], async () => {
  const requestRevision = ++revision
  capability.value = { available: false }
  if (!visible.value) return
  try {
    const result = await loadCodexCapability()
    if (requestRevision === revision) capability.value = result
  } catch { /* 能力检测失败时保留禁用入口。 */ }
}, { immediate: true })
onBeforeUnmount(() => { revision += 1 })
async function openTool() {
  if (manual.value) { panelOpen.value = true; return }
  await performAction(false)
}
async function copyPrompt() {
  if (manual.value) { panelOpen.value = true; return }
  await performAction(true)
}
async function performAction(copy: boolean) {
  const task = props.task
  if (busy.value) return
  busy.value = true
  try {
    if (!task.execution_mode) {
      await assignTaskToCodex(task.uuid, false)
      emit('changed')
    }
    if (copy) {
      await copyTaskCodexPrompt(task.uuid)
      message.success(t('workflows.task.detail.codexPromptCopied'))
    } else {
      const result = await openTaskInCodex(task.uuid)
      if (result.opened) message.success(t('workflows.task.detail.codexOpened'))
      else if (result.copied) message.warning(t('workflows.task.detail.codexCopiedFallback'))
      else if (result.error) message.error(result.error.message)
      else message.warning(t('workflows.task.detail.codexOpenFailed'))
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t(copy ? 'workflows.task.detail.codexCopyFailed' : 'workflows.task.detail.codexOpenFailed'))
  } finally { busy.value = false }
}
</script>

<style scoped>
.codex-button { display: inline-flex; height: 28px; align-items: center; gap: 6px; padding: 0 8px; border: 1px solid #d9d9d9; border-radius: 6px; color: #595959; background: #fff; font-size: 12px; cursor: pointer; }
.codex-button img { width: 16px; height: 16px; }
.codex-button:hover:not(:disabled) { border-color: #3157e2; color: #3157e2; }
.codex-button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.codex-button:disabled { color: #bfbfbf; cursor: not-allowed; }
</style>
