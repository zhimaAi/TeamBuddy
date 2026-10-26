<template>
  <div class="chat-composer">
	<div v-if="expertMentionOpen" class="expert-mention-menu" role="listbox" :aria-label="t('agents.expertTeam')">
	  <button v-for="(member, index) in filteredExpertMembers" :key="member.uuid" type="button" role="option" :class="{ active: index === expertMentionActiveIndex }" :aria-selected="index === expertMentionActiveIndex" @mouseenter="expertMentionActiveIndex = index" @click="selectExpertMember(member)">
		<img :src="member.avatar || agentIcon" alt="" /><span><strong>{{ member.name }}</strong><small>{{ member.member_role === 'leader' ? t('expertGroups.leader') : t('expertGroups.members', { count: 1 }) }}</small></span>
	  </button>
	</div>
    <DocumentMentionMenu
      ref="documentMentionMenuRef"
      :open="documentMentionOpen || fragmentPreviewState.open"
      :mode="fragmentPreviewState.open ? 'preview' : 'picker'"
      :query="documentMentionQuery"
      :items="documentMentionMatches"
      :status="documentMentionStatus"
      :error="documentMentionError"
      :knowledge-entry="knowledgeMentionEntry"
      :fragment="fragmentPreviewState.open ? fragmentPreviewState : null"
      @close="onDocumentMentionClose"
      @select="insertDocumentReference"
      @select-knowledge="openKnowledgePicker"
      @retry="reloadDocumentMention"
    />
    <div v-if="notice" class="composer-notice" role="status">
      <span>{{ notice }}</span>
      <a-button v-if="locateStepUuid" type="link" size="small" @click="emit('select-step', locateStepUuid)">{{ t('workflows.task.detail.locateRunningStep') }}</a-button>
    </div>
    <div
      class="composer-shell"
      :class="{ 'is-disabled': !canAsk && !running }"
    >
      <ComposerImageAttachments
        :items="attachmentItems"
        :disabled="fullyDisabled || submitting || preparingSubmission || isSavingPastedImages"
        @remove="removeAttachment($event)"
      />
      <div
        v-if="fileAttachmentItems.length"
        class="attachment-chips"
        :aria-label="t('workflows.task.progress.attachedFiles')"
      >
        <button
          v-for="attachment in fileAttachmentItems"
          :key="attachment.id"
          type="button"
          class="attachment-chip"
          :disabled="fullyDisabled || submitting || preparingSubmission || isSavingPastedImages"
          :title="t('workflows.task.progress.removeAttachment', { name: attachment.name })"
          @click="removeAttachment(attachment.id)"
        >
          <span class="attachment-chip-icon-slot">
            <PaperClipOutlined class="attachment-chip-icon" />
            <CloseOutlined class="attachment-chip-close" aria-hidden="true" />
          </span>
          <span class="attachment-chip-name">{{ attachment.name }}</span>
        </button>
      </div>
      <div
        class="composer-input"
        :class="{ 'is-disabled': !canAsk && !allowDraft, 'is-composing': isComposing }"
      >
        <div
          ref="inputLayerRef"
          class="composer-input-layer"
          aria-hidden="true"
        >
          <span
            v-for="(token, index) in mentionTokens"
            :key="index"
            :class="{
              'mention-token': token.reference,
              'mention-token-knowledge': token.isKnowledge,
              'mention-token-invalid': token.invalid,
              'mention-token-fragment': token.isFragment,
              'mention-token-clickable': token.isFragment,
            }"
            :title="token.isKnowledge ? token.label : undefined"
            @mousedown="onMentionTokenClick(token)"
          >
            <template v-if="token.reference">
              <span v-if="token.isKnowledge" class="knowledge-reference-content">
                <img :src="knowledgeReferenceIcon" alt="" aria-hidden="true" />
                <span>{{ token.label }}</span>
              </span>
              <svg
                v-else
                class="mention-token-icon"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linejoin="round"
                stroke-linecap="round"
                aria-hidden="true"
              >
                <path d="M9.4 1.9H4.5a1.1 1.1 0 0 0-1.1 1.1v10a1.1 1.1 0 0 0 1.1 1.1h7a1.1 1.1 0 0 0 1.1-1.1V5z" />
                <path d="M9.4 1.9V5h3.2" />
              </svg>
              <span class="mention-token-syntax">{{ token.syntaxPrefix }}</span>
              <span class="mention-token-label">{{ token.label }}</span>
              <span class="mention-token-syntax">{{ token.syntaxSuffix }}</span>
            </template>
            <template v-else>{{ token.text }}</template>
          </span>
        </div>
        <textarea
          ref="textareaRef"
          :value="modelValue"
          rows="3"
          :disabled="fullyDisabled || submitting || preparingSubmission || (!canAsk && !allowDraft)"
          :placeholder="effectivePlaceholder"
          :aria-label="t('workflows.task.progress.messageAria')"
          @input="handleInput"
          @click="handleComposerClick"
          @paste="handlePaste"
          @keydown="handleComposerKeydown"
          @scroll="syncInputLayer"
          @compositionstart="isComposing = true"
          @compositionend="handleCompositionEnd"
        />
      </div>
      <div class="composer-toolbar">
        <input
          ref="fileInputRef"
          class="attachment-file-input"
          type="file"
          multiple
          tabindex="-1"
          aria-hidden="true"
          @change="handleFileSelection"
        />
        <div class="composer-tools">
          <!--
            加号菜单：将「附件 / 产出文档 / 知识库」三个入口收到一起，
            按钮太多在窄窗口容易挤出换行；点击加号展开下拉菜单再选具体操作。
            只有整体不可用（fullyDisabled）时才禁用菜单触发器；
            「产出文档」依赖任务步骤上下文，没有 documentsStep 时自动隐藏；
            「知识库」「附件」不依赖 taskUuid，可以在没有任务上下文的场景下使用
            （例如基于知识库和附件创建任务）。
          -->
          <ComposerPlusMenu
            v-if="!executionTask"
            :disabled="fullyDisabled"
            :show-task-documents="Boolean(documentsStep) && !fullyDisabled"
            :show-knowledge="true"
            @attachment="fileInputRef?.click()"
            @documents="taskDocumentsOpen = true"
            @knowledge="openKnowledgePicker"
          />
          <template v-else>
            <button type="button" class="tool-button" :disabled="fullyDisabled" @click="openKnowledgePicker"><img class="tool-button-icon composer-design-icon" :src="knowledgeIcon" alt="" aria-hidden="true" />{{ t('workflows.task.progress.knowledge') }}</button>
            <button type="button" class="tool-button" :disabled="fullyDisabled" @click="fileInputRef?.click()"><img class="tool-button-icon composer-design-icon" :src="attachmentIcon" alt="" aria-hidden="true" />{{ t('workflows.task.progress.attachment') }}</button>
          </template>
          <button
            v-if="showWorkDirectory"
            type="button"
            class="tool-button work-directory-button"
            :class="{ selected: workDirectory }"
            :title="workDirectory || t('workflows.task.progress.chooseWorkDirectory')"
            :aria-label="t('workflows.task.progress.chooseWorkDirectory')"
            :disabled="fullyDisabled"
            @click="emit('select-work-directory')"
          >
            <FolderOpenOutlined class="tool-button-icon" />
            <span>{{ workDirectory || t('workflows.task.progress.chooseWorkDirectory') }}</span>
          </button>
          <GitBranchPicker
            v-if="taskUuid"
            :task-uuid="taskUuid"
            :disabled="running || submitting || preparingSubmission"
            @busy="branchChanging = $event"
          />
          <CliRuntimePicker
            v-if="runtimeEnabled && !fullyDisabled && (!executionTask || executionTask.execution_mode === 'cli')"
            v-model:open="cliPickerOpen"
            :cli-options="cliOptions"
            :cli-loading="cliLoading"
            :selected-cli-type="selectedCliType"
            :selected-model-name="selectedModelName"
            :model-options="availableModels"
            :model-loading="modelLoading"
            :model-counts="modelCounts"
            :show-execution-switch="Boolean(executionTask)"
            @switch-execution="switchExecution"
            @select-cli="handleCliChange"
            @select-model="handleModelChange"
          >
            <button
              type="button"
              class="tool-button cli-picker-trigger"
              :title="runtimeTriggerLabel"
              aria-haspopup="dialog"
              :aria-expanded="cliPickerOpen"
              :aria-label="t('workflows.task.progress.chooseRuntime')"
            >
              <img
                class="tool-button-icon cli-brand-icon"
                :class="{ 'composer-design-icon': selectedCliLogo === cliTriggerIcon }"
                :src="selectedCliLogo"
                alt=""
                aria-hidden="true"
              />
              <span>{{ runtimeTriggerLabel }}</span>
            </button>
          </CliRuntimePicker>
          <a-dropdown v-if="executionTask && executionTask.execution_mode !== 'cli'" v-model:open="executionMenuOpen" :trigger="['click']">
            <button type="button" class="tool-button execution-picker-trigger" :disabled="fullyDisabled || submitting" :title="executionLabel" :aria-label="t('workflows.task.assign.executionMode')" aria-haspopup="menu" :aria-expanded="executionMenuOpen"><TeamOutlined v-if="executionTask.execution_mode === 'expert_group'" class="tool-button-icon" /><span v-else class="tool-button-icon composer-pipeline-icon" :style="{ '--pipeline-icon': pipelineIconMask }" aria-hidden="true" /><span>{{ executionLabel }}</span></button>
            <template #overlay><a-menu :selected-keys="[currentStep?.uuid || '']">
              <a-menu-item key="switch" @click="switchExecution"><SwapOutlined class="execution-switch-icon" /> <span class="execution-switch-label">{{ t('workflows.task.detail.switchExecutionMode') }}</span></a-menu-item>
              <a-menu-item v-for="step in executionSteps" :key="step.uuid" @click="selectExecutionStep(step.uuid)"><span class="execution-step-name"><img :src="resolveAgentAvatar(step)" alt="" />{{ step.name }}</span></a-menu-item>
            </a-menu></template>
          </a-dropdown>
          <button v-if="executionTask && documentsStep" type="button" class="tool-button" :disabled="fullyDisabled" @click="taskDocumentsOpen = true"><img class="tool-button-icon composer-design-icon" :src="documentIcon" alt="" aria-hidden="true" />{{ t('workflows.task.progress.documents') }}</button>
          <span
            v-if="runtimeEnabled && !fullyDisabled && !executionTask"
            class="tool-divider"
            aria-hidden="true"
          />
          <button
            v-if="!hideAgentPrompt"
            type="button"
            class="tool-button"
            :disabled="fullyDisabled || !taskUuid"
            @click="agentPromptOpen = true"
          >
            <img
              class="tool-button-icon"
              :src="agentIcon"
              alt=""
            />{{ t('workflows.task.progress.agentPrompt') }}
          </button>
        </div>
        <div class="composer-actions">
          <button
            v-if="running"
            type="button"
            class="send-button stop-button"
            :disabled="stopping"
            :title="t('workflows.task.progress.stopTask')"
            :aria-label="t('workflows.task.progress.stopTask')"
            @click="emit('stop')"
          >
            <LoadingOutlined
              v-if="stopping"
              spin
            />
            <span
              v-else
              class="stop-square"
              aria-hidden="true"
            />
          </button>
          <button
            v-else
            type="button"
            class="send-button submit-button"
            :disabled="!canSubmit"
            :title="resolvedSubmitButtonLabel"
            @click="handleSubmit"
          >
            <LoadingOutlined
              v-if="submitting || preparingSubmission || (showAdvanceAction && advanceAction?.loading)"
              spin
            />
            <span
              v-else
              class="send-button-text"
            >
              {{ resolvedSubmitButtonLabel }}
            </span>
          </button>
        </div>
      </div>
    </div>
    <AgentPromptDrawer
      v-model:open="agentPromptOpen"
      :task-uuid="taskUuid"
      :current-step="currentStep"
      :steps="steps"
      :executing-step-uuid="executingStepUuid"
      @saved="emit('prompt-saved')"
    />
    <TaskDocumentsDrawer
      v-model:open="taskDocumentsOpen"
      :task-uuid="taskUuid"
	  :current-step="documentsStep"
      @reference="handleDrawerReference"
    />
    <!-- S-UI-19/20: 两个入口（# 菜单固定项 / 工具按钮）共用的「从资料库中选择」选择器 -->
    <KnowledgeDocumentPickerModal
      v-model:open="knowledgePickerOpen"
      :referenced-uuids="knowledgeReferencedUuids"
      @confirm="insertKnowledgeReferences"
    />
    <p v-if="invalidKnowledgeReferenceNames.length" class="composer-hint composer-hint-error">
      <span>{{ t('workflows.task.progress.referenceInvalid') }}</span>
      <span>{{ invalidKnowledgeReferenceNames.join('、') }}</span>
    </p>
    <p v-if="disabledReason" class="composer-hint">{{ disabledReason }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import {
  CloseOutlined,
  FolderOpenOutlined,
  LoadingOutlined,
  PaperClipOutlined,
  SwapOutlined,
  TeamOutlined,
} from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import agentIcon from '@/assets/icons/task-composer-agent.svg'
import knowledgeIcon from '@/assets/icons/task-composer-knowledge.svg'
import knowledgeReferenceIcon from '@/assets/icons/task-knowledge-reference.svg'
import attachmentIcon from '@/assets/icons/task-composer-attachment.svg'
import documentIcon from '@/assets/icons/task-composer-documents.svg'
import cliTriggerIcon from '@/assets/icons/task-composer-cli-trigger.svg'
import cliFallbackIcon from '@/assets/icons/task-composer-cli.svg'
import pipelineIcon from '@/assets/icons/task-composer-pipeline.svg'
import { resolveAgentAvatar } from '@/config/agentAvatars'
import type { TaskWithDetails } from '@/types/task-detail'
import { cliLogo } from '@/composables/useCliLogo'
import { useChatRuntimeDefaults } from '@/composables/useChatRuntimeDefaults'
import { useCliModelOptions } from '@/composables/useCliModelOptions'
import { useTaskImageAttachments } from '@/composables/useTaskImageAttachments'
import { readComposerDraftReferences, writeComposerDraftReferences } from '@/composables/useComposerDraft'
import {
  buildKnowledgeFolderPaths,
  buildKnowledgeReferenceBlock,
  countCharacters,
  KNOWLEDGE_REFERENCE_CHAR_LIMIT,
  loadKnowledgeReferenceContent,
  knowledgeDocumentName,
  spillKnowledgeReference,
  validateKnowledgeReference,
} from '@/composables/useKnowledgeReferences'
import { normalizeClipboardText } from '@/utils/clipboard'
import type { ChatComposerSubmission } from '@/types/task-attachments'
import type { PipelineStep } from '@/types/pipeline'
import type { KnowledgeDocumentReference, KnowledgeReferenceDraft, KnowledgeReferenceFragment } from '@/types/knowledge-reference'
import type { KnowledgeDocument } from '@/api/knowledge'
import { listKnowledgeFolders } from '@/api/knowledge'
import { useKnowledgeReferenceStore } from '@/stores/knowledge-reference'
import AgentPromptDrawer from './AgentPromptDrawer.vue'
import CliRuntimePicker from './CliRuntimePicker.vue'
import ComposerPlusMenu from './ComposerPlusMenu.vue'
import ComposerImageAttachments from './ComposerImageAttachments.vue'
import DocumentMentionMenu from './DocumentMentionMenu.vue'
import KnowledgeDocumentPickerModal from './KnowledgeDocumentPickerModal.vue'
import TaskDocumentsDrawer from './TaskDocumentsDrawer.vue'
import GitBranchPicker from './GitBranchPicker.vue'
import {
  collectMarkerRanges,
  matchDocumentMentionFiles,
  resolveDocumentMentionContext,
  resolveDocumentMentionPreview,
  splitDocumentMentionTokens,
  collectDocumentMentionBlocks,
} from './documentMention'
import type { DocumentMentionContext, DocumentMentionMarker, DocumentMentionRange } from './documentMention'
import type { MentionableTaskFile, TaskFileNode } from '@/types/task-files'
import { useTaskFilesIndex } from '@/composables/useTaskFilesIndex'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

const props = withDefaults(
  defineProps<{
    modelValue: string
    canAsk: boolean
    submitting?: boolean
    running?: boolean
    stopping?: boolean
    placeholder: string
    contextText: string
    taskUuid: string
    currentStep?: PipelineStep
	documentStep?: PipelineStep
    steps?: PipelineStep[]
    executingStepUuid?: string
    cliType?: string
    modelName?: string
    startMode?: boolean
    showWorkDirectory?: boolean
    workDirectory?: string
    fullyDisabled?: boolean
    allowDraft?: boolean
    draftKey?: string
    notice?: string
    locateStepUuid?: string
    disabledReason?: string
	expertMembers?: Array<{ uuid: string; name: string; avatar?: string; member_role?: 'leader' | 'member' | '' }>
    /** 需求 2204：看板-任务详情的 CLI 任务输入框不展示 Agent 提示词入口。 */
    hideAgentPrompt?: boolean
    submitButtonLabel?: string
    advanceAction?: { label: string; disabled: boolean; loading: boolean }
    executionTask?: TaskWithDetails
  }>(),
  {
    submitting: false,
    running: false,
    stopping: false,
    cliType: '',
    modelName: '',
    startMode: false,
    showWorkDirectory: false,
    workDirectory: '',
    fullyDisabled: false,
    disabledReason: '',
    steps: () => [],
    executingStepUuid: '',
	expertMembers: () => [],
    hideAgentPrompt: false,
    submitButtonLabel: '',
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  submit: [submission: ChatComposerSubmission]
  stop: []
  advance: []
  'prompt-saved': []
  'select-work-directory': []
  'switch-execution': []
  'select-step': [uuid: string]
}>()

const agentPromptOpen = ref(false)
const executionMenuOpen = ref(false)
const executionSteps = computed(() => [...(props.executionTask?.steps || [])].sort((a, b) => a.sort_order - b.sort_order))
const executionLabel = computed(() => props.executionTask?.execution_mode === 'expert_group' ? props.executionTask.expert_group_name_snapshot || t('agents.expertTeam') : props.executionTask?.pipeline_name_snapshot || t('workflows.task.common.pipeline'))
function switchExecution() {
  cliPickerOpen.value = false
  executionMenuOpen.value = false
  emit('switch-execution')
}
function selectExecutionStep(uuid: string) {
  executionMenuOpen.value = false
  emit('select-step', uuid)
}
const taskDocumentsOpen = ref(false)
// S-UI-19: 「从资料库中选择」选择器（# 菜单固定项与工具按钮共用）
const knowledgePickerOpen = ref(false)
const textareaRef = ref<HTMLTextAreaElement>()
const inputLayerRef = ref<HTMLElement>()
const isComposing = ref(false)
const fileInputRef = ref<HTMLInputElement>()
const documentMentionMenuRef = ref<InstanceType<typeof DocumentMentionMenu>>()
const documentMentionContext = ref<DocumentMentionContext | null>(null)
const documentMentionDismissed = ref<DocumentMentionContext | null>(null)
const {
  entry: documentMentionIndex,
  bind: bindDocumentMentionIndex,
  load: loadDocumentMentionIndex,
  invalidate: invalidateDocumentMentionIndex,
} = useTaskFilesIndex()
const expertMentionOpen = ref(false)
const expertMentionStart = ref(0)
const expertMentionEnd = ref(0)
const expertMentionQuery = ref('')
const expertMentionActiveIndex = ref(0)
const selectedExpertMember = ref<{ uuid: string; name: string; avatar?: string; member_role?: 'leader' | 'member' | '' }>()
const filteredExpertMembers = computed(() => props.expertMembers.filter((member) => member.name.toLowerCase().includes(expertMentionQuery.value.toLowerCase())))
const selectedCliType = ref('')
const selectedModelName = ref('')
const runtimeTouched = ref(false)
const cliPickerOpen = ref(false)
const preparingSubmission = ref(false)
const branchChanging = ref(false)
/*
 * 已插入过的引用登记，只追加、不剪枝：标记从正文里消失（删除、清空重来）时
 * 登记先留着，撤销把标记原样放回来就能立刻恢复成胶囊、提交时也还能换成路径。
 * 不在正文里的登记本身无害 —— 渲染区间按正文匹配，序列化也是按标记替换。
 */
const composerDraftIdentity = computed(() => props.draftKey || props.taskUuid)
const documentReferences = ref<DocumentReference[]>([])
// 引用的归属独立于正在更新的 Props，避免会话切换时写入目标会话。
let documentReferencesDraftKey = composerDraftIdentity.value
// S-UI-13: 选中片段预览状态（点击输入框里的片段胶囊触发，DocumentMentionMenu 用 'preview' 模式展示）
const fragmentPreviewState = reactive({
  open: false,
  marker: '',
  name: '',
  fragmentText: '',
})
// S-UI-13: 知识库页「添加给 Agent」投递过来的待发引用（跨页队列，绑定任务后消费）
const knowledgeReferenceStore = useKnowledgeReferenceStore()
// 知识库文件夹「位置」路径：引用来源需要，首次用到时按 S-IN-11 复用既有 /knowledge/folders 加载一次
const knowledgeFolderPaths = ref<Map<number, string>>(new Map())
let knowledgeFolderPathsPending: Promise<Map<number, string>> | null = null
const {
  items: attachmentItems,
  remove: removeAttachment,
  clear: clearImageAttachments,
  isSaving: isSavingPastedImages,
  addFiles,
  addImages,
  uploadAttachmentsToTask,
} = useTaskImageAttachments(composerDraftIdentity)
const {
  cliLoading,
  cliOptions,
  loadCliOptions,
  loadModelOptions,
  modelCounts,
  modelLoading,
  modelOptions,
  prefetchModelCounts,
  resetModelOptions,
} = useCliModelOptions()
const { getChatRuntimeSelection, saveChatRuntimeSelection } = useChatRuntimeDefaults()
const runtimeEnabled = computed(() => Boolean(props.taskUuid && (props.currentStep || props.executionTask?.execution_mode === 'cli')))
const documentsStep = computed(() => props.documentStep || props.currentStep)
const showAdvanceAction = computed(() => Boolean(props.advanceAction) && !props.modelValue.trim() && !hasAttachments.value)
const resolvedSubmitButtonLabel = computed(
  () =>
    (showAdvanceAction.value ? props.advanceAction?.label : '') ||
    props.submitButtonLabel ||
    (props.startMode && !props.modelValue.trim()
      ? t('workflows.task.progress.begin')
      : t('workflows.task.progress.send')),
)
const runtimeIdentity = computed(() => `${props.taskUuid}:${props.currentStep?.uuid || ''}`)
const availableModels = computed(() => {
  const result = [...modelOptions.value]
  if (selectedModelName.value && !result.includes(selectedModelName.value)) {
    result.unshift(selectedModelName.value)
  }
  return result
})
const fileAttachmentItems = computed(() => attachmentItems.value.filter((item) => !item.isImage))
/** 附件只在上方附件行里，正文可能为空，所以「只有附件」也算可发送 */
const hasAttachments = computed(() => attachmentItems.value.length > 0)
const canSubmit = computed(
  () =>
    props.canAsk &&
    !props.fullyDisabled &&
    !props.running &&
    !props.submitting &&
    !branchChanging.value &&
    !preparingSubmission.value &&
    !isSavingPastedImages.value &&
    (showAdvanceAction.value
      ? !props.advanceAction?.disabled && !props.advanceAction?.loading
      : (Boolean(props.modelValue.trim()) || hasAttachments.value || props.startMode) &&
        (!runtimeEnabled.value || Boolean(selectedCliType.value && selectedModelName.value))),
)
const selectedCliLabel = computed(
  () =>
    cliOptions.value.find((item) => item.type === selectedCliType.value)?.name ||
    selectedCliType.value,
)
const pipelineIconMask = `url("${pipelineIcon}")`
const selectedCliLogo = computed(() => {
  const logo = cliLogo(selectedCliType.value)
  return logo === cliFallbackIcon ? cliTriggerIcon : logo
})
const runtimeTriggerLabel = computed(() => {
  if (selectedCliType.value && selectedModelName.value) {
    return `${selectedCliLabel.value} / ${selectedModelName.value}`
  }
  if (selectedCliType.value) return selectedCliLabel.value
  return t('workflows.task.progress.chooseCli')
})
/**
 * # 引用只需要任务上下文（文件列表接口就是 /tasks/:uuid/files）。
 * 不能复用 runtimeEnabled：它依赖 currentStep，而 CLI 直接执行与专家团这两种模式
 * 传进来的是 documentStep 而非 currentStep，会让引用菜单在这类任务里永远打不开。
 */
const documentMentionAvailable = computed(() => Boolean(props.taskUuid))
const documentMentionQuery = computed(() => documentMentionContext.value?.query ?? '')
/**
 * 在原有 placeholder 末尾补一句 # 引用提示，仅在有任务上下文时生效。
 * 不改各父组件的 i18n 词条——新会话等无 taskUuid 的场景不会出现 # 菜单，
 * 也不该看到这条提示。
 */
const effectivePlaceholder = computed(() => {
  if (!documentMentionAvailable.value) return props.placeholder
  const hint = t('workflows.task.progress.hashMentionHint')
  return props.placeholder ? `${props.placeholder}  ${hint}` : hint
})
const documentMentionStatus = computed(() => documentMentionIndex.value.status)
const documentMentionError = computed(() => documentMentionIndex.value.error)
const documentMentionMatches = computed(() =>
  documentMentionContext.value
    ? matchDocumentMentionFiles(
        documentMentionIndex.value.files,
        documentMentionContext.value.query,
      )
    : [],
)
/** S-UI-18: `#` 菜单里置顶的「知识库」固定项；无任务上下文时不展示（CX-5） */
const knowledgeMentionEntry = computed(() =>
  documentMentionAvailable.value ? { label: t('workflows.task.progress.knowledge') } : null,
)
/**
 * 输入框渲染层的分段：已登记的引用标记渲染成标签块，其余按原文本显示。
 * 末尾补一个换行，避免文本以换行结尾时渲染层比 textarea 少一行而错位。
 * S-UI-22：失效的知识库引用额外标记，胶囊呈现为失效态。
 */
const mentionTokens = computed(() => {
  const invalidMarkers = new Set(
    documentReferences.value
      .filter((reference) => reference.kind === 'knowledge' && reference.knowledge.invalid)
      .map((reference) => reference.marker),
  )
  const knowledgeMarkers = new Set(
    documentReferences.value
      .filter((reference) => reference.kind === 'knowledge')
      .map((reference) => reference.marker),
  )
  return [
    ...splitDocumentMentionTokens(
      props.modelValue,
      documentReferences.value.map((reference) => ({
        marker: reference.marker,
        name: reference.name,
        // 知识库选中片段 → 渲染层走「· 选」标记；整篇 / 任务产出文件无 fragment → 视为整篇
        isFragment:
          reference.kind === 'knowledge' && Boolean(reference.knowledge.fragment),
        // S-UI-13：悬浮 tooltip 展示选中内容
        fragmentText:
          reference.kind === 'knowledge' ? reference.knowledge.fragment?.text : undefined,
      })),
    ).map((token) => ({
      ...token,
      isKnowledge: token.reference && knowledgeMarkers.has(token.text),
      invalid: token.reference && invalidMarkers.has(token.text),
    })),
    { text: '\n', reference: false, isKnowledge: false, invalid: false },
  ]
})

/**
 * 已登记引用标记在正文里的区间。内联胶囊在视觉上是一个整体，
 * 删除、光标移动都按这个区间整块处理，而不是让用户逐字符退格。
 */
const documentReferenceRanges = computed(() =>
  collectMarkerRanges(
    props.modelValue,
    documentReferences.value.map((reference) => reference.marker),
  ),
)

/**
 * 手输 / 粘贴时能命中并自动登记的「可选文件」候选池。
 *
 * 与 documentReferences 的差别：
 * - documentReferences：当前正文里已经登记的引用，是渲染胶囊的唯一依据。
 * - mentionCandidateMap：手输 `#[name]` 时能查到「这一类文件可能可被引用」的来源，
 *   用于把恰好写对的 #[xxx] 自动补登到 documentReferences。
 *
 * 与正文无关：删除正文里的 marker 不会清空候选池；候选池只随数据源更新而变化。
 *
 * 多源汇聚（按 name 去重，重复时优先用更早出现的源）：
 * 1. 任务产出文件（documentMentionIndex.files）：`/tasks/{uuid}/files` 接口返回的
 *    全量可引用文件，是用户手输引用最常见的命中目标。
 * 2. 知识库已登记引用（documentReferences 中 kind='knowledge' 的项）：覆盖
 *    知识库选择器 / 投递草稿已落地的文档，复制粘贴已渲染好的 #[xxx] 也能命中。
 *
 * 为什么不包含附件：附件在文档中以 `![](attachments/...)` 的 Markdown 形式存在，
 * 不走 #[xxx] 引用体系；手输 `#[image.png]` 命中后没有对应的渲染分支，会造成
 * 「显示像引用、提交时无法替换」的旧问题。如果未来要支持「手输文件名引用附件」，
 * 再扩展 kind='attachment' 渲染分支并把 attachmentItems 接入候选池。
 */
interface MentionCandidate {
  kind: 'task-file' | 'knowledge'
  name: string
  /** task-file 的本地绝对路径；提交时替换 marker 使用 */
  absolutePath?: string
  /** 知识库引用对应的文档 UUID；提交时按 UUID 取文档全文 / 选中片段 */
  uuid?: string
}

const mentionCandidateMap = computed<Map<string, MentionCandidate>>(() => {
  const map = new Map<string, MentionCandidate>()
  // 1. 任务产出文件：候选池的主要来源
  for (const file of documentMentionIndex.value.files) {
    if (map.has(file.name)) continue
    map.set(file.name, {
      kind: 'task-file',
      name: file.name,
      absolutePath: file.absolute_path,
    })
  }
  // 2. 知识库已登记引用：覆盖"复制粘贴已渲染的 #[xxx]"场景；首次手输仍需走选择器
  for (const reference of documentReferences.value) {
    if (reference.kind !== 'knowledge') continue
    if (map.has(reference.name)) continue
    map.set(reference.name, {
      kind: 'knowledge',
      name: reference.name,
      uuid: reference.knowledge.uuid,
    })
  }
  return map
})

/**
 * S-UI-18 / CF-6：只要存在 # 上下文且有任务上下文就打开——「知识库」是固定项，
 * 与文件候选是否命中无关；候选为空、索引加载中或加载失败时菜单也要能出现
 * （加载态与失败态由菜单内部呈现）。
 */
const documentMentionOpen = computed(
  () => Boolean(documentMentionContext.value) && documentMentionAvailable.value,
)
let runtimeRequestId = 0
let documentReferenceSequence = 0
const COMPOSER_MIN_HEIGHT = 66
const COMPOSER_MAX_HEIGHT = 212

function handleInput(event: Event) {
  const textarea = event.target as HTMLTextAreaElement
	if (selectedExpertMember.value && !textarea.value.includes(`@${selectedExpertMember.value.name}`)) {
	  selectedExpertMember.value = undefined
	}
  emit('update:modelValue', textarea.value)
	updateExpertMention(textarea)
  syncDocumentMention(textarea)
  resizeTextarea()
}

function updateExpertMention(textarea: HTMLTextAreaElement) {
  if (!props.expertMembers.length) { expertMentionOpen.value = false; return }
	if (selectedExpertMember.value && textarea.value.includes(`@${selectedExpertMember.value.name}`)) {
	  expertMentionOpen.value = false
	  return
	}
  const caret = textarea.selectionStart ?? textarea.value.length
  const before = textarea.value.slice(0, caret)
  const match = before.match(/(?:^|\s)@([^\s@]*)$/)
  if (!match) { expertMentionOpen.value = false; return }
  expertMentionStart.value = caret - match[1].length - 1
  expertMentionEnd.value = caret
  expertMentionQuery.value = match[1]
	 expertMentionActiveIndex.value = 0
  expertMentionOpen.value = true
}

function selectExpertMember(member: { uuid: string; name: string; avatar?: string; member_role?: 'leader' | 'member' | '' }) {
  const value = textareaRef.value?.value ?? props.modelValue
  const marker = `@${member.name} `
  const nextValue = value.slice(0, expertMentionStart.value) + marker + value.slice(expertMentionEnd.value)
  selectedExpertMember.value = member
  expertMentionOpen.value = false
  emit('update:modelValue', nextValue)
  nextTick(() => {
    const caret = expertMentionStart.value + marker.length
    textareaRef.value?.focus()
    textareaRef.value?.setSelectionRange(caret, caret)
  })
}

/**
 * 已登记的引用。两种来源共用同一套标记（`#[名称]`）与胶囊渲染，
 * 但提交语义不同：产出文件替换为绝对路径（现状不变），知识库引用替换为引用块（S-UI-22）。
 */
interface TaskFileDocumentReference {
  id: string
  marker: string
  name: string
  kind: 'task-file'
  absolutePath: string
}

type DocumentReference = TaskFileDocumentReference | KnowledgeDocumentReference

/** 已在输入框里引用过的知识库文档（选择器据此标注「已引用」并禁用重复勾选） */
const knowledgeReferencedUuids = computed(() =>
  documentReferences.value
    .filter((reference) => reference.kind === 'knowledge')
    .map((reference) => reference.knowledge.uuid),
)

/** S-UI-22: 失效引用必须可见，不静默丢弃 */
const invalidKnowledgeReferenceNames = computed(() =>
  documentReferences.value
    .filter((reference) => reference.kind === 'knowledge' && reference.knowledge.invalid)
    .map((reference) => reference.name),
)

/**
 * S-UI-13: 选中片段标记的「· 选」后缀文案。
 * 同步写入 marker 文本与渲染层标签，因此必须放在 setup 作用域内通过 computed 取值；
 * 切换语言时 marker 不会自动重建，但现有引用已经带的是历史语言的角标，
 * 重新插入 / 失效校验刷新后会自然走新语言，属于可接受行为。
 */
const fragmentBadge = computed(() => t('workflows.task.progress.referenceFragmentBadge'))

function resizeTextarea() {
  const textarea = textareaRef.value
  if (!textarea) return

  textarea.style.height = `${COMPOSER_MIN_HEIGHT}px`
  const height = Math.min(Math.max(textarea.scrollHeight, COMPOSER_MIN_HEIGHT), COMPOSER_MAX_HEIGHT)
  textarea.style.height = `${height}px`
  textarea.style.overflowY = textarea.scrollHeight > COMPOSER_MAX_HEIGHT ? 'auto' : 'hidden'
  syncInputLayer()
}

/**
 * 点中胶囊时立即开 fragment preview 的兜底入口。
 *
 * 现在挂在 @mousedown 上，没有 .prevent：让 textarea 正常拿到 mousedown 的默认行为
 * （focus + caret 设置），避免过去 .prevent 让 caret 在视觉上「消失」。
 *
 * 主路径仍交给 syncFragmentPreview（基于 caret 的 selectionchange 同步）：
 * - 点中胶囊：onMentionTokenClick 立即开 preview → 同一帧 click → selectionchange
 *   → caret 落进 marker 区间 → sync 命中并保持开。
 * - 点中 capsule 外的位置：handleDocumentMousedown 命中 tokenEl，跳过同步；click 再把
 *   caret 挪到 marker 外 → sync 不命中 → preview 关闭。
 *
 * 数据源统一走 documentReferences，不再走 token.fragmentText 那条之前丢字段的链路。
 */
function onMentionTokenClick(token: {
  reference?: boolean
  text?: string
}) {
  // 兜底路径（与 selectionchange 主路径并存）：这里只打开 preview，不修改正文档、不动 selection，
  // 把"点中哪个 marker"的语义直接落到 fragmentPreviewState。
  if (!token.reference) return
  if (!token.text) return
  const reference = documentReferences.value.find((ref) => ref.marker === token.text)
  if (!reference || reference.kind !== 'knowledge' || !reference.knowledge.fragment) return
  // 同步设置 preview 状态；下一步 click + selectionchange 会再次调用 syncFragmentPreview，
  // 若 caret 仍落在这个 marker 内则保持开，否则会被 sync 关掉——这是正确的最终态。
  fragmentPreviewState.open = true
  fragmentPreviewState.marker = reference.marker
  fragmentPreviewState.fragmentText = reference.knowledge.fragment.text ?? ''
  fragmentPreviewState.name = reference.name
}

/**
 * 片段预览的单一开关决策点：所有会让 caret 变化的事件（click、keydown、input、paste、
 * 程序设置 selection）汇集到 selectionchange，最终都调用这一个函数判断 preview
 * 是开还是关。
 *
 * 决策规则：
 * 1. picker 开着时强制关 preview（互斥），避免 picker 被 preview 遮挡。
 * 2. caret 落在某个已登记的 #[...] 区间内时开 preview；
 *    区间外则关，确保点击/方向键/退格等所有路径都能立刻关闭。
 * 3. 用户点 marker 之外的位置时立刻关，不再依赖 mousedown 路径。
 *
 * 数据源统一走 documentReferences，避免之前 token.fragmentText 在渲染层链路里丢字段的问题。
 */
function syncFragmentPreview() {
  // 互斥：picker 开着就强制关 preview，让位给 picker
  if (documentMentionContext.value) {
    if (fragmentPreviewState.open) closeFragmentPreview()
    return
  }
  const textarea = textareaRef.value
  if (!textarea) {
    if (fragmentPreviewState.open) closeFragmentPreview()
    return
  }
  const selectionStart = textarea.selectionStart ?? 0
  const selectionEnd = textarea.selectionEnd ?? selectionStart
  const markers: DocumentMentionMarker[] = documentReferences.value.map((entry) => ({
    marker: entry.marker,
    name: entry.name,
    isFragment: entry.kind === 'knowledge' && Boolean(entry.knowledge.fragment),
    fragmentText:
      entry.kind === 'knowledge' ? entry.knowledge.fragment?.text : undefined,
  }))
  // 用区间交集判断：单点 caret 落在 marker 内，以及整段 marker 被选中，两种情形都命中
  const matched = resolveDocumentMentionPreview(
    textarea.value,
    selectionStart,
    selectionEnd,
    markers,
  )
  if (!matched) {
    if (fragmentPreviewState.open) closeFragmentPreview()
    return
  }
  const reference = documentReferences.value.find(
    (ref) => ref.marker === matched.marker.marker,
  )
  if (!reference || reference.kind !== 'knowledge' || !reference.knowledge.fragment) {
    if (fragmentPreviewState.open) closeFragmentPreview()
    return
  }
  // 命中同一个 marker：不重复写状态；命中不同 marker：刷新为最新内容
  if (
    fragmentPreviewState.open &&
    fragmentPreviewState.marker === reference.marker
  ) {
    return
  }
  fragmentPreviewState.open = true
  fragmentPreviewState.marker = reference.marker
  // 即便 fragment.text 为空也允许开预览：弹窗内部自行处理空片段的展示。
  fragmentPreviewState.fragmentText = reference.knowledge.fragment.text ?? ''
  fragmentPreviewState.name = reference.name
}

function closeFragmentPreview() {
  fragmentPreviewState.open = false
  fragmentPreviewState.marker = ''
  fragmentPreviewState.fragmentText = ''
  fragmentPreviewState.name = ''
}

function syncInputLayer() {
  const textarea = textareaRef.value
  const layer = inputLayerRef.value
  if (!textarea || !layer) return
  layer.scrollTop = textarea.scrollTop
  layer.scrollLeft = textarea.scrollLeft
}

function handleCompositionEnd() {
  isComposing.value = false
  // 组合输入结束后文本与行数都可能变化，下一帧再对齐一次
  nextTick(syncInputLayer)
}

/**
 * 内联引用胶囊的「整体」交互。
 *
 * 视觉上它是一颗胶囊，所以操作也按一整块处理：
 * - 点进胶囊内部 → 整颗标记被选中，用户直接看到它是一块
 * - 光标紧贴胶囊右侧按退格 / 紧贴左侧按删除键 → 整块删掉
 * - 光标落在胶囊内部、或选区与胶囊相交 → 先把范围扩到整颗胶囊再删
 * - 左右方向键整块跨过胶囊，不会停在中间
 *
 * 判定区间复用 documentReferenceRanges，和渲染层是同一份数据，
 * 不会出现「看着是胶囊、删掉的却是别的字符」。
 */
function markerRangeAt(caret: number): DocumentMentionRange | undefined {
  return documentReferenceRanges.value.find((range) => range.start < caret && caret < range.end)
}

function resolveMarkerDeletion(
  selectionStart: number,
  selectionEnd: number,
  key: 'Backspace' | 'Delete',
): DocumentMentionRange | null {
  const ranges = documentReferenceRanges.value
  if (!ranges.length) return null

  if (selectionStart !== selectionEnd) {
    // 只有真正压到胶囊才算命中；紧挨着不算，避免误删旁边的普通文字
    const touched = ranges.filter(
      (range) => range.start < selectionEnd && range.end > selectionStart,
    )
    if (!touched.length) return null
    return {
      start: Math.min(selectionStart, touched[0].start),
      end: Math.max(selectionEnd, touched[touched.length - 1].end),
    }
  }

  const inside = markerRangeAt(selectionStart)
  if (inside) return { ...inside }
  const adjacent = ranges.find((range) =>
    key === 'Backspace' ? range.end === selectionStart : range.start === selectionStart,
  )
  return adjacent ? { ...adjacent } : null
}

function handleMarkerDeletion(event: KeyboardEvent): boolean {
  if (event.key !== 'Backspace' && event.key !== 'Delete') return false
  const textarea = textareaRef.value
  if (!textarea) return false
  const target = resolveMarkerDeletion(
    textarea.selectionStart ?? 0,
    textarea.selectionEnd ?? 0,
    event.key,
  )
  if (!target) return false

  event.preventDefault()
  textarea.setSelectionRange(target.start, target.end)
  /*
   * 走原生编辑命令、而不是自己拼新值再 emit：只有原生编辑才会进撤销栈，
   * 否则删掉胶囊之后 Cmd+Z 撤不回来（普通输入的撤销是好的，别把它弄丢）。
   * 命令触发的原生 input 事件会走 handleInput，正文与引用登记随之更新。
   */
  if (document.execCommand('delete')) {
    nextTick(syncInputLayer)
    return true
  }

  // execCommand 不可用时退回手工替换：功能正确，但这一步不会留下撤销记录
  const nextValue = textarea.value.slice(0, target.start) + textarea.value.slice(target.end)
  emit('update:modelValue', nextValue)
  nextTick(() => {
    const updated = textareaRef.value
    if (!updated) return
    updated.focus()
    updated.setSelectionRange(target.start, target.start)
    resizeTextarea()
  })
  return true
}

/** 左右方向键整块跨过胶囊；带修饰键时交回浏览器（选区扩展等） */
function handleMarkerCaretMove(event: KeyboardEvent): boolean {
  if (event.shiftKey || event.altKey || event.metaKey || event.ctrlKey) return false
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return false
  const textarea = textareaRef.value
  if (!textarea) return false
  const caret = textarea.selectionStart ?? 0
  if (caret !== (textarea.selectionEnd ?? caret)) return false
  const range =
    event.key === 'ArrowLeft'
      ? documentReferenceRanges.value.find((item) => item.end === caret)
      : documentReferenceRanges.value.find((item) => item.start === caret)
  if (!range) return false
  event.preventDefault()
  const next = event.key === 'ArrowLeft' ? range.start : range.end
  textarea.setSelectionRange(next, next)
  return true
}

/** 点进胶囊内部时整块选中，让「它是一个整体」这件事直接被看到 */
function selectMarkerAtCaret() {
  const textarea = textareaRef.value
  if (!textarea) return
  const caret = textarea.selectionStart ?? 0
  if (caret !== (textarea.selectionEnd ?? caret)) return
  const range = markerRangeAt(caret)
  if (!range) return
  textarea.setSelectionRange(range.start, range.end)
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.isComposing) return
	if (expertMentionOpen.value) {
	  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
		event.preventDefault()
		const count = filteredExpertMembers.value.length
		if (count) expertMentionActiveIndex.value = (expertMentionActiveIndex.value + (event.key === 'ArrowDown' ? 1 : -1) + count) % count
		return
	  }
	  if (event.key === 'Escape') {
		event.preventDefault()
		expertMentionOpen.value = false
		return
	  }
	  const activeMember = filteredExpertMembers.value[expertMentionActiveIndex.value]
	  if (event.key === 'Enter' && activeMember) {
		event.preventDefault()
		selectExpertMember(activeMember)
		return
	  }
	}
  if (documentMentionMenuRef.value?.handleKeydown(event)) return
  if (handleMarkerCaretMove(event)) return
  if (handleMarkerDeletion(event)) return
  if (event.key !== 'Enter') return
  const plainEnter = !event.shiftKey && !event.altKey && !event.metaKey && !event.ctrlKey
  if (!plainEnter && !event.ctrlKey) return
  event.preventDefault()
  handleSubmit()
}

