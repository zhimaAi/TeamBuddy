'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { BrowserWindow, dialog, ipcMain, shell } = require('electron')
const { openTerminalWindow } = require('../terminal-launcher.cjs')
const channels = require('../../shared/channels.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')
const { locateTaskOutputFile } = require('./reveal-task-file.cjs')
const { t } = require('../desktop-i18n.cjs')

function registerDialogIPC(allowedOrigins, getConnection) {
  /** @param {Electron.IpcMainInvokeEvent} event @param {{ defaultPath?: string, multiple?: boolean }} options */
  ipcMain.handle(channels.SELECT_DIRECTORIES, async (event, options = {}) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted directory picker request')
    }

    const parent = BrowserWindow.fromWebContents(event.sender)
    const defaultPath = typeof options.defaultPath === 'string' && path.isAbsolute(options.defaultPath)
      ? options.defaultPath
      : undefined
    /** @type {Electron.OpenDialogOptions['properties']} */
    const properties = ['openDirectory']
    if (options.multiple === true) properties.push('multiSelections')

    /** @type {Electron.OpenDialogOptions} */
    const dialogOptions = {
      title: t('dialog.selectWorkDirectory.title'),
      defaultPath,
      properties,
    }
    const result = parent
      ? await dialog.showOpenDialog(parent, dialogOptions)
      : await dialog.showOpenDialog(dialogOptions)
    return result.canceled ? [] : result.filePaths
  })

  ipcMain.handle(channels.OPEN_DIRECTORY, async (event, directoryPath) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted open-directory request')
    }

    const requestedPath = normalizeDirectoryPath(directoryPath)
    const realPath = await fs.realpath(requestedPath)
    const info = await fs.stat(realPath)
    if (!info.isDirectory()) {
      throw new Error('Requested path is not a directory')
    }
    const openError = await shell.openPath(realPath)
    if (openError) {
      throw new Error(`Failed to open directory: ${openError}`)
    }
    return { opened: true }
  })

  ipcMain.handle(channels.REVEAL_TASK_FILE, async (event, payload) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted reveal-file request')
    }
    const connection = typeof getConnection === 'function' ? getConnection() : null
    const located = await locateTaskOutputFile({
      baseURL: connection && connection.baseURL,
      apiToken: connection && connection.apiToken,
      taskUuid: payload && payload.taskUuid,
      fileRef: payload && payload.fileRef,
      sessionUuid: payload && payload.sessionUuid,
      locale: payload && payload.locale,
    })
    const realPath = await fs.realpath(located)
    if (realPath !== located) throw new Error('Located file path is invalid')
    const info = await fs.stat(realPath)
    if (!info.isFile()) throw new Error('Requested path is not a file')
    shell.showItemInFolder(realPath)
    return { revealed: true }
  })

  ipcMain.handle(channels.OPEN_TERMINAL, async (event, directoryPath) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) throw new Error('Untrusted open-terminal request')
    const requestedPath = normalizeDirectoryPath(directoryPath)
    const realPath = await fs.realpath(requestedPath)
    const info = await fs.stat(realPath)
    if (!info.isDirectory()) throw new Error('Requested path is not a directory')
    try {
      await openTerminalWindow(realPath)
    } catch {
      throw new Error(t('dialog.openTerminal.failed'))
    }
    return { opened: true }
  })

  return () => {
    ipcMain.removeHandler(channels.SELECT_DIRECTORIES)
    ipcMain.removeHandler(channels.OPEN_DIRECTORY)
    ipcMain.removeHandler(channels.REVEAL_TASK_FILE)
    ipcMain.removeHandler(channels.OPEN_TERMINAL)
  }
}

module.exports = { registerDialogIPC }
