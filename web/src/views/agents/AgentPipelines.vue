<template>
  <div class="agent-page">
    <header class="page-bar">
      <div class="page-identity">
        <h1>{{ t('agents.title') }}</h1>
        <div class="page-description">{{ t('agents.description') }}</div>
      </div>
      <!-- <a-button
        size="small"
        :loading="syncing"
        @click="syncCloudPipelines(true)"
      >
        <template #icon><CloudDownloadOutlined /></template>
        同步云端流水线
      </a-button> -->
    </header>

    <div
      class="agent-layout"
      :aria-busy="!initialLoadFinished"
    >
	  <template v-if="initialLoadFinished">
        <PipelineListPane
          :pipelines="pipelines"
		  :expert-groups="expertGroups"
		  :selected-uuid="selectedResourceUuid"
          :copying-uuid="copyingUuid"
		  :mode="managementMode"
		  :navigation-disabled="expertBatchSaving"
		  @change-mode="changeManagementMode"
		  @copy="copyResource"
		  @create="createResource"
		  @delete="deleteResource"
		  @edit="editResource"
		  @select="selectResource"
        />
        <PipelineStepsPane
		  v-if="managementMode === 'pipeline'"
          :pipeline="selectedPipeline"
          @create-agent="openAgentModal()"
          @copy-agent="copyModalOpen = true"
          @edit-agent="openAgentModal"
          @move-agent="moveStep"
          @remove-agent="removeStep"
          @updated="pipelineStore.upsertPipeline"
        />
		<ExpertGroupsWorkspace
		  v-else
		  ref="expertWorkspaceRef"
		  v-model:selected-uuid="selectedExpertUuid"
		  @batch-saving-change="expertBatchSaving = $event"
		/>
      </template>
      <span
        v-else
        class="visually-hidden"
        role="status"
      >
        {{ t('agents.loading') }}
      </span>
    </div>

    <PipelineEditorModal
      v-model:open="pipelineModalOpen"
      :pipeline="editingPipeline"
      @created="handlePipelineCreated"
      @updated="handlePipelineUpdated"
    />
    <AgentEditorModal
      v-model:open="agentModalOpen"
      :pipeline-uuid="selectedUuid"
      :is-cloud-pipeline="isCloudPipeline"
      :step="editingStep"
      :current-avatar="editingStepAvatar"
      @saved="refreshSelectedPipeline"
    />
    <CopyAgentModal
      v-model:open="copyModalOpen"
      :target-pipeline-uuid="selectedUuid"
      :source-steps="allReusableSteps"
      multiple
      @saved="refreshSelectedPipeline"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { message, Modal } from 'ant-design-vue'
