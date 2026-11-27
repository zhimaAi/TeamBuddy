import { h } from 'vue'
import { Modal } from 'ant-design-vue'
import apiClient from '@/api/client'
import { t, type MessageKey } from '@/i18n'
import router from '@/router'
import { isDesktopRuntime, openCodexThread, openVibeCli, openQoderIDE, openAntigravityApp } from '@/composables/useDesktop'
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
  launch_prompt?: string
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
  antigravityProject?: {
    directoryPath: string
    reason: 'missing' | 'ambiguous' | 'unavailable'
    createFailed: boolean
    recheck: (beforeConsent: () => void) => Promise<VibeToolOpenResult>
    createAndOpen?: (beforeConsent: () => void) => Promise<VibeToolOpenResult>
    openSetup?: () => Promise<void>
  }
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
  if (context.tool === 'antigravity_app') return nativeAntigravityApp(context, 'copy')
  if (context.tool === 'antigravity' && context.reporting_owner === 'hooks') {
    return { opened: false, copied: false, error: new Error(t('components.errors.antigravityOpenFromDesktop')) }
  }
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
  if (context.tool === 'antigravity_app') return nativeAntigravityApp(context, 'open')
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
    if (context.tool === 'codex_cli' && context.reporting_owner === 'hooks') return nativeCodex(context, 'open')
    const nativeAgy = context.tool === 'antigravity' && context.reporting_owner === 'hooks'
    if (!isDesktopRuntime() || !context.exec_path || !context.tool) {
      if (nativeAgy) return { opened: false, copied: false, error: new Error(t('components.errors.antigravityOpenFromDesktop')) }
      return copyPrompt(context.prompt)
    }
    try {
      const result = await openVibeCli({
        tool: context.tool,
        execPath: context.exec_path,
        directoryPath: context.work_dir,
        prompt: nativeAgy ? context.launch_prompt || '' : context.prompt,
        threadId: context.thread_id,
        modelName: context.model_name,
        ...(nativeAgy ? { nativeHook: { taskUuid: context.task_uuid, executionIndex: context.execution_index ?? -1, instruction: context.prompt } } : {}),
      })
      return {
        opened: result.opened, copied: false, cancelled: !result.opened,
        guidanceKey: nativeAgy && context.delivery_warning && result.opened ? 'workflows.task.detail.codexDeliveryWarning' : undefined,
      }
    } catch (openError) {
      if (nativeAgy) return { opened: false, copied: false, error: asError(openError) }
      const copied = await copyPrompt(context.prompt)
      return copied.copied ? copied : { opened: false, copied: false, error: asError(openError) }
    }
  }
  return openLoadedCodex(context)
}

let antigravityConsentActive = false

async function confirmAntigravityAppHooks(payload: DesktopAntigravityAppRequest, route: string) {
  if (antigravityConsentActive || router.currentRoute.value.fullPath !== route) return null
  antigravityConsentActive = true
  try {
    return await new Promise<DesktopAntigravityAppResult | null>((resolve, reject) => {
      let closed = false
      const dialog = Modal.confirm({
        title: t('workflows.task.detail.antigravityAppSetup.hookTitle'),
        content: h('div', [
          h('p', t('workflows.task.detail.antigravityAppSetup.hookDescription')),
          h('p', t('workflows.task.detail.antigravityAppSetup.hookSettings')),
        ]),
        okText: t(payload.action === 'copy' ? 'workflows.task.detail.antigravityAppSetup.enableAndCopy'
          : 'workflows.task.detail.antigravityAppSetup.enableAndOpen'),
        cancelText: t('workflows.task.detail.qoderIDELater'),
        maskClosable: false,
        async onOk() {
          try { finish(await openAntigravityApp({ ...payload, confirmHooks: true })) }
          catch (error) { finish(null, asError(error)) }
        },
        onCancel: onExit,
        afterClose: onExit,
      })
      const removeRoute = router.afterEach(() => { if (router.currentRoute.value.fullPath !== route) onExit() })
      window.addEventListener('pagehide', onExit)
      function onExit() { finish(null) }
      function finish(result: DesktopAntigravityAppResult | null, error?: Error) {
        if (closed) return
        closed = true
        window.removeEventListener('pagehide', onExit)
        removeRoute()
        dialog.destroy()
        if (error) reject(error)
        else resolve(result)
      }
    })
  } finally { antigravityConsentActive = false }
}

