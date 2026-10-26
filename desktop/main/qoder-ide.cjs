'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const os = require('node:os')
const { pathToFileURL } = require('node:url')
const { promisify } = require('node:util')
const { execFile, spawn } = require('node:child_process')
const runFile = promisify(execFile)
const MAX_URL_LENGTH = 8000
const SESSION_PATTERN = /^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,254}[A-Za-z0-9])?$/

// 只读检测；不替用户退出 IDE，避免中断其他窗口或未保存的工作。
/** @param {string} appPath @param {string} [platform] @param {(file: string, args: string[], options: object) => Promise<{stdout: string | Buffer}>} [run] @param {AbortSignal} [signal] */
async function isQoderIDERunning(appPath, platform = process.platform, run = runFile, signal = undefined) {
  const options = { timeout: 3000, maxBuffer: 1024 * 1024, signal }
  if (platform === 'win32') {
    const executable = path.win32.basename(appPath)
    const systemRoot = process.env.SystemRoot || 'C:\\Windows'
    const { stdout } = await run(path.win32.join(systemRoot, 'System32', 'tasklist.exe'), ['/FI', `IMAGENAME eq ${executable}`, '/FO', 'CSV', '/NH'], options)
    // 同名进程也保守视为运行中，避免误判后在旧进程里执行未加载 Hooks 的聊天。
    return String(stdout).split(/\r?\n/).some(line => line.toLowerCase().startsWith(`"${executable.toLowerCase()}",`))
  }
  const { stdout } = await run('/bin/ps', ['-ax', '-o', 'comm='], options)
  const prefix = platform === 'darwin' ? `${appPath}/Contents/MacOS/` : appPath
  return String(stdout).split('\n').some(line => platform === 'darwin' ? line.trim().startsWith(prefix) : line.trim() === prefix)
}

function validateQoderPayload(payload) {
  if (!payload || payload.tool !== 'qoder_ide' || typeof payload.directoryPath !== 'string'
      || !path.isAbsolute(payload.directoryPath) || payload.directoryPath.includes('\0')
      || typeof payload.prompt !== 'string' || !payload.prompt.trim() || payload.prompt.length > 65536
      || payload.prompt.includes('\0')
      || (payload.threadId !== undefined && !SESSION_PATTERN.test(payload.threadId))) {
    throw new Error('invalid_payload')
  }
  if (Object.keys(payload).some(key => !['tool', 'directoryPath', 'prompt', 'threadId'].includes(key))) {
    throw new Error('invalid_payload')
  }
}

/** @param {string} prompt @param {string} [platform] */
function buildQoderChatURL(prompt, platform = process.platform) {
  // macOS 和 Windows 使用 IDE 专属协议；即便直接调用 IDE 程序，
  // 1.33 的共存逻辑仍会将共享 qoder:// 链接转交给 Qoder 应用。
  const scheme = platform === 'darwin' || platform === 'win32' ? 'qoder-ide' : 'qoder'
  const url = new URL(`${scheme}://aicoding.aicoding-deeplink/chat`)
  url.searchParams.set('text', prompt)
  url.searchParams.set('mode', 'agent')
  url.searchParams.set('isNewChat', 'true')
  if (url.toString().length > MAX_URL_LENGTH) throw new Error('prompt_too_long')
  return url.toString()
}

// 验证 IDE 的产品标识，避免误启动同名 Qoder 桌面应用。
async function isQoderIDE(candidate, platform) {
  const productPath = platform === 'darwin'
    ? path.join(candidate, 'Contents', 'Resources', 'app', 'product.json')
    : path.join(path.dirname(candidate), 'resources', 'app', 'product.json')
  try {
    const info = await fs.stat(candidate)
    if (platform === 'darwin' ? !info.isDirectory() : !info.isFile()) return false
    const product = JSON.parse(await fs.readFile(productPath, 'utf8'))
    return product.urlProtocol === 'qoder' && product.darwinBundleIdentifier === 'com.qoder.ide'
  } catch { return false }
}

/** @param {string} [platform] @param {string} [protocolPath] @param {(file: string, args: string[], options: object) => Promise<{stdout: string | Buffer}>} [run] @param {string} [home] @param {NodeJS.ProcessEnv} [env] */
async function findQoderIDE(platform = process.platform, protocolPath = '', run = runFile, home = os.homedir(), env = process.env) {
  const candidates = []
  if (platform === 'darwin') {
    candidates.push('/Applications/Qoder IDE.app', '/Applications/Qoder.app', path.join(home, 'Applications', 'Qoder IDE.app'), path.join(home, 'Applications', 'Qoder.app'))
  } else if (platform === 'win32') {
    // 协议可能未注册或被同名应用接管；仅探测固定安装目录，并复验产品标识。
    const localAppData = env.LOCALAPPDATA || path.join(home, 'AppData', 'Local')
    const roots = [path.join(localAppData, 'Programs'), env.ProgramFiles, env['ProgramFiles(x86)']]
    for (const root of roots) {
      if (!root || !path.isAbsolute(root)) continue
      for (const name of ['Qoder IDE', 'Qoder']) {
        // IDE 产品配置声明 win32ExecutableName 为 Qoder IDE；兼容旧版名称。
        for (const executable of ['Qoder IDE.exe', 'Qoder.exe']) candidates.push(path.join(root, name, executable))
      }
    }
  }
  if (protocolPath && path.isAbsolute(protocolPath)) candidates.unshift(protocolPath)
  for (const candidate of [...new Set(candidates)]) {
    if (await isQoderIDE(candidate, platform)) return candidate
  }
  if (platform === 'darwin') {
    try {
      const result = await run('/usr/bin/mdfind', ['kMDItemCFBundleIdentifier == "com.qoder.ide"'], { timeout: 3000, maxBuffer: 64 * 1024 })
      for (const candidate of String(result.stdout).split('\n').filter(value => path.isAbsolute(value))) {
        if (await isQoderIDE(candidate, platform)) return candidate
      }
    } catch { /* 固定安装目录仍可用。 */ }
  }
  throw new Error('not_installed')
}

