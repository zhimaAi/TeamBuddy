'use strict'

const { spawn } = require('node:child_process')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { prepareVibeMCP, withMcpChannel } = require('./vibe-mcp/index.cjs')

const MAX_PROMPT_LENGTH = 16 * 1024
const CODEX_THREAD_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const SESSION_ID_PATTERN = /^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$/

const VIBE_CLI_TOOLS = Object.freeze({
  codex_cli: Object.freeze({ executables: Object.freeze(['codex']), resume: id => ['resume', id] }),
  claude: Object.freeze({ executables: Object.freeze(['claude']), resume: id => ['--resume', id] }),
  codebuddy: Object.freeze({ executables: Object.freeze(['codebuddy', 'cbc']), resume: id => ['--resume', id] }),
  qoder: Object.freeze({ executables: Object.freeze(['qodercli']), resume: id => ['--resume', id] }),
  pi: Object.freeze({ executables: Object.freeze(['pi']), resume: id => ['--session', id] }),
  // agy 把裸参数当命令；--prompt-interactive 才会带着提示词留在 TUI。
  antigravity: Object.freeze({
    executables: Object.freeze(['agy']),
    initial: prompt => ['--prompt-interactive', prompt],
    resume: id => ['--conversation', id],
  }),
})

function shellQuote(value) {
  return `'${String(value).replace(/'/g, "'\\''")}'`
}

function powershellQuote(value) {
  return `'${String(value).replace(/'/g, "''")}'`
}

function normalizeVibeThreadID(tool, threadId) {
  if (threadId == null || threadId === '') return ''
  if (typeof threadId !== 'string') return null
  const normalized = threadId.trim()
  if (tool === 'codex_cli') {
    return CODEX_THREAD_ID_PATTERN.test(normalized) ? normalized.toLowerCase() : null
  }
  return SESSION_ID_PATTERN.test(normalized) ? normalized : null
}

function normalizePrompt(prompt) {
  if (typeof prompt !== 'string') throw new TypeError('Vibe CLI prompt must be a string')
  const normalized = prompt.trim()
  if (!normalized || normalized.length > MAX_PROMPT_LENGTH || normalized.includes('\0')) {
    throw new Error('Vibe CLI prompt is invalid')
  }
  return normalized
}

function assertExecutable(tool, execPath) {
  const spec = VIBE_CLI_TOOLS[tool]
  if (!spec) throw new Error('Vibe CLI tool is invalid')
  if (typeof execPath !== 'string' || !path.isAbsolute(execPath) || execPath.includes('\0')) {
    throw new Error('Vibe CLI executable is invalid')
  }
  const base = path.basename(execPath).replace(/\.(exe|cmd|bat)$/i, '').toLowerCase()
  if (!spec.executables.includes(base)) throw new Error('Vibe CLI executable is invalid')
  return spec
}

function buildVibeArguments(tool, execPath, prompt, threadId) {
  const spec = assertExecutable(tool, execPath)
  const sessionID = normalizeVibeThreadID(tool, threadId)
  if (sessionID == null) throw new Error('Vibe CLI session ID is invalid')
  const args = sessionID
    ? spec.resume(sessionID)
    : (typeof spec.initial === 'function' ? spec.initial(normalizePrompt(prompt)) : [normalizePrompt(prompt)])
  return { execPath, args }
}

function vibeModelArguments(tool, model) {
  if (model == null || model === '') return []
  if (typeof model !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._:/@+\-]{0,255}$/.test(model)
    || !['codex_cli', 'pi', 'antigravity', 'qoder', 'codebuddy'].includes(tool)) {
    throw new Error('Vibe CLI model is invalid')
  }
  if (tool === 'pi' && model.includes('/')) {
    const separator = model.indexOf('/')
    if (separator === model.length - 1) throw new Error('Vibe CLI model is invalid')
    return ['--provider', model.slice(0, separator), '--model', model.slice(separator + 1)]
  }
  return ['--model', model]
}

