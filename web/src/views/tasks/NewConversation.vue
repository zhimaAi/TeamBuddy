<template>
  <div class="new-conversation-page">
    <div class="new-conversation-center">
      <div class="title-area">
        <h1 class="main-title">{{ t('workflows.task.newConversation.title') || '开始一个新任务' }}</h1>
        <p class="sub-title">
          {{ t('workflows.task.newConversation.subtitle') || '描述你的目标，选择工作目录和执行方式。' }}
        </p>
      </div>

      <div class="composer-stack">
        <OnboardingGuide
          v-if="guideVisible"
          :phase="onboardingPhase"
          :has-prompt="hasPrompt"
          @skip="dismissOnboarding"
          @start="startOnboarding"
          @fill-example="fillOnboardingExample"
        />

        <div
          class="task-composer-card"
          :class="{ 'is-onboarding-input': onboardingTarget === 'input' }"
          @paste="handlePaste"
          @dragover.prevent
          @drop.prevent="handleDrop"
        >
        <ComposerImageAttachments
          :items="attachments"
          :disabled="submitting"
          @remove="removeAttachmentItem"
        />

        <div
          v-if="knowledgeDocuments.length"
          class="knowledge-reference-row"
          :aria-label="t('workflows.task.progress.referencedDocuments')"
        >
          <span
            v-for="document in knowledgeDocuments"
            :key="document.uuid"
            class="knowledge-reference-chip"
            :title="knowledgeDocumentName(document)"
          >
            <button
              type="button"
              :disabled="submitting"
              :aria-label="t('workflows.task.progress.removeReference', { name: knowledgeDocumentName(document) })"
              @click="removeKnowledgeReference(document.uuid)"
            >
              <img :src="knowledgeReferenceIcon" alt="" aria-hidden="true" />
              <CloseOutlined class="knowledge-reference-remove" aria-hidden="true" />
            </button>
            <span class="knowledge-reference-name">{{ knowledgeDocumentName(document) }}</span>
          </span>
        </div>

        <!-- 文本输入区 -->
        <textarea
          ref="textareaRef"
          v-model="question"
          class="composer-textarea"
          :placeholder="t('workflows.task.newConversation.placeholder') || '描述你希望完成的任务⋯'"
          rows="4"
          :disabled="submitting"
          @keydown="handleKeydown"
        />

        <!-- 隐藏的图片上传 input -->
        <input
          ref="fileInputRef"
          type="file"
          accept="image/png,image/jpeg,image/webp,image/gif,.png,.jpg,.jpeg,.webp,.gif"
          multiple
          class="hidden-file-input"
          @change="handleFileInputChange"
        />

        <!-- 底部工具栏 -->
        <div class="composer-toolbar">
          <div class="toolbar-left">
            <button
              type="button"
              class="action-btn"
              :disabled="submitting"
              aria-haspopup="dialog"
              :aria-expanded="knowledgePickerOpen"
              @click="knowledgePickerOpen = true"
            >
              <img class="tool-icon" :src="knowledgeIcon" alt="" aria-hidden="true" />
              <span>{{ t('workflows.task.progress.knowledge') }}</span>
            </button>

            <!-- 添加附件按钮 -->
            <button
              type="button"
              class="action-btn"
              :disabled="submitting"
              :aria-label="t('workflows.task.attachments.uploadImage')"
              :title="t('workflows.task.attachments.uploadImage') || '上传图片'"
              @click="triggerFileInput"
            >
              <img class="tool-icon" :src="attachmentIcon" alt="" aria-hidden="true" />
              <span>{{ t('workflows.task.progress.attachment') }}</span>
            </button>

            <!-- 选择执行方式：与看板、任务详情共用指定执行方式弹窗 -->
            <button
              type="button"
              class="action-btn execution-mode-button"
              :class="{
                'is-onboarding-target': onboardingTarget === 'execution',
                'is-onboarding-pulse': onboardingTarget === 'execution',
              }"
              :disabled="submitting"
              :title="executionModeDisplayName || t('workflows.task.newConversation.chooseExecutionMode')"
              aria-haspopup="dialog"
              :aria-expanded="executionPickerOpen"
              @click="executionPickerOpen = true"
            >
              <TeamOutlined
                v-if="executionMode?.mode === 'expert_group'"
                class="tool-icon execution-mode-icon"
                aria-hidden="true"
              />
              <span
                v-else-if="executionMode?.mode === 'pipeline'"
                class="tool-icon execution-mode-icon pipeline-icon"
                :style="{ '--pipeline-icon': `url(${pipelineIcon})` }"
                aria-hidden="true"
              />
              <img
                v-else
                class="tool-icon execution-mode-icon"
                :src="executionModeIcon"
                alt=""
                aria-hidden="true"
              />
              <span class="btn-text">
                {{ executionModeToolbarLabel || t('workflows.task.newConversation.chooseExecutionMode') || '选择执行方式' }}
              </span>
            </button>

            <!-- 选择工作目录 -->
            <WorkDirectoryPicker
              v-model="workDirectory"
              @select="onWorkDirSelect"
            >
              <template #default="{ isOpen }">
                <button
                  type="button"
                  class="action-btn work-directory-button"
                  :disabled="submitting"
                  :title="workDirectory || t('workflows.task.newConversation.chooseDirectory')"
                  :aria-label="t('workflows.task.newConversation.chooseDirectory')"
                  aria-haspopup="menu"
                  :aria-expanded="isOpen"
                  :class="{
                    'is-onboarding-target': onboardingTarget === 'directory',
                  }"
                >
                  <img class="tool-icon directory-icon" :src="directoryIcon" alt="" aria-hidden="true" />
                  <span class="btn-text">
                    {{ displayWorkDir || t('workflows.task.newConversation.chooseDirectory') || '选择工作目录' }}
                  </span>
                </button>
              </template>
            </WorkDirectoryPicker>
          </div>

          <div class="toolbar-right">
            <!-- 发送按钮 -->
            <button
              type="button"
              class="send-btn"
              :class="{ 'is-onboarding-pulse': onboardingTarget === 'send' }"
              :disabled="!canSubmit"
              :title="t('workflows.task.progress.send')"
              :aria-label="t('workflows.task.progress.send')"
              @click="handleSubmit"
            >
              <LoadingOutlined v-if="submitting" spin />
              <span v-else class="send-button-text">{{ t('workflows.task.progress.send') }}</span>
            </button>
          </div>
        </div>
        </div>
      </div>
    </div>

    <KnowledgeDocumentPickerModal
      v-model:open="knowledgePickerOpen"
      :referenced-uuids="knowledgeDocuments.map((document) => document.uuid)"
      @confirm="addKnowledgeReferences"
    />

    <AssignExecutionModeModal
      v-model:open="executionPickerOpen"
      select-only
      intent="create"
      :initial-mode="executionMode?.mode || ''"
      :preferred-pipeline-uuid="executionMode?.pipelineUuid || ''"
      :preferred-expert-group-uuid="executionMode?.expertGroupUuid || ''"
      :initial-cli-type="executionMode?.mode === 'cli' ? executionMode.executionTool || '' : ''"
      :initial-model-name="executionMode?.modelName || ''"
      :initial-vibe-tool="executionMode?.mode === 'vibe_coding' ? executionMode.executionTool || '' : ''"
      @selected="onExecutionModeSelected"
    />
  </div>
