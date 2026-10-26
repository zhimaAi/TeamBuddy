<template>
  <a-modal
    :open="open"
    :width="760"
    :footer="null"
    :title="t('workflows.task.detail.manualTitle', { tool: toolLabel })"
    @cancel="emit('update:open', false)"
  >
    <div class="manual-panel">
      <p class="manual-panel__intro">
        {{ t(introKey) }}
      </p>

      <div
        v-if="aibridgeAvailable"
        class="manual-panel__bridge"
      >
        <span class="manual-panel__bridge-text">
          {{ t('workflows.task.detail.manualAibridgeHint') }}
        </span>
        <button
          type="button"
          class="manual-panel__dispatch"
          :disabled="dispatching || !prompt"
          @click="dispatch"
        >
          {{ t('workflows.task.detail.manualDispatch') }}
        </button>
      </div>

      <div
        v-if="loading"
        class="manual-panel__loading"
      >
        <a-spin size="small" />
      </div>
      <div
        v-else-if="loadError"
        class="manual-panel__error"
      >
        <span>{{ loadError }}</span>
        <button
          type="button"
          class="manual-panel__retry"
          @click="reload"
        >
          {{ t('workflows.task.detail.manualRetry') }}
        </button>
      </div>

      <template v-else>
        <section
          v-for="section in sections"
          :key="section.key"
          class="manual-block"
        >
          <header class="manual-block__head">
            <span class="manual-block__title">{{ section.title }}</span>
            <button
              type="button"
              class="manual-block__copy"
              :disabled="!section.value"
              @click="copySection(section)"
            >
              {{ t('workflows.task.detail.manualCopy') }}
            </button>
          </header>
          <p
            v-if="section.steps"
            class="manual-block__steps"
          >
            {{ section.steps }}
          </p>
          <p class="manual-block__hint">
            {{ section.hint }}
          </p>
          <pre class="manual-block__code">{{ section.value || section.empty }}</pre>
        </section>
      </template>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { copyText } from '@/utils/clipboard'
import { injectVibeToolPrompt, loadTaskCodexContext } from '@/composables/useTaskCodex'
import { loadVibeMcpConfig, type VibeMcpConfig } from '@/composables/useDesktop'
import { vibeToolLabelKey, vibeToolMcpConfigPath, vibeToolMcpSetup, vibeToolSupportsAiBridge } from '@/composables/useVibeCoding'
import { useAppI18n, type MessageKey } from '@/i18n'

const props = defineProps<{
  open: boolean
  taskUuid: string
  tool: string
}>()

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
}>()

const { t } = useAppI18n()

interface ManualSection {
  key: string
  title: string
  steps: string
  hint: string
  value: string
  empty: string
}

const loading = ref(false)
const loadError = ref('')
const prompt = ref('')
const skillPath = ref('')
const mcp = ref<VibeMcpConfig>({ available: false, config: '', configPath: '', command: '' })
// AiBridge：装了扩展就能一键把提示词下发到编辑器；未装则只提供手动复制。
const aibridgeAvailable = ref(false)
const dispatching = ref(false)
// 已成功加载的 taskUuid：弹窗反复开关时不重复请求。
const loadedFor = ref('')

const toolLabel = computed(() => t(vibeToolLabelKey(props.tool)))
// AiBridge 只对编辑器宿主（Cursor）开放：WorkBuddy / DSH 不出现「发送到 Cursor」，
// 引导文案里也不提这条捷径。
const introKey = computed<MessageKey>(() => (vibeToolSupportsAiBridge(props.tool)
  ? 'workflows.task.detail.manualIntroAibridge'
  : 'workflows.task.detail.manualIntro'))
// 推荐写入路径优先取 sidecar 返回值；拿不到时按工具取本地默认（cursor / workbuddy）。
const mcpPathText = computed(() => mcp.value.configPath || vibeToolMcpConfigPath(props.tool) || 'mcp.json')
// DSH 不改 mcp.json：改用工具自带的可视化插件添加服务器，面板给出对应表单取值。
const mcpViaPanel = computed(() => vibeToolMcpSetup(props.tool) === 'panel')

function mcpSection(): ManualSection {
  if (mcpViaPanel.value) {
    return {
      key: 'mcp',
      title: t('workflows.task.detail.manualStepMcp'),
      steps: t('workflows.task.detail.manualPanelMcpSteps'),
      hint: t('workflows.task.detail.manualPanelMcpHint'),
      value: mcp.value.command
        ? t('workflows.task.detail.manualPanelMcpConfig', { command: mcp.value.command })
        : '',
      empty: t('workflows.task.detail.manualMcpUnavailable'),
    }
  }
  return {
    key: 'mcp',
    title: t('workflows.task.detail.manualStepMcp'),
    steps: t('workflows.task.detail.manualMcpSteps', { path: mcpPathText.value }),
    hint: t('workflows.task.detail.manualMcpHint', { path: mcpPathText.value }),
    value: mcp.value.available ? mcp.value.config : '',
    empty: t('workflows.task.detail.manualMcpUnavailable'),
  }
}