function syncDocumentMention(textarea: HTMLTextAreaElement) {
  if (!documentMentionAvailable.value) {
    closeDocumentMention()
    return
  }
  const context = resolveDocumentMentionContext(
    textarea.value,
    textarea.selectionStart ?? textarea.value.length,
  )
  if (!context) {
    closeDocumentMention()
    return
  }
  const dismissed = documentMentionDismissed.value
  // 仅抑制尚未变化的同一次引用，避免刚关掉又被点击事件重新顶开
  if (dismissed && dismissed.start === context.start && dismissed.query === context.query) {
    documentMentionContext.value = null
    return
  }
  documentMentionDismissed.value = null
  documentMentionContext.value = context
}

function refreshDocumentMention(event: MouseEvent) {
  syncDocumentMention(event.target as HTMLTextAreaElement)
  // 点击输入区时按 TTL 静默校准列表，任务产出新文件后不必重开会话
  void loadDocumentMentionIndex()
}

/**
 * 把正文中「恰好写对」的 #[xxx] 块自动登记到引用表。
 *
 * 渲染层（splitDocumentMentionTokens）只把 documentReferences 里登记过的 marker
 * 当成标签块。手输、粘贴、外部 v-model 注入的 #[xxx] 在被登记之前都只能以纯文本显示，
 * 这会让用户误以为没引用成功、或不得不每次都走候选菜单。
 *
 * 对账策略：
 * - 数据源统一走 mentionCandidateMap（产出文件 + 知识库已登记），与 documentReferences 解耦。
 * - 跳过 documentReferences 已登记的 marker（候选菜单、工具按钮、知识库投递已经覆盖这条路径）。
 * - 从块文本里剥掉去重序号后缀（`· N`），按 name 在候选池里精确查找命中项。
 * - 仅 kind='task-file' 的命中项按 buildDocumentMarker 的去重规则补登；kind='knowledge'
 *   的命中项已存在于 documentReferences（粘贴时 documentReferences 里已有对应登记），
 *   跳过以避免重复。
 * - 不动正文档、不改写光标，避免输入抖动；marker 文本与生成结果不一致就跳过登记。
 * - 命中不到（拼写错误、文件已删、首次手输未走选择器的知识库文档）按用户意图保持原文本。
 */
