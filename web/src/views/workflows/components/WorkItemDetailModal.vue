<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="work-item-root"
      role="dialog"
      aria-modal="true"
      @keydown.esc="emit('update:open', false)"
    >
      <div
        class="work-item-mask"
        @click="emit('update:open', false)"
      />
      <section class="work-item-panel">
        <header class="work-item-header">
          <strong>{{ t('teamwork.workItem.title') }}</strong>
          <button
            type="button"
            class="work-item-close"
            :aria-label="t('teamwork.workItem.close')"
            @click="emit('update:open', false)"
          >
            <CloseOutlined />
          </button>
        </header>

        <a-spin :spinning="loading">
          <div
            class="work-item-body"
            :class="{ 'aside-collapsed': collapsed }"
          >
            <div class="work-item-main scrollbar--subtle">
              <div class="work-item-heading">
                <h2 class="work-item-title">
                  <span class="work-item-title-text">{{ detail?.title || item?.title }}</span>
                  <a
                    v-if="workItemURL"
                    class="work-item-external-link"
                    :href="workItemURL"
                    target="_blank"
                    rel="noopener noreferrer"
                    :aria-label="t('teamwork.workItem.openInBrowser')"
                    :title="t('teamwork.workItem.openInBrowser')"
                  >
                    <ExportOutlined aria-hidden="true" />
                  </a>
                  <button
                    type="button"
                    class="work-item-aside-toggle"
                    :aria-label="collapsed ? t('teamwork.workItem.expand') : t('teamwork.workItem.collapse')"
                    :aria-expanded="!collapsed"
                    @click="collapsed = !collapsed"
                  >
                    <img
                      :src="directoryToggleIcon"
                      alt=""
                      aria-hidden="true"
                      class="work-item-aside-toggle-icon"
                    />
                  </button>
                </h2>
                <div
                  class="work-item-description"
                  @click="handleRichTextClick"
                >
                  <div
                    v-if="descriptionHTML"
                    class="rich-text"
                    v-html="descriptionHTML"
                  />
                  <MarkdownPreview
                    v-else
                    :content="descriptionText"
                    :empty-text="t('teamwork.workItem.empty')"
                  />
                </div>
              </div>

              <section class="work-item-section">
                <h3 class="work-item-section-title">
                  <PaperClipOutlined
                    class="section-icon"
                    aria-hidden="true"
                  />
                  <span>{{ t('teamwork.workItem.attachments') }}</span>
                  <span class="section-count">{{ attachments.length }}</span>
                </h3>
                <ul
                  v-if="attachments.length"
                  class="attachment-list"
                >
                  <li
                    v-for="file in attachments"
                    :key="file.id"
                  >
                    <a
                      v-if="file.url"
                      class="attachment-row"
                      :href="file.url"
                      target="_blank"
                      rel="noopener noreferrer"
                      :title="file.name"
                    >
                      <span class="attachment-main">
                        <PaperClipOutlined
                          class="attachment-icon"
                          aria-hidden="true"
                        />
                        <span class="attachment-name">{{ file.name }}</span>
                      </span>
                      <span
                        v-if="file.sizeLabel"
                        class="attachment-size"
                        >{{ file.sizeLabel }}</span
                      >
                    </a>
                    <div
                      v-else
                      class="attachment-row"
                      :title="file.name"
                    >
                      <span class="attachment-main">
                        <PaperClipOutlined
                          class="attachment-icon"
                          aria-hidden="true"
                        />
                        <span class="attachment-name">{{ file.name }}</span>
                      </span>
                      <span
                        v-if="file.sizeLabel"
                        class="attachment-size"
                        >{{ file.sizeLabel }}</span
                      >
                    </div>
                  </li>
                </ul>
                <p
                  v-else
                  class="work-item-section-empty"
                >
                  {{ t('teamwork.workItem.empty') }}
                </p>
              </section>

              <section class="work-item-section">
                <h3 class="work-item-section-title">
                  <CommentOutlined
                    class="section-icon"
                    aria-hidden="true"
                  />
                  <span>{{ t('teamwork.workItem.comments') }}</span>
                  <span class="section-count">{{ comments.length }}</span>
                </h3>
                <ul
                  v-if="comments.length"
                  class="comment-list"
                >
                  <li
                    v-for="comment in comments"
                    :key="comment.id"
                    class="comment-item"
                  >
                    <div class="comment-rail">
                      <img
                        v-if="comment.avatar"
                        class="comment-avatar"
                        :src="comment.avatar"
                        alt=""
                      />
                      <span
                        v-else
                        class="comment-avatar comment-avatar-fallback"
                      >
                        {{ comment.author.slice(0, 1).toUpperCase() }}
                      </span>
                      <span
                        class="comment-rail-line"
                        aria-hidden="true"
                      />
                    </div>
                    <div class="comment-body">
                      <div class="comment-meta">
                        <strong>{{ comment.author }}</strong>
                        <span>{{ comment.time }}</span>
                      </div>
                      <div
                        v-if="comment.html"
                        class="comment-content rich-text"
                        v-html="comment.html"
                        @click="handleRichTextClick"
                      />
                      <p
                        v-else
                        class="comment-content"
                      >
                        {{ comment.text }}
                      </p>
                    </div>
                  </li>
                </ul>
                <p
                  v-else
                  class="work-item-section-empty"
                >
                  {{ t('teamwork.workItem.empty') }}
                </p>
              </section>
            </div>

            <aside class="work-item-aside">
              <div class="aside-header">
                <nav
                  v-show="!collapsed"
                  class="aside-tabs"
                  :aria-label="t('teamwork.workItem.title')"
                >
                  <button
                    v-for="tab in detailTabs"
                    :key="tab.key"
                    type="button"
                    :class="{ active: activeTab === tab.key }"
                    :aria-current="activeTab === tab.key ? 'true' : undefined"
                    @click="activeTab = tab.key"
                  >
                    {{ tab.label }}
                  </button>
                </nav>
              </div>

              <!-- 属性：设计稿 3205:37823 的卡片式信息表，标签在左、值在右。 -->
              <dl
                v-if="activeTab === 'attributes'"
                v-show="!collapsed"
                class="attribute-card scrollbar--subtle"
              >
                <div
                  v-for="row in attributeRows"
                  :key="row.label"
                  class="attribute-row"
                >
                  <dt>{{ row.label }}</dt>
                  <dd :title="row.value">
                    <span
                      v-if="row.pillTone"
                      class="attribute-pill"
                      :class="row.pillTone"
                      >{{ row.value }}</span
                    >
                    <template v-else>{{ row.value }}</template>
                  </dd>
                </div>
              </dl>

              <!-- 任务信息：设计稿 3173:30534 的行式信息表，行首图标块 + 标签在上、值在下。 -->
              <ul
                v-else-if="!collapsed"
                class="task-info-list scrollbar--subtle"
              >
                <li
                  v-for="row in taskInfoRows"
                  :key="row.label"
                  class="task-info-row"
                >
                  <span
                    class="task-info-icon"
                    aria-hidden="true"
                  >
                    <component :is="row.icon" />
                  </span>
                  <div class="task-info-body">
                    <span class="task-info-label">{{ row.label }}</span>
                    <span
                      v-if="row.pillTone"
                      class="task-info-pill"
                      :class="row.pillTone"
                      :title="row.value"
                      >{{ row.value }}</span
                    >
                    <span
                      v-else
                      class="task-info-value"
                      :title="row.value"
                    >
                      <img
                        v-if="row.avatar"
                        class="task-info-avatar"
                        :src="row.avatar"
                        alt=""
                      />
                      <span class="task-info-text">{{ row.value }}</span>
                    </span>
                  </div>
                </li>
                <li
                  v-if="!taskInfoRows.length"
                  class="task-info-empty"
                >
                  {{ t('teamwork.workItem.empty') }}
                </li>
              </ul>
            </aside>
          </div>
        </a-spin>

        <TaskImagePreviewModal
          v-model:open="previewImageVisible"
          :image-url="previewImageUrl"
        />
      </section>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import {
  AppstoreOutlined,
  CalendarOutlined,
  CheckOutlined,
  CheckSquareOutlined,
  CloseOutlined,
  CommentOutlined,
  ExportOutlined,
  FolderOutlined,
  PaperClipOutlined,
  ProjectOutlined,
  ShareAltOutlined,
} from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import directoryToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import MarkdownPreview from '@/components/MarkdownPreview.vue'
import { useAppI18n } from '@/i18n'
import { useLocale } from '@/composables/useLocale'
import type { MyWorkItem } from '@/types/workitem'
import TaskImagePreviewModal from './TaskImagePreviewModal.vue'

