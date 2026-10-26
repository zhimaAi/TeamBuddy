<script setup lang="ts">
import { showVibeToolOpenResult } from '@/composables/useVibeToolFeedback'
import { computed, nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { ConfigProvider, message } from 'ant-design-vue'
import {
  AppstoreOutlined,
  CalendarOutlined,
  CloseOutlined,
  DeploymentUnitOutlined,
  DownOutlined,
  EllipsisOutlined,
  FlagOutlined,
  FolderOpenOutlined,
  FullscreenOutlined,
  FullscreenExitOutlined,
  PlusOutlined,
} from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import { useContentFullscreen } from '@/composables/useContentFullscreen'
import TaskDescriptionEditor from '@/components/TaskDescriptionEditor.vue'
import AssignExecutionModeModal, {
  type ExecutionModeSelection,
} from '@/components/AssignExecutionModeModal.vue'
import { useCreateTaskDefaults } from '@/composables/useCreateTaskDefaults'
import { useCliKickoffNavigation } from '@/composables/useCliKickoff'
import { isDesktopRuntime, selectDirectory } from '@/composables/useDesktop'
import { openTaskVibeTool } from '@/composables/useTaskVibeTool'
import { isManualVibeTool, isVibeCodingTool, vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import { readRecentVibeModels, useVibeModelPreferences } from '@/composables/useVibeModelPreferences'
import { useTaskImageAttachments } from '@/composables/useTaskImageAttachments'
import { usePipelineStore } from '@/stores/pipeline'
import { useExpertGroupStore } from '@/stores/expert-group'
import type { Pipeline, TaskExecutionMode } from '@/types/pipeline'
import type { LocalProject } from '@/types/project'
import type { CloudWorkItem } from '@/types/workitem'
import { useAppI18n } from '@/i18n'
import WorkDirectoryPicker from '@/components/WorkDirectoryPicker.vue'

const { t } = useAppI18n()
const { openCliConversation } = useCliKickoffNavigation()
const relatedProjectTheme = {
  components: {
    Select: {
      fontSize: 14,
      lineHeight: 22 / 14,
      controlHeight: 32,
      controlHeightSM: 24,
      borderRadius: 6,
    },
  },
}

const props = defineProps<{
  open: boolean
  initialStatus?: string
  initialWorkItem?: CloudWorkItem
}>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  created: [uuid: string, options?: { openChat?: boolean }]
}>()
const pipelineStore = usePipelineStore()
const { pipelines } = storeToRefs(pipelineStore)
const expertGroupStore = useExpertGroupStore()
const { items: expertGroups } = storeToRefs(expertGroupStore)
const loading = ref(false)
const saving = ref(false)
const { getCreateTaskDefaults, saveCreateTaskDefaults } = useCreateTaskDefaults()
const {
  addImages,
  buildDraftAttachmentInputs,
  clear: clearImageAttachments,
  isSaving: isSavingPastedImages,
} = useTaskImageAttachments()
const descriptionEditor = ref<{ getValue: () => string }>()
const projects = ref<LocalProject[]>([])
const executionPickerOpen = ref(false)
const form = reactive({
  title: '',
  description: '',
  pipeline_uuid: '',
  execution_mode: '' as TaskExecutionMode,
  execution_tool: '',
  model_name: '',
	expert_group_uuid: '',
  project_uuid: '',
  child_project_uuids: [] as string[],
  work_dir: '',
  additional_dirs: [] as string[],
  priority: '',
  planned_start_date: '',
  planned_end_date: '',
})
const chipRowRef = ref<HTMLElement | null>(null)
const showDates = ref(false)
const showChildren = ref(false)
const showDirs = ref(false)

function toggleRelatedProjects() {
  if (showChildren.value) form.child_project_uuids = []
  showChildren.value = !showChildren.value
}

function toggleRelatedDirectories() {
  if (showDirs.value) form.additional_dirs = []
  showDirs.value = !showDirs.value
}
const { fullscreen } = useContentFullscreen()

const selectedPipeline = computed(() =>
  pipelines.value.find((item) => item.uuid === form.pipeline_uuid),
)
const selectedExpertGroup = computed(() => expertGroups.value.find((item) => item.uuid === form.expert_group_uuid))
const isCodexSelected = computed(
  () => form.execution_mode === 'vibe_coding' && isVibeCodingTool(form.execution_tool),
)
const isCLISelected = computed(() => form.execution_mode === 'cli')

const selectedProject = computed(() =>
  projects.value.find((item) => item.uuid === form.project_uuid),
)
const selectedChildren = computed(() =>
  projects.value.filter((item) => form.child_project_uuids.includes(item.uuid)),
)
const childProjectOptions = computed(() =>
  projects.value
    .filter((item) => item.uuid !== form.project_uuid)
    .map((item) => ({ label: item.name, value: item.uuid })),
)
const allWorkDirs = computed(() => {
  const dirs = [
    form.work_dir,
    ...selectedChildren.value.map((item) => item.local_dir),
    ...form.additional_dirs,
  ]
    .map((value) => value.trim())
    .filter(Boolean)
  return [...new Map(dirs.map((value) => [value.toLowerCase(), value])).values()]
})

function projectLabel(project?: LocalProject) {
  return project?.name || t('workflows.task.create.project')
}
function pipelineLabel(pipeline?: Pipeline) {
  return pipeline?.name || t('workflows.task.create.pipeline')
}
function executionChipLabel() {
  if (isCodexSelected.value) return [t(vibeToolLabelKey(form.execution_tool)), form.model_name].filter(Boolean).join(' · ')
  if (isCLISelected.value) {
    return form.execution_tool
      ? `${t('workflows.task.assign.cliMode')} · ${form.execution_tool}`
      : t('workflows.task.assign.cliMode')
  }
  if (form.execution_mode === 'pipeline') return pipelineLabel(selectedPipeline.value)
	if (form.execution_mode === 'expert_group') return selectedExpertGroup.value?.name || t('agents.expertTeam')
  return t('workflows.task.create.executionMode')
}
// form.priority 保存的是后端约定的中文枚举值，展示时必须走词条，避免英文界面出现中文。
function priorityLabel(priority?: string) {
  if (priority === 'high' || priority === '高') return t('workflows.task.priority.high')
  if (priority === 'medium' || priority === '中') return t('workflows.task.priority.medium')
  if (priority === 'low' || priority === '低') return t('workflows.task.priority.low')
  return ''
}
function reset() {
  clearImageAttachments()
  Object.assign(form, {
    title: props.initialWorkItem?.title || '',
    description: props.initialWorkItem?.description || '',
    pipeline_uuid: '',
    execution_mode: '',
    execution_tool: '',
    model_name: '',
	  expert_group_uuid: '',
    project_uuid: '',
    child_project_uuids: [],
    work_dir: '',
    additional_dirs: [],
    priority: '',
    planned_start_date: '',
    planned_end_date: '',
  })
  showDates.value = false
  showChildren.value = false
  showDirs.value = false
  executionPickerOpen.value = false
  fullscreen.value = false
}

async function handleDescriptionImages(files: File[]) {
  if (saving.value || isSavingPastedImages.value) throw new Error(t('workflows.task.create.savingImages'))
  return addImages(files).map((attachment) => attachment.marker)
}

function close() {
  if (!saving.value) emit('update:open', false)
}

async function load() {
  loading.value = true
  const [pipelineResult, expertResult, projectResult] = await Promise.allSettled([
    pipelineStore.loadPipelines(true),
	expertGroupStore.load(true),
    apiClient.get<{ items: LocalProject[] }>('/projects'),
  ])
  projects.value = projectResult.status === 'fulfilled' ? projectResult.value.items || [] : []
  if (pipelineResult.status === 'rejected')
    message.warning(t('workflows.task.create.pipelineLoadFailed'))
	if (expertResult.status === 'rejected') message.warning(t('expertGroups.loadFailed'))
  if (projectResult.status === 'rejected') message.warning(t('workflows.task.create.projectLoadFailed'))
  applyCreateTaskDefaults()
  try {
    await validateVibeFormModel()
  } catch {
    // 提交时重试校验，暂时保留历史偏好。
  }
  loading.value = false
}

const { resetVibeModels, resolveVibeModel } = useVibeModelPreferences()

async function validateVibeFormModel() {
  if (form.execution_mode !== 'vibe_coding') return
  const tool = form.execution_tool
  const candidate = form.model_name || ''
  resetVibeModels(tool, candidate)
  const model = await resolveVibeModel(tool)
  if (form.execution_mode === 'vibe_coding' && form.execution_tool === tool && (form.model_name || '') === candidate) form.model_name = model
}

function applyCreateTaskDefaults() {
  const defaults = getCreateTaskDefaults()
  if (defaults.project_uuid && projects.value.some((item) => item.uuid === defaults.project_uuid)) {
    form.project_uuid = defaults.project_uuid
  }
  if (defaults.work_dir) {
    form.work_dir = defaults.work_dir
  } else if (form.project_uuid) {
    const project = projects.value.find((item) => item.uuid === form.project_uuid)
    if (project?.local_dir) form.work_dir = project.local_dir
  }
  form.priority = defaults.priority

  const executionMode =
    defaults.execution_mode || (defaults.pipeline_uuid ? 'pipeline' : '')
  if (
    executionMode === 'pipeline' &&
    defaults.pipeline_uuid &&
    pipelines.value.some((item) => item.uuid === defaults.pipeline_uuid)
  ) {
    form.execution_mode = 'pipeline'
    form.pipeline_uuid = defaults.pipeline_uuid
  } else if (
    executionMode === 'expert_group' &&
    defaults.expert_group_uuid &&
    expertGroups.value.some((item) => item.uuid === defaults.expert_group_uuid)
  ) {
    form.execution_mode = 'expert_group'
    form.expert_group_uuid = defaults.expert_group_uuid
  } else if (
    executionMode === 'vibe_coding' &&
    isVibeCodingTool(defaults.execution_tool)
  ) {
    form.execution_mode = 'vibe_coding'
    form.execution_tool = defaults.execution_tool
    form.model_name = readRecentVibeModels()[defaults.execution_tool] ?? defaults.model_name ?? ''
  } else if (
    executionMode === 'cli' &&
    defaults.execution_tool &&
    defaults.model_name
  ) {
    form.execution_mode = 'cli'
    form.execution_tool = defaults.execution_tool
    form.model_name = defaults.model_name
  }
}

function chooseProject(uuid: string) {
  form.project_uuid = uuid
  const project = projects.value.find((item) => item.uuid === uuid)
  if (project) form.work_dir = project.local_dir
}

function applyExecutionSelection(selection: ExecutionModeSelection) {
  form.execution_mode = selection.mode
  form.pipeline_uuid = selection.pipelineUuid
  form.expert_group_uuid = selection.expertGroupUuid
  form.execution_tool = selection.tool
  form.model_name = selection.modelName
}

function handleWorkDirSelect(dir: string) {
  form.work_dir = dir
  form.project_uuid = ''
}

async function addDirectory() {
  if (isDesktopRuntime()) {
    try {
      const selected = await selectDirectory(form.work_dir)
      if (!selected) return
      if (allWorkDirs.value.some((dir) => dir.toLowerCase() === selected.toLowerCase())) {
        message.warning(t('workflows.task.create.duplicateDirectory'))
        return
      }
      form.additional_dirs.push(selected)
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('workflows.task.create.directoryPickerFailed'))
    }
  } else {
    form.additional_dirs.push('')
  }
}