</template>

<script setup lang="ts">
import { showVibeToolOpenResult } from '@/composables/useVibeToolFeedback'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import {
  TeamOutlined,
  CloseOutlined,
  LoadingOutlined,
} from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import { getKnowledgeDocumentPath, type KnowledgeDocument } from '@/api/knowledge'
import {
  buildKnowledgeReferenceBlock,
  countCharacters,
  KNOWLEDGE_REFERENCE_CHAR_LIMIT,
  loadKnowledgeReferenceContent,
  knowledgeDocumentName,
} from '@/composables/useKnowledgeReferences'
import KnowledgeDocumentPickerModal from '@/components/task-progress/KnowledgeDocumentPickerModal.vue'
import knowledgeIcon from '@/assets/icons/task-composer-knowledge.svg'
import knowledgeReferenceIcon from '@/assets/icons/task-knowledge-reference.svg'
import attachmentIcon from '@/assets/icons/task-composer-attachment.svg'
import pipelineIcon from '@/assets/icons/task-composer-pipeline.svg'
import cliTriggerIcon from '@/assets/icons/task-composer-cli-trigger.svg'
import directoryIcon from '@/assets/icons/task-header-directory.svg'
import { cliLogo } from '@/composables/useCliLogo'
import WorkDirectoryPicker from '@/components/WorkDirectoryPicker.vue'
import AssignExecutionModeModal, {
  type ExecutionModeSelection as AssignedExecutionMode,
} from '@/components/AssignExecutionModeModal.vue'
import type { ExecutionModeSelection } from '@/components/ExecutionModePicker.vue'
import OnboardingGuide from '@/views/tasks/components/OnboardingGuide.vue'
import { resolveOnboardingTarget, useOnboardingGuide } from '@/composables/useOnboardingGuide'
import {
  NEW_CONVERSATION_DRAFT_KEY,
  readComposerDraft,
  writeComposerDraft,
} from '@/composables/useComposerDraft'
import { useConversationStore, getLastDirSegment } from '@/stores/conversation'
import { useCreateTaskDefaults } from '@/composables/useCreateTaskDefaults'
import { useTaskImageAttachments } from '@/composables/useTaskImageAttachments'
import { openTaskVibeTool } from '@/composables/useTaskVibeTool'
import { pendingCliKickoffUuid } from '@/composables/useCliKickoff'
import { usePipelineStore } from '@/stores/pipeline'
import { useExpertGroupStore } from '@/stores/expert-group'
import { useCliNames } from '@/composables/useCliNames'
import { vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import { readRecentVibeModels, useVibeModelPreferences } from '@/composables/useVibeModelPreferences'
import { useAppI18n } from '@/i18n'
import ComposerImageAttachments from '@/components/task-progress/ComposerImageAttachments.vue'

const LAST_EXEC_MODE_KEY = 'goteams.newConversation.lastExecutionMode'
const LAST_WORK_DIR_KEY = 'goteams.newConversation.lastWorkDir'

const route = useRoute()
const router = useRouter()
const { t } = useAppI18n()
const {
  status: onboardingStatus,
  phase: onboardingPhase,
  start: beginOnboardingSteps,
  reconcile: reconcileOnboarding,
  skip: dismissOnboarding,
  complete: completeOnboarding,
} = useOnboardingGuide()
const conversationStore = useConversationStore()
const pipelineStore = usePipelineStore()
const expertGroupStore = useExpertGroupStore()
const { cliDisplayName, ensureLoaded: ensureCliNamesLoaded } = useCliNames()
const executionPickerOpen = ref(false)
const knowledgePickerOpen = ref(false)
const knowledgeDocuments = ref<KnowledgeDocument[]>([])
const { getCreateTaskDefaults, saveCreateTaskDefaults } = useCreateTaskDefaults()
const {
  items: attachments,
  addImages,
  buildDraftAttachmentInputs,
  clear: clearAttachments,
  remove: removeAttachmentItem,
} = useTaskImageAttachments()

const question = ref(readComposerDraft(NEW_CONVERSATION_DRAFT_KEY))
watch(question, (value) => writeComposerDraft(NEW_CONVERSATION_DRAFT_KEY, value))
const workDirectory = ref('')
const executionMode = ref<ExecutionModeSelection | null>(null)
const submitting = ref(false)
const textareaRef = ref<HTMLTextAreaElement | null>(null)
const fileInputRef = ref<HTMLInputElement | null>(null)

const displayWorkDir = computed(() => {
  if (!workDirectory.value) return ''
  const info = conversationStore.resolveWorkDirInfo(workDirectory.value)
  return info.projectName || getLastDirSegment(workDirectory.value) || workDirectory.value
})

const canSubmit = computed(() => {
  return (
    !submitting.value &&
    question.value.trim().length > 0 &&
    workDirectory.value.trim().length > 0 &&
    executionMode.value !== null
  )
})

const hasPrompt = computed(() => question.value.trim().length > 0)
const guideVisible = computed(() => onboardingStatus.value === 'active')
const onboardingTarget = computed(() =>
  resolveOnboardingTarget(onboardingStatus.value, onboardingPhase.value, hasPrompt.value),
)

function onboardingFacts() {
  return {
    hasExecutionMode: executionMode.value !== null,
    hasWorkDirectory: workDirectory.value.trim().length > 0,
  }
}

function startOnboarding() {
  beginOnboardingSteps(onboardingFacts())
}

function fillOnboardingExample() {
  question.value = t('workflows.task.newConversation.onboarding.examplePrompt')
  nextTick(() => {
    const input = textareaRef.value
    if (!input) return
    input.focus()
    const end = input.value.length
    input.setSelectionRange(end, end)
  })
}

watch([executionMode, workDirectory], () => {
  reconcileOnboarding(onboardingFacts())
})

watch(onboardingTarget, (target) => {
  if (target !== 'input') return
  nextTick(() => textareaRef.value?.focus())
})

const { resetVibeModels, resolveVibeModel } = useVibeModelPreferences()

async function validateVibeMode(mode: ExecutionModeSelection) {
  if (mode.mode !== 'vibe_coding' || !mode.executionTool) return
  resetVibeModels(mode.executionTool, mode.modelName ?? readRecentVibeModels()[mode.executionTool] ?? '')
  const model = await resolveVibeModel(mode.executionTool)
  if (executionMode.value === mode) mode.modelName = model
}

function restorePreferences() {
  const queryDir = (route.query.workDir as string | undefined)?.trim()
  if (queryDir) {
    workDirectory.value = queryDir
  } else {
    try {
      const savedDir = localStorage.getItem(LAST_WORK_DIR_KEY)
      if (savedDir) {
        workDirectory.value = savedDir
      } else {
        const defaults = getCreateTaskDefaults()
        if (defaults.work_dir) workDirectory.value = defaults.work_dir
      }
    } catch {
      // ignore
    }
  }

  try {
    const rawMode = localStorage.getItem(LAST_EXEC_MODE_KEY)
    if (rawMode) {
      const parsed = JSON.parse(rawMode) as ExecutionModeSelection
      // 历史缓存包含保存时语言下的 displayName。只恢复语义字段，展示文案按当前
      // locale 重新计算，避免切换语言或重启后继续显示旧语言。
      executionMode.value = { ...parsed, displayName: '' }
      if (parsed.mode === 'pipeline') void pipelineStore.loadPipelines().catch(() => {})
      if (parsed.mode === 'expert_group') void expertGroupStore.load().catch(() => {})
    }
  } catch {
    // ignore
  }
}

watch(
  () => route.query.workDir,
  (newDir) => {
    if (typeof newDir === 'string' && newDir.trim()) {
      workDirectory.value = newDir.trim()
    }
  },
)

function onWorkDirSelect(dir: string) {
  workDirectory.value = dir
  try {
    localStorage.setItem(LAST_WORK_DIR_KEY, dir)
  } catch {
    // ignore
  }
  const defaults = getCreateTaskDefaults()
  saveCreateTaskDefaults({ ...defaults, work_dir: dir })
}

function selectionDisplayName(selection: AssignedExecutionMode): string {
  if (selection.mode === 'pipeline') {
    const name = pipelineStore.pipelines.find((item) => item.uuid === selection.pipelineUuid)?.name
    return name
      ? `${t('workflows.task.common.pipeline')} · ${name}`
      : t('workflows.task.common.pipeline')
  }
  if (selection.mode === 'expert_group') {
    const name = expertGroupStore.items.find((item) => item.uuid === selection.expertGroupUuid)?.name
    return name ? `${t('agents.expertTeam')} · ${name}` : t('agents.expertTeam')
  }
  if (selection.mode === 'vibe_coding') {
    const label = `${t('workflows.task.create.vibeCodingMode')} · ${t(vibeToolLabelKey(selection.tool))}`
    return selection.modelName ? `${label} · ${selection.modelName}` : label
  }
  const cliName = cliDisplayName(selection.tool)
  return selection.modelName
    ? `${t('workflows.task.common.directExecution')} · ${cliName} (${selection.modelName})`
    : `${t('workflows.task.common.directExecution')} · ${cliName}`
}

const executionModeDisplayName = computed(() => {
  const mode = executionMode.value
  if (!mode) return ''
  return selectionDisplayName({
    mode: mode.mode,
    tool: mode.executionTool || '',
    pipelineUuid: mode.pipelineUuid || '',
    expertGroupUuid: mode.expertGroupUuid || '',
    modelName: mode.modelName || '',
  })
})

const executionModeIcon = computed(() => {
  const mode = executionMode.value
  if (mode?.mode === 'cli') return cliLogo(mode.executionTool)
  if (mode?.mode === 'vibe_coding') return vibeToolLogo(mode.executionTool || 'codex')
  return cliTriggerIcon
})

const executionModeToolbarLabel = computed(() => {
  const mode = executionMode.value
  if (!mode) return ''
  if (mode.mode === 'cli') {
    const name = cliDisplayName(mode.executionTool || '')
    return mode.modelName ? `${name} / ${mode.modelName}` : name
  }
  if (mode.mode === 'pipeline') {
    return (
      pipelineStore.pipelines.find((item) => item.uuid === mode.pipelineUuid)?.name
      || t('workflows.task.common.pipeline')
    )
  }
  if (mode.mode === 'expert_group') {
    return (
      expertGroupStore.items.find((item) => item.uuid === mode.expertGroupUuid)?.name
      || t('agents.expertTeam')
    )
  }
  return t(vibeToolLabelKey(mode.executionTool || 'codex'))
})

function serializeExecutionMode(mode: ExecutionModeSelection) {
  return JSON.stringify({
    mode: mode.mode,
    pipelineUuid: mode.pipelineUuid,
    expertGroupUuid: mode.expertGroupUuid,
    executionTool: mode.executionTool,
    modelName: mode.modelName,
  })
}

async function onExecutionModeSelected(selection: AssignedExecutionMode) {
  if (selection.mode === 'cli') await ensureCliNamesLoaded()
  const mode: ExecutionModeSelection = {
    mode: selection.mode,
    pipelineUuid: selection.pipelineUuid || undefined,
    expertGroupUuid: selection.expertGroupUuid || undefined,
    executionTool: selection.tool || undefined,
    modelName: selection.modelName || undefined,
    displayName: selectionDisplayName(selection),
  }
  executionMode.value = mode
  try {
    localStorage.setItem(LAST_EXEC_MODE_KEY, serializeExecutionMode(mode))
  } catch {
    // ignore
  }
}

function addKnowledgeReferences(documents: KnowledgeDocument[]) {
  if (submitting.value) return
  const existing = new Set(knowledgeDocuments.value.map((document) => document.uuid))
  knowledgeDocuments.value = [
    ...knowledgeDocuments.value,
    ...documents.filter((document) => {
      if (existing.has(document.uuid)) return false
      existing.add(document.uuid)
      return true
    }),
  ]
  knowledgePickerOpen.value = false
  nextTick(() => textareaRef.value?.focus())
}

function removeKnowledgeReference(uuid: string) {
  if (submitting.value) return
  knowledgeDocuments.value = knowledgeDocuments.value.filter((document) => document.uuid !== uuid)
}

async function buildKnowledgeInputs() {
  const blocks: string[] = []
  for (const document of knowledgeDocuments.value) {
    const path = await getKnowledgeDocumentPath(document.uuid)
    if (!path.exists || !path.absolute_path) {
      throw new Error(`${t('workflows.task.progress.referenceInvalid')}：${document.title}`)
    }
    const content = await loadKnowledgeReferenceContent(document.uuid, null)
    const segments = document.file_path.replace(/\\/g, '/').split('/').filter(Boolean)
    const name = knowledgeDocumentName(document)
    segments.pop()
    const folderPath = [t('knowledge.breadcrumbRoot'), ...segments].join(' / ')
    // 新任务尚无 UUID，超长文档直接引用已校验的知识库文件，避免调用任务产出落盘接口。
    blocks.push(
      countCharacters(content) > KNOWLEDGE_REFERENCE_CHAR_LIMIT
        ? `【知识库引用】${name}\n来源：${folderPath} / ${name}\n范围：整篇文档\n文件：${path.absolute_path}`
        : buildKnowledgeReferenceBlock({ name, folderPath, fragment: null, content }),
    )
  }
  return blocks
}

function triggerFileInput() {
  fileInputRef.value?.click()
}

function attachImages(files: File[]) {
  if (!files.length || submitting.value) return
  try {
    addImages(files)
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('workflows.task.attachments.uploadFailed'))
  }
}

