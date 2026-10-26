<template>
  <a-drawer
    :open="open"
    :width="drawerWidth"
    :closable="false"
    :mask-closable="true"
    :body-style="{ padding: 0, overflow: 'hidden' }"
    root-class-name="task-documents-drawer"
    @close="requestClose"
  >
    <div class="documents-drawer-shell">
      <div class="drawer-resize-handle" role="separator" tabindex="0"
        :aria-label="t('workflows.task.progress.resizeDocuments')" aria-orientation="vertical"
        :aria-valuemin="minDrawerWidth" :aria-valuemax="viewportWidth" :aria-valuenow="drawerWidth"
        @pointerdown="startResize" @pointermove="moveResize" @pointerup="finishResize"
        @pointercancel="finishResize" @lostpointercapture="finishResize" @keydown="resizeWithKeyboard" />
      <header class="documents-header">
        <div class="documents-heading">
          <span class="step-avatar">
            <img
              v-if="currentStep?.avatar"
              :src="currentStep.avatar"
              :alt="currentStep.name"
            />
            <span v-else>{{ initials(currentStep?.name) }}</span>
          </span>
          <h2>{{ currentStep?.name || t('workflows.task.progress.drawerCurrentStep') }}</h2>
          <button
            type="button"
            class="open-directory-button"
            :disabled="!taskDirectory || openingDirectory"
            :aria-label="openingDirectory ? t('workflows.task.progress.openingFolder') : t('workflows.task.progress.openInFolder')"
            :title="openingDirectory ? t('workflows.task.progress.openingFolder') : t('workflows.task.progress.openInFolder')"
            @click="openTaskDirectory"
          >
            <LoadingOutlined
              v-if="openingDirectory"
              spin
            />
            <img
              v-else
              :src="openDirectoryIcon"
              alt=""
              aria-hidden="true"
            />
          </button>
          <!-- 上报进度放在文件夹按钮右侧，不随步骤名长短移动 -->
          <a-tooltip
            v-if="uploadEnabled && !autoUploadEnabled"
            :title="t('workflows.task.progress.taskDocumentAutoSyncOffHint')"
          >
            <span class="documents-auto-sync-off">{{
              t('workflows.task.progress.taskDocumentAutoSyncOff')
            }}</span>
          </a-tooltip>
          <span
            v-if="uploadEnabled && uploadStats.total"
            class="documents-upload-count"
            :class="{ 'has-failed': uploadStats.failed > 0 }"
          >
            <span>{{ t('workflows.task.progress.taskDocumentUploadSynced', { count: uploadStats.synced }) }}</span>
            <span>{{ t('workflows.task.progress.taskDocumentUploadTotal', { count: uploadStats.total }) }}</span>
          </span>
        </div>
        <div class="documents-header-actions">
          <!-- 任务默认不上报文件时，提供一次性手动上报入口 -->
          <button
            v-if="uploadEnabled && !autoUploadEnabled"
            type="button"
            class="report-files-button"
            :disabled="reporting"
            :aria-label="t('workflows.task.progress.taskDocumentReportNow')"
            @click="reportAllFiles"
          >
            <LoadingOutlined
              v-if="reporting"
              spin
            />
            <CloudUploadOutlined v-else />
            <span>{{ t('workflows.task.progress.taskDocumentReportNow') }}</span>
          </button>
          <a-tooltip
            v-if="uploadEnabled && uploadStats.failed > 0"
            :title="t('workflows.task.progress.taskDocumentReuploadAll')"
            placement="bottomRight"
          >
            <button
              type="button"
              class="reupload-all-button"
              :disabled="reuploadingAll"
              :aria-label="t('workflows.task.progress.taskDocumentReuploadAll')"
              @click="reuploadFailed"
            >
              <LoadingOutlined
                v-if="reuploadingAll"
                spin
              />
              <CloudUploadOutlined v-else />
            </button>
          </a-tooltip>
          <button
            type="button"
            class="close-button"
            :aria-label="t('workflows.task.progress.closeDocuments')"
            @click="requestClose"
          >
            <CloseOutlined />
          </button>
        </div>
      </header>

      <div class="documents-workspace" :class="{ 'is-directory-collapsed': sidebarCollapsed }">
        <aside v-show="!sidebarCollapsed" class="file-sidebar">
          <div
            v-if="treeLoading"
            class="sidebar-state"
          >
            <a-spin />
          </div>
          <div
            v-else-if="treeError"
            class="sidebar-state sidebar-error"
          >
            <span>{{ treeError }}</span>
            <a-button
              size="small"
              @click="loadTree"
            >
              <ReloadOutlined />{{ t('common.actions.retry') }}
            </a-button>
          </div>
          <a-empty
            v-else-if="!treeData.length"
            :image="false"
            :description="t('workflows.task.progress.noDocuments')"
          />
          <a-tree
            v-else
            block-node
            :tree-data="treeData"
            :selected-keys="selectedKeys"
            :expanded-keys="expandedKeys"
            @expand="expandedKeys = $event.map(String)"
            @select="handleSelect"
          >
            <template #title="{ title, isDir, fileType, meta, source }">
              <div class="file-tree-node">
                <FolderOutlined
                  v-if="isDir"
                  class="file-node-icon folder-icon"
                />
                <FileMarkdownOutlined
                  v-else-if="isMarkdown(fileType)"
                  class="file-node-icon markdown-icon"
                />
                <FileImageOutlined
                  v-else-if="isImageType(fileType)"
                  class="file-node-icon image-icon"
                />
                <FileTextOutlined
                  v-else-if="isTextType(fileType)"
                  class="file-node-icon text-icon"
                />
                <FileOutlined
                  v-else
                  class="file-node-icon"
                />
                <span
                  class="file-node-name"
                  :title="title"
                  >{{ title }}</span
                >
                <span
                  v-if="meta"
                  class="file-node-meta"
                  >{{ meta }}</span
                >
                <!-- 目录：聚合子项的上报进度，折叠状态下也能看出是否有失败 -->
                <a-tooltip
                  v-if="isDir && uploadSummaryLabel(source)"
                  :title="uploadSummaryTooltip(source)"
                >
                  <span
                    class="file-node-upload-summary"
                    :class="{ 'has-failed': (source?.upload_summary?.failed || 0) > 0 }"
                    >{{ uploadSummaryLabel(source) }}</span
                  >
                </a-tooltip>
                <!-- 文件：产出文档上报状态，失败态点击即重新上报；悬停显示状态与失败原因 -->
                <a-tooltip
                  v-if="!isDir && source?.upload"
                  :title="uploadTooltip(source.upload)"
                  placement="topLeft"
                >
                  <span
                    class="file-node-upload"
                    :class="`is-${source.upload.status}`"
                  >
                    <button
                      v-if="source.upload.status === 'failed'"
                      type="button"
                      class="upload-action"
                      :aria-label="t('workflows.task.progress.taskDocumentReupload')"
                      @click.stop="reuploadFile(source)"
                    >
                      <ExclamationCircleFilled />
                    </button>
                    <span
                      v-else
                      class="upload-indicator"
                    >
                      <CheckCircleFilled v-if="source.upload.status === 'synced'" />
                      <LoadingOutlined
                        v-else-if="source.upload.status === 'uploading'"
                        spin
                      />
                      <CloudUploadOutlined v-else-if="source.upload.status === 'pending'" />
                      <StopOutlined v-else />
                    </span>
                    <!-- 失败态悬停展开「重新上报」文字按钮 -->
                    <button
                      v-if="source.upload.status === 'failed'"
                      type="button"
                      class="upload-reupload-text"
                      :disabled="reuploading"
                      @click.stop="reuploadFile(source)"
                    >
                      {{ t('workflows.task.progress.taskDocumentReupload') }}
                    </button>
                  </span>
                </a-tooltip>
                <!-- 非目录且拿得到绝对路径的文件，提供一键引用到输入框（没有输入框的形态下隐藏） -->
                <button
                  v-if="!isDir && source?.absolute_path && referenceEnabled"
                  type="button"
                  class="file-node-reference"
                  :title="t('workflows.task.progress.referenceToInput')"
                  :aria-label="t('workflows.task.progress.referenceToInput')"
                  @click.stop="emit('reference', source)"
                >
                  <LinkOutlined />
                </button>
              </div>
            </template>
          </a-tree>
        </aside>

        <main class="file-workspace">
          <div class="file-toolbar">
            <button
              type="button"
              class="directory-collapse-btn"
              :title="sidebarCollapsed ? t('workflows.task.progress.expandDirectory') : t('workflows.task.progress.collapseDirectory')"
              :aria-label="sidebarCollapsed ? t('workflows.task.progress.expandDirectory') : t('workflows.task.progress.collapseDirectory')"
              :aria-expanded="!sidebarCollapsed"
              @click="sidebarCollapsed = !sidebarCollapsed"
            >
              <img
                :src="toggleDirectoryIcon"
                alt=""
                aria-hidden="true"
                :class="{ 'is-collapsed': sidebarCollapsed }"
              />
            </button>
            <div
              v-if="selectedFile"
              class="file-toolbar-copy"
            >
              <!-- 上报状态与提醒文案同一行、紧随其后：不随文件名长短变化 -->
              <div class="file-toolbar-meta">
                <span class="save-hint"><EditOutlined />{{
                  uploadEnabled && !autoUploadEnabled
                    ? t('workflows.task.progress.saveHintNoSync')
                    : t('workflows.task.progress.saveHint')
                }}</span>
                <!-- 悬停显示完整状态；失败时气泡里给出服务端返回的原因 -->
                <a-tooltip
                  v-if="uploadEnabled && selectedUpload"
                  :title="uploadTooltip(selectedUpload)"
                  placement="bottomLeft"
                >
                  <span
                    class="selected-upload-status"
                    :class="`is-${selectedUpload.status}`"
                  >
                    <LoadingOutlined
                      v-if="selectedUpload.status === 'uploading'"
                      spin
                    />
                    <CheckCircleFilled v-else-if="selectedUpload.status === 'synced'" />
                    <CloudUploadOutlined v-else-if="selectedUpload.status === 'pending'" />
                    <ExclamationCircleFilled v-else-if="selectedUpload.status === 'failed'" />
                    <StopOutlined v-else />
                    <span>{{ uploadStatusText(selectedUpload) }}</span>
                    <span
                      v-if="selectedUpload.uploaded_at"
                      class="selected-upload-time"
                      >{{ t('workflows.task.progress.taskDocumentUploadedAt', { time: formatUploadTime(selectedUpload.uploaded_at) }) }}</span
                    >
                    <button
                      type="button"
                      class="selected-upload-action"
                      :disabled="reuploading || !selectedUpload.reuploadable"
                      @click="reuploadSelected"
                    >
                      {{ t('workflows.task.progress.taskDocumentReupload') }}
                    </button>
                  </span>
                </a-tooltip>
              </div>
              <div class="file-toolbar-title">
                <strong>
                  <FileImageOutlined v-if="selectedContent?.encoding === 'base64'" />
                  <FileTextOutlined v-else />
                  {{ selectedFile.name }}
                </strong>
              </div>
            </div>
            <div v-if="selectedFile" class="file-toolbar-actions">
            <a-button
              v-if="selectedContent?.encoding !== 'base64'"
              type="primary"
              :loading="saving"
              :disabled="contentLoading || Boolean(contentError) || !dirty"
              @click="saveFile"
            >
              {{ t('common.actions.save') }}
            </a-button>
            </div>
          </div>

          <div class="file-content">
            <div
              v-if="contentLoading"
              class="content-state"
            >
              <a-spin />
            </div>
            <div
              v-else-if="contentError"
              class="content-state content-error"
            >
              <a-alert
                type="error"
                show-icon
                :message="contentError"
              />
              <a-button @click="retrySelectedFile">{{ t('common.actions.retry') }}</a-button>
            </div>
            <a-empty
              v-else-if="!selectedFile || !selectedContent"
              :description="t('workflows.task.progress.chooseDocument')"
            />
            <div
              v-else-if="selectedContent.encoding === 'base64'"
              class="image-preview"
            >
              <img
                :src="imageDataUrl"
                :alt="selectedFile.name"
              />
            </div>
            <TiptapMarkdownEditor
              v-else-if="canUseDocumentEditor"
              :key="selectedFile.path"
              v-model="draftContent"
              class="file-markdown-editor"
              :resolve-image-src="resolveDocumentImage"
              :placeholder="t('workflows.task.progress.editFile', { name: selectedFile.name })"
              :disabled="saving"
            />
            <a-textarea
              v-else
              v-model:value="draftContent"
              class="file-editor"
              :disabled="saving"
              :aria-label="t('workflows.task.progress.editFile', { name: selectedFile.name })"
            />
          </div>
        </main>
      </div>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  CheckCircleFilled,
  CloseOutlined,
  CloudUploadOutlined,
  EditOutlined,
  ExclamationCircleFilled,
  FileImageOutlined,
  FileMarkdownOutlined,
  FileOutlined,
  FileTextOutlined,
  FolderOutlined,
  LinkOutlined,
  LoadingOutlined,
  ReloadOutlined,
  StopOutlined,
} from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import TiptapMarkdownEditor from '@/components/TiptapMarkdownEditor.vue'
import { useAppI18n } from '@/i18n'