async function create() {
  if (loading.value || saving.value) return
  form.description = descriptionEditor.value?.getValue() ?? form.description
  if (!form.title.trim() && !form.description.trim())
    return message.warning(t('workflows.task.create.titleOrDescriptionRequired'))
  if (!form.work_dir.trim()) return message.warning(t('workflows.task.create.directoryRequired'))
  if (isSavingPastedImages.value) return message.warning(t('workflows.task.create.waitImages'))
  if (form.additional_dirs.some((item) => !item.trim()))
    return message.warning(t('workflows.task.create.emptyRelatedDirectory'))
  if (form.execution_mode === 'pipeline' && !form.pipeline_uuid)
    return message.warning(t('workflows.task.create.executionTargetRequired'))
  if (form.execution_mode === 'vibe_coding' && !isVibeCodingTool(form.execution_tool))
    return message.warning(t('workflows.task.create.executionTargetRequired'))
  if (form.execution_mode === 'cli' && (!form.execution_tool || !form.model_name))
    return message.warning(t('workflows.task.create.executionTargetRequired'))
	if (form.execution_mode === 'expert_group' && !selectedExpertGroup.value?.ready)
	  return message.warning(t('expertGroups.notReady'))
  saving.value = true
  let createdTaskUuid = ''
  // 弹窗关闭后 form 可能被重置，先固定本次创建的执行方式。
  const createdExecutionMode = form.execution_mode
  const shouldOpenVibe = form.execution_mode === 'vibe_coding'
  const vibeTool = form.execution_tool
  try {
    await validateVibeFormModel()
    const attachments = await buildDraftAttachmentInputs(form.description)
    const result = await apiClient.post<{ uuid: string }>('/tasks', {
      title: form.title.trim(),
      content: form.description,
      description: form.description,
      pipeline_uuid: form.pipeline_uuid || undefined,
      execution_mode: form.execution_mode || undefined,
      execution_tool: form.execution_tool || undefined,
      model_name: form.model_name || undefined,
	  expert_group_uuid: form.expert_group_uuid || undefined,
      project_uuid: form.project_uuid || undefined,
      child_project_uuids: form.child_project_uuids,
      work_dir: form.work_dir,
      work_dirs: allWorkDirs.value,
      priority: form.priority || undefined,
      planned_start_date: form.planned_start_date || undefined,
      planned_end_date: form.planned_end_date || undefined,
      // CLI 直接执行创建后立即进入进行中，等待用户在对话里发出第一条指令；
      // Vibe Coding 保持待开始，由外部编辑器流程驱动。
      status:
        form.execution_mode === 'cli'
          ? 'active'
          : form.execution_mode === 'vibe_coding'
            ? 'pending'
            : props.initialStatus || 'pending',
      work_item_type: props.initialWorkItem?.type,
      work_item_id: props.initialWorkItem?.id,
      attachments,
      // 需求 2204：CLI 任务创建后要直接出现在对话列表里并被选中，因此随创建写入通知。
      create_notification: createdExecutionMode === 'cli',
    })
    saveCreateTaskDefaults({
      project_uuid: form.project_uuid,
      work_dir: form.work_dir,
      pipeline_uuid: form.pipeline_uuid,
      priority: form.priority,
      execution_mode: form.execution_mode,
      execution_tool: form.execution_tool,
      model_name: form.model_name,
      expert_group_uuid: form.expert_group_uuid,
      // 本弹窗没有「同步 Agent 产出」选项，沿用上次在团队任务配置里的选择
      sync_agent_output: getCreateTaskDefaults().sync_agent_output,
    })
    message.success(t('workflows.task.create.created'))
    createdTaskUuid = result.uuid
    // 父级若同时打开任务详情，会和下面的对话页跳转互相取消。CLI 由本弹窗负责进对话。
    emit('created', result.uuid, { openChat: createdExecutionMode === 'cli' })
    emit('update:open', false)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.feedback.taskCreateFailed'))
  } finally {
    saving.value = false
  }
  if (!createdTaskUuid) return
  // 需求 2204（评论 5）：CLI 任务确定创建后跳到对话界面，由对话页自动向 CLI
  // 发送 task.md 引用与起始提示词，不再预填输入框。
  if (createdExecutionMode === 'cli') {
    void openCliConversation(createdTaskUuid).catch(() => undefined)
    return
  }
  if (!shouldOpenVibe) return

  const toolName = t(vibeToolLabelKey(vibeTool))
  const openResult = await openTaskVibeTool(createdTaskUuid)
  showVibeToolOpenResult(openResult, toolName)
}

