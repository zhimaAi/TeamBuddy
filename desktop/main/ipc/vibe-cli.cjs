'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { app, BrowserWindow, dialog, ipcMain } = require('electron')
const channels = require('../../shared/channels.cjs')
const { t } = require('../desktop-i18n.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')
const { openVibeCli } = require('../vibe-cli.cjs')
const { ensureAntigravityHooks, prepareAntigravityLaunch, confirmAntigravityVersion } = require('../antigravity-hooks.cjs')

function registerVibeCliIPC(allowedOrigins, options = {}) {
  const controller = new AbortController()
  let agyBusy = false
  ipcMain.handle(channels.OPEN_VIBE_CLI, async (event, payload = {}) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted Vibe CLI request')
    }
    const native = payload.tool === 'antigravity' && payload.nativeHook
    if (native && agyBusy) throw new Error(t('error.antigravity.busy'))
    if (native) agyBusy = true
    try {
      const requestedPath = normalizeDirectoryPath(payload.directoryPath)
      const realPath = await fs.realpath(requestedPath)
      const info = await fs.stat(realPath)
      if (!info.isDirectory()) throw new Error('not a directory')
      if (native) {
        const { signal } = controller
        const backend = await options.antigravityBackendPath(signal)
        const prepare = (check = false) => prepareAntigravityLaunch(backend, realPath, native, payload.threadId, signal, check)
        // Validate the real task first, and again after any delayed consent.
        const checked = await prepare(true)
        await confirmAntigravityVersion(checked.runtime, signal)
        const ready = await ensureAntigravityHooks({
          home: os.homedir(), workDir: realPath, stateRoot: app.getPath('userData'), backend,
          consent: async () => {
            const settings = /** @type {import('electron').MessageBoxOptions} */ ({
              type: 'question', message: t('antigravity.hooks.title'), detail: t('antigravity.hooks.detail'),
              buttons: [t('antigravity.hooks.enable'), t('antigravity.hooks.cancel')], defaultId: 1, cancelId: 1,
            })
            const win = BrowserWindow.fromWebContents(event.sender)
            const result = await (win ? dialog.showMessageBox(win, settings) : dialog.showMessageBox(settings))
            signal.throwIfAborted()
            if (result.response !== 0) return false
            await prepare(true) // Recheck before installing user configuration.
            return true
          },
        })
        if (!ready) return { opened: false }
        const { ticket, runtime } = await prepare()
        if (runtime !== checked.runtime) throw new Error('binding_unavailable')
        signal.throwIfAborted()
        await openVibeCli(realPath, 'antigravity', runtime, payload.prompt, payload.threadId, process.platform, undefined,
          undefined, payload.modelName, '', ticket)
        return { opened: true }
      }
      if (typeof payload.execPath !== 'string' || !path.isAbsolute(payload.execPath)) {
        throw new Error('not a file')
      }
      const resolvedExec = await fs.realpath(payload.execPath)
      const execInfo = await fs.stat(resolvedExec)
      if (!execInfo.isFile()) throw new Error('not a file')
      const backendPath = typeof options.backendPath === 'function' ? options.backendPath() : ''
      await openVibeCli(realPath, payload.tool, payload.execPath, payload.prompt, payload.threadId, process.platform, undefined, {
        backendPath,
        workDir: realPath,
        homeDir: os.homedir(),
        codexHome: process.env.CODEX_HOME,
      }, payload.modelName)
      return { opened: true }
    } catch (error) {
      if (!native) throw new Error(t('error.vibeCli.openFailed'))
      const key = ({ version_unsupported: 'version', hooks_disabled: 'disabled', configuration_conflict: 'config',
        configuration_modified: 'config', configuration_locked: 'busy', binding_unavailable: 'binding' })[error.message] || 'openFailed'
      throw new Error(t(`error.antigravity.${key}`))
    } finally {
      if (native) agyBusy = false
    }
  })
  return () => {
    controller.abort()
    ipcMain.removeHandler(channels.OPEN_VIBE_CLI)
  }
}

module.exports = { registerVibeCliIPC }
