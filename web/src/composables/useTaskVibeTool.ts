import apiClient from '@/api/client'
import { t, type MessageKey } from '@/i18n'
import { isDesktopRuntime, openCodexThread, openVibeCli, openQoderIDE } from '@/composables/useDesktop'
import { ensureCodexSync, syncDescription } from '@/composables/useCodexSync'
import { copyText } from '@/utils/clipboard'

export interface VibeToolCapability {
  available: boolean
  installed?: boolean
  reason_code?: string
  message?: string
  launch?: 'app' | 'terminal' | 'clipboard'
  exec_path?: string
  binding_kind?: 'native' | 'generic'
  requires_report_id?: boolean
  supports_resume?: boolean
}

export interface TaskVibeToolContext {
  delivery_warning?: boolean
  reporting_owner?: 'hooks'
  execution_index?: number
  task_uuid: string
  work_dir: string
  prompt: string
  bound?: boolean
  tool_name?: string
  thread_id?: string
  tool?: string
  launch?: 'app' | 'terminal' | 'clipboard'
  exec_path?: string
  model_name?: string
}

export interface VibeToolsCapability {
  available: boolean
  reason_code?: string
  message?: string
  tools?: Record<string, VibeToolCapability>
}

export interface VibeToolOpenResult {
  cancelled?: boolean
  opened: boolean
  copied: boolean
  guidanceKey?: MessageKey
  error?: Error
  setup?: CodexSetupReason
  review?: () => Promise<void>
  requiresIDEExit?: boolean
  retry?: () => Promise<VibeToolOpenResult>
}

export async function loadVibeToolsCapability(): Promise<VibeToolsCapability> {
  return apiClient.get<VibeToolsCapability>('/tasks/codex-capability')
}

export async function assignTaskToCodex(taskUuid: string, start = false): Promise<void> {
  await apiClient.put(`/tasks/${encodeURIComponent(taskUuid)}/execution-mode`, {
    mode: 'vibe_coding',
    tool: 'codex',
    start,
  })
}

export async function loadTaskVibeToolContext(taskUuid: string): Promise<TaskVibeToolContext> {
  return apiClient.get<TaskVibeToolContext>(`/tasks/${encodeURIComponent(taskUuid)}/codex-context`)
}

export async function copyTaskVibeToolPrompt(taskUuid: string): Promise<VibeToolOpenResult> {
  const context = await loadTaskVibeToolContext(taskUuid)
  if (context.reporting_owner) return nativeCodex(context, 'copy')
  const result = await copyPrompt(context.prompt)
  if (result.copied) result.guidanceKey = context.tool === 'generic'
    ? 'workflows.task.detail.genericPromptCopied' : 'workflows.task.detail.codexPromptCopied'
  return result
}

export async function openTaskVibeTool(taskUuid: string): Promise<VibeToolOpenResult> {
  let context: TaskVibeToolContext
  try {
    context = await loadTaskVibeToolContext(taskUuid)
  } catch (error) {
    return { opened: false, copied: false, error: asError(error) }
  }
  // 通用工具只交付提示词：复制后由用户在目标工具中粘贴发送。
  if (context.launch === 'clipboard') {
    const result = await copyPrompt(context.prompt)
    return { ...result, guidanceKey: result.copied ? 'workflows.task.detail.genericPromptCopied' : undefined }
  }
  if (context.tool === 'qoder_ide') {
    if (!isDesktopRuntime()) {
      const copied = await copyPrompt(context.prompt)
      return { ...copied, guidanceKey: copied.copied ? 'workflows.task.detail.qoderIDEManualSetup' : undefined }
    }
    try {
      const result = await openQoderIDE({ directoryPath: context.work_dir, prompt: context.prompt, threadId: context.thread_id })
      const requiresRestart = result.requiresRestart ?? result.hooksUpdated
      if (requiresRestart && !result.opened) {
        return { opened: false, copied: false, requiresIDEExit: true, retry: () => openTaskVibeTool(taskUuid) }
      }
      if (result.promptDelivery === 'clipboard') {
        const guidanceKey = requiresRestart
          ? result.promptCopied ? 'qoderIDERestartClipboard' : 'qoderIDERestartClipboardFailed'
          : result.promptCopied ? 'qoderIDEClipboard' : 'qoderIDEClipboardFailed'
        return {
          opened: result.opened, copied: result.promptCopied,
          guidanceKey: `workflows.task.detail.${guidanceKey}`,
        }
      }
      return {
        opened: result.opened, copied: false,
        guidanceKey: requiresRestart ? 'workflows.task.detail.qoderIDERestart'
          : context.thread_id ? 'workflows.task.detail.qoderIDESelectSession' : 'workflows.task.detail.qoderIDEConfirmStart',
      }
    } catch (error) {
      if (context.thread_id) return { opened: false, copied: false, error: asError(error) }
      const copied = await copyPrompt(context.prompt)
      return { opened: false, copied: copied.copied, error: asError(error) }
    }
  }
  if ((context.launch || 'app') === 'terminal') {
    if (!isDesktopRuntime() || !context.exec_path || !context.tool) {
      return copyPrompt(context.prompt)
    }
    try {
      await openVibeCli({
        tool: context.tool,
        execPath: context.exec_path,
        directoryPath: context.work_dir,
        prompt: context.prompt,
        threadId: context.thread_id,
        modelName: context.model_name,
      })
      return { opened: true, copied: false }
    } catch (openError) {
      const copied = await copyPrompt(context.prompt)
      return copied.copied ? copied : { opened: false, copied: false, error: asError(openError) }
    }
  }
  return openLoadedCodex(context)
}

