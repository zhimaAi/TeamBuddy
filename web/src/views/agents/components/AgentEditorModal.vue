<template>
  <a-modal
    :open="open"
    class="agent-editor-modal"
	:title="editingAgent ? t('agents.editAgent') : t('agents.newAgentTitle')"
    width="730px"
    centered
    @update:open="handleOpenChange"
  >

    <div
      v-if="cloudLocked"
      class="cloud-lock"
    >
      <CloudDownloadOutlined />{{ t('agents.cloudLocked') }}
    </div>

    <button
      class="agent-avatar-preview"
      type="button"
      :disabled="cloudLocked"
      :aria-label="t('agents.uploadAgentAvatar')"
      @click="openFilePicker"
    >
      <img
        :src="previewAvatar"
        alt=""
      />
      <span class="agent-avatar-preview__camera">
        <img
          :src="projectModalCameraIcon"
          alt=""
          aria-hidden="true"
        />
      </span>
    </button>
    <p class="agent-avatar-hint">{{ t('agents.iconHint') }}</p>
    <input
      ref="fileInput"
      class="agent-avatar-file-input"
      type="file"
      :accept="ICON_FILE_ACCEPT"
      :disabled="cloudLocked"
      @change="handleFileChange"
    />

    <a-form
      class="agent-editor-form"
      layout="vertical"
    >
      <a-form-item
        class="agent-avatar-form-item"
        :label="t('agents.defaultAgentAvatar')"
      >
        <div
          class="avatar-options"
          :class="{ locked: cloudLocked }"
        >
          <button
            v-for="avatar in AGENT_AVATARS"
            :key="avatar"
            type="button"
            :disabled="cloudLocked"
            :class="{ active: !avatarFile && form.avatar === avatar }"
            :aria-pressed="!avatarFile && form.avatar === avatar"
            :aria-label="t('agents.selectAgentAvatar')"
            @click="selectPresetAvatar(avatar)"
          >
            <img
              :src="avatar"
              alt=""
            />
          </button>
        </div>
      </a-form-item>

      <a-form-item
        class="agent-editor-form-item"
      >
        <template #label>
          <span class="agent-modal-label">{{ t('agents.agentName') }}</span>
          <span class="agent-modal-label-hint">{{ t('agents.nameLimit') }}</span>
        </template>
        <a-input
          v-model:value="form.name"
          :disabled="cloudLocked"
          :maxlength="20"
          :show-count="false"
          :placeholder="t('agents.enter')"
        />
      </a-form-item>

      <a-form-item
        class="agent-editor-form-item"
      >
        <template #label>
          <span class="agent-modal-label">{{ t('agents.instruction') }}</span>
          <span class="agent-modal-label-hint agent-modal-label-hint--flush">{{ t('agents.promptHint') }}</span>
        </template>
        <a-textarea
          v-model:value="form.prompt"
          :disabled="cloudLocked"
          :maxlength="10000"
          :placeholder="t('agents.promptPlaceholder')"
        />
      </a-form-item>

      <a-form-item
        class="agent-editor-form-item cli-form-item"
        required
      >
        <template #label>
          <span class="agent-modal-label">{{ t('agents.cliTool') }}</span>
          <span class="agent-modal-label-hint">{{ t('agents.cliDetectionHint') }}</span>
        </template>
        <div class="cli-row">
          <a-select
            v-model:value="form.cli_type"
            :loading="cliLoading"
            :placeholder="t('agents.selectCli')"
            @change="loadModels(String($event))"
          >
            <a-select-option
              v-for="cli in cliOptions"
              :key="cli.type"
              :value="cli.type"
              :disabled="!cli.installed"
            >
			  {{ cliOptionLabel(cli) }}
            </a-select-option>
          </a-select>
          <a-button
            class="cli-reload-button"
            :loading="cliLoading"
            @click="detectCli()"
          >
            <img
              :src="agentCliReloadIcon"
              alt=""
              aria-hidden="true"
              class="cli-reload-button__icon"
            />{{ t('agents.detectAgain') }}
          </a-button>
        </div>
        <p
          class="cli-hint"
          :class="form.cli_type ? 'ok' : ''"
        >
          {{
            form.cli_type
              ? t('agents.selectedCliAvailable', { name: cliOptions.find((item) => item.type === form.cli_type)?.name || form.cli_type })
              : t('agents.selectAvailableCli')
          }}
        </p>
      </a-form-item>

      <a-form-item
        class="agent-editor-form-item model-form-item"
      >
        <template #label>
          <span class="agent-modal-label">{{ t('agents.model') }}</span>
          <span class="agent-modal-label-hint">{{ t('agents.modelHint') }}</span>
        </template>
        <a-select
          v-model:value="form.model"
          :loading="modelLoading"
          :disabled="!form.cli_type"
          show-search
          :placeholder="form.cli_type ? t('agents.selectModel') : t('agents.selectCliFirst')"
        >
          <a-select-option
            v-for="model in modelOptions"
            :key="model"
            :value="model"
          >
            {{ model }}
          </a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item v-if="form.cli_type && form.model" :label="t('components.cliThinking.title')">
        <CliThinkingSelect v-model="form.thinking_level" :cli-type="form.cli_type" :model-name="form.model" />
      </a-form-item>
    </a-form>

    <template #footer>
      <div class="agent-modal-footer-actions">
        <a-button @click="handleOpenChange(false)">{{ t('agents.cancel') }}</a-button>
        <a-button
          type="primary"
          :loading="saving"
          @click="save"
        >
          {{ t('agents.confirm') }}
        </a-button>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, toRef, watch } from 'vue'
import { CloudDownloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import apiClient from '@/api/client'
import CliThinkingSelect from '@/components/CliThinkingSelect.vue'
import { useCliThinkingSelection } from '@/composables/useCliThinkingSelection'
import { useCLIModelCatalogStore } from '@/stores/cli-models'
import agentCliReloadIcon from '@/assets/icons/agent-cli-reload.svg'
import projectModalCameraIcon from '@/assets/icons/project-modal-camera.svg'
import { ICON_FILE_ACCEPT, useIconFile } from '@/composables/useIconFile'
import type { ExpertMember, PipelineStep } from '@/types/pipeline'
import {
  AGENT_AVATARS,
  type CloudStepExecutionInput,
  type DiscoveredCLI,
  type StepInput,
  stepModel,
} from './agentPipeline'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()
const modelDirectories = useCLIModelCatalogStore()

const props = defineProps<{
  open: boolean
	pipelineUuid?: string
	isCloudPipeline?: boolean
  step?: PipelineStep
	expertGroupUuid?: string
	isCloudExpertGroup?: boolean
	expertMember?: ExpertMember
	expertRole?: 'leader' | 'member'
  currentAvatar?: string
}>()

const emit = defineEmits<{
  saved: []
  'update:open': [open: boolean]
}>()

const saving = ref(false)
const cliLoading = ref(false)
const modelLoading = ref(false)
const cliOptions = ref<DiscoveredCLI[]>([])
const modelOptions = ref<string[]>([])
const fileInput = ref<HTMLInputElement>()
const form = reactive({
  name: '',
  description: '',
  avatar: AGENT_AVATARS[0],
  prompt: '',
  cli_type: '',
  model: '',
  thinking_level: '',
})
const { resetThinkingDrafts } = useCliThinkingSelection(() => form.cli_type, () => form.model, toRef(form, 'thinking_level'))
const { file: avatarFile, previewUrl, selectFile, reset: resetAvatarFile } = useIconFile()
// 设计稿约束：Agent 头像大小不超过 100KB，比共享图标校验（2MB）更严格，仅作用于本弹窗
const MAX_AVATAR_FILE_SIZE = 100 * 1024
let modelRequestId = 0

// 云端资源（流水线步骤/专家团成员）仅锁编排字段，仍允许本地配置 CLI 与模型
const cloudLocked = computed(
  () =>
    (props.isCloudPipeline && Boolean(props.step)) ||
    (props.isCloudExpertGroup && Boolean(props.expertMember)),
)
const editingAgent = computed(() => props.expertMember || props.step)
const previewAvatar = computed(
  () => previewUrl.value || form.avatar || props.currentAvatar || AGENT_AVATARS[0],
)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    resetAvatarFile()
    void initialiseForm()
  },
)

