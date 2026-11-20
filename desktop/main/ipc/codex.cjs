'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { app, clipboard, ipcMain, shell } = require('electron')
const channels = require('../../shared/channels.cjs')
const { buildCodexThreadURL, normalizeCodexThreadID, MAX_CODEX_PROMPT_LENGTH } = require('../codex-url.cjs')
const { userHookHome, codexHookCommand, findCodexAppRuntime, prepareCodexHooks, classifyCodexHooks, callCodexCallback, openCodexHookReview } = require('../codex-app.cjs')
const { t, getLocale } = require('../desktop-i18n.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')

function registerCodexIPC(allowedOrigins, { backendPath }, legacyBackendPath = backendPath) {
  const controller = new AbortController()
  const { signal } = controller
  let busy = false
  ipcMain.handle(channels.OPEN_CODEX_THREAD, async (event, payload) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) throw new Error('Untrusted Codex request')
    if (busy) throw new Error(t('error.codex.busy'))
    busy = true
    let stage = 'invalid_request'
    try {
      const { taskUuid, executionIndex, reportingOwner, action = 'open' } = payload
      const threadId = normalizeCodexThreadID(payload.threadId)
      if (payload.threadId && !threadId) throw new Error(stage)
      // Existing pre-Hook bindings continue in their original conversation.
      if (reportingOwner === undefined && action === 'open') {
        if (!threadId) throw new Error(stage)
        stage = 'openFailed'
        await shell.openExternal(buildCodexThreadURL('', payload.prompt, threadId))
        return { opened: true }
      }
      if (reportingOwner !== 'hooks' || !['open', 'copy', 'review'].includes(action)
          || !normalizeCodexThreadID(taskUuid) || !Number.isSafeInteger(executionIndex) || executionIndex < 0
          || typeof payload.prompt !== 'string' || !payload.prompt.trim() || payload.prompt.includes('\0')
          || payload.prompt.length > MAX_CODEX_PROMPT_LENGTH) throw new Error(stage)
      stage = 'directory'
      const directory = await fs.realpath(normalizeDirectoryPath(payload.directoryPath))
      if (!(await fs.stat(directory)).isDirectory()) throw new Error(stage)
      stage = 'backend_unavailable'
      let backend = await backendPath(signal)
      const args = ['--task', taskUuid, '--execution-index', String(executionIndex), '--work-dir', directory]
      // Authorize this specific project and generation before writing project files.
      stage = threadId ? 'binding_unavailable' : 'task_changed'
      const binding = await callCodexCallback(backend, ['check', ...args, ...(threadId ? ['--thread', threadId] : ['--scope', 'user'])], signal)
      const user = binding.scope === 'user'
      if (!user && threadId) backend = await legacyBackendPath(signal)
      stage = 'not_installed'
      const application = await app.getApplicationInfoForProtocol('codex://threads/new')
      const runtime = await findCodexAppRuntime(application.path, process.platform, application.name, signal)
      stage = 'configuration_conflict'
      const command = user ? codexHookCommand(backend, process.platform, true) : await prepareCodexHooks(directory, backend, process.platform, app.getPath('userData'))
      stage = 'inspection_failed'
      const inspection = await callCodexCallback(backend, ['inspect', '--runtime', runtime, '--work-dir', directory, ...(user ? ['--source', path.join(userHookHome(), 'hooks.json')] : [])], signal)
      const setup = classifyCodexHooks(inspection, directory, command, process.platform, user)
      if (setup !== 'ready') {
        if (action === 'review' && ['project_untrusted', 'hooks_untrusted'].includes(setup)) {
          stage = 'review_failed'
          await openCodexHookReview(runtime, directory, process.platform, signal)
        }
        return { opened: false, setup, userSetup: user }
      }
      if (action === 'review') return { opened: false }
      let prompt = payload.prompt
      if (!threadId) {
        stage = 'task_changed'
        const { launch_id, marker } = await callCodexCallback(backend, ['prepare', ...args, '--locale', getLocale(), ...(user ? ['--scope', 'user'] : [])], signal)
        if (!normalizeCodexThreadID(launch_id) || marker !== (user ? 'TeamBuddy-Codex-Hook: ' : 'TeamBuddy-Codex-App: ') + launch_id) throw new Error(stage)
        prompt += '\n\n' + marker
      }
      signal.throwIfAborted()
      if (action === 'copy') {
        stage = 'copy_failed'
        clipboard.writeText(prompt)
        return { opened: false, copied: true }
      }
      stage = 'openFailed'
      try {
        await shell.openExternal(buildCodexThreadURL(directory, prompt, threadId))
      } catch (error) {
        if (threadId) throw error
        // Keep the original failure visible; copy the same one-time launch.
        stage = 'open_copy_failed'
        clipboard.writeText(prompt)
        return { opened: false, copied: true, openError: t('error.codex.openFailed') }
      }
      return { opened: true }
    } catch (error) {
      const code = ['not_installed', 'runtime_unavailable', 'backend_unavailable', 'configuration_locked', 'configuration_modified', 'configuration_conflict'].includes(error.message) ? error.message : stage
      throw new Error(t(`error.codex.${code}`))
    } finally { busy = false }
  })
  return () => { controller.abort(); ipcMain.removeHandler(channels.OPEN_CODEX_THREAD) }
}

module.exports = { registerCodexIPC }