function handleFileInputChange(e: Event) {
  const target = e.target as HTMLInputElement
  attachImages([...(target.files || [])])
  target.value = ''
}

function handlePaste(e: ClipboardEvent) {
  const files = [...(e.clipboardData?.items || [])]
    .filter((item) => item.type.startsWith('image/'))
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file))
  if (!files.length) return
  e.preventDefault()
  attachImages(files)
}

function handleDrop(e: DragEvent) {
  attachImages([...(e.dataTransfer?.files || [])])
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    if (canSubmit.value) {
      void handleSubmit()
    }
  }
}

async function handleSubmit() {
  if (!canSubmit.value) {
    if (!workDirectory.value.trim()) {
      message.warning(t('workflows.task.feedback.chooseDirectory') || '请先选择工作目录')
      return
    }
    if (!executionMode.value) {
      message.warning(t('workflows.task.newConversation.chooseExecutionMode') || '请选择执行方式')
      return
    }
    return
  }

  const promptText = question.value.trim()
  const workDir = workDirectory.value.trim()
  const mode = executionMode.value
  if (!mode) return

  submitting.value = true
  try {
    await validateVibeMode(mode)
    conversationStore.recordWorkingDir(workDir)
    try {
      localStorage.setItem(LAST_WORK_DIR_KEY, workDir)
      localStorage.setItem(LAST_EXEC_MODE_KEY, serializeExecutionMode(mode))
    } catch {
      // ignore
    }

    const knowledgeInputs = await buildKnowledgeInputs()
    const attachmentInputs = await buildDraftAttachmentInputs()
    const taskDescription = [promptText, ...knowledgeInputs, ...attachmentInputs.map((item) => item.marker)]
      .filter(Boolean)
      .join('\n\n')
    const payload: Record<string, unknown> = {
      title: promptText.slice(0, 50).replace(/[\r\n]+/g, ' '),
      description: taskDescription,
      work_dir: workDir,
      work_dirs: [workDir],
      status: 'active',
      create_notification: true,
      created_from_conversation: true,
      attachments: attachmentInputs,
    }

    if (mode.mode === 'pipeline') {
      await pipelineStore.loadPipelines()
      const pipeline = pipelineStore.pipelines.find((item) => item.uuid === mode.pipelineUuid)
      if (!pipeline) throw new Error(t('workflows.task.common.noPipeline'))
      payload.execution_mode = 'pipeline'
      payload.pipeline_uuid = pipeline.uuid
      payload.step_configs = (pipeline.steps || []).map((step) => ({
        step_uuid: step.uuid,
        source_step_id: step.source_step_id == null ? '' : String(step.source_step_id),
        cloud_step_id: step.cloud_step_id == null ? '' : String(step.cloud_step_id),
        cli_type: step.cli_type || '',
        model: step.model || '',
        model_name: step.model_name || '',
      }))
    } else if (mode.mode === 'expert_group') {
      payload.execution_mode = 'expert_group'
      payload.expert_group_uuid = mode.expertGroupUuid
    } else if (mode.mode === 'vibe_coding') {
      payload.execution_mode = 'vibe_coding'
      payload.execution_tool = mode.executionTool || 'codex'
      payload.model_name = mode.modelName
    } else if (mode.mode === 'cli') {
      payload.execution_mode = 'cli'
      payload.execution_tool = mode.executionTool
      payload.model_name = mode.modelName
    }

    const created = await apiClient.post<{ uuid: string; title: string }>('/tasks', payload)
    completeOnboarding()
    message.success(t(mode.mode === 'vibe_coding'
      ? 'workflows.task.feedback.taskCreatedAwaitingTool'
      : 'workflows.task.feedback.taskCreated'))

    if (mode.mode === 'vibe_coding') {
      const toolName = executionModeDisplayName.value
      try {
        const openResult = await openTaskVibeTool(created.uuid)
        showVibeToolOpenResult(openResult, toolName)
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('workflows.task.detail.toolUnavailable', { tool: toolName }))
      }
    }

    if (mode.mode === 'cli') {
      pendingCliKickoffUuid.value = created.uuid
    }

    question.value = ''
    knowledgeDocuments.value = []
    await conversationStore.loadConversations(true)
    void router.push({ name: 'tasks', query: { taskUuid: created.uuid } })
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('workflows.task.feedback.taskCreateFailed') || '任务创建失败')
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  void ensureCliNamesLoaded()
  restorePreferences()
  if (executionMode.value) void validateVibeMode(executionMode.value).catch(() => {
    // 提交时再次校验，短暂加载失败不会擦除原模型偏好。
  })
  nextTick(() => {
    const target = onboardingTarget.value
    if (target === 'execution' || target === 'directory') return
    textareaRef.value?.focus()
  })
})