const { t, locale } = useAppI18n()
import openDirectoryIcon from '@/assets/icons/task-documents-open-directory.svg'
import toggleDirectoryIcon from '@/assets/icons/task-documents-toggle-directory.svg'
import { isDesktopRuntime, openDirectory } from '@/composables/useDesktop'
import { useLocalWS } from '@/composables/useLocalWebSocket'
import { copyText } from '@/utils/clipboard'
import { formatRelativeTime } from '@/utils/relativeTime'
import type {
  TaskFileContentResponse,
  TaskFileNode,
  TaskFileReportResponse,
  TaskFilesResponse,
  TaskFileUploadState,
  TaskFileUploadStatusResponse,
} from '@/types/task-files'
import type { PipelineStep } from '@/types/pipeline'
import { initials } from './utils'
import { createTaskDocumentImageResolver } from './taskDocumentImages'

interface FileTreeNode {
  key: string
  title: string
  path: string
  name: string
  isDir: boolean
  fileType: string
  meta: string
  source: TaskFileNode
  children?: FileTreeNode[]
}

const props = withDefaults(
  defineProps<{
    open: boolean
    taskUuid: string
    currentStep?: PipelineStep
    /** 没有输入框的形态（如 Vibe Coding）下隐藏「引用到输入框」入口 */
    referenceEnabled?: boolean
  }>(),
  { referenceEnabled: true },
)

