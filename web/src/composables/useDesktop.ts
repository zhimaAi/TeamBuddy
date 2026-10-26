import { t, getCurrentLocale } from '@/i18n'
import apiClient from '@/api/client'
import { copyText } from '@/utils/clipboard'

export function isDesktopRuntime(): boolean {
  return typeof window !== 'undefined' && Boolean(window.goteamsDesktop)
}

export function isMacDesktopRuntime(): boolean {
  return isDesktopRuntime() && window.goteamsDesktop?.platform === 'darwin'
}

export function isWindowsDesktopRuntime(): boolean {
  return isDesktopRuntime() && window.goteamsDesktop?.platform === 'win32'
}

export type DesktopCloseBehavior = 'hide' | 'quit'

function updateBridge() {
  const bridge = window.goteamsDesktop
  if (!bridge) throw new Error(t('components.errors.desktopSettingsUnsupported'))
  return bridge
}

export const getUpdateState = () => updateBridge().getUpdateState()
export const checkForUpdates = (manual = false) => updateBridge().checkForUpdates(manual)
export const deferUpdate = () => updateBridge().deferUpdate()
export const downloadUpdate = () => updateBridge().downloadUpdate()
export const cancelUpdateDownload = () => updateBridge().cancelUpdateDownload()
export const installUpdate = () => updateBridge().installUpdate()
export const onUpdateState = (listener: (state: DesktopUpdateState) => void) =>
  updateBridge().onUpdateState(listener)

export interface DesktopTaskNotification {
  title: string
  body: string
  taskUuid: string
  stepUuid?: string
}

async function pickDirectories(
  defaultPath: string | undefined,
  multiple: boolean,
): Promise<string[]> {
  const bridge = window.goteamsDesktop
  if (!bridge) {
    throw new Error(t('components.errors.selectDirectoryUnsupported'))
  }
  return (await bridge.selectDirectories({ defaultPath, multiple })) ?? []
}

export async function selectDirectory(defaultPath?: string): Promise<string | undefined> {
  const paths = await pickDirectories(defaultPath, false)
  return paths[0]
}

export async function selectDirectories(defaultPath?: string): Promise<string[]> {
  return pickDirectories(defaultPath, true)
}

export async function openDirectory(directoryPath: string): Promise<void> {
  const bridge = window.goteamsDesktop
  if (!bridge) {
    throw new Error(t('components.errors.openDirectoryUnsupported'))
  }
  await bridge.openDirectory(directoryPath)
}

export async function revealTaskOutputFile(
  taskUuid: string,
  fileRef: string,
  sessionUuid = '',
): Promise<'revealed' | 'copied'> {
  const bridge = window.goteamsDesktop
  if (bridge?.revealTaskFile) {
    await bridge.revealTaskFile({
      taskUuid,
      fileRef,
      sessionUuid,
      locale: getCurrentLocale(),
    })
    return 'revealed'
  }
  const located = await apiClient.post<{ path: string }>(
    `/tasks/${encodeURIComponent(taskUuid)}/files/locate`,
    { path: fileRef, session_uuid: sessionUuid },
  )
  await copyText(located.path)
  return 'copied'
}

export async function openTerminal(directoryPath: string): Promise<void> {
  const bridge = window.goteamsDesktop
  if (!bridge?.openTerminal) throw new Error(t('workflows.task.progress.terminalUnsupported'))
  await bridge.openTerminal(directoryPath)
}

export async function openVibeCli(payload: {
  tool: string
  execPath: string
  directoryPath: string
  prompt: string
  threadId?: string
  modelName?: string
}): Promise<void> {
  const bridge = window.goteamsDesktop
  if (!bridge?.openVibeCli) {
    throw new Error(t('components.errors.openVibeToolUnsupported'))
  }
  await bridge.openVibeCli(payload)
}

export interface VibeMcpConfig {
  available: boolean
  config: string
  configPath: string
  command: string
}

/**
 * 读取手动模式的 MCP 配置文本，供用户复制到外部工具的 mcp.json。
 * 客户端只展示、不写入任何用户配置；非桌面运行时返回 available:false。
 * tool 决定推荐写入路径（cursor / workbuddy / dsh），未知工具由调用方兜底展示。
 */
export async function loadVibeMcpConfig(tool?: string): Promise<VibeMcpConfig> {
  const bridge = window.goteamsDesktop
  if (!bridge?.getVibeMcpConfig) {
    return { available: false, config: '', configPath: '', command: '' }
  }
  return bridge.getVibeMcpConfig(tool)
}

export async function openQoderIDE(payload: {
  directoryPath: string
  prompt: string
  threadId?: string
}) {
  const bridge = window.goteamsDesktop
  if (!bridge?.openQoderIDE) throw new Error(t('components.errors.openVibeToolUnsupported'))
  return bridge.openQoderIDE(payload)
}

export async function openCodexThread(
  directoryPath: string,
  prompt: string,
  threadId?: string,
): Promise<void> {
  const bridge = window.goteamsDesktop
  if (!bridge) {
    throw new Error(t('components.errors.openCodexUnsupported'))
  }
  await bridge.openCodexThread({ directoryPath, prompt, threadId })
}

export async function getDesktopCloseBehavior(): Promise<DesktopCloseBehavior> {
  const bridge = window.goteamsDesktop
  if (!bridge) {
    throw new Error(t('components.errors.desktopSettingsUnsupported'))
  }
  return (await bridge.getCloseBehavior()).closeBehavior
}

export async function setDesktopCloseBehavior(
  closeBehavior: DesktopCloseBehavior,
): Promise<DesktopCloseBehavior> {
  const bridge = window.goteamsDesktop
  if (!bridge) {
    throw new Error(t('components.errors.desktopSettingsUnsupported'))
  }
  return (await bridge.setCloseBehavior(closeBehavior)).closeBehavior
}

export async function restartDesktopApp(): Promise<void> {
  const bridge = window.goteamsDesktop
  if (!bridge?.relaunchApp) {
    throw new Error(t('settings.workspaceRestartUnsupported'))
  }
  await bridge.relaunchApp()
}

export function syncTrayTaskCount(runningTaskCount: number) {
  window.goteamsDesktop?.setTrayStatus({ runningTaskCount })
}

export function showTaskNotification(notification: DesktopTaskNotification): Promise<{
  shown: boolean
  reason?: string
  message?: string
}> {
  return (
    window.goteamsDesktop?.showTaskNotification(notification) ??
    Promise.resolve({ shown: false, reason: 'unsupported' })
  )
}

export function onOpenTaskConversation(
  listener: (target: { taskUuid: string; stepUuid?: string }) => void,
) {
  return window.goteamsDesktop?.onOpenTaskConversation(listener) ?? (() => {})
}
