'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { promisify } = require('node:util')
const { execFile } = require('node:child_process')
const { prepareCommandHooks } = require('./managed-command-hooks.cjs')
const { readRegular, digest } = require('./qoder-hook-store.cjs')
const { darwinLaunchOptions } = require('./vibe-cli.cjs')
const { terminalLaunchOptions, windowsPowerShellPath } = require('./terminal-launcher.cjs')
const runFile = promisify(execFile)
const EVENTS = ['sessionStart', 'userPromptSubmit', 'stop']
const CODEX_THREAD_URL = 'codex://threads/new'

// MSIX 可以用 DelegateExecute 接收链接，导致 Electron 查不到 exe 路径。
// 查询当前生效的 ProgID，再核对应用身份，避免把链接交给同名或其他处理程序。
const WINDOWS_CODEX_PACKAGE_QUERY = String.raw`
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public static class CodexProtocolAssociation {
    [DllImport("shlwapi.dll", EntryPoint = "AssocQueryStringW", CharSet = CharSet.Unicode, ExactSpelling = true)]
    public static extern int Query(uint flags, uint kind, string scheme, IntPtr extra, StringBuilder result, ref uint length);
}
'@
$progId = New-Object System.Text.StringBuilder 1024
$length = [uint32]$progId.Capacity
# ASSOCF_IS_PROTOCOL (0x1000), ASSOCSTR_PROGID (20)
if ([CodexProtocolAssociation]::Query(0x1000, 20, 'codex', [IntPtr]::Zero, $progId, [ref]$length) -ne 0) { return }
$association = $progId.ToString()
if ($association -notmatch '^AppX[a-z0-9]+$') { return }
$family = 'OpenAI.Codex_2p2nqsd0c76g0'
$application = Get-ItemProperty -LiteralPath ('Registry::HKEY_CLASSES_ROOT\' + $association + '\Application') -ErrorAction SilentlyContinue
if ($application.AppUserModelID -ne ($family + '!App')) { return }
(Get-AppxPackage -Name OpenAI.Codex | Where-Object { $_.PackageFamilyName -eq $family } | Sort-Object { try { [version]$_.Version } catch { [version]'0.0.0.0' } } -Descending | Select-Object -First 1).InstallLocation
`

/** @param {Electron.App} app @param {AbortSignal} [signal] */
async function findCodexAppRuntime(app, signal) {
  const platform = process.platform
  if (platform !== 'win32' && platform !== 'darwin') throw new Error('runtime_unavailable')
  const application = await app.getApplicationInfoForProtocol(CODEX_THREAD_URL).catch(() => null)
  signal?.throwIfAborted()
  const native = platform === 'win32' ? path.win32 : path.posix
  const applicationPath = application?.path
  const absolute = typeof applicationPath === 'string' && native.isAbsolute(applicationPath) && !/[\0\r\n]/.test(applicationPath)
  let base = ''
  if (absolute) {
    if (platform === 'darwin') base = native.join(applicationPath, 'Contents', 'Resources')
    else if (/^codex\.exe$/i.test(native.basename(applicationPath))) base = native.join(native.dirname(applicationPath), 'resources')
  }
  const locate = async base => {
    const candidates = platform === 'darwin'
      ? [native.join(base, 'codex-cli', 'CodexCLI.app', 'Contents', 'MacOS', 'codex'), native.join(base, 'codex')]
      : [native.join(base, 'codex.exe'), native.join(base, 'codex-cli', 'codex.exe'), native.join(base, 'codex-cli', 'bin', 'codex.exe')]
    for (const candidate of candidates) {
      try { if ((await fs.stat(candidate)).isFile()) return await fs.realpath(candidate) } catch { /* Try the next layout. */ }
    }
    return ''
  }
  if (base) {
    const file = await locate(base)
    if (file) return file
  }
  if (platform === 'win32') {
    const query = Buffer.from(WINDOWS_CODEX_PACKAGE_QUERY, 'utf16le').toString('base64')
    const result = await runFile(windowsPowerShellPath(), ['-NoLogo', '-NoProfile', '-NonInteractive', '-EncodedCommand', query],
      { timeout: 10000, maxBuffer: 32 * 1024, windowsHide: true, signal }).catch(() => null)
    signal?.throwIfAborted()
    if (!result) throw new Error('runtime_unavailable')
    const install = String(result.stdout || '').trim()
    if (native.isAbsolute(install) && !/[\0\r\n]/.test(install)) {
      const file = await locate(native.join(install, 'app', 'resources'))
      if (file) return file
      throw new Error('runtime_unavailable')
    }
  }
  throw new Error(absolute || application?.name ? 'runtime_unavailable' : 'not_installed')
}