const emit = defineEmits<{
  'update:open': [open: boolean]
  /** 把某个产出文件引用进输入框（插入 #[名称] 标记） */
  reference: [file: import('@/types/task-files').MentionableTaskFile]
}>()

const treeData = ref<FileTreeNode[]>([])
const fileMap = ref(new Map<string, FileTreeNode>())
const selectedKeys = ref<string[]>([])
const expandedKeys = ref<string[]>([])
const viewportWidth = ref(typeof window === 'undefined' ? 1097 : window.innerWidth)
const minDrawerWidth = computed(() => Math.min(640, viewportWidth.value))
const drawerWidth = ref(Math.min(1097, viewportWidth.value))
const sidebarCollapsed = ref(false)
let dragging = false
let resizeStartX = 0
let resizeStartWidth = 1097
let liveWidth = 1097
let resizeWrapper: HTMLElement | null = null
let resizeRaf = 0
const selectedFile = ref<TaskFileNode>()
const selectedContent = ref<TaskFileContentResponse>()
const draftContent = ref('')
const resolveDocumentImage = ref<(src: string) => Promise<string>>()
const canUseDocumentEditor = computed(() => Boolean(selectedContent.value && selectedContent.value.encoding !== 'base64' && (isMarkdown(selectedFile.value?.file_type) || normalizedFileType(selectedFile.value?.file_type) === 'txt')))
const savedContent = ref('')
const treeLoading = ref(false)
const contentLoading = ref(false)
const saving = ref(false)
const openingDirectory = ref(false)
const taskDirectory = ref('')
const treeError = ref('')
const contentError = ref('')
// 产出文档上报：开关由后端配置决定，关闭时完全不显示状态
const uploadEnabled = ref(false)
// 任务级「同步agent产出」：false 表示该任务默认不上报文件，需要手动上报
const autoUploadEnabled = ref(true)
const reporting = ref(false)
const reuploading = ref(false)
const reuploadingAll = ref(false)
let treeLoadVersion = 0
let contentLoadVersion = 0
let saveVersion = 0
let statusPollTimer: ReturnType<typeof setInterval> | null = null
let treeReloadTimer: ReturnType<typeof setTimeout> | null = null

/** 当前打开文件的上报状态（来自 tree 节点的 upload 字段） */
const selectedUpload = computed<TaskFileUploadState | undefined>(() => {
  const path = selectedFile.value?.path
  if (!path) return undefined
  return fileMap.value.get(path)?.source.upload || undefined
})

/** 抽屉头部的「已上报 N / 共 M」计数 */
const uploadStats = computed(() => {
  const stats = { total: 0, synced: 0, failed: 0, pending: 0, skipped: 0 }
  const walk = (nodes: FileTreeNode[]) => {
    for (const node of nodes) {
      if (node.isDir) {
        walk(node.children || [])
        continue
      }
      const status = node.source.upload?.status
      // 不参与上报的文件（白名单外、已删除）不计入分母，
      // 否则 .exe/.zip 这类跳过项会让「已上报 N / 共 M」永远到不了 M
      if (status === 'skipped' || status === 'deleted') {
        stats.skipped += 1
        continue
      }
      stats.total += 1
      switch (status) {
        case 'synced':
          stats.synced += 1
          break
        case 'failed':
          stats.failed += 1
          break
        default:
          stats.pending += 1
      }
    }
  }
  walk(treeData.value)
  return stats
})

const dirty = computed(
  () =>
    Boolean(selectedContent.value) &&
    selectedContent.value?.encoding !== 'base64' &&
    draftContent.value !== savedContent.value,
)

const imageDataUrl = computed(() => {
  const content = selectedContent.value
  if (!content || content.encoding !== 'base64') return ''
  const type = normalizedFileType(content.file_type)
  const mime =
    {
      jpg: 'image/jpeg',
      jpeg: 'image/jpeg',
      svg: 'image/svg+xml',
      png: 'image/png',
      gif: 'image/gif',
      webp: 'image/webp',
      bmp: 'image/bmp',
      ico: 'image/x-icon',
    }[type] || `image/${type}`
  return `data:${mime};base64,${content.content}`
})

