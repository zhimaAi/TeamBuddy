'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

const { SERVER_NAME, SERVER_NAMES } = require('./common.cjs')

const CHANNEL_MCP = '回传通道：mcp'

function tomlBasicString(value) {
  return `"${String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

// 新旧 server 名都算「用户已配置」：旧名是重命名前注入过的，漏检会导致重复注入。
function configNamesServer(text) {
  if (!text) return false
  for (const name of SERVER_NAMES) {
    if (new RegExp(`^\\s*\\[mcp_servers\\.${name}(?:\\.|\\])`, 'm').test(text)) return true
    if (new RegExp(`^\\s*mcp_servers\\.${name}(?:\\.|\\s*=)`, 'm').test(text)) return true
  }
  return text.split(/^\s*\[/m).some(block => {
    if (!block.startsWith('mcp_servers]')) return false
    const body = block.slice(block.indexOf(']') + 1)
    return SERVER_NAMES.some(name => new RegExp(`^\\s*${name}\\s*=`, 'm').test(body))
  })
}

function mcpBackendPath(command) {
  return typeof command === 'string' && path.isAbsolute(command) ? command : ''
}

async function readConfig(file) {
  try {
    const data = await fs.readFile(file)
    if (data.length > 1024 * 1024) return null
    return data.toString('utf8')
  } catch {
    return ''
  }
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string, codexHome?: string }} [options] */
async function prepareCodexCLI({ backendPath, workDir, homeDir = os.homedir(), codexHome } = {}) {
  const home = codexHome || path.join(homeDir, '.codex')
  const files = [path.join(home, 'config.toml')]
  if (typeof workDir === 'string' && workDir !== '') {
    files.push(path.join(workDir, '.codex', 'config.toml'))
  }
  for (const file of files) {
    const text = await readConfig(file)
    if (text == null || configNamesServer(text)) {
      return { channel: 'script', prefixArgs: [] }
    }
  }
  const command = mcpBackendPath(backendPath)
  if (command === '') return { channel: 'script', prefixArgs: [] }
  return {
    channel: 'mcp',
    prefixArgs: [
      '-c', `mcp_servers.${SERVER_NAME}.command=${tomlBasicString(command)}`,
      '-c', `mcp_servers.${SERVER_NAME}.args=["mcp"]`,
    ],
  }
}

function withCodexChannel(prompt, channel) {
  if (channel !== 'mcp' || String(prompt).includes('回传通道：')) return prompt
  return `${prompt}\n${CHANNEL_MCP}`
}

module.exports = {
  prepareCodexCLI,
  withCodexChannel,
  mcpBackendPath,
  configNamesServer,
}