async function openLoadedCodex(context: TaskVibeToolContext): Promise<VibeToolOpenResult> {
  if (context.reporting_owner) return nativeCodex(context, 'open')
  if (!isDesktopRuntime()) return copyPrompt(context.prompt)
  try {
    await openCodexThread({ directoryPath: context.work_dir, prompt: context.prompt, threadId: context.thread_id })
    return { opened: true, copied: false }
  } catch (openError) {
    const copied = await copyPrompt(context.prompt)
    return copied.copied
      ? copied
      : { opened: false, copied: false, error: asError(openError) }
  }
}

async function nativeCodex(context: TaskVibeToolContext, action: 'open' | 'copy'): Promise<VibeToolOpenResult> {
  if (!isDesktopRuntime()) return { opened: false, copied: false, error: new Error(t('components.errors.openCodexUnsupported')) }
  const payload: DesktopCodexRequest = {
    directoryPath: context.work_dir, prompt: context.prompt, threadId: context.thread_id,
    taskUuid: context.task_uuid, executionIndex: context.execution_index, reportingOwner: context.reporting_owner, action,
  }
  try {
    let result = await openCodexThread(payload)
    if (result.setup && result.userSetup) {
      if (!await ensureCodexSync(result.setup)) return { opened: false, copied: false, cancelled: true }
      // 原任务、目录与代次仍由 Main/Go 重新核对；不把延迟的确认用于另一代任务。
      result = await openCodexThread(payload)
      if (result.setup) throw new Error(syncDescription(result.setup))
    }
    if (result.setup) return {
      opened: false, copied: false, setup: result.setup,
      retry: () => action === 'copy' ? copyTaskVibeToolPrompt(context.task_uuid) : openTaskVibeTool(context.task_uuid),
      review: ['project_untrusted', 'hooks_untrusted'].includes(result.setup) ? async () => {
        try { await openCodexThread({ ...payload, action: 'review' }) }
        catch (error) { throw asError(error) }
      } : undefined,
    }
    return {
      opened: result.opened, copied: !!result.copied, error: result.openError ? new Error(result.openError) : undefined,
      guidanceKey: context.delivery_warning && result.opened ? 'workflows.task.detail.codexDeliveryWarning' : result.copied ? context.thread_id
        ? 'workflows.task.detail.codexBoundCopy' : 'workflows.task.detail.codexNativeCopy'
        : !context.thread_id && result.opened ? 'workflows.task.detail.codexAwaitSend' : undefined,
    }
  } catch (error) { return { opened: false, copied: false, error: asError(error) } }
}

async function copyPrompt(prompt: string): Promise<VibeToolOpenResult> {
  try {
    await copyText(prompt)
    return { opened: false, copied: true }
  } catch (error) {
    return { opened: false, copied: false, error: asError(error) }
  }
}

function asError(error: unknown, fallback = 'Codex operation failed'): Error {
  if (!(error instanceof Error)) return new Error(fallback)
  // Electron invoke 会附加内部通道前缀，界面只展示主进程的业务错误。
  const text = error.message.replace(/^Error invoking remote method '[^']+': (?:Error: )?/, '')
  return new Error(text)
}