function reconcileDocumentReferences(value: string) {
  if (!documentMentionAvailable.value) return
  const candidates = mentionCandidateMap.value
  if (!candidates.size) return
  const blocks = collectDocumentMentionBlocks(value)
  if (!blocks.length) return
  // 已登记 marker 集合：候选菜单路径已先 push 再 insertMarkerText，本轮不会再登记同一项；
  // 加上本轮内的 push，避免同一段文本在两次 handleInput 间被重复登记。
  const registeredMarkers = new Set(
    documentReferences.value.map((reference) => reference.marker),
  )
  for (const block of blocks) {
    const marker = value.slice(block.start, block.end)
    if (!marker || registeredMarkers.has(marker)) continue
    // 把 #[label] 剥成 label，再去掉去重序号后缀（任务文件 marker 最多带"· N"）。
    const label = marker.startsWith('#[') && marker.endsWith(']')
      ? marker.slice(2, -1)
      : marker
    const name = label.replace(/\s·\s\d+$/, '').trim()
    if (!name) continue
    const candidate = candidates.get(name)
    if (!candidate) continue
    // knowledge 命中项的 marker 必已登记在 documentReferences（候选池来自它），
    // 上面的 registeredMarkers 检查已覆盖；这里再显式跳过 task-file 之外的 kind
    if (candidate.kind !== 'task-file') continue
    // 与候选菜单共用 buildDocumentMarker：fragment=null 表示任务产出文件，按 task-file 去重
    const registered = buildDocumentMarker(name, null, fragmentBadge.value)
    // marker 文本必须与生成结果完全一致才登记，避免悄悄改写用户已写的 marker
    if (registered !== marker) continue
    documentReferences.value.push({
      id: crypto.randomUUID(),
      marker: registered,
      name,
      kind: 'task-file',
      absolutePath: candidate.absolutePath || '',
    })
    registeredMarkers.add(registered)
  }
}

