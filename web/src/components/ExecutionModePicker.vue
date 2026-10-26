<template>
  <a-dropdown
    v-model:open="isOpen"
    :trigger="['click']"
    placement="bottomLeft"
    :get-popup-container="getPopupContainer"
    overlay-class-name="execution-mode-dropdown"
    @open-change="handleOpenChange"
  >
    <slot :selected-mode="modelValue" :is-open="isOpen">
      <button
        type="button"
        class="exec-mode-default-trigger"
        :class="{ 'is-selected': !!modelValue }"
      >
        <BranchesOutlined />
        <span class="trigger-text">{{ displayLabel || t('workflows.task.newConversation.chooseExecutionMode') || '选择执行方式' }}</span>
      </button>
    </slot>

    <template #overlay>
      <div
        class="exec-mode-panel scrollbar--subtle"
        role="menu"
        @click.stop
      >
        <!-- 流水线 -->
        <div class="mode-section-heading">
          <PipelineFlowIcon class="section-icon" />
          <span>{{ t('workflows.task.common.pipeline') }}</span>
        </div>
        <div
          v-if="pipelineLoading"
          class="mode-loading"
        >
          <a-spin size="small" />
        </div>
        <div
          v-else-if="!pipelines.length"
          class="mode-empty"
        >
          {{ t('workflows.task.common.noPipeline') }}
        </div>
        <div
          v-else
          class="mode-items-list"
        >
          <button
            v-for="p in pipelines"
            :key="p.uuid"
            type="button"
            class="mode-item"
            :class="{ 'is-selected': modelValue?.mode === 'pipeline' && modelValue?.pipelineUuid === p.uuid }"
            @click="selectPipeline(p)"
          >
            <img
              v-if="p.avatar"
              :src="p.avatar"
              alt=""
              class="mode-item__avatar"
            />
            <span
              v-else
              class="mode-item__avatar-fallback"
            >{{ p.name.slice(0, 1) }}</span>
            <span class="mode-item__name">{{ p.name }}</span>
            <CheckOutlined
              v-if="modelValue?.mode === 'pipeline' && modelValue?.pipelineUuid === p.uuid"
              class="check-icon"
            />
          </button>
        </div>

        <!-- 专家团队 -->
        <div class="mode-section-heading">
          <TeamOutlined class="section-icon" />
          <span>{{ t('agents.expertTeam') }}</span>
        </div>
        <div
          v-if="expertLoading"
          class="mode-loading"
        >
          <a-spin size="small" />
        </div>
        <div
          v-else-if="!expertGroups.length"
          class="mode-empty"
        >
          {{ t('workflows.task.notifications.noConversations') }}
        </div>
        <div
          v-else
          class="mode-items-list"
        >
          <button
            v-for="eg in expertGroups"
            :key="eg.uuid"
            type="button"
            class="mode-item"
            :class="{ 'is-selected': modelValue?.mode === 'expert_group' && modelValue?.expertGroupUuid === eg.uuid }"
            :disabled="!eg.ready"
            @click="selectExpertGroup(eg)"
          >
            <img
              v-if="eg.avatar"
              :src="eg.avatar"
              alt=""
              class="mode-item__avatar"
            />
            <span
              v-else
              class="mode-item__avatar-fallback"
            >{{ eg.name.slice(0, 1) }}</span>
            <span class="mode-item__name">{{ eg.name }}</span>
            <CheckOutlined
              v-if="modelValue?.mode === 'expert_group' && modelValue?.expertGroupUuid === eg.uuid"
              class="check-icon"
            />
          </button>
        </div>

        <!-- Vibe Coding -->
        <div class="mode-section-heading">
          <img
            :src="vibeCodingLogo"
            alt=""
            class="section-logo"
          />
          <span>{{ t('workflows.task.create.vibeCodingMode') }}</span>
        </div>
        <div class="mode-items-list">
          <div
            v-for="tool in VIBE_CODING_TOOLS"
            :key="tool.id"
            class="mode-item"
            :class="{ 'is-selected': modelValue?.mode === 'vibe_coding' && modelValue?.executionTool === tool.id }"
            :title="vibeToolDisabledReason(tool.id)"
          >
            <button type="button" class="vibe-tool-button" :disabled="vibeToolDisabled(tool.id)" @click="selectVibeTool(tool.id)">
              <img :src="vibeToolLogo(tool.id)" alt="" class="mode-item__avatar" />
              <span class="mode-item__name">{{ t(vibeToolLabelKey(tool.id)) }}</span>
            </button>
            <VibeModelPicker
              v-if="supportsVibeModel(tool.id)"
              :tool="tool.id"
              :model-value="vibeModels[tool.id] || ''"
              :selected="modelValue?.mode === 'vibe_coding' && modelValue.executionTool === tool.id"
              :disabled="vibeToolDisabled(tool.id)"
              @update:model-value="selectVibeTool(tool.id, $event)"
              @resolved="setVibeModel(tool.id, $event)"
            />
            <CheckOutlined v-if="modelValue?.mode === 'vibe_coding' && modelValue.executionTool === tool.id" class="check-icon" />
          </div>
        </div>

        <!-- 直接执行 (CLI) -->
        <div class="mode-section-heading">
          <img
            :src="cliExecutionLogo"
            alt=""
            class="section-logo"
          />
          <span>{{ t('workflows.task.common.directExecution') }}</span>
        </div>
        <div class="direct-cli-form">
          <a-select
            v-model:value="selectedCliType"
            class="cli-select"
            :placeholder="t('workflows.task.assign.chooseCli')"
            :options="cliSelectOptions"
            :loading="cliLoading"
            size="small"
            @change="handleCliTypeChange"
          />
          <a-select
            v-model:value="selectedCliModel"
            class="cli-select"
            :placeholder="selectedCliType ? t('workflows.task.assign.chooseModel') : t('workflows.task.assign.chooseCliFirst')"
            :options="modelSelectOptions"
            :loading="modelLoading"
            :disabled="!selectedCliType"
            size="small"
          />
          <button
            type="button"
            class="cli-confirm-btn"
            :disabled="!selectedCliType || !selectedCliModel"
            @click="confirmCliSelection"
          >
            {{ t('common.actions.confirm') }}
          </button>
        </div>
      </div>
    </template>
  </a-dropdown>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import VibeModelPicker from '@/components/VibeModelPicker.vue'