import { useRoute, useRouter } from 'vue-router'
import apiClient, { ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { usePipelineStore } from '@/stores/pipeline'
import { useExpertGroupStore } from '@/stores/expert-group'
import type { ExpertGroup, Pipeline, PipelineStep } from '@/types/pipeline'
import AgentEditorModal from './components/AgentEditorModal.vue'
import CopyAgentModal from './components/CopyAgentModal.vue'
import PipelineEditorModal from './components/PipelineEditorModal.vue'
import PipelineListPane from './components/PipelineListPane.vue'
import PipelineStepsPane from './components/PipelineStepsPane.vue'
import ExpertGroupsWorkspace from './components/ExpertGroupsWorkspace.vue'
import {
  AGENT_AVATARS,
  type DeleteResponse,
  isPipelineCloud,
  resolveAgentAvatar,
  type ReorderResponse,
  type ReusablePipelineStep,
  sortPipelineSteps,
} from './components/agentPipeline'
import { useAppI18n } from '@/i18n'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const pipelineStore = usePipelineStore()
const { pipelines, loaded } = storeToRefs(pipelineStore)
const expertGroupStore = useExpertGroupStore()
const { items: expertGroups } = storeToRefs(expertGroupStore)
const initialLoadFinished = ref(loaded.value)
const syncing = ref(false)
const selectedUuid = ref('')
const selectedExpertUuid = ref('')
const expertBatchSaving = ref(false)
const expertWorkspaceRef = ref<InstanceType<typeof ExpertGroupsWorkspace>>()
const handledPipelineQuery = ref('')
const pipelineModalOpen = ref(false)
const agentModalOpen = ref(false)
const copyModalOpen = ref(false)
const copyingUuid = ref('')
const editingPipeline = ref<Pipeline>()
const editingStep = ref<PipelineStep>()
const editingStepAvatar = ref<string>(AGENT_AVATARS[0])
const pageActive = ref(true)
type ManagementMode = 'pipeline' | 'expert_group'
const MANAGEMENT_MODE_STORAGE_KEY = 'goteams.agents.management-mode'

function readManagementMode(): ManagementMode {
  try {
    return window.localStorage.getItem(MANAGEMENT_MODE_STORAGE_KEY) === 'expert_group'
      ? 'expert_group'
      : 'pipeline'
  } catch {
    return 'pipeline'
  }
}

const managementMode = ref<ManagementMode>(readManagementMode())
const { t } = useAppI18n()
let hasActivatedOnce = false

const selectedPipeline = computed(() =>
  pipelines.value.find((item) => item.uuid === selectedUuid.value),
)
const selectedResourceUuid = computed(() =>
	managementMode.value === 'expert_group' ? selectedExpertUuid.value : selectedUuid.value,
)
const isCloudPipeline = computed(() => isPipelineCloud(selectedPipeline.value))
const allReusableSteps = computed<ReusablePipelineStep[]>(() =>
  pipelines.value.flatMap((pipeline) =>
    (pipeline.steps || []).map((step) => ({ ...step, pipelineName: pipeline.name })),
  ),
)

function pipelineUuidFromQuery() {
  const value = route.query.pipeline
  return Array.isArray(value) ? value[0] || '' : value || ''
}

function rememberManagementMode(mode: ManagementMode) {
  managementMode.value = mode
  try {
    window.localStorage.setItem(MANAGEMENT_MODE_STORAGE_KEY, mode)
  } catch {
    // 本地存储不可用时，仍保留本次应用会话内的选择。
  }
}

function syncManagementModeFromQuery() {
  if (pipelineUuidFromQuery()) {
    rememberManagementMode('pipeline')
    return
  }
  const value = Array.isArray(route.query.mode) ? route.query.mode[0] : route.query.mode
  if (value === 'expert_group' || value === 'pipeline') rememberManagementMode(value)
}

function isActiveAgentRoute() {
  return pageActive.value && route.name === 'agents'
}

async function load(showError = true) {
  try {
	const [pipelineResult, expertResult] = await Promise.allSettled([
	  pipelineStore.loadPipelines(true),
	  expertGroupStore.load(true),
	])
	if (pipelineResult.status === 'rejected') throw pipelineResult.reason
	if (expertResult.status === 'rejected' && managementMode.value === 'expert_group') {
	  throw expertResult.reason
	}
    const queryPipelineUuid = pipelineUuidFromQuery()
    if (
      queryPipelineUuid &&
      queryPipelineUuid !== handledPipelineQuery.value &&
      pipelines.value.some((item) => item.uuid === queryPipelineUuid)
    ) {
      selectedUuid.value = queryPipelineUuid
      handledPipelineQuery.value = queryPipelineUuid
    } else if (!pipelines.value.some((item) => item.uuid === selectedUuid.value)) {
      selectedUuid.value = pipelines.value[0]?.uuid || ''
    }
    if (selectedUuid.value) await loadPipeline(selectedUuid.value, false)
	if (!expertGroups.value.some((item) => item.uuid === selectedExpertUuid.value)) {
	  selectedExpertUuid.value = expertGroups.value[0]?.uuid || ''
	}
  } catch (error) {
    if (showError) message.error(error instanceof Error ? error.message : t('agents.loadFailed'))
  } finally {
    initialLoadFinished.value = true
  }
}

async function loadPipeline(uuid: string, showError = true) {
  selectedUuid.value = uuid
  try {
    await pipelineStore.loadPipeline(uuid)
  } catch (error) {
    if (showError) message.error(error instanceof Error ? error.message : t('agents.detailLoadFailed'))
  }
}

async function selectPipelineFromQuery() {
  const uuid = pipelineUuidFromQuery()
  if (!uuid) {
    handledPipelineQuery.value = ''
    return
  }
  if (uuid === handledPipelineQuery.value || !pipelines.value.some((item) => item.uuid === uuid))
    return
  handledPipelineQuery.value = uuid
  if (uuid === selectedUuid.value) return
  await loadPipeline(uuid)
}

async function syncCloudPipelines(manual = false) {
  if (!authStore.cloudLoggedIn) return
  if (syncing.value) return
  syncing.value = true
  try {
    const result = await apiClient.post<{ synced?: number }>('/pipelines/sync-cloud', {})
    if (manual)
      message.success(
        result.synced === undefined
          ? t('agents.synced')
          : t('agents.syncedCount', { count: result.synced }),
      )
    await load(false)
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      authStore.clearCloudAuth()
      if (manual) message.warning(t('agents.sessionExpired'))
      return
    }
    message.warning(error instanceof Error ? error.message : t('agents.syncFailed'))
  } finally {
    syncing.value = false
  }
}

