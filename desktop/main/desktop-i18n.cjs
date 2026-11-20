'use strict'

// 主进程原生界面（托盘、系统对话框）的双语词条。
// 渲染层的 vue-i18n 资源无法在主进程使用：主进程在窗口创建之前就要展示托盘，
// 且沙箱化 preload 不能引入渲染层模块，因此这里维护独立的最小词条表。
const SUPPORTED_LOCALES = Object.freeze(['zh-CN', 'en-US'])
const DEFAULT_LOCALE = 'zh-CN'

// one/other 与 go-i18n、vue-i18n 保持一致：中文只有 other，英文按数量选单复数。
const MESSAGES = Object.freeze({
  'update.assetMissing': {
    'zh-CN': { other: '新版本没有适用于当前系统和架构的安装包' },
    'en-US': { other: 'No installer is available for this system and architecture' },
  },
  'update.checkFailed': {
    'zh-CN': { other: '检查更新失败' },
    'en-US': { other: 'Failed to check for updates' },
  },
  'update.downloadFailed': {
    'zh-CN': { other: '下载更新失败' },
    'en-US': { other: 'Failed to download the update' },
  },
  'update.packageInvalid': {
    'zh-CN': { other: '安装包校验失败' },
    'en-US': { other: 'Installer verification failed' },
  },
  'update.installInvalid': {
    'zh-CN': { other: '安装包不存在或校验失败，请重新下载' },
    'en-US': { other: 'The installer is missing or invalid. Download it again.' },
  },
  'update.unavailable': {
    'zh-CN': { other: '当前无法下载更新' },
    'en-US': { other: 'The update cannot be downloaded now' },
  },
  'update.installFailedTitle': {
    'zh-CN': { other: '安装更新失败' },
    'en-US': { other: 'Failed to install the update' },
  },
  'dialog.openTerminal.failed': {
    'zh-CN': { other: '无法打开终端，请检查系统终端是否可用' },
    'en-US': { other: 'Unable to open the system terminal' },
  },
  'tray.tooltip.starting': {
    'zh-CN': { other: 'TeamBuddy · 正在启动' },
    'en-US': { other: 'TeamBuddy · Starting' },
  },
  'tray.tooltip.runningTasks': {
    'zh-CN': { other: 'TeamBuddy · {count} 个任务执行中' },
    'en-US': {
      one: 'TeamBuddy · {count} task running',
      other: 'TeamBuddy · {count} tasks running',
    },
  },
  'tray.menu.show': {
    'zh-CN': { other: '打开 TeamBuddy' },
    'en-US': { other: 'Open TeamBuddy' },
  },
  'tray.menu.hide': {
    'zh-CN': { other: '隐藏主窗口' },
    'en-US': { other: 'Hide main window' },
  },
  'tray.menu.statusStarting': {
    'zh-CN': { other: '任务状态：正在启动' },
    'en-US': { other: 'Task status: starting' },
  },
  'tray.menu.statusWaiting': {
    'zh-CN': { other: '任务状态：等待同步' },
    'en-US': { other: 'Task status: waiting to sync' },
  },
  'tray.menu.runningTasks': {
    'zh-CN': { other: '执行中任务：{count}' },
    'en-US': { other: 'Running tasks: {count}' },
  },
  'tray.menu.quit': {
    'zh-CN': { other: '退出 TeamBuddy' },
    'en-US': { other: 'Quit TeamBuddy' },
  },
  'dialog.selectWorkDirectory.title': {
    'zh-CN': { other: '选择工作目录' },
    'en-US': { other: 'Select working directory' },
  },
  'dialog.quit.title': {
    'zh-CN': { other: '退出 TeamBuddy' },
    'en-US': { other: 'Quit TeamBuddy' },
  },
  'dialog.quit.statusUnknown.message': {
    'zh-CN': { other: '无法确认是否有任务正在执行' },
    'en-US': { other: 'Cannot confirm whether tasks are still running' },
  },
  'dialog.quit.statusUnknown.detail': {
    'zh-CN': { other: '退出 TeamBuddy 将停止本地后台服务。建议继续在后台运行。' },
    'en-US': { other: 'Quitting TeamBuddy stops the local background service. Keeping it running is recommended.' },
  },
  'dialog.quit.runningTasks.message': {
    'zh-CN': { other: '当前有 {count} 个任务正在执行' },
    'en-US': {
      one: '{count} task is currently running',
      other: '{count} tasks are currently running',
    },
  },
  'dialog.quit.runningTasks.detail': {
    'zh-CN': { other: '退出 TeamBuddy 将中断这些任务。' },
    'en-US': { other: 'Quitting TeamBuddy interrupts these tasks.' },
  },
  'dialog.quit.keepRunning': {
    'zh-CN': { other: '继续后台运行' },
    'en-US': { other: 'Keep running in background' },
  },
  'dialog.quit.quitAnyway': {
    'zh-CN': { other: '仍然退出' },
    'en-US': { other: 'Quit anyway' },
  },
  'dialog.close.title': {
    'zh-CN': { other: '关闭 TeamBuddy' },
    'en-US': { other: 'Close TeamBuddy' },
  },
  'dialog.close.message': {
    'zh-CN': { other: '关闭窗口后，TeamBuddy 可以继续在后台运行' },
    'en-US': { other: 'TeamBuddy can keep running in the background after the window closes' },
  },
  'dialog.close.detail': {
    'zh-CN': { other: '正在执行的任务不会中断，你可以通过系统托盘重新打开或退出应用。' },
    'en-US': { other: 'Running tasks are not interrupted. Reopen or quit the app from the system tray.' },
  },
  'dialog.close.minimizeToTray': {
    'zh-CN': { other: '最小化到托盘' },
    'en-US': { other: 'Minimize to tray' },
  },
  'dialog.close.quit': {
    'zh-CN': { other: '退出 TeamBuddy' },
    'en-US': { other: 'Quit TeamBuddy' },
  },
  'dialog.close.cancel': {
    'zh-CN': { other: '取消' },
    'en-US': { other: 'Cancel' },
  },
  'error.backendStopped.title': {
    'zh-CN': { other: 'TeamBuddy 服务已停止' },
    'en-US': { other: 'TeamBuddy service stopped' },
  },
  'error.backendStopped.detail': {
    'zh-CN': { other: '本地服务意外退出，客户端即将关闭。' },
    'en-US': { other: 'The local service exited unexpectedly. The client will now close.' },
  },
  'error.backendStartFailed.title': {
    'zh-CN': { other: 'TeamBuddy 服务启动失败' },
    'en-US': { other: 'TeamBuddy service failed to start' },
  },
  'error.appStartFailed.title': {
    'zh-CN': { other: 'TeamBuddy 启动失败' },
    'en-US': { other: 'TeamBuddy failed to start' },
  },
  'error.codex.busy': {
    'zh-CN': { other: "正在准备 Codex，请稍后重试。" },
    'en-US': { other: "Codex setup is in progress. Try again shortly." },
  },
  'error.codex.not_installed': {
    'zh-CN': { other: "未找到可打开 codex:// 的 Codex App，请检查安装。" },
    'en-US': { other: "No Codex App handles codex://. Check your installation." },
  },
  'error.codex.runtime_unavailable': {
    'zh-CN': { other: "无法定位 Codex App 随附的运行程序，请核对桌面版安装位置和版本。" },
    'en-US': { other: "Could not locate the runtime bundled with Codex App. Check the desktop installation and version." },
  },
  'error.codex.backend_unavailable': {
    'zh-CN': { other: "无法准备 TeamBuddy 回调程序，请检查本地服务后重试。" },
    'en-US': { other: "Could not prepare the TeamBuddy callback. Check the local service and retry." },
  },
  'error.codex.configuration_conflict': {
    'zh-CN': { other: "无法安全合并 Codex 配置，请检查文件、写入权限和已有 Hooks。" },
    'en-US': { other: "Could not safely merge the Codex settings. Check the files, permissions and existing hooks." },
  },
  'error.codex.configuration_modified': {
    'zh-CN': { other: "TeamBuddy Hooks 已被修改，请先核对 .codex/hooks.json；已保留你的配置。" },
    'en-US': { other: "TeamBuddy hooks were changed. Review .codex/hooks.json; your settings were preserved." },
  },
  'error.codex.configuration_locked': {
    'zh-CN': { other: "Codex Hooks 正在安装或安装中断，请核对本机安装锁后重试。" },
    'en-US': { other: "Codex hook installation is busy or interrupted. Check the local installation lock and retry." },
  },
  'error.codex.inspection_failed': {
    'zh-CN': { other: "无法检测 Codex Hooks，请核对桌面版版本和 Hooks 设置后重试。" },
    'en-US': { other: "Could not inspect Codex hooks. Check the desktop version and hook settings, then retry." },
  },
  'error.codex.invalid_request': {
    'zh-CN': { other: "Codex 启动信息无效，请从任务重新打开。" },
    'en-US': { other: "Invalid Codex launch request. Reopen it from the task." },
  },
  'error.codex.directory': {
    'zh-CN': { other: "无法访问任务工作目录，请检查目录是否存在。" },
    'en-US': { other: "Could not access the task workspace. Check that the directory exists." },
  },
  'error.codex.binding_unavailable': {
    'zh-CN': { other: "无法核对原会话绑定。请确认 TeamBuddy 正在运行、当前任务执行代次未变，且本机会话记录仍在。" },
    'en-US': { other: "Could not verify the original binding. Check the local service, task generation and local session record." },
  },
  'error.codex.task_changed': {
    'zh-CN': { other: "无法核对任务、执行代次或工作目录。请检查 TeamBuddy 服务并从任务重新打开。" },
    'en-US': { other: "Could not verify the task, generation or workspace. Check the TeamBuddy service and reopen the task." },
  },
  'error.codex.settings_failed': {
    'zh-CN': { other: '无法打开 Codex 设置。请在 Codex App 的「设置 → Hooks」中查看 TeamBuddy 回调，然后返回。' },
    'en-US': { other: 'Could not open Codex settings. Open Settings → Hooks in the Codex app to review the TeamBuddy hooks, then return.' },
  },
  'error.codex.review_failed': {
    'zh-CN': { other: "无法打开原生审阅终端。请在 Codex 中信任该项目并审阅三个 TeamBuddy Hooks，然后重新检测。" },
    'en-US': { other: "Could not open the review terminal. Trust the project and review the three TeamBuddy hooks in Codex, then recheck." },
  },
  'error.codex.open_copy_failed': {
    'zh-CN': { other: 'Codex 未能打开，启动提示词也未复制。请检查应用和剪贴板后重试。' },
    'en-US': { other: 'Could not open Codex or copy the launch prompt. Check the app and clipboard, then retry.' },
  },
  'error.codex.copy_failed': {
    'zh-CN': { other: "无法写入剪贴板，请重试。" },
    'en-US': { other: "Could not write to the clipboard. Try again." },
  },
  'error.codex.openFailed': {
    'zh-CN': { other: '无法打开 Codex，请检查应用及 codex:// 协议关联。' },
    'en-US': { other: 'Could not open Codex. Check the app and its codex:// association.' },
  },
  'error.qoderIDE.not_installed': {
    'zh-CN': { other: "未找到 Qoder IDE，请先安装 IDE（Qoder 应用不能替代 IDE）" },
    'en-US': { other: "Qoder IDE was not found. Install the IDE; the Qoder app is a separate product." },
  },
  'error.qoderIDE.backend_unavailable': {
    'zh-CN': { other: "无法准备 Qoder IDE 回调程序，请检查开发环境中的 Go 命令或桌面版服务文件后重试" },
    'en-US': { other: "Could not prepare Qoder IDE callbacks. Check Go in the development environment or the packaged service files and retry." },
  },
  'error.qoderIDE.configuration_conflict': {
    'zh-CN': { other: "Qoder Hooks 配置或安装记录无效，请检查项目 .qoder/settings.local.json 及本机安装记录后重试" },
    'en-US': { other: "Qoder Hooks configuration or installation records are invalid. Check the project .qoder/settings.local.json and local installation records, then retry." },
  },
  'error.qoderIDE.configuration_modified': {
    'zh-CN': { other: 'Qoder Hooks 受管配置已被修改或移除，为保留现有设置已停止安装，请核对项目 .qoder/settings.local.json' },
    'en-US': { other: 'Managed Qoder Hooks were modified or removed. Installation stopped to preserve your settings. Check the project .qoder/settings.local.json.' },
  },
  'error.qoderIDE.configuration_locked': {
    'zh-CN': { other: 'Qoder Hooks 安装锁已存在，请等待安装结束；若客户端曾异常退出，请核对本机 install.lock 后重试' },
    'en-US': { other: 'A Qoder Hooks installation lock exists. Wait for installation to finish; if the client exited unexpectedly, inspect the local install.lock before retrying.' },
  },
  'error.qoderIDE.prompt_too_long': {
    'zh-CN': { other: "提示词超过 Qoder 深链长度限制，请重启 Qoder IDE，在任务目录粘贴提示词并手动发送" },
    'en-US': { other: "The prompt exceeds the Qoder deeplink length limit. Restart Qoder IDE, paste the prompt in the task workspace, and send it manually." },
  },
  'error.qoderIDE.openFailed': {
    'zh-CN': { other: "无法打开 Qoder IDE，请检查安装并重试" },
    'en-US': { other: "Could not open Qoder IDE. Check the installation and retry." },
  },
  'error.qoderIDE.busy': {
    'zh-CN': { other: "Qoder IDE 正在打开，请稍候" },
    'en-US': { other: "Qoder IDE is opening. Please wait." },
  },
  'error.vibeCli.openFailed': {
    'zh-CN': { other: '无法打开 CLI，请复制提示词后手动打开' },
    'en-US': { other: 'Could not open the CLI. Copy the prompt and open it manually.' },
  },
  'notification.action.openTask': {
    'zh-CN': { other: '打开任务' },
    'en-US': { other: 'Open task' },
  },
  'notification.failure.darwin': {
    'zh-CN': {
      other:
        '系统通知未能显示。请使用签名后的 TeamBuddy.app，并在 macOS「系统设置 → 通知 → TeamBuddy」中允许通知；同时检查专注模式。',
    },
    'en-US': {
      other:
        'The system notification could not be shown. Use a signed TeamBuddy.app, allow notifications in macOS Settings → Notifications → TeamBuddy, and check Focus mode.',
    },
  },
  'notification.failure.win32': {
    'zh-CN': {
      other:
        '系统通知未能显示。请通过安装程序安装 TeamBuddy，并在 Windows「设置 → 系统 → 通知」中允许 TeamBuddy 通知；同时检查勿扰模式。',
    },
    'en-US': {
      other:
        'The system notification could not be shown. Install TeamBuddy with the installer, allow notifications in Windows Settings → System → Notifications, and check Do Not Disturb.',
    },
  },
  'notification.failure.generic': {
    'zh-CN': { other: '系统通知未能显示，请检查系统通知权限及勿扰设置。' },
    'en-US': {
      other: 'The system notification could not be shown. Check notification permissions and Do Not Disturb settings.',
    },
  },
})