function handleComposerClick(event: MouseEvent) {
  refreshDocumentMention(event)
  // 先算引用上下文再整块选中：点进胶囊内部时菜单本来也不该弹（查询词以 [ 开头）
  selectMarkerAtCaret()
  // preview 的开关统一交给 syncFragmentPreview：
  // click 之后 selectionchange 会带着最新的 caret 再调一次同步，
  // 这里不再手动触发，避免出现"click 开、mousedown 关"的来回闪烁。
}

/** 用户主动收起：记住当前上下文，改词之前不再自动弹出 */
function dismissDocumentMention() {
  const context = documentMentionContext.value
  documentMentionDismissed.value = context ? { ...context } : null
  documentMentionContext.value = null
}

/**
 * DocumentMentionMenu 关闭事件统一入口：
 * - 预览模式：清空 fragmentPreviewState
 * - 选文件/选知识库：触发的是 picker 自身流程，不会走到这里；
 *   主动关闭走 dismissDocumentMention 保留用户的"不再弹出"意图。
 */
function onDocumentMentionClose() {
  if (fragmentPreviewState.open) {
    closeFragmentPreview()
    return
  }
  dismissDocumentMention()
}

/** 选中引用、提交消息或切换会话时的程序性关闭，不抑制后续弹出 */
function closeDocumentMention() {
  documentMentionDismissed.value = null
  documentMentionContext.value = null
}