// chip 下拉统一向下展开：按 chip 行下沿到视口底部的距离限制菜单高度，
// 避免菜单被自动翻转向上，或被弹窗面板与视口裁剪。
function syncChipMenuMaxHeight() {
  const row = chipRowRef.value
  if (!row) return
  const available = window.innerHeight - row.getBoundingClientRect().bottom - 24
  document.documentElement.style.setProperty(
    '--chip-menu-max-height',
    `${Math.max(140, Math.min(380, available))}px`,
  )
}

function handleChipMenuOpenChange(open: boolean) {
  if (open) syncChipMenuMaxHeight()
}

onUnmounted(() => {
  document.body.style.overflow = ''
  window.removeEventListener('resize', syncChipMenuMaxHeight)
  document.documentElement.style.removeProperty('--chip-menu-max-height')
})

watch(pipelines, (items) => {
  if (!items.some((item) => item.uuid === form.pipeline_uuid)) {
    form.pipeline_uuid = ''
  }
})

watch(
  () => props.open,
  (open) => {
    document.body.style.overflow = open ? 'hidden' : ''
    if (open) {
      reset()
      void load()
      void nextTick(syncChipMenuMaxHeight)
      window.addEventListener('resize', syncChipMenuMaxHeight)
    } else {
      executionPickerOpen.value = false
      window.removeEventListener('resize', syncChipMenuMaxHeight)
    }
  },
)
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="linear-task-root"
      :class="{ 'is-fullscreen': fullscreen }"
      role="dialog"
      aria-modal="true"
      @keydown.esc="close"
    >
      <div v-show="!fullscreen" class="linear-task-mask" />
      <div class="linear-task-panel">
        <a-spin :spinning="loading">
      <header class="task-modal-header">
        <strong>{{ t('workflows.task.create.title') }}</strong
        ><div class="task-modal-header-actions">
          <button type="button" :aria-label="fullscreen ? t('workflows.task.common.exitFullscreen') : t('workflows.task.common.fullscreen')" @click="fullscreen = !fullscreen">
            <FullscreenExitOutlined v-if="fullscreen" /><FullscreenOutlined v-else />
          </button>
          <button type="button" :disabled="saving" :aria-label="t('workflows.task.create.close')" @click="close"><CloseOutlined /></button>
        </div>
      </header>
      <section class="task-primary">
        <a-input
          v-model:value="form.title"
          :disabled="saving"
          :maxlength="50"
          :bordered="false"
          :placeholder="t('workflows.task.create.customName')"
          class="title-input"
        />
        <div class="description-box">
          <TaskDescriptionEditor
            v-if="open"
            ref="descriptionEditor"
            v-model="form.description"
            :placeholder="t('workflows.task.create.description')"
            :disabled="saving"
            :on-upload-images="handleDescriptionImages"
          />
        </div>
      </section>
      <div
        ref="chipRowRef"
        class="chip-row"
        :class="{ 'has-expanded': showDates || showChildren || showDirs }"
      >
        <a-dropdown
          trigger="click"
          placement="bottomLeft"
          overlay-class-name="chip-select-menu"
          @open-change="handleChipMenuOpenChange"
        >
          <button
            type="button"
            class="chip"
            :class="{ selected: selectedProject }"
          >
            <AppstoreOutlined class="chip-icon" />
            <span class="chip-label">{{ projectLabel(selectedProject) }}</span>
            <DownOutlined />
          </button>
          <template #overlay>
            <a-menu>
              <a-menu-item
                key="none"
                @click="form.project_uuid = ''"
                >{{ t('workflows.task.create.noProject') }}</a-menu-item
              >
              <a-menu-divider />
              <a-menu-item
                v-for="project in projects"
                :key="project.uuid"
                @click="chooseProject(project.uuid)"
              >
                <span class="menu-primary">{{ project.name }}</span>
                <small>{{ project.local_dir }}</small>
              </a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
        <WorkDirectoryPicker
          v-model="form.work_dir"
          @select="handleWorkDirSelect"
        >
          <template #default>
            <button
              type="button"
              class="chip"
              :class="{ selected: form.work_dir }"
              :title="t('workflows.task.create.mainDirectory')"
            >
              <FolderOpenOutlined class="chip-icon" />
              <span class="chip-label">{{ form.work_dir || t('workflows.task.create.chooseDirectory') }}</span>
              <b>*</b>
            </button>
          </template>
        </WorkDirectoryPicker>
        <a-dropdown
          trigger="click"
          placement="bottomLeft"
          overlay-class-name="chip-select-menu chip-select-menu--rows"
          @open-change="handleChipMenuOpenChange"
        >
          <button
            type="button"
            class="chip"
            :class="{ selected: form.priority }"
          >
            <FlagOutlined class="chip-icon" />
            <span
              v-if="form.priority"
              class="priority-value"
              :class="{
                high: form.priority === '高',
                medium: form.priority === '中',
                low: form.priority === '低',
              }"
              >{{ priorityLabel(form.priority) }}</span
            >
            <span
              v-else
              class="chip-label"
              >{{ t('workflows.task.create.choosePriority') }}</span
            >
            <DownOutlined />
          </button>
          <template #overlay>
            <a-menu>
              <a-menu-item
                key=""
                @click="form.priority = ''"
                >{{ t('workflows.task.create.none') }}</a-menu-item
              >
              <a-menu-item
                key="high"
                @click="form.priority = '高'"
                >🔴 {{ t('workflows.task.priority.high') }}</a-menu-item
              >
              <a-menu-item
                key="medium"
                @click="form.priority = '中'"
                >🟡 {{ t('workflows.task.priority.medium') }}</a-menu-item
              >
              <a-menu-item
                key="low"
                @click="form.priority = '低'"
                >🔵 {{ t('workflows.task.priority.low') }}</a-menu-item
              >
            </a-menu>
          </template>
        </a-dropdown>
        <button
          type="button"
          class="chip execution-chip"
          :class="{ selected: form.execution_mode }"
          @click="executionPickerOpen = true"
        >
          <img
            v-if="isCodexSelected"
            class="chip-avatar"
            :class="{ 'is-brand-icon': isManualVibeTool(form.execution_tool) }"
            :src="vibeToolLogo(form.execution_tool)"
            alt=""
          />
          <img
            v-else-if="selectedPipeline?.avatar"
            class="chip-avatar"
            :src="selectedPipeline.avatar"
            alt=""
          />
          <img
            v-else-if="selectedExpertGroup?.avatar"
            class="chip-avatar"
            :src="selectedExpertGroup.avatar"
            alt=""
          />
          <DeploymentUnitOutlined
            v-else
            class="chip-icon"
          />
          <span class="chip-label">{{ executionChipLabel() }}</span>
        </button>
        <a-dropdown
          trigger="click"
          placement="bottomRight"
          overlay-class-name="chip-select-menu chip-select-menu--rows"
          @open-change="handleChipMenuOpenChange"
        >
          <button
            type="button"
            class="more-chip"
            :aria-label="t('workflows.task.create.more')"
          >
            <EllipsisOutlined />
          </button>
          <template #overlay>
            <a-menu>
              <a-menu-item
                key="dates"
                @click="showDates = !showDates"
                ><CalendarOutlined />{{ t('workflows.task.create.expectedDates') }}</a-menu-item
              >
              <a-menu-item
                key="children"
                @click="toggleRelatedProjects"
                ><AppstoreOutlined />{{ t('workflows.task.create.relatedProject') }}</a-menu-item
              >
              <a-menu-item
                key="dirs"
                @click="toggleRelatedDirectories"
                ><FolderOpenOutlined />{{ t('workflows.task.create.relatedDirectory') }}</a-menu-item
              >
            </a-menu>
          </template>
        </a-dropdown>
      </div>

      <section class="expanded-fields">
        <div
          v-if="showDates"
          class="expanded-row"
        >
          <span>{{ t('workflows.task.create.expectedStart') }}</span
          ><a-date-picker
            v-model:value="form.planned_start_date"
            value-format="YYYY-MM-DD"
            :placeholder="t('workflows.task.create.chooseDate')"
            size="small"
          /><button
            type="button"
            class="expanded-close"
            :aria-label="t('workflows.task.create.hideStart')"
            @click="showDates = false"
          >
            <CloseOutlined />
          </button>
        </div>
        <div
          v-if="showDates"
          class="expanded-row"
        >
          <span>{{ t('workflows.task.create.expectedEnd') }}</span
          ><a-date-picker
            v-model:value="form.planned_end_date"
            value-format="YYYY-MM-DD"
            :placeholder="t('workflows.task.create.chooseDate')"
            size="small"
          /><button
            type="button"
            class="expanded-close"
            :aria-label="t('workflows.task.create.hideEnd')"
            @click="showDates = false"
          >
            <CloseOutlined />
          </button>
        </div>
        <div
          v-if="showChildren"
          class="expanded-row children-row"
        >
          <span>{{ t('workflows.task.create.relatedProject') }}</span>
          <ConfigProvider :theme="relatedProjectTheme">
            <a-select
              v-model:value="form.child_project_uuids"
              mode="multiple"
              :max-tag-count="1"
              :options="childProjectOptions"
              :placeholder="t('workflows.task.create.choose')"
              size="middle"
            />
          </ConfigProvider>
          <button
            type="button"
            class="expanded-close"
            :aria-label="t('workflows.task.create.removeRelatedProjects')"
            @click="toggleRelatedProjects"
          >
            <CloseOutlined />
          </button>
        </div>
        <div
          v-if="showDirs"
          class="expanded-row dirs-row"
          :class="{ 'has-directories': form.additional_dirs.length > 0 }"
        >
          <span>{{ t('workflows.task.create.relatedDirectory') }}</span>
          <div class="directory-content">
            <div
              v-if="form.additional_dirs.length"
              class="directory-list"
            >
              <div
                v-for="(dir, index) in form.additional_dirs"
                :key="`${dir}-${index}`"
                class="directory-item"
              >
                <span :title="dir || t('workflows.task.create.notEntered')">{{ dir || t('workflows.task.create.notEntered') }}</span>
                <button
                  type="button"
                  :aria-label="t('workflows.task.detail.removeRelatedDirectory')"
                  @click="form.additional_dirs.splice(index, 1)"
                >
                  <CloseOutlined />
                </button>
              </div>
            </div>
            <a-button
              size="small"
              class="directory-add"
              @click="addDirectory"
              ><PlusOutlined />{{ t('workflows.task.create.addRelatedDirectory') }}</a-button
            >
          </div>
          <button
            type="button"
            class="expanded-close"
            :aria-label="t('workflows.task.create.removeRelatedDirectories')"
            @click="toggleRelatedDirectories"
          >
            <CloseOutlined />
          </button>
        </div>
      </section>

      <footer class="task-modal-footer">
        <span v-if="selectedPipeline"
          >{{ t('workflows.task.create.selectedPipeline', { name: selectedPipeline.name }) }}</span
        ><span v-else-if="isCLISelected && form.execution_tool && form.model_name"
          >{{ t('workflows.task.assign.cliSelected', { cli: form.execution_tool, model: form.model_name }) }}</span
        ><span v-else-if="form.execution_mode === 'vibe_coding' && form.execution_tool">{{ t(vibeToolLabelKey(form.execution_tool)) }}</span
        ><span v-else-if="form.execution_mode === 'expert_group' && selectedExpertGroup">{{ t('workflows.task.create.selectedExpertGroup', { name: selectedExpertGroup.name }) }}</span
        ><span v-else-if="form.execution_mode">{{ t('workflows.task.create.executionTargetRequired') }}</span
        ><span v-else>{{ t('workflows.task.create.pipelineLater') }}</span>
        <div>
          <a-button :disabled="saving" @click="close">{{ t('common.actions.cancel') }}</a-button
          ><a-button
            type="primary"
            :loading="saving"
            :disabled="loading"
            @click="create"
            >{{ t('workflows.task.import.create') }}</a-button
          >
        </div>
      </footer>
        </a-spin>
      </div>
    </div>
    <AssignExecutionModeModal
      v-model:open="executionPickerOpen"
      select-only
      :task-title="form.title.trim()"
      :initial-mode="form.execution_mode"
      :preferred-pipeline-uuid="form.pipeline_uuid"
      :preferred-expert-group-uuid="form.expert_group_uuid"
      :initial-cli-type="form.execution_mode === 'cli' ? form.execution_tool : ''"
      :initial-model-name="form.model_name"
      :initial-vibe-tool="form.execution_mode === 'vibe_coding' ? form.execution_tool : ''"
      @selected="applyExecutionSelection"
    />
  </Teleport>
