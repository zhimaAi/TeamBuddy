<template>
  <a-modal
    :open="open"
    :footer="null"
    :closable="false"
    :body-style="{ padding: 0 }"
    :width="922"
    centered
    :wrap-class-name="editing ? 'task-detail-info-modal task-edit-modal' : 'task-detail-info-modal'"
    @cancel="handleOpenChange(false)"
    @update:open="handleOpenChange"
  >
    <div
      class="task-detail-modal"
      :class="{ 'is-editing': editing }"
    >
      <template v-if="task && editing">
        <div
          class="modal-body edit-modal-body"
          :class="{ 'task-info-collapsed': taskInfoCollapsed }"
        >
          <section class="requirement-pane edit-requirement-pane">
            <div class="edit-title-heading">
              <a-input
                v-model:value="form.title"
                :maxlength="50"
                :bordered="false"
                :placeholder="t('workflows.task.create.titleRequired')"
                class="title-input"
              />
              <div
                v-if="taskInfoCollapsed"
                class="requirement-header-actions"
              >
                <button
                  type="button"
                  class="task-info-toggle"
                  :disabled="saving"
                  :aria-label="t('workflows.task.detail.expandTaskInfo')"
                  :aria-expanded="false"
                  @click="taskInfoCollapsed = false"
                >
                  <img
                    :src="directoryToggleIcon"
                    alt=""
                    aria-hidden="true"
                    class="task-info-toggle-icon"
                  />
                </button>
                <button
                  type="button"
                  class="task-info-close task-info-close--inline"
                  :disabled="saving"
                  :aria-label="t('workflows.task.detail.closeEdit')"
                  @click="handleOpenChange(false)"
                >
                  <img
                    :src="closeIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </button>
              </div>
            </div>
            <div class="description-box">
              <TiptapMarkdownEditor
                v-if="open && editing"
                ref="descriptionEditor"
                :model-value="form.description"
                :placeholder="t('workflows.task.create.description')"
                :disabled="saving"
                :on-upload-images="handleDescriptionImages"
                variant="compact"
                @update:model-value="handleDescriptionChange"
                @ready="normalizeDescriptionForEditor"
              />
            </div>
          </section>

          <aside
            v-if="!taskInfoCollapsed"
            class="task-info-pane edit-task-info-pane"
          >
            <div class="task-info-pane-heading">
              <h3>{{ t('workflows.task.detail.taskInfo') }}</h3>
              <div class="task-info-heading-actions">
                <button
                  type="button"
                  class="task-info-toggle"
                  :disabled="saving"
                  :aria-label="t('workflows.task.detail.collapseTaskInfo')"
                  :aria-expanded="true"
                  @click="taskInfoCollapsed = true"
                >
                  <img
                    :src="directoryToggleIcon"
                    alt=""
                    aria-hidden="true"
                    class="task-info-toggle-icon"
                  />
                </button>
                <button
                  type="button"
                  class="task-info-close task-info-close--inline"
                  :disabled="saving"
                  :aria-label="t('workflows.task.detail.closeEdit')"
                  @click="handleOpenChange(false)"
                >
                  <img
                    :src="closeIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </button>
              </div>
            </div>
            <div class="task-info-list edit-task-info-list">
              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="workDirIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.workDirectory') }}</span>
                  <button
                    type="button"
                    class="chip info-control"
                    :class="{ selected: form.work_dir }"
                    :disabled="!canEditContext"
                    :title="
                      canEditContext ? t('workflows.task.create.mainDirectory') : form.work_dir || t('workflows.task.detail.workDirectory')
                    "
                    @click="chooseMainDirectory"
                  >
                    <span class="chip-label">{{ form.work_dir || t('workflows.task.create.chooseDirectory') }}</span>
                    <b v-if="canEditContext">*</b>
                    <DownOutlined />
                  </button>
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="priorityIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.priority') }}</span>
                  <a-dropdown trigger="click">
                    <button
                      type="button"
                      class="chip info-control"
                      :class="{ selected: form.priority }"
                    >
                      <span
                        v-if="form.priority"
                        class="priority-value"
                        :class="{
                          high: form.priority === '高',
                          medium: form.priority === '中',
                          low: form.priority === '低',
                        }"
                        >{{ normalizePriority(form.priority) }}</span
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
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="statusIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.status') }}</span>
                  <span
                    class="info-value"
                    :class="statusClass"
                    >{{ statusLabel }}</span
                  >
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="projectIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.project') }}</span>
                  <a-dropdown
                    v-if="canEditContext"
                    trigger="click"
                  >
                    <button
                      type="button"
                      class="chip info-control"
                      :class="{ selected: selectedProject || form.project_uuid }"
                    >
                      <span class="chip-label">{{ projectChipLabel }}</span>
                      <DownOutlined />
                    </button>
                    <template #overlay>
                      <a-menu>
                        <a-menu-item
                          key="none"
                          @click="chooseProject('')"
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
                  <button
                    v-else
                    type="button"
                    class="chip info-control selected"
                    disabled
                  >
                    <span class="chip-label">{{ projectChipLabel }}</span>
                    <DownOutlined />
                  </button>
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="pipelineIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ executionLabel }}</span>
                  <a-dropdown
                    v-if="canEditPipeline"
                    trigger="click"
                  >
                    <button
                      type="button"
                      class="chip info-control pipeline-chip"
                      :class="{ selected: selectedPipeline || form.pipeline_uuid }"
                    >
                      <img
                        v-if="selectedPipeline?.avatar"
                        class="chip-avatar"
                        :src="selectedPipeline.avatar"
                        alt=""
                      />
                      <span class="chip-label">{{ pipelineChipLabel }}</span>
                      <DownOutlined />
                    </button>
                    <template #overlay>
                      <a-menu class="pipeline-menu">
                        <a-menu-item
                          key="none"
                          @click="choosePipeline('')"
                          >{{ t('workflows.task.create.noPipeline') }}</a-menu-item
                        >
                        <a-menu-divider />
                        <a-menu-item
                          v-for="pipeline in pipelines"
                          :key="pipeline.uuid"
                          @click="choosePipeline(pipeline.uuid)"
                        >
                          <span class="menu-primary">{{ pipeline.name }}</span>
                          <small>
                            {{ isPipelineCloud(pipeline) ? t('workflows.task.create.teamSync') : t('workflows.task.create.localPipeline') }} ·
                            {{ t('workflows.task.create.steps', { count: pipeline.steps?.length || 0 }) }}
                          </small>
                        </a-menu-item>
                      </a-menu>
                    </template>
                  </a-dropdown>
                  <button
                    v-else
                    type="button"
                    class="chip info-control pipeline-chip selected"
                    disabled
                  >
                    <img
                      v-if="task.pipeline_avatar_snapshot || selectedPipeline?.avatar"
                      class="chip-avatar"
                      :src="selectedPipeline?.avatar || task.pipeline_avatar_snapshot"
                      alt=""
                    />
                    <span class="chip-label">{{ pipelineChipLabel }}</span>
                    <DownOutlined />
                  </button>
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="dateIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.expectedDates') }}</span>
                  <div class="date-range-control">
                    <a-date-picker
                      v-model:value="form.planned_start_date"
                      value-format="YYYY-MM-DD"
                      :placeholder="t('workflows.task.create.expectedStart')"
                      size="small"
                    />
                    <span aria-hidden="true">～</span>
                    <a-date-picker
                      v-model:value="form.planned_end_date"
                      value-format="YYYY-MM-DD"
                      :placeholder="t('workflows.task.create.expectedEnd')"
                      size="small"
                    />
                  </div>
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="relatedProjectIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.relatedProject') }}</span>
                  <a-select
                    v-model:value="form.child_project_uuids"
                    class="info-select"
                    mode="multiple"
                    :max-tag-count="1"
                    :options="childProjectOptions"
                    :placeholder="t('workflows.task.create.choose')"
                    size="small"
                    :disabled="!canEditContext"
                  />
                </div>
              </div>

              <div class="task-info-item edit-task-info-item">
                <span class="info-icon">
                  <img
                    :src="workDirIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </span>
                <div class="info-content">
                  <span class="info-label">{{ t('workflows.task.detail.relatedDirectories') }}</span>
                  <div class="directory-content info-directory-control">
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
                          v-if="canEditContext"
                          type="button"
                          :aria-label="t('workflows.task.detail.removeRelatedDirectory')"
                          @click="form.additional_dirs.splice(index, 1)"
                        >
                          ×
                        </button>
                      </div>
                    </div>
                    <a-button
                      v-if="canEditContext"
                      size="small"
                      class="directory-add"
                      @click="addDirectory"
                    >
                      <PlusOutlined />{{ t('workflows.task.create.addRelatedDirectory') }}
                    </a-button>
                    <span
                      v-else-if="!form.additional_dirs.length"
                      class="directory-empty"
                      >{{ t('workflows.task.detail.notSet') }}</span
                    >
                  </div>
                </div>
              </div>
            </div>
          </aside>
        </div>

        <footer class="modal-footer">
          <a-button
            :disabled="saving"
            @click="cancelEdit"
            >{{ t('common.actions.cancel') }}</a-button
          >
          <a-button
            type="primary"
            :loading="saving"
            @click="save"
            >{{ t('common.actions.save') }}</a-button
          >
        </footer>
      </template>

      <div
        v-else-if="task"
        class="modal-body"
        :class="{ 'task-info-collapsed': taskInfoCollapsed }"
      >
        <section class="requirement-pane">
          <div class="requirement-summary">
            <div class="requirement-title-rule">
              <h3>{{ task.title }}</h3>
              <div
                v-if="taskInfoCollapsed"
                class="requirement-header-actions"
              >
                <button
                  type="button"
                  class="task-info-toggle"
                  :aria-label="t('workflows.task.detail.expandTaskInfo')"
                  :aria-expanded="false"
                  @click="taskInfoCollapsed = false"
                >
                  <img
                    :src="directoryToggleIcon"
                    alt=""
                    aria-hidden="true"
                    class="task-info-toggle-icon"
                  />
                </button>
                <button
                  type="button"
                  class="task-info-close task-info-close--inline"
                  :aria-label="t('workflows.task.detail.closeDetails')"
                  @click="handleOpenChange(false)"
                >
                  <img
                    :src="closeIcon"
                    alt=""
                    aria-hidden="true"
                  />
                </button>
              </div>
            </div>
          </div>
          <div
            v-if="descriptionUsesHtml"
            class="detail-desc-markdown"
            v-html="descriptionContent"
            @click="handleDescriptionClick"
          />
          <MarkdownPreview
            v-else
            class="detail-desc-markdown"
            :content="task.description"
            :task-uuid="task.uuid"
            :empty-text="t('workflows.task.detail.emptyDescription')"
            @click="handleDescriptionClick"
          />
          <button
            type="button"
            class="requirement-edit-button"
            @click="enterEdit"
          >
            <EditOutlined />
            <span>{{ t('workflows.task.detail.edit') }}</span>
          </button>
        </section>

        <aside
          v-if="!taskInfoCollapsed"
          class="task-info-pane"
        >
          <div class="task-info-pane-heading">
            <h3>{{ t('workflows.task.detail.taskInfo') }}</h3>
            <div class="task-info-heading-actions">
              <button
                type="button"
                class="task-info-toggle"
                :aria-label="t('workflows.task.detail.collapseTaskInfo')"
                :aria-expanded="true"
                @click="taskInfoCollapsed = true"
              >
                <img
                  :src="directoryToggleIcon"
                  alt=""
                  aria-hidden="true"
                  class="task-info-toggle-icon"
                />
              </button>
              <button
                type="button"
                class="task-info-close task-info-close--inline"
                :aria-label="t('workflows.task.detail.closeDetails')"
                @click="handleOpenChange(false)"
              >
                <img
                  :src="closeIcon"
                  alt=""
                  aria-hidden="true"
                />
              </button>
            </div>
          </div>
          <div class="task-info-list">
            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="workDirIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.workDirectory') }}</span>
                <span
                  class="info-value"
                  :title="primaryWorkDir"
                  >{{ primaryWorkDir }}</span
                >
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="priorityIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.priority') }}</span>
                <span
                  class="info-value"
                  :class="priorityClass"
                  >{{ priorityLabel || t('workflows.task.detail.notSet') }}</span
                >
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="statusIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.status') }}</span>
                <span
                  class="info-value"
                  :class="statusClass"
                  >{{ statusLabel }}</span
                >
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="projectIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.project') }}</span>
                <span class="info-value info-value-with-avatar">
                  <span
                    class="project-symbol"
                    :style="projectIconStyle(task.project_icon)"
                  >
                    {{ projectIconSymbol(task.project_icon) }}
                  </span>
                  <span :title="task.project_name || t('workflows.task.detail.notSet')">
                    {{ task.project_name || t('workflows.task.detail.notSet') }}
                  </span>
                </span>
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="pipelineIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ executionLabel }}</span>
                <span class="info-value info-value-with-avatar">
                  <img
                    v-if="task.pipeline_avatar_snapshot"
                    :src="task.pipeline_avatar_snapshot"
                    alt=""
                  />
                  <span :title="pipelineDisplayName">{{ pipelineDisplayName }}</span>
                </span>
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="dateIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.expectedDates') }}</span>
                <span
                  class="info-value"
                  :title="plannedDateRange"
                  >{{ plannedDateRange }}</span
                >
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="relatedProjectIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.relatedProject') }}</span>
                <span
                  v-for="project in relatedProjects"
                  :key="project.uuid"
                  class="info-value info-value-with-avatar"
                >
                  <span
                    class="project-symbol"
                    :style="projectIconStyle(project.icon_type || project.icon)"
                  >
                    {{ projectIconSymbol(project.icon_type || project.icon) }}
                  </span>
                  <span :title="project.name">{{ project.name }}</span>
                </span>
                <span
                  v-if="!relatedProjects.length"
                  class="info-value"
                  >{{ t('workflows.task.detail.notSet') }}</span
                >
              </div>
            </div>

            <div class="task-info-item">
              <span class="info-icon">
                <img
                  :src="workDirIcon"
                  alt=""
                  aria-hidden="true"
                />
              </span>
              <div class="info-content">
                <span class="info-label">{{ t('workflows.task.detail.relatedDirectories') }}</span>
                <span
                  v-for="dir in relatedDirectories"
                  :key="dir"
                  class="info-value"
                  :title="dir"
                >
                  {{ dir }}
                </span>
                <span
                  v-if="!relatedDirectories.length"
                  class="info-value"
                  >{{ t('workflows.task.detail.notSet') }}</span
                >
              </div>
            </div>
          </div>
        </aside>
      </div>

      <div
        v-else
        class="modal-empty"
      >
        {{ t('workflows.task.detail.noTaskInfo') }}
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { message } from 'ant-design-vue'
import { DownOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import MarkdownPreview from '@/components/MarkdownPreview.vue'
import TiptapMarkdownEditor from '@/components/TiptapMarkdownEditor.vue'
import { useTaskImageAttachments } from '@/composables/useTaskImageAttachments'
import closeIcon from '@/assets/icons/task-detail-close.svg'
import directoryToggleIcon from '@/assets/icons/common-sidebar-toggle.svg'
import dateIcon from '@/assets/icons/task-detail-date.svg'
import pipelineIcon from '@/assets/icons/task-detail-pipeline.svg'
import priorityIcon from '@/assets/icons/task-detail-priority.svg'
import projectIcon from '@/assets/icons/task-detail-project.svg'
import relatedProjectIcon from '@/assets/icons/task-detail-related-project.svg'
import statusIcon from '@/assets/icons/task-detail-status.svg'
import workDirIcon from '@/assets/icons/task-detail-work-dir.svg'
import { selectDirectory } from '@/composables/useDesktop'
import { vibeToolDisplayLabel, vibeToolLabelKey } from '@/composables/useVibeCoding'
import { projectIconPreset } from '@/constants/project-icons'
import { usePipelineStore } from '@/stores/pipeline'
import type { LocalProject } from '@/types/project'
import type { TaskWithDetails } from '@/types/task-detail'
import { isPipelineCloud } from '@/utils/pipeline'
import { isLegacyRichTextHtml } from '@/utils/richText'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

const props = defineProps<{
  open: boolean
  task?: TaskWithDetails
  status: string
  renderedDescription: string
  initialEditing?: boolean
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  'preview-image': [url: string]
  saved: []
}>()

const pipelineStore = usePipelineStore()
const { pipelines } = storeToRefs(pipelineStore)
const editing = ref(false)
const TASK_INFO_COLLAPSED_STORAGE_KEY = 'goteams.workflows.task-info-collapsed.v1'
const taskInfoCollapsed = ref(readTaskInfoCollapsed())
const saving = ref(false)
let saveVersion = 0
const originalDescription = ref('')
const descriptionTouched = ref(false)
const descriptionEditor = ref<{
  getValue: () => string
  htmlToMarkdown: (value: string) => string
}>()
const { addImages, clear: clearImageAttachments, isSaving: isSavingPastedImages, resolveAttachmentMarkdown, uploadAttachmentsToTask } = useTaskImageAttachments()
const projects = ref<LocalProject[]>([])
const form = reactive({
  title: '',
  description: '',
  pipeline_uuid: '',
  project_uuid: '',
  child_project_uuids: [] as string[],
  work_dir: '',
  additional_dirs: [] as string[],
  priority: '',
  planned_start_date: '',
  planned_end_date: '',
})

const canEditContext = computed(() => props.status === 'pending')
const canEditPipeline = computed(
  () => canEditContext.value && !props.task?.pipeline_snapshot_uuid &&
    (!props.task?.execution_mode || props.task.execution_mode === 'pipeline'),
)
const executionLabel = computed(() => props.task?.execution_mode && props.task.execution_mode !== 'pipeline'
	? t('workflows.task.assign.executionMode')
	: t('workflows.task.detail.pipeline'))
const descriptionUsesHtml = computed(() => /<[a-z][\s\S]*>/i.test(props.task?.description || ''))
const statusLabel = computed(() => {
  if (props.status === 'pending') return t('workflows.task.status.pending')
  if (props.status === 'in_progress') return t('workflows.task.status.inProgress')
  if (props.status === 'blocked') return t('workflows.task.status.blocked')
  return t('workflows.task.status.done')
})
const statusClass = computed(() => `status-${props.status || 'done'}`)
const priorityLabel = computed(() => normalizePriority(props.task?.priority))
const priorityClass = computed(() => {
  const value = editing.value ? form.priority : priorityLabel.value
  if (value === '高') return 'priority-高'
  if (value === '中') return 'priority-中'
  if (value === '低') return 'priority-低'
  return ''
})
const plannedStartDateLabel = computed(() => formatDate(props.task?.planned_start_date))
const plannedEndDateLabel = computed(() => formatDate(props.task?.planned_end_date))
const plannedDateRange = computed(
  () => `${plannedStartDateLabel.value} ～ ${plannedEndDateLabel.value}`,
)
const descriptionContent = computed(() => props.renderedDescription || t('workflows.task.detail.emptyDescription'))
const primaryWorkDir = computed(() =>
  editing.value
    ? form.work_dir || t('workflows.task.detail.notSet')
    : props.task?.work_dir || props.task?.task_dir || t('workflows.task.detail.notSet'),
)
const relatedProjects = computed(() =>
  (props.task?.projects || []).filter((project) => project.uuid !== props.task?.project_uuid),
)
const relatedDirectories = computed(() => {
  const directories = [
    ...(props.task?.work_dirs || []),
    ...relatedProjects.value.map((project) => project.local_dir || project.main_dir || ''),
  ]
  return [
    ...new Set(
      directories.filter((dir) => dir && dir !== (props.task?.work_dir || props.task?.task_dir)),
    ),
  ]
})
const selectedProject = computed(() =>
  projects.value.find((item) => item.uuid === form.project_uuid),
)
const selectedChildren = computed(() =>
  projects.value.filter((item) => form.child_project_uuids.includes(item.uuid)),
)
const selectedPipeline = computed(() =>
  pipelines.value.find((item) => item.uuid === form.pipeline_uuid),
)
const childProjectOptions = computed(() =>
  projects.value
    .filter((item) => item.uuid !== form.project_uuid)
    .map((item) => ({ label: item.name, value: item.uuid })),
)
const pipelineDisplayName = computed(() => {
  if (props.task?.execution_mode === 'cli') return t('workflows.task.common.directExecution')
  if (props.task?.execution_mode === 'vibe_coding') return vibeToolDisplayLabel(props.task.execution_tool || '', t(vibeToolLabelKey(props.task.execution_tool || '')), props.task.execution_tool_name)
	if (props.task?.execution_mode === 'expert_group') return props.task.expert_group_name_snapshot || t('agents.expertTeam')
  if (form.pipeline_uuid && editing.value) {
    return pipelines.value.find((item) => item.uuid === form.pipeline_uuid)?.name || t('workflows.task.detail.unassigned')
  }
  return props.task?.pipeline_name_snapshot || t('workflows.task.detail.unassigned')
})
const projectChipLabel = computed(
  () => selectedProject.value?.name || props.task?.project_name || t('workflows.task.detail.project'),
)
const pipelineChipLabel = computed(
  () => props.task?.execution_mode === 'cli'
    ? pipelineDisplayName.value
    : selectedPipeline.value?.name || props.task?.pipeline_name_snapshot || t('workflows.task.detail.pipeline'),
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

function readTaskInfoCollapsed(): boolean {
  try {
    return window.localStorage.getItem(TASK_INFO_COLLAPSED_STORAGE_KEY) !== 'false'
  } catch {
    return true
  }
}

watch(taskInfoCollapsed, (collapsed) => {
  try {
    window.localStorage.setItem(TASK_INFO_COLLAPSED_STORAGE_KEY, String(collapsed))
  } catch {
    // 本地存储不可用时，仍保留当前弹窗实例内的展开状态。
  }
})

watch(
  () => props.open,
  (open) => {
    if (!open) {
      saveVersion += 1
      editing.value = false
      saving.value = false
      clearImageAttachments()
      return
    }
    syncFormFromTask()
    if (props.initialEditing) void enterEdit()
  },
)

watch(
  () => props.task?.uuid,
  () => {
    saveVersion += 1
    editing.value = false
    saving.value = false
    clearImageAttachments()
    if (props.open) syncFormFromTask()
  },
)

onBeforeUnmount(() => {
  saveVersion += 1
  clearImageAttachments()
})

function normalizePriority(value?: string) {
  if (value === 'high' || value === '高') return t('workflows.task.priority.high')
  if (value === 'medium' || value === '中') return t('workflows.task.priority.medium')
  if (value === 'low' || value === '低') return t('workflows.task.priority.low')
  return ''
}

function formatDateValue(value?: string | number) {
  if (!value) return ''
  if (typeof value === 'string' && !/^\d+$/.test(value)) return value
  const timestamp = Number(value)
  const date = new Date(timestamp < 1e12 ? timestamp * 1000 : timestamp)
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function projectIconStyle(type?: string) {
  const icon = projectIconPreset(type)
  return { color: icon.color, background: icon.background }
}

function projectIconSymbol(type?: string) {
  return projectIconPreset(type).symbol
}

function formatDate(value?: string | number) {
  return formatDateValue(value) || t('workflows.task.detail.notSet')
}

function handleOpenChange(open: boolean) {
  if (!open && saving.value) return
  emit('update:open', open)
}

function handleDescriptionClick(event: MouseEvent) {
  const target = event.target
  if (target instanceof HTMLImageElement) emit('preview-image', target.src)
}

function syncFormFromTask() {
  const task = props.task
  form.title = task?.title || ''
  form.description = task?.description || ''
  originalDescription.value = form.description
  descriptionTouched.value = false
  form.pipeline_uuid = task?.selected_pipeline_uuid || ''
  form.project_uuid = task?.project_uuid || ''
  form.child_project_uuids = (task?.projects || [])
    .filter((project) => project.uuid !== task?.project_uuid)
    .map((project) => project.uuid)
  form.work_dir = task?.work_dir || ''
  const related = new Set(form.child_project_uuids)
  const childDirs = new Set(
    (task?.projects || [])
      .filter((project) => related.has(project.uuid))
      .map((project) => (project.local_dir || project.main_dir || '').trim())
      .filter(Boolean),
  )
  form.additional_dirs = (task?.work_dirs || []).filter(
    (dir) => dir && dir !== form.work_dir && !childDirs.has(dir),
  )
  form.priority = normalizePriority(task?.priority)
  form.planned_start_date = formatDateValue(task?.planned_start_date)
  form.planned_end_date = formatDateValue(task?.planned_end_date)
}

function normalizeDescriptionForEditor() {
  if (!isLegacyRichTextHtml(originalDescription.value)) return
  const markdown = descriptionEditor.value?.htmlToMarkdown(form.description)
  if (markdown !== undefined) form.description = markdown
}

function handleDescriptionChange(value: string) {
  form.description = value
  descriptionTouched.value = true
}

async function enterEdit() {
  clearImageAttachments()
  syncFormFromTask()
  editing.value = true
  try {
    const [pipelineResult, projectResult] = await Promise.allSettled([
      pipelineStore.loadPipelines(true),
      apiClient.get<{ items: LocalProject[] }>('/projects'),
    ])
    projects.value = projectResult.status === 'fulfilled' ? projectResult.value.items || [] : []
    if (pipelineResult.status === 'rejected') message.warning(t('workflows.task.detail.pipelineLoadRetry'))
    if (projectResult.status === 'rejected') message.warning(t('workflows.task.create.projectLoadFailed'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.detail.optionsLoadFailed'))
  }
}

function cancelEdit() {
  editing.value = false
  syncFormFromTask()
}

function chooseProject(uuid: string) {
  form.project_uuid = uuid
  form.child_project_uuids = form.child_project_uuids.filter((item) => item !== form.project_uuid)
  const project = projects.value.find((item) => item.uuid === form.project_uuid)
  if (project) form.work_dir = project.local_dir
}

function choosePipeline(uuid: string) {
  form.pipeline_uuid = uuid
}

async function chooseMainDirectory() {
  if (!canEditContext.value) return
  try {
    const selected = await selectDirectory(form.work_dir)
    if (selected) {
      form.work_dir = selected
      form.project_uuid = ''
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('workflows.task.create.directoryPickerFailed'))
  }
}

async function addDirectory() {
  if (!canEditContext.value) return
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
}

async function save() {
  if (!props.task || saving.value) return
  if (!form.title.trim()) return message.warning(t('workflows.task.create.titleRequired'))
  if (canEditContext.value && !form.work_dir.trim())
    return message.warning(t('workflows.task.create.directoryRequired'))
  // 附件上传期间任务或弹窗可能被父页面切换，固定写入目标并丢弃过期的界面回调。
  const taskUuid = props.task.uuid
  const requestVersion = ++saveVersion
  const isCurrentSave = () => requestVersion === saveVersion && props.open && props.task?.uuid === taskUuid
  const description = descriptionTouched.value
    ? descriptionEditor.value?.getValue() ?? form.description
    : originalDescription.value
  const payload: Record<string, unknown> = {
    title: form.title.trim(),
    priority: form.priority || undefined,
    planned_start_date: form.planned_start_date || undefined,
    planned_end_date: form.planned_end_date || undefined,
  }
  if (canEditContext.value) {
    payload.project_uuid = form.project_uuid || undefined
    payload.child_project_uuids = [...form.child_project_uuids]
    payload.work_dir = form.work_dir
    payload.work_dirs = [...allWorkDirs.value]
    if (canEditPipeline.value) payload.pipeline_uuid = form.pipeline_uuid || undefined
  }
  saving.value = true
  try {
    await uploadAttachmentsToTask(taskUuid)
    if (!isCurrentSave()) return
    const content = resolveAttachmentMarkdown(description).content
    payload.content = content
    payload.description = content
    await apiClient.put(`/tasks/${encodeURIComponent(taskUuid)}`, payload)
    if (!isCurrentSave()) return
    form.description = content
    originalDescription.value = content
    descriptionTouched.value = false
    message.success(t('workflows.task.detail.updated'))
    editing.value = false
    emit('saved')
  } catch (error) {
    if (isCurrentSave()) message.error(error instanceof Error ? error.message : t('workflows.task.detail.updateFailed'))
  } finally {
    if (isCurrentSave()) saving.value = false
  }
}

async function handleDescriptionImages(files: File[]) {
  if (isSavingPastedImages.value) throw new Error(t('workflows.task.create.savingImages'))
  return addImages(files).map((attachment) => attachment.marker)
}
</script>

<style scoped>
.task-detail-modal {
  overflow: hidden;
  border-radius: 20px;
  color: #262626;
  background: #fff;
  font-family: 'PingFang SC', 'Microsoft YaHei', sans-serif;
}

.task-detail-modal.is-editing {
  overflow: visible;
}

.modal-header {
  display: flex;
  min-height: 60px;
  box-sizing: border-box;
  align-items: center;
  justify-content: space-between;
  padding: 18px 24px 14px;
  border-bottom: 1px solid #f0f0f0;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.header-text-button {
  height: 24px;
  padding: 0;
  border: 0;
  color: #3157e2;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
}

.header-close {
  display: flex;
  width: 16px;
  height: 16px;
  padding: 0;
  border: 0;
  align-items: center;
  justify-content: center;
  background: transparent;
  cursor: pointer;
}

.header-close img {
  width: 100%;
  height: 100%;
}

.modal-header h2,
.task-info-pane h3 {
  margin: 0;
  color: #262626;
  font-size: 18px;
  font-weight: 600;
  line-height: 28px;
}

.modal-body {
  display: grid;
  height: min(644px, calc(100vh - 88px));
  min-height: 420px;
  grid-template-columns: minmax(0, 600px) minmax(0, 322px);
}

.requirement-pane {
  min-width: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 24px;
  border-right: 1px solid #f0f0f0;
}

.requirement-summary {
  margin-bottom: 16px;
}

.requirement-summary h3,
.task-primary .title-input {
  margin: 0 0 12px;
  color: #262626;
  font-size: 22px;
  font-weight: 600;
  line-height: 32px;
  overflow-wrap: anywhere;
}

.task-primary {
  padding: 20px 24px 0;
}

/* antd borderless 的 border:none 选择器特异性更高，下边框需 !important 才能稳定生效（含聚焦态） */
.task-primary .title-input {
  height: 56px;
  margin: 0;
  padding: 0;
  border: 0;
  border-bottom: 1px solid #f0f0f0 !important;
  border-radius: 0;
  font-weight: 500;
  line-height: 36px;
}

.description-box {
  display: flex;
  height: 272px;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
}

.description-input {
  height: 272px;
  min-height: 0;
  flex: 1;
  border: 0;
  border-radius: 0;
  color: #262626;
  font-size: 14px;
  line-height: 26px;
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
  min-height: 56px;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 10px 24px 14px;
}

.chip-row.has-expanded {
  min-height: 60px;
  padding-bottom: 18px;
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

.chip-icon {
  flex: 0 0 auto;
  color: #bfbfbf;
  font-size: 16px;
}

.chip :deep(.anticon-down) {
  display: none;
}

.chip.selected {
  border-color: #d9d9d9;
  color: #262626;
  background: #fff;
}

.chip:disabled {
  cursor: not-allowed;
  opacity: 1;
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

.more-chip {
  width: 32px;
  margin-left: auto;
  justify-content: center;
  padding: 0;
  border-radius: 50%;
  color: #262626;
}

.more-menu {
  position: absolute;
  z-index: 30;
  top: 48px;
  right: 24px;
  width: 200px;
  padding: 5px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.14);
}

.more-menu button {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 0;
  border-radius: 6px;
  color: #4b5563;
  background: transparent;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
}

.more-menu button:hover:not(:disabled) {
  background: #f8fafc;
}

.more-menu button:disabled {
  color: #bfbfbf;
  cursor: not-allowed;
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
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #8c8c8c;
  background: transparent;
  cursor: pointer;
}

.expanded-close:hover {
  color: #4b5563;
  background: #f5f6f8;
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
  flex: 0 0 auto;
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

.requirement-meta {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.meta-tag {
  display: inline-flex;
  height: 22px;
  box-sizing: border-box;
  flex: 0 0 auto;
  align-items: center;
  padding: 0 9px;
  border: 1px solid currentColor;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 400;
  line-height: 20px;
}

.status-in_progress {
  color: #fa8c16;
}

.status-pending {
  color: #7a8699;
}

.status-blocked,
.priority-高 {
  color: #fb363f;
}

.status-done {
  color: #52c41a;
}

.priority-中 {
  color: #fa8c16;
}

.priority-低 {
  color: #52c41a;
}

.meta-divider {
  width: 1px;
  height: 12px;
  flex: 0 0 auto;
  border-radius: 1px;
  background: #f0f0f0;
}

.detail-desc-markdown {
  min-height: 0;
  padding: 0;
  color: #262626;
  background: transparent;
  font-size: 14px;
  font-weight: 400;
  line-height: 28px;
  overflow-wrap: anywhere;
}

.detail-desc-markdown :deep(p),
.detail-desc-markdown :deep(ul),
.detail-desc-markdown :deep(ol),
.detail-desc-markdown :deep(blockquote),
.detail-desc-markdown :deep(pre) {
  margin: 0;
}

.detail-desc-markdown :deep(ul),
.detail-desc-markdown :deep(ol) {
  padding-left: 21px;
}

.detail-desc-markdown :deep(h1),
.detail-desc-markdown :deep(h2),
.detail-desc-markdown :deep(h3),
.detail-desc-markdown :deep(h4),
.detail-desc-markdown :deep(h5),
.detail-desc-markdown :deep(h6) {
  margin: 12px 0 4px;
  color: inherit;
  font-size: inherit;
  font-weight: 600;
  line-height: inherit;
}

.detail-desc-markdown :deep(pre),
.detail-desc-markdown :deep(table) {
  max-width: 100%;
  overflow: auto;
}

.detail-desc-markdown :deep(img) {
  max-width: 100%;
  max-height: 320px;
  object-fit: contain;
  border: 1px solid #f0f0f0;
  border-radius: 4px;
  cursor: pointer;
  vertical-align: middle;
}

.detail-desc-markdown :deep(img):hover {
  border-color: #3157e2;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.15);
}

.task-info-pane {
  min-width: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 24px;
}

.task-info-pane h3 {
  margin-bottom: 24px;
}

.task-info-list {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.task-info-item {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 8px;
}

.info-icon {
  display: flex;
  width: 26px;
  height: 26px;
  flex: 0 0 26px;
  align-items: center;
  justify-content: center;
  border-radius: 7px;
  background: #f2f4f7;
}

.info-icon img {
  width: 16px;
  height: 16px;
}

.info-content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 4px;
}

.info-label {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.info-value {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: #242933;
  font-size: 14px;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.info-value-with-avatar {
  display: flex;
  align-items: center;
  gap: 8px;
}

.info-value-with-avatar > img {
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
  border-radius: 50%;
  object-fit: cover;
}

.project-symbol {
  display: flex;
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
  align-items: center;
  justify-content: center;
  border-radius: 5px;
  font-size: 9px;
  font-weight: 700;
  line-height: 20px;
}

.info-value-with-avatar > span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modal-footer {
  display: flex;
  height: 64px;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 0 24px;
}

.modal-empty {
  display: flex;
  height: 360px;
  align-items: center;
  justify-content: center;
  color: #8c8c8c;
  font-size: 14px;
}

.menu-primary {
  display: block;
  color: #374151;
}

:global(.task-detail-info-modal .ant-modal) {
  max-width: calc(100vw - 32px);
  padding-bottom: 0;
}

:global(.task-detail-info-modal .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border-radius: 16px;
}

:global(.task-edit-modal .ant-modal-content) {
  overflow: visible;
}

:global(.task-detail-info-modal .ant-modal-body) {
  padding: 0;
}

:global(.task-edit-modal .ant-dropdown-menu-item) {
  min-width: 220px;
}

:global(.task-edit-modal .ant-dropdown-menu-item small) {
  display: block;
  max-width: 270px;
  overflow: hidden;
  color: #9ca3af;
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Figma node 2870:9784 — 922px two-column requirement detail dialog. */
.task-detail-modal,
.task-detail-modal.is-editing {
  overflow: hidden;
  border-radius: 16px;
}

.modal-body {
  height: 716px;
  min-height: 0;
  grid-template-columns: 600px 322px;
}

.modal-body.task-info-collapsed {
  grid-template-columns: minmax(0, 1fr);
}

.modal-body.task-info-collapsed .requirement-pane {
  border-right: 0;
}

.requirement-pane {
  position: relative;
  box-sizing: border-box;
  padding: 24px;
}

.requirement-title-rule {
  display: flex;
  width: 100%;
  align-items: flex-start;
  gap: 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid #3157e2;
}

.requirement-title-rule > h3 {
  min-width: 0;
  flex: 1;
}

.requirement-header-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.requirement-summary {
  margin-bottom: 16px;
}

.requirement-summary h3 {
  margin: 0;
  color: #262626;
  font-size: 22px;
  font-weight: 600;
  line-height: 32px;
}

.requirement-meta {
  margin-top: 12px;
}

.detail-desc-markdown {
  color: #262626;
  font-size: 14px;
  line-height: 28px;
}

.requirement-edit-button {
  position: absolute;
  z-index: 2;
  top: 88px;
  right: 22px;
  display: inline-flex;
  height: 32px;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  border: 1px solid #e6eaf1;
  border-radius: 9px;
  color: #262626;
  background: rgba(255, 255, 255, 0.96);
  box-shadow: 0 5px 16px rgba(23, 35, 60, 0.07);
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  letter-spacing: -0.15px;
  line-height: 22px;
}

.requirement-edit-button :deep(.anticon) {
  font-size: 14px;
}

.task-info-pane {
  position: relative;
  box-sizing: border-box;
  padding: 24px;
}

.task-info-pane h3 {
  margin: 0 0 16px;
  color: #262626;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
}

.task-info-pane-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.task-info-pane-heading h3 {
  margin: 0;
}

.task-info-heading-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.task-info-toggle {
  display: inline-flex;
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  align-items: center;
  justify-content: center;
  margin: 0;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #5f6368;
  background: transparent;
  cursor: pointer;
  transition: background 0.16s ease, color 0.16s ease;
}

.task-info-toggle:hover {
  color: #3157e2;
  background: #f0f1f3;
}

.task-info-toggle:disabled,
.task-info-close:disabled {
  cursor: default;
  opacity: 0.45;
}

.task-info-toggle:focus-visible {
  color: #3157e2;
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.task-info-toggle-icon {
  display: block;
  width: 20px;
  height: 20px;
  transform: rotate(180deg);
}

.task-info-close {
  position: absolute;
  top: 12px;
  right: 12px;
  display: inline-flex;
  width: 16px;
  height: 16px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
}

.task-info-close img {
  width: 100%;
  height: 100%;
}

.task-info-close--inline {
  position: static;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  padding: 6px;
}

.task-info-close:hover,
.task-info-close:focus-visible {
  background: #f3f4f6;
}

.task-info-close:focus-visible {
  outline: 2px solid rgba(49, 87, 226, 0.3);
  outline-offset: 2px;
}

.task-info-list {
  gap: 16px;
}

.task-info-item {
  gap: 8px;
}

.info-content {
  gap: 4px;
}

.info-label {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.info-value {
  display: flex;
  width: 100%;
  height: 32px;
  box-sizing: border-box;
  align-items: center;
  padding: 4px 12px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  color: #262626;
  background: #fff;
  font-size: 14px;
  line-height: 22px;
}

.info-value-with-avatar {
  gap: 8px;
}

.info-value-with-avatar > span:last-child {
  min-width: 0;
  flex: 1;
}

.info-icon :deep(.anticon) {
  color: #7a8699;
  font-size: 16px;
}

.edit-modal-body {
  overflow: hidden;
}

.edit-requirement-pane {
  display: flex;
  overflow: hidden;
  flex-direction: column;
}

.edit-title-heading {
  display: flex;
  align-items: center;
  gap: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid #3157e2;
}

.edit-title-heading .title-input {
  min-width: 0;
  flex: 1;
  height: 32px;
  margin: 0;
  padding: 0;
  border: 0 !important;
  border-radius: 0;
  color: #262626;
  font-size: 22px;
  font-weight: 600;
  line-height: 32px;
}

.edit-requirement-pane :deep(.ant-input),
.edit-requirement-pane :deep(textarea) {
  padding-right: 0 !important;
  padding-left: 0 !important;
}

.edit-requirement-pane .description-box {
  height: auto;
  min-height: 0;
  flex: 1;
  margin-top: 16px;
}

.edit-requirement-pane .description-input {
  height: 100% !important;
  min-height: 0;
  padding: 0 4px 0 0 !important;
  color: #262626;
  font-size: 14px;
  line-height: 28px;
}

.edit-task-info-pane {
  overflow-x: hidden;
  overflow-y: auto;
}

.edit-info-list {
  display: flex;
  min-height: 0;
  flex-direction: column;
  align-items: stretch;
  gap: 16px;
  padding: 0;
}

.edit-info-list.has-expanded {
  min-height: 0;
  padding-bottom: 0;
}

.edit-info-field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.edit-info-label {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.edit-info-field .chip {
  width: 100%;
  max-width: none;
  height: 32px;
  box-sizing: border-box;
  gap: 8px;
  padding: 4px 12px;
  border-color: #d9d9d9;
  border-radius: 6px;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
}

.edit-info-field .chip-icon {
  color: #7a8699;
}

.edit-info-field .chip :deep(.anticon-down) {
  display: inline-flex;
  margin-left: auto;
  color: #7a8699;
  font-size: 12px;
}

.edit-info-field .chip-label {
  flex: 1;
  color: inherit;
  text-align: left;
}

.edit-info-field .priority-value {
  height: 22px;
  padding: 0 8px;
}

.edit-extra-fields {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 16px 0 0;
}

.edit-extra-fields .expanded-row {
  position: relative;
  display: flex;
  width: 100%;
  height: auto;
  min-height: 0;
  box-sizing: border-box;
  flex-direction: column;
  align-items: stretch;
  gap: 4px;
  padding: 0;
  border-radius: 0;
  background: transparent;
}

.edit-extra-fields .expanded-row > span:first-child {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.edit-extra-fields .expanded-row :deep(.ant-picker) {
  width: 100%;
  min-width: 0;
  height: 32px;
  box-sizing: border-box;
  border-color: #d9d9d9;
  border-radius: 6px;
}

.edit-extra-fields .children-row :deep(.ant-select) {
  width: 100%;
  min-width: 0;
}

.edit-extra-fields .children-row :deep(.ant-select-selector) {
  min-height: 32px !important;
  border-color: #d9d9d9 !important;
  border-radius: 6px !important;
}

.edit-extra-fields .expanded-close {
  position: absolute;
  z-index: 1;
  right: 4px;
  bottom: 4px;
  width: 24px;
  height: 24px;
  color: #8c8c8c;
  background: #fff;
}

.edit-extra-fields .directory-content {
  width: 100%;
  box-sizing: border-box;
  padding: 4px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  background: #fff;
}

.edit-extra-fields .directory-item {
  padding: 4px 8px;
  background: #f2f4f7;
}

.edit-extra-fields .directory-add {
  margin-top: 2px;
  border-color: #d9d9d9;
  border-radius: 6px;
}

.edit-task-info-list {
  gap: 16px;
}

.edit-task-info-item .info-control {
  width: 100%;
  max-width: none;
  height: 32px;
  box-sizing: border-box;
  gap: 8px;
  padding: 4px 12px;
  border-color: #d9d9d9;
  border-radius: 6px;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
}

.edit-task-info-item .info-control .chip-label {
  flex: 1;
  color: inherit;
  text-align: left;
}

.edit-task-info-item .info-control :deep(.anticon-down) {
  display: inline-flex;
  margin-left: auto;
  color: #7a8699;
  font-size: 12px;
}

.edit-task-info-item .priority-value {
  height: 22px;
  padding: 0;
  border: 0;
  border-radius: 0;
  font-size: 14px;
  line-height: 22px;
}

.info-value.priority-高 {
  color: #fb363f;
}

.info-value.priority-中 {
  color: #fa8c16;
}

.info-value.priority-低 {
  color: #3157e2;
}

.date-range-control {
  display: flex;
  width: 100%;
  height: 32px;
  box-sizing: border-box;
  align-items: center;
  gap: 4px;
  padding: 0 8px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  background: #fff;
}

.date-range-control > span {
  flex: 0 0 auto;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.date-range-control :deep(.ant-picker) {
  min-width: 0;
  height: 30px;
  flex: 1;
  padding: 0;
  border: 0;
  border-radius: 0;
  background: transparent;
  box-shadow: none;
}

.date-range-control :deep(.ant-picker-input > input) {
  min-width: 0;
  color: #262626;
  font-size: 12px;
}

.edit-task-info-item :deep(.info-select) {
  width: 100%;
}

.edit-task-info-item :deep(.info-select .ant-select-selector) {
  min-height: 32px !important;
  border-color: #d9d9d9 !important;
  border-radius: 6px !important;
  background: #fff !important;
}

.info-directory-control {
  width: 100%;
  box-sizing: border-box;
  padding: 4px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  background: #fff;
}

.info-directory-control .directory-item {
  padding: 4px 8px;
  background: #f2f4f7;
}

.info-directory-control .directory-add {
  margin-top: 2px;
  border-color: #d9d9d9;
  border-radius: 6px;
}

.directory-empty {
  display: block;
  padding: 0 8px;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.modal-footer {
  height: 52px;
  box-sizing: border-box;
  padding: 0 24px;
  border-top: 1px solid #d9d9d9;
}

.modal-footer :deep(.ant-btn) {
  height: 32px;
  padding: 5px 16px;
  border-color: #d9d9d9;
  border-radius: 6px;
  color: #595959;
  font-size: 14px;
  line-height: 20px;
}

.modal-footer :deep(.ant-btn-primary) {
  border-color: #3157e2;
  color: #fff;
  background: #3157e2;
}

:global(.task-edit-modal .ant-modal-content) {
  overflow: hidden;
}


@media (min-width: 761px) and (max-width: 954px) {
  .modal-body {
    grid-template-columns: minmax(0, 1fr) 322px;
  }
}

@media (max-width: 760px) {
  .modal-body {
    display: block;
    height: calc(100vh - 96px);
    min-height: 0;
    overflow-y: auto;
  }

  .requirement-pane,
  .task-info-pane {
    overflow: visible;
  }

  .requirement-pane {
    border-right: 0;
  }

  .task-info-pane {
    border-top: 1px solid #f0f0f0;
  }

  .requirement-meta {
    flex-wrap: wrap;
  }

  .description-box,
  .description-input {
    height: 180px;
  }

  .edit-requirement-pane {
    min-height: 252px;
  }

  .requirement-edit-button {
    right: 18px;
  }

  .expanded-fields {
    grid-template-columns: 1fr;
  }
}
</style>
