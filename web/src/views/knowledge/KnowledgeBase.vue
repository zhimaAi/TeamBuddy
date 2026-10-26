<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, watch, type ComponentPublicInstance } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import MarkdownIt from 'markdown-it'
import {
  CheckOutlined,
  ClockCircleOutlined,
  FileTextOutlined,
  FolderOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
} from '@ant-design/icons-vue'
import apiClient, { ApiError } from '@/api/client'
import {
  createKnowledgeFolder,
  deleteKnowledgeFolder,
  getKnowledgeRoot,
  migrateKnowledgeRoot,
  scanKnowledge,
  type KnowledgeMigrateConflictItem,
  type KnowledgeMigrateRollback,
} from '@/api/knowledge'
import { isDesktopRuntime, selectDirectory as pickDirectory } from '@/composables/useDesktop'
import { useAppI18n } from '@/i18n'
import knowledgeAddIcon from '@/assets/icons/knowledge-add.svg'
import knowledgeAddDocumentIcon from '@/assets/icons/knowledge-add-document.svg'
import knowledgeAddFolderIcon from '@/assets/icons/knowledge-add-folder.svg'
import knowledgeCopyDocumentIcon from '@/assets/icons/knowledge-copy-document.svg'
import knowledgeDeleteIcon from '@/assets/icons/knowledge-delete.svg'
import knowledgeDeletePermanentIcon from '@/assets/icons/knowledge-delete-permanent.svg'
import directoryToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import knowledgeDocumentIcon from '@/assets/icons/knowledge-document.svg'
import knowledgeDocumentTrashIcon from '@/assets/icons/knowledge-document-trash.svg'
import knowledgeFolderTreeIcon from '@/assets/icons/knowledge-folder-tree.svg'
import knowledgeMoreIcon from '@/assets/icons/knowledge-more.svg'
import knowledgeOutlineIcon from '@/assets/icons/knowledge-outline.svg'
import knowledgeRefreshIcon from '@/assets/icons/knowledge-refresh.svg'
import knowledgeRenameIcon from '@/assets/icons/knowledge-rename.svg'
import knowledgeRootCurrentIcon from '@/assets/icons/knowledge-root-current.svg'
import knowledgeRootMenuIcon from '@/assets/icons/knowledge-root-menu.svg'
import knowledgeRootSelectIcon from '@/assets/icons/knowledge-root-select.svg'
import knowledgeSettingsIcon from '@/assets/icons/knowledge-settings.svg'
import knowledgeSourceModeIcon from '@/assets/icons/knowledge-source-mode.svg'
import knowledgeTrashMenuIcon from '@/assets/icons/knowledge-trash-menu.svg'
import knowledgeTreeToggleIcon from '@/assets/icons/knowledge-tree-toggle.svg'
import trashRestoreIcon from '@/assets/icons/knowledge-trash-restore.svg'
import KnowledgeAddMenu from '@/views/knowledge/components/KnowledgeAddMenu.vue'
import KnowledgeBlankAddDropdown from '@/views/knowledge/components/KnowledgeBlankAddDropdown.vue'
import KnowledgeDocumentEditor from '@/views/knowledge/components/KnowledgeDocumentEditor.vue'
import KnowledgeAppearanceSettings from '@/views/knowledge/components/KnowledgeAppearanceSettings.vue'
import { useKnowledgeTabs, draftSnapshot, tabDirty, type KnowledgeDraft, type KnowledgeTab } from '@/views/knowledge/composables/useKnowledgeTabs'
import { KNOWLEDGE_EDITOR_MODE_KEY, readKnowledgeAppearance, readKnowledgeEditorMode, readWorkspacePreference, workspacePreferenceKey, type KnowledgeAppearance } from '@/views/knowledge/composables/knowledgePreferences'

const { t, locale } = useAppI18n()
const rootPathSeparator = '>'
const inlineMarkdown = new MarkdownIt({ html: true, linkify: false, typographer: false })

type FolderNode = {
  id: number
  name: string
  parent_id: number
  created_at: number
  updated_at: number
  children?: FolderNode[]
}

type KnowledgeDocument = {
  uuid: string
  folder_id: number
  title: string
  file_path: string
  ext: 'md' | 'txt'
  tags: string[]
  content_hash: string
  word_count: number
  created_at: number
  updated_at: number
  content?: string
}

type SearchResult = {
  uuid: string
  title: string
  snippet: string
  folder_id: number
}

// 大纲项：`key` 仅用于列表渲染；跳转按序号交给编辑器定位（S-UI-10）
type OutlineItem = {
  key: string
  level: number
  title: string
}

type DirectoryNode = {
  key: string
  title: string
  kind: 'folder' | 'document'
  entityId: number | string
  parentKey: string
  folderID: number
  count?: number
  isLeaf?: boolean
  children?: DirectoryNode[]
}

type DirectorySelectInfo = {
  node: DirectoryNode
}

type DirectoryDropInfo = {
  node: DirectoryNode & { pos?: string }
  dragNode: DirectoryNode
  dropToGap: boolean
  dropPosition: number
}

const loading = ref(false)
const autosaveState = computed(() => activeTab.value?.state ?? 'idle')
const autosaveSavedAt = computed(() => activeTab.value?.savedAt ?? 0)
const autosaveErrorMessage = computed(() => activeTab.value?.error ?? '')
const folders = ref<FolderNode[]>([])
const documents = ref<KnowledgeDocument[]>([])
const trashDocuments = ref<KnowledgeDocument[]>([])
const searchResults = ref<SearchResult[]>([])
const selectedFolderID = ref<number>(0)
const selectedTreeKey = ref('')
const searchKeyword = ref('')
const searchLoading = ref(false)
const trashVisible = ref(false)
// 重命名弹窗（取代原 editor-header 内的标题就地编辑）
const renameModalVisible = ref(false)
const renameInputValue = ref('')
const folderCreating = ref(false)
// 大纲 / 属性抽屉显隐（独立于媒体查询，避免窄屏下点击无响应）
const inspectorOpen = ref(false)
const documentActionsOpen = ref(false)

// 左侧目录栏（knowledge-sidebar）：可折叠、可拖拽改宽度。
// 拖拽宽度限制在 100–600px，折叠仅由按钮触发。
const SIDEBAR_MAX_WIDTH = 600
const SIDEBAR_MIN_WIDTH = 100
const SIDEBAR_DEFAULT_WIDTH = 300
const sidebarCollapsed = ref(false)
const sidebarWidth = ref(SIDEBAR_DEFAULT_WIDTH)
const directoryOrder = ref<Record<string, string[]>>({})
// 目录树展开状态：记录当前展开的文件夹 key
const expandedKeys = ref<string[]>([])
// 记录之前展开的 keys，用于恢复（当 sidebar 收起时）
let previousExpandedKeys: string[] = []
let searchTimer: ReturnType<typeof setTimeout> | undefined
let searchSequence = 0

function collapseSidebar() {
  if (sidebarCollapsed.value) return
  previousExpandedKeys = [...expandedKeys.value]
  expandedKeys.value = []
  sidebarCollapsed.value = true
}

function expandSidebar() {
  if (!sidebarCollapsed.value) return
  expandedKeys.value = [...previousExpandedKeys]
  sidebarCollapsed.value = false
}

function toggleSidebar() {
  if (sidebarCollapsed.value) expandSidebar()
  else collapseSidebar()
}

// 拖拽改宽度：在 mousedown 时记录起始位置和宽度，mousemove 时实时计算并夹紧。
// 监听挂到 document 上以保证鼠标拖出 sidebar 也能继续调整；mouseup 时统一清理。
let sidebarResizeCleanup: (() => void) | null = null
function startSidebarResize(event: MouseEvent) {
  if (event.button !== 0) return
  event.preventDefault()
  const startX = event.clientX
  const startWidth = sidebarWidth.value
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'

  const finishResize = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', finishResize)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    sidebarResizeCleanup = null
  }
  const onMove = (moveEvent: MouseEvent) => {
    const next = startWidth + (moveEvent.clientX - startX)
    sidebarWidth.value = Math.max(SIDEBAR_MIN_WIDTH, Math.min(SIDEBAR_MAX_WIDTH, next))
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', finishResize)
  sidebarResizeCleanup = finishResize
}

const isSidebarNarrow = computed(
  () => !sidebarCollapsed.value && sidebarWidth.value < 240,
)
const knowledgePageStyle = computed(() => {
  const sidebar = sidebarCollapsed.value ? '0px' : `${sidebarWidth.value}px`
  return { '--knowledge-sidebar-width': sidebar }
})

// S-UI-01: 知识库根目录（KR）设置相关状态
const knowledgeRoot = ref('')
const knowledgeDefaultRoot = ref('')
const rootModalVisible = ref(false)
const rootModalLoading = ref(false)
const pendingRootDir = ref('')

// S-UI-03: 删除文件夹双选项弹窗状态
const deleteFolderModalVisible = ref(false)
const deleteFolderModalLoading = ref(false)
const deleteFolderTarget = ref<FolderNode | null>(null)
// 文件夹重命名弹窗状态（与文档重命名分开，避免共用草稿状态）
const folderRenameModalVisible = ref(false)
const folderRenameModalLoading = ref(false)
const folderRenameTarget = ref<FolderNode | null>(null)
const folderRenameInputValue = ref('')

const editorRefs = new Map<string, { scrollToHeading: (index: number) => void }>()
const scanning = ref(false)
const workspaceMutating = ref(false)
const { tabs, activeUUID, activeTab, draft, addTab, removeTab, cancelTimer, scheduleSave } =
  useKnowledgeTabs((tab) => performSave('auto', tab))
watch([activeUUID, trashVisible], () => { documentActionsOpen.value = false })
const appearance = ref(readKnowledgeAppearance())
const editorMode = ref(readKnowledgeEditorMode())
const appearanceSettingsOpen = ref(false)
let workspaceReady = false
let disposed = false
let openSequence = 0
const pendingOpens = new Map<string, Promise<KnowledgeDocument>>()

function persistWorkspace() {
  if (!workspaceReady || !knowledgeRoot.value) return
  try {
    localStorage.setItem(workspacePreferenceKey(knowledgeRoot.value), JSON.stringify({
      version: 1,
      documents: tabs.value.map((tab) => ({ uuid: tab.draft.uuid })),
      activeUUID: activeUUID.value,
      expandedKeys: sidebarCollapsed.value ? previousExpandedKeys : expandedKeys.value,
    }))
  } catch {
    message.error(t('knowledge.preferenceSaveFailed'))
  }
}
watch(() => JSON.stringify([
  tabs.value.map((tab) => tab.draft.uuid), activeUUID.value, expandedKeys.value,
]), persistWorkspace)

function updateAppearance(value: KnowledgeAppearance) {
  appearance.value = value
  try {
    localStorage.setItem('knowledge-appearance-v1', JSON.stringify(value))
  } catch {
    message.error(t('knowledge.preferenceSaveFailed'))
  }
}

function toggleEditorMode() {
  const tab = activeTab.value
  if (!tab || tab.draft.ext !== 'md') return
  editorMode.value = editorMode.value === 'source' ? 'visual' : 'source'
  try {
    localStorage.setItem(KNOWLEDGE_EDITOR_MODE_KEY, editorMode.value)
  } catch {
    message.error(t('knowledge.preferenceSaveFailed'))
  }
}

function documentDraft(document: KnowledgeDocument): KnowledgeDraft {
  return {
    uuid: document.uuid, title: document.title, ext: document.ext, folderID: document.folder_id,
    tags: [...(document.tags || [])], content: document.content || '', filePath: document.file_path,
    contentHash: document.content_hash, wordCount: document.word_count,
    createdAt: document.created_at, updatedAt: document.updated_at,
  }
}

function setEditorRef(uuid: string, instance: Element | ComponentPublicInstance | null) {
  if (instance && 'scrollToHeading' in instance && typeof instance.scrollToHeading === 'function') {
    const scrollToHeading = instance.scrollToHeading
    editorRefs.set(uuid, { scrollToHeading: (index) => scrollToHeading(index) })
  } else editorRefs.delete(uuid)
}

function activateTab(uuid: string, treeKey = '') {
  openSequence += 1
  renameModalVisible.value = false
  activeUUID.value = uuid
  trashVisible.value = false
  selectedTreeKey.value = treeKey || `doc:${uuid}`
  selectedFolderID.value = draft.value.folderID
  const keys = folderAncestorChain(draft.value.folderID).map((folder) => `folder:${folder.id}`)
  expandedKeys.value = [...new Set([...expandedKeys.value, ...keys])]
}