watch(
  () => [props.open, props.taskUuid] as const,
  ([open]) => {
    if (open) void loadTree()
    else resetState()
  },
)

async function loadTree(preservePath?: string) {
  if (!props.taskUuid) {
    treeError.value = t('workflows.task.progress.taskMissing')
    return
  }
  const requestVersion = ++treeLoadVersion
  contentLoadVersion += 1
  saveVersion += 1
  saving.value = false
  treeLoading.value = true
  treeError.value = ''
  taskDirectory.value = ''
  clearSelection()
  try {
    const result = await apiClient.get<TaskFilesResponse>(
      `/tasks/${encodeURIComponent(props.taskUuid)}/files`,
    )
    if (requestVersion !== treeLoadVersion) return
    taskDirectory.value = result.task_dir || ''
    uploadEnabled.value = Boolean(result.upload_enabled)
    autoUploadEnabled.value = result.auto_upload_enabled !== false
    const nextMap = new Map<string, FileTreeNode>()
    treeData.value = prioritizeTaskDocument(result.tree || []).map((node) =>
      mapTreeNode(node, nextMap),
    )
    fileMap.value = nextMap
    expandedKeys.value = [...nextMap.values()].filter((node) => node.isDir).map((node) => node.key)
    const preserved = preservePath ? nextMap.get(preservePath) : undefined
    const target = preserved && !preserved.isDir ? preserved : findFirstFile(treeData.value)
    if (target) await loadFile(target)
    startUploadStatusPoll()
  } catch (error) {
    if (requestVersion !== treeLoadVersion) return
    treeError.value = error instanceof Error ? error.message : t('workflows.task.progress.documentTreeFailed')
  } finally {
    if (requestVersion === treeLoadVersion) treeLoading.value = false
  }
}

function prioritizeTaskDocument(nodes: TaskFileNode[]) {
  return [...nodes].sort((left, right) => {
    const leftIsTaskDocument = !left.is_dir && left.name.toLowerCase() === 'task.md'
    const rightIsTaskDocument = !right.is_dir && right.name.toLowerCase() === 'task.md'
    if (leftIsTaskDocument === rightIsTaskDocument) return 0
    return leftIsTaskDocument ? -1 : 1
  })
}

function mapTreeNode(node: TaskFileNode, target: Map<string, FileTreeNode>): FileTreeNode {
  const children = (node.children || []).map((child) => mapTreeNode(child, target))
  const mapped: FileTreeNode = {
    key: node.path,
    title: node.name,
    path: node.path,
    name: node.name,
    isDir: node.is_dir,
    fileType: normalizedFileType(node.file_type),
    meta: node.is_dir
      ? children.length
        ? String(children.length)
        : ''
      : formatFileSize(node.size),
    source: node,
    children: node.is_dir ? children : undefined,
  }
  target.set(node.path, mapped)
  return mapped
}

function startResize(event: PointerEvent) {
  if (event.button !== 0 || dragging) return
  event.preventDefault()
  dragging = true
  resizeStartX = event.clientX
  resizeStartWidth = drawerWidth.value
  liveWidth = drawerWidth.value
  resizeWrapper = document.querySelector<HTMLElement>('.task-documents-drawer .ant-drawer-content-wrapper')
  document.querySelector('.task-documents-drawer')?.classList.add('is-resizing')
  if (resizeWrapper) resizeWrapper.style.transition = 'none'
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}
function moveResize(event: PointerEvent) {
  if (!dragging) return
  liveWidth = Math.max(minDrawerWidth.value, Math.min(viewportWidth.value, resizeStartWidth + resizeStartX - event.clientX))
  if (!resizeRaf) resizeRaf = requestAnimationFrame(flushResizeFrame)
}
function flushResizeFrame() {
  resizeRaf = 0
  if (resizeWrapper) resizeWrapper.style.width = `${liveWidth}px`
}
function finishResize() {
  if (!dragging) return
  if (resizeRaf) {
    cancelAnimationFrame(resizeRaf)
    resizeRaf = 0
    flushResizeFrame()
  }
  dragging = false
  drawerWidth.value = liveWidth
  document.querySelector('.task-documents-drawer')?.classList.remove('is-resizing')
  if (resizeWrapper) resizeWrapper.style.transition = ''
  resizeWrapper = null
}
function resizeWithKeyboard(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const next = event.key === 'Home' ? minDrawerWidth.value : event.key === 'End' ? viewportWidth.value
    : drawerWidth.value + (event.key === 'ArrowLeft' ? 24 : -24)
  drawerWidth.value = Math.max(minDrawerWidth.value, Math.min(viewportWidth.value, next))
}
function updateViewport() {
  viewportWidth.value = window.innerWidth
  drawerWidth.value = Math.max(minDrawerWidth.value, Math.min(viewportWidth.value, drawerWidth.value))
}
onMounted(() => window.addEventListener('resize', updateViewport))
onBeforeUnmount(() => {
  window.removeEventListener('resize', updateViewport)
  if (resizeRaf) cancelAnimationFrame(resizeRaf)
  resetState()
})
function findFirstFile(nodes: FileTreeNode[]): FileTreeNode | undefined {
  for (const node of nodes) {
    if (!node.isDir) return node
    const child = findFirstFile(node.children || [])
    if (child) return child
  }
  return undefined
}

function handleSelect(keys: Array<string | number>) {
  if (saving.value) return
  const path = String(keys[0] || '')
  const node = fileMap.value.get(path)
  if (!node || node.isDir || node.path === selectedFile.value?.path) return
  if (!dirty.value) {
    void loadFile(node)
    return
  }
  Modal.confirm({
    title: t('workflows.task.progress.discardFileTitle'),
    content: t('workflows.task.progress.discardFileContent'),
    okText: t('workflows.task.progress.discardAndSwitch'),
    cancelText: t('workflows.task.progress.continueEditing'),
    onOk: () => loadFile(node),
  })
}

