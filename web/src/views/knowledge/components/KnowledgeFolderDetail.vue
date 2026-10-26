<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  SearchOutlined,
  SortAscendingOutlined,
  SortDescendingOutlined,
} from '@ant-design/icons-vue'
import type { KnowledgeDocument, KnowledgeFolderNode } from '@/api/knowledge'
import KnowledgeAddMenu from '@/views/knowledge/components/KnowledgeAddMenu.vue'
import KnowledgeBlankAddDropdown from '@/views/knowledge/components/KnowledgeBlankAddDropdown.vue'
import directoryToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import knowledgeDocumentIcon from '@/assets/icons/knowledge-document.svg'
import knowledgeFolderHeadingIcon from '@/assets/icons/knowledge-folder-heading.svg'
import knowledgeFolderTreeIcon from '@/assets/icons/knowledge-folder-tree.svg'
import knowledgeQuickAddIcon from '@/assets/icons/knowledge-quick-add.svg'
import { useAppI18n } from '@/i18n'

// S-UI-14: 文件夹详情列表页（与「回收站」「编辑器」并列的独立主区工作区）
// S-DA-11: 搜索与排序为纯前端行为，不写 localStorage、不调接口
const { t, locale } = useAppI18n()

type BreadcrumbItem = { id: number; name: string }
type SortKey = 'name' | 'updated_at'

/**
 * 名称列的「本地」占位由词条固定渲染；类型上预留远程登录人的名称与 id（S-UI-15 / S-DA-10，
 * 本次不新增数据库列，远程字段仅在此预留结构）。
 */
type DetailOwner = { id: string; name: string }

const props = defineProps<{
  folderId: number
  folderName: string
  breadcrumbs: BreadcrumbItem[]
  childFolders: KnowledgeFolderNode[]
  childDocuments: KnowledgeDocument[]
  loading?: boolean
  folderCreating?: boolean
  /** 父级 sidebar 折叠状态，用于切换按钮图标 */
  sidebarCollapsed?: boolean
}>()

const emit = defineEmits<{
  'open-folder': [folderId: number]
  'open-document': [uuid: string]
  'create-document': [folderId: number]
  'create-folder': [folderId: number]
  'delete-folder': [folderId: number]
  /** 触发父级折叠 / 展开左侧目录栏 */
  'toggle-sidebar': []
  navigate: [folderId: number]
}>()

const keyword = ref('')
const sortKey = ref<SortKey>('updated_at')
const sortAsc = ref(false)

// 模板中的裸标点会被 @intlify/vue-i18n/no-raw-text 判为未国际化文本，故用常量插值渲染分隔符
const crumbSeparator = '/'

type DetailRow =
  | { key: string; kind: 'folder'; folderId: number; name: string; updatedAt: number }
  | { key: string; kind: 'document'; uuid: string; name: string; updatedAt: number }

const rows = computed<DetailRow[]>(() => {
  const folderRows = props.childFolders.map<DetailRow>((folder) => ({
    key: `folder:${folder.id}`,
    kind: 'folder',
    folderId: folder.id,
    name: folder.name,
    updatedAt: folder.updated_at ?? 0,
  }))
  const documentRows = props.childDocuments.map<DetailRow>((document) => ({
    key: `doc:${document.uuid}`,
    kind: 'document',
    uuid: document.uuid,
    name: document.ext ? `${document.title}.${document.ext}` : document.title,
    updatedAt: document.updated_at ?? 0,
  }))
  const text = keyword.value.trim().toLowerCase()
  const matched = text
    ? [...folderRows, ...documentRows].filter((row) => row.name.toLowerCase().includes(text))
    : [...folderRows, ...documentRows]
  const direction = sortAsc.value ? 1 : -1
  return matched.sort((left, right) => {
    if (sortKey.value === 'name') {
      return direction * left.name.localeCompare(right.name, locale.value)
    }
    if (left.updatedAt === right.updatedAt) {
      // Minor-6：主排序平级时才补「文件夹优先」次级键，且不参与升降序方向，避免同组内顺序被反向
      if (left.kind !== right.kind) return left.kind === 'folder' ? -1 : 1
      return left.name.localeCompare(right.name, locale.value)
    }
    return direction * (left.updatedAt - right.updatedAt)
  })
})

const sortDirectionLabel = computed(() =>
  sortAsc.value ? t('knowledge.detailSortDesc') : t('knowledge.detailSortAsc'),
)

// CF-4: 文档表无「最近访问」字段，按既有约定以 updated_at 承载展示
function formatTime(value: number) {
  if (!value) return '—'
  return new Date(value).toLocaleString(locale.value, { hour12: false })
}

function ownerLabel(owner?: DetailOwner | null) {
  return owner?.name || t('knowledge.detailOwnerLocal')
}