function reloadDocumentMention() {
  void loadDocumentMentionIndex(true)
}

/** S-UI-13: 知识库选中片段 vs 整篇文档的标记差异：
 * 选中片段在标签后追加「· 选」肉眼可见地与整篇区分；
 * 整篇与选中片段各自单独计数去重序号，互不干扰。
 * 标记文本是 textarea 文本与渲染层共用的字面量，加字符只会让两者等宽同步变宽，
 * 不会破坏渲染层与 textarea 的逐字对齐约束。
 */
function buildDocumentMarker(
  name: string,
  fragment: KnowledgeReferenceFragment | null,
  fragmentBadge: string,
) {
  const hasFragment = Boolean(fragment)
  const duplicateCount = documentReferences.value.filter(
    (reference) =>
      reference.name === name &&
      (reference.kind !== 'knowledge' ||
        Boolean(reference.knowledge.fragment) === hasFragment),
  ).length
  const label = hasFragment ? `${name} · ${fragmentBadge}` : name
  return duplicateCount ? `#[${label} · ${++documentReferenceSequence}]` : `#[${label}]`
}

/**
 * 把标记文本插入到指定区间，并补足前后的空格分隔。
 *
 * 优先走原生插入命令。直接改 value（emit 新值让 Vue 回写）会清空原生撤销栈，
 * 用户刚插进来的引用就没法 Cmd+Z 撤掉，连插入之前的输入历史也会一起丢掉。
 * 命令触发的原生 input 事件会走 handleInput，正文与渲染层随之更新。
 */
function insertMarkerText(marker: string, start: number, end: number) {
  const textarea = textareaRef.value
  const value = textarea?.value ?? props.modelValue
  const insertStart = Math.min(start, value.length)
  const insertEnd = Math.min(Math.max(end, insertStart), value.length)
  const before = value.slice(0, insertStart)
  const after = value.slice(insertEnd)
  const leadingSpace = before && !/\s$/.test(before) ? ' ' : ''
  const trailingSpace = after && !/^\s/.test(after) ? ' ' : ''
  const inserted = `${leadingSpace}${marker}${trailingSpace}`
  const caret = insertStart + inserted.length

  let applied = false
  if (textarea) {
    textarea.focus()
    textarea.setSelectionRange(insertStart, insertEnd)
    applied = document.execCommand('insertText', false, inserted)
  }
  if (!applied) {
    // execCommand 不可用时退回手工替换：功能正确，但这一步不会留下撤销记录
    const nextValue = `${before}${inserted}${after}`
    emit('update:modelValue', nextValue)
  }
  nextTick(() => {
    textareaRef.value?.focus()
    textareaRef.value?.setSelectionRange(caret, caret)
    resizeTextarea()
  })
}

/** 在指定位置插入 #[名称] 引用标记（Agent 产出文件，行为与原实现逐项一致） */
function insertDocumentMarker(file: MentionableTaskFile, context: DocumentMentionContext) {
  const name = file.name || basenameFromPath(file.absolute_path)
  const marker = buildDocumentMarker(name, null, fragmentBadge.value)
  // 先登记引用再落笔：登记表只追加，插入过程中的 input 事件不会把它冲掉
  documentReferences.value.push({
    id: crypto.randomUUID(),
    marker,
    name,
    kind: 'task-file',
    absolutePath: file.absolute_path,
  })
  closeDocumentMention()
  insertMarkerText(marker, context.start, context.end)
}

function insertDocumentReference(file: MentionableTaskFile) {
  const context = documentMentionContext.value
  if (!context) return
  insertDocumentMarker(file, context)
}

/** 按需加载一次文件夹路径表；失败时退化为默认文件夹，不阻断引用（位置仅用于提示来源） */
async function loadKnowledgeFolderPaths() {
  if (knowledgeFolderPaths.value.size) return knowledgeFolderPaths.value
  if (!knowledgeFolderPathsPending) {
    knowledgeFolderPathsPending = listKnowledgeFolders()
      .then((result) => buildKnowledgeFolderPaths(result.data || []))
      .catch(() => new Map<number, string>())
      .then((paths) => {
        knowledgeFolderPaths.value = paths
        knowledgeFolderPathsPending = null
        return paths
      })
  }
  return knowledgeFolderPathsPending
}

/** 引用来源路径：根为 teamsboard；folder_id = 0（默认文件夹）直接取默认文件夹名 */
function knowledgeFolderPath(folderID: number) {
  // S-IN-12：folder_id = 0 或 map 中查不到时 folder 为空字符串，拼接时跳过空段
  // 避免出现「知识库 /  / 文件名」的痕迹；map 自身已按 S-IN-11 跳过空 name 节点。
  const folder = folderID ? knowledgeFolderPaths.value.get(folderID) || '' : ''
  const segments = [t('knowledge.breadcrumbRoot'), folder].filter((segment) => segment.trim())
  return segments.join(' / ')
}

/** S-UI-19/20：选择器确认后把选中文档逐个登记为知识库引用并插入标记 */
async function insertKnowledgeReferences(documents: KnowledgeDocument[]) {
  if (!documents.length) return
  // 位置路径用于引用来源，取不到时退化为默认文件夹，不阻断插入
  await loadKnowledgeFolderPaths()
  const markers: string[] = []
  for (const document of documents) {
    const name = knowledgeDocumentName(document)
    const marker = buildDocumentMarker(name, null, fragmentBadge.value)
    documentReferences.value.push({
      id: crypto.randomUUID(),
      marker,
      name,
      kind: 'knowledge',
      knowledge: {
        uuid: document.uuid,
        filePath: document.file_path,
        // 整篇引用（fragment = null）：提交时取文档全文
        folderPath: knowledgeFolderPath(document.folder_id),
        fragment: null,
        invalid: false,
      },
    })
    markers.push(marker)
  }
  closeDocumentMention()
  knowledgePickerOpen.value = false
  const textarea = textareaRef.value
  const value = textarea?.value ?? props.modelValue
  const caret = textarea
    ? Math.min(textarea.selectionStart ?? value.length, value.length)
    : value.length
  // 从 textarea 当前状态重新解析 # 块位置，覆盖用户从 # 菜单和工具按钮两条路径。
  // - 用户从 # 菜单进入：光标停在 #query 末尾，context 非空 → 替换 #query。
  // - 用户从工具按钮进入：光标在任意位置，context 多半为 null → 走 caret 路径直接插入。
  // 走实时计算而不是缓存，避免模态框打开期间 documentMentionContext 已被清空
  // 或者用户编辑了正文导致缓存与实际值不一致时丢替换。
  const context = resolveDocumentMentionContext(value, caret)
  if (context) {
    insertMarkerText(markers.join(' '), context.start, context.end)
    return
  }
  // 一次插入全部标记：逐条插入会读到尚未回写的正文，导致位置错乱
  insertMarkerText(markers.join(' '), caret, caret)
}