async function nativeAntigravityApp(
  context: TaskVibeToolContext, action: 'open' | 'copy' | 'create-project', beforeConsent?: () => void,
): Promise<VibeToolOpenResult> {
  const route = router.currentRoute.value.fullPath
  const payload: DesktopAntigravityAppRequest = {
    directoryPath: context.work_dir, taskUuid: context.task_uuid, executionIndex: context.execution_index ?? -1,
    prompt: context.launch_prompt || '', instruction: context.prompt, threadId: context.thread_id, action,
  }
  try {
    let result = await openAntigravityApp(payload)
    if (router.currentRoute.value.fullPath !== route) return { opened: false, copied: false, cancelled: true }
    if (result.setup === 'hook_consent') {
      beforeConsent?.()
      const confirmed = await confirmAntigravityAppHooks(payload, route)
      if (!confirmed) return { opened: false, copied: false, cancelled: true }
      result = confirmed
    }
    if (result.setup === 'project_required') {
      const reason = result.reason ?? 'missing'
      const continueWith = (nextAction: 'open' | 'create-project', onConsent: () => void) =>
        router.currentRoute.value.fullPath !== route
          ? Promise.resolve({ opened: false, copied: false, cancelled: true })
          : nativeAntigravityApp(context, nextAction, onConsent)
      return { opened: false, copied: false, antigravityProject: {
        directoryPath: result.directoryPath, reason, createFailed: !!result.createFailed,
        recheck: onConsent => continueWith('open', onConsent),
        createAndOpen: result.canCreate ? onConsent => continueWith('create-project', onConsent) : undefined,
        openSetup: reason === 'missing' ? async () => {
          if (router.currentRoute.value.fullPath === route) await openAntigravityApp({ ...payload, action: 'open-project-setup' })
        } : undefined,
      } }
    }
    return {
      opened: result.opened, copied: result.copied, cancelled: result.cancelled,
      guidanceKey: result.copied ? result.projectRequested ? 'workflows.task.detail.antigravityAppProjectOpened'
        : 'workflows.task.detail.antigravityAppPaste' : result.opened
        ? context.delivery_warning ? 'workflows.task.detail.codexDeliveryWarning' : 'workflows.task.detail.antigravityAppResume'
        : undefined,
    }
  } catch (error) { return { opened: false, copied: false, error: asError(error) } }
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
  const cli = context.tool === 'codex_cli'
  const payload: DesktopCodexRequest = {
    directoryPath: context.work_dir, prompt: context.prompt, threadId: context.thread_id,
    taskUuid: context.task_uuid, executionIndex: context.execution_index, reportingOwner: context.reporting_owner, action,
    ...(cli ? { tool: 'codex_cli' as const, modelName: context.model_name } : {}),
  }
  try {
    let result = await openCodexThread(payload)
    if (result.setup && result.userSetup) {
      if (!await ensureCodexSync({
        initial: result.setup,
        configureCLI: cli ? async setupAction =>
          (await openCodexThread({ ...payload, action: setupAction })).setup ?? 'ready' : undefined,
      })) return { opened: false, copied: false, cancelled: true }
      // 原任务、目录与代次仍由 Main/Go 重新核对；不把延迟的确认用于另一代任务。
      result = await openCodexThread(payload)
      if (result.setup) throw new Error(syncDescription(result.setup, cli))
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
        : !cli && !context.thread_id && result.opened ? 'workflows.task.detail.codexAwaitSend' : undefined,
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
