import { computed, h, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import apiClient from '@/api/client'
import { useAppI18n } from '@/i18n'
import { useConversationStore } from '@/stores/conversation'
import { useWorkItemStore } from '@/stores/work-item'
import { openTerminal } from '@/composables/useDesktop'
import type { TaskWithDetails } from '@/types/task-detail'
import type { MyWorkItem } from '@/types/workitem'

export function normalizeTaskStatus(status?: string) {
  if (['todo', 'pending'].includes(status || '')) return 'pending'
  if (['active', 'running', 'developing', 'developed', 'in_progress'].includes(status || '')) return 'in_progress'
  if (['done', 'completed'].includes(status || '')) return 'done'
  return status || 'pending'
}

export function useTaskWorkspaceControls(
  task: Ref<TaskWithDetails | undefined>,
  callbacks: { refresh: () => void | Promise<void>; deleted: () => void | Promise<void> },
) {
  const { t } = useAppI18n()
  const conversationStore = useConversationStore()
  const workItemStore = useWorkItemStore()
  const changingStatus = ref(false)
  const starting = ref(false)
  const deleting = ref(false)
  const openingTerminal = ref(false)
  const originalLoading = ref(false)
  const originalOpen = ref(false)
  const originalItem = ref<MyWorkItem>()
  const assignModalOpen = ref(false)
  const assignReplaceExisting = ref(false)
  const isCloudWorkItem = computed(() => task.value?.source_type === 'cloud' && Boolean(task.value.work_item_id))
  const linkedWorkItem = computed(() => workItemStore.items.find(item =>
    String(item.id) === String(task.value?.work_item_id) &&
    item.type === task.value?.work_item_type &&
    String(item.workspace_id) === String(task.value?.workspace_id),
  ))
  const hasRequirementUpdate = computed(() => {
    if (!isCloudWorkItem.value || !linkedWorkItem.value) return false
    // 本轮只提示创建快照之后的云端更新，不写回任务正文，也不记录已读版本。
    const updated = Number(linkedWorkItem.value.updated_at || 0)
    const created = Number(task.value?.created_at || 0)
    const milliseconds = (value: number) => value < 1e12 ? value * 1000 : value
    return updated > 0 && created > 0 && milliseconds(updated) > milliseconds(created)
  })
  watch(() => task.value?.uuid, () => {
    originalOpen.value = false
    assignModalOpen.value = false
    originalItem.value = undefined
  })
  watch(() => [task.value?.uuid, task.value?.updated_at, isCloudWorkItem.value], () => {
    if (isCloudWorkItem.value) void workItemStore.load().catch(() => undefined)
  }, { immediate: true })
  let updateTimer: ReturnType<typeof setInterval> | undefined
  function refreshRequirementIndicator() {
    if (!document.hidden && isCloudWorkItem.value) void workItemStore.load().catch(() => undefined)
  }
  onMounted(() => {
    document.addEventListener('visibilitychange', refreshRequirementIndicator)
    updateTimer = setInterval(refreshRequirementIndicator, 60_000)
  })
  onBeforeUnmount(() => {
    if (updateTimer) clearInterval(updateTimer)
    document.removeEventListener('visibilitychange', refreshRequirementIndicator)
  })

  function openExecutionModeModal(replaceExisting = Boolean(task.value?.execution_mode)) {
    assignReplaceExisting.value = replaceExisting
    assignModalOpen.value = true
  }

  async function changeStatus(next: string) {
    const current = task.value
    if (!current || changingStatus.value || next === normalizeTaskStatus(current.status)) return
    if (current.execution_mode === 'vibe_coding') return
    if (next === 'in_progress' && (!current.execution_mode ||
      (['pipeline', 'cli'].includes(current.execution_mode) && !current.steps.length) ||
      (current.execution_mode === 'pipeline' && current.steps.some(step => !step.prompt_snapshot || !step.cli_type || !step.model_name)))) {
      openExecutionModeModal(false)
      return
    }
    changingStatus.value = true
    try {
      await apiClient.put(`/tasks/${encodeURIComponent(current.uuid)}/status`, { status: next })
      conversationStore.setTaskStatus(current.uuid, next)
      if (task.value?.uuid === current.uuid) task.value = { ...task.value, status: next }
      await callbacks.refresh()
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('workflows.board.statusUpdateFailed'))
    } finally { changingStatus.value = false }
  }

  async function startTask() {
    const current = task.value
    if (!current || starting.value) return
    if (!current.execution_mode || (current.execution_mode === 'pipeline' && !current.steps.length)) {
      openExecutionModeModal(false)
      return
    }
    starting.value = true
    try {
      await apiClient.post(`/tasks/${encodeURIComponent(current.uuid)}/start`, {})
      message.success(t('workflows.task.detail.started'))
      await callbacks.refresh()
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('workflows.task.detail.startFailed'))
    } finally { starting.value = false }
  }

  async function openOriginalRequirement() {
    const current = task.value
    if (!current || !isCloudWorkItem.value || originalLoading.value) return
    originalLoading.value = true
    try {
      await workItemStore.load(true)
      if (task.value?.uuid !== current.uuid) return
      originalItem.value = linkedWorkItem.value || {
        type: current.work_item_type === 'defect' ? 'defect' : 'requirement',
        id: Number(current.work_item_id), workspace_id: Number(current.workspace_id || 0),
        title: current.title, description: current.description,
        local_task_uuid: current.uuid, local_task_status: current.status,
      }
      originalOpen.value = true
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('teamwork.workItem.loadFailed'))
    } finally { originalLoading.value = false }
  }

  async function openTaskTerminal() {
    const directory = task.value?.work_dir || task.value?.task_dir
    if (!directory || openingTerminal.value) return
    openingTerminal.value = true
    try { await openTerminal(directory) }
    catch (error) { message.error(error instanceof Error ? error.message : t('workflows.task.progress.openTerminalFailed')) }
    finally { openingTerminal.value = false }
  }

  function deleteTask() {
    const current = task.value
    if (!current || deleting.value) return
    const cloud = isCloudWorkItem.value
    Modal.confirm({
      title: t(cloud ? 'teamwork.delete.title' : 'workflows.task.detail.deleteConfirm'),
      content: cloud ? h('div', [h('p', t('teamwork.delete.item1')), h('p', t('teamwork.delete.item2'))]) : t('workflows.task.detail.deleteWarning'),
      okText: t(cloud ? 'teamwork.delete.confirm' : 'common.actions.delete'), okType: 'danger', cancelText: t('common.actions.cancel'),
      onOk: async () => {
        deleting.value = true
        try {
          await conversationStore.deleteTask(current.uuid)
          message.success(t('workflows.task.detail.deleted'))
          if (task.value?.uuid === current.uuid) await callbacks.deleted()
        } catch (error) {
          message.error(error instanceof Error ? error.message : t('workflows.task.detail.deleteFailed'))
          throw error
        } finally { deleting.value = false }
      },
    })
  }

  return { changingStatus, starting, deleting, openingTerminal, originalLoading, originalOpen, originalItem,
    assignModalOpen, assignReplaceExisting, isCloudWorkItem, hasRequirementUpdate,
    changeStatus, startTask, openOriginalRequirement, openTaskTerminal, deleteTask, openExecutionModeModal }
}
