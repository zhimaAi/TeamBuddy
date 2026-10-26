'use strict'

const path = require('node:path')
const { spawn } = require('node:child_process')
const { app, dialog, Menu, session, shell, Tray } = require('electron')
const { APP_TRAY_ICON_PATH, configureAppIdentity, shouldStartInstance } = require('./app-identity.cjs')
const { BackendSupervisor, resolveBackendLaunch } = require('./backend-supervisor.cjs')
const { DesktopPreferences } = require('./desktop-preferences.cjs')
const { setLocale, t } = require('./desktop-i18n.cjs')
const { createBrowserLoginDeepLink, registerProtocolClient } = require('./deep-link.cjs')
const { registerDialogIPC } = require('./ipc/dialog.cjs')
const { registerApiTokenIPC } = require('./ipc/api-token.cjs')
const { registerBrowserLoginIPC, notifyBrowserLoginResult } = require('./ipc/browser-login.cjs')
const { registerCodexIPC } = require('./ipc/codex.cjs')
const { resolveQoderBackend } = require('./qoder-backend.cjs')
const { registerQoderIDEIPC } = require('./ipc/qoder-ide.cjs')
const { registerVibeCliIPC } = require('./ipc/vibe-cli.cjs')
const { mcpBackendPath } = require('./vibe-mcp/codex-cli.cjs')
const { registerTaskNotificationIPC } = require('./ipc/notification.cjs')
const { registerTrayIPC } = require('./ipc/tray.cjs')
const { registerUpdateIPC } = require('./ipc/update.cjs')
const { UpdateManager } = require('./update-manager.cjs')
const { installOnWindows } = require('./update-installer.cjs')
const { registerRelaunchIPC } = require('./ipc/relaunch.cjs')
const { installApplicationMenu } = require('./application-menu.cjs')
const { TrayManager } = require('./tray-manager.cjs')
const { createMainWindow } = require('./window-manager.cjs')

const repoRoot = path.resolve(__dirname, '..', '..')
const rendererURL = process.env.GOTEAMS_DESKTOP_RENDERER_URL || ''

// API 响应已由后端 Cache-Control: no-store 与前端 fetch cache: 'no-store' 保证实时性；
// 图片、文件等静态资源恢复走 Chromium 磁盘缓存。
configureAppIdentity(app)
installApplicationMenu(Menu)
// teamsboard:// 必须在 app ready 之前注册，否则协议唤起的新进程拿不到入口参数。
registerProtocolClient({
  isPackaged: app.isPackaged,
  execPath: process.execPath,
  argv: process.argv,
})
// userData 已固定为 TeamBuddy。重复启动必须调用 Electron 单实例锁，已有进程才能收到 second-instance。
// macOS/Linux 在拿到该锁后，若旧 TeamsBoard 的 SingletonLock 仍指向本机存活进程，则退出且不改旧目录。
const singleInstance = shouldStartInstance(app.getPath('appData'), () => app.requestSingleInstanceLock())

let mainWindow = null
let backend = null
let backendConnection = null
let removeIPC = null
let removeApiTokenIPC = null
let removeBrowserLoginIPC = null
let removeCodexIPC = null
let removeVibeCliIPC = null
let removeQoderIDEIPC = null
let removeTaskNotificationIPC = null
let removeTrayIPC = null
let removeUpdateIPC = null
let updateManager = null
let removeRelaunchIPC = null
let desktopPreferences = null
let trayManager = null
let quitting = false
let exitCode = 0
let closeDecisionPending = false
let quitConfirmationPending = false
const UPDATE_CANCEL_TIMEOUT_MS = 3_000

function failAndQuit(title, error) {
  if (quitting) return
  exitCode = 1
  dialog.showErrorBox(title, error instanceof Error ? error.message : String(error))
  app.quit()
}

// 浏览器登录深链接：sidecar 未就绪时先入队，ready 后补处理。
const browserLoginDeepLink = createBrowserLoginDeepLink({
  getConnection: () => backendConnection,
  notifyRenderer: result => notifyBrowserLoginResult(() => mainWindow, result),
})

function isMainWindowVisible() {
  return Boolean(mainWindow && !mainWindow.isDestroyed() && mainWindow.isVisible())
}

function refreshTray() {
  trayManager?.refresh()
}

function cancelUpdateForQuit() {
  const cancellation = updateManager?.cancelDownload()
  if (!cancellation) return Promise.resolve()
  let timer
  const deadline = new Promise(resolve => {
    timer = setTimeout(resolve, UPDATE_CANCEL_TIMEOUT_MS)
  })
  return Promise.race([cancellation, deadline]).finally(() => clearTimeout(timer))
}

function trackMainWindow(win) {
  mainWindow = win
  win.on('close', event => {
    if (quitting) return
    event.preventDefault()
    void handleMainWindowClose(win)
  })
  win.on('show', refreshTray)
  win.on('hide', refreshTray)
  win.on('minimize', refreshTray)
  win.on('restore', refreshTray)
  win.once('closed', () => {
    if (mainWindow === win) mainWindow = null
    refreshTray()
  })
}