async function initialiseForm() {
  modelRequestId += 1
  modelOptions.value = []
	const agent = editingAgent.value
	form.name = agent?.name || ''
	form.description = agent?.description || ''
	form.avatar = agent?.avatar || props.currentAvatar || AGENT_AVATARS[0]
	form.prompt = agent?.prompt || props.step?.prompt_snapshot || ''
	form.cli_type = agent?.cli_type || ''
	form.model = props.expertMember?.model_name || (props.step ? stepModel(props.step) : '')
  form.thinking_level = agent?.thinking_level || ''
  resetThinkingDrafts()
  await detectCli(false)
}

function openFilePicker() {
  if (!cloudLocked.value) fileInput.value?.click()
}

function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const nextFile = input.files?.[0]
  input.value = ''
  if (!nextFile) return

  if (nextFile.size > MAX_AVATAR_FILE_SIZE) {
    message.warning(t('agents.imageTooLarge'))
    return
  }

  try {
    selectFile(nextFile)
  } catch (error) {
    message.warning(error instanceof Error ? error.message : t('agents.avatarSelectFailed'))
  }
}

function selectPresetAvatar(avatar: string) {
  resetAvatarFile()
  form.avatar = avatar
}

function cliOptionLabel(cli: DiscoveredCLI) {
  return `${cli.name}（${cli.installed ? t('agents.installed') : t('agents.notInstalled')}）`
}

async function detectCli(showSuccess = true) {
  cliLoading.value = true
  try {
    const result = await apiClient.get<{ items: DiscoveredCLI[] }>('/tasks/cli-discovery', { refresh: showSuccess })
    // 已安装的 CLI 排在前面，未安装的置后且不可选择
    cliOptions.value = [...(result.items || [])].sort((a, b) => Number(b.installed) - Number(a.installed))
    // 手动检测必须同步刷新模型，而不是重新读取旧快照。
    await loadModels(form.cli_type, form.model, showSuccess)
    if (showSuccess) message.success(t('agents.cliDetected'))
  } catch (error) {
    if (showSuccess) message.error(error instanceof Error ? error.message : t('agents.cliDetectionFailed'))
  } finally {
    cliLoading.value = false
  }
}

async function loadModels(cliType: string, keepModel = '', refresh = false) {
  const requestId = ++modelRequestId
  modelOptions.value = modelDirectories.catalog(cliType)?.models || []
  form.model = keepModel
  if (!cliType) {
    modelLoading.value = false
    return
  }

  modelLoading.value = true
  try {
    const result = await modelDirectories.load(cliType, {}, refresh)
    if (requestId !== modelRequestId || cliType !== form.cli_type) return
    modelOptions.value = result.models || []
    if (!form.model) form.model = modelOptions.value[0] || ''
  } catch {
    if (requestId !== modelRequestId || cliType !== form.cli_type) return
    modelOptions.value = cliOptions.value.find((item) => item.type === cliType)?.models || []
  } finally {
    if (requestId === modelRequestId) modelLoading.value = false
  }
}

function buildStepPayload(): StepInput {
  return {
    name: form.name.trim(),
    description: form.description.trim(),
    avatar: form.avatar,
    prompt: form.prompt.trim(),
    cli_type: form.cli_type,
    model_name: form.model.trim(),
    thinking_level: form.thinking_level,
  }
}

function buildStepFormData(payload: StepInput) {
  const formData = new FormData()
  formData.append('name', payload.name)
  formData.append('description', payload.description)
  formData.append('avatar', payload.avatar)
  formData.append('prompt', payload.prompt)
  formData.append('cli_type', payload.cli_type)
  formData.append('model_name', payload.model_name)
  formData.append('thinking_level', payload.thinking_level || '')
  if (avatarFile.value) formData.append('avatar_file', avatarFile.value)
  return formData
}