</template>

<style scoped>
.task-modal-header {
  display: flex;
  height: 56px;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
}

.task-modal-header strong {
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.task-modal-header-actions { display: flex; align-items: center; gap: 4px; }

.task-modal-header button,
.expanded-close {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #8c8c8c;
  background: transparent;
  cursor: pointer;
}

.task-modal-header button:hover,
.expanded-close:hover {
  color: #4b5563;
  background: #f5f6f8;
}

.task-primary {
  padding: 16px 24px 0;
}

/* antd borderless 的 border:none 选择器特异性更高，下边框需 !important 才能稳定生效（含聚焦态） */
.title-input {
  height: 51px;
  border: 0;
  border-bottom: 1px solid #f0f0f0 !important;
  border-radius: 0;
  color: #262626;
  font-size: 22px;
  font-weight: 500;
  line-height: 34px;
}

.description-box {
  display: flex;
  height: 293px;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
}

.description-input {
  height: 293px;
  min-height: 0;
  flex: 1;
  border: 0;
  border-radius: 0;
  color: #262626;
  font-size: 14px;
  line-height: 28px;
  overflow-y: auto;
  resize: none;
}

.task-primary :deep(.ant-input),
.task-primary :deep(textarea) {
  padding-right: 0 !important;
  padding-left: 0 !important;
}

.task-primary :deep(.description-input) {
  padding: 8px 4px !important;
}

.chip-row {
  position: relative;
  display: flex;
  min-height: 52px;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 8px 24px 12px;
}

.chip-row.has-expanded {
  min-height: 56px;
  padding-bottom: 16px;
}

.chip,
.more-chip {
  display: flex;
  min-width: 0;
  height: 32px;
  max-width: 180px;
  align-items: center;
  gap: 6px;
  padding: 0 12px;
  border: 1px solid #d9d9d9;
  border-radius: 16px;
  color: #bfbfbf;
  background: #fff;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
}

/* chip 前置图标颜色固定，不随选中态文字变色 */
.chip-icon {
  flex: 0 0 auto;
  color: #bfbfbf;
  font-size: 16px;
}

/* 需求设计图中 chip 不展示下拉箭头（触发器仍保留 DownOutlined，仅做视觉隐藏） */
.chip :deep(.anticon-down) {
  display: none;
}

.chip.selected {
  border-color: #d9d9d9;
  color: #262626;
  background: #fff;
}

.chip b {
  flex: 0 0 auto;
  color: #e34d59;
  font-weight: 500;
}

.chip-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.priority-value {
  display: inline-flex;
  height: 22px;
  align-items: center;
  padding: 0 9px;
  border: 1px solid currentColor;
  border-radius: 12px;
  font-size: 12px;
  line-height: 20px;
}

.priority-value.high {
  color: #fb363f;
}

.priority-value.medium {
  color: #d97706;
}

.priority-value.low {
  color: #3157e2;
}

.chip-avatar {
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
  border-radius: 50%;
  object-fit: cover;
}

/* 手动工具直接用官方应用图标（自带圆角方形或透明底），不能再被裁成圆形。 */
.chip-avatar.is-brand-icon {
  border-radius: 5px;
}

/* chip 下拉共用一套菜单外观：内边距、圆角、阴影、悬停底色一致。
   高度上限由 syncChipMenuMaxHeight 按视口可用空间写入，保证向下展开时不被裁剪。 */
:global(.chip-select-menu .ant-dropdown-menu) {
  min-width: 240px;
  max-height: var(--chip-menu-max-height, 380px);
  overflow-y: auto;
  padding: 8px;
  border-radius: 12px;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.12);
}

