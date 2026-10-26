'use strict'

const { app, ipcMain, clipboard } = require('electron')
const channels = require('../../shared/channels.cjs')
const { isTrustedURL } = require('../security.cjs')
const { t } = require('../desktop-i18n.cjs')
const { validateQoderPayload, findQoderIDE, isQoderIDERunning, openQoderIDE } = require('../qoder-ide.cjs')
const { prepareQoderHooks } = require('../qoder-hooks.cjs')
const { acknowledgeQoderRestart } = require('../qoder-hook-store.cjs')
const { createQoderDiagnostics } = require('../qoder-diagnostics.cjs')
const fs = require('node:fs/promises')

function registerQoderIDEIPC(allowedOrigins, options = {}) {
  let opening = false
  const controller = new AbortController()
  ipcMain.handle(channels.OPEN_QODER_IDE, async (event, payload) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) throw new Error('Untrusted Qoder IDE request')
    if (controller.signal.aborted) throw new Error(t('error.qoderIDE.openFailed'))
    if (opening) throw new Error(t('error.qoderIDE.busy'))
    opening = true
    const trace = createQoderDiagnostics(app.getPath('userData'))
    let stage = 'validate'
    try {
      trace('request', {
        platform: process.platform, arch: process.arch, version: app.getVersion?.(),
        electron: process.versions.electron, node: process.versions.node,
        packaged: app.isPackaged, promptLength: typeof payload?.prompt === 'string' ? payload.prompt.length : undefined,
        hasSession: Boolean(payload?.threadId),
        inheritedFlags: Object.fromEntries(['VSCODE_DEV', 'VSCODE_CLI', 'VSCODE_IPC_HOOK_CLI', 'ELECTRON_RUN_AS_NODE', 'NODE_OPTIONS'].map(key => [key, Boolean(process.env[key])])),
      })
      validateQoderPayload(payload)
      stage = 'directory'
      trace('directory.requested', { path: payload.directoryPath })
      const directory = await fs.realpath(payload.directoryPath)
      if (!(await fs.stat(directory)).isDirectory()) throw new Error('invalid_directory')
      trace('directory.validated', { requested: payload.directoryPath, resolved: directory })
      stage = 'discovery'
      let protocolPath = ''
      if (process.platform !== 'darwin') {
        const protocol = process.platform === 'win32' ? 'qoder-ide' : 'qoder'
        try { protocolPath = (await app.getApplicationInfoForProtocol(`${protocol}://aicoding.aicoding-deeplink/chat`)).path }
        catch (error) { trace('protocol.unavailable', { code: error.code }) }
      }
      trace('protocol.path', { path: protocolPath })
      const appPath = await findQoderIDE(process.platform, protocolPath)
      trace('ide.selected', { appPath })
      stage = 'backend'
      const backendPath = typeof options.backendPath === 'function' ? await options.backendPath(controller.signal) : ''
      trace('backend.prepared', { backendPath })
      if (controller.signal.aborted) throw new Error('cancelled')
      stage = 'hooks'
      const hooks = await prepareQoderHooks(directory, backendPath, process.platform, app.getPath('userData'))
      const requiresRestart = hooks.restartRequired ?? hooks.updated
      trace('hooks.prepared', { updated: hooks.updated, requiresRestart })
      if (controller.signal.aborted) throw new Error('cancelled')
      if (requiresRestart && await isQoderIDERunning(appPath, process.platform, undefined, controller.signal)) {
        // 等用户保存并退出后再冷启动。展示提示不消费持久标记，也不污染剪贴板。
        return { opened: false, sessionLocated: false, promptDelivery: 'none', promptCopied: false,
          requiresManualStart: !payload.threadId, requiresManualSessionSelection: Boolean(payload.threadId),
          hooksUpdated: hooks.updated, requiresRestart: true }
      }
      stage = 'launch'
      const result = await openQoderIDE({ ...payload, directoryPath: directory }, appPath, process.platform, undefined, undefined, controller.signal, trace)
      if (result.promptDelivery === 'clipboard') {
        try {
          clipboard.writeText(payload.prompt)
          result.promptCopied = true
        } catch {
          // 目录已打开；复制失败单独反馈，保留前端手动复制引导。
          result.promptCopied = false
        }
      }
      if (controller.signal.aborted) throw new Error('cancelled')
      if (result.opened && hooks.restartToken) {
        try {
          await acknowledgeQoderRestart(directory, app.getPath('userData'), hooks.restartToken)
        } catch {
          // 冷启动请求完成才消费标记；失败保留记录，不误报会话已绑定。
          trace('hooks.restart_guidance_pending')
        }
      }
      trace('request.completed', result)
      return { ...result, hooksUpdated: hooks.updated, requiresRestart: false }
    } catch (error) {
      const key = ['not_installed', 'backend_unavailable', 'configuration_conflict', 'configuration_modified', 'configuration_locked', 'prompt_too_long'].includes(error.message) ? error.message : 'openFailed'
      trace('request.failed', { stage, reason: key, code: error.code, name: error.name })
      throw new Error(t(`error.qoderIDE.${key}`))
    } finally { opening = false }
  })
  return () => { controller.abort(); ipcMain.removeHandler(channels.OPEN_QODER_IDE) }
}
module.exports = { registerQoderIDEIPC }
