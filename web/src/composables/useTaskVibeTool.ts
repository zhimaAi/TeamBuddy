import apiClient from '@/api/client'
import type { MessageKey } from '@/i18n'
import { isDesktopRuntime, openCodexThread, openVibeCli, openQoderIDE } from '@/composables/useDesktop'
import { copyText } from '@/utils/clipboard'

export interface VibeToolCapability {
  available: boolean
  installed?: boolean
  reason_code?: string
  message?: string
  launch?: 'app' | 'terminal' | 'clipboard' | 'manual'
  exec_path?: string
  binding_kind?: 'native' | 'generic'
  requires_report_id?: boolean
  supports_resume?: boolean
}

export interface TaskVibeToolContext {
  task_uuid: string
  work_dir: string
  prompt: string
  bound?: boolean
  tool_name?: string
  thread_id?: string
  tool?: string
  launch?: 'app' | 'terminal' | 'clipboard' | 'manual'
  exec_path?: string
  model_name?: string
  /** 手动模式：本地 Skill 文件路径，供用户或外部 IDE 读取 */
  skill_path?: string
  /** 手动模式：桥接程序是否可用（开发态为 false） */
  mcp_available?: boolean
  /** 手动模式：可直接写入外部 IDE 的 MCP 配置文本 */
  mcp_config?: string
  /** 手动模式：推荐写入路径，例如 ~/.cursor/mcp.json */
  mcp_config_path?: string
  /** 手动模式：桥接程序绝对路径 */
  mcp_command?: string
  /** 手动模式：本机是否有可用的 AiBridge 扩展（有则不必手动复制提示词） */
  aibridge_available?: boolean
  /** 手动模式：AiBridge 探测到的编辑器类型 */
  aibridge_editor?: string
}

export interface VibeToolsCapability {
  available: boolean
  reason_code?: string
  message?: string
  tools?: Record<string, VibeToolCapability>
}

export interface VibeToolOpenResult {
  opened: boolean
  copied: boolean
  guidanceKey?: MessageKey
  error?: Error
  requiresIDEExit?: boolean
  retry?: () => Promise<VibeToolOpenResult>
  /** 手动模式：没有可拉起的进程，调用方应改为展示可复制面板。 */
  manual?: boolean
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

export interface VibeToolInjectResult {
  injected: boolean
  /** AiBridge 探测到的编辑器类型（cursor / vscode / windsurf） */
  editor?: string
}

/**
 * 把手动模式提示词交给本机 AiBridge 扩展直接下发到编辑器 AI 对话。
 * 未安装扩展或下发失败时接口返回错误，调用方回退为手动复制。
 */
export async function injectVibeToolPrompt(taskUuid: string): Promise<VibeToolInjectResult> {
  return apiClient.post<VibeToolInjectResult>(
    `/tasks/${encodeURIComponent(taskUuid)}/vibe-manual-inject`,
    {},
  )
}

export async function copyTaskVibeToolPrompt(taskUuid: string): Promise<TaskVibeToolContext> {
  const context = await loadTaskVibeToolContext(taskUuid)
  await copyText(context.prompt)
  return context
}

export async function openTaskVibeTool(taskUuid: string): Promise<VibeToolOpenResult> {
  let context: TaskVibeToolContext
  try {
    context = await loadTaskVibeToolContext(taskUuid)
  } catch (error) {
    return { opened: false, copied: false, error: asError(error) }
  }
  // 手动模式不启动任何进程：由调用方展示 MCP 配置、Skill 路径与提示词的复制面板。
  if (context.launch === 'manual') {
    return { opened: false, copied: false, manual: true }
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
  if (!isDesktopRuntime()) return copyPrompt(context.prompt)
  if (!context.thread_id) {
    try {
      await apiClient.post(`/tasks/${encodeURIComponent(context.task_uuid)}/codex-session-watch`, {})
    } catch {
      // 监听失败不阻止打开 Codex。用户仍可手动粘贴提示词。
    }
  }
  try {
    await openCodexThread(context.work_dir, context.prompt, context.thread_id)
    return { opened: true, copied: false }
  } catch (openError) {
    const copied = await copyPrompt(context.prompt)
    return copied.copied
      ? copied
      : { opened: false, copied: false, error: asError(openError) }
  }
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
