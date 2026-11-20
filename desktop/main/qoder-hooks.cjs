'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { prepareCommandHooks } = require('./managed-command-hooks.cjs')

function hookCommand(backendPath, platform) {
  if (!path.isAbsolute(backendPath) || /[\0\r\n]/.test(backendPath)) throw new Error('backend_unavailable')
  if (platform === 'win32') {
    if (/["%&|<>^]/.test(backendPath)) throw new Error('backend_unavailable')
    return `"${backendPath}" qoder-hook`
  }
  return `'${backendPath.replace(/'/g, "'\\''")}' qoder-hook`
}

async function prepareQoderHooks(workDir, backendPath, platform = process.platform, stateRoot = undefined) {
  const command = hookCommand(backendPath, platform)
  if (!(await fs.stat(backendPath)).isFile()) throw new Error('backend_unavailable')
  return prepareCommandHooks({ workDir, stateRoot, namespace: 'qoder-hooks', directory: '.qoder', filename: 'settings.local.json',
    group: { hooks: [{ type: 'command', command, timeout: 10 }] }, commandName: 'qoder-hook', restart: true })
}

module.exports = { prepareQoderHooks, hookCommand }