async function loadFile(node: FileTreeNode) {
  if (!props.taskUuid || node.isDir) return
  const requestVersion = ++contentLoadVersion
  selectedKeys.value = [node.path]
  selectedFile.value = node.source
  selectedContent.value = undefined
  draftContent.value = ''
  savedContent.value = ''
  contentLoading.value = true
  contentError.value = ''
  resolveDocumentImage.value = createTaskDocumentImageResolver(props.taskUuid, node.path, taskDirectory.value)
  try {
    const result = await apiClient.get<TaskFileContentResponse>(
      `/tasks/${encodeURIComponent(props.taskUuid)}/files/content`,
      { path: node.path },
    )
    if (requestVersion !== contentLoadVersion) return
    selectedContent.value = result
    draftContent.value = result.content || ''
    savedContent.value = draftContent.value
  } catch (error) {
    if (requestVersion !== contentLoadVersion) return
    contentError.value = error instanceof Error ? error.message : t('workflows.task.progress.readFailed', { name: node.name })
  } finally {
    if (requestVersion === contentLoadVersion) contentLoading.value = false
  }
}

function retrySelectedFile() {
  const node = selectedFile.value ? fileMap.value.get(selectedFile.value.path) : undefined
  if (node) void loadFile(node)
}

async function saveFile() {
  const file = selectedFile.value
  if (!file || !props.taskUuid || !dirty.value || saving.value) return
  const requestVersion = ++saveVersion
  const taskUuid = props.taskUuid
  const content = draftContent.value
  saving.value = true
  try {
    await apiClient.post(`/tasks/${encodeURIComponent(taskUuid)}/files/content`, {
      path: file.path,
      content,
    })
    if (requestVersion !== saveVersion) return
    savedContent.value = content
    // 关闭「同步agent产出」的任务保存后不会自动上报，文案不能再说「正在同步」
    const willAutoReport = uploadEnabled.value && autoUploadEnabled.value
    message.success(
      t(
        willAutoReport
          ? 'workflows.task.progress.documentSavedSyncing'
          : 'workflows.task.progress.documentSavedNoSync',
      ),
    )
    // 乐观更新：保存后立即进入「正在上报」，真实状态由状态轮询刷新
    if (willAutoReport) {
      const node = fileMap.value.get(file.path)
      if (node) {
        node.source.upload = {
          status: 'uploading',
          attempts: 0,
          last_error: '',
          uploaded_at: node.source.upload?.uploaded_at,
          doc_key: node.source.upload?.doc_key,
          remote_revision: node.source.upload?.remote_revision,
          reuploadable: true,
        }
      }
      startUploadStatusPoll()
    }
  } catch (error) {
    if (requestVersion !== saveVersion) return
    message.error(error instanceof Error ? error.message : t('workflows.task.progress.saveFailed', { name: file.name }))
  } finally {
    if (requestVersion === saveVersion) saving.value = false
  }
}

// ======================== 产出文档上报状态 ========================

/** 需要轮询的文件路径：仍在等待/正在上报的文件 */
function pendingUploadPaths() {
  const paths: string[] = []
  for (const node of fileMap.value.values()) {
    if (node.isDir) continue
    const status = node.source.upload?.status
    if (status === 'pending' || status === 'uploading') paths.push(node.path)
  }
  return paths
}

/** 状态轮询：只在存在待上报文件时运行，避免无意义请求 */
function startUploadStatusPoll() {
  if (!uploadEnabled.value || statusPollTimer || !props.open) return
  statusPollTimer = setInterval(() => {
    void pollUploadStatus()
  }, 3000)
}

function stopUploadStatusPoll() {
  if (!statusPollTimer) return
  clearInterval(statusPollTimer)
  statusPollTimer = null
}

async function pollUploadStatus() {
  if (!props.open || !props.taskUuid || !uploadEnabled.value) {
    stopUploadStatusPoll()
    return
  }
  const paths = pendingUploadPaths().slice(0, 50)
  if (!paths.length) {
    stopUploadStatusPoll()
    return
  }
  try {
    const result = await apiClient.get<TaskFileUploadStatusResponse>(
      `/tasks/${encodeURIComponent(props.taskUuid)}/files/upload-status`,
      { paths: paths.join(',') },
    )
    if (result.enabled === false) {
      // 该任务不参与上报（本地任务/开关关闭）：停止轮询，避免空转
      stopUploadStatusPoll()
      return
    }
    applyUploadStates(result.items || [])
  } catch {
    // 轮询失败不打扰用户，下一轮或重新打开抽屉时自然恢复
  }
  if (!pendingUploadPaths().length) stopUploadStatusPoll()
}

function applyUploadStates(items: Array<TaskFileUploadState & { path: string }>) {
  for (const item of items) {
    // 尚未入库的文件没有状态，不能覆盖当前的乐观状态
    if (!item.status) continue
    const node = findFileNodeByPath(item.path)
    if (!node) continue
    node.source.upload = {
      status: item.status,
      attempts: item.attempts || 0,
      last_error: item.last_error || '',
      uploaded_at: item.uploaded_at,
      doc_key: item.doc_key,
      remote_revision: item.remote_revision,
      reuploadable: item.reuploadable,
    }
  }
}

/**
 * 后端返回的相对路径一律正斜杠，而树节点的 path 来自本机文件系统（Windows 为反斜杠），
 * 跨端比对前必须归一，否则嵌套文件的状态更新会全部落空。
 */
function normalizeDocumentPath(path: string) {
  return path.replace(/\\/g, '/')
}

function findFileNodeByPath(path: string): FileTreeNode | undefined {
  const direct = fileMap.value.get(path)
  if (direct) return direct
  const target = normalizeDocumentPath(path)
  for (const node of fileMap.value.values()) {
    if (normalizeDocumentPath(node.path) === target) return node
  }
  return undefined
}

function formatUploadTime(timestamp?: number) {
  if (!timestamp) return ''
  return formatRelativeTime(timestamp, locale.value)
}

function uploadStatusText(upload?: TaskFileUploadState) {
  switch (upload?.status) {
    case 'synced':
      return t('workflows.task.progress.taskDocumentUploaded')
    case 'uploading':
      return t('workflows.task.progress.taskDocumentUploading')
    case 'failed':
      return t('workflows.task.progress.taskDocumentUploadFailed')
    case 'skipped':
      return t('workflows.task.progress.taskDocumentSkipped')
    case 'deleted':
      return t('workflows.task.progress.taskDocumentDeleted')
    default:
      return t('workflows.task.progress.taskDocumentPending')
  }
}