// 云端团队专家团同步（需求 1896 优化点2）：慢败静默；登出态直接返回。
async function syncCloudExpertGroups() {
  if (!authStore.cloudLoggedIn) return
  try {
    await apiClient.post<{ synced?: number }>('/expert-groups/sync-cloud', {})
    await expertGroupStore.load(true)
  } catch (error) {
    // 专家团拉取失败不阻塞页面可用性；仅记录。
    console.warn('同步云端专家团失败', error)
  }
}

async function refreshAfterActivation() {
  await load(false)
  if (isActiveAgentRoute()) {
    await syncCloudPipelines(false)
    void syncCloudExpertGroups()
  }
}

function openPipelineModal(pipeline?: Pipeline) {
  if (pipeline && isPipelineCloud(pipeline)) return
  editingPipeline.value = pipeline
  pipelineModalOpen.value = true
}

async function copyPipeline(pipeline: Pipeline) {
  if (copyingUuid.value) return
  copyingUuid.value = pipeline.uuid
  try {
    const copied = await apiClient.post<Pipeline>(
      `/pipelines/${encodeURIComponent(pipeline.uuid)}/copy`,
      {},
    )
    pipelineStore.upsertPipeline(copied)
    selectedUuid.value = copied.uuid
    message.success(t('agents.copied', { name: copied.name }))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.copyFailed'))
  } finally {
    copyingUuid.value = ''
  }
}

function openAgentModal(step?: PipelineStep) {
  editingStep.value = step
  if (step) {
    // 与步骤卡片展示一致：按排序后的位置取模轮换，避免编辑弹窗默认头像与卡片显示不一致
    const steps = sortPipelineSteps(selectedPipeline.value?.steps || [])
    const index = steps.findIndex((item) => item.uuid === step.uuid)
    editingStepAvatar.value = resolveAgentAvatar(step, index)
  } else {
    editingStepAvatar.value = AGENT_AVATARS[0]
  }
  agentModalOpen.value = true
}

async function handlePipelineCreated(pipeline: Pipeline) {
  selectedUuid.value = pipeline.uuid
  await load()
}

async function handlePipelineUpdated() {
  await load()
}

function deletePipeline(pipeline: Pipeline) {
  if (isPipelineCloud(pipeline)) return
  Modal.confirm({
    title: t('agents.deleteTitle', { name: pipeline.name }),
    content: t('agents.deleteDescription'),
    okType: 'danger',
    onOk: async () => {
      try {
        await apiClient.delete<DeleteResponse>(`/pipelines/${pipeline.uuid}`)
        await load()
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('agents.deleteFailed'))
        throw error
      }
    },
  })
}

async function refreshSelectedPipeline() {
  if (selectedUuid.value) await loadPipeline(selectedUuid.value)
}

async function moveStep(step: PipelineStep, offset: number) {
  const pipeline = selectedPipeline.value
  if (!pipeline || isPipelineCloud(pipeline)) return
  const steps = [...(pipeline.steps || [])].sort((a, b) => a.sort_order - b.sort_order)
  const index = steps.findIndex((item) => item.uuid === step.uuid)
  const target = index + offset
  if (target < 0 || target >= steps.length) return

  const [moved] = steps.splice(index, 1)
  steps.splice(target, 0, moved)
  try {
    await apiClient.put<ReorderResponse>(`/pipelines/${selectedUuid.value}/steps/reorder`, {
      ids: steps.map((item) => item.uuid),
    })
    await refreshSelectedPipeline()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.sortFailed'))
  }
}

function removeStep(step: PipelineStep) {
  if (isCloudPipeline.value) return
  Modal.confirm({
    title: t('agents.removeTitle', { name: step.name }),
    okType: 'danger',
    onOk: async () => {
      try {
        await apiClient.delete<DeleteResponse>(
          `/pipelines/${selectedUuid.value}/steps/${step.uuid}`,
        )
        await refreshSelectedPipeline()
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('agents.removeFailed'))
        throw error
      }
    },
  })
}

onMounted(async () => {
  syncManagementModeFromQuery()
  await load()
  await syncCloudPipelines(false)
})

onActivated(() => {
  pageActive.value = true
  syncManagementModeFromQuery()
  // KeepAlive 首次挂载也会触发激活钩子，首轮请求继续只由 onMounted 负责。
  if (!hasActivatedOnce) {
    hasActivatedOnce = true
    return
  }
  void refreshAfterActivation()
})

onDeactivated(() => {
  pageActive.value = false
  handledPipelineQuery.value = ''
  pipelineModalOpen.value = false
  agentModalOpen.value = false
  copyModalOpen.value = false
  editingPipeline.value = undefined
  editingStep.value = undefined
})