onUnmounted(() => {
  clearAttachments()
})
</script>

<style scoped>
.new-conversation-page {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: safe center;
  min-height: 100%;
  height: 100%;
  padding: 40px 24px;
  background: #ffffff;
  overflow-y: auto;
}

.new-conversation-center {
  width: 100%;
  max-width: 800px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 32px;
}

.composer-stack {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.title-area {
  text-align: center;
}

.main-title {
  font-size: 32px;
  font-weight: 600;
  color: #17181a;
  line-height: 40px;
  letter-spacing: -1.12px;
  margin: 0;
}

.sub-title {
  font-size: 14px;
  color: #8c8c8c;
  line-height: 22px;
  margin: 8px 0 0;
}

.task-composer-card {
  width: 100%;
  background: #ffffff;
  border: 2px solid #e5e7eb;
  border-radius: 12px;
  box-shadow: 0 4px 8px rgba(0, 0, 0, 0.08);
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  transition: border-color 0.2s;
}

.task-composer-card:focus-within {
  border-color: #e5e7eb;
}

.composer-textarea {
  width: 100%;
  min-height: 100px;
  border: none;
  outline: none;
  resize: vertical;
  font-size: 14px;
  line-height: 22px;
  color: #262626;
  font-family: inherit;
  background: transparent;
}

.composer-textarea::placeholder {
  color: #8c8c8c;
}

.hidden-file-input {
  display: none;
}

.composer-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 24px;
}