function showOrCreateMainWindow() {
  if (mainWindow && !mainWindow.isDestroyed()) {
    if (mainWindow.isMinimized()) mainWindow.restore()
    mainWindow.show()
    mainWindow.focus()
    refreshTray()
    return
  }
  if (!quitting && backendConnection) {
    const created = createMainWindow({ ...backendConnection, browserTicket: '' })
    trackMainWindow(created.win)
  }
}

function hideMainWindow() {
  if (!mainWindow || mainWindow.isDestroyed()) return
  mainWindow.hide()
  refreshTray()
}

function showNativeDialog(options) {
  return isMainWindowVisible()
    ? dialog.showMessageBox(mainWindow, options)
    : dialog.showMessageBox(options)
}

async function requestQuit() {
  if (quitting || quitConfirmationPending) return

  if (await confirmQuit()) app.quit()
}

async function confirmQuit() {
  if (quitting || quitConfirmationPending) return false

  const taskStatus = trayManager?.getTaskStatus()
  let dialogOptions = null
  if (!taskStatus?.known) {
    dialogOptions = {
      type: 'warning',
      title: t('dialog.quit.title'),
      message: t('dialog.quit.statusUnknown.message'),
      detail: t('dialog.quit.statusUnknown.detail'),
      buttons: [t('dialog.quit.keepRunning'), t('dialog.quit.quitAnyway')],
      defaultId: 0,
      cancelId: 0,
    }
  } else if (taskStatus.count > 0) {
    dialogOptions = {
      type: 'warning',
      title: t('dialog.quit.title'),
      message: t('dialog.quit.runningTasks.message', { count: taskStatus.count }),
      detail: t('dialog.quit.runningTasks.detail'),
      buttons: [t('dialog.quit.keepRunning'), t('dialog.quit.quitAnyway')],
      defaultId: 0,
      cancelId: 0,
    }
  }

  if (!dialogOptions) {
    return true
  }

  quitConfirmationPending = true
  try {
    const result = await showNativeDialog(dialogOptions)
    return result.response === 1
  } finally {
    quitConfirmationPending = false
  }
}

async function installUpdate() {
  const installerPath = await updateManager.verifiedInstallerPath()
  if (process.platform === 'darwin') {
    const error = await shell.openPath(installerPath)
    if (error) throw new Error(error)
    return { started: true, manual: true }
  }
  if (process.platform !== 'win32') throw new Error('当前系统不支持安装更新')
  return installOnWindows({
    confirmQuit,
    onStart: () => { updateManager.status = 'installing'; updateManager.emitState() },
    stopBackend: () => backend?.stop(),
    spawnInstaller: () => new Promise((resolve, reject) => {
        const child = spawn(installerPath, [], { detached: true, stdio: 'ignore', windowsHide: false })
        child.once('spawn', () => { child.unref(); resolve(undefined) })
        child.once('error', reject)
      }),
    quit: () => app.quit(),
    onFailure: error => {
      dialog.showErrorBox(t('update.installFailedTitle'), error instanceof Error ? error.message : String(error))
      // sidecar 已经停止；重新启动客户端以恢复正常运行。
      app.relaunch()
      app.quit()
    },
  })
}

async function handleMainWindowClose(win) {
  if (quitting || closeDecisionPending || !desktopPreferences) return

  const preferences = desktopPreferences.get()
  if (preferences.closeTipShown) {
    if (preferences.closeBehavior === 'quit') {
      await requestQuit()
    } else if (!win.isDestroyed()) {
      hideMainWindow()
    }
    return
  }

  closeDecisionPending = true
  try {
    const result = await showNativeDialog({
      type: 'question',
      title: t('dialog.close.title'),
      message: t('dialog.close.message'),
      detail: t('dialog.close.detail'),
      buttons: [t('dialog.close.minimizeToTray'), t('dialog.close.quit'), t('dialog.close.cancel')],
      defaultId: 0,
      cancelId: 2,
    })
    if (result.response === 0) {
      try {
        desktopPreferences.setCloseBehavior('hide')
      } catch {
        // 保存偏好失败时仍优先保留后台任务，下一次关闭会再次提示。
      }
      if (!win.isDestroyed()) hideMainWindow()
    } else if (result.response === 1) {
      try {
        desktopPreferences.setCloseBehavior('quit')
      } catch {
        // 用户已明确选择退出，偏好保存失败不应阻止本次退出。
      }
      await requestQuit()
    }
  } finally {
    closeDecisionPending = false
  }
}