function uploadTooltip(upload?: TaskFileUploadState) {
  if (!upload) return ''
  const reason = (upload.last_error || '').trim()
  if (upload.status === 'failed') {
    return reason
      ? t('workflows.task.progress.taskDocumentFailedHint', { reason })
      : t('workflows.task.progress.taskDocumentFailedUnknown')
  }
  if (upload.status === 'skipped') {
    return reason
      ? t('workflows.task.progress.taskDocumentSkippedHint', { reason })
      : uploadStatusText(upload)
  }
  if (upload.status === 'synced') {
    const time = formatUploadTime(upload.uploaded_at)
    return time
      ? `${uploadStatusText(upload)} · ${time}`
      : uploadStatusText(upload)
  }
  return uploadStatusText(upload)
}

/** 目录节点的「已上报/总数」聚合文案 */
function uploadSummaryLabel(node?: TaskFileNode) {
  const summary = node?.upload_summary
  if (!uploadEnabled.value || !summary || summary.total <= 0) return ''
  return `${summary.synced}/${summary.total}`
}

function uploadSummaryTooltip(node?: TaskFileNode) {
  const summary = node?.upload_summary
  if (!summary) return ''
  return t('workflows.task.progress.taskDocumentSummaryHint', {
    synced: summary.synced,
    total: summary.total,
    failed: summary.failed,
    pending: summary.pending,
  })
}

async function reuploadFile(node: TaskFileNode) {
  if (!props.taskUuid || reuploading.value) return
  await requestReupload([node.path])
}

async function reuploadSelected() {
  const path = selectedFile.value?.path
  if (!path) return
  await requestReupload([path])
}

async function reuploadFailed() {
  const paths = pendingFailedPaths()
  if (!paths.length) return
  await requestReupload(paths.slice(0, 50), true)
}

/**
 * 任务默认不上报文件时的手动上报：把任务产出目录下符合上报规则的文件全部排入队列。
 * 这是唯一绕过任务级开关的入口，单文件重报仍走 /files/reupload。
 */
async function reportAllFiles() {
  if (!props.taskUuid || reporting.value) return
  const taskUuid = props.taskUuid
  reporting.value = true
  try {
    const result = await apiClient.post<TaskFileReportResponse>(
      `/tasks/${encodeURIComponent(taskUuid)}/files/report`,
    )
    if (!result?.queued) {
      message.info(t('workflows.task.progress.taskDocumentReportNothing'))
      return
    }
    message.success(
      t('workflows.task.progress.taskDocumentReportQueued', { count: result.count || 0 }),
    )
    // 重新拉取文件树：刚入队的文件需要显示上报状态，随后由状态轮询刷新
    await loadTree(selectedFile.value?.path)
  } catch (error) {
    message.error(
      error instanceof Error
        ? error.message
        : t('workflows.task.progress.taskDocumentReportFailed'),
    )
  } finally {
    reporting.value = false
  }
}

function pendingFailedPaths() {
  const paths: string[] = []
  for (const node of fileMap.value.values()) {
    if (!node.isDir && node.source.upload?.status === 'failed') paths.push(node.path)
  }
  return paths
}

async function requestReupload(paths: string[], all = false) {
  if (!props.taskUuid || !paths.length) return
  const taskUuid = props.taskUuid
  if (all) reuploadingAll.value = true
  else reuploading.value = true
  // 乐观更新：点击后立即显示「正在上报」
  for (const path of paths) {
    const node = fileMap.value.get(path)
    if (node) {
      node.source.upload = {
        status: 'uploading',
        attempts: 0,
        last_error: '',
        uploaded_at: node.source.upload?.uploaded_at,
        doc_key: node.source.upload?.doc_key,
        remote_revision: node.source.upload?.remote_revision,
        reuploadable: true,
      }
    }
  }
  try {
    await apiClient.post(`/tasks/${encodeURIComponent(taskUuid)}/files/reupload`, { paths })
    message.success(t('workflows.task.progress.taskDocumentReuploadQueued'))
    startUploadStatusPoll()
  } catch (error) {
    message.error(
      error instanceof Error
        ? error.message
        : t('workflows.task.progress.taskDocumentReuploadFailed'),
    )
    void pollUploadStatus()
  } finally {
    reuploading.value = false
    reuploadingAll.value = false
  }
}

/** 任务变更推送后延迟重载文件树，避免编辑中的未保存内容被冲掉 */
function scheduleTreeReload() {
  if (treeReloadTimer) clearTimeout(treeReloadTimer)
  treeReloadTimer = setTimeout(() => {
    treeReloadTimer = null
    if (!props.open || dirty.value || saving.value) return
    void loadTree(selectedFile.value?.path)
  }, 800)
}

useLocalWS('task.changed', (data: { task_uuid?: string }) => {
  if (!props.open || data?.task_uuid !== props.taskUuid) return
  scheduleTreeReload()
})

function requestClose() {
  if (saving.value) return
  if (!dirty.value) {
    emit('update:open', false)
    return
  }
  Modal.confirm({
    title: t('workflows.task.progress.discardCloseTitle'),
    content: t('workflows.task.progress.discardCloseContent'),
    okText: t('workflows.task.progress.discardChanges'),
    cancelText: t('workflows.task.progress.continueEditing'),
    onOk: () => emit('update:open', false),
  })
}

function clearSelection() {
  resolveDocumentImage.value = undefined
  selectedKeys.value = []
  selectedFile.value = undefined
  selectedContent.value = undefined
  draftContent.value = ''
  savedContent.value = ''
  contentLoading.value = false
  contentError.value = ''
}

async function openTaskDirectory() {
  if (!taskDirectory.value || openingDirectory.value) return
  if (!isDesktopRuntime()) {
    try {
      await copyText(taskDirectory.value)
      message.info(t('workflows.task.progress.browserFolderCopied'))
    } catch {
      message.warning(t('workflows.task.progress.browserFolderUnavailable', { path: taskDirectory.value }))
    }
    return
  }

  openingDirectory.value = true
  try {
    await openDirectory(taskDirectory.value)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.progress.openWorkspaceFailed'))
  } finally {
    openingDirectory.value = false
  }
}

