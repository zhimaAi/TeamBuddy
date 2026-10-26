import cliExecutionLogo from '@/assets/icons/task-composer-cli.svg'
import { useCliNames } from '@/composables/useCliNames'
import { vibeToolDisplayLabel, vibeToolLabelKey, vibeToolLogo } from '@/composables/useVibeCoding'
import { useAppI18n } from '@/i18n'
import type { TaskBoardTask } from '@/types/task-board'

export function useTaskExecutionDisplay() {
  const { t } = useAppI18n()
  const { cliDisplayName, ensureLoaded } = useCliNames()
  void ensureLoaded()

  function executionName(task: TaskBoardTask) {
    if (task.execution_mode === 'vibe_coding') {
      const label = vibeToolDisplayLabel(task.execution_tool || '', t(vibeToolLabelKey(task.execution_tool || '')), task.execution_tool_name)
      return task.execution_model ? `${label} · ${task.execution_model}` : label
    }
    if (task.execution_mode === 'expert_group') return task.expert_group_name_snapshot || ''
    if (task.execution_mode === 'cli') return cliDisplayName(task.execution_tool || '')
    return task.pipeline_name_snapshot || task.agent_name_snapshot || ''
  }

  function executionAvatar(task: TaskBoardTask) {
    if (task.execution_mode === 'vibe_coding') return vibeToolLogo(task.execution_tool || '')
    if (task.execution_mode === 'cli') return cliExecutionLogo
    if (task.execution_mode === 'expert_group') return task.expert_group_avatar_snapshot || ''
    return task.pipeline_avatar_snapshot || ''
  }

  return { executionName, executionAvatar }
}