/** S-UI-18/19：`#` 菜单固定项与工具按钮共用的入口 */
function openKnowledgePicker() {
  closeDocumentMention()
  knowledgePickerOpen.value = true
}

/**
 * S-UI-13: 消费知识库页投递的待发引用。
 * 草稿不带任务上下文（知识库页没有任务可选），绑定任一任务后即被消费；
 * 无 taskUuid（新会话）时不消费，留在队列里等有任务上下文的输入框。
 */
function applyKnowledgeReferenceDraft(draft: KnowledgeReferenceDraft) {
  const marker = buildDocumentMarker(draft.title, draft.fragment, fragmentBadge.value)
  documentReferences.value.push({
    id: crypto.randomUUID(),
    marker,
    name: draft.title,
    kind: 'knowledge',
    knowledge: {
      uuid: draft.uuid,
      filePath: draft.filePath,
      folderPath: draft.folderPath,
      fragment: draft.fragment,
      invalid: false,
    },
  })
  return draft.instruction ? `${marker} ${draft.instruction}` : marker
}

function consumeKnowledgeReferenceDrafts() {
  if (!props.taskUuid) return
  const drafts = knowledgeReferenceStore.consume()
  if (!drafts.length) return
  const additions = drafts.map(applyKnowledgeReferenceDraft)
  const base = props.modelValue.trim()
  emit('update:modelValue', [base, ...additions].filter(Boolean).join('\n'))
  void nextTick(resizeTextarea)
}

/** 从任务详情等外部入口打开产出文档抽屉：没有步骤上下文时返回 false，由调用方自行兜底 */
function openDocuments(): boolean {
  if (!documentsStep.value) return false
  taskDocumentsOpen.value = true
  return true
}

/** 产出文档抽屉里点「引用到输入框」：不依赖 # 触发上下文，直接在光标处插入标记 */
function handleDrawerReference(file: TaskFileNode) {
  if (!file.absolute_path) return
  const textarea = textareaRef.value
  const value = props.modelValue
  const caret = textarea
    ? Math.min(textarea.selectionStart ?? value.length, value.length)
    : value.length
  insertDocumentMarker(
    { ...file, absolute_path: file.absolute_path },
    { start: caret, end: caret, query: '' },
  )
  taskDocumentsOpen.value = false
}

function basenameFromPath(path: string) {
  const normalized = path.replace(/\\/g, '/').replace(/\/+$/, '')
  return normalized.split('/').filter(Boolean).at(-1) || path
}

async function handlePaste(event: ClipboardEvent) {
  if (!event.clipboardData) return
  const files = [...event.clipboardData.items]
    .filter((item) => item.type.startsWith('image/'))
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file))
  if (!files.length) return
  if (isSavingPastedImages.value) {
    event.preventDefault()
    message.warning(t('workflows.task.create.savingImages'))
    return
  }

  const textarea = textareaRef.value
  const value = textarea?.value ?? props.modelValue
  const start = textarea?.selectionStart ?? value.length
  const end = textarea?.selectionEnd ?? start
  const pastedText = normalizeClipboardText(event.clipboardData.getData('text/plain'))
  event.preventDefault()
  // 图片一律进上方附件行，正文里只保留剪贴板里的文本，
  // 避免把 ![](attachments/...) 这类语法直接摊在输入框里
  if (pastedText) insertPlainText(pastedText, start, end)
  await attachFiles(files, addImages, t('workflows.task.create.pasteFailed'))
}

async function handleFileSelection(event: Event) {
  const input = event.target as HTMLInputElement
  const files = [...(input.files || [])]
  input.value = ''
  if (!files.length) return
  if (isSavingPastedImages.value) {
    message.warning(t('workflows.task.progress.savingAttachments'))
    return
  }
  await attachFiles(files, addFiles, t('workflows.task.progress.attachmentUploadFailed'))
}

type AttachmentAdder = (files: File[]) => Array<{ marker: string }>

/**
 * 粘贴和「附件」按钮共用的落地流程：加入附件列表 → 上传 → 留在附件行。
 * 正文里不写任何占位标记，markdown 引用等上传完成后再在提交时拼到消息末尾。
 */
async function attachFiles(files: File[], add: AttachmentAdder, failureText: string) {
  if (props.submitting || preparingSubmission.value) return
  const taskUuid = props.taskUuid
  const draftKey = composerDraftIdentity.value
  let added: Array<{ marker: string }> = []
  try {
    added = add(files)
    await uploadAttachmentsToTask(taskUuid)
    await nextTick()
    if (draftKey === composerDraftIdentity.value) {
      textareaRef.value?.focus()
      resizeTextarea()
    }
  } catch (error) {
    // 撤掉这一批，避免附件行里留下没上传成功的条目
    added.forEach((attachment) => removeAttachment(attachment.marker, draftKey))
    message.error(error instanceof Error ? error.message : failureText)
  }
}

/** 按光标位置插入剪贴板文本；附件不进正文，所以这里只处理文本 */
function insertPlainText(text: string, start: number, end: number) {
  const textarea = textareaRef.value
  const value = textarea?.value ?? props.modelValue
  const insertStart = Math.min(start, value.length)
  const insertEnd = Math.min(Math.max(end, insertStart), value.length)
  const nextValue = `${value.slice(0, insertStart)}${text}${value.slice(insertEnd)}`
  const caret = insertStart + text.length
  emit('update:modelValue', nextValue)
  nextTick(() => {
    const updatedTextarea = textareaRef.value
    if (!updatedTextarea) return
    updatedTextarea.focus()
    updatedTextarea.setSelectionRange(caret, caret)
    syncDocumentMention(updatedTextarea)
    resizeTextarea()
  })
}

/**
 * 附件在正文里没有位置，提交时统一拼到消息末尾。
 * 只取已落地的 markdown：还没上传完的条目拿不到引用，此时 canSubmit 也会挡住提交。
 */
function attachmentMarkdownText() {
  return attachmentItems.value
    .map((attachment) => attachment.markdown)
    .filter((markdown): markdown is string => Boolean(markdown))
    .join('\n')
}

function serializeDocumentReferences(value: string) {
  let serialized = value
  for (const reference of documentReferences.value) {
    // S-UI-21: 产出文件引用行为不变（标记 → 绝对路径）；知识库引用由下方的引用块流程处理
    if (reference.kind !== 'task-file') continue
    serialized = serialized.split(reference.marker).join(reference.absolutePath)
  }
  return serialized
}

/**
 * S-UI-23 / S-IN-08：按长度选择引用形态。
 * 未超限时直接内联正文；超限时落盘为产出文件并把说明写回 `display_content`（可感知，不静默改形态）。
 * 落盘失败返回 null，由调用方中止提交（边界 E13）。
 */
async function buildKnowledgeReferenceContent(
  reference: KnowledgeDocumentReference,
  content: string,
  taskUuid: string,
): Promise<{ content: string; displayNote: string; spilledPath: string } | null> {
  const block = {
    name: reference.name,
    folderPath: reference.knowledge.folderPath,
    fragment: reference.knowledge.fragment,
    content,
  }
  if (countCharacters(content) <= KNOWLEDGE_REFERENCE_CHAR_LIMIT) {
    return { content: buildKnowledgeReferenceBlock(block), displayNote: '', spilledPath: '' }
  }
  try {
    const spilled = await spillKnowledgeReference(taskUuid, reference.name, content)
    // 新产出文件立刻进入「产出文档」索引，用户切到抽屉即可看到
    invalidateDocumentMentionIndex(taskUuid)
    return {
      content: buildKnowledgeReferenceBlock({ ...block, spilledPath: spilled.absolute_path }),
      displayNote: t('workflows.task.progress.referenceSpillNote', {
        name: reference.name,
        path: spilled.path,
      }),
      spilledPath: spilled.path,
    }
  } catch (error) {
    message.error(
      error instanceof Error && error.message
        ? error.message
        : t('workflows.task.progress.referenceSpillFailed'),
    )
    return null
  }
}

/**
 * S-UI-22 / S-IN-09：提交前处理知识库引用。
 *
 * 1. 有效性校验（`/knowledge/documents/:uuid/path` 的 `exists`）；失效则标记胶囊失效并中止提交；
 * 2. 取内容（选中片段 / 整篇）；
 * 3. 超过 10000 码点则落盘为产出文件；
 * 4. 用引用块替换正文中的 `#[标记]`。
 *
 * 返回 null 表示提交已中止（已给出提示）。
 */
async function resolveKnowledgeReferences(promptContent: string, displayContent: string) {
  let content = promptContent
  let display = displayContent
  const taskUuid = props.taskUuid
  const references = [...documentReferences.value]
  for (const reference of references) {
    if (reference.kind !== 'knowledge') continue
    if (!content.includes(reference.marker)) continue

    const validation = await validateKnowledgeReference(reference.knowledge.uuid)
    if (!validation.valid) {
      reference.knowledge.invalid = true
      message.warning(t('workflows.task.progress.referenceInvalid'))
      return null
    }
    reference.knowledge.invalid = false

    const text = await loadKnowledgeReferenceContent(
      reference.knowledge.uuid,
      reference.knowledge.fragment,
    )
    const resolved = await buildKnowledgeReferenceContent(reference, text, taskUuid)
    if (!resolved) return null
    content = content.split(reference.marker).join(resolved.content)
    if (resolved.displayNote) {
      display = `${display}\n\n${resolved.displayNote}`
      message.info(t('workflows.task.progress.referenceSpilled', { path: resolved.spilledPath }))
    }
  }
  return { content, displayContent: display }
}

async function handleSubmit() {
  if (!canSubmit.value) return
  if (showAdvanceAction.value) {
    emit('advance')
    return
  }
  const identity = runtimeIdentity.value
  closeDocumentMention()
	expertMentionOpen.value = false
  preparingSubmission.value = true
  try {
    const rawContent =
      props.modelValue.trim() ||
      (props.startMode ? t('workflows.task.progress.startProcessing') : '')
    // 附件在正文里没有位置，统一追加到消息末尾；只有附件、没有文字时附件就是全部内容
    const displayContent = [rawContent, attachmentMarkdownText()].filter(Boolean).join('\n')
    // 产出文件引用先按现状序列化；知识库引用再做校验 / 取值 / 落盘 / 替换
    const promptContent = serializeDocumentReferences(displayContent)
    const resolved = await resolveKnowledgeReferences(promptContent, displayContent)
    if (!resolved || identity !== runtimeIdentity.value) return
    emit('submit', {
      content: resolved.content.trim(),
      // 聊天记录展示原始输入（保留 #[名称] 标记）：绝对路径与引用正文只有 CLI 需要，
      // 不应该跟着落进会话记录里
      display_content: resolved.displayContent.trim(),
      config: {
        cli_type: selectedCliType.value,
        model_name: selectedModelName.value,
      },
	  member_uuid: selectedExpertMember.value?.uuid,
    })
	selectedExpertMember.value = undefined
    await nextTick()
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workflows.task.progress.prepareMessageFailed'),
    )
  } finally {
    preparingSubmission.value = false
  }
}