function codexHookCommand(backend, platform = process.platform, user = false) {
  const args = user ? 'codex-hook user' : 'codex-hook'
  return nativeHookCommand(backend, platform, args)
}

function antigravityHookCommand(backend, platform = process.platform, app = false) {
  return nativeHookCommand(backend, platform, app ? 'antigravity-hook app' : 'antigravity-hook')
}

function nativeHookCommand(backend, platform, args) {
  const native = platform === 'win32' ? path.win32 : path.posix
  if (!native.isAbsolute(backend) || /[\0\r\n]/.test(backend)) throw new Error('backend_unavailable')
  if (platform !== 'win32') return `'${backend.replace(/'/g, "'\\''")}' ${args}`
  // The app can supply PowerShell as the session shell; cmd /C is the
  // runner fallback. A quoted executable is not a command in PowerShell.
  if (/^[A-Z]:\\[\p{L}\p{N}_.\\-]+$/iu.test(backend)) return `${backend} ${args}`
  const powershell = windowsPowerShellPath()
  if (!/^[A-Z]:\\[\w.\\-]+$/i.test(powershell)) throw new Error('backend_unavailable')
  // Keep all user-controlled path bytes out of both parsers and relay the
  // original streams without decoding them through PowerShell's ANSI pipes.
  const script = [
    '$p = New-Object System.Diagnostics.Process',
    `$p.StartInfo.FileName = '${backend.replace(/'/g, "''")}'`,
    `$p.StartInfo.Arguments = '${args}'`,
    '$p.StartInfo.UseShellExecute = $false; $p.StartInfo.CreateNoWindow = $true',
    '$p.StartInfo.RedirectStandardInput = $true; $p.StartInfo.RedirectStandardOutput = $true; $p.StartInfo.RedirectStandardError = $true',
    'if (!$p.Start()) { exit 1 }',
    '$out = $p.StandardOutput.BaseStream.CopyToAsync([Console]::OpenStandardOutput())',
    '$err = $p.StandardError.BaseStream.CopyToAsync([Console]::OpenStandardError())',
    '[Console]::OpenStandardInput().CopyTo($p.StandardInput.BaseStream); $p.StandardInput.BaseStream.Close()',
    '$out.GetAwaiter().GetResult(); $err.GetAwaiter().GetResult(); $p.WaitForExit(); exit $p.ExitCode',
  ].join('\n')
  return `${powershell} -NoLogo -NoProfile -NonInteractive -EncodedCommand ${Buffer.from(script, 'utf16le').toString('base64')}`
}

async function prepareCodexHooks(directory, backend, platform, stateRoot) {
  if (!(await fs.stat(backend).catch(() => null))?.isFile()) throw new Error('backend_unavailable')
  const command = codexHookCommand(backend, platform)
  const handler = { type: 'command', command, timeout: 10 }
  if (platform === 'win32') handler['commandWindows'] = command
  await prepareCommandHooks({ workDir: directory, stateRoot, namespace: 'codex-hooks', directory: '.codex', filename: 'hooks.json',
    group: { hooks: [handler] }, commandName: 'codex-hook' })
  // Establish the project config layer only when missing. Existing settings and
  // explicit feature choices remain user-owned and are inspected by Codex.
  const config = path.join(directory, '.codex', 'config.toml')
  if (await readRegular(config, null) === null) {
    await fs.writeFile(config, '# TeamBuddy project hooks are configured in hooks.json.\n', { flag: 'wx', mode: 0o600 })
      .catch(error => { if (error.code !== 'EEXIST') throw error })
  }
  return command
}