async function save() {
	if (!props.pipelineUuid && !props.expertGroupUuid) return
  if (!form.name.trim()) return message.warning(t('agents.enterAgentName'))
  if (form.name.trim().length > 20) return message.warning(t('agents.agentNameTooLong'))
  if (!form.prompt.trim()) return message.warning(t('agents.enterPrompt'))
  if (!form.cli_type || !form.model.trim()) {
    return message.warning(t('agents.selectCliAndModel'))
  }

  saving.value = true
  try {
    const payload = buildStepPayload()
	const isExpertGroup = Boolean(props.expertGroupUuid)
	const path = isExpertGroup
	  ? props.expertMember
		? `/expert-groups/${encodeURIComponent(props.expertGroupUuid || '')}/members/${encodeURIComponent(props.expertMember.uuid)}`
		: `/expert-groups/${encodeURIComponent(props.expertGroupUuid || '')}/members`
	  : props.step
		? `/pipelines/${encodeURIComponent(props.pipelineUuid || '')}/steps/${encodeURIComponent(props.step.uuid)}`
		: `/pipelines/${encodeURIComponent(props.pipelineUuid || '')}/steps`
	const requestPayload = isExpertGroup
	  ? { ...payload, member_role: props.expertRole || props.expertMember?.member_role || 'member' }
	  : payload
    if (cloudLocked.value) {
      const executionPayload: CloudStepExecutionInput = {
        cli_type: payload.cli_type,
        model_name: payload.model_name,
        thinking_level: payload.thinking_level,
      }
      await apiClient.put<PipelineStep | ExpertMember>(path, executionPayload)
	} else if (avatarFile.value) {
	  const multipartPayload = buildStepFormData(payload)
	  if (isExpertGroup) multipartPayload.append('member_role', props.expertRole || props.expertMember?.member_role || 'member')
	  if (editingAgent.value) await apiClient.put<PipelineStep | ExpertMember>(path, multipartPayload)
	  else await apiClient.post<PipelineStep | ExpertMember>(path, multipartPayload)
	} else if (editingAgent.value) {
	  await apiClient.put<PipelineStep | ExpertMember>(path, requestPayload)
    } else {
	  await apiClient.post<PipelineStep | ExpertMember>(path, requestPayload)
    }
	message.success(editingAgent.value ? t('agents.agentUpdated') : t('agents.agentAdded'))
    emit('saved')
    handleOpenChange(false)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.agentSaveFailed'))
  } finally {
    saving.value = false
  }
}

function handleOpenChange(open: boolean) {
  if (!open) resetAvatarFile()
  emit('update:open', open)
}
</script>

<style scoped>
.cloud-lock {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 16px;
  padding: 9px 12px;
  border-radius: 8px;
  color: #b45309;
  background: #fff7e6;
  font-size: 12px;
  line-height: 20px;
}

.agent-avatar-preview {
  position: relative;
  display: flex;
  width: 60px;
  height: 60px;
  align-items: center;
  justify-content: center;
  margin: 0 auto;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: #f2f3f5;
  cursor: pointer;
}

.agent-avatar-preview:disabled {
  cursor: not-allowed;
}

.agent-avatar-preview > img {
  display: block;
  width: 60px;
  height: 60px;
  border-radius: 50%;
  object-fit: cover;
}

.agent-avatar-preview__camera {
  position: absolute;
  right: -4px;
  bottom: -4px;
  display: flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border: 0.75px solid #fff;
  border-radius: 50%;
  background: #262626;
  box-shadow: 0 2px 4px rgba(221, 221, 221, 0.12);
}

.agent-avatar-preview__camera img {
  display: block;
  width: 12px;
  height: 12px;
}

.agent-avatar-preview:focus-visible,
.avatar-options button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.agent-avatar-hint {
  margin: 10px 0 0;
  color: rgba(0, 0, 0, 0.45);
  font-size: 14px;
  line-height: 22px;
  text-align: center;
}

.agent-avatar-file-input {
  position: fixed;
  width: 1px;
  height: 1px;
  overflow: hidden;
  opacity: 0;
  pointer-events: none;
}

.agent-editor-form {
  margin-top: 24px;
}

.agent-avatar-form-item,
.agent-editor-form-item {
  margin-bottom: 20px;
}

.model-form-item {
  margin-bottom: 0;
}