function resetState() {
  treeLoadVersion += 1
  contentLoadVersion += 1
  saveVersion += 1
  stopUploadStatusPoll()
  if (treeReloadTimer) {
    clearTimeout(treeReloadTimer)
    treeReloadTimer = null
  }
  reuploading.value = false
  reuploadingAll.value = false
  reporting.value = false
  treeData.value = []
  expandedKeys.value = []
  fileMap.value = new Map()
  treeLoading.value = false
  saving.value = false
  openingDirectory.value = false
  taskDirectory.value = ''
  treeError.value = ''
  uploadEnabled.value = false
  autoUploadEnabled.value = true
  clearSelection()
  sidebarCollapsed.value = false
  dragging = false
  if (resizeRaf) {
    cancelAnimationFrame(resizeRaf)
    resizeRaf = 0
  }
  document.querySelector('.task-documents-drawer')?.classList.remove('is-resizing')
  resizeWrapper = null
}

function normalizedFileType(type?: string) {
  return (type || '').replace(/^\./, '').toLowerCase()
}

function isMarkdown(type?: string) {
  return ['md', 'markdown'].includes(normalizedFileType(type))
}

function isImageType(type?: string) {
  return ['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'bmp', 'ico'].includes(
    normalizedFileType(type),
  )
}

function isTextType(type?: string) {
  return [
    'txt',
    'json',
    'yaml',
    'yml',
    'xml',
    'html',
    'css',
    'js',
    'ts',
    'tsx',
    'jsx',
    'vue',
    'go',
    'py',
    'java',
    'sh',
    'sql',
    'toml',
    'ini',
    'log',
  ].includes(normalizedFileType(type))
}

function formatFileSize(size?: number) {
  if (size === undefined || size < 0) return ''
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}
</script>