function openRow(row: DetailRow) {
  if (row.kind === 'folder') {
    emit('open-folder', row.folderId)
    return
  }
  emit('open-document', row.uuid)
}

function toggleSortDirection() {
  sortAsc.value = !sortAsc.value
}
</script>

<template>
  <main class="folder-detail">
    <!-- S-UI-14: 面包屑「上级文件夹 / 当前文件夹」，点击根节点回到空态。 -->
    <nav class="detail-breadcrumb">
      <div class="detail-breadcrumb-leading">
        <!-- 左侧目录栏折叠 / 展开切换（固定在面包屑最左侧） -->
        <button
          class="icon-button sidebar-nav-toggle"
          type="button"
          :aria-label="(props.sidebarCollapsed ?? false) ? t('knowledge.expandSidebar') : t('knowledge.collapseSidebar')"
          @click="emit('toggle-sidebar')"
        >
          <img
            :src="directoryToggleIcon"
            alt=""
            aria-hidden="true"
            class="sidebar-nav-toggle-icon"
            :class="{ 'is-collapsed': props.sidebarCollapsed ?? false }"
          />
        </button>
        <div class="detail-breadcrumb-path">
          <template v-for="(item, index) in breadcrumbs" :key="item.id">
            <button
              v-if="index < breadcrumbs.length - 1"
              type="button"
              class="crumb crumb-link"
              @click="emit('navigate', item.id)"
            >
              {{ item.name }}
            </button>
            <span v-else class="crumb crumb-current">{{ item.name }}</span>
            <span
              v-if="index < breadcrumbs.length - 1"
              class="crumb-separator"
            >{{ crumbSeparator }}</span>
          </template>
        </div>
      </div>
    </nav>

    <header class="detail-header">
      <div class="detail-title-section">
        <span class="detail-folder-icon">
          <img :src="knowledgeFolderHeadingIcon" alt="" aria-hidden="true" />
        </span>
        <h2 class="detail-title">{{ folderName }}</h2>
      </div>
      <div class="detail-actions">
        <!-- S-UI-14: 右上「快速添加」 -->
        <a-dropdown :trigger="['click']">
          <button class="quick-add-button" type="button">
            <img :src="knowledgeQuickAddIcon" alt="" aria-hidden="true" />
            <span>{{ t('knowledge.quickAdd') }}</span>
          </button>
          <template #overlay>
            <KnowledgeAddMenu
              :folder-creating="folderCreating"
              @create-document="emit('create-document', folderId)"
              @create-folder="emit('create-folder', folderId)"
            />
          </template>
        </a-dropdown>
      </div>
    </header>

    <div class="detail-toolbar">
      <a-input
        v-model:value="keyword"
        class="detail-search"
        size="small"
        allow-clear
        :placeholder="t('knowledge.detailSearchPlaceholder')"
      >
        <template #suffix><SearchOutlined /></template>
      </a-input>
      <!-- S-UI-16: 列表内搜索（按名称过滤）与排序（按名称 / 按更新时间），均为前端本地行为 -->
      <a-select v-model:value="sortKey" class="detail-sort-select" size="small">
        <a-select-option value="updated_at">{{ t('knowledge.sortByUpdatedAt') }}</a-select-option>
        <a-select-option value="name">{{ t('knowledge.sortByName') }}</a-select-option>
      </a-select>
      <a-tooltip :title="sortDirectionLabel">
        <button
          class="icon-button detail-sort-direction"
          type="button"
          :aria-label="sortDirectionLabel"
          @click="toggleSortDirection"
        >
          <SortAscendingOutlined v-if="sortAsc" />
          <SortDescendingOutlined v-else />
        </button>
      </a-tooltip>
    </div>

    <KnowledgeBlankAddDropdown
      :key="folderId"
      class="detail-content"
      :disabled="loading"
      :folder-creating="folderCreating"
      @create-document="emit('create-document', folderId)"
      @create-folder="emit('create-folder', folderId)"
    >
      <a-spin :spinning="Boolean(loading)">
        <table class="detail-table" data-knowledge-add-ignore>
          <thead>
            <tr>
              <th scope="col" class="col-name">
                {{ t('knowledge.detailNameWithCount', { count: rows.length }) }}
              </th>
              <th scope="col" class="col-owner">{{ t('knowledge.detailUpdatedBy') }}</th>
              <th scope="col" class="col-time">{{ t('knowledge.detailUpdatedAt') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.key" class="detail-row">
              <td class="col-name">
                <button type="button" class="detail-row-name" @click="openRow(row)">
                  <img
                    v-if="row.kind === 'folder'"
                    :src="knowledgeFolderTreeIcon"
                    alt=""
                    aria-hidden="true"
                    class="row-folder-icon"
                  />
                  <img
                    v-else
                    :src="knowledgeDocumentIcon"
                    alt=""
                    aria-hidden="true"
                    class="row-document-icon"
                  />
                  <span class="row-title">{{ row.name }}</span>
                </button>
              </td>
              <td class="col-owner">{{ ownerLabel() }}</td>
              <td class="col-time">{{ formatTime(row.updatedAt) }}</td>
            </tr>
          </tbody>
        </table>
        <!-- 边界 E15：空文件夹显示空态，不报错 -->
        <div v-if="!rows.length" class="detail-empty">{{ t('knowledge.detailEmpty') }}</div>
      </a-spin>
    </KnowledgeBlankAddDropdown>
  </main>
</template>

<style scoped>
.folder-detail {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  background: #fff;
}

.detail-breadcrumb {
  display: flex;
  height: 44px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 24px;
  border-bottom: 1px solid #f0f0f0;
  font-size: 14px;
}

.detail-breadcrumb-path {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
  overflow: hidden;
}

/* 面包屑行左侧容器：包含 sidebar 折叠按钮 + 路径导航 */
.detail-breadcrumb-leading {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  overflow: hidden;
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

.crumb {
  padding: 0;
  border: 0;
  color: #8c8c8c;
  background: transparent;
  font-size: 14px;
}

.crumb-link {
  cursor: pointer;
}

.crumb-link:hover {
  color: #3157e2;
}

.crumb-link:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.crumb-current {
  color: #262626;
  font-weight: 600;
}

.crumb-separator {
  color: #bfbfbf;
}

.detail-header {
  display: flex;
  min-width: 0;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 24px;
}

.detail-title-section {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.detail-folder-icon {
  display: flex;
  width: 20px;
  height: 20px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  color: #262626;
  background: transparent;
  font-size: 16px;
}

.detail-folder-icon img,
.row-folder-icon,
.row-document-icon {
  display: block;
}

.detail-title {
  overflow: hidden;
  margin: 0;
  color: #262626;
  font-size: 18px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.quick-add-button {
  display: inline-flex;
  height: 32px;
  align-items: center;
  gap: 6px;
  padding: 0 14px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  color: #595959;
  background: #fff;
  cursor: pointer;
  font-size: 14px;
  transition: color 0.18s ease, border-color 0.18s ease, background 0.18s ease;
}

.quick-add-button:hover {
  border-color: #3157e2;
  color: #3157e2;
  background: #f9fafb;
}

.quick-add-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.quick-add-button img {
  display: block;
}

.icon-button {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 8px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 16px;
  transition: color 0.18s ease, background 0.18s ease;
}

.icon-button:hover {
  color: #3157e2;
  background: #f0f1f3;
}

.icon-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.danger-button:hover {
  color: #fb363f;
  background: #fef2f2;
}

.detail-toolbar {
  display: flex;
  min-height: 40px;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  padding: 5px 24px;
  border-bottom: 1px solid #f0f0f0;
}

.detail-search {
  max-width: 240px;
  padding: 4px 7px;
  font-size: 12px;
}

.detail-search :deep(.ant-input) {
  font-size: 12px;
}

.detail-sort-select {
  width: 148px;
}

.detail-sort-select :deep(.ant-select-selector) {
  height: auto !important;
  padding: 2px 7px !important;
  font-size: 12px;
}

.detail-sort-select :deep(.ant-select-selection-item) {
  font-size: 12px;
  line-height: 20px !important;
}

.detail-sort-direction {
  width: 28px;
  height: 28px;
}

.detail-content {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0 24px 24px;
}

.detail-content :deep(.ant-spin-nested-loading),
.detail-content :deep(.ant-spin-container) {
  display: block;
  width: 100%;
}

.detail-table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
}

.detail-table th {
  height: 49px;
  border-bottom: 1px solid #f3f4f6;
  color: #8c8c8c;
  font-size: 12px;
  font-weight: 400;
  text-align: left;
}

.detail-table th.col-name {
  padding-left: 8px;
}

.detail-table .col-owner,
.detail-table .col-time {
  width: 180px;
}

.detail-count {
  margin-left: 4px;
  color: #bfbfbf;
}

.detail-row {
  height: 49px;
  border-bottom: 1px solid #f3f4f6;
  color: #595959;
  font-size: 14px;
}

.detail-row:hover {
  background: #f5f8ff;
}

.detail-row td {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-row td.col-name {
  padding-left: 8px;
}

.detail-row-name {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 8px;
  padding: 0;
  border: 0;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  text-align: left;
}

.detail-row-name:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.row-folder-icon {
  display: block;
  flex: 0 0 auto;
}

.row-document-icon {
  display: block;
  flex: 0 0 auto;
}

.row-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-row .col-owner {
  color: #8c8c8c;
}

.detail-row .col-time {
  color: #8c8c8c;
  font-variant-numeric: tabular-nums;
}

.detail-empty {
  padding: 64px 12px;
  color: #8c8c8c;
  font-size: 14px;
  text-align: center;
}
</style>
