'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { execFile } = require('node:child_process')
const { app, clipboard, ipcMain, shell } = require('electron')
const channels = require('../../shared/channels.cjs')
const { t } = require('../desktop-i18n.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')
const { ensureAntigravityHooks, confirmAntigravityVersion } = require('../antigravity-hooks.cjs')
const { findAntigravityAppProject, createAntigravityAppProject } = require('../antigravity-app-project.cjs')

const APP_URL = 'antigravity://open'
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const MARKER = 'TeamBuddy-Antigravity-App-Hook: '

function prepareAppLaunch(backend, directory, payload, signal, check, projectCLI) {
  const args = ['antigravity-hook', 'app', 'prepare', '--task', payload.taskUuid, '--execution-index', String(payload.executionIndex), '--work-dir', directory]
  if (payload.threadId) args.push('--thread', payload.threadId)
  if (check) args.push('--check')
  if (projectCLI) args.push('--project-cli')
  return new Promise((resolve, reject) => {
    const child = execFile(backend, args, { cwd: directory, timeout: 10000, maxBuffer: 64 * 1024, windowsHide: true, signal }, (error, stdout) => {
      if (error) { reject(new Error('binding_unavailable')); return }
      try {
        const { marker, runtime } = JSON.parse(stdout)
        if (check || payload.threadId) {
          if (marker !== undefined) throw new Error()
        } else if (typeof marker !== 'string' || !marker.startsWith(MARKER) || !UUID.test(marker.slice(MARKER.length))) throw new Error()
        if (runtime !== undefined && (!projectCLI || typeof runtime !== 'string' || !path.isAbsolute(runtime) || runtime.includes('\0'))) throw new Error()
        resolve({ marker, runtime })
      } catch { reject(new Error('binding_unavailable')) }
    })
    child.stdin?.on('error', () => {})
    child.stdin?.end(check || payload.threadId ? undefined : payload.instruction)
  })
}

function registerAntigravityAppIPC(allowedOrigins, { backendPath }) {
  const controller = new AbortController()
  const { signal } = controller
  let busy = false
  ipcMain.handle(channels.OPEN_ANTIGRAVITY_APP, async (event, payload) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) throw new Error('Untrusted Antigravity App request')
    if (busy) throw new Error(t('error.antigravityApp.busy'))
    busy = true
    try {
      if (!payload || !['open', 'copy', 'open-project-setup', 'create-project'].includes(payload.action) ||
          (payload.confirmHooks !== undefined && payload.confirmHooks !== true) || typeof payload.taskUuid !== 'string' || !UUID.test(payload.taskUuid) ||
          !Number.isSafeInteger(payload.executionIndex) || payload.executionIndex < 0 ||
          (payload.threadId !== undefined && (typeof payload.threadId !== 'string' || !UUID.test(payload.threadId))) ||
          typeof payload.prompt !== 'string' || !payload.prompt.trim() || payload.prompt.includes('\0') || Buffer.byteLength(payload.prompt) > 4096 ||
          typeof payload.instruction !== 'string' || !payload.instruction.trim() || payload.instruction.includes('\0') || Buffer.byteLength(payload.instruction) > 16 * 1024) throw new Error('invalid_request')
      if (payload.threadId && payload.action !== 'open') throw new Error('already_bound')
      if (payload.action !== 'copy' && !/^(?:Google )?Antigravity$/i.test(app.getApplicationNameForProtocol(APP_URL))) throw new Error('not_installed')
      const directory = await fs.realpath(normalizeDirectoryPath(payload.directoryPath))
      if (!(await fs.stat(directory)).isDirectory()) throw new Error('invalid_request')
      const backend = await backendPath(signal)
      const opens = payload.action === 'open' || payload.action === 'create-project'
      const inspectProject = opens && !payload.threadId
      // The empty-stream create path has only been verified on macOS.
      const mayCreate = inspectProject && process.platform === 'darwin'
      const prepare = (check = false, projectCLI = false) => prepareAppLaunch(backend, directory, payload, signal, check, projectCLI)
      const { runtime } = await prepare(true, mayCreate)
      if (payload.action === 'open-project-setup') {
        signal.throwIfAborted()
        clipboard.writeText(directory)
        await shell.openExternal(APP_URL)
        return { opened: false, copied: false }
      }
      const home = os.homedir()
      let projectId
      if (inspectProject) {
        const lookup = () => findAntigravityAppProject(home, directory, signal)
        let project = await lookup()
        const creating = payload.action === 'create-project'
        const supported = project.status === 'missing' && !!runtime &&
          await confirmAntigravityVersion(runtime, signal, 15).then(() => true, () => false)
        signal.throwIfAborted()
        if (creating && supported) {
          project = await lookup()
          if (project.status === 'missing' && (await prepare(true, true)).runtime === runtime) {
            try { await createAntigravityAppProject(runtime, directory, signal) } catch { signal.throwIfAborted() }
            // An exit code alone cannot prove that the exact checkout was registered.
            project = await lookup()
            await prepare(true)
          }
        }
        signal.throwIfAborted()
        if (project.status !== 'found') {
          return { opened: false, copied: false, setup: 'project_required', directoryPath: directory, reason: project.status,
            canCreate: project.status === 'missing' && !creating && supported,
            createFailed: project.status === 'missing' && creating }
        }
        projectId = project.projectId
      }
      const ready = await ensureAntigravityHooks({
        home, workDir: directory, stateRoot: app.getPath('userData'), backend, app: true,
        consent: async () => {
          if (payload.confirmHooks !== true) return false
          signal.throwIfAborted()
          await prepare(true)
          return true
        },
      })
      if (!ready) return { opened: false, copied: false, setup: 'hook_consent' }
      const { marker } = await prepare()
      signal.throwIfAborted()
      if (opens) {
        const url = new URL(APP_URL)
        if (projectId) url.searchParams.set('project', projectId)
        await shell.openExternal(url.toString())
      }
      if (marker) clipboard.writeText(`${payload.prompt.trim()}\n\n${marker}`)
      return { opened: opens, copied: !!marker, ...(projectId ? { projectRequested: true } : {}) }
    } catch (error) {
      const key = ({ hooks_disabled: 'disabled', configuration_conflict: 'config', configuration_modified: 'config',
        configuration_locked: 'busy', binding_unavailable: 'binding', not_installed: 'notInstalled', already_bound: 'alreadyBound' })[error.message]
      if (key === 'config' || key === 'binding') throw new Error(t(`error.antigravity.${key}`))
      throw new Error(t(`error.antigravityApp.${key || 'openFailed'}`))
    } finally {
      busy = false
    }
  })
  return () => {
    controller.abort()
    ipcMain.removeHandler(channels.OPEN_ANTIGRAVITY_APP)
  }
}

module.exports = { registerAntigravityAppIPC }