<style scoped>
.documents-drawer-shell {
  position: relative;
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  background: #fff;
}
.documents-header {
  display: flex;
  min-height: 72px;
  flex: 0 0 auto;
  box-sizing: border-box;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #d9d9d9;
  padding: 16px 24px;
}
.documents-header-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 12px;
}
.open-directory-button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 4px;
  color: #bfbfbf;
  background: transparent;
  cursor: pointer;
}
.open-directory-button img,
.open-directory-button :deep(.anticon) {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
}
.open-directory-button:not(:disabled):hover {
  background: #e4e6eb;
}
.open-directory-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}
.open-directory-button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.documents-heading {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: center;
  gap: 12px;
}
.step-avatar {
  display: inline-flex;
  overflow: hidden;
  width: 40px;
  height: 40px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  color: #fff;
  background: #3157e2;
  font-size: 16px;
  font-weight: 600;
}
.step-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.documents-heading h2 {
  overflow: hidden;
  margin: 0;
  color: #262626;
  font-size: 24px;
  font-weight: 600;
  line-height: 32px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.close-button {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-size: 20px;
}
.close-button:hover,
.close-button:focus-visible {
  background: #f3f4f6;
}
.close-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}
.documents-workspace {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
}
.file-sidebar {
  position: relative;
  display: flex;
  width: 248px;
  min-width: 0;
  flex: 0 0 248px;
  flex-direction: column;
  overflow: auto;
  border-right: 1px solid #d9d9d9;
  padding: 16px;
}
.drawer-resize-handle { position: absolute; top: 0; left: 0; z-index: 3; width: 8px; touch-action: none; height: 100%; cursor: col-resize; }
.drawer-resize-handle:hover,
.drawer-resize-handle:focus-visible,
:global(.task-documents-drawer.is-resizing) .drawer-resize-handle {
  background: rgba(49, 87, 226, 0.18);
  outline: none;
}
:global(.task-documents-drawer.is-resizing),
:global(.task-documents-drawer.is-resizing *) {
  cursor: col-resize !important;
  user-select: none !important;
}
:global(.task-documents-drawer.is-resizing .ant-drawer-content-wrapper) {
  transition: none !important;
}
.sidebar-state {
  display: flex;
  min-height: 180px;
  align-items: center;
  justify-content: center;
}
.sidebar-error {
  flex-direction: column;
  gap: 12px;
  color: #8c8c8c;
  font-size: 12px;
  text-align: center;
}
.file-tree-node {
  display: flex;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  gap: 6px;
}
.file-node-icon {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 16px;
}
.folder-icon {
  color: #262626;
}
.markdown-icon {
  color: #16a6a1;
}
.image-icon {
  color: #7c3aed;
}
.text-icon {
  color: #d97706;
}
.file-node-name {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  color: #262626;
  font-size: 12px;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.file-node-meta {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 10px;
  line-height: 16px;
}
/* 任务默认不上报文件：常驻提示，说明为什么没有上报进度 */
.documents-auto-sync-off {
  flex: 0 0 auto;
  border-radius: 10px;
  padding: 1px 8px;
  color: #8c6d1f;
  background: #fffbe6;
  font-size: 12px;
  line-height: 20px;
  white-space: nowrap;
  cursor: default;
}
.report-files-button {
  display: inline-flex;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  border: 1px solid #3157e2;
  border-radius: 6px;
  padding: 0 12px;
  color: #3157e2;
  background: #fff;
  cursor: pointer;
  font-size: 13px;
  line-height: 20px;
}
.report-files-button:hover,
.report-files-button:focus-visible {
  outline: none;
  background: #f0f4ff;
}
.report-files-button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
/* 抽屉头部的「已上报 N / 共 M」计数 */
.documents-upload-count {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 12px;
  border-radius: 10px;
  padding: 1px 8px;
  color: #595959;
  background: #f2f4f7;
  font-size: 12px;
  line-height: 20px;
  white-space: nowrap;
}
.documents-upload-count.has-failed {
  color: #cf1322;
  background: #fff1f0;
}
.reupload-all-button {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  color: #cf1322;
  background: transparent;
  cursor: pointer;
  font-size: 18px;
}
.reupload-all-button:hover,
.reupload-all-button:focus-visible {
  background: #fff1f0;
}
.reupload-all-button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
/* 单个文件的上报状态标识：贴在文件名右侧，不遮挡步骤归属标签 */
.file-node-upload {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  min-width: 16px;
  height: 16px;
  margin-left: auto;
  font-size: 14px;
}
.file-node-upload .upload-indicator {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  color: #bfbfbf;
  cursor: default;
}
.file-node-upload.is-synced .upload-indicator {
  color: #52c41a;
}
.file-node-upload.is-uploading .upload-indicator {
  color: #3157e2;
}
.file-node-upload.is-pending .upload-indicator {
  color: #8c8c8c;
}
.file-node-upload.is-skipped .upload-indicator {
  color: #bfbfbf;
}
.file-node-upload .upload-action {
  display: inline-flex;
  width: 16px;
  height: 16px;
  align-items: center;
  justify-content: center;
  border: 0;
  padding: 0;
  color: #cf1322;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
}
.file-node-upload .upload-action:hover,
.file-node-upload .upload-action:focus-visible {
  color: #a8071a;
  outline: none;
}
/* 失败态：悬停行时才展开「重新上报」文字按钮，避免常态噪音 */
.file-node-upload .upload-reupload-text {
  display: none;
  flex: 0 0 auto;
  align-items: center;
  border: 0;
  padding: 0 0 0 4px;
  color: #cf1322;
  background: transparent;
  cursor: pointer;
  font-size: 12px;
  line-height: 16px;
  white-space: nowrap;
}
.file-tree-node:hover .file-node-upload .upload-reupload-text {
  display: inline-flex;
}
.file-node-upload .upload-reupload-text:hover,
.file-node-upload .upload-reupload-text:focus-visible {
  color: #a8071a;
  text-decoration: underline;
  outline: none;
}
.file-node-upload .upload-reupload-text:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.file-node-upload-summary {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 10px;
  line-height: 16px;
  white-space: nowrap;
}
/* 上传状态与引用按钮并存时，只让状态标识吃右侧剩余空间，
   引用按钮紧跟其后，避免两个 margin-left:auto 把图标挤到中间 */
.file-node-upload + .file-node-reference {
  margin-left: 6px;
}
.file-node-upload-summary.has-failed {
  color: #cf1322;
}
/* 一键引用到输入框：默认隐藏，悬停行时出现，避免树里视觉噪音 */
.file-node-reference {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  margin-left: auto;
  border: 1px solid #e5e7eb;
  border-radius: 4px;
  color: #595959;
  background: #fff;
  font-size: 10px;
  opacity: 0;
  transition: opacity 120ms ease-out;
}
.file-tree-node:hover .file-node-reference {
  opacity: 1;
}
.file-node-reference:hover {
  color: #3157e2;
  border-color: #3157e2;
}
.file-workspace {
  position: relative;
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}
.file-toolbar {
  display: flex;
  min-height: 68px;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  padding: 10px 16px 10px 12px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}
.directory-collapse-btn {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  padding: 4px;
  background: transparent;
  cursor: pointer;
}
.directory-collapse-btn img {
  display: block;
  width: 16px;
  height: 16px;
}
.directory-collapse-btn img.is-collapsed {
  transform: rotate(180deg);
}
.directory-collapse-btn:hover,
.directory-collapse-btn:focus-visible {
  background: #f2f4f7;
}
.directory-collapse-btn:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}
.file-toolbar-actions { display: flex; gap: 8px; flex: 0 0 auto; }
.file-toolbar-copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 4px;
}
.save-hint {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}
/* 提醒文案 + 当前文档上报状态：同一行，避免因文件名长短而错位 */
.file-toolbar-meta {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 16px;
}
.file-toolbar-copy strong {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
  overflow: hidden;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 当前文档的上报状态行：与「修改内容保存后同步更新」提醒保持一段距离 */
.file-toolbar-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 16px;
}
.selected-upload-status {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  color: #8c8c8c;
  font-size: 12px;
  font-weight: 400;
  line-height: 20px;
  white-space: nowrap;
}
.selected-upload-status.is-synced {
  color: #389e0d;
}
.selected-upload-status.is-uploading {
  color: #3157e2;
}
.selected-upload-status.is-failed {
  color: #cf1322;
}
.selected-upload-time {
  color: #8c8c8c;
}
.selected-upload-action {
  border: 0;
  padding: 0 2px;
  color: #3157e2;
  background: transparent;
  cursor: pointer;
  font-size: 12px;
}
.selected-upload-action:hover {
  text-decoration: underline;
}
.selected-upload-action:disabled {
  color: #bfbfbf;
  cursor: not-allowed;
  text-decoration: none;
}
.file-content {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
  overflow: hidden;
  padding: 16px;
}
.file-markdown-editor {
  width: 100%;
  height: 100%;
  min-height: 0;
}
.content-state {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: center;
}
.content-error {
  flex-direction: column;
  gap: 16px;
}
.file-editor {
  height: 100%;
  min-height: 300px;
  resize: none;
  border: 0;
  padding: 0;
  color: #262626;
  font-family: 'PingFang SC', sans-serif;
  font-size: 14px;
  line-height: 26px;
  box-shadow: none;
}
.file-editor:focus {
  box-shadow: none;
}
.image-preview {
  display: flex;
  width: 100%;
  min-height: 0;
  align-items: center;
  justify-content: center;
  overflow: auto;
  background: #f8f9fb;
}
.image-preview img {
  display: block;
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}
:deep(.ant-tree) {
  background: transparent;
}
:deep(.ant-tree .ant-tree-node-content-wrapper) {
  min-width: 0;
  border-radius: 6px;
  padding-inline: 6px;
}
:deep(.ant-tree .ant-tree-node-content-wrapper.ant-tree-node-selected) {
  background: #e5efff;
}
:global(.task-documents-drawer .ant-drawer-content) {
  overflow: hidden;
  border-radius: 22px 0 0 22px;
}
@media (max-width: 760px) {
  .documents-header {
    min-height: 76px;
    padding: 18px 16px;
  }
  .documents-heading h2 {
    font-size: 20px;
    line-height: 28px;
  }
  .documents-workspace {
    flex-direction: column;
  }
  .file-sidebar {
    width: auto;
    max-height: 220px;
    flex: 0 0 220px;
    border-right: 0;
    border-bottom: 1px solid #d9d9d9;
  }
}
</style>