const { t } = useAppI18n()
const { locale } = useLocale()

const props = defineProps<{
  open: boolean
  item?: MyWorkItem
}>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

type DetailTab = 'attributes' | 'taskInfo'

/** 已绑定本地任务时读回的本地任务字段，只取「任务信息」页签用得到的部分。 */
interface LocalTaskDetail {
  work_dir?: string
  work_dirs?: string[]
  priority?: string
  status?: string
  project_name?: string
  project_icon?: string
  pipeline_name_snapshot?: string
  pipeline_avatar_snapshot?: string
  expert_group_name_snapshot?: string
  expert_group_avatar_snapshot?: string
  planned_start_date?: string
  planned_end_date?: string
  projects?: Array<{ name?: string; icon?: string }>
}

interface WorkItemDetailPayload {
  item?: Record<string, unknown>
  comments?: Array<Record<string, unknown>>
  attachments?: Array<Record<string, unknown>>
  custom_fields?: CustomFieldDefinition[]
}

interface CloudConfigPayload {
  api_base_url?: string
}

interface CustomFieldOption {
  key?: string
  name?: string
}

interface CustomFieldDefinition {
  id?: number
  key?: string
  name?: string
  field_type?: string
  is_enabled?: number
  options?: CustomFieldOption[]
}

interface AttachmentRow {
  id: string
  name: string
  sizeLabel: string
  url: string
}

