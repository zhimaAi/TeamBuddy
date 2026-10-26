'use strict'

const { contextBridge, ipcRenderer } = require('electron')

// Sandboxed preload scripts only have Electron's limited CommonJS `require`.
// Keep the channel literal here instead of importing a local module, otherwise
// the preload fails before the bridge can be exposed to the renderer.
const SELECT_DIRECTORIES_CHANNEL = 'desktop:select-directories'
const OPEN_DIRECTORY_CHANNEL = 'desktop:open-directory'
const REVEAL_TASK_FILE_CHANNEL = 'desktop:reveal-task-file'
const OPEN_TERMINAL_CHANNEL = 'desktop:open-terminal'
const OPEN_CODEX_THREAD_CHANNEL = 'desktop:open-codex-thread'
const OPEN_QODER_IDE_CHANNEL = 'desktop:open-qoder-ide'
const OPEN_VIBE_CLI_CHANNEL = 'desktop:open-vibe-cli'
const GET_VIBE_MCP_CONFIG_CHANNEL = 'desktop:get-vibe-mcp-config'
const GET_API_TOKEN_CHANNEL = 'desktop:get-api-token'
const GET_CLOSE_BEHAVIOR_CHANNEL = 'desktop:get-close-behavior'
const SET_CLOSE_BEHAVIOR_CHANNEL = 'desktop:set-close-behavior'
const SET_LOCALE_CHANNEL = 'desktop:set-locale'
const TRAY_STATUS_CHANNEL = 'desktop:tray-status'
const SHOW_TASK_NOTIFICATION_CHANNEL = 'desktop:show-task-notification'
const OPEN_TASK_CONVERSATION_CHANNEL = 'desktop:open-task-conversation'
const UPDATE_STATE_CHANNEL = 'desktop:update-state'
const UPDATE_CHECK_CHANNEL = 'desktop:update-check'
const UPDATE_DEFER_CHANNEL = 'desktop:update-defer'
const UPDATE_DOWNLOAD_CHANNEL = 'desktop:update-download'
const UPDATE_CANCEL_CHANNEL = 'desktop:update-cancel'
const UPDATE_INSTALL_CHANNEL = 'desktop:update-install'
const UPDATE_CHANGED_CHANNEL = 'desktop:update-changed'
const START_BROWSER_LOGIN_CHANNEL = 'desktop:start-browser-login'
const BROWSER_LOGIN_RESULT_CHANNEL = 'desktop:browser-login-result'
const RELAUNCH_APP_CHANNEL = 'desktop:relaunch-app'

const MAX_NOTIFICATION_TITLE_LENGTH = 120
const MAX_NOTIFICATION_BODY_LENGTH = 512
const MAX_NOTIFICATION_UUID_LENGTH = 128
const queuedOpenTaskConversations = []
const openTaskConversationListeners = new Set()
const queuedBrowserLoginResults = []
const browserLoginResultListeners = new Set()

function normalizeNotificationText(value, maxLength) {
  return typeof value === 'string' ? value.trim().slice(0, maxLength) : ''
}

function normalizeTaskNotificationPayload(payload) {
  const title = normalizeNotificationText(payload?.title, MAX_NOTIFICATION_TITLE_LENGTH)
  const body = normalizeNotificationText(payload?.body, MAX_NOTIFICATION_BODY_LENGTH)
  const taskUuid = normalizeNotificationText(payload?.taskUuid, MAX_NOTIFICATION_UUID_LENGTH)
  const stepUuid = normalizeNotificationText(payload?.stepUuid, MAX_NOTIFICATION_UUID_LENGTH)
  if (!title || !body || !taskUuid) {
    throw new TypeError('Notification title, body and taskUuid are required')
  }
  return stepUuid ? { title, body, taskUuid, stepUuid } : { title, body, taskUuid }
}

function normalizeOpenTaskConversationPayload(payload) {
  const taskUuid = normalizeNotificationText(payload?.taskUuid, MAX_NOTIFICATION_UUID_LENGTH)
  const stepUuid = normalizeNotificationText(payload?.stepUuid, MAX_NOTIFICATION_UUID_LENGTH)
  if (!taskUuid) return null
  return stepUuid ? { taskUuid, stepUuid } : { taskUuid }
}

ipcRenderer.on(OPEN_TASK_CONVERSATION_CHANNEL, (_event, payload) => {
  const normalized = normalizeOpenTaskConversationPayload(payload)
  if (!normalized) return
  if (!openTaskConversationListeners.size) {
    queuedOpenTaskConversations.push(normalized)
    return
  }
  for (const listener of openTaskConversationListeners) {
    listener(normalized)
  }
})