.toolbar-left {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.action-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 24px;
  max-width: 100%;
  padding: 1px 6px;
  border: 0;
  border-radius: 6px;
  background: #ffffff;
  color: #595959;
  cursor: pointer;
  font-family: inherit;
  line-height: 20px;
  letter-spacing: -0.1504px;
  transition: background-color 180ms ease;
  font-size: 12px;
}

.action-btn:hover:not(:disabled) {
  background: #f2f4f7;
  color: #262626;
}

.action-btn.is-onboarding-target {
  background: #e5efff;
  border-color: #3157e2;
  color: #3157e2;
}

.task-composer-card.is-onboarding-input {
  border-color: #3157e2;
}

.task-composer-card.is-onboarding-input .composer-textarea {
  background: #f5f9ff;
  border-radius: 8px;
}

.execution-mode-button {
  max-width: 220px;
}

.work-directory-button {
  max-width: 240px;
}

.execution-mode-button .btn-text,
.work-directory-button .btn-text {
  max-width: none;
}

.action-btn .execution-mode-icon,
.action-btn .directory-icon {
  width: 16px;
  height: 16px;
  object-fit: contain;
}

.pipeline-icon {
  display: inline-flex;
  border-radius: 4px;
  background: #3157e2;
}

.pipeline-icon::before {
  content: '';
  width: 16px;
  height: 16px;
  background: #ffffff;
  mask: var(--pipeline-icon) center / 16px 16px no-repeat;
}