const sections = computed<ManualSection[]>(() => [
  mcpSection(),
  {
    key: 'skill',
    title: t('workflows.task.detail.manualStepSkill'),
    steps: '',
    hint: t('workflows.task.detail.manualSkillHint'),
    value: skillPath.value,
    empty: t('workflows.task.detail.manualSkillUnavailable'),
  },
  {
    key: 'prompt',
    title: t('workflows.task.detail.manualStepPrompt'),
    steps: '',
    hint: t('workflows.task.detail.manualPromptHint'),
    value: prompt.value,
    empty: t('workflows.task.detail.manualPromptFailed'),
  },
])

async function load() {
  if (!props.taskUuid || loading.value) return
  loading.value = true
  loadError.value = ''
  try {
    const context = await loadTaskCodexContext(props.taskUuid)
    prompt.value = context.prompt
    skillPath.value = context.skill_path || ''
    mcp.value = {
      available: Boolean(context.mcp_available && context.mcp_config),
      config: context.mcp_config || '',
      configPath: context.mcp_config_path || '',
      command: context.mcp_command || '',
    }
    // sidecar 未提供时退回 Electron 侧；旧版 preload 没有该入口时自动忽略。
    if (!mcp.value.available) {
      const fallback = await loadVibeMcpConfig(props.tool)
      if (fallback.available) mcp.value = fallback
    }
    aibridgeAvailable.value = Boolean(context.aibridge_available)
    loadedFor.value = props.taskUuid
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : t('workflows.task.detail.manualLoadFailed')
  } finally {
    loading.value = false
  }
}

function reload() {
  loadedFor.value = ''
  void load()
}

async function dispatch() {
  if (dispatching.value || !props.taskUuid) return
  dispatching.value = true
  try {
    const result = await injectVibeToolPrompt(props.taskUuid)
    message.success(t('workflows.task.detail.manualDispatched', { editor: result.editor || toolLabel.value }))
  } catch (error) {
    // 后端把「未检测到扩展 / 下发失败」的本地化原因放在 message 里，直接展示更准确。
    message.error(error instanceof Error ? error.message : t('workflows.task.detail.manualDispatchFailed'))
  } finally {
    dispatching.value = false
  }
}

async function copySection(section: ManualSection) {
  if (!section.value) return
  try {
    await copyText(section.value)
    message.success(t('workflows.task.detail.manualCopied', { section: section.title }))
  } catch {
    message.error(t('workflows.task.detail.manualCopyFailed'))
  }
}

watch(
  () => [props.open, props.taskUuid] as const,
  ([open, uuid]) => {
    if (!open || !uuid) return
    if (loadedFor.value === uuid) return
    void load()
  },
  { immediate: true },
)
</script>

<style scoped>
.manual-panel__intro {
  margin: 0 0 14px;
  color: #595959;
  font-size: 13px;
  line-height: 1.6;
}

.manual-panel__bridge {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 0 0 14px;
  border: 1px solid #c5d4f1;
  border-radius: 8px;
  padding: 8px 12px;
  background: #f5f8ff;
}

.manual-panel__bridge-text {
  flex: 1;
  min-width: 0;
  color: #3157e2;
  font-size: 12px;
  line-height: 1.6;
}

.manual-panel__dispatch {
  height: 28px;
  flex: 0 0 auto;
  border: 1px solid #3157e2;
  border-radius: 6px;
  padding: 0 12px;
  background: #3157e2;
  color: #fff;
  font-size: 12px;
  cursor: pointer;
}

.manual-panel__dispatch:hover:not(:disabled) {
  background: #1d4ed8;
}

.manual-panel__dispatch:disabled {
  border-color: #d9d9d9;
  background: #d9d9d9;
  cursor: not-allowed;
}

.manual-panel__loading {
  padding: 24px 0;
  text-align: center;
}

.manual-panel__error {
  display: flex;
  align-items: center;
  gap: 12px;
  color: #d4380d;
  font-size: 13px;
}

.manual-panel__retry {
  height: 26px;
  flex: 0 0 auto;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  padding: 0 10px;
  background: #fff;
  color: #595959;
  font-size: 12px;
  cursor: pointer;
}

.manual-block {
  margin-bottom: 18px;
}

.manual-block__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}

.manual-block__title {
  color: #262626;
  font-size: 13px;
  font-weight: 500;
}

.manual-block__copy {
  height: 24px;
  padding: 0 10px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  color: #595959;
  background: #fff;
  font-size: 12px;
  cursor: pointer;
}

.manual-block__copy:hover:not(:disabled) {
  border-color: #3157e2;
  color: #3157e2;
}

.manual-block__copy:disabled {
  color: #bfbfbf;
  cursor: not-allowed;
}

.manual-block__steps {
  margin: 0 0 6px;
  color: #262626;
  font-size: 12px;
  line-height: 1.9;
  white-space: pre-line;
}

.manual-block__hint {
  margin: 0 0 6px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 1.5;
}

.manual-block__code {
  max-height: 190px;
  margin: 0;
  padding: 10px;
  overflow: auto;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
  color: #262626;
  background: #fafafa;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