/** @param {{tool: string, directoryPath: string, prompt: string, threadId?: string}} payload @param {string} appPath @param {string} [platform] @param {(file: string, args: string[], options: object) => Promise<unknown>} [run] @param {typeof spawn} [spawnProcess] @param {AbortSignal} [signal] @param {(event: string, details?: object) => void} [trace] */
async function openQoderIDE(payload, appPath, platform = process.platform, run = runFile, spawnProcess = spawn, signal = undefined, trace = () => {}) {
  validateQoderPayload(payload)
  if (!await isQoderIDE(appPath, platform)) throw new Error('not_installed')
  const directory = await fs.realpath(payload.directoryPath)
  if (!(await fs.stat(directory)).isDirectory()) throw new Error('invalid_directory')
  const options = { timeout: 10000, maxBuffer: 64 * 1024, signal }
  // Main 已完成接入检查；未绑定任务优先预填，剪贴板仅作为交付失败的兜底。
  let chatURL
  let promptDelivery = payload.threadId ? 'none' : 'deeplink'
  if (promptDelivery === 'deeplink') {
    try { chatURL = buildQoderChatURL(payload.prompt, platform) }
    catch (error) {
      if (error.message !== 'prompt_too_long') throw error
      promptDelivery = 'clipboard'
    }
  }
  trace('delivery.selected', { promptDelivery, directory })
  if (signal?.aborted) throw new Error('cancelled')
  // 参数以 argv 传递，不拼接 shell。启动请求完成不代表会话已绑定。
  if (platform === 'darwin') {
    trace('launch.requested', { appPath, directory, kind: 'macos-open', chatRequested: Boolean(chatURL) })
    await run('/usr/bin/open', ['-a', appPath, directory], options)
    if (chatURL) {
      try { await run('/usr/bin/open', ['-a', appPath, chatURL], options) }
      catch (error) {
        if (signal?.aborted) throw error
        promptDelivery = 'clipboard'
      }
    }
  } else {
    // 与 macOS 一样先请求目录，再请求聊天预填；窗口路由结果由实机日志继续定位。
    const launch = async (args) => {
      if (signal?.aborted) throw new Error('cancelled')
      await new Promise((resolve, reject) => {
        // 聊天 URL 内含提示词，只记录类型；目录参数不包含提示词。
        trace('launch.requested', { appPath, args: args[0] === '--open-url' ? ['--open-url', '[redacted]'] : args, cwd: process.cwd(), detached: true, shell: false })
        const child = spawnProcess(appPath, args, { detached: true, stdio: 'ignore', shell: false })
        const timer = setTimeout(() => finish(new Error('launch_timeout')), 10000)
        const cancelled = () => finish(new Error('cancelled'))
        let settled = false
        function finish(error = undefined) {
          if (settled) return
          settled = true
          clearTimeout(timer)
          signal?.removeEventListener('abort', cancelled)
          if (error) trace('launch.failed', { code: error.code, reason: ['launch_timeout', 'cancelled'].includes(error.message) ? error.message : 'process_error' })
          error ? reject(error) : resolve(undefined)
        }
        signal?.addEventListener('abort', cancelled, { once: true })
        child.once('error', finish)
        child.once('spawn', () => {
          trace('process.spawned', { pid: child.pid })
          // 最多观察 15 秒退出状态，不采集 stdout/stderr，避免第三方输出泄露提示词。
          // 退出码 0 也可能仅表示转交给已有实例，不能证明工作区已打开。
          const exited = (code, exitSignal) => { trace('process.exited', { pid: child.pid, code, signal: exitSignal }); cleanup() }
          const cleanup = () => {
            clearTimeout(observation)
            child.removeListener('exit', exited)
            signal?.removeEventListener('abort', cleanup)
          }
          const observation = setTimeout(() => { trace('process.observation_ended', { pid: child.pid }); cleanup() }, 15000)
          observation.unref()
          child.once('exit', exited)
          signal?.addEventListener('abort', cleanup, { once: true })
          if (signal?.aborted) cleanup()
          child.unref(); finish()
        })
      })
    }
    // Qoder IDE 1.33 支持显式文件夹 URI，绕过普通位置参数的路径解析。
    const directoryArgs = platform === 'win32'
      ? ['--folder-uri', pathToFileURL(directory, { windows: true }).href]
      : [directory]
    await launch(directoryArgs)
    if (chatURL) {
      try { await launch(['--open-url', chatURL]) }
      catch (error) {
        if (signal?.aborted) throw error
        promptDelivery = 'clipboard'
      }
    }
  }
  return { opened: true, sessionLocated: false, promptDelivery, promptCopied: false, requiresManualStart: !payload.threadId, requiresManualSessionSelection: Boolean(payload.threadId) }
}

module.exports = { validateQoderPayload, buildQoderChatURL, findQoderIDE, isQoderIDE, isQoderIDERunning, openQoderIDE }