async function closeTab(uuid: string) {
  const tab = tabs.value.find((item) => item.draft.uuid === uuid)
  if (!tab) return
  openSequence += 1
  await flushTab(tab)
  if (tabDirty(tab) && !(await confirmDiscardChanges(tab))) return
  removeTab(uuid)
  if (activeUUID.value) activateTab(activeUUID.value)
  else selectedTreeKey.value = ''
}

async function restoreWorkspace() {
  loading.value = true
  const preference = readWorkspacePreference(knowledgeRoot.value)
  if (preference) {
    expandedKeys.value = preference.expandedKeys.filter((key) =>
      flatFolders.value.some((folder) => key === `folder:${folder.id}`),
    )
    const existing = new Set(documents.value.map((document) => document.uuid))
    const saved = preference.documents.filter((item) => existing.has(item.uuid))
    const results = await Promise.allSettled(saved.map((item) =>
      apiClient.get<KnowledgeDocument>(`/knowledge/documents/${encodeURIComponent(item.uuid)}`),
    ))
    if (disposed) return
    let failed = false
    results.forEach((result) => {
      if (result.status === 'fulfilled') {
        addTab(documentDraft(result.value))
      } else failed = true
    })
    const target = tabs.value.find((tab) => tab.draft.uuid === preference.activeUUID) ?? tabs.value[0]
    if (target) activateTab(target.draft.uuid)
    if (failed) message.error(t('knowledge.workspaceRestoreFailed'))
  }
  workspaceReady = true
  loading.value = false
  persistWorkspace()
}

const flatFolders = computed(() => {
  const result: Array<FolderNode & { depth: number }> = []
  const walk = (nodes: FolderNode[], depth: number) => {
    for (const node of nodes) {
      result.push({ ...node, depth })
      walk(node.children || [], depth + 1)
    }
  }
  walk(folders.value, 0)
  return result
})

// S-UI-07: 知识库为空时不自动创建任何文件夹或文档，只展示引导与创建入口
const knowledgeEmpty = computed(() => !folders.value.length && !documents.value.length)

// 从根到目标文件夹的祖先链（含目标自身），用于目录展开与来源路径。
function folderAncestorChain(folderID: number): FolderNode[] {
  const current = flatFolders.value.find((folder) => folder.id === folderID)
  if (!current) return []
  const chain: FolderNode[] = [current]
  let parentID = current.parent_id
  // 后端已有环检测（S-BE-04），此处上限仅作死循环兜底
  let guard = 0
  while (parentID > 0 && guard < 256) {
    const parent = flatFolders.value.find((folder) => folder.id === parentID)
    if (!parent) break
    chain.unshift(parent)
    parentID = parent.parent_id
    guard += 1
  }
  return chain
}

function toggleInspector() {
  inspectorOpen.value = !inspectorOpen.value
}

async function copyDocumentContent() {
  const content = draft.value.content
  if (!content) return
  try {
    try {
      await navigator.clipboard.writeText(content)
    } catch {
      const textarea = document.createElement('textarea')
      try {
        textarea.value = content
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.select()
        if (!document.execCommand('copy')) throw new Error(t('knowledge.copyDocumentFailed'))
      } finally {
        textarea.remove()
      }
    }
    message.success(t('knowledge.documentCopied'))
  } catch {
    message.error(t('knowledge.copyDocumentFailed'))
  }
}

// S-UI-01: 设置弹窗内显示待确认的目录路径
const modalRootBreadcrumbSegments = computed(() => {
  const path = pendingRootDir.value || knowledgeRoot.value
  const segments = path.split(/[\\/]+/).filter(Boolean)
  const leaf = segments.pop() || ''
  return [t('knowledge.rootBreadcrumbRoot'), leaf].filter(Boolean)
})

const directoryDocuments = computed(() => {
  if (!searchKeyword.value.trim()) return documents.value
  const matches = new Set(searchResults.value.map((result) => result.uuid))
  return documents.value.filter((document) => matches.has(document.uuid))
})

function orderDirectoryNodes(parentKey: string, nodes: DirectoryNode[]) {
  const order = directoryOrder.value[parentKey] || []
  if (!order.length) return nodes
  const indexes = new Map(order.map((key, index) => [key, index]))
  return [...nodes].sort((left, right) => {
    const leftIndex = indexes.get(left.key)
    const rightIndex = indexes.get(right.key)
    if (leftIndex === undefined && rightIndex === undefined) return 0
    if (leftIndex === undefined) return 1
    if (rightIndex === undefined) return -1
    return leftIndex - rightIndex
  })
}

function persistDirectoryOrder() {
  localStorage.setItem('knowledge-directory-order', JSON.stringify(directoryOrder.value))
}

const directoryTreeData = computed<DirectoryNode[]>(() => {
  const documentNode = (
    document: KnowledgeDocument,
    parentKey: string,
    keyPrefix = 'doc',
  ): DirectoryNode => ({
    key: `${keyPrefix}:${document.uuid}`,
    title: document.ext ? `${document.title}.${document.ext}` : document.title,
    kind: 'document',
    entityId: document.uuid,
    parentKey,
    folderID: document.folder_id,
    isLeaf: true,
  })

  const countDocuments = (folder: FolderNode): number => {
    const direct = documents.value.filter((document) => document.folder_id === folder.id).length
    return direct + (folder.children || []).reduce((sum, child) => sum + countDocuments(child), 0)
  }

  const mapFolder = (folder: FolderNode, parentKey: string): DirectoryNode | null => {
    const key = `folder:${folder.id}`
    const childFolders = (folder.children || [])
      .map((child) => mapFolder(child, key))
      .filter((child): child is DirectoryNode => Boolean(child))
    const childDocuments = directoryDocuments.value
      .filter((document) => document.folder_id === folder.id)
      .map((document) => documentNode(document, key))
    const children = orderDirectoryNodes(key, [...childFolders, ...childDocuments])
    if (searchKeyword.value.trim() && !children.length) return null
    return {
      key,
      title: folder.name,
      kind: 'folder',
      entityId: folder.id,
      parentKey,
      folderID: folder.id,
      count: countDocuments(folder),
      isLeaf: false,
      children,
    }
  }

  const sortableRootNodes = orderDirectoryNodes('root', [
    ...folders.value
      .map((folder) => mapFolder(folder, 'root'))
      .filter((folder): folder is DirectoryNode => Boolean(folder)),
    // 根目录文件（folder_id === 0）与根目录同级展示，支持搜索过滤、选中、拖拽到文件夹等已有交互
    ...directoryDocuments.value
      .filter((document) => document.folder_id === 0)
      .map((document) => documentNode(document, 'root')),
  ])

  return sortableRootNodes
})

const isMarkdownDraft = computed(() => draft.value.ext === 'md')

// 大纲和文件名只使用标题的可见文字。TipTap 会把颜色等内联样式序列化为 HTML，
// 这里通过 Markdown token 提取文本，避免目录显示 `<span style=...>` 等源代码。
function plainTextFromMarkdownInline(source: string): string {
  const children = inlineMarkdown.parseInline(source, {})[0]?.children || []
  return children
    .filter((token) => token.type !== 'html_inline')
    .map((token) =>
      token.type === 'softbreak' || token.type === 'hardbreak' ? ' ' : token.content,
    )
    .join('')
    .replace(/\s+/g, ' ')
    .trim()
}