import { message } from 'ant-design-vue'
import { useVibeModelPreferences } from '@/composables/useVibeModelPreferences'
import {
  BranchesOutlined,
  TeamOutlined,
  CheckOutlined,
} from '@ant-design/icons-vue'
import PipelineFlowIcon from '@/components/task-progress/PipelineFlowIcon.vue'
import vibeCodingLogo from '@/assets/icons/vibe-coding-logo.svg'
import cliExecutionLogo from '@/assets/icons/task-composer-cli.svg'
import { usePipelineStore } from '@/stores/pipeline'
import { useExpertGroupStore } from '@/stores/expert-group'
import { useCliModelOptions } from '@/composables/useCliModelOptions'
import { loadVibeToolsCapability, type VibeToolsCapability } from '@/composables/useTaskVibeTool'
import {
  VIBE_CODING_TOOLS,
  isManualVibeTool,
  supportsVibeModel,
  isVibeCodingTool,
  vibeToolLabelKey,
  vibeToolLogo,
  writeRecentVibeTool,
  type VibeCodingToolId,
} from '@/composables/useVibeCoding'
import { useCliNames } from '@/composables/useCliNames'
import type { Pipeline, ExpertGroup } from '@/types/pipeline'
import { useAppI18n } from '@/i18n'

export interface ExecutionModeSelection {
  mode: 'pipeline' | 'expert_group' | 'vibe_coding' | 'cli'
  pipelineUuid?: string
  expertGroupUuid?: string
  executionTool?: string
  modelName?: string
  displayName: string
  avatar?: string
}

interface Props {
  modelValue?: ExecutionModeSelection | null
  getPopupContainer?: (triggerNode: HTMLElement) => HTMLElement
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: null,
  getPopupContainer: undefined,
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: ExecutionModeSelection): void
  (e: 'select', value: ExecutionModeSelection): void
}>()