interface CommentRow {
  id: string
  author: string
  avatar: string
  time: string
  text: string
  html: string
}

interface AttributeRow {
  label: string
  value: string
  pillTone?: string
}

interface DetailRow {
  label: string
  value: string
}

/** 「任务信息」页签的行：比属性行多一个行首图标块，值可选描边胶囊或带圆形头像。 */
interface TaskInfoRow extends DetailRow {
  icon: Component
  /** 值的展示形态：给定则渲染为描边胶囊，取值同任务详情面板的配色 class。 */
  pillTone?: string
  /** 值前面的 20×20 圆形头像。 */
  avatar?: string
}

const activeTab = ref<DetailTab>('attributes')
const collapsed = ref(false)
const loading = ref(false)
const detail = ref<Record<string, unknown>>()
const cloudBaseURL = ref('')
const customFieldDefs = ref<CustomFieldDefinition[]>([])
const localTask = ref<LocalTaskDetail>()
const comments = ref<CommentRow[]>([])
const attachments = ref<AttachmentRow[]>([])
const previewImageVisible = ref(false)
const previewImageUrl = ref('')

const hasLocalTask = computed(() => Boolean(props.item?.local_task_uuid))

const detailTabs = computed<Array<{ key: DetailTab; label: string }>>(() => {
  const tabs: Array<{ key: DetailTab; label: string }> = [
    { key: 'attributes', label: t('teamwork.workItem.attributes') },
  ]
  if (hasLocalTask.value) {
    tabs.push({ key: 'taskInfo', label: t('teamwork.workItem.taskInfo') })
  }
  return tabs
})