const outline = computed<OutlineItem[]>(() => {
  const items: OutlineItem[] = []
  for (const line of draft.value.content.split(/\r?\n/)) {
    const match = /^(#{1,6})\s+(.+?)\s*#*$/.exec(line)
    if (!match) continue
    const title = plainTextFromMarkdownInline(match[2].trim())
    if (!title) continue
    items.push({
      key: `${items.length}:${title}`,
      level: match[1].length,
      title,
    })
  }
  return items
})

// 自动保存指示器文案：根据状态机返回不同模板；initial drafts 不显示
const autosaveHint = computed(() => {
  if (!draft.value.uuid) return ''
  if (autosaveState.value === 'pending') return t('knowledge.autosavePending')
  if (autosaveState.value === 'saving') return t('knowledge.autosaveSaving')
  if (autosaveState.value === 'saved' && autosaveSavedAt.value) {
    const time = new Date(autosaveSavedAt.value).toLocaleTimeString(locale.value, {
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
    return t('knowledge.autosaveSavedAt', { time })
  }
  if (autosaveState.value === 'error') return t('knowledge.autosaveFailed')
  return ''
})

function formatTime(value: number) {
  if (!value) return '—'
  return new Date(value).toLocaleString(locale.value, { hour12: false })
}

async function loadFolders() {
  const result = await apiClient.get<{ data: FolderNode[] }>('/knowledge/folders')
  folders.value = result.data || []
}

async function loadDocuments() {
  const result = await apiClient.get<{ data: KnowledgeDocument[] }>('/knowledge/documents')
  documents.value = result.data || []
}

function reconcileTabLocations() {
  for (const tab of [...tabs.value]) {
    const document = documents.value.find((item) => item.uuid === tab.draft.uuid)
    if (!document) {
      removeTab(tab.draft.uuid)
      continue
    }
    const saving = tab.saving
    tab.saving = true
    tab.draft.folderID = document.folder_id
    tab.draft.filePath = document.file_path
    tab.saving = saving
    tab.savedSnapshot = JSON.stringify({ ...JSON.parse(tab.savedSnapshot), folderID: document.folder_id })
  }
  if (activeUUID.value) activateTab(activeUUID.value)
}

async function loadTrash() {
  const result = await apiClient.get<{ data: KnowledgeDocument[] }>('/knowledge/trash')
  trashDocuments.value = result.data || []
}

async function refreshTrash() {
  try {
    await loadTrash()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.trashLoadFailed'))
  }
}

async function loadWorkspace(scan = false) {
  loading.value = true
  try {
    await Promise.all([loadFolders(), loadDocuments(), loadTrash()])
    if (scan) {
      await scanKnowledgeBase()
    }
    return true
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.loadFailed'))
    return false
  } finally {
    loading.value = false
  }
}

// S-UI-05: 进入页面与手动刷新时扫描 KR，索引外部 .md/.txt
async function scanKnowledgeBase() {
  scanning.value = true
  try {
    const result = await scanKnowledge()
    if (result.added_folders || result.added_documents) {
      await Promise.all([loadFolders(), loadDocuments()])
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.scanFailed'))
  } finally {
    scanning.value = false
  }
}

async function refreshWorkspace() {
  if (loading.value || scanning.value) return
  await loadWorkspace(true)
}

// S-UI-01: 加载当前 KR 与默认 KR
async function loadKnowledgeRoot() {
  try {
    const result = await getKnowledgeRoot()
    knowledgeRoot.value = result.root
    knowledgeDefaultRoot.value = result.default
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.rootLoadFailed'))
  }
}

function openRootModal() {
  pendingRootDir.value = knowledgeRoot.value
  rootModalVisible.value = true
}

async function chooseRootDirectory() {
  if (!isDesktopRuntime()) return
  const defaultPath = pendingRootDir.value || knowledgeDefaultRoot.value
  try {
    const selected = await pickDirectory(defaultPath)
    if (selected) {
      pendingRootDir.value = selected
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.selectDirectoryFailed'))
  }
}

// S-UI-17: 迁移属破坏性操作，确认前先二次确认，迁移期间锁定弹窗防重复提交
async function confirmSetKnowledgeRoot() {
  const dir = pendingRootDir.value.trim()
  if (!dir || rootModalLoading.value) return

  const confirmed = await new Promise<boolean>((resolve) => {
    Modal.confirm({
      title: t('knowledge.migrateConfirmTitle'),
      content: t('knowledge.migrateConfirmDescription'),
      okText: t('knowledge.confirm'),
      okType: 'danger',
      cancelText: t('knowledge.cancel'),
      onOk: () => resolve(true),
      onCancel: () => resolve(false),
    })
  })
  if (!confirmed) return

  rootModalLoading.value = true
  try {
    if (!(await flushTabs())) return
    persistWorkspace()
    const result = await migrateKnowledgeRoot(dir)
    knowledgeRoot.value = result.root
    await loadKnowledgeRoot()
    await loadWorkspace(false)
    reconcileTabLocations()
    persistWorkspace()
    rootModalVisible.value = false
    pendingRootDir.value = ''
    message.success(
      t('knowledge.migrateSuccess', {
        folders: result.migrated.folders,
        documents: result.migrated.documents,
      }),
    )
  } catch (error) {
    handleMigrateFailure(error)
  } finally {
    rootModalLoading.value = false
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isMigrateConflictItem(value: unknown): value is KnowledgeMigrateConflictItem {
  return (
    isRecord(value) &&
    typeof value.path === 'string' &&
    (value.type === 'file' || value.type === 'dir')
  )
}

function readMigrateConflicts(payload: unknown): KnowledgeMigrateConflictItem[] {
  if (!isRecord(payload) || !Array.isArray(payload.conflicts)) return []
  return payload.conflicts.filter(isMigrateConflictItem)
}

function readMigrateRollback(payload: unknown): KnowledgeMigrateRollback | null {
  if (!isRecord(payload) || !isRecord(payload.rollback)) return null
  const { completed, failed } = payload.rollback
  if (typeof completed !== 'boolean') return null
  return {
    completed,
    failed: Array.isArray(failed)
      ? failed.filter((item): item is string => typeof item === 'string')
      : [],
  }
}

function migrateFailureFallback(code: unknown) {
  if (code === 'knowledge_root_target_invalid') return t('knowledge.migrateTargetInvalid')
  if (code === 'knowledge_root_migrate_conflict') return t('knowledge.migrateConflictTitle')
  if (code === 'knowledge_root_migrate_failed') return t('knowledge.migrateFailed')
  return t('knowledge.knowledgeRootSetFailed')
}

// S-IN-07: 冲突清单直接渲染后端返回的 path/type，配置与目录树保持不变
function showMigrateConflicts(conflicts: KnowledgeMigrateConflictItem[]) {
  Modal.error({
    title: t('knowledge.migrateConflictTitle'),
    okText: t('knowledge.confirm'),
    content: () =>
      h('div', [
        h(
          'p',
          { style: { margin: '0 0 8px', color: '#8C8C8C', fontSize: '13px' } },
          t('knowledge.migrateConflictListTitle'),
        ),
        h(
          'ul',
          { style: { margin: 0, paddingLeft: '18px', maxHeight: '240px', overflowY: 'auto' } },
          conflicts.map((item) =>
            h(
              'li',
              { key: item.path },
              `${item.type === 'dir' ? t('knowledge.migrateConflictDir') : t('knowledge.migrateConflictFile')}：${item.path}`,
            ),
          ),
        ),
      ]),
  })
}

function handleMigrateFailure(error: unknown) {
  const apiError = error instanceof ApiError ? error : null
  const code = apiError?.code
  const conflicts = readMigrateConflicts(apiError?.payload)
  if (apiError?.status === 409 && conflicts.length) {
    showMigrateConflicts(conflicts)
    return
  }
  // 优先展示后端真实文案，缺失时按错误码回退到词条
  const detail = error instanceof Error && error.message ? error.message : ''
  message.error(detail || migrateFailureFallback(code))

  if (code === 'knowledge_root_migrate_failed') {
    const rollback = readMigrateRollback(apiError?.payload)
    if (rollback && !rollback.completed && rollback.failed.length) {
      message.error(t('knowledge.migrateRollbackFailed', { items: rollback.failed.join('、') }))
    }
  }
}

function cancelRootModal() {
  if (rootModalLoading.value) {
    message.info(t('knowledge.migrateBlocked'))
    return
  }
  rootModalVisible.value = false
  pendingRootDir.value = ''
}

function confirmDiscardChanges(tab = activeTab.value) {
  if (!tab || !tabDirty(tab)) return Promise.resolve(true)
  return new Promise<boolean>((resolve) => {
    Modal.confirm({
      title: t('knowledge.unsavedTitle'),
      content: t('knowledge.unsavedDescription'),
      okText: t('knowledge.discard'), okType: 'danger', cancelText: t('knowledge.continueEditing'),
      onOk: () => resolve(true), onCancel: () => resolve(false),
    })
  })
}

async function openDocument(uuid: string, force = false, treeKey = '') {
  if (loading.value || disposed) return
  const sequence = ++openSequence
  const existing = tabs.value.find((tab) => tab.draft.uuid === uuid)
  if (existing && !force) {
    activateTab(uuid, treeKey)
    return
  }
  try {
    let request = pendingOpens.get(uuid)
    if (!request) {
      request = apiClient.get<KnowledgeDocument>(`/knowledge/documents/${encodeURIComponent(uuid)}`)
      pendingOpens.set(uuid, request)
    }
    const document = await request
    if (disposed) return
    let tab = tabs.value.find((item) => item.draft.uuid === uuid)
    if (!tab) tab = addTab(documentDraft(document))
    else if (force) {
      cancelTimer(uuid)
      tab.saving = true
      Object.assign(tab.draft, documentDraft(document))
      tab.savedSnapshot = draftSnapshot(tab.draft)
      tab.saving = false
      tab.state = 'idle'
      tab.error = ''
    }
    if (sequence === openSequence) activateTab(uuid, treeKey)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.documentLoadFailed'))
  } finally {
    pendingOpens.delete(uuid)
  }
}

// 新建 Markdown 文档后直接进入编辑器；首行标题同时作为默认文件名。
async function createDocument(targetFolderId?: number) {
  const folderId = targetFolderId ?? selectedFolderID.value
  try {
    // 默认「未命名文档」也预判重名 (n)，避免同一文件夹下出现同名文件；正文首行同步保持一致
    const title = uniqueTitleInFolder(folderId, t('knowledge.untitled'))
    const created = await apiClient.post<KnowledgeDocument>('/knowledge/documents', {
      title,
      folder_id: folderId,
      content: buildNewDocumentContent(title),
      tags: [],
      ext: 'md',
    })
    await loadDocuments()
    // 创建完成后自动展开目标目录
    if (folderId > 0) {
      const folderKey = `folder:${folderId}`
      if (!expandedKeys.value.includes(folderKey)) {
        expandedKeys.value = [...expandedKeys.value, folderKey]
      }
    }
    selectedFolderID.value = folderId
    await openDocument(created.uuid, true)
    message.success(t('knowledge.documentCreated'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.documentCreateFailed'))
  }
}

function updateDocumentInList(document: KnowledgeDocument) {
  const index = documents.value.findIndex((item) => item.uuid === document.uuid)
  if (index >= 0) {
    documents.value[index] = { ...documents.value[index], ...document, content: undefined }
  } else {
    documents.value.unshift({ ...document, content: undefined })
  }
}

async function saveAsCopy(tab: KnowledgeTab) {
  const targetDraft = tab.draft
  try {
    const copyTitle = t('knowledge.conflictCopy', { title: targetDraft.title.trim() || t('knowledge.untitled') })
    // 同目录预判重名 (n)，正文首行同步换成副本标题，保持首行与文件名一致
    const finalTitle = uniqueTitleInFolder(targetDraft.folderID, copyTitle)
    const heading = extractFirstHeading(targetDraft.content)
    const finalContent = heading !== null ? replaceFirstHeading(targetDraft.content, finalTitle) : targetDraft.content
    const created = await apiClient.post<KnowledgeDocument>('/knowledge/documents', {
      title: finalTitle,
      folder_id: targetDraft.folderID,
      content: finalContent,
      tags: targetDraft.tags,
      ext: targetDraft.ext,
    })
    await loadDocuments()
    removeTab(tab.draft.uuid)
    await openDocument(created.uuid, true)
    message.success(t('knowledge.savedAsCopy'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.saveAsFailed'))
  }
}

function handleSaveConflict(tab: KnowledgeTab) {
  Modal.confirm({
    title: t('knowledge.conflictTitle'),
    content: t('knowledge.conflictDescription'),
    okText: t('knowledge.reload'),
    cancelText: t('knowledge.saveAsCopy'),
    async onOk() {
      await openDocument(tab.draft.uuid, true)
    },
    async onCancel() {
      await saveAsCopy(tab)
    },
  })
}

// 保存始终绑定发起请求的标签。请求期间继续输入时，只更新磁盘基线，不覆盖新草稿。
const saveRequests = new Map<string, Promise<void>>()
async function performSave(source: 'manual' | 'auto', tab = activeTab.value): Promise<void> {
  if (!tab || disposed || workspaceMutating.value) return
  const running = saveRequests.get(tab.draft.uuid)
  if (running) {
    await running
    return
  }
  if (!tabDirty(tab)) return
  const request = saveTab(source, tab)
  saveRequests.set(tab.draft.uuid, request)
  try { await request } finally { saveRequests.delete(tab.draft.uuid) }
}

async function saveTab(source: 'manual' | 'auto', tab: KnowledgeTab) {
  const targetDraft = tab.draft
  cancelTimer(targetDraft.uuid)
  tab.saving = true
  tab.state = 'saving'
  const heading = targetDraft.ext === 'md' ? extractFirstHeading(targetDraft.content) : null
  const emptyHeading = targetDraft.ext === 'md' && hasEmptyFirstHeading(targetDraft.content)
  const titleSource = emptyHeading ? t('knowledge.untitled') : heading ?? targetDraft.title.trim()
  const nextTitle = uniqueTitleInFolder(targetDraft.folderID, titleSource, targetDraft.uuid)
  if (!nextTitle.trim()) {
    tab.saving = false
    tab.state = 'error'
    tab.error = t('knowledge.titleRequired')
    return
  }
  targetDraft.title = nextTitle
  if (heading !== null && nextTitle !== heading) targetDraft.content = replaceFirstHeading(targetDraft.content, nextTitle)
  const sentSnapshot = draftSnapshot(targetDraft)
  try {
    const updated = await apiClient.put<KnowledgeDocument>(
      `/knowledge/documents/${encodeURIComponent(targetDraft.uuid)}`,
      { title: nextTitle, folder_id: targetDraft.folderID, content: targetDraft.content,
        tags: targetDraft.tags.map((tag) => tag.trim()).filter(Boolean), base_hash: targetDraft.contentHash },
    )
    const unchanged = draftSnapshot(targetDraft) === sentSnapshot
    const saved = documentDraft(updated)
    if (unchanged) Object.assign(targetDraft, saved)
    else Object.assign(targetDraft, {
      contentHash: saved.contentHash, filePath: saved.filePath,
      wordCount: saved.wordCount, updatedAt: saved.updatedAt,
    })
    tab.savedSnapshot = draftSnapshot(saved)
    updateDocumentInList(updated)
    tab.savedAt = Date.now()
    tab.error = ''
    tab.state = 'saved'
    if (source === 'manual') message.success(t('knowledge.documentSaved'))
  } catch (error) {
    tab.error = error instanceof Error ? error.message : t('knowledge.saveFailed')
    tab.state = 'error'
    if (error instanceof ApiError && error.status === 409 && source === 'manual') handleSaveConflict(tab)
    else if (source === 'manual') message.error(tab.error)
  } finally {
    tab.saving = false
    if (tabDirty(tab) && tab.state !== 'error' && !disposed) scheduleSave(tab)
  }
}

function saveDocument() { return performSave('manual') }
async function retryAutosave() { await performSave('manual') }

// 目录树文档节点三点菜单「重命名」→ 打开目标文档后唤起重命名弹窗。
// 三点按钮会阻止树节点的选中事件，不能依赖 selectedDocumentUUID 已预先更新。
async function handleRenameDocument(uuid: string) {
  if (!uuid) return
  if (draft.value.uuid !== uuid) {
    await openDocument(uuid, false, `doc:${uuid}`)
  }
  // 目标文档加载失败时，不打开错误目标的弹窗。
  if (draft.value.uuid !== uuid) return
  startRename()
}

// 重命名入口：把当前标题作为弹窗初值。弹窗提交后只更新 草稿标题，由自动保存负责落盘
function startRename() {
  if (!draft.value.uuid) return
  renameInputValue.value = draft.value.title
  renameModalVisible.value = true
}

function cancelRename() {
  renameModalVisible.value = false
}

async function submitRename() {
  const next = renameInputValue.value.trim()
  if (!next) {
    message.error(t('knowledge.titleRequired'))
    return
  }
  // 提前按文件名规则清洗，避免标题里含非法字符导致后端落盘失败
  const sanitized = sanitizeTitle(next)
  if (sanitized === draft.value.title.trim()) {
    renameModalVisible.value = false
    return
  }
  renameModalVisible.value = false
  // 直接修改草稿标题，监听到草稿变化后会自动保存；触发表单 → dirty 的过渡
  draft.value.title = sanitized
  // 正文首行是文件名的体现，同步更新首行保持一致，避免被 performSave 的「首行为准」逻辑覆盖回旧值
  if (extractFirstHeading(draft.value.content) !== null) {
    draft.value.content = replaceFirstHeading(draft.value.content, sanitized)
  }
}

// 目录树文档节点三点菜单「移到回收站」
function handleDeleteDocument(uuid: string) {
  if (!uuid) return
  Modal.confirm({
    title: t('knowledge.deleteTitle', { title: documents.value.find((item) => item.uuid === uuid)?.title || '' }),
    content: t('knowledge.deleteDescription'),
    okText: t('knowledge.moveToTrash'), okType: 'danger', cancelText: t('knowledge.cancel'),
    async onOk() {
      try {
        const tab = tabs.value.find((item) => item.draft.uuid === uuid)
        cancelTimer(uuid)
        const running = saveRequests.get(uuid)
        if (running) await running
        cancelTimer(uuid)
        await apiClient.delete(`/knowledge/documents/${encodeURIComponent(uuid)}`)
        if (tab) removeTab(uuid)
        if (activeUUID.value) activateTab(activeUUID.value)
        else selectedTreeKey.value = ''
        await Promise.all([loadDocuments(), loadTrash()])
        message.success(t('knowledge.movedToTrash'))
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('knowledge.deleteFailed'))
      }
    },
  })
}

async function openTrash() {
  trashVisible.value = true
  await refreshTrash()
}

async function restoreDocument(document: KnowledgeDocument) {
  try {
    await apiClient.post(`/knowledge/documents/${document.uuid}/restore`)
    await Promise.all([loadTrash(), loadDocuments()])
    message.success(t('knowledge.restored'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.restoreFailed'))
  }
}

function hardDeleteDocument(document: KnowledgeDocument) {
  Modal.confirm({
    title: t('knowledge.hardDeleteTitle', { title: document.title }),
    content: t('knowledge.hardDeleteDescription'),
    okText: t('knowledge.deletePermanently'),
    okType: 'danger',
    cancelText: t('knowledge.cancel'),
    async onOk() {
      try {
        await apiClient.delete(`/knowledge/documents/${document.uuid}/permanent`)
        await loadTrash()
        message.success(t('knowledge.hardDeleted'))
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('knowledge.hardDeleteFailed'))
      }
    },
  })
}

async function selectDirectory(keys: Array<string | number>, info: DirectorySelectInfo) {
  const node = info.node
  if (node.kind === 'document') {
    if (!keys.length) return
    selectedFolderID.value = node.folderID
    await openDocument(String(node.entityId), false, node.key)
    return
  }
  if (sidebarCollapsed.value) expandSidebar()
  selectedFolderID.value = node.folderID
  expandedKeys.value = expandedKeys.value.includes(node.key)
    ? expandedKeys.value.filter((key) => key !== node.key)
    : [...expandedKeys.value, node.key]
}

async function moveDirectoryNode(info: DirectoryDropInfo) {
  if (workspaceMutating.value) return
  const dragged = info.dragNode
  const target = info.node
  if (!info.dropToGap && target.kind !== 'folder') return

  const findTreeNode = (nodes: DirectoryNode[], key: string): DirectoryNode | undefined => {
    for (const node of nodes) {
      if (node.key === key) return node
      const found = findTreeNode(node.children || [], key)
      if (found) return found
    }
    return undefined
  }
  const childrenOf = (parentKey: string) => {
    if (parentKey === 'root') {
      return directoryTreeData.value
    }
    return findTreeNode(directoryTreeData.value, parentKey)?.children || []
  }
  const findFolder = (nodes: FolderNode[], id: number): FolderNode | undefined => {
    for (const folder of nodes) {
      if (folder.id === id) return folder
      const found = findFolder(folder.children || [], id)
      if (found) return found
    }
    return undefined
  }

  const positionIndex = Number(target.pos?.split('-').at(-1) || 0)
  const relativeDropPosition = info.dropPosition - positionIndex
  const targetIsEmptyFolder = target.kind === 'folder' && !(target.children?.length)
  // Ant Tree 对空文件夹没有可展开的子区域，落在节点中部/下部会上报为后间隙。
  // 将“文档 + 空文件夹 + 非上方间隙”兼容为移入，上方间隙仍保留同级排序语义。
  const dropDocumentIntoFolder =
    dragged.kind === 'document' &&
    target.kind === 'folder' &&
    (!info.dropToGap || (targetIsEmptyFolder && relativeDropPosition >= 0))
  let targetParentKey: string
  if (dropDocumentIntoFolder) {
    targetParentKey = target.key
  } else if (info.dropToGap) {
    targetParentKey = target.parentKey
  } else {
    targetParentKey = target.key
  }

  const targetFolderID = targetParentKey.startsWith('folder:')
    ? Number(targetParentKey.slice('folder:'.length))
    : 0

  if (dragged.kind === 'folder' && Number(dragged.entityId) === targetFolderID) return

  if (dragged.kind === 'folder' && targetFolderID > 0) {
    const draggedFolderID = Number(dragged.entityId)
    const containsFolder = (folder: FolderNode, id: number): boolean =>
      (folder.children || []).some(
        (child) => child.id === id || containsFolder(child, id),
      )
    const draggedFolder = findFolder(folders.value, draggedFolderID)
    if (draggedFolder && containsFolder(draggedFolder, targetFolderID)) {
      message.error(t('knowledge.folderCycle'))
      return
    }
  }

  const sourceFolderID =
    dragged.kind === 'folder'
      ? findFolder(folders.value, Number(dragged.entityId))?.parent_id || 0
      : dragged.folderID
  const draggedOrderKey =
    dragged.kind === 'document' ? `doc:${dragged.entityId}` : dragged.key
  const targetSiblingKeys = childrenOf(targetParentKey)
    .map((node) => node.key)
    .filter((key) => key !== dragged.key && key !== draggedOrderKey)
  if (info.dropToGap && !dropDocumentIntoFolder) {
    const targetIndex = targetSiblingKeys.indexOf(target.key)
    const insertAfter = relativeDropPosition > 0
    targetSiblingKeys.splice(
      Math.max(0, targetIndex + (insertAfter ? 1 : 0)),
      0,
      draggedOrderKey,
    )
  } else {
    targetSiblingKeys.push(draggedOrderKey)
  }

  try {
    if (!(await flushTabs())) return
    workspaceMutating.value = true
    if (dragged.kind === 'folder' && sourceFolderID !== targetFolderID) {
      await apiClient.put(`/knowledge/folders/${dragged.entityId}`, {
        parent_id: targetFolderID,
      })
      await loadFolders()
      await loadDocuments()
      reconcileTabLocations()
    } else if (dragged.kind === 'document' && sourceFolderID !== targetFolderID) {
      const updated = await apiClient.put<KnowledgeDocument>(
        `/knowledge/documents/${dragged.entityId}`,
        { folder_id: targetFolderID },
      )
      updateDocumentInList(updated)
      reconcileTabLocations()
      if (draft.value.uuid === dragged.entityId) selectedTreeKey.value = draggedOrderKey
    }

    const nextDirectoryOrder = {
      ...directoryOrder.value,
      [targetParentKey]: targetSiblingKeys,
    }
    if (dragged.parentKey !== targetParentKey) {
      nextDirectoryOrder[dragged.parentKey] = (
        directoryOrder.value[dragged.parentKey] ||
        childrenOf(dragged.parentKey).map((node) => node.key)
      ).filter((key) => key !== dragged.key && key !== draggedOrderKey)
    }
    directoryOrder.value = nextDirectoryOrder
    persistDirectoryOrder()
    selectedFolderID.value = targetFolderID
    if (dragged.kind === 'document') {
      message.success(sourceFolderID === targetFolderID ? t('knowledge.documentOrderUpdated') : t('knowledge.documentMoved'))
    } else {
      message.success(sourceFolderID === targetFolderID ? t('knowledge.folderOrderUpdated') : t('knowledge.folderMoved'))
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.moveFailed'))
  } finally {
    workspaceMutating.value = false
    for (const tab of tabs.value) {
      if (tabDirty(tab) && tab.state !== 'error') scheduleSave(tab)
    }
  }
}

// 提取正文首行的 `# xxx` 标题；首行必须是 `# ` 开头（单 #）才返回；
// 返回纯文本标题，忽略颜色 span、强调、链接等内联标记。
function extractFirstHeading(content: string): string | null {
  if (!content) return null
  const firstLine = content.split(/\r?\n/, 1)[0]
  const match = /^#\s+(.+?)\s*#*\s*$/.exec(firstLine)
  if (!match) return null
  const heading = plainTextFromMarkdownInline(match[1].trim())
  return heading || null
}

function hasEmptyFirstHeading(content: string): boolean {
  const firstLine = content.split(/\r?\n/, 1)[0] || ''
  return /^#\s*$/.test(firstLine.replace(/[\u00a0\u200b]/g, ' '))
}

// 把标题清洗为合法文件名，与后端 internal/knowledge/store.go 的 SanitizeFileName 保持一致：
// 替换控制字符与 / \ : * ? " < > | 为下划线；去掉首尾 . 与空白；空结果回退到「未命名文档」。
// 创建/保存前调用一次，避免标题里含这些字符导致后端文件落盘失败。
function sanitizeTitle(title: string): string {
  const trimmed = (title || '').trim()
  let result = ''
  for (const ch of trimmed) {
    const code = ch.codePointAt(0) ?? 0
    const isControl = (code >= 0x00 && code <= 0x1f) || (code >= 0x7f && code <= 0x9f)
    if (
      isControl ||
      ch === '/' || ch === '\\' || ch === ':' || ch === '*' ||
      ch === '?' || ch === '"' || ch === '<' || ch === '>' || ch === '|'
    ) {
      result += '_'
    } else {
      result += ch
    }
  }
  result = result.replace(/^[. ]+|[. ]+$/g, '')
  return result || t('knowledge.untitled')
}

// 用新标题替换正文首行；保留其余内容；如果首行后没有空行则补一行，与默认 newContent 模板结构保持一致
function replaceFirstHeading(content: string, heading: string): string {
  const lines = content.split(/\r?\n/)
  if (!lines.length) return `# ${heading}\n`
  lines[0] = `# ${heading}`
  if (lines.length === 1 || lines[1].trim() !== '') {
    lines.splice(1, 0, '')
  }
  return lines.join('\n')
}

// 同目录下不冲突的标题：与后端 uniqueNewRelPath 的 (n) 后缀行为一致；
// 用于在创建/保存前预判重名，把 (n) 同步反映到正文首行，避免后端改 file_path 而前端 title/content 不一致。
// 同时对输入和已有文档 title 做 sanitize，确保含非法字符的脏数据不会漏判冲突。
function uniqueTitleInFolder(folderId: number, baseTitle: string, excludeUUID = ''): string {
  const normalized = sanitizeTitle(baseTitle)
  const exists = (candidate: string) =>
    documents.value.some(
      (doc) =>
        doc.folder_id === folderId &&
        doc.uuid !== excludeUUID &&
        sanitizeTitle(doc.title) === candidate,
    )
  if (!exists(normalized)) return normalized
  for (let i = 1; i < 10000; i++) {
    const candidate = `${normalized}(${i})`
    if (!exists(candidate)) return candidate
  }
  return normalized
}

// 用默认 newContent 模板构造新文档正文，只把首行标题换成传入的 title，保留「首行 + 空行 + 正文提示」结构
function buildNewDocumentContent(title: string): string {
  const template = t('knowledge.newContent')
  const lines = template.split(/\r?\n/)
  lines[0] = `# ${title}`
  return lines.join('\n')
}

async function createFolder(folderId: number) {
  if (folderCreating.value) return
  folderCreating.value = true
  try {
    const names = new Set(
      flatFolders.value.filter((folder) => folder.parent_id === folderId).map((folder) => folder.name),
    )
    const baseName = t('knowledge.untitledFolder')
    let name = baseName
    // 最多检查同级文件夹数量个后缀，总能找到一个未占用的名称。
    for (let index = 1; names.has(name) && index <= names.size; index++) {
      name = `${baseName}(${index})`
    }
    await createKnowledgeFolder({ name, parent_id: folderId })
    await loadFolders()
    if (folderId > 0) {
      const folderKey = `folder:${folderId}`
      if (!expandedKeys.value.includes(folderKey)) {
        expandedKeys.value = [...expandedKeys.value, folderKey]
      }
    }
    message.success(t('knowledge.folderCreated'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.folderCreateFailed'))
  } finally {
    folderCreating.value = false
  }
}

function renameSelectedFolder(folderID = selectedFolderID.value) {
  if (!folderID || folderID === 0) return
  const folder = flatFolders.value.find((item) => item.id === folderID)
  if (!folder) return
  selectedFolderID.value = folderID
  folderRenameTarget.value = folder
  folderRenameInputValue.value = folder.name
  folderRenameModalVisible.value = true
}

function cancelFolderRename() {
  if (folderRenameModalLoading.value) return
  folderRenameModalVisible.value = false
  folderRenameTarget.value = null
  folderRenameInputValue.value = ''
}

async function submitFolderRename() {
  const folder = folderRenameTarget.value
  const name = folderRenameInputValue.value.trim()
  if (!folder || !name || folderRenameModalLoading.value) return
  if (name === folder.name.trim()) {
    cancelFolderRename()
    return
  }
  folderRenameModalLoading.value = true
  try {
    await apiClient.put(`/knowledge/folders/${folder.id}`, { name })
    await loadFolders()
    folderRenameModalVisible.value = false
    folderRenameTarget.value = null
    folderRenameInputValue.value = ''
    message.success(t('knowledge.folderRenamed'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.renameFailed'))
  } finally {
    folderRenameModalLoading.value = false
  }
}

// S-UI-03: 删除文件夹双选项弹窗
function deleteSelectedFolder() {
  if (!selectedFolderID.value || selectedFolderID.value === 0) return
  const folder = flatFolders.value.find((item) => item.id === selectedFolderID.value)
  if (!folder) return
  deleteFolderTarget.value = folder
  deleteFolderModalVisible.value = true
}

async function confirmDeleteFolder(mode: 'keep' | 'purge') {
  const folder = deleteFolderTarget.value
  if (!folder) return
  deleteFolderModalLoading.value = true
  try {
    if (!(await flushTabs())) return
    await deleteKnowledgeFolder(folder.id, mode)
    deleteFolderModalVisible.value = false
    deleteFolderTarget.value = null
    // 删除后仍保留未受影响的文档标签，并校准文件所在位置。
    const parentID = folder.parent_id
    selectedFolderID.value = parentID
    selectedTreeKey.value = parentID > 0 ? `folder:${parentID}` : 'root'
    if (mode === 'purge') {
      await Promise.all([loadFolders(), loadDocuments(), loadTrash()])
    } else {
      await Promise.all([loadFolders(), loadDocuments()])
    }
    reconcileTabLocations()
    message.success(mode === 'purge' ? t('knowledge.folderDeleted') : t('knowledge.folderKept'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('knowledge.folderDeleteFailed'))
  } finally {
    deleteFolderModalLoading.value = false
  }
}

async function performSearch() {
  const keyword = searchKeyword.value.trim()
  const sequence = ++searchSequence
  if (!keyword) {
    searchResults.value = []
    searchLoading.value = false
    return
  }
  searchLoading.value = true
  try {
    const result = await apiClient.get<{ items: SearchResult[]; total: number }>(
      '/knowledge/search',
      { q: keyword },
    )
    if (sequence === searchSequence) searchResults.value = result.items || []
  } catch (error) {
    if (sequence === searchSequence) {
      message.error(error instanceof Error ? error.message : t('knowledge.searchFailed'))
    }
  } finally {
    if (sequence === searchSequence) searchLoading.value = false
  }
}

function scheduleSearch() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void performSearch(), 260)
}

// S-UI-10: 大纲跳转由编辑器定位第 index 个标题元素
function scrollToHeading(index: number) {
  editorRefs.get(draft.value.uuid)?.scrollToHeading(index)
}

async function flushTab(tab: KnowledgeTab) {
  await performSave('auto', tab)
  // 已有请求期间产生的新输入再保存一次；失败保留草稿，交给用户处理。
  if (tabDirty(tab) && tab.state !== 'error') await performSave('auto', tab)
}

async function flushTabs() {
  await Promise.all(tabs.value.map(flushTab))
  if (tabs.value.some(tabDirty)) {
    message.error(t('knowledge.pendingChangesBlocked'))
    return false
  }
  return true
}

function beforeUnload(event: BeforeUnloadEvent) {
  if (!tabs.value.some(tabDirty)) return
  event.preventDefault()
  event.returnValue = ''
}

onBeforeRouteLeave(async () => {
  await Promise.all(tabs.value.map(flushTab))
  for (const tab of tabs.value) {
    if (tabDirty(tab) && !(await confirmDiscardChanges(tab))) return false
  }
  persistWorkspace()
  return true
})

function handleKeyboard(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
    event.preventDefault()
    void saveDocument()
  }
}

onMounted(async () => {
  window.addEventListener('keydown', handleKeyboard)
  window.addEventListener('beforeunload', beforeUnload)
  try {
    directoryOrder.value = JSON.parse(
      localStorage.getItem('knowledge-directory-order') || '{}',
    )
  } catch {
    directoryOrder.value = {}
  }
  await loadKnowledgeRoot()
  if (await loadWorkspace(true)) await restoreWorkspace()
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeyboard)
  window.removeEventListener('beforeunload', beforeUnload)
  persistWorkspace()
  if (searchTimer) clearTimeout(searchTimer)
  // 防止组件卸载时仍残留拖拽监听与光标样式
  sidebarResizeCleanup?.()
  disposed = true
  openSequence += 1
  editorRefs.clear()
})
</script>

<template>
  <div class="knowledge-shell">
    <div
      class="knowledge-page"
      :class="{ 'is-loading': loading, 'is-sidebar-collapsed': sidebarCollapsed, 'is-sidebar-narrow': isSidebarNarrow }"
      :style="knowledgePageStyle"
    >
      <aside v-show="!sidebarCollapsed" class="knowledge-sidebar">
        <header class="knowledge-titlebar">
          <span class="knowledge-title" :title="t('knowledge.title')">{{ t('knowledge.title') }}</span>
          <div v-if="sidebarWidth >= 140" class="directory-actions">
            <a-dropdown :trigger="['click']" placement="bottomRight">
              <button class="icon-button titlebar-action" type="button" :aria-label="t('knowledge.settings')">
                <img :src="knowledgeSettingsIcon" alt="" aria-hidden="true" class="knowledge-toolbar-icon" />
              </button>
              <template #overlay>
                <a-menu>
                  <a-menu-item key="root" @click="openRootModal">
                    <img :src="knowledgeRootMenuIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                    {{ t('knowledge.knowledgeRootLabel') }}
                  </a-menu-item>
                  <a-menu-item key="trash" @click="openTrash">
                    <img :src="knowledgeTrashMenuIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                    {{ t('knowledge.trash') }}
                  </a-menu-item>
                  <a-menu-item key="refresh" :disabled="scanning" @click="refreshWorkspace">
                    <img
                      :src="knowledgeRefreshIcon"
                      alt=""
                      aria-hidden="true"
                      class="knowledge-menu-icon"
                      :class="{ spinning: scanning }"
                    />
                    {{ t('knowledge.refreshDirectory') }}
                  </a-menu-item>
                  <a-menu-item key="appearance" @click="appearanceSettingsOpen = true">
                    <img :src="knowledgeSettingsIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                    {{ t('knowledge.appearanceSettings') }}
                  </a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
            <a-dropdown :trigger="['click']" placement="bottomRight">
              <button class="icon-button add-button" type="button" :aria-label="t('knowledge.add')">
                <img :src="knowledgeAddIcon" alt="" aria-hidden="true" class="knowledge-toolbar-icon" />
              </button>
              <template #overlay>
                <KnowledgeAddMenu
                  :folder-creating="folderCreating"
                  @create-document="createDocument(0)"
                  @create-folder="createFolder(0)"
                />
              </template>
            </a-dropdown>
          </div>
        </header>
        <!-- 目录栏右侧的可拖拽改宽度把手（仅在展开时可见，折叠后自动隐藏） -->
        <button
          v-show="!sidebarCollapsed"
          class="sidebar-resize-handle"
          type="button"
          :aria-label="t('knowledge.resizeSidebar')"
          :title="t('knowledge.resizeSidebar')"
          @mousedown="startSidebarResize"
        />
        <!-- S-UI-07: 空知识库不自动创建内容，仅给创建入口与引导提示 -->
        <div v-if="knowledgeEmpty && !searchKeyword.trim()" class="sidebar-empty-action">
          <button class="create-first-button" type="button" @click="createDocument()">
            <PlusOutlined />
            <span>{{ t('knowledge.emptyCreateFirst') }}</span>
          </button>
        </div>
        <div v-else class="sidebar-search">
          <a-input
            v-model:value="searchKeyword"
            allow-clear
            :placeholder="t('knowledge.search')"
            @input="scheduleSearch"
            @press-enter="performSearch"
          >
            <template #suffix><SearchOutlined /></template>
          </a-input>
        </div>

        <KnowledgeBlankAddDropdown
          class="directory-tree-area"
          :disabled="loading || searchLoading || scanning"
          :folder-creating="folderCreating"
          @create-document="createDocument(0)"
          @create-folder="createFolder(0)"
        >
          <a-spin :spinning="searchLoading" class="directory-spin">
            <div class="directory-scroll">
              <a-tree
                data-knowledge-add-ignore
                block-node
                :draggable="true"
                :tree-data="directoryTreeData"
                :selected-keys="trashVisible || !selectedTreeKey ? [] : [selectedTreeKey]"
                :expanded-keys="expandedKeys"
                @select="selectDirectory"
                @drop="moveDirectoryNode"
                @expand="(keys: string[]) => { expandedKeys = keys }"
              >
                <template #switcherIcon="{ expanded }">
                  <img
                    :src="knowledgeTreeToggleIcon"
                    alt=""
                    aria-hidden="true"
                    class="knowledge-tree-toggle-icon"
                    :class="{ 'is-expanded': expanded }"
                  />
                </template>
                <template #title="{ title, key, kind, entityId, count }">
                  <div class="directory-node" :class="`node-${kind}`">
                    <img
                      v-if="kind === 'folder'"
                      :src="knowledgeFolderTreeIcon"
                      alt=""
                      aria-hidden="true"
                      class="node-icon"
                    />
                    <img
                      v-else
                      :src="knowledgeDocumentIcon"
                      alt=""
                      aria-hidden="true"
                      class="node-icon document-icon"
                    />
                    <span class="node-title">{{ title }}</span>
                    <span v-if="kind === 'folder'" class="node-count">{{ count }}</span>
                    <!-- 文件夹 / 文档 hover 弹出三点按钮：所有操作统一收纳到 dropdown -->
                    <a-dropdown
                      v-if="kind === 'folder' || kind === 'document'"
                      :trigger="['click']"
                      placement="bottomRight"
                    >
                      <span
                        class="node-actions"
                        role="button"
                        tabindex="0"
                        :aria-label="t('knowledge.moreActionsTip')"
                        @click.stop
                      >
                        <img :src="knowledgeMoreIcon" alt="" aria-hidden="true" class="knowledge-more-icon" />
                      </span>
                      <template #overlay>
                        <a-menu>
                          <template v-if="kind === 'folder'">
                            <a-menu-item
                              key="new-doc"
                              @click="createDocument(Number(entityId))"
                            >
                              <img :src="knowledgeAddDocumentIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('knowledge.addDocument') }}
                            </a-menu-item>
                            <a-menu-item
                              key="new-folder"
                              :disabled="folderCreating"
                              @click="createFolder(Number(entityId))"
                            >
                              <img :src="knowledgeAddFolderIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('knowledge.addFolder') }}
                            </a-menu-item>
                            <a-menu-item
                              key="rename"
                              @click="renameSelectedFolder(Number(entityId))"
                            >
                              <img :src="knowledgeRenameIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('knowledge.rename') }}
                            </a-menu-item>
                            <a-menu-item
                              v-if="String(key).startsWith('folder:')"
                              key="delete"
                              danger
                              @click="selectedFolderID = Number(entityId); deleteSelectedFolder()"
                            >
                              <img :src="knowledgeDeleteIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('common.actions.delete') }}
                            </a-menu-item>
                          </template>
                          <template v-else>
                            <a-menu-item
                              key="rename-doc"
                              @click="handleRenameDocument(String(entityId))"
                            >
                              <img :src="knowledgeRenameIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('knowledge.rename') }}
                            </a-menu-item>
                            <a-menu-item
                              key="delete-doc"
                              danger
                              @click="handleDeleteDocument(String(entityId))"
                            >
                              <img :src="knowledgeDeleteIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
                              {{ t('common.actions.delete') }}
                            </a-menu-item>
                          </template>
                        </a-menu>
                      </template>
                    </a-dropdown>
                  </div>
                </template>
              </a-tree>
              <div v-if="searchKeyword && !directoryDocuments.length" class="directory-empty">
                {{ t('knowledge.noMatches') }}
              </div>
            </div>
          </a-spin>
        </KnowledgeBlankAddDropdown>

      </aside>

      <main v-if="trashVisible" class="trash-workspace">
        <div class="trash-header">
          <div class="trash-header-leading">
            <button
              class="icon-button sidebar-nav-toggle"
              type="button"
              :aria-label="sidebarCollapsed ? t('knowledge.expandSidebar') : t('knowledge.collapseSidebar')"
              @click="toggleSidebar"
            >
              <img
                :src="directoryToggleIcon"
                alt=""
                aria-hidden="true"
                class="sidebar-nav-toggle-icon"
                :class="{ 'is-collapsed': sidebarCollapsed }"
              />
            </button>
            <h2>{{ t('knowledge.trash') }}</h2>
          </div>
          <a-tooltip :title="t('common.actions.refresh')">
            <button class="icon-button" type="button" :aria-label="t('knowledge.refreshTrash')" @click="refreshTrash">
              <img :src="knowledgeRefreshIcon" alt="" aria-hidden="true" class="knowledge-toolbar-icon" />
            </button>
          </a-tooltip>
        </div>
        <div v-if="trashDocuments.length" class="trash-list">
          <div v-for="document in trashDocuments" :key="document.uuid" class="trash-card">
            <div class="trash-document-icon">
              <img :src="knowledgeDocumentTrashIcon" alt="" aria-hidden="true" />
            </div>
            <div class="trash-card-main">
              <div class="trash-card-title">{{ document.title }}</div>
              <div class="trash-card-meta">{{ t('knowledge.deletedAt', { time: formatTime(document.updated_at) }) }}</div>
            </div>
            <div class="trash-card-actions">
              <a-tooltip :title="t('knowledge.restore')">
                <button
                  class="icon-button"
                  type="button"
                  :aria-label="t('knowledge.restore')"
                  @click="restoreDocument(document)"
                >
                  <img
                    :src="trashRestoreIcon"
                    alt=""
                    aria-hidden="true"
                    class="trash-action-icon"
                  />
                </button>
              </a-tooltip>
              <a-tooltip :title="t('knowledge.deletePermanently')">
                <button
                  class="icon-button danger-button"
                  type="button"
                  :aria-label="t('knowledge.deletePermanently')"
                  @click="hardDeleteDocument(document)"
                >
                  <img
                    :src="knowledgeDeletePermanentIcon"
                    alt=""
                    aria-hidden="true"
                    class="trash-action-icon"
                  />
                </button>
              </a-tooltip>
            </div>
          </div>
        </div>
        <div v-else class="trash-empty">{{ t('knowledge.trashEmpty') }}</div>
      </main>

      <main v-else-if="draft.uuid" class="editor-workspace" :class="{ 'is-inspector-collapsed': !inspectorOpen }">
        <a-tabs
          class="document-tabs"
          :active-key="activeUUID"
          type="editable-card"
          hide-add
          @change="(key: string | number) => activateTab(String(key))"
          @edit="(key: string | number) => closeTab(String(key))"
        >
          <a-tab-pane v-for="tab in tabs" :key="tab.draft.uuid">
            <template #tab>
              <span :title="`${tab.draft.title}.${tab.draft.ext}`" class="document-tab-title">
                {{ `${tab.draft.title}.${tab.draft.ext}` }}
              </span>
              <span v-if="tab.state === 'error'" role="status">{{ t('knowledge.tabSaveFailed') }}</span>
              <span v-else-if="tabDirty(tab)" :aria-label="t('knowledge.autosavePending')">{{ t('knowledge.tabUnsavedMark') }}</span>
            </template>
          </a-tab-pane>
        </a-tabs>
        <section class="editor-body">
          <div class="editor-floating-tools">
            <a-tooltip v-if="autosaveState === 'error'" :title="autosaveErrorMessage || t('knowledge.autosaveFailed')">
              <a-button
                type="text"
                class="autosave-indicator autosave-indicator--error"
                :aria-label="t('knowledge.autosaveFailed')"
                @click="retryAutosave"
              >
                <ReloadOutlined />
              </a-button>
            </a-tooltip>
            <a-tooltip v-else-if="autosaveHint" :title="autosaveHint">
              <span
                class="autosave-indicator"
                :class="{
                  'autosave-indicator--pending': autosaveState === 'pending',
                  'autosave-indicator--saving': autosaveState === 'saving',
                  'autosave-indicator--saved': autosaveState === 'saved',
                }"
                role="status"
                :aria-label="autosaveHint"
              >
                <span v-if="autosaveState === 'saving'" class="autosave-spinner" aria-hidden="true" />
                <CheckOutlined v-else-if="autosaveState === 'saved'" aria-hidden="true" />
                <ClockCircleOutlined v-else aria-hidden="true" />
              </span>
            </a-tooltip>
            <a-popover
              v-model:open="documentActionsOpen"
              trigger="hover"
              placement="bottom"
              :overlay-inner-style="{ padding: '4px 8px' }"
            >
              <a-button
                type="text"
                class="editor-settings-trigger"
                :aria-label="t('knowledge.settings')"
                :aria-expanded="documentActionsOpen"
                @click="documentActionsOpen = true"
                @keydown.esc="documentActionsOpen = false"
              >
                <img :src="knowledgeSettingsIcon" alt="" aria-hidden="true" class="knowledge-toolbar-icon" />
              </a-button>
              <template #content>
                <div
                  class="editor-settings-actions"
                  role="group"
                  :aria-label="t('knowledge.moreActions')"
                  @click="documentActionsOpen = false"
                  @keydown.esc="documentActionsOpen = false"
                >
                  <a-tooltip placement="left" :title="t('knowledge.copyDocument')">
                    <a-button
                      type="text"
                      :disabled="!draft.content"
                      :aria-label="t('knowledge.copyDocument')"
                      @click="copyDocumentContent"
                    >
                      <img :src="knowledgeCopyDocumentIcon" alt="" aria-hidden="true" class="knowledge-toolbar-icon" />
                    </a-button>
                  </a-tooltip>
                  <a-tooltip
                    v-if="isMarkdownDraft && activeTab"
                    placement="left"
                    :title="t(editorMode === 'source' ? 'knowledge.visualMode' : 'knowledge.sourceMode')"
                  >
                    <a-button
                      type="text"
                      class="source-mode-toggle"
                      :class="{ 'is-active': editorMode === 'source' }"
                      :aria-label="t(editorMode === 'source' ? 'knowledge.visualMode' : 'knowledge.sourceMode')"
                      :aria-pressed="editorMode === 'source'"
                      @click="toggleEditorMode"
                    >
                      <img :src="knowledgeSourceModeIcon" class="source-mode-icon" alt="" aria-hidden="true" />
                    </a-button>
                  </a-tooltip>
                  <a-tooltip placement="left" :title="sidebarCollapsed ? t('knowledge.expandSidebar') : t('knowledge.collapseSidebar')">
                    <a-button
                      type="text"
                      :aria-label="sidebarCollapsed ? t('knowledge.expandSidebar') : t('knowledge.collapseSidebar')"
                      :aria-expanded="!sidebarCollapsed"
                      @click="toggleSidebar"
                    >
                      <img
                        :src="directoryToggleIcon"
                        alt=""
                        aria-hidden="true"
                        class="sidebar-nav-toggle-icon"
                        :class="{ 'is-collapsed': sidebarCollapsed }"
                      />
                    </a-button>
                  </a-tooltip>
                  <a-tooltip placement="left" :title="inspectorOpen ? t('knowledge.inspectorCollapse') : t('knowledge.inspectorExpand')">
                    <a-button
                      type="text"
                      :aria-label="inspectorOpen ? t('knowledge.inspectorCollapse') : t('knowledge.inspectorExpand')"
                      :aria-expanded="inspectorOpen"
                      @click="toggleInspector"
                    >
                      <img :src="directoryToggleIcon" alt="" aria-hidden="true" class="inspector-nav-toggle-icon" />
                    </a-button>
                  </a-tooltip>
                </div>
              </template>
            </a-popover>
          </div>

          <KnowledgeDocumentEditor
            v-for="tab in tabs"
            v-show="tab.draft.uuid === activeUUID"
            :key="tab.draft.uuid"
            :ref="(instance) => setEditorRef(tab.draft.uuid, instance)"
            v-model="tab.draft.content"
            :markdown="tab.draft.ext === 'md'"
            :mode="tab.draft.ext === 'md' ? editorMode : 'visual'"
            :font-size="appearance.fontSize"
            :line-height="appearance.lineHeight"
            :text-color="appearance.textColor"
            :disabled="workspaceMutating || rootModalLoading || deleteFolderModalLoading"
          />
        </section>

        <a-popover trigger="hover" placement="leftTop" overlay-class-name="knowledge-outline-popover">
          <button
            class="editor-outline-trigger"
            type="button"
            :aria-label="t('knowledge.documentOutline')"
          >
            <img :src="knowledgeOutlineIcon" alt="" aria-hidden="true" class="knowledge-outline-icon" />
          </button>
          <template #content>
            <div class="document-outline-popover">
              <div class="document-outline-title">{{ t('knowledge.documentOutline') }}</div>
              <div v-if="outline.length" class="outline-list">
                <button
                  v-for="(item, index) in outline"
                  :key="item.key"
                  class="outline-item"
                  :style="{ paddingLeft: `${12 + (item.level - 1) * 12}px` }"
                  @click="scrollToHeading(index)"
                >
                  {{ item.title }}
                </button>
              </div>
              <div v-else class="inspector-empty">{{ t('knowledge.noOutline') }}</div>
            </div>
          </template>
        </a-popover>
      </main>

      <main v-else class="knowledge-empty">
        <button
          class="icon-button empty-sidebar-toggle"
          type="button"
          :aria-label="sidebarCollapsed ? t('knowledge.expandSidebar') : t('knowledge.collapseSidebar')"
          @click="toggleSidebar"
        >
          <img :src="directoryToggleIcon" alt="" aria-hidden="true" class="sidebar-nav-toggle-icon" />
        </button>
        <div class="empty-state">
          <div class="empty-illustration" aria-hidden="true">
            <FolderOutlined class="empty-folder" />
            <FileTextOutlined class="empty-document" />
            <span class="empty-check"><CheckOutlined /></span>
          </div>
          <h2>{{ knowledgeEmpty ? t('knowledge.emptyNoContent') : t('knowledge.emptyTitle') }}</h2>
          <p v-if="!knowledgeEmpty">{{ t('knowledge.emptyDescription') }}</p>
        </div>
      </main>

      <aside v-if="!trashVisible && draft.uuid && inspectorOpen" class="knowledge-inspector">
        <h2 class="inspector-title">{{ t('knowledge.properties') }}</h2>
        <dl class="property-list">
          <div>
            <dt>{{ t('knowledge.folder') }}</dt>
            <dd>
              <a-select
                v-model:value="draft.folderID"
                class="property-folder-select"
                :bordered="false"
                :show-arrow="false"
              >
                <a-select-option :value="0">{{ t('knowledge.defaultFolder') }}</a-select-option>
                <a-select-option v-for="folder in flatFolders" :key="folder.id" :value="folder.id">
                  {{ `${'\u3000'.repeat(folder.depth)}${folder.name}` }}
                </a-select-option>
              </a-select>
            </dd>
          </div>
          <div><dt>{{ t('knowledge.wordCount') }}</dt><dd>{{ draft.wordCount }}</dd></div>
          <div><dt>{{ t('knowledge.createdAt') }}</dt><dd>{{ formatTime(draft.createdAt) }}</dd></div>
          <div><dt>{{ t('knowledge.updatedAt') }}</dt><dd>{{ formatTime(draft.updatedAt) }}</dd></div>
        </dl>
      </aside>

    <!-- S-UI-01: 设置知识库目录弹窗（S-UI-17: 迁移语义 + 二次确认 + 全流程反馈） -->
    <KnowledgeAppearanceSettings
      v-model:open="appearanceSettingsOpen"
      :value="appearance"
      @change="updateAppearance"
    />
    <a-modal
      v-model:open="rootModalVisible"
      :width="584"
      wrap-class-name="knowledge-modal knowledge-root-modal"
      :title="t('knowledge.knowledgeRootDialogTitle')"
      :confirm-loading="rootModalLoading"
      :closable="!rootModalLoading"
      :mask-closable="false"
      :keyboard="!rootModalLoading"
      :ok-text="t('common.actions.save')"
      :cancel-text="t('knowledge.cancel')"
      :ok-button-props="{ disabled: !pendingRootDir.trim() || rootModalLoading }"
      :cancel-button-props="{ disabled: rootModalLoading }"
      @ok="confirmSetKnowledgeRoot"
      @cancel="cancelRootModal"
    >
      <p class="root-modal-description">{{ t('knowledge.knowledgeRootDialogDescription') }}</p>
      <div class="root-current-path">
        <img :src="knowledgeRootCurrentIcon" alt="" aria-hidden="true" class="root-current-path-icon" />
        <div class="root-current-path-label">{{ t('knowledge.knowledgeRootCurrent') }}</div>
        <div class="root-breadcrumb root-breadcrumb-modal">
          <template v-for="(segment, index) in modalRootBreadcrumbSegments" :key="index">
            <span class="root-breadcrumb-segment">{{ segment }}</span>
            <span
              v-if="index < modalRootBreadcrumbSegments.length - 1"
              class="root-breadcrumb-separator"
            >{{ rootPathSeparator }}</span>
          </template>
        </div>
      </div>
      <div v-if="rootModalLoading" class="root-migrate-state">
        <a-spin size="small" />
        <span>{{ t('knowledge.migrateRunning') }}</span>
      </div>
      <button
        v-if="isDesktopRuntime()"
        class="root-select-button"
        type="button"
        :disabled="rootModalLoading"
        @click="chooseRootDirectory"
      >
        <img :src="knowledgeRootSelectIcon" alt="" aria-hidden="true" />
        {{ t('knowledge.knowledgeRootSelectFolder') }}
      </button>
      <a-form v-else layout="vertical">
        <a-form-item :label="t('knowledge.knowledgeRootManualPath')">
          <a-input
            v-model:value="pendingRootDir"
            :placeholder="t('knowledge.knowledgeRootPathPlaceholder')"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 文件夹重命名弹窗 -->
    <a-modal
      v-model:open="folderRenameModalVisible"
      :width="584"
      wrap-class-name="knowledge-modal knowledge-rename-modal"
      :title="t('knowledge.renameDialogTitle')"
      :ok-text="t('knowledge.renameConfirm')"
      :cancel-text="t('knowledge.cancel')"
      :confirm-loading="folderRenameModalLoading"
      :ok-button-props="{ disabled: !folderRenameInputValue.trim() }"
      :cancel-button-props="{ disabled: folderRenameModalLoading }"
      :mask-closable="false"
      @ok="submitFolderRename"
      @cancel="cancelFolderRename"
    >
      <a-form layout="vertical">
        <a-form-item>
          <a-input
            v-model:value="folderRenameInputValue"
            :placeholder="t('knowledge.renameDialogPlaceholder')"
            autofocus
            @press-enter="submitFolderRename"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- S-UI-03: 删除文件夹双选项弹窗 -->
    <a-modal
      v-model:open="deleteFolderModalVisible"
      :title="t('knowledge.deleteFolderTitle', { name: deleteFolderTarget?.name || '' })"
      :confirm-loading="deleteFolderModalLoading"
      :footer="null"
      @cancel="deleteFolderModalVisible = false"
    >
      <div class="delete-folder-options">
        <button
          class="delete-folder-option keep"
          type="button"
          :disabled="deleteFolderModalLoading"
          @click="confirmDeleteFolder('keep')"
        >
          <strong>{{ t('knowledge.deleteFolderKeep') }}</strong>
          <span>{{ t('knowledge.deleteFolderKeepDescription') }}</span>
        </button>
        <button
          class="delete-folder-option purge"
          type="button"
          :disabled="deleteFolderModalLoading"
          @click="confirmDeleteFolder('purge')"
        >
          <strong>{{ t('knowledge.deleteFolderPurge') }}</strong>
          <span>{{ t('knowledge.deleteFolderPurgeDescription') }}</span>
        </button>
      </div>
      <div class="delete-folder-modal-footer">
        <a-button :disabled="deleteFolderModalLoading" @click="deleteFolderModalVisible = false">
          {{ t('knowledge.cancel') }}
        </a-button>
      </div>
    </a-modal>

    <!-- 文档重命名弹窗（取代原 editor-header 内的标题就地编辑）。提交只更新 draft.title，
         由草稿防抖自动保存落盘；与新建/编辑文档共用 title 校验。 -->
    <a-modal
      v-model:open="renameModalVisible"
      :width="584"
      wrap-class-name="knowledge-modal knowledge-rename-modal"
      :title="t('knowledge.renameDialogTitle')"
      :ok-text="t('knowledge.renameConfirm')"
      :cancel-text="t('knowledge.cancel')"
      :mask-closable="false"
      @ok="submitRename"
      @cancel="cancelRename"
    >
      <a-form layout="vertical">
        <a-form-item>
          <a-input
            v-model:value="renameInputValue"
            :placeholder="t('knowledge.renameDialogPlaceholder')"
            autofocus
            @press-enter="submitRename"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    </div>
  </div>
</template>

<style scoped>
.empty-sidebar-toggle { position: absolute; top: 12px; left: 12px; }
.document-tabs { flex: 0 0 auto; min-width: 0; }
.document-tabs :deep(.ant-tabs-nav) { margin: 0; }
.document-tabs :deep(.ant-tabs-content-holder) { display: none; }
.document-tabs :deep(.ant-tabs-tab-active .ant-tabs-tab-btn) {
  color: #262626;
  font-weight: 600;
  text-shadow: none;
}
.document-tab-title {
  display: inline-block;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

.knowledge-shell {
  /* 使用标题栏下方的可用高度；clip 阻止光标定位滚动外层，正文仍由编辑器内部滚动。 */
  height: 100%;
  overflow: clip;
  color: #262626;
  background: #fff;
}

.knowledge-titlebar {
  display: flex;
  height: 44px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
}

.knowledge-title {
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

.knowledge-page {
  display: grid;
  grid-template-columns: var(--knowledge-sidebar-width, 300px) minmax(480px, 1fr) 229px;
  height: 100%;
  min-height: 0;
  overflow: clip;
  background: #fff;
}

.knowledge-sidebar {
  position: relative;
  display: flex;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  padding: 0 24px 20px;
  border-right: 1px solid #e6e8eb;
  background: #fff;
}

/* 可拖拽调整宽度的把手 */
.sidebar-resize-handle {
  position: absolute;
  top: 0;
  right: -2px;
  width: 4px;
  height: 100%;
  cursor: col-resize;
  background: transparent;
  border: 0;
  padding: 0;
  z-index: 10;
  transition: background 0.18s ease;
}

.sidebar-resize-handle:hover,
.sidebar-resize-handle:focus-visible {
  background: rgba(49, 87, 226, 0.35);
}

.sidebar-resize-handle:focus-visible {
  outline: none;
}

.icon-button {
  display: inline-flex;
  width: 28px;
  height: 28px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #5f6368;
  background: transparent;
  cursor: pointer;
  font-size: 16px;
  transition: background 0.16s ease, color 0.16s ease;
}

.icon-button:hover {
  color: #3157e2;
  background: #f0f1f3;
}

.icon-button:disabled {
  cursor: default;
  opacity: 0.55;
}

.add-button {
  width: 24px;
  height: 24px;
  color: #2f6bff;
  font-size: 16px;
}

.titlebar-action {
  width: 24px;
  height: 24px;
}

.sidebar-search {
  flex: 0 0 auto;
  padding: 8px 0 12px;
}

.sidebar-search :deep(.ant-input-affix-wrapper) {
  height: 32px;
  padding: 4px 11px;
  border-color: #dadde2;
  border-radius: 6px;
  box-shadow: none;
  font-size: 14px;
}

.sidebar-search :deep(.ant-input-suffix) {
  color: #555a61;
  font-size: 16px;
}

.sidebar-empty-action {
  flex: 0 0 auto;
  padding-bottom: 14px;
}

/* S-UI-07: 空知识库的创建入口（不自动创建任何内容，仅给按钮与引导） */
.create-first-button {
  display: flex;
  width: 100%;
  height: 40px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border: 1px solid #d9d9d9;
  border-radius: 12px;
  color: #595959;
  background: #fff;
  cursor: pointer;
  font-size: 14px;
  transition: color 0.18s ease, border-color 0.18s ease, background 0.18s ease;
}

.create-first-button:hover {
  border-color: #3157e2;
  color: #3157e2;
  background: #f9fafb;
}

.create-first-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.directory-tree-area {
  min-height: 0;
  flex: 1;
  overflow: hidden;
}

.directory-tree-area :deep(.ant-spin-nested-loading),
.directory-tree-area :deep(.ant-spin-container) {
  display: block;
  width: 100%;
  min-height: 0;
  height: 100%;
  overflow: hidden;
}

.directory-scroll {
  min-height: 0;
  height: 100%;
  max-height: 100%;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 0 0 16px;
  scrollbar-gutter: stable;
}

.directory-scroll :deep(.ant-tree) {
  color: #292b2f;
  background: transparent;
  font-size: 14px;
}

.directory-scroll :deep(.ant-tree-treenode) {
  width: 100%;
  min-height: 32px;
  align-items: center;
  padding: 0 0 2px;
}

.directory-scroll :deep(.ant-tree-switcher) {
  display: flex;
  width: 22px;
  height: 32px;
  align-items: center;
  justify-content: center;
  color: #303236;
}

.directory-scroll :deep(.ant-tree-node-content-wrapper) {
  min-width: 0;
  height: 32px;
  flex: 1;
  padding: 0 10px 0 4px;
  border-radius: 8px;
  line-height: 32px;
}

.directory-scroll :deep(.ant-tree-node-content-wrapper:hover) {
  background: #f5f5f5;
}

.directory-scroll :deep(.ant-tree-node-content-wrapper.ant-tree-node-selected) {
  background: #f0f0f0;
}

.directory-scroll :deep(.ant-tree-treenode.drag-over .ant-tree-node-content-wrapper),
.directory-scroll :deep(.ant-tree-treenode.drag-over-gap-top .ant-tree-node-content-wrapper),
.directory-scroll :deep(.ant-tree-treenode.drag-over-gap-bottom .ant-tree-node-content-wrapper) {
  outline: 1px solid #7aa3ff;
  color: #2458d3;
  background: #e9f1ff;
}

.directory-node {
  display: flex;
  min-width: 0;
  height: 32px;
  align-items: center;
  gap: 8px;
}

.node-icon {
  display: block;
  flex: 0 0 auto;
}

.knowledge-tree-toggle-icon {
  display: block;
  transition: transform 0.18s ease;
}

.knowledge-tree-toggle-icon.is-expanded {
  transform: rotate(90deg);
}

.document-icon {
  display: block;
}

.node-title {
  overflow: hidden;
  flex: 1;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-count,
.trash-count {
  flex: 0 0 auto;
  color: #9ca0a7;
  font-size: 12px;
}

.directory-empty {
  padding: 24px 12px;
  color: #999da4;
  font-size: 14px;
  text-align: center;
}

.trash-entry {
  display: grid;
  height: 32px;
  flex: 0 0 auto;
  grid-template-columns: 20px 1fr auto;
  align-items: center;
  gap: 8px;
  padding: 0 12px;
  border: 0;
  border-radius: 8px;
  color: #33363b;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  text-align: left;
}

.trash-entry:hover,
.trash-entry.active {
  background: #f0f0f0;
}

.trash-entry.active .trash-count {
  color: #595959;
}

.editor-workspace,
.trash-workspace,
.knowledge-empty {
  min-width: 0;
  min-height: 0;
}

.editor-workspace {
  position: relative;
  display: flex;
  flex-direction: column;
  overflow: clip;
  /* 明确占据中间列，由 inspectorOpen 控制 inspector aside 是否显示 */
  grid-column: 2;
}

/* S-UI-14: 右侧属性面板收起时，让编辑器工作区横跨中间与右侧两列，
   避免出现 300px 的空白列。展开属性面板时恢复到默认单列宽度。 */
.editor-workspace.is-inspector-collapsed {
  grid-column: 2 / 4;
}

.editor-floating-tools {
  position: absolute;
  z-index: 5;
  top: 6px;
  right: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.editor-settings-trigger,
.editor-settings-actions :deep(.ant-btn),
.autosave-indicator {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  padding: 0;
  border-radius: 6px;
}

.editor-settings-trigger:focus-visible,
.editor-settings-actions :deep(.ant-btn:focus-visible),
.autosave-indicator:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.editor-settings-actions {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.editor-settings-actions :deep(.ant-btn) {
  width: 28px;
  height: 28px;
}

.autosave-indicator {
  color: #8c8c8c;
  font-size: 16px;
}

.autosave-indicator--pending,
.autosave-indicator--saved {
  color: #595959;
}

.autosave-indicator--saving {
  color: #3157e2;
}

.autosave-indicator--error {
  color: #fb363f;
  background: #fef2f2;
}

.autosave-indicator--error:hover {
  color: #fb363f;
  background: #fef2f2;
}

.autosave-spinner {
  display: inline-block;
  width: 12px;
  height: 12px;
  border: 1.5px solid currentColor;
  border-top-color: transparent;
  border-radius: 50%;
  animation: autosave-spin 0.8s linear infinite;
}

@media (prefers-reduced-motion: reduce) {
  .autosave-spinner { animation: none; }
}

@keyframes autosave-spin {
  to {
    transform: rotate(360deg);
  }
}

/* S-UI-09: 编辑器工作区由 TipTap 所见即所得编辑器承载。 */
.editor-body {
  position: relative;
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  overflow: clip;
  background: #fff;
}

.knowledge-inspector {
  min-width: 0;
  overflow-y: auto;
  padding: 24px;
  border-left: 1px solid #f0f0f0;
  background: #fff;
}

.inspector-title {
  margin: 0 0 16px;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.outline-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.outline-item {
  overflow: hidden;
  width: 100%;
  padding-top: 6px;
  padding-bottom: 6px;
  border: 0;
  border-radius: 5px;
  color: #59606d;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.outline-item:hover {
  color: #3157e2;
  background: #eef2ff;
}

.property-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin: 0;
}

.property-list > div {
  padding: 0;
}

.property-list dt {
  margin-bottom: 4px;
  color: #9298a4;
  font-size: 12px;
  line-height: 20px;
}

.property-list dd {
  margin: 0;
  color: #3e4450;
  font-size: 14px;
  line-height: 22px;
}

.property-folder-select {
  width: 100%;
  margin: -5px 0;
}

.property-folder-select :deep(.ant-select-selector) {
  padding: 0 !important;
  box-shadow: none !important;
}

.property-folder-select :deep(.ant-select-selection-item) {
  padding: 0;
  color: #3e4450;
  font-size: 14px;
  line-height: 32px;
}

.editor-outline-trigger {
  position: absolute;
  z-index: 5;
  top: 30%;
  right: 0;
  display: inline-flex;
  width: 28px;
  height: 48px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px 0 0 6px;
  color: #8c8c8c;
  background: #fff;
  cursor: pointer;
  font-size: 16px;
}

.editor-outline-trigger:hover {
  color: #3157e2;
  background: #f5f8ff;
}

.document-outline-popover {
  width: 199px;
  max-height: 60vh;
  overflow-y: auto;
}

.document-outline-title {
  margin-bottom: 8px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.folder-select {
  width: 100%;
}

.inspector-empty {
  padding: 38px 12px;
  color: #a0a4ab;
  font-size: 13px;
  text-align: center;
}


.trash-workspace {
  display: flex;
  min-height: 0;
  flex-direction: column;
  grid-column: 2 / 4;
  overflow: hidden;
}

.trash-header {
  display: flex;
  height: 44px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  border-bottom: 1px solid #f0f0f0;
}

.trash-header-leading {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.trash-header h2 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  color: #595959;
  font-size: 14px;
  font-weight: 400;
}

.trash-list {
  display: flex;
  min-height: 0;
  flex-direction: column;
  overflow-y: auto;
  padding: 0 24px 24px;
}

.trash-card {
  display: flex;
  min-height: 49px;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid #f0f0f0;
  border-radius: 8px;
  transition: background 0.16s ease;
}

.trash-card:hover {
  background: #f5f8ff;
}

.trash-document-icon {
  display: flex;
  width: 20px;
  height: 20px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  color: #8c8c8c;
  background: transparent;
  font-size: 16px;
}

.trash-card-main {
  min-width: 0;
  flex: 1;
}

.trash-card-title {
  color: #262626;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
}

.trash-card-meta {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.trash-card-actions {
  display: flex;
  flex: 0 0 auto;
  gap: 4px;
}

.trash-action-icon {
  display: block;
}

.knowledge-toolbar-icon,
.knowledge-outline-icon,
.knowledge-more-icon {
  display: block;
}

.knowledge-menu-icon {
  display: inline-block;
  margin-right: 8px;
  vertical-align: -3px;
}

.danger-button {
  color: #ff4d4f;
}

.trash-empty {
  flex: 1;
  padding: 120px 20px;
  color: #a0a4ab;
  text-align: center;
}

.knowledge-empty {
  position: relative;
  grid-column: 2 / 4;
  display: flex;
  align-items: center;
  justify-content: center;
}

.empty-state {
  transform: translateY(-20px);
  text-align: center;
}

.empty-illustration {
  position: relative;
  width: 82px;
  height: 70px;
  margin: 0 auto 20px;
}

.empty-folder {
  position: absolute;
  left: 1px;
  bottom: 0;
  color: #e5efff;
  font-size: 70px;
}

.empty-document {
  position: absolute;
  right: 8px;
  bottom: 0;
  z-index: 1;
  color: #3157e2;
  background: #fff;
  font-size: 43px;
}

.empty-check {
  position: absolute;
  top: 1px;
  right: 0;
  z-index: 2;
  display: flex;
  width: 19px;
  height: 19px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  color: #fff;
  background: #3157e2;
  font-size: 11px;
}

.empty-state h2 {
  margin: 0;
  color: #2c2e32;
  font-size: 16px;
  font-weight: 650;
}

.empty-state p {
  margin: 8px 0 0;
  color: #9b9fa6;
  font-size: 14px;
}

.modal-select {
  width: 100%;
}

.sidebar-nav-toggle {
  flex: 0 0 auto;
}

.sidebar-nav-toggle-icon {
  display: block;
  width: 20px;
  height: 20px;
  transition: transform 0.18s ease;
}

.sidebar-nav-toggle-icon.is-collapsed {
  transform: rotate(180deg);
}

.inspector-nav-toggle-icon {
  display: block;
  width: 20px;
  height: 20px;
  transform: rotate(180deg);
}

.source-mode-icon {
  width: 16px;
  height: 16px;
  display: block;
}

.source-mode-toggle.is-active,
.source-mode-toggle.is-active:hover {
  color: #3157e2;
  background: #e5efff;
}

.source-mode-toggle:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

/* 目录树三点按钮 */
.directory-node .node-actions {
  display: none;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 4px;
  color: #797e87;
  cursor: pointer;
  font-size: 14px;
  user-select: none;
}

.directory-node:hover .node-actions {
  display: flex;
}

.directory-node:hover .node-count {
  display: none;
}

.directory-node .node-actions:hover {
  color: #3157e2;
  background: #f1f4fa;
}

.directory-actions {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  gap: 8px;
}

.refresh-button {
  width: 28px;
  height: 28px;
  color: #5f6368;
  font-size: 15px;
}

.refresh-button:disabled {
  opacity: 0.5;
}

.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

.root-entry {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  margin: 0 0 8px;
  padding: 8px 12px;
  border: 0;
  border-radius: 8px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 13px;
  text-align: left;
  transition: background 0.16s ease;
}

.root-entry:hover {
  background: #f3f6fb;
}

.root-entry-label {
  flex: 0 0 auto;
  color: #8c8c8c;
}

.root-breadcrumb {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.root-breadcrumb-segment {
  color: #3157e2;
  white-space: nowrap;
}

.root-breadcrumb-segment:last-child {
  overflow: hidden;
  text-overflow: ellipsis;
}

.root-breadcrumb-separator {
  flex: 0 0 auto;
  color: #bfbfbf;
}

.root-modal-description {
  margin: 0 0 12px;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.root-current-path {
  position: relative;
  margin-bottom: 12px;
  padding: 10px 16px 10px 52px;
  border-radius: 8px;
  background: #f3f4f6;
}

.root-current-path-icon {
  position: absolute;
  top: 50%;
  left: 16px;
  display: block;
  transform: translateY(-50%);
}

.root-select-button img {
  display: block;
}

.root-current-path-label {
  margin-bottom: 2px;
  color: #8c8c8c;
  font-size: 12px;
}

/* S-UI-17: 迁移进行中的加载反馈（同时由确认按钮 loading 与弹窗禁用兜底防重复提交） */
.root-migrate-state {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  color: #8c8c8c;
  font-size: 13px;
}

.root-breadcrumb-modal .root-breadcrumb-segment {
  color: #262626;
  font-size: 14px;
}

.root-select-button {
  display: flex;
  width: 100%;
  height: 32px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 0 12px;
  border: 1px solid #3157e2;
  border-radius: 6px;
  color: #3157e2;
  background: #fff;
  cursor: pointer;
  font-size: 14px;
  transition: background 0.16s ease, border-color 0.16s ease;
}

.root-select-button:hover {
  border-color: #3157e2;
  background: #f5f8ff;
}

.root-select-button:disabled {
  cursor: default;
  opacity: 0.55;
}

:global(.knowledge-modal .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border-radius: 24px;
  box-shadow: 0 18px 48px rgba(15, 23, 42, 0.2);
}

:global(.knowledge-modal .ant-modal-header) {
  display: flex;
  min-height: 84px;
  align-items: center;
  margin: 0;
  padding: 0 32px;
}

:global(.knowledge-modal .ant-modal-title) {
  color: #1d1d1f;
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
}

:global(.knowledge-modal .ant-modal-close) {
  top: 26px;
  right: 24px;
}

:global(.knowledge-modal .ant-modal-body) {
  padding: 12px 32px 16px;
}

:global(.knowledge-modal .ant-modal-footer) {
  display: flex;
  min-height: 65px;
  align-items: center;
  justify-content: flex-end;
  margin: 0;
  padding: 16px 32px;
  border-top: 1px solid #f0f0f0;
}

:global(.knowledge-modal .ant-btn) {
  height: 32px;
  border-radius: 6px;
}

:global(.knowledge-rename-modal .ant-modal-body) {
  padding-block: 12px;
}

:global(.knowledge-rename-modal .ant-form-item) {
  margin-bottom: 0;
}

:global(.knowledge-rename-modal .ant-input) {
  height: 40px;
  border-radius: 8px;
}

.delete-folder-options {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 20px;
}

.delete-folder-option {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 14px 16px;
  border: 1px solid #d9d9d9;
  border-radius: 8px;
  color: #262626;
  background: #fff;
  cursor: pointer;
  font-size: 14px;
  text-align: left;
  transition: border-color 0.16s ease, background 0.16s ease;
}

.delete-folder-option:hover {
  border-color: #3157e2;
  background: #f5f6f8;
}

.delete-folder-option:disabled {
  cursor: default;
  opacity: 0.55;
}

.delete-folder-option strong {
  font-weight: 600;
}

.delete-folder-option span {
  color: #8c8c8c;
  font-size: 13px;
  line-height: 1.5;
}

.delete-folder-option.purge:hover {
  border-color: #ff4d4f;
  background: #fff2f0;
}

.delete-folder-modal-footer {
  display: flex;
  justify-content: flex-end;
}

@media (max-width: 1320px) {
  .knowledge-page {
    grid-template-columns: var(--knowledge-sidebar-width, 300px) minmax(420px, 1fr);
  }

  .knowledge-inspector {
    display: none;
  }

  .trash-workspace,
    .knowledge-empty {
    grid-column: 2;
  }
}

@media (max-width: 860px) {
  .knowledge-page {
    grid-template-columns: var(--knowledge-sidebar-width, 270px) minmax(360px, 1fr);
  }

  .knowledge-sidebar {
    padding-inline: 18px;
  }

}

.knowledge-page.is-sidebar-narrow .knowledge-sidebar {
  padding-inline: 8px;
}

/* 折叠状态：移除 sidebar 可见边框，防止 0px 时仍有边框线残留 */
.knowledge-page.is-sidebar-collapsed .knowledge-sidebar {
  border-right-color: transparent;
}
</style>