function persistRuntimeSelection() {
  const stepUuid = props.currentStep?.uuid || ''
  if (!props.taskUuid || !stepUuid || !selectedCliType.value) return
  saveChatRuntimeSelection(props.taskUuid, stepUuid, {
    cli_type: selectedCliType.value,
    model_name: selectedModelName.value,
  })
}

function cliStillAvailable(cliType: string) {
  if (!cliType) return false
  const match = cliOptions.value.find((item) => item.type === cliType)
  return Boolean(match?.installed)
}

async function initialiseRuntimeConfig() {
  const requestId = ++runtimeRequestId
  runtimeTouched.value = false
  const cached = getChatRuntimeSelection(props.taskUuid, props.currentStep?.uuid || '')
  selectedCliType.value = cached?.cli_type || props.cliType || props.currentStep?.cli_type || props.executionTask?.execution_tool || ''
  selectedModelName.value =
    cached?.model_name ||
    props.modelName ||
    props.currentStep?.model_name ||
    props.currentStep?.model ||
    props.executionTask?.execution_model ||
    ''
  if (!runtimeEnabled.value) {
    resetModelOptions()
    return
  }

  try {
    if (!cliOptions.value.length) await loadCliOptions()
    if (requestId !== runtimeRequestId) return
    if (cached?.cli_type && !cliStillAvailable(cached.cli_type)) {
      selectedCliType.value = props.cliType || props.currentStep?.cli_type || ''
      selectedModelName.value =
        props.modelName || props.currentStep?.model_name || props.currentStep?.model || ''
    }
    void prefetchModelCounts(cliOptions.value.map((item) => item.type))
    if (!selectedCliType.value) {
      resetModelOptions()
      return
    }
    const models = await loadModelOptions(selectedCliType.value)
    if (requestId !== runtimeRequestId) return
    if (selectedModelName.value && models.length && !models.includes(selectedModelName.value)) {
      selectedModelName.value = models[0] || ''
    } else if (!selectedModelName.value) {
      selectedModelName.value = models[0] || ''
    }
  } catch (error) {
    if (requestId === runtimeRequestId) {
      message.warning(
        error instanceof Error ? error.message : t('workflows.task.progress.runtimeLoadFailed'),
      )
    }
  }
}

async function handleCliChange(cliType: string) {
  if (cliType === selectedCliType.value) return
  runtimeRequestId++
  runtimeTouched.value = true
  selectedCliType.value = cliType
  selectedModelName.value = ''
  persistRuntimeSelection()
  try {
    const models = await loadModelOptions(cliType)
    if (selectedCliType.value === cliType && !selectedModelName.value) {
      selectedModelName.value = models[0] || ''
      persistRuntimeSelection()
    }
  } catch (error) {
    if (selectedCliType.value === cliType) {
      message.warning(
        error instanceof Error ? error.message : t('components.cliSelect.modelsLoadFailed'),
      )
    }
  }
}

function handleModelChange(model: string) {
  runtimeTouched.value = true
  selectedModelName.value = model
  persistRuntimeSelection()
  cliPickerOpen.value = false
}

onMounted(() => {
  resizeTextarea()
  // 捕获阶段处理外部点击，避免其他控件阻止冒泡后文档选择菜单无法关闭。
  document.addEventListener('mousedown', handleDocumentMousedown, true)
  // selectionchange 是浏览器原生的「光标 / 选区变化」事件，覆盖 click、方向键、
  // Backspace、Delete、粘贴、程序设置 selection 等所有路径，
  // 是 preview 跟随 caret 同步最稳的事件源。
  document.addEventListener('selectionchange', syncFragmentPreview)
})

onUnmounted(() => {
  document.removeEventListener('mousedown', handleDocumentMousedown, true)
  document.removeEventListener('selectionchange', syncFragmentPreview)
})

/**
 * 点击菜单外部关闭文档选择菜单；片段预览仍按光标位置同步。
 * mousedown 比 click 更早触发，能在用户聚焦其他控件之前完成关闭，避免闪烁。
 *
 * 决策不再直接 closeFragmentPreview，而是交给 syncFragmentPreview 统一处理：
 * - 点中菜单 / 片段胶囊：跳过同步，由 click + selectionchange 自然决定（caret 落在 marker 上则保持开）。
 * - 其他位置：触发一次同步，未命中 marker 即关。
 * 这避免了过去「mousedown 关、click 又开」在点中 marker 时造成的闪烁。
 */
function handleDocumentMousedown(event: MouseEvent) {
  const target = event.target as Node | null
  if (!target) return
  const menu = documentMentionMenuRef.value?.menuRef
  const tokenEl = (target as Element).closest?.('.mention-token-clickable')
  if (menu?.contains(target)) return
  if (documentMentionOpen.value) dismissDocumentMention()
  if (tokenEl) return
  // 其他位置：提前同步一次，click 期间 selectionchange 再调一次，最终态由 sync 决定
  syncFragmentPreview()
}

// 同步保存，确保路由卸载或任务切换前最后一次选择也已写入草稿。
// 产出文件仍通过任务文件索引重新登记，避免缓存失效的绝对路径。
function persistKnowledgeReferences() {
  if (documentReferencesDraftKey !== composerDraftIdentity.value) return
  writeComposerDraftReferences(
    documentReferencesDraftKey,
    documentReferences.value.filter((reference): reference is KnowledgeDocumentReference => reference.kind === 'knowledge'),
  )
}

watch(documentReferences, persistKnowledgeReferences, { deep: true, flush: 'sync' })

watch(
  runtimeIdentity,
  () => {
    closeDocumentMention()
    cliPickerOpen.value = false
    executionMenuOpen.value = false
    knowledgePickerOpen.value = false
    documentReferencesDraftKey = composerDraftIdentity.value
    documentReferences.value = readComposerDraftReferences(composerDraftIdentity.value)
    // 恢复去重序号，避免返回页面后再选同名文档生成已有的 marker。
    documentReferenceSequence = documentReferences.value.reduce(
      (sequence, reference) => Math.max(sequence, Number(reference.marker.match(/ · (\d+)\]$/)?.[1] || 0)),
      0,
    )
    closeFragmentPreview()
    bindDocumentMentionIndex(props.taskUuid)
    // 挂载即预取产出文件列表，用户真正输入 # 时过滤已退化为内存计算
    void loadDocumentMentionIndex()
    void initialiseRuntimeConfig()
    // S-UI-13: 消费知识库页投递过来的待发引用
    consumeKnowledgeReferenceDrafts()
  },
  { immediate: true },
)

watch(cliPickerOpen, (open) => {
  if (!open || !cliOptions.value.length) return
  void prefetchModelCounts(cliOptions.value.map((item) => item.type))
})

watch(
  () => [props.cliType, props.modelName] as const,
  ([cliType, modelName], [previousCLIType, previousModelName]) => {
    if (runtimeTouched.value || (cliType === previousCLIType && modelName === previousModelName))
      return
    void initialiseRuntimeConfig()
  },
)

watch(
  () => props.modelValue,
  (value) => {
    nextTick(resizeTextarea)
    // 更新 Props 时正文可能先触发监听；等待 runtimeIdentity 恢复目标会话的引用。
    if (documentReferencesDraftKey !== composerDraftIdentity.value) return
    // 同步清理「既不在 value 里、候选池里也没同名」的引用：
    // - 不在 value 里但候选池里仍有同名（task-file 还在 files / knowledge 引用由选择器管理），
    //   用户大概率是剪切走 marker 准备粘贴到别处，保留登记让粘贴后能直接渲染。
    //   渲染层按 marker 字符串在 value 里精确匹配，保留登记即可命中，无需再次 push。
    // - 真正无效的（文件被删后 files 刷新、candidate 清空）会被剪掉。
    const candidates = mentionCandidateMap.value
    const stillValidIds = new Set(
      documentReferences.value
        .filter((r) => value.includes(r.marker) || candidates.has(r.name))
        .map((r) => r.id),
    )
    if (stillValidIds.size < documentReferences.value.length) {
      documentReferences.value = documentReferences.value.filter((r) => stillValidIds.has(r.id))
    }
    // 手输 / 粘贴 / 外部 v-model 修改：把恰好写对的 #[xxx] 补登到引用表（首次出现的 marker）
    reconcileDocumentReferences(value)
    // 正文清空后撤销时，引用登记可能未变化，也需要重新关联到恢复的草稿。
    persistKnowledgeReferences()
  },
)

// 候选池（产出文件 / 知识库已登记）任一变化时立即对账，捕捉 files 加载完成、
// 知识库选择器新登记等场景下的「恰好写对」的 #[xxx]
watch(
  () => mentionCandidateMap.value,
  () => {
    if (documentReferencesDraftKey !== composerDraftIdentity.value) return
    reconcileDocumentReferences(props.modelValue)
  },
)

function resetAfterSubmit(draftKey = composerDraftIdentity.value) {
  clearImageAttachments(draftKey)
  writeComposerDraftReferences(draftKey, [])
  if (draftKey !== composerDraftIdentity.value) return
  closeDocumentMention()
  documentReferences.value = []
  documentReferenceSequence = 0
}

defineExpose({ resetAfterSubmit, openDocuments })
</script>