.btn-text {
  min-width: 0;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.toolbar-right {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
}

.send-btn {
  display: inline-flex;
  width: auto;
  min-width: 32px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 2px 4px;
  color: #ffffff;
  background: #262626;
  cursor: pointer;
  white-space: nowrap;
  transition: background-color 180ms ease;
}

.send-btn:hover:not(:disabled) {
  background: #434343;
}

.send-btn:active:not(:disabled) {
  background: #141414;
}

.send-btn:disabled {
  color: #aeb4be;
  background: #eceef2;
  cursor: not-allowed;
}

.send-btn:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.send-button-text {
  font-size: 12px;
  line-height: 20px;
  white-space: nowrap;
}

.tool-icon {
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
}

.action-btn:disabled {
  color: #bfbfbf;
  background: #ffffff;
  cursor: not-allowed;
}

.action-btn:focus-visible,
.knowledge-reference-chip button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.knowledge-reference-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.knowledge-reference-chip {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 4px;
  padding: 0 6px;
  border-radius: 999px;
  background: rgba(0, 0, 0, 0.05);
  color: #262626;
  font-size: 12px;
  font-weight: 400;
  line-height: 20px;
}

.knowledge-reference-chip img {
  flex: 0 0 12px;
}

.knowledge-reference-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.knowledge-reference-chip button {
  display: inline-flex;
  position: relative;
  flex: 0 0 12px;
  width: 12px;
  height: 20px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}

.knowledge-reference-chip button::before {
  content: '';
  position: absolute;
  inset: -2px -6px;
}

.knowledge-reference-remove {
  display: none;
}

.knowledge-reference-chip button:hover:not(:disabled) img,
.knowledge-reference-chip button:focus-visible img {
  display: none;
}

.knowledge-reference-chip button:hover:not(:disabled) .knowledge-reference-remove,
.knowledge-reference-chip button:focus-visible .knowledge-reference-remove {
  display: inline-flex;
}

.knowledge-reference-chip button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

@media (max-width: 720px) {
  .composer-toolbar {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }

  .toolbar-left,
  .toolbar-right {
    width: 100%;
  }

  .toolbar-right {
    justify-content: flex-end;
  }
}

@media (max-width: 640px) {
  .new-conversation-page {
    padding: 24px 16px;
  }
}

/* 设计稿要求当前目标以 1s 缓入缓出闪烁；减少动效时只保留静态焦点环。 */
@keyframes onboarding-pulse {
  0%,
  100% {
    box-shadow: 0 0 0 0 rgba(49, 87, 226, 0.45);
  }
  50% {
    box-shadow: 0 0 0 5px rgba(49, 87, 226, 0);
  }
}

.is-onboarding-pulse {
  animation: onboarding-pulse 1s ease-in-out infinite;
}

@media (prefers-reduced-motion: reduce) {
  .is-onboarding-pulse {
    animation: none;
    box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.35);
  }
}
</style>
