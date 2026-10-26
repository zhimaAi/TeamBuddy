import type { TaskExecutionMode } from '@/types/pipeline'

export interface CreateTaskDefaults {
  project_uuid: string
  work_dir: string
  pipeline_uuid: string
  priority: string
  execution_mode: TaskExecutionMode
  execution_tool: string
  model_name: string
  expert_group_uuid: string
  /** 是否同步 Agent 产出（团队任务配置里的勾选项）；默认开启并记住上次选择 */
  sync_agent_output: boolean
}

const STORAGE_KEY = 'goteams.createTask.lastDefaults'

function emptyDefaults(): CreateTaskDefaults {
  return {
    project_uuid: '',
    work_dir: '',
    pipeline_uuid: '',
    priority: '',
    execution_mode: '',
    execution_tool: '',
    model_name: '',
    expert_group_uuid: '',
    sync_agent_output: true,
  }
}

function parseExecutionMode(value: unknown): TaskExecutionMode {
  if (
    value === 'pipeline' ||
    value === 'expert_group' ||
    value === 'vibe_coding' ||
    value === 'cli'
  ) {
    return value
  }
  return ''
}

export function useCreateTaskDefaults() {
  function getCreateTaskDefaults(): CreateTaskDefaults {
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (!raw) return emptyDefaults()
      const parsed = JSON.parse(raw) as Partial<CreateTaskDefaults>
      return {
        project_uuid: typeof parsed.project_uuid === 'string' ? parsed.project_uuid : '',
        work_dir: typeof parsed.work_dir === 'string' ? parsed.work_dir : '',
        pipeline_uuid: typeof parsed.pipeline_uuid === 'string' ? parsed.pipeline_uuid : '',
        priority: typeof parsed.priority === 'string' ? parsed.priority : '',
        execution_mode: parseExecutionMode(parsed.execution_mode),
        execution_tool: typeof parsed.execution_tool === 'string' ? parsed.execution_tool : '',
        model_name: typeof parsed.model_name === 'string' ? parsed.model_name : '',
        expert_group_uuid:
          typeof parsed.expert_group_uuid === 'string' ? parsed.expert_group_uuid : '',
        // 未记录过选择时默认勾选，与产品默认值保持一致
        sync_agent_output:
          typeof parsed.sync_agent_output === 'boolean' ? parsed.sync_agent_output : true,
      }
    } catch {
      return emptyDefaults()
    }
  }

  function saveCreateTaskDefaults(defaults: CreateTaskDefaults): void {
    try {
      localStorage.setItem(
        STORAGE_KEY,
        JSON.stringify({
          project_uuid: defaults.project_uuid || '',
          work_dir: defaults.work_dir || '',
          pipeline_uuid: defaults.pipeline_uuid || '',
          priority: defaults.priority || '',
          execution_mode: defaults.execution_mode || '',
          execution_tool: defaults.execution_tool || '',
          model_name: defaults.model_name || '',
          expert_group_uuid: defaults.expert_group_uuid || '',
          sync_agent_output: defaults.sync_agent_output !== false,
        }),
      )
    } catch {
      /* Ignore persistence failures in scenarios such as privacy mode */
    }
  }

  return { getCreateTaskDefaults, saveCreateTaskDefaults }
}