// 本地资源保留；账号变化后校准选中项，停用页面由激活钩子刷新。
watch(
  () => authStore.cloudUser?.sessionGeneration || authStore.cloudUser?.accountScope || '',
  () => {
    if (!isActiveAgentRoute()) return
    if (!pipelines.value.some((item) => item.uuid === selectedUuid.value)) {
      selectedUuid.value = pipelines.value[0]?.uuid || ''
    }
    if (!expertGroups.value.some((item) => item.uuid === selectedExpertUuid.value)) {
      selectedExpertUuid.value = expertGroups.value[0]?.uuid || ''
    }
    if (authStore.cloudLoggedIn) {
      void syncCloudPipelines(false)
    } else {
      void load(false)
    }
  },
)

watch(
  () => route.query.pipeline,
  () => {
    if (!isActiveAgentRoute()) return
    void selectPipelineFromQuery()
  },
)

watch(
  () => route.query.mode,
  () => {
    if (!isActiveAgentRoute()) return
    syncManagementModeFromQuery()
  },
)

function isExpertGroup(resource: Pipeline | ExpertGroup): resource is ExpertGroup {
  return 'ready' in resource
}

async function changeManagementMode(mode: ManagementMode) {
  if (expertBatchSaving.value) return
  rememberManagementMode(mode)
  const query = { ...route.query }
  if (mode === 'expert_group') {
    query.mode = 'expert_group'
    delete query.pipeline
  } else {
    delete query.mode
  }
  void router.replace({ query })
	if (mode === 'expert_group') {
	  try {
		await expertGroupStore.load(true)
		if (!expertGroups.value.some((item) => item.uuid === selectedExpertUuid.value)) {
		  selectedExpertUuid.value = expertGroups.value[0]?.uuid || ''
		}
	  } catch (error) {
		message.error(error instanceof Error ? error.message : t('expertGroups.loadFailed'))
	  }
  }
}

function selectResource(uuid: string) {
  if (managementMode.value === 'expert_group') {
    if (expertBatchSaving.value) return
    selectedExpertUuid.value = uuid
    return
  }
  void loadPipeline(uuid)
}

function createResource() {
  if (expertBatchSaving.value) return
  if (managementMode.value === 'expert_group') {
    expertWorkspaceRef.value?.openGroupEditor()
    return
  }
  openPipelineModal()
}

function editResource(resource: Pipeline | ExpertGroup) {
  if (expertBatchSaving.value) return
  if (isExpertGroup(resource)) {
    expertWorkspaceRef.value?.openGroupEditor(resource)
    return
  }
  openPipelineModal(resource)
}

function copyResource(resource: Pipeline | ExpertGroup) {
  if (expertBatchSaving.value) return
  if (isExpertGroup(resource)) {
    void expertWorkspaceRef.value?.handleGroupAction('copy', resource)
    return
  }
  void copyPipeline(resource)
}

function deleteResource(resource: Pipeline | ExpertGroup) {
  if (expertBatchSaving.value) return
  if (isExpertGroup(resource)) {
    void expertWorkspaceRef.value?.handleGroupAction('delete', resource)
    return
  }
  deletePipeline(resource)
}
</script>

<style scoped>
.agent-page {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  position: relative;
  background: #f5f6f8;
  color: #111827;
}

.page-bar {
  display: flex;
  min-height: 44px;
  flex: 0 0 44px;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  border-bottom: 1px solid #f0f0f0;
  background: #fff;
}

.page-identity {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
}

.page-icon {
  display: flex;
  width: 30px;
  height: 30px;
  flex: 0 0 30px;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  color: #fff;
  background: #1677ff;
}

.page-identity > h1 {
  margin: 0;
  color: #111827;
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
}

.page-description {
  color: #98a2b3;
  font-size: 12px;
  font-weight: 400;
}

.page-bar > :deep(.ant-btn) {
  height: 28px;
  padding-inline: 14px;
  border-radius: 14px;
  box-shadow: none;
}

.agent-layout {
  display: flex;
  min-height: 0;
  flex: 1;
  box-sizing: border-box;
  align-items: stretch;
  gap: 0;
  overflow: hidden;
  padding: 0;
  background: #fff;
}

.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  padding: 0;
  border: 0;
  margin: -1px;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
}

@media (max-width: 900px) {
  .agent-layout {
    align-items: stretch;
    overflow-y: auto;
    flex-direction: column;
  }

  .page-description {
    display: none;
  }
}

@media (max-width: 640px) {
  .page-bar {
    padding: 0 16px;
  }

  .agent-layout {
    gap: 12px;
  }
}
</style>
