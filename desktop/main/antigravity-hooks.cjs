'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { execFile } = require('node:child_process')
const { antigravityHookCommand } = require('./codex-app.cjs')
const { prepareCommandHooks } = require('./managed-command-hooks.cjs')
const { readRegular } = require('./qoder-hook-store.cjs')


const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

async function readAgyConfig(file) {
  const raw = await readRegular(file, null)
  if (raw === null) return {}
  let value
  try { value = JSON.parse(raw) } catch { throw new Error('configuration_conflict') }
  if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error('configuration_conflict')
  return value
}

async function ensureAntigravityHooks({ home, workDir, stateRoot, backend, consent, app = false }) {
  const name = app ? 'teambuddy-antigravity-app' : 'teambuddy-antigravity-cli'
  const events = app ? ['PreInvocation', 'Stop'] : ['SessionStart', 'PreInvocation', 'Stop']
  const dir = path.join(home, '.gemini', 'config')
  for (const parent of [path.join(home, '.gemini'), dir]) {
    const info = await fs.lstat(parent).catch(error => {
      if (error.code === 'ENOENT') return null
      throw error
    })
    if (info && !info.isDirectory()) throw new Error('configuration_conflict')
  }
  const workspace = await readAgyConfig(path.join(workDir, '.agents', 'hooks.json'))
  if (Object.hasOwn(workspace, name)) throw new Error('configuration_conflict')
  const command = antigravityHookCommand(backend, process.platform, app)
  const group = Object.fromEntries(events.map(event => [event, [{ type: 'command', command, timeout: 8 }]]))
  const current = (await readAgyConfig(path.join(dir, 'hooks.json')))[name]
  if (current?.enabled === false) throw new Error('hooks_disabled')
  if (current !== undefined) {
    if (Object.keys(current ?? {}).some(k => k !== 'enabled' && !events.includes(k)) ||
        !events.every(event => JSON.stringify(current?.[event]) === JSON.stringify(group[event]))) throw new Error('configuration_conflict')
    return true
  }
  if (!await consent()) return false
  await prepareCommandHooks({ workDir: dir, stateRoot, namespace: 'antigravity-hooks', directory: '.', filename: 'hooks.json', group, namedGroup: name, commandName: 'antigravity-hook' })
  return true
}

function prepareAntigravityLaunch(backend, directory, native, threadId, signal, check = false) {
  const { taskUuid, executionIndex, instruction } = native
  if (typeof taskUuid !== 'string' || !UUID.test(taskUuid) || !Number.isSafeInteger(executionIndex) || executionIndex < 0 ||
      (threadId && !UUID.test(threadId)) || typeof instruction !== 'string' || !instruction.trim() || instruction.includes('\0') ||
      Buffer.byteLength(instruction) > 16 * 1024) throw new Error('invalid_request')
  const args = ['antigravity-hook', 'prepare', '--task', taskUuid, '--execution-index', String(executionIndex), '--work-dir', directory]
  if (check) args.push('--check')
  if (threadId) args.push('--thread', threadId)
  return new Promise((resolve, reject) => {
    const child = execFile(backend, args, { cwd: directory, timeout: 10000, maxBuffer: 64 * 1024, windowsHide: true, signal }, (error, stdout) => {
      if (error) { reject(new Error('binding_unavailable')); return }
      try {
        const { ticket, runtime } = JSON.parse(stdout)
        if ((!check && (typeof ticket !== 'string' || !UUID.test(ticket))) || typeof runtime !== 'string' || !path.isAbsolute(runtime)) throw new Error()
        resolve({ ticket, runtime })
      } catch { reject(new Error('binding_unavailable')) }
    })
    child.stdin?.on('error', () => {})
    child.stdin?.end(instruction)
  })
}

async function confirmAntigravityVersion(file, signal, minimumPatch = 14) {
  const text = await new Promise((resolve, reject) => {
    execFile(file, ['--version'], { timeout: 5000, killSignal: 'SIGKILL', maxBuffer: 64 * 1024, windowsHide: true, signal }, (err, stdout) => {
      if (err) reject(new Error('version_unsupported'))
      else resolve(stdout)
    })
  })
  const m = /^\s*(\d+)\.(\d+)\.(\d+)\s*$/.exec(text)
  if (!m || !(+m[1] > 1 || +m[1] === 1 && (+m[2] > 5 || +m[2] === 5 && +m[3] >= minimumPatch))) throw new Error('version_unsupported')
}

module.exports = { ensureAntigravityHooks, prepareAntigravityLaunch, confirmAntigravityVersion }