const { t } = useAppI18n()
const { cliDisplayName, ensureLoaded: ensureCliNamesLoaded } = useCliNames()

const pipelineStore = usePipelineStore()
const expertGroupStore = useExpertGroupStore()
const {
  cliOptions,
  modelOptions,
  cliLoading,
  modelLoading,
  loadCliOptions,
  loadModelOptions,
} = useCliModelOptions()

const isOpen = ref(false)
const { vibeModels, resetVibeModels, setVibeModel, resolveVibeModel } = useVibeModelPreferences()
let vibeSelectionRequest = 0
watch(isOpen, open => {
  vibeSelectionRequest++
  if (open) resetVibeModels(props.modelValue?.mode === 'vibe_coding' ? props.modelValue.executionTool || '' : '', props.modelValue?.modelName || '')
})
const codexCapability = ref<VibeToolsCapability>({ available: true })

const selectedCliType = ref('')
const selectedCliModel = ref('')

const pipelines = computed(() => pipelineStore.pipelines)
const pipelineLoading = computed(() => pipelineStore.loading)
const expertGroups = computed(() => expertGroupStore.items)
const expertLoading = computed(() => expertGroupStore.loading)

const cliSelectOptions = computed(() =>
  cliOptions.value.map((item) => ({
    label: `${cliDisplayName(item.type)}${item.installed ? '' : ` (${t('workflows.task.assign.notInstalled')})`}`,
    value: item.type,
    disabled: !item.installed,
  })),
)

const modelSelectOptions = computed(() =>
  modelOptions.value.map((m) => ({ label: m, value: m })),
)

const displayLabel = computed(() => {
  if (!props.modelValue) return ''
  return props.modelValue.displayName
})

watch(
  () => props.modelValue,
  (val) => {
    if (val?.mode === 'cli') {
      if (val.executionTool) selectedCliType.value = val.executionTool
      if (val.modelName) selectedCliModel.value = val.modelName
    }
  },
  { immediate: true },
)

function selectPipeline(p: Pipeline) {
  const selection: ExecutionModeSelection = {
    mode: 'pipeline',
    pipelineUuid: p.uuid,
    displayName: `${t('workflows.task.common.pipeline')} · ${p.name}`,
    avatar: p.avatar,
  }
  emit('update:modelValue', selection)
  emit('select', selection)
  isOpen.value = false
}

function selectExpertGroup(eg: ExpertGroup) {
  const selection: ExecutionModeSelection = {
    mode: 'expert_group',
    expertGroupUuid: eg.uuid,
    displayName: `${t('agents.expertTeam')} · ${eg.name}`,
    avatar: eg.avatar,
  }
  emit('update:modelValue', selection)
  emit('select', selection)
  isOpen.value = false
}

function vibeCapability(tool: string) {
  return codexCapability.value.tools?.[tool]
}

function vibeToolDisabled(tool: string) {
  if (!isVibeCodingTool(tool)) return true
  const capability = vibeCapability(tool)
  if (!capability) return tool !== 'codex' || !codexCapability.value.available
  if (!capability.available) return true
  // 手动模式不启动本机进程，没有可执行文件可探测，只要能力可用就允许选择。
  if (isManualVibeTool(tool)) return false
  return tool !== 'codex' && capability.installed === false
}

function vibeToolDisabledReason(tool: string) {
  if (!vibeToolDisabled(tool)) return ''
  return vibeCapability(tool)?.message || codexCapability.value.message || t('workflows.task.codex.capabilityUnavailable')
}

async function selectVibeTool(tool: VibeCodingToolId, model?: string) {
  if (vibeToolDisabled(tool)) return
  const toolName = t(vibeToolLabelKey(tool))
  const request = ++vibeSelectionRequest
  if (model !== undefined) setVibeModel(tool, model)
  else if (props.modelValue?.mode === 'vibe_coding' && props.modelValue.executionTool === tool) {
    vibeModels.value[tool] = props.modelValue.modelName || ''
  }
  let modelName = ''
  try {
    modelName = await resolveVibeModel(tool)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.assign.vibeModelLoadFailed'))
    return
  }
  if (request !== vibeSelectionRequest || !isOpen.value) return
  writeRecentVibeTool(tool)
  const selection: ExecutionModeSelection = {
    mode: 'vibe_coding',
    executionTool: tool,
    modelName,
    displayName: [`${t('workflows.task.create.vibeCodingMode')} · ${toolName}`, modelName].filter(Boolean).join(' · '),
  }
  emit('update:modelValue', selection)
  emit('select', selection)
  isOpen.value = false
}

