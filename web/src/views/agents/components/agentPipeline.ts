import type { Pipeline, PipelineStep } from '@/types/pipeline'

export { AGENT_AVATARS, resolveAgentAvatar } from '@/config/agentAvatars'
export { isPipelineCloud } from '@/utils/pipeline'

export interface DiscoveredCLI {
  type: string
  name: string
  installed: boolean
  exec_path?: string
  version?: string
  models?: string[]
}

export interface PipelineInput {
  name: string
  description: string
  avatar: string
}

export interface StepInput {
  name: string
  description: string
  avatar: string
  prompt: string
  cli_type: string
  model_name: string
  thinking_level?: string
}

export interface CloudStepExecutionInput {
  cli_type: string
  model_name: string
  thinking_level?: string
}

export interface DeleteResponse {
  deleted: boolean
}

export interface ReorderResponse {
  ok: boolean
}

export interface ReusablePipelineStep extends PipelineStep {
  pipelineName: string
}

// 预设头像托管在 public/avatars 下：URL 即文件名、无构建 hash，dev 与生产地址一致（同 Agent 头像约定）
export const PIPELINE_AVATARS = [
  '/avatars/pipeline-avatar-1.svg',
  '/avatars/pipeline-avatar-2.svg',
  '/avatars/pipeline-avatar-3.svg',
  '/avatars/pipeline-avatar-4.svg',
  '/avatars/pipeline-avatar-5.svg',
  '/avatars/pipeline-avatar-6.svg',
  '/avatars/pipeline-avatar-7.svg',
]

export function stepModel(step: PipelineStep) {
  return step.model_name || step.model || ''
}

export function deduplicateReusableSteps(steps: ReusablePipelineStep[]) {
  const seen = new Set<string>()
  return steps.filter((step) => {
    const fingerprint = JSON.stringify([
      step.name,
      step.prompt || step.prompt_snapshot || '',
      step.cli_type || '',
      stepModel(step),
      step.thinking_level || '',
    ])
    if (seen.has(fingerprint)) return false
    seen.add(fingerprint)
    return true
  })
}

export function missingSteps(pipeline: Pipeline) {
  return (pipeline.steps || []).filter((step) => !step.cli_type || !stepModel(step))
}

export function sortPipelineSteps(steps: PipelineStep[]) {
  return [...steps].sort((a, b) => a.sort_order - b.sort_order)
}