function normalizeLocale(value) {
  return SUPPORTED_LOCALES.includes(value) ? value : DEFAULT_LOCALE
}

let currentLocale = DEFAULT_LOCALE

function getLocale() {
  return currentLocale
}

function setLocale(locale) {
  currentLocale = normalizeLocale(locale)
  return currentLocale
}

function selectForm(entry, params) {
  const count = params && Number.isFinite(params.count) ? params.count : null
  if (count !== null && count === 1 && typeof entry.one === 'string') return entry.one
  return entry.other
}

function interpolate(text, params) {
  if (!params) return text
  return text.replace(/\{(\w+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match
  ))
}

/**
 * 主进程翻译入口。缺词时回退中文，再回退 key 本身，
 * 保证原生界面永远不会因为漏词条而显示空白。
 * @param {string} key @param {{ count?: number }} [params]
 */
function t(key, params) {
  const message = MESSAGES[key]
  if (!message) return key
  const entry = message[currentLocale] || message[DEFAULT_LOCALE]
  if (!entry) return key
  return interpolate(selectForm(entry, params), params)
}

module.exports = {
  DEFAULT_LOCALE,
  MESSAGES,
  SUPPORTED_LOCALES,
  getLocale,
  normalizeLocale,
  setLocale,
  t,
}