:global(.chip-select-menu .ant-dropdown-menu-item),
:global(.chip-select-menu .ant-dropdown-menu-submenu-title) {
  border-radius: 8px;
}

:global(.chip-select-menu .ant-dropdown-menu-item:hover),
:global(.chip-select-menu .ant-dropdown-menu-submenu-title:hover),
:global(.chip-select-menu .ant-dropdown-menu-submenu-active .ant-dropdown-menu-submenu-title) {
  background: #f5f6f8;
}

/* 优先级、更多菜单：行高与执行方式菜单保持一致 */
:global(.chip-select-menu--rows .ant-dropdown-menu-item) {
  height: 36px;
  min-height: 36px;
  padding: 0 12px;
  border-radius: 8px;
  color: #262626;
  font-size: 14px;
  line-height: 36px;
}

:global(.chip-select-menu--rows .ant-dropdown-menu-item .anticon) {
  margin-right: 8px;
  color: #8c8c8c;
}

.more-chip {
  width: 32px;
  margin-left: auto;
  justify-content: center;
  padding: 0;
  border-radius: 50%;
  color: #262626;
}

.expanded-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  padding: 0 24px;
}

.expanded-row {
  display: flex;
  min-width: 0;
  height: 48px;
  align-items: center;
  gap: 8px;
  padding: 0 16px;
  border-radius: 6px;
  background: #f2f4f7;
}