function userHookHome() {
  const home = process.env.CODEX_HOME || path.join(require('node:os').homedir(), '.codex')
  if (!path.isAbsolute(home) || /[\0\r\n]/.test(home)) throw new Error('configuration_conflict')
  return path.normalize(home)
}
async function prepareUserCodexHooks(backend, platform, stateRoot) {
  if (!(await fs.stat(backend)).isFile()) throw new Error('backend_unavailable')
  const home = userHookHome()
  const command = codexHookCommand(backend, platform, true)
  const handler = { type: 'command', command, timeout: 8 }
  if (platform === 'win32') handler['commandWindows'] = command
  await prepareCommandHooks({ workDir: home, directory: '.', filename: 'hooks.json', stateRoot, namespace: 'codex-user-hooks', group: { hooks: [handler] }, commandName: 'codex-hook' })
  return command
}

function classifyCodexHooks(inspection, directory, command, platform = process.platform, user = false) {
  if (inspection?.project_disabled) return 'project_untrusted'
  if (inspection?.hooks_disabled) return 'hooks_disabled'
  const native = platform === 'win32' ? path.win32 : path.posix
  const equal = (a, b) => typeof a === 'string' && (platform === 'win32'
    ? native.normalize(a).toLowerCase() === native.normalize(b).toLowerCase() : native.normalize(a) === native.normalize(b))
  if (!inspection?.discovery_ok || !equal(inspection.cwd, directory) || !Array.isArray(inspection.hooks)) return 'inspection_failed'
  const hash = digest(command)
  const ours = inspection.hooks.filter(h => h.commandHash === hash && EVENTS.includes(h.eventName))
  if (user && !ours.length) return 'not_configured'
  if (EVENTS.some(event => ours.filter(h => h.eventName === event).length !== 1)) return 'inspection_failed'
  if (ours.some(h => !h.enabled)) return 'hooks_disabled'
  if (ours.some(h => h.trustStatus !== 'trusted')) return 'hooks_untrusted'
  return 'ready'
}

/** @param {string} backend @param {string[]} args @param {AbortSignal} signal @param {typeof runFile} [run] */
async function callCodexCallback(backend, args, signal, run = runFile) {
  const result = await run(backend, ['codex-hook', ...args], { timeout: 10000, maxBuffer: 2 * 1024 * 1024, windowsHide: true, signal })
  return JSON.parse(String(result.stdout))
}

// Open the matching CLI for native /hooks review. Only the user can grant trust.
async function openCodexHookReview(runtime, directory, platform, signal, codexHome = '') {
  if (typeof runtime !== 'string' || !path.isAbsolute(runtime) || /[\0\r\n]/.test(runtime)) throw new Error('runtime_unavailable')
  let launch
  if (platform === 'darwin') {
    launch = darwinLaunchOptions(directory, runtime, ['--no-alt-screen', '-C', directory], 'codex', [], '', codexHome)
  } else if (platform === 'win32') {
    launch = terminalLaunchOptions(directory, platform)
    // cmd can replace a UNC cwd when it starts the console; always pass -C.
    const quote = value => "'"+value.replace(/'/g, "''")+"'"
    const home = codexHome ? `$env:CODEX_HOME = ${quote(codexHome)}; ` : ''
    const script = `${home}& ${quote(runtime)} --no-alt-screen -C ${quote(directory)}`
    launch.args.push('-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64'))
  } else throw new Error('runtime_unavailable')
  await runFile(launch.command, launch.args, { cwd: directory, timeout: 10000, maxBuffer: 64 * 1024, signal })
}

module.exports = { antigravityHookCommand, userHookHome, prepareUserCodexHooks, findCodexAppRuntime, codexHookCommand, prepareCodexHooks, classifyCodexHooks, callCodexCallback, openCodexHookReview }
