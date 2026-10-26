import { computed, toValue, type MaybeRefOrGetter, type Ref } from 'vue'
import type { TaskWithDetails } from '@/types/task-detail'
import type { TaskProgress } from '@/types/pipeline'
import { isUserMessage } from '@/components/task-progress/utils'
import { composerDraftKey } from '@/composables/useComposerDraft'
import { useAppI18n } from '@/i18n'

/** 两处任务详情共用同一套越序对话规则，执行进度仍由各页面的手动推进管理。 */
export function usePipelineStepConversation(
  task: Ref<TaskWithDetails | undefined>,
  progress: Ref<TaskProgress[]>,
  selectedStepUuid: Ref<string>,
  taskUuid: MaybeRefOrGetter<string>,
) {
  const { t } = useAppI18n()
  const pipeline = computed(() => task.value?.execution_mode === 'pipeline')
  const draftKey = computed(() => composerDraftKey(toValue(taskUuid), pipeline.value ? selectedStepUuid.value : ''))
  const activeTaskProgress = computed(() => [...progress.value].reverse().find(
    item => !isUserMessage(item) && ['created', 'running'].includes(item.status),
  ))
  const selectedHasSession = computed(() => progress.value.some(
    item => item.task_step_uuid === selectedStepUuid.value && !isUserMessage(item) && Boolean(item.session_uuid),
  ))
  const needsReopen = computed(() => task.value?.status === 'done' && !selectedHasSession.value)
  const canSend = computed(() => Boolean(
    task.value?.uuid === toValue(taskUuid) &&
    task.value.steps.some(step => step.uuid === selectedStepUuid.value) &&
    !activeTaskProgress.value && !needsReopen.value,
  ))
  const firstRun = computed(() => !selectedHasSession.value)
  const notice = computed(() => {
    if (!pipeline.value) return ''
    const active = activeTaskProgress.value
    if (active && active.task_step_uuid !== selectedStepUuid.value) {
      const step = task.value?.steps.find(item => item.uuid === active.task_step_uuid)
      return t('workflows.task.detail.otherStepRunning', { step: step?.name || t('workflows.task.common.currentStep') })
    }
    if (needsReopen.value) return t('workflows.task.detail.reopenForNewStep')
    return firstRun.value ? t('workflows.task.detail.anyStepConversationHint') : ''
  })
  return { draftKey, activeTaskProgress, firstRun, canSend, notice }
}