.expanded-row > span:first-child {
  flex: 0 0 104px;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
}

.expanded-row :deep(.ant-picker) {
  min-width: 0;
  height: 32px;
  flex: 1;
  border-color: #d9d9d9;
  border-radius: 6px;
  background: #fff;
}

.expanded-row :deep(.ant-picker-input > input) {
  color: #8c8c8c;
  font-size: 14px;
}

.expanded-close {
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  color: #8c8c8c;
}

.expanded-close :deep(.anticon) {
  font-size: 14px;
}

.dirs-row.has-directories {
  height: auto;
  min-height: 82px;
  align-items: flex-start;
  padding-top: 8px;
  padding-bottom: 8px;
}

.dirs-row.has-directories > span:first-child {
  margin-top: 5px;
}

.directory-content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
}

.directory-list {
  display: flex;
  width: 100%;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
}

.children-row :deep(.ant-select) {
  min-width: 0;
  flex: 1;
}

.children-row,
.dirs-row {
  grid-column: 1 / -1;
}

.children-row :deep(.ant-select-selector) {
  min-height: 32px !important;
  border-color: #d9d9d9 !important;
  border-radius: 6px !important;
  background: #fff !important;
}

.children-row :deep(.ant-select-selection-placeholder) {
  color: #bfbfbf;
  font-size: 14px;
}