async function handleCliTypeChange(cliType: unknown) {
  const typeStr = String(cliType || '')
  selectedCliModel.value = ''
  if (typeStr) {
    const models = await loadModelOptions(typeStr)
    if (models.length > 0) {
      selectedCliModel.value = models[0]
    }
  }
}

function confirmCliSelection() {
  if (!selectedCliType.value || !selectedCliModel.value) return
  const cliName = cliDisplayName(selectedCliType.value)
  const selection: ExecutionModeSelection = {
    mode: 'cli',
    executionTool: selectedCliType.value,
    modelName: selectedCliModel.value,
    displayName: `${t('workflows.task.common.directExecution')} · ${cliName} (${selectedCliModel.value})`,
  }
  emit('update:modelValue', selection)
  emit('select', selection)
  isOpen.value = false
}

async function handleOpenChange(open: boolean) {
  isOpen.value = open
  if (open) {
    void pipelineStore.loadPipelines()
    void expertGroupStore.load()
    void loadCliOptions()
    void ensureCliNamesLoaded()
    try {
      codexCapability.value = await loadVibeToolsCapability()
    } catch {
      // ignore
    }
  }
}

onMounted(() => {
  void ensureCliNamesLoaded()
  if (props.modelValue?.mode === 'cli' && props.modelValue.executionTool) {
    void loadModelOptions(props.modelValue.executionTool)
  }
})
</script>

<style scoped>
.vibe-tool-button { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer; }
.vibe-tool-button:disabled { color: #bfbfbf; cursor: not-allowed; }
.vibe-tool-button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.exec-mode-default-trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 10px;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
  background: #f9f9f9;
  color: #595959;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.2s;
}

.exec-mode-default-trigger:hover {
  background: #f0f0f0;
  border-color: #d1d5db;
}

.exec-mode-default-trigger.is-selected {
  background: #f0f7ff;
  border-color: #bfdbfe;
  color: #2563eb;
}

.trigger-text {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.exec-mode-panel {
  width: 320px;
  max-width: calc(100vw - 32px);
  max-height: 480px;
  background: #ffffff;
  border-radius: 10px;
  box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.1);
  border: 1px solid #f0f0f0;
  padding: 8px 6px;
  overflow-y: auto;
}

.mode-section-heading {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 8px 4px;
  font-size: 12px;
  font-weight: 600;
  color: #8c8c8c;
}

.section-icon {
  font-size: 14px;
}

.section-logo {
  width: 14px;
  height: 14px;
}

.mode-loading {
  padding: 8px;
  text-align: center;
}

.mode-empty {
  padding: 6px 8px;
  color: #bfbfbf;
  font-size: 12px;
}

.mode-items-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.mode-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: #262626;
  font-size: 13px;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s;
}

.mode-item:hover:not(:disabled) {
  background: #f5f5f5;
}

.mode-item.is-selected {
  background: #f0f7ff;
  color: #2563eb;
}

.mode-item:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.mode-item__avatar {
  width: 18px;
  height: 18px;
  border-radius: 4px;
  object-fit: cover;
}

.mode-item__avatar-fallback {
  width: 18px;
  height: 18px;
  border-radius: 4px;
  background: #e5e7eb;
  color: #595959;
  font-size: 11px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.mode-item__name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.check-icon {
  color: #2563eb;
  font-size: 13px;
}

.direct-cli-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 6px 8px;
}

.cli-select {
  width: 100%;
}

.cli-confirm-btn {
  margin-top: 2px;
  height: 28px;
  padding: 0 12px;
  background: #2563eb;
  color: #ffffff;
  border: none;
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  transition: background 0.2s;
}

.cli-confirm-btn:hover:not(:disabled) {
  background: #1d4ed8;
}

.cli-confirm-btn:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}
</style>