<style scoped>
.composer-notice { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; margin-bottom: 8px; color: #8c8c8c; font-size: 12px; line-height: 20px; }
.execution-picker-trigger span { overflow: hidden; max-width: 180px; text-overflow: ellipsis; white-space: nowrap; }
.execution-step-name { display: inline-flex; align-items: center; gap: 8px; }
.execution-step-name img { width: 20px; height: 20px; border-radius: 50%; object-fit: cover; }
.execution-switch-label, .execution-switch-icon { color: #3157e2; }

.expert-mention-menu {
  position: absolute;
  z-index: 12;
  bottom: calc(100% + 8px);
  left: 24px;
  width: 280px;
  max-height: 240px;
  overflow-y: auto;
  padding: 6px;
  border: 1px solid #d9d9d9;
  border-radius: 12px;
  background: #fff;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.12);
}

.expert-mention-menu button {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 10px;
  padding: 8px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.expert-mention-menu button:hover,
.expert-mention-menu button:focus-visible,
.expert-mention-menu button.active { background: #f5f6f8; }
.expert-mention-menu img { width: 28px; height: 28px; border-radius: 50%; object-fit: cover; }
.expert-mention-menu span { display: flex; min-width: 0; flex-direction: column; }
.expert-mention-menu small { color: #8c8c8c; }
.chat-composer {
  position: relative;
  flex: 0 0 auto;
  background: #fff;
  padding: 8px 24px 24px;
}
.composer-shell {
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow: hidden;
  border: 2px solid #e5e7eb;
  border-radius: 12px;
  padding: 10px 12px;
  background: #fff;
  box-shadow: 0 4px 8px rgba(0, 0, 0, 0.08);
  transition:
    border-color 0.15s,
    box-shadow 0.15s;
}
.composer-shell:focus-within {
  border-color: #e5e7eb;
  box-shadow: 0 4px 8px rgba(0, 0, 0, 0.08);
}
.composer-shell.is-disabled,
.composer-shell.is-disabled:focus-within {
  border-color: #e5e7eb;
  background: #f7f8fa;
  box-shadow: none;
}
.composer-input {
  position: relative;
}
/* 渲染层与 textarea 必须共用同一套排版，任何一项不一致都会让标签块与光标错位 */
.composer-input-layer,
.composer-shell textarea {
  margin: 0;
  border: 0;
  padding: 0;
  font-family: inherit;
  font-size: 14px;
  line-height: 22px;
  letter-spacing: normal;
  white-space: pre-wrap;
  overflow-wrap: break-word;
  word-break: normal;
  tab-size: 4;
}
.composer-input-layer {
  position: absolute;
  inset: 0;
  overflow: hidden;
  color: #262626;
  background: #fff;
  pointer-events: none;
  user-select: none;
}
/*
 * # 引用标签块。视觉对齐 WorkBuddy 的文件引用胶囊（100px 圆角、浅灰底、深色字、前置文件图标）。
 *
 * 两条不能碰的约束——渲染层与 textarea 必须逐字同宽，否则光标会整体偏移：
 * 1. 字号/字重/字距必须与 textarea 完全一致，所以这里不覆盖 font-size、font-weight；
 *    需要更实的字重用 -webkit-text-stroke 描边实现，它不参与排版宽度计算。
 * 2. 只用纵向 padding 撑高度。行内元素的纵向 padding 不参与行盒高度计算，
 *    横向内边距则由不着色的 #[ 与 ] 两个语法字符天然提供。
 */
.mention-token {
  position: relative;
  padding: 3px 0;
  border-radius: 999px;
  color: rgba(0, 0, 0, 0.88);
  background: #f2f2f2;
  -webkit-text-stroke: 0.3px currentColor;
}
/* 语法字符只占位、不着色：撑出标签块左右内边距，同时保持逐字对齐 */
.mention-token-syntax {
  color: transparent;
  -webkit-text-stroke: 0;
}
/*
 * S-UI-13: 知识库选中片段引用，与整篇文档引用做视觉区分。
 * 同样只允许改颜色与背景：Primary Soft #E5EFFF + 主蓝文字，提示「这里是选中片段而非整篇」。
 * 标记里的「· 选」文本由 marker 字面量承载，肉眼也可识别，这里再用颜色强化。
 * 必须先于 .mention-token-invalid 声明——提交校验失败时无效态会盖在片段态之上，
 * 避免把「已失效的选中片段」错误地显示为主蓝配色。
 */
.mention-token-fragment {
  color: #3157e2;
  background: #e5efff;
}
/*
 * S-UI-22: 失效的知识库引用（文档已删除 / 磁盘文件不存在）。
 * 只允许改颜色与背景——字号、字重、内边距、字距都会破坏渲染层与 textarea 的逐字对齐。
 * 必须先于 .mention-token-invalid 之外的最后一道声明，确保无效态视觉优先。
 */
.mention-token-invalid {
  color: #fb363f;
  background: #fef2f2;
}
.mention-token-label {
  white-space: pre;
}
.mention-token-icon {
  position: absolute;
  top: 50%;
  left: 2.5px;
  width: 9px;
  height: 9px;
  transform: translateY(-50%);
  color: rgba(0, 0, 0, 0.5);
  pointer-events: none;
}
/* 原始标记保留 textarea 的排版宽度，独立绘制 12px 标签，避免改变光标与选区的位置。 */
.mention-token-knowledge {
  padding: 0;
  background: transparent;
  -webkit-text-stroke: 0;
}
.mention-token-knowledge > .mention-token-label {
  color: transparent;
}
.knowledge-reference-content {
  position: absolute;
  top: 50%;
  left: 50%;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  width: max-content;
  padding: 0 6px;
  border-radius: 999px;
  background: rgba(0, 0, 0, 0.05);
  color: #262626;
  font-size: 12px;
  font-weight: 400;
  line-height: 20px;
  transform: translate(-50%, -50%);
  box-sizing: border-box;
}
.knowledge-reference-content img {
  flex: 0 0 12px;
}
.knowledge-reference-content > span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mention-token-fragment .knowledge-reference-content {
  color: #3157e2;
  background: #e5efff;
}
.mention-token-invalid .knowledge-reference-content {
  color: #fb363f;
  background: #fef2f2;
}
/*
 * 不可编辑时不做富文本渲染，直接显示原生文本。
 * 中文组合输入期间同理：渲染层读的是 modelValue，而组合期间的 input 事件被
 * handleInput 按 isComposing 跳过，渲染层拿不到新值，所以必须让位给原生 textarea。
 *
 * 但 textarea 平时是 color: transparent 的（文字交给渲染层画），
 * 隐藏渲染层的同时必须把文字颜色还回来，否则预编辑的拼音、以及依赖
 * 预编辑文字位置的候选框会一起看不见。
 */
.composer-input.is-disabled .composer-input-layer,
.composer-input.is-composing .composer-input-layer {
  display: none;
}
.composer-input.is-composing textarea {
  color: #262626;
  -webkit-text-fill-color: #262626;
}
.composer-shell textarea {
  position: relative;
  z-index: 1;
  display: block;
  width: 100%;
  height: 66px;
  min-height: 66px;
  max-height: 212px;
  overflow-y: hidden;
  resize: none;
  outline: 0;
  /* 文字交给渲染层显示，这里只保留光标和选区 */
  color: transparent;
  -webkit-text-fill-color: transparent;
  caret-color: #262626;
  background: transparent;
  scrollbar-width: none;
}
.composer-shell textarea::-webkit-scrollbar {
  width: 0;
  height: 0;
}
.composer-shell textarea::selection {
  background: rgba(49, 87, 226, 0.18);
}
.composer-shell textarea::placeholder {
  color: #8c8c8c;
  -webkit-text-fill-color: #8c8c8c;
}
.composer-shell textarea:disabled {
  color: #9ca3af;
  background: transparent;
  cursor: not-allowed;
  opacity: 1;
  -webkit-text-fill-color: #9ca3af;
}
.composer-shell textarea:disabled::placeholder {
  color: #9ca3af;
  opacity: 1;
}
/*
 * 附件行：只放附件，# 引用在输入框里已有内联胶囊，不在这里重复。
 * 规格对齐 WorkBuddy 的文件标签——100px 圆角胶囊、23px 高、13px/500 深色字、
 * 浅灰底，悬停底色加深，同时图标原地换成关闭图标，不用额外占位一个删除按钮。
 */
.attachment-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
}
.attachment-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 220px;
  height: 23px;
  padding: 0 8px;
  border: 0;
  border-radius: 100px;
  color: rgba(0, 0, 0, 0.9);
  background: #f2f2f2;
  cursor: pointer;
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  transition: background-color 140ms ease;
}
.attachment-chip:hover {
  background: #e6e6e6;
}
.attachment-chip:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 1px;
}
.attachment-chip-icon-slot {
  position: relative;
  display: inline-flex;
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
  align-items: center;
  justify-content: center;
}
.attachment-chip-icon {
  font-size: 12px;
}
.attachment-chip-close {
  position: absolute;
  inset: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: rgba(0, 0, 0, 0.55);
  font-size: 10px;
  visibility: hidden;
}
.attachment-chip:hover .attachment-chip-icon {
  visibility: hidden;
}
.attachment-chip:hover .attachment-chip-close {
  visibility: visible;
}
.attachment-chip-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.composer-toolbar {
  display: flex;
  min-height: 24px;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.composer-tools {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.attachment-file-input {
  display: none;
}
.tool-divider {
  width: 1px;
  height: 12px;
  flex: 0 0 1px;
  background: #d9d9d9;
}
.cli-picker-trigger {
  max-width: 220px;
}
.cli-picker-trigger span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.composer-actions {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}
.tool-button {
  display: inline-flex;
  height: 24px;
  align-items: center;
  gap: 4px;
  border: 0;
  border-radius: 6px;
  padding: 1px 6px;
  color: #595959;
  background: #fff;
  cursor: pointer;
  font-family: inherit;
  font-size: 12px;
  line-height: 20px;
  letter-spacing: -0.1504px;
  transition: background-color 0.15s;
}
.tool-button:hover {
  background: #f2f4f7;
}
.tool-button:disabled {
  color: #bfbfbf;
  background: #fff;
  cursor: not-allowed;
}
.tool-button-icon {
  width: 16px;
  height: 16px;
  flex: 0 0 auto;
}
.composer-design-icon {
  width: 14px;
  height: 14px;
}
.composer-pipeline-icon {
  display: inline-flex;
  border-radius: 4px;
  background: #3157e2;
}
.composer-pipeline-icon::before {
  content: '';
  width: 16px;
  height: 16px;
  background: #fff;
  mask: var(--pipeline-icon) center / 16px 16px no-repeat;
}
.cli-brand-icon {
  object-fit: contain;
}
.work-directory-button {
  max-width: 240px;
}
.work-directory-button span:last-child {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.work-directory-button.selected {
  color: #3157e2;
  background: #eef2ff;
}
.send-button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 4px;
  color: #fff;
  background: #262626;
  cursor: pointer;
}
.send-button:not(.stop-button) {
  width: auto;
  min-width: 24px;
  white-space: nowrap;
}
.send-button:disabled {
  color: #aeb4be;
  background: #eceef2;
  cursor: not-allowed;
}
.stop-button:hover:not(:disabled) {
  background: #1f1f1f;
}
.stop-button:disabled {
  color: #fff;
  background: #595959;
}
.stop-square {
  width: 9px;
  height: 9px;
  border-radius: 1px;
  background: currentColor;
}
.submit-button {
  width: auto;
  min-width: 32px;
  padding: 2px 4px;
  border-radius: 6px;
  background: #262626;
  transition: background-color 180ms ease;
}
.submit-button:hover:not(:disabled) {
  background: #434343;
}
.submit-button:active:not(:disabled) {
  background: #141414;
}
.send-button:focus-visible,
.tool-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}
.send-button-text {
  font-size: 12px;
  line-height: 20px;
  white-space: nowrap;
}
.composer-hint {
  margin-top: 6px;
  font-size: 11px;
  color: #9ca3af;
  line-height: 1.4;
}
/* S-UI-22: 失效引用提示常驻显示，直到引用被删除或重新提交成功 */
.composer-hint-error {
  display: flex;
  gap: 6px;
  color: #fb363f;
}
@media (max-width: 720px) {
  .chat-composer {
    padding: 8px 16px 16px;
  }
  .composer-toolbar {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }
  .composer-tools {
    width: 100%;
    flex-wrap: wrap;
  }
  .composer-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
@media (prefers-reduced-motion: reduce) {
  .composer-shell,
  .submit-button,
  .tool-button {
    transition: none;
  }
}

/*
 * S-UI-13: 选中片段胶囊增加点击态。
 * 父容器 .composer-input-layer 整体 pointer-events: none 防止遮挡 textarea，
 * 片段胶囊需要单独重新开启 pointer-events 才能响应 click 打开预览；
 * pointer 改为手指，提示可点击查看选中内容。
 */
.mention-token-clickable {
  pointer-events: auto;
  cursor: pointer;
}
</style>
