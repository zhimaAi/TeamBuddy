'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { app, ipcMain, shell } = require('electron')
const channels = require('../../shared/channels.cjs')
const { isTrustedURL } = require('../security.cjs')
const { t } = require('../desktop-i18n.cjs')
const { userHookHome, codexHookCommand, findCodexAppRuntime, prepareUserCodexHooks, classifyCodexHooks, callCodexCallback } = require('../codex-app.cjs')

function registerCodexReportingIPC(origins, { backendPath }) {
  const controller = new AbortController()
  const { signal } = controller
  let installing = false
  ipcMain.handle(channels.CODEX_REPORTING, async (event, action) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, origins)) throw new Error('Untrusted Codex request')
    if (!['status', 'enable', 'review'].includes(action)) throw new Error(t('error.codex.invalid_request'))
    if (action === 'enable' && installing) throw new Error(t('error.codex.busy'))
    if (action === 'enable') installing = true
    let stage = 'not_installed'
    try {
      const runtime = await findCodexAppRuntime(app, signal)
      stage = 'configuration_conflict'
      const source = path.join(userHookHome(), 'hooks.json')
      if (action !== 'enable') {
        try { await fs.lstat(source) } catch (e) { if (e.code === 'ENOENT') return 'not_configured'; throw e }
      }
      const stateRoot = app.getPath('userData')
      stage = 'backend_unavailable'
      const backend = await backendPath(signal)
      stage = 'configuration_conflict'
      const command = action === 'enable' ? await prepareUserCodexHooks(backend, process.platform, stateRoot)
        : codexHookCommand(backend, process.platform, true)
      stage = 'inspection_failed'
      const cwd = await fs.realpath(stateRoot)
      const inspection = await callCodexCallback(backend, ['inspect', '--runtime', runtime, '--work-dir', cwd, '--source', source], signal)
      const state = classifyCodexHooks(inspection, cwd, command, process.platform, true)
      if (action !== 'status' && (state === 'hooks_untrusted' || state === 'hooks_disabled')) {
        stage = 'settings_failed'
        await shell.openExternal('codex://settings')
      }
      return action === 'enable' && state === 'not_configured' ? 'inspection_failed' : state
    } catch (error) {
      const code = ['not_installed', 'runtime_unavailable', 'backend_unavailable', 'configuration_locked', 'configuration_modified', 'configuration_conflict'].includes(error.message) ? error.message : stage
      throw new Error(t('error.codex.' + code))
    } finally { if (action === 'enable') installing = false }
  })
  return () => { controller.abort(); ipcMain.removeHandler(channels.CODEX_REPORTING) }
}
module.exports = { registerCodexReportingIPC }
