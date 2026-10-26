<template>
  <a-tooltip v-if="visible" :title="capability.available ? undefined : capability.message || t('workflows.task.detail.toolCapabilityUnavailable', { tool: toolName })">
    <span :tabindex="capability.available ? undefined : 0">
      <a-dropdown v-model:open="menuOpen" :disabled="busy || !capability.available" :trigger="['click']">
        <button type="button" class="tool-button" :disabled="busy || !capability.available" :aria-label="t('workflows.task.detail.toolActions', { tool: toolName })" :aria-expanded="menuOpen" :aria-busy="busy" aria-haspopup="menu"><LoadingOutlined v-if="busy" spin /><img v-else :src="vibeToolLogo(tool)" alt="" aria-hidden="true" /><span class="tool-name" :title="toolName">{{ toolName }}</span></button>
        <template #overlay><a-menu><a-menu-item v-if="capability.launch !== 'clipboard'" key="open" @click="openTool"><DesktopOutlined /> {{ t('workflows.task.detail.openCodex') }}</a-menu-item><a-menu-item key="copy" @click="copyPrompt"><CopyOutlined /> {{ t('workflows.task.detail.copyCodexPrompt') }}</a-menu-item></a-menu></template>
      </a-dropdown>
    </span>
  </a-tooltip>
</template>

<script setup lang="ts">
import { showVibeToolOpenResult } from '@/composables/useVibeToolFeedback'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined, DesktopOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import { assignTaskToCodex, copyTaskVibeToolPrompt, loadVibeToolsCapability, openTaskVibeTool, type VibeToolCapability } from '@/composables/useTaskVibeTool'
import { vibeToolDefinition, vibeToolDisplayLabel, vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import { useAppI18n } from '@/i18n'
import type { TaskWithDetails } from '@/types/task-detail'

const props = defineProps<{ task: TaskWithDetails }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useAppI18n()
const visible = computed(() => !props.task.execution_mode || (props.task.execution_mode === 'vibe_coding' && ['app', 'clipboard'].includes(vibeToolDefinition(props.task.execution_tool || '')?.launch || '')))
const tool = computed(() => props.task.execution_mode ? props.task.execution_tool || 'codex' : 'codex')
const toolName = computed(() => vibeToolDisplayLabel(tool.value, t(vibeToolLabelKey(tool.value)), props.task.execution_tool_name))
const capability = ref<VibeToolCapability>({ available: false })
const busy = ref(false)
const menuOpen = ref(false)
let revision = 0
watch(() => [props.task.uuid, visible.value, tool.value, props.task.execution_index], async () => {
  const requestRevision = ++revision
  const requestedTool = tool.value
  menuOpen.value = false
  capability.value = { available: false }
  if (!visible.value) return
  try {
    const result = await loadVibeToolsCapability()
    if (requestRevision === revision) {
      capability.value = result.tools?.[requestedTool] || (requestedTool === 'codex' ? result : { available: false })
    }
  } catch { /* 能力检测失败时保留禁用入口。 */ }
}, { immediate: true })
onBeforeUnmount(() => { revision += 1 })
async function performAction(copy: boolean) {
  const task = props.task
  const currentToolName = toolName.value
  if (busy.value || !visible.value || !capability.value.available) return
  menuOpen.value = false
  busy.value = true
  try {
    if (!task.execution_mode) {
      await assignTaskToCodex(task.uuid, false)
      emit('changed')
    }
    if (copy) {
      await copyTaskVibeToolPrompt(task.uuid)
      if (tool.value === 'generic') {
        showVibeToolOpenResult({ opened: false, copied: true, guidanceKey: 'workflows.task.detail.genericPromptCopied' }, currentToolName)
      } else {
        message.success(t('workflows.task.detail.codexPromptCopied'))
      }
    } else {
      const result = await openTaskVibeTool(task.uuid)
      showVibeToolOpenResult(result, currentToolName)
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : copy
      ? t('workflows.task.detail.codexCopyFailed')
      : t('workflows.task.detail.toolOpenFailed', { tool: currentToolName }))
  } finally { busy.value = false }
}
function openTool() { void performAction(false) }
function copyPrompt() { void performAction(true) }
</script>

<style scoped>
.tool-button { display: inline-flex; height: 28px; align-items: center; gap: 6px; padding: 0 8px; border: 1px solid #d9d9d9; border-radius: 6px; color: #595959; background: #fff; font-size: 12px; cursor: pointer; }
.tool-name { max-width: 240px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-button img { width: 16px; height: 16px; }
.tool-button:hover:not(:disabled) { border-color: #3157e2; color: #3157e2; }
.tool-button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.tool-button:disabled { color: #bfbfbf; cursor: not-allowed; }
</style>
