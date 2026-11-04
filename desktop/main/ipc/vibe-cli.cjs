'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { ipcMain } = require('electron')
const channels = require('../../shared/channels.cjs')
const { t } = require('../desktop-i18n.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')
const { openVibeCli } = require('../vibe-cli.cjs')

function registerVibeCliIPC(allowedOrigins, options = {}) {
  ipcMain.handle(channels.OPEN_VIBE_CLI, async (event, payload = {}) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted Vibe CLI request')
    }
    try {
      const requestedPath = normalizeDirectoryPath(payload.directoryPath)
      const realPath = await fs.realpath(requestedPath)
      const info = await fs.stat(realPath)
      if (!info.isDirectory()) throw new Error('not a directory')
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
    } catch {
      throw new Error(t('error.vibeCli.openFailed'))
    }
  })
  return () => {
    ipcMain.removeHandler(channels.OPEN_VIBE_CLI)
  }
}

module.exports = { registerVibeCliIPC }
