'use strict'

const { spawn } = require('node:child_process')
const path = require('node:path')

/**
 * 解析 Windows 命令处理器（cmd.exe）的可执行文件路径。
 *
 * Electron 由桌面快捷方式启动时，进程 PATH 可能不包含 System32，仅传 `cmd.exe`
 * 会直接 spawn ENOENT；因此优先使用 Windows 自己导出的 ComSpec 绝对路径，
 * 缺失或不是绝对路径时按 SystemRoot 兜底拼装。
 *
 * @returns {string}
 */
function windowsCommandProcessor() {
  const fromEnv = typeof process.env.ComSpec === 'string' ? process.env.ComSpec.trim() : ''
  if (fromEnv && path.win32.isAbsolute(fromEnv)) return fromEnv
  const windowsRoot = (process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows').trim()
  return path.win32.join(windowsRoot, 'System32', 'cmd.exe')
}

// Windows PowerShell 5.1 随系统提供。不依赖可选的 PowerShell 7（pwsh）。
function windowsPowerShellPath() {
  const windowsRoot = (process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows').trim()
  return path.win32.join(windowsRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe')
}

/**
 * @param {string} directory
 * @param {NodeJS.Platform} platform
 * @returns {{ command: string, args: string[], options: import('node:child_process').SpawnOptions, waitForExit: boolean }}
 */
function terminalLaunchOptions(directory, platform = process.platform) {
  if (platform === 'darwin') {
    return { command: '/usr/bin/open', args: ['-a', 'Terminal', directory], options: { stdio: 'ignore' }, waitForExit: true }
  }
  if (platform === 'win32') {
    const command = windowsCommandProcessor()
    const powershell = windowsPowerShellPath()
    // 通过 cwd 定位目录，不把目录拼入命令，路径中的 %、& 等字符不会被 shell 解释。
    return {
      command,
      // Electron 是 GUI 进程，自身没有控制台；直接 spawn PowerShell 时，新控制台由
      // 系统控制台宿主按隐藏句柄转交，可能拿不到可见窗口。外层仍用 cmd start 创建
      // 可见窗口，内层打开 Windows PowerShell 并保持常驻。
      args: ['/D', '/C', 'start', '', powershell, '-NoLogo', '-NoExit', '-NoProfile'],
      options: {
        cwd: directory,
        detached: true,
        stdio: 'ignore',
        // 显式保留控制台窗口，不做任何隐藏。
        windowsHide: false,
      },
      // start 不等待被启动的程序，外层 cmd /C 会立刻退出；等待它可以拿到
      // 退出码，把「启动失败」如实反馈到界面，而不是静默无响应。
      waitForExit: true,
    }
  }
  throw new Error('Unsupported desktop platform')
}

/**
 * @param {string} directory
 * @param {NodeJS.Platform} platform
 * @param {(command: string, args: string[], options: import('node:child_process').SpawnOptions) => import('node:events').EventEmitter & { unref: () => void }} spawnProcess
 */
async function openTerminalWindow(directory, platform = process.platform, spawnProcess = spawn) {
  const launch = terminalLaunchOptions(directory, platform)
  await new Promise((resolve, reject) => {
    const child = spawnProcess(launch.command, launch.args, launch.options)
    child.once('error', reject)
    if (launch.waitForExit) {
      child.once('exit', code => code === 0 ? resolve(undefined) : reject(new Error('Terminal launcher failed')))
    } else {
      child.once('spawn', () => { child.unref(); resolve(undefined) })
    }
  })
}

module.exports = { terminalLaunchOptions, openTerminalWindow, windowsPowerShellPath }