function createTray() {
  desktopPreferences = new DesktopPreferences(app.getPath('userData'))
  updateManager = new UpdateManager({
    userDataPath: app.getPath('userData'),
    currentVersion: app.getVersion(),
    isPackaged: app.isPackaged,
  })
  // 托盘在渲染层加载之前就要创建，先用上次持久化的语言渲染；
  // 渲染层就绪后会通过 desktop:set-locale 把当前语言同步回来。
  setLocale(desktopPreferences.get().locale)
  trayManager = new TrayManager({
    Tray,
    Menu,
    iconPath: APP_TRAY_ICON_PATH,
    onShowWindow: showOrCreateMainWindow,
    onHideWindow: hideMainWindow,
    onQuit: () => void requestQuit(),
    isWindowVisible: isMainWindowVisible,
  })
  trayManager.create()
}

async function startApplication() {
  // 启动时兜底清一次历史磁盘缓存（旧版本曾缓存过 /api/local/* 的过期响应）；
  // 清理后静态资源恢复正常缓存
  try {
    await session.defaultSession.clearCache()
  } catch {
    // 清理失败不影响启动
  }
  if (quitting) return

  backend = new BackendSupervisor({
    isPackaged: app.isPackaged,
    resourcesPath: process.resourcesPath,
    repoRoot,
    rendererURL,
  })

  backend.on('exit', ({ expected }) => {
    if (!expected && !quitting) {
      failAndQuit(t('error.backendStopped.title'), t('error.backendStopped.detail'))
    }
  })
  backend.on('error', error => {
    failAndQuit(t('error.backendStartFailed.title'), error)
  })

  backendConnection = await backend.start()
  if (quitting) return
  const created = createMainWindow(backendConnection)
  trackMainWindow(created.win)
  removeIPC = registerDialogIPC(created.allowedOrigins, () => backendConnection)
  removeApiTokenIPC = registerApiTokenIPC(created.allowedOrigins, backendConnection.apiToken)
  removeBrowserLoginIPC = registerBrowserLoginIPC(created.allowedOrigins, () => backendConnection)
  removeCodexIPC = registerCodexIPC(created.allowedOrigins)
  removeVibeCliIPC = registerVibeCliIPC(created.allowedOrigins, {
    backendPath: () => mcpBackendPath(resolveBackendLaunch({
      isPackaged: app.isPackaged,
      resourcesPath: process.resourcesPath,
      repoRoot,
      platform: process.platform,
    }).command),
  })
  removeQoderIDEIPC = registerQoderIDEIPC(created.allowedOrigins, {
    backendPath: signal => resolveQoderBackend({
      isPackaged: app.isPackaged,
      resourcesPath: process.resourcesPath,
      repoRoot,
      stateRoot: app.getPath('userData'),
      platform: process.platform,
      signal,
    }),
  })
  removeTaskNotificationIPC = registerTaskNotificationIPC(created.allowedOrigins, {
    getMainWindow: () => mainWindow,
    showOrCreateMainWindow,
  })
  removeTrayIPC = registerTrayIPC(created.allowedOrigins, desktopPreferences, trayManager)
  removeUpdateIPC = registerUpdateIPC(created.allowedOrigins, updateManager, installUpdate)
  removeRelaunchIPC = registerRelaunchIPC(created.allowedOrigins)
  trayManager.setReady()

  // 首次启动就可能由 teamsboard:// 唤起（协议唤起会新开进程），此时 sidecar 才刚就绪。
  browserLoginDeepLink.handleArgv(process.argv)
  browserLoginDeepLink.flush()
}

if (!singleInstance) {
  // Electron 锁失败时，requestSingleInstanceLock 已通知先启动的 TeamBuddy。
  // 旧 TeamsBoard 仍存活时，本进程已拿到新锁但拒绝启动 sidecar，避免两套进程同时碰数据。
  app.quit()
} else {
  app.whenReady().then(() => {
    createTray()
    return startApplication()
  }).catch(error => {
    failAndQuit(t('error.appStartFailed.title'), error)
  })

  app.on('second-instance', (_event, argv) => {
    // Windows/Linux 的协议唤起会把 URL 作为参数交给已有实例。
    browserLoginDeepLink.handleArgv(argv)
    showOrCreateMainWindow()
  })

  // macOS 的协议唤起不会新建进程，URL 通过 open-url 事件送达；该事件可能早于 ready，
  // 此时深链接处理器会把回调入队，sidecar 就绪后再补处理。
  app.on('open-url', (event, url) => {
    event.preventDefault()
    if (browserLoginDeepLink.handleURL(url)) showOrCreateMainWindow()
  })

  app.on('activate', () => {
    showOrCreateMainWindow()
  })

  app.on('before-quit', event => {
    if (quitting) return
    event.preventDefault()
    quitting = true
    removeIPC?.()
    removeApiTokenIPC?.()
    removeBrowserLoginIPC?.()
    removeCodexIPC?.()
    removeVibeCliIPC?.()
    removeQoderIDEIPC?.()
    removeTaskNotificationIPC?.()
    removeTrayIPC?.()
    removeUpdateIPC?.()
    const updateCleanup = cancelUpdateForQuit()
    removeRelaunchIPC?.()
    trayManager?.destroy()
    void Promise.allSettled([backend?.stop(), updateCleanup]).finally(() => app.exit(exitCode))
  })
}