function normalizeBrowserLoginResult(payload) {
  const status = typeof payload?.status === 'string' ? payload.status : ''
  if (status !== 'ok' && status !== 'failed') return null
  const result = { status }
  if (status === 'ok') {
    result.user = payload?.user ?? null
    result.adminId = typeof payload?.adminId === 'string' ? payload.adminId : ''
  } else {
    result.error = typeof payload?.error === 'string' ? payload.error : ''
  }
  return result
}

ipcRenderer.on(BROWSER_LOGIN_RESULT_CHANNEL, (_event, payload) => {
  const normalized = normalizeBrowserLoginResult(payload)
  if (!normalized) return
  if (!browserLoginResultListeners.size) {
    queuedBrowserLoginResults.push(normalized)
    return
  }
  for (const listener of browserLoginResultListeners) {
    listener(normalized)
  }
})

contextBridge.exposeInMainWorld('goteamsDesktop', Object.freeze({
  platform: process.platform,
  /** @param {{ defaultPath?: string, multiple?: boolean }} options */
  selectDirectories(options = {}) {
    return ipcRenderer.invoke(SELECT_DIRECTORIES_CHANNEL, {
      defaultPath: typeof options.defaultPath === 'string' ? options.defaultPath : undefined,
      multiple: options.multiple === true,
    })
  },
  /** @param {string} directoryPath */
  openDirectory(directoryPath) {
    if (typeof directoryPath !== 'string') {
      return Promise.reject(new TypeError('Directory path must be a string'))
    }
    return ipcRenderer.invoke(OPEN_DIRECTORY_CHANNEL, directoryPath)
  },
  /** @param {{ taskUuid: string, fileRef: string, sessionUuid?: string, locale?: string }} payload */
  revealTaskFile(payload) {
    if (!payload || typeof payload.taskUuid !== 'string' || typeof payload.fileRef !== 'string') {
      return Promise.reject(new TypeError('Task file reference is invalid'))
    }
    return ipcRenderer.invoke(REVEAL_TASK_FILE_CHANNEL, {
      taskUuid: payload.taskUuid,
      fileRef: payload.fileRef,
      sessionUuid: typeof payload.sessionUuid === 'string' ? payload.sessionUuid : '',
      locale: payload.locale === 'en-US' ? 'en-US' : 'zh-CN',
    })
  },
  /** @param {string} directoryPath */
  openTerminal(directoryPath) {
    if (typeof directoryPath !== 'string') return Promise.reject(new TypeError('Directory path must be a string'))
    return ipcRenderer.invoke(OPEN_TERMINAL_CHANNEL, directoryPath)
  },
  /**
   * @param {{ tool: string, execPath: string, directoryPath: string, prompt: string, threadId?: string, modelName?: string }} payload
   */
  openVibeCli(payload) {
    if (
      !payload ||
      typeof payload.tool !== 'string' ||
      typeof payload.execPath !== 'string' ||
      typeof payload.directoryPath !== 'string' ||
      typeof payload.prompt !== 'string' ||
      (payload.threadId !== undefined && typeof payload.threadId !== 'string') ||
      (payload.modelName !== undefined && typeof payload.modelName !== 'string')
    ) {
      return Promise.reject(new TypeError('Vibe CLI payload is invalid'))
    }
    return ipcRenderer.invoke(OPEN_VIBE_CLI_CHANNEL, {
      tool: payload.tool,
      execPath: payload.execPath,
      directoryPath: payload.directoryPath,
      prompt: payload.prompt,
      threadId: payload.threadId,
      modelName: payload.modelName,
    })
  },
/** 手动模式：返回可复制到外部工具的 MCP 配置文本，不写入任何文件。 */
  getVibeMcpConfig(tool) {
    return ipcRenderer.invoke(GET_VIBE_MCP_CONFIG_CHANNEL, { tool: typeof tool === 'string' ? tool : '' })
  },
  /** @param {{ directoryPath: string, prompt: string, threadId?: string }} payload */
  openQoderIDE(payload) {
    if (!payload || typeof payload.directoryPath !== 'string' || typeof payload.prompt !== 'string'
        || (payload.threadId !== undefined && typeof payload.threadId !== 'string')) {
      return Promise.reject(new TypeError('Qoder IDE payload is invalid'))
    }
    return ipcRenderer.invoke(OPEN_QODER_IDE_CHANNEL, {
      tool: 'qoder_ide', directoryPath: payload.directoryPath,
      prompt: payload.prompt, threadId: payload.threadId,
    })
  },
  openCodexThread(payload) {
    if (
      !payload ||
      typeof payload.directoryPath !== 'string' ||
      typeof payload.prompt !== 'string' ||
      (payload.threadId !== undefined && typeof payload.threadId !== 'string')
    ) {
      return Promise.reject(new TypeError('Codex thread payload is invalid'))
    }
    return ipcRenderer.invoke(OPEN_CODEX_THREAD_CHANNEL, {
      directoryPath: payload.directoryPath,
      prompt: payload.prompt,
      threadId: payload.threadId,
    })
  },
  /** Resolves to the startup API token generated by the Electron main process. */
  getApiToken() {
    return ipcRenderer.invoke(GET_API_TOKEN_CHANNEL)
  },
  getCloseBehavior() {
    return ipcRenderer.invoke(GET_CLOSE_BEHAVIOR_CHANNEL)
  },
  setCloseBehavior(closeBehavior) {
    return ipcRenderer.invoke(SET_CLOSE_BEHAVIOR_CHANNEL, closeBehavior)
  },
  getUpdateState() { return ipcRenderer.invoke(UPDATE_STATE_CHANNEL) },
  checkForUpdates(manual = false) { return ipcRenderer.invoke(UPDATE_CHECK_CHANNEL, manual === true) },
  deferUpdate() { return ipcRenderer.invoke(UPDATE_DEFER_CHANNEL) },
  downloadUpdate() { return ipcRenderer.invoke(UPDATE_DOWNLOAD_CHANNEL) },
  cancelUpdateDownload() { return ipcRenderer.invoke(UPDATE_CANCEL_CHANNEL) },
  installUpdate() { return ipcRenderer.invoke(UPDATE_INSTALL_CHANNEL) },
  onUpdateState(listener) {
    if (typeof listener !== 'function') throw new TypeError('Update listener must be a function')
    const handler = (_event, state) => listener(state)
    ipcRenderer.on(UPDATE_CHANGED_CHANNEL, handler)
    return () => ipcRenderer.removeListener(UPDATE_CHANGED_CHANNEL, handler)
  },
  /**
   * 把渲染层当前语言同步给主进程，用于托盘与原生对话框。
   * @param {string} locale
   */
  setLocale(locale) {
    if (typeof locale !== 'string') {
      return Promise.reject(new TypeError('Locale must be a string'))
    }
    return ipcRenderer.invoke(SET_LOCALE_CHANNEL, locale)
  },
  setTrayStatus(status = {}) {
    const count = Number.isFinite(status.runningTaskCount) ? Math.max(0, Math.floor(status.runningTaskCount)) : 0
    ipcRenderer.send(TRAY_STATUS_CHANNEL, { runningTaskCount: count })
  },
  showTaskNotification(payload) {
    try {
      return ipcRenderer.invoke(
        SHOW_TASK_NOTIFICATION_CHANNEL,
        normalizeTaskNotificationPayload(payload),
      )
    } catch (error) {
      return Promise.reject(error)
    }
  },
  onOpenTaskConversation(listener) {
    if (typeof listener !== 'function') {
      throw new TypeError('Task conversation listener must be a function')
    }
    while (queuedOpenTaskConversations.length) {
      listener(queuedOpenTaskConversations.shift())
    }
    openTaskConversationListeners.add(listener)
    return () => {
      openTaskConversationListeners.delete(listener)
    }
  },
  /**
   * 发起浏览器登录：主进程向 Go sidecar 申请登录会话并打开系统浏览器。
   * @param {{ serverType?: 'official' | 'custom', serverUrl?: string }} [options]
   */
  startBrowserLogin(options = {}) {
    const payload = {}
    if (options && (options.serverType === 'official' || options.serverType === 'custom')) {
      payload.serverType = options.serverType
    }
    if (options && typeof options.serverUrl === 'string' && options.serverUrl) {
      payload.serverUrl = options.serverUrl
    }
    return ipcRenderer.invoke(START_BROWSER_LOGIN_CHANNEL, payload)
  },
  /** 订阅浏览器登录深链接回注结果，返回取消订阅函数。 */
  relaunchApp() {
    return ipcRenderer.invoke(RELAUNCH_APP_CHANNEL)
  },
  onBrowserLoginResult(listener) {
    if (typeof listener !== 'function') {
      throw new TypeError('Browser login listener must be a function')
    }
    while (queuedBrowserLoginResults.length) {
      listener(queuedBrowserLoginResults.shift())
    }
    browserLoginResultListeners.add(listener)
    return () => {
      browserLoginResultListeners.delete(listener)
    }
  },
}))