.directory-item {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: 6px;
  color: #4b5563;
  background: #fff;
  font-size: 11px;
}

.directory-item > span {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.directory-item > button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
}

.directory-add {
  height: 32px;
  border-color: #d9d9d9;
  border-radius: 6px;
  color: #4b5563;
  background: #fff;
}

.task-modal-footer {
  display: flex;
  height: 64px;
  align-items: center;
  justify-content: flex-end;
  gap: 16px;
  padding: 0 24px;
}

.task-modal-footer > span {
  display: none;
}

.task-modal-footer > div {
  display: flex;
  gap: 8px;
}



:global(.linear-task-modal .ant-modal-body) {
  padding: 0;
}



:global(.linear-task-modal .ant-dropdown-menu-item) {
  min-width: 220px;
}

.menu-primary {
  display: block;
  color: #374151;
}

:global(.ant-dropdown-menu-item small) {
  display: block;
  max-width: 270px;
  overflow: hidden;
  color: #9ca3af;
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 760px) {
  .description-box,
  .description-input {
    height: 180px;
  }

  .expanded-fields {
    grid-template-columns: 1fr;
  }

  .task-modal-footer > span {
    display: none;
  }
}
</style>

<style>
.linear-task-root {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  left: 0;
  z-index: 1000;
  display: flex;
  width: 100%;
  height: 100%;
  align-items: center;
  justify-content: center;
}
.linear-task-mask {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  left: 0;
  background: rgba(0, 0, 0, 0.45);
}
.linear-task-panel {
  position: relative;
  z-index: 1;
  display: flex;
  width: 879px;
  max-width: calc(100% - 32px);
  max-height: calc(100% - 32px);
  flex-direction: column;
  overflow: hidden;
  background: #fff;
  border-radius: 16px;
}
.linear-task-root.is-fullscreen .linear-task-panel {
  width: 100%;
  max-width: 100%;
  height: 100%;
  max-height: 100%;
  border-radius: 0;
}
.linear-task-root.is-fullscreen .linear-task-panel .ant-spin-nested-loading,
.linear-task-root.is-fullscreen .linear-task-panel .ant-spin-container {
  display: flex;
  height: 100%;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}
.linear-task-root.is-fullscreen .task-primary {
  display: flex;
  min-height: 180px;
  flex: 1;
  flex-direction: column;
}
.linear-task-root.is-fullscreen .description-box {
  display: flex;
  height: auto !important;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}
</style>