watch(
  () =>
    [props.open, props.item?.id, props.item?.workspace_id, props.item?.local_task_uuid] as const,
  async () => {
    activeTab.value = 'attributes'
    collapsed.value = false
    detail.value = undefined
    cloudBaseURL.value = ''
    customFieldDefs.value = []
    localTask.value = undefined
    comments.value = []
    attachments.value = []
    previewImageVisible.value = false
    previewImageUrl.value = ''
    if (!props.open || !props.item) return

    loading.value = true
    try {
      const taskUuid = props.item.local_task_uuid
      const workspaceId = Number(props.item.workspace_id)
      const workItemId = Number(props.item.id)
      const requests: Array<Promise<unknown>> = [
        workspaceId && workItemId
          ? apiClient
              .get<WorkItemDetailPayload>(`/team/work-items/${workspaceId}/${workItemId}`)
              .then((result) => {
                detail.value = result.item || undefined
                customFieldDefs.value = result.custom_fields || []
                attachments.value = (result.attachments || [])
                  .map(toAttachment)
                  .filter((file) => file.name)
                comments.value = (result.comments || []).map(toComment).reverse()
              })
              .catch(() => {
                // 详情拉取失败时退回「我的工作」列表字段，正文和属性仍可查看。
                detail.value = props.item as unknown as Record<string, unknown>
              })
          : Promise.resolve(),
        apiClient
          .get<CloudConfigPayload>('/config/cloud')
          .then((config) => {
            cloudBaseURL.value = config.api_base_url?.trim() || ''
          })
          .catch(() => {
            // 云端地址只影响外部跳转，详情本身仍可正常查看。
          }),
      ]
      if (taskUuid) {
        requests.push(
          apiClient
            .get<LocalTaskDetail>(`/tasks/${encodeURIComponent(taskUuid)}`)
            .then((task) => {
              localTask.value = task
            })
            .catch(() => {
              // 本地任务读取失败只影响「任务信息」页签，属性仍可查看。
            }),
        )
      }
      await Promise.all(requests)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const descriptionText = computed(
  () => readString(detail.value?.description) || readString(props.item?.description),
)
const descriptionHTML = computed(() => (isHTML(descriptionText.value) ? descriptionText.value : ''))

const workItemURL = computed(() => {
  const workspaceId = Number(detail.value?.workspace_id ?? props.item?.workspace_id)
  const workItemId = Number(detail.value?.id ?? props.item?.id)
  if (!Number.isSafeInteger(workspaceId) || workspaceId <= 0) return ''
  if (!Number.isSafeInteger(workItemId) || workItemId <= 0) return ''

  try {
    const baseURL = new URL(cloudBaseURL.value)
    if (baseURL.protocol !== 'https:' && baseURL.protocol !== 'http:') return ''
    if (baseURL.username || baseURL.password) return ''
    return new URL(`/workspace/${workspaceId}/work-items/${workItemId}`, baseURL).toString()
  } catch {
    return ''
  }
})

function handleRichTextClick(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof HTMLImageElement)) return
  const imageURL = target.currentSrc || target.src
  if (!imageURL) return
  event.preventDefault()
  previewImageUrl.value = imageURL
  previewImageVisible.value = true
}

/** 设计稿 3205:37823 的属性行：父需求、状态、优先级、迭代、处理人、预计起止、自定义字段、创建信息。 */
const attributeRows = computed<AttributeRow[]>(() => {
  const source = (detail.value || props.item) as unknown as Record<string, unknown> | undefined
  if (!source) return []
  const priority = readString(source.priority_name)
  const status = readString(source.status_name)
  return [
    {
      label: t('teamwork.workItem.parentRequirement'),
      value: readString(source.parent_title) || '-',
    },
    {
      label: t('teamwork.workItem.status'),
      value: status || '-',
      pillTone: status ? 'status-pending' : '',
    },
    {
      label: t('teamwork.workItem.priority'),
      value: priority || '-',
      pillTone: priorityTone(priority),
    },
    { label: t('teamwork.workItem.iteration'), value: readString(source.version_name) || '-' },
    {
      label: t('teamwork.workItem.assignee'),
      value: toStringList(source.assignee_names).join('、') || '-',
    },
    { label: t('teamwork.workItem.plannedStart'), value: formatDate(source.planned_start_date) },
    { label: t('teamwork.workItem.plannedEnd'), value: formatDate(source.planned_end_date) },
    ...customFieldRows(source),
    { label: t('teamwork.workItem.createdAt'), value: formatDateTime(source.created_at) },
    { label: t('teamwork.workItem.creator'), value: readString(source.creator_name) || '-' },
  ]
})

function customFieldRows(source: Record<string, unknown>): AttributeRow[] {
  const raw = source.custom_fields
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
  const values = raw as Record<string, unknown>
  return customFieldDefs.value
    .filter((field) => field.is_enabled !== 0)
    .flatMap((field) => {
      const stored = values[String(field.id)]
      if (stored === undefined || stored === null || readString(stored) === '') return []
      return [
        {
          label: readString(field.name) || readString(field.key),
          value: customFieldValue(field, stored),
        },
      ]
    })
}

/** 选项型字段存的是选项 key（多选是 JSON 数组），需要换回选项名称。 */
function customFieldValue(field: CustomFieldDefinition, stored: unknown) {
  const options = new Map(
    (field.options || []).map((option) => [readString(option.key), readString(option.name)]),
  )
  const resolve = (value: unknown) => {
    const text = readString(value)
    return options.get(text) || text
  }
  if (Array.isArray(stored)) return stored.map(resolve).filter(Boolean).join('、')
  const text = readString(stored)
  if (text.startsWith('[')) {
    try {
      const parsed = JSON.parse(text)
      if (Array.isArray(parsed)) return parsed.map(resolve).filter(Boolean).join('、')
    } catch {
      // 不是合法 JSON 就按普通文本展示。
    }
  }
  return resolve(text)
}

/** 设计稿 3173:30534 共 9 行；「截止日期」取本地任务的 planned_end_date。 */
const taskInfoRows = computed<TaskInfoRow[]>(() => {
  const task = localTask.value
  if (!task) return []
  const priority = task.priority?.trim() || ''
  return [
    {
      label: t('teamwork.workItem.workDir'),
      value: task.work_dir?.trim() || '-',
      icon: FolderOutlined,
    },
    {
      label: t('teamwork.workItem.taskPriority'),
      value: priority ? priorityLabel(priority) : '-',
      icon: CheckSquareOutlined,
      pillTone: priorityTone(priority),
    },
    {
      label: t('teamwork.workItem.taskStatus'),
      value: taskStatusLabel(task.status),
      icon: CheckOutlined,
      pillTone: taskStatusTone(task.status),
    },
    {
      label: t('teamwork.workItem.project'),
      value: task.project_name?.trim() || '-',
      icon: ProjectOutlined,
      avatar: task.project_icon?.trim() || '',
    },
    {
      label: t('teamwork.workItem.pipeline'),
      value: pipelineName(task),
      icon: ShareAltOutlined,
      avatar: pipelineAvatar(task),
    },
    {
      label: t('teamwork.workItem.plannedRange'),
      value: plannedRange(task),
      icon: CalendarOutlined,
    },
    {
      label: t('teamwork.workItem.dueDate'),
      value: formatDate(task.planned_end_date),
      icon: CalendarOutlined,
    },
    {
      label: t('teamwork.workItem.relatedProjects'),
      value:
        (task.projects || [])
          .map((project) => project.name || '')
          .filter(Boolean)
          .join('、') || '-',
      icon: AppstoreOutlined,
      avatar: (task.projects || []).map((project) => project.icon || '').filter(Boolean)[0] || '',
    },
    {
      label: t('teamwork.workItem.relatedDirs'),
      value: (task.work_dirs || []).filter(Boolean).join('、') || '-',
      icon: FolderOutlined,
    },
  ]
})

function toAttachment(row: Record<string, unknown>): AttachmentRow {
  const size = Number(row.file_size ?? row.size ?? 0)
  return {
    id: String(row.id ?? row.uuid ?? row.file_name ?? ''),
    name: readString(row.original_name) || readString(row.file_name) || readString(row.name),
    sizeLabel: formatSize(size),
    url: readString(row.url) || readString(row.stored_path),
  }
}

function toComment(row: Record<string, unknown>): CommentRow {
  const content = readString(row.content)
  return {
    id: String(row.id ?? row.uuid ?? content),
    author:
      readString(row.user_name) ||
      readString(row.author_name_snapshot) ||
      t('teamwork.board.unassigned'),
    avatar: readString(row.user_avatar) || readString(row.author_avatar_snapshot),
    time: formatDateTime(row.created_at),
    text: isHTML(content) ? '' : content,
    html: isHTML(content) ? content : '',
  }
}

function readString(value: unknown) {
  return typeof value === 'string' ? value.trim() : value == null ? '' : String(value).trim()
}

function toStringList(value: unknown) {
  if (Array.isArray(value)) return value.map((entry) => readString(entry)).filter(Boolean)
  return readString(value) ? [readString(value)] : []
}

function isHTML(value: string) {
  return /<[a-z][\s\S]*>/i.test(value)
}

function formatSize(size: number) {
  if (!Number.isFinite(size) || size <= 0) return ''
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

function priorityLabel(priority: string) {
  if (priority === '高' || priority === 'high') return t('workflows.task.priority.high')
  if (priority === '中' || priority === 'medium') return t('workflows.task.priority.medium')
  if (priority === '低' || priority === 'low') return t('workflows.task.priority.low')
  return priority
}

/** 优先级胶囊配色，沿用任务详情面板既有的 .priority-* 约定。 */
function priorityTone(priority: string) {
  if (priority === '高' || priority === 'high') return 'priority-高'
  if (priority === '中' || priority === 'medium') return 'priority-中'
  if (priority === '低' || priority === 'low') return 'priority-低'
  return ''
}

/** 状态胶囊配色，沿用任务详情面板既有的 .status-* 约定。 */
function taskStatusTone(status?: string) {
  if (status === 'pending') return 'status-pending'
  if (status === 'active') return 'status-active'
  if (status === 'done') return 'status-done'
  if (status === 'blocked') return 'status-blocked'
  return ''
}

function pipelineName(task: LocalTaskDetail) {
  return task.pipeline_name_snapshot?.trim() || task.expert_group_name_snapshot?.trim() || '-'
}

function pipelineAvatar(task: LocalTaskDetail) {
  return task.pipeline_avatar_snapshot?.trim() || task.expert_group_avatar_snapshot?.trim() || ''
}

function taskStatusLabel(status?: string) {
  if (!status) return '-'
  if (status === 'pending') return t('workflows.task.status.pending')
  if (status === 'active') return t('workflows.task.status.inProgress')
  if (status === 'done') return t('workflows.task.status.done')
  if (status === 'blocked') return t('workflows.task.status.blocked')
  return status
}

function plannedRange(task: LocalTaskDetail) {
  const start = formatDate(task.planned_start_date)
  const end = formatDate(task.planned_end_date)
  if (start === '-' && end === '-') return '-'
  return `${start} ～ ${end}`
}

/** 云端日期字段可能是时间戳，也可能是已经格式化好的字符串，两种都要能直接展示。 */
function toDate(value?: unknown) {
  if (value === undefined || value === null || value === '') return undefined
  if (typeof value === 'string' && !/^\d+$/.test(value)) {
    const parsed = new Date(value.replace(' ', 'T'))
    return Number.isNaN(parsed.getTime()) ? undefined : parsed
  }
  const numeric = Number(value)
  if (!Number.isFinite(numeric) || numeric <= 0) return undefined
  return new Date(numeric < 1e12 ? numeric * 1000 : numeric)
}

function formatDate(value?: unknown) {
  return toDate(value)?.toLocaleDateString(locale.value) ?? '-'
}

function formatDateTime(value?: unknown) {
  return toDate(value)?.toLocaleString(locale.value) ?? '-'
}
</script>

<style scoped>
.work-item-root {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  justify-content: flex-end;
}

.work-item-mask {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
}

/* 设计稿 3173:30488：右侧抽屉 922×900，仅左侧圆角 16。 */
.work-item-panel {
  position: relative;
  z-index: 1;
  display: flex;
  width: 922px;
  max-width: 100%;
  height: 100%;
  flex-direction: column;
  overflow: hidden;
  border-radius: 16px 0 0 16px;
  background: #fff;
}

.work-item-panel :deep(.ant-spin-nested-loading),
.work-item-panel :deep(.ant-spin-container) {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.work-item-header {
  display: flex;
  height: 56px;
  flex: 0 0 56px;
  box-sizing: border-box;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  border-bottom: 1px solid #f0f0f0;
}

.work-item-header strong {
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.work-item-close {
  display: inline-flex;
  width: 16px;
  height: 16px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  color: rgba(0, 0, 0, 0.45);
  background: transparent;
  cursor: pointer;
  font-size: 16px;
}

.work-item-close:hover,
.work-item-close:focus-visible {
  outline: none;
  color: #262626;
}

.work-item-body {
  display: flex;
  min-height: 0;
  flex: 1;
}

/* 展开时左列为 600、右栏为 322；右栏收起后左列自动占满剩余空间。 */
.work-item-main {
  display: flex;
  min-width: 0;
  box-sizing: border-box;
  flex: 1;
  flex-direction: column;
  gap: 24px;
  padding: 24px;
  overflow-y: auto;
}

.work-item-heading {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.work-item-title {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0;
  padding-bottom: 8px;
  border-bottom: 1px solid #f0f0f0;
  color: #262626;
  font-size: 22px;
  font-weight: 600;
  line-height: 32px;
  word-break: break-word;
}

.work-item-title-text {
  min-width: 0;
  flex: 1;
}

.work-item-external-link {
  display: inline-flex;
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  align-items: center;
  justify-content: center;
  margin-top: 2px;
  border-radius: 6px;
  color: #8c8c8c;
  font-size: 16px;
}

.work-item-external-link:hover {
  color: #3157e2;
  background: #eef2ff;
}

.work-item-external-link:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.work-item-aside-toggle {
  display: inline-flex;
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  align-items: center;
  justify-content: center;
  margin-top: 2px;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #5f6368;
  background: transparent;
  cursor: pointer;
  transition: background 0.16s ease, color 0.16s ease;
}

.work-item-aside-toggle:hover {
  color: #3157e2;
  background: #f0f1f3;
}

.work-item-aside-toggle:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.work-item-aside-toggle-icon {
  display: block;
  width: 20px;
  height: 20px;
  transform: rotate(180deg);
}

.work-item-description {
  color: #262626;
  font-size: 14px;
  line-height: 28px;
}

.rich-text {
  color: #262626;
  font-size: 14px;
  line-height: 28px;
  word-break: break-word;
}

.rich-text :deep(p) {
  margin: 0 0 8px;
}

.rich-text :deep(p:last-child) {
  margin-bottom: 0;
}

.rich-text :deep(img) {
  max-width: 100%;
}

.work-item-description :deep(img),
.comment-content.rich-text :deep(img) {
  cursor: zoom-in;
}

.work-item-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.work-item-section-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  color: #1d1d1f;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
}

.section-icon {
  font-size: 16px;
  color: #262626;
}

.section-count {
  color: #8c8c8c;
  font-weight: 400;
}

.work-item-section-empty {
  margin: 0;
  color: #8c8c8c;
  font-size: 13px;
  line-height: 22px;
}

.attachment-list,
.comment-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

/* 设计稿 3173:30502：附件行 552×36，浅底 #fafafa，圆角 12。 */
.attachment-row {
  display: flex;
  height: 36px;
  box-sizing: border-box;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 12px;
  color: inherit;
  background: #fafafa;
  text-decoration: none;
}

.attachment-row:hover .attachment-name {
  color: #3157e2;
}

.attachment-main {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.attachment-icon {
  flex: 0 0 16px;
  color: #262626;
  font-size: 16px;
}

.attachment-name {
  overflow: hidden;
  color: #262626;
  font-size: 12px;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.attachment-size {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

/* 设计稿 3173:30521：头像 32 + 竖向时间线，正文缩进。 */
.comment-item {
  display: flex;
  gap: 12px;
  padding-bottom: 16px;
}

.comment-rail {
  display: flex;
  width: 32px;
  flex: 0 0 32px;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.comment-avatar {
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  border-radius: 50%;
  object-fit: cover;
}

.comment-avatar-fallback {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #595959;
  background: #edeff2;
  font-size: 14px;
  font-weight: 600;
}

.comment-rail-line {
  width: 1px;
  min-height: 20px;
  flex: 1;
  border-radius: 1px;
  background: #d8dde5;
}

.comment-body {
  min-width: 0;
  flex: 1;
}

.comment-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 22px;
}

.comment-meta strong {
  color: #262626;
  font-weight: 600;
}

.comment-content {
  padding-top: 8px;
  margin: 0;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
  word-break: break-word;
}

.work-item-aside {
  display: flex;
  width: 322px;
  min-width: 322px;
  box-sizing: border-box;
  flex-direction: column;
  border-left: 1px solid #f0f0f0;
  background: #fff;
  transition:
    width 200ms ease,
    min-width 200ms ease;
}

.work-item-body.aside-collapsed .work-item-aside {
  width: 0;
  min-width: 0;
  overflow: hidden;
  border-left: 0;
}

.aside-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 16px 24px 0;
}

.aside-tabs {
  display: flex;
  align-items: center;
  gap: 12px;
}

.aside-tabs button {
  padding: 0;
  border: 0;
  color: #8c8c8c;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
}

.aside-tabs button.active {
  color: #262626;
  font-weight: 600;
}

.aside-tabs button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

/* 设计稿 3205:37823：单张信息卡，行高 35，标签左、值右。 */
.attribute-card {
  margin: 16px 24px 24px;
  padding: 14px 16px;
  overflow-y: auto;
  border: 1px solid #e8ecf2;
  border-radius: 12px;
  background: #fff;
  box-shadow: 0 6px 10px rgba(23, 35, 60, 0.04);
}

.attribute-row {
  display: flex;
  min-height: 35px;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
}

.attribute-row dt {
  width: 64px;
  flex: 0 0 64px;
  color: rgba(0, 0, 0, 0.45);
  font-size: 12px;
  line-height: 14px;
}

.attribute-row dd {
  min-width: 0;
  flex: 1;
  margin: 0;
  overflow: hidden;
  color: #182b50;
  font-size: 13px;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.attribute-pill {
  display: inline-flex;
  align-items: center;
  padding: 0 10px;
  border-radius: 12px;
  font-size: 12px;
  line-height: 24px;
}

.task-info-list {
  min-height: 0;
  flex: 1;
  margin: 0;
  padding: 24px;
  list-style: none;
  overflow-y: auto;
}

/* 设计稿 3173:30534：行距 24，行首 26×26 图标块。 */
.task-info-row {
  display: flex;
  gap: 8px;
  margin-bottom: 24px;
}

.task-info-icon {
  display: inline-flex;
  width: 26px;
  height: 26px;
  flex: 0 0 26px;
  align-items: center;
  justify-content: center;
  border-radius: 7px;
  color: #595959;
  background: #f2f4f7;
  font-size: 14px;
}

.task-info-body {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 4px;
}

.task-info-label {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.task-info-value {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  color: #242933;
  font-size: 14px;
  line-height: 22px;
}

.task-info-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-info-avatar {
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
  border-radius: 50%;
  object-fit: cover;
}

.task-info-pill {
  display: inline-flex;
  align-self: flex-start;
  align-items: center;
  padding: 0 8px;
  border-radius: 12px;
  font-size: 12px;
  line-height: 22px;
}

.attribute-pill.status-pending,
.task-info-pill.status-pending {
  color: #8c8c8c;
  background: #f2f4f7;
}

.attribute-pill.status-active,
.task-info-pill.status-active {
  color: #3157e2;
  background: #eef2ff;
}

.attribute-pill.status-done,
.task-info-pill.status-done {
  color: #16a34a;
  background: #e8f9ef;
}

.attribute-pill.status-blocked,
.task-info-pill.status-blocked {
  color: #ef4444;
  background: #fef2f2;
}

.attribute-pill.priority-高,
.task-info-pill.priority-高 {
  color: #ed744a;
  background: #fff5e5;
}

.attribute-pill.priority-中,
.task-info-pill.priority-中 {
  color: #d97706;
  background: #fff7ed;
}

.attribute-pill.priority-低,
.task-info-pill.priority-低 {
  color: #2563eb;
  background: #eff6ff;
}

.task-info-empty {
  color: #8c8c8c;
  font-size: 13px;
  line-height: 22px;
}

@media (max-width: 960px) {
  .work-item-panel {
    width: 100%;
    border-radius: 0;
  }

  .work-item-body {
    flex-direction: column;
    overflow-y: auto;
  }

  .work-item-main,
  .work-item-aside {
    width: 100%;
    min-width: 0;
  }

  .work-item-body.aside-collapsed .work-item-aside {
    width: 0;
    min-width: 0;
    height: 0;
    overflow: hidden;
    border: 0;
  }

  .work-item-aside {
    border-top: 1px solid #f0f0f0;
    border-left: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .work-item-aside {
    transition: none;
  }
}
</style>