function codebuddyUpdateGuard(tool) {
  // CodeBuddy 会在交互会话里执行 npm install -g 升级自己。
  // 升级会删掉当前进程还要加载的 dist/lazy 分片，随后 Read/Bash 报找不到模块。
  return tool === 'codebuddy' ? 'DISABLE_AUTOUPDATER=1 ' : ''
}

function darwinLaunchOptions(directory, execPath, args, tool, prefixArgs = [], cleanupDir = '') {
  const command = [shellQuote(execPath), ...prefixArgs.map(shellQuote), ...args.map(shellQuote)].join(' ')
  const cleanup = cleanupDir ? ` ; rm -rf ${shellQuote(cleanupDir)}` : ''
  const script = `cd ${shellQuote(directory)} && ${codebuddyUpdateGuard(tool)}${command}${cleanup}`
  const source = [
    'on run argv',
    'tell application "Terminal"',
    'activate',
    'do script (item 1 of argv)',
    'end tell',
    'end run',
  ].join('\n')
  return {
    command: '/usr/bin/osascript',
    args: ['-e', source, script],
    options: { stdio: 'ignore' },
    waitForExit: true,
  }
}

function windowsPowerShellPath() {
  const windowsRoot = (process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows').trim()
  return path.win32.join(windowsRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe')
}

function windowsCommandProcessor() {
  const fromEnv = typeof process.env.ComSpec === 'string' ? process.env.ComSpec.trim() : ''
  if (fromEnv && path.win32.isAbsolute(fromEnv)) return fromEnv
  const windowsRoot = (process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows').trim()
  return path.win32.join(windowsRoot, 'System32', 'cmd.exe')
}

function windowsUtf8Reader(filePath) {
  // Windows PowerShell 5.1 的 Get-Content 默认按系统 ANSI 代码页解码。
  // 无 BOM 的 UTF-8 中文会被读成乱码，再作为参数交给 Qoder 等 CLI。
  return `[System.IO.File]::ReadAllText(${powershellQuote(filePath)}, (New-Object System.Text.UTF8Encoding $false))`
}

// npm 的 .cmd/.bat 垫片把 "%dp0%\...\入口" 交给 node。返回该相对路径。
// 不接受上级目录，避免垫片把启动目标带到安装目录外面。
function npmGlobalShimTarget(cmdText) {
  if (typeof cmdText !== 'string' || !cmdText) return ''
  const patterns = [
    /%dp0%\\([^"\r\n]+)/gi,
    /%~dp0%\\([^"\r\n]+)/gi,
    /%~dp0\\([^"\r\n]+)/gi,
  ]
  for (const pattern of patterns) {
    for (const match of cmdText.matchAll(pattern)) {
      const relative = match[1]
      const parts = relative.split(/[\\/]+/)
      if (parts.length === 0 || parts.some(part => part === '.' || part === '..' || !/^[@A-Za-z0-9._-]+$/.test(part))) continue
      const base = parts[parts.length - 1].toLowerCase()
      if (base === 'node.exe' || base === 'node') continue
      return relative
    }
  }
  return ''
}

async function readNpmShimTarget(execPath) {
  if (!/\.(cmd|bat)$/i.test(execPath)) return ''
  try {
    return npmGlobalShimTarget(await fs.readFile(execPath, 'utf8'))
  } catch {
    return ''
  }
}

function windowsPromptLauncherLines(execPath, promptPath, shimRelative, prefixArgs = []) {
  // cmd.exe 按行读取命令，Windows PowerShell 5.1 的调用运算符也不能把含换行的参数完整交给 .cmd。
  // 提示词第一行是 Skill 链接，后面的任务 UUID 和工具类型会被截掉，Skill 因此拒绝启用。
  const lines = [
    `$prompt = ${windowsUtf8Reader(promptPath)}`,
    `Remove-Item -LiteralPath ${powershellQuote(promptPath)} -Force`,
    'function ConvertTo-CommandLineArgument([string]$Value) {',
    '  $builder = New-Object System.Text.StringBuilder',
    '  [void]$builder.Append([char]34)',
    '  $slashes = 0',
    '  foreach ($char in $Value.ToCharArray()) {',
    '    if ($char -eq [char]92) { $slashes++; continue }',
    '    if ($char -eq [char]34) {',
    '      if ($slashes -gt 0) { [void]$builder.Append([char]92, ($slashes * 2)) }',
    '      [void]$builder.Append([char]92)',
    '      [void]$builder.Append([char]34)',
    '      $slashes = 0',
    '      continue',
    '    }',
    '    if ($slashes -gt 0) { [void]$builder.Append([char]92, $slashes); $slashes = 0 }',
    '    [void]$builder.Append($char)',
    '  }',
    '  if ($slashes -gt 0) { [void]$builder.Append([char]92, ($slashes * 2)) }',
    '  [void]$builder.Append([char]34)',
    '  return $builder.ToString()',
    '}',
    'function Start-VibeProcess([string]$FileName, [string[]]$ArgumentList) {',
    '  $quoted = foreach ($item in $ArgumentList) { ConvertTo-CommandLineArgument $item }',
    '  $start = New-Object System.Diagnostics.ProcessStartInfo',
    '  $start.FileName = $FileName',
    "  $start.Arguments = ($quoted -join ' ')",
    '  $start.UseShellExecute = $false',
    '  $start.WorkingDirectory = (Get-Location).ProviderPath',
    '  $process = [System.Diagnostics.Process]::Start($start)',
    '  if ($null -eq $process) { throw "Vibe CLI process did not start" }',
    '  $process.WaitForExit()',
    '}',
  ]
  const prefixList = prefixArgs.map(powershellQuote).join(', ')
  const withPrefix = rest => (prefixList ? `${prefixList}, ${rest}` : rest)
  const startPrompt = `Start-VibeProcess ${powershellQuote(execPath)} @(${withPrefix('$prompt')})`
  if (shimRelative) {
    const quotedShim = powershellQuote(shimRelative)
    lines.push(`$shimScript = Join-Path (Split-Path -Parent ${powershellQuote(execPath)}) ${quotedShim}`)
    lines.push(`$node = Join-Path (Split-Path -Parent ${powershellQuote(execPath)}) 'node.exe'`)
    lines.push('if (-not (Test-Path -LiteralPath $node)) {')
    lines.push('  $found = Get-Command -Name node -CommandType Application -ErrorAction SilentlyContinue')
    lines.push('  if ($null -ne $found) { $node = $found.Source } else { $node = "" }')
    lines.push('}')
    lines.push('if ((Test-Path -LiteralPath $shimScript) -and $node) {')
    lines.push(`  Start-VibeProcess $node @($shimScript, ${withPrefix('$prompt')})`)
    lines.push('} else {')
    lines.push(`  ${startPrompt}`)
    lines.push('}')
    return lines
  }
  lines.push(startPrompt)
  return lines
}

function windowsArgumentLauncherLines(execPath, args) {
  const list = args.map(powershellQuote).join(', ')
  const invocation = list ? `@(${list})` : '@()'
  return [
    'function ConvertTo-CommandLineArgument([string]$Value) {',
    '  $builder = New-Object System.Text.StringBuilder',
    '  [void]$builder.Append([char]34)',
    '  $slashes = 0',
    '  foreach ($char in $Value.ToCharArray()) {',
    '    if ($char -eq [char]92) { $slashes++; continue }',
    '    if ($char -eq [char]34) {',
    '      if ($slashes -gt 0) { [void]$builder.Append([char]92, ($slashes * 2)) }',
    '      [void]$builder.Append([char]92)',
    '      [void]$builder.Append([char]34)',
    '      $slashes = 0',
    '      continue',
    '    }',
    '    if ($slashes -gt 0) { [void]$builder.Append([char]92, $slashes); $slashes = 0 }',
    '    [void]$builder.Append($char)',
    '  }',
    '  if ($slashes -gt 0) { [void]$builder.Append([char]92, ($slashes * 2)) }',
    '  [void]$builder.Append([char]34)',
    '  return $builder.ToString()',
    '}',
    'function Start-VibeProcess([string]$FileName, [string[]]$ArgumentList) {',
    '  $quoted = foreach ($item in $ArgumentList) { ConvertTo-CommandLineArgument $item }',
    "  $start = New-Object System.Diagnostics.ProcessStartInfo",
    '  $start.FileName = $FileName',
    "  $start.Arguments = ($quoted -join ' ')",
    '  $start.UseShellExecute = $false',
    '  $start.WorkingDirectory = (Get-Location).ProviderPath',
    '  $process = [System.Diagnostics.Process]::Start($start)',
    '  if ($null -eq $process) { throw "Vibe CLI process did not start" }',
    '  $process.WaitForExit()',
    '}',
    `Start-VibeProcess ${powershellQuote(execPath)} ${invocation}`,
  ]
}

async function writeWindowsLauncher(directory, execPath, args, tool, prefixArgs = [], cleanupDir = '') {
  const stamp = `${process.pid}-${Date.now()}-${Math.random().toString(16).slice(2)}`
  const directoryPath = path.join(os.tmpdir(), `goteams-vibe-${stamp}`)
  await fs.mkdir(directoryPath, { mode: 0o700 })
  const scriptPath = path.join(directoryPath, 'launch.ps1')
  const lines = [
    "$ErrorActionPreference = 'Stop'",
    '$utf8 = New-Object System.Text.UTF8Encoding $false',
    '$OutputEncoding = $utf8',
    '[Console]::InputEncoding = $utf8',
    '[Console]::OutputEncoding = $utf8',
    '& "$env:SystemRoot\\System32\\chcp.com" 65001 | Out-Null',
    'try {',
    `Set-Location -LiteralPath ${powershellQuote(directory)}`,
  ]
  // RemoteSigned authorizes only our generated launcher. Restore the incoming
  // process policy before starting the CLI so its PowerShell children retain
  // the user's policy rather than inheriting the launcher's temporary override.
  const inheritedPolicy = process.env.PSExecutionPolicyPreference
  lines.push(inheritedPolicy == null
    ? 'Remove-Item Env:PSExecutionPolicyPreference -ErrorAction SilentlyContinue'
    : `$env:PSExecutionPolicyPreference = ${powershellQuote(inheritedPolicy)}`)
  if (tool === 'codebuddy') {
    lines.push("$env:DISABLE_AUTOUPDATER = '1'")
  }
  const interactivePrompt = args.length === 2 && args[0] === '--prompt-interactive'
  if (args.length === 1 || interactivePrompt) {
    const promptPath = path.join(directoryPath, 'prompt.txt')
    await fs.writeFile(promptPath, interactivePrompt ? args[1] : args[0], { encoding: 'utf8', mode: 0o600 })
    const basePrefix = interactivePrompt ? ['--prompt-interactive'] : []
    lines.push(...windowsPromptLauncherLines(execPath, promptPath, await readNpmShimTarget(execPath), [...prefixArgs, ...basePrefix]))
  } else {
    lines.push(...windowsArgumentLauncherLines(execPath, [...prefixArgs, ...args]))
  }
  lines.push('} finally {')
  if (cleanupDir) {
    lines.push(`  Remove-Item -LiteralPath ${powershellQuote(cleanupDir)} -Recurse -Force -ErrorAction SilentlyContinue`)
  }
  lines.push('  Remove-Item -LiteralPath $PSScriptRoot -Recurse -Force -ErrorAction SilentlyContinue', '}')
  // BOM 让 Windows PowerShell 5.1 按 UTF-8 解析脚本，中文路径才不会被当成 ANSI。
  await fs.writeFile(scriptPath, `\uFEFF${lines.join('\n')}\n`, { encoding: 'utf8', mode: 0o600 })
  return scriptPath
}

function windowsLaunchOptions(scriptPath, directory) {
  const command = windowsCommandProcessor()
  const powershell = windowsPowerShellPath()
  return {
    command,
    args: ['/D', '/C', 'start', '', powershell, '-NoProfile', '-NoExit', '-ExecutionPolicy', 'RemoteSigned', '-File', scriptPath],
    options: { cwd: directory, detached: true, stdio: 'ignore', windowsHide: false },
    waitForExit: true,
  }
}

async function vibeLaunchPlan(directory, tool, execPath, prompt, threadId, platform = process.platform, mcp = undefined, model = '') {
  const modelArgs = vibeModelArguments(tool, model)
  // 恢复已有会话时沿用该会话配置，模型只用于首次启动。
  let prefixArgs = threadId ? [] : modelArgs
  let launchPrompt = prompt
  let cleanupDir = ''
  if (mcp) {
    const prepared = await prepareVibeMCP(tool, { ...mcp, workDir: directory })
    if (prepared.channel === 'mcp') {
      prefixArgs = [...prefixArgs, ...prepared.prefixArgs]
      launchPrompt = withMcpChannel(prompt, 'mcp')
      cleanupDir = prepared.cleanupDir || ''
    }
  }
  let built
  try {
    built = buildVibeArguments(tool, execPath, launchPrompt, threadId)
  } catch (error) {
    if (cleanupDir) await fs.rm(cleanupDir, { recursive: true, force: true })
    throw error
  }
  if (platform === 'darwin') {
    return {
      launch: darwinLaunchOptions(directory, built.execPath, built.args, tool, prefixArgs, cleanupDir),
      cleanup: null,
      cleanupDir,
    }
  }
  if (platform === 'win32') {
    const scriptPath = await writeWindowsLauncher(directory, built.execPath, built.args, tool, prefixArgs, cleanupDir)
    return { launch: windowsLaunchOptions(scriptPath, directory), cleanup: scriptPath, cleanupDir }
  }
  if (cleanupDir) await fs.rm(cleanupDir, { recursive: true, force: true })
  throw new Error('Unsupported desktop platform')
}

async function openVibeCli(directory, tool, execPath, prompt, threadId, platform = process.platform, spawnProcess = spawn, mcp = undefined, model = '') {
  const plan = await vibeLaunchPlan(directory, tool, execPath, prompt, threadId, platform, mcp, model)
  await new Promise((resolve, reject) => {
    const options = /** @type {import('node:child_process').SpawnOptions} */ (plan.launch.options)
    const child = spawnProcess(plan.launch.command, plan.launch.args, options)
    let settled = false
    const timer = setTimeout(() => {
      // The terminal may already have accepted the script. Leave its files for
      // the script's finally block; deleting them here can break a slow launch.
      settled = true
      child.kill()
      reject(new Error('Timed out waiting for the Vibe CLI launcher'))
    }, 30000)
    const finish = (error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (error) {
        if (plan.cleanup) fs.rm(path.dirname(plan.cleanup), { recursive: true, force: true }).catch(() => {})
        if (plan.cleanupDir) fs.rm(plan.cleanupDir, { recursive: true, force: true }).catch(() => {})
        reject(error)
      }
      else resolve(undefined)
    }
    child.once('error', finish)
    if (plan.launch.waitForExit) {
      child.once('exit', code => finish(code === 0 ? null : new Error('Vibe CLI launcher failed')))
    } else {
      child.once('spawn', () => { child.unref(); finish(null) })
    }
  })
}

module.exports = {
  MAX_PROMPT_LENGTH,
  VIBE_CLI_TOOLS,
  buildVibeArguments,
  darwinLaunchOptions,
  npmGlobalShimTarget,
  normalizeVibeThreadID,
  openVibeCli,
  vibeLaunchPlan,
  vibeModelArguments,
  windowsLaunchOptions,
}