.avatar-options {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.avatar-options button {
  display: flex;
  width: 34px;
  height: 34px;
  align-items: center;
  justify-content: center;
  padding: 1px;
  border: 2px solid transparent;
  border-radius: 50%;
  background: #fff;
  cursor: pointer;
}

.avatar-options button.active {
  border-color: #3157e2;
}

.avatar-options button:disabled {
  cursor: not-allowed;
}

.avatar-options img {
  display: block;
  width: 28px;
  height: 28px;
  border-radius: 50%;
  object-fit: cover;
}

.avatar-options.locked {
  opacity: 0.45;
}

.cli-row {
  display: flex;
  align-items: center;
  gap: 16px;
}

.cli-row :deep(.ant-select) {
  min-width: 0;
  flex: 1;
}

.cli-reload-button {
  flex: 0 0 auto;
}

.cli-hint {
  margin: 2px 0 0;
  color: #ed744a;
  font-size: 14px;
  line-height: 22px;
}

.cli-hint.ok {
  color: #15803d;
}

.agent-modal-footer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

:deep(.agent-editor-modal) {
  max-width: calc(100vw - 32px);
  padding-bottom: 0;
}

:deep(.agent-editor-modal .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border-radius: 24px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.16);
}

:deep(.agent-editor-modal .ant-modal-header) {
  margin-bottom: 0;
  padding: 24px;
  border-radius: 24px 24px 0 0;
  background: #fff;
}

:deep(.agent-editor-modal .ant-modal-title) {
  color: #262626;
  font-size: 24px;
  font-weight: 600;
  line-height: 32px;
}

:deep(.agent-editor-modal .ant-modal-body) {
  max-height: calc(100vh - 184px);
  overflow-y: auto;
  padding: 16px 48px 24px;
}

:deep(.agent-editor-modal .ant-modal-footer) {
  margin-top: 0;
  padding: 16px 48px;
  border-top: 1px solid #d9d9d9;
}

:deep(.agent-editor-modal .ant-modal-footer .ant-btn) {
  min-width: 65px;
  height: 32px;
  padding: 5px 16px;
  border-radius: 6px;
  font-size: 14px;
  line-height: 22px;
}

:deep(.agent-editor-form .ant-form-item-label) {
  padding-bottom: 8px;
}

:deep(.agent-editor-form .ant-form-item-label > label) {
  height: 22px;
  color: #262626;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
}

:deep(.agent-editor-form .ant-form-item-label > label.ant-form-item-required::before) {
  color: #fb363f;
}

.agent-modal-label-hint {
  margin-left: 4px;
  color: #8c8c8c;
  font-weight: 400;
}

.agent-modal-label-hint--flush {
  margin-left: 0;
}

:deep(.agent-editor-form .ant-input),
:deep(.agent-editor-form .ant-input-affix-wrapper),
:deep(.agent-editor-form .ant-select-selector) {
  min-height: 40px;
  border-color: #d9d9d9 !important;
  border-radius: 12px !important;
  color: #262626;
  font-size: 14px;
}

:deep(.agent-editor-form .ant-input) {
  padding: 8px 12px;
  line-height: 22px;
}

:deep(.agent-editor-form textarea.ant-input) {
  height: 128px;
  min-height: 128px;
  resize: none;
}

:deep(.agent-editor-form .ant-select-selector) {
  padding: 4px 12px !important;
}

:deep(.agent-editor-form .ant-select-selection-item),
:deep(.agent-editor-form .ant-select-selection-placeholder) {
  line-height: 30px !important;
}

:deep(.agent-editor-form .ant-input:hover),
:deep(.agent-editor-form .ant-select:not(.ant-select-disabled):hover .ant-select-selector) {
  border-color: #3157e2 !important;
}

:deep(.agent-editor-form .ant-input:focus),
:deep(.agent-editor-form .ant-input-focused),
:deep(.agent-editor-form .ant-select-focused .ant-select-selector) {
  border-color: #3157e2 !important;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.15) !important;
}

/* 与表单其他控件（输入框/下拉框）尺寸对齐：40px 高、14px 字号，避免 ant-btn 默认行高把按钮撑高 */
:deep(.ant-btn.cli-reload-button) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 4px 12px;
  color: #595959;
  font-size: 14px;
  line-height: 22px;
}

.cli-reload-button__icon {
  display: block;
  width: 18px;
  height: 18px;
}

@media (max-width: 760px) {
  :deep(.agent-editor-modal .ant-modal-header) {
    padding: 20px 24px;
  }

  :deep(.agent-editor-modal .ant-modal-title) {
    font-size: 20px;
    line-height: 28px;
  }

  :deep(.agent-editor-modal .ant-modal-body) {
    padding: 16px 24px 24px;
  }

  :deep(.agent-editor-modal .ant-modal-footer) {
    padding: 16px 24px;
  }

  .cli-row {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
