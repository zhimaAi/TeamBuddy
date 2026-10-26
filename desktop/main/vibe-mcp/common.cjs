'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')

const CHANNEL_MCP = '回传通道：mcp'
const SERVER_NAME = 'teambuddy'
// 旧版本注入过的 server 名。仍然按「已配置」处理，漏检会在用户配置里重复注入一份。
const LEGACY_SERVER_NAMES = ['teamsboard']
const SERVER_NAMES = [SERVER_NAME, ...LEGACY_SERVER_NAMES]

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

function serverMapHas(map, name) {
  return Boolean(map) && typeof map === 'object' && !Array.isArray(map)
    && Object.prototype.hasOwnProperty.call(map, name)
}

function serverMapHasAny(map, names) {
  return names.some(name => serverMapHas(map, name))
}

function projectEntry(projects, workDir) {
  if (!projects || typeof projects !== 'object' || !workDir) return null
  if (Object.prototype.hasOwnProperty.call(projects, workDir)) return projects[workDir]
  let wanted = ''
  try {
    wanted = path.resolve(workDir)
  } catch {
    return null
  }
  for (const key of Object.keys(projects)) {
    try {
      if (path.resolve(key) === wanted) return projects[key]
    } catch {
      // Ignore a project key that is not a path.
    }
  }
  return null
}

function stripJsonConfig(text) {
  let out = ''
  let inString = false
  let escaped = false
  for (let i = 0; i < text.length; i += 1) {
    const char = text[i]
    const next = text[i + 1]
    if (inString) {
      out += char
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === '"') inString = false
      continue
    }
    if (char === '"') {
      inString = true
      out += char
      continue
    }
    if (char === '/' && next === '/') {
      i += 1
      while (i + 1 < text.length && text[i + 1] !== '\n') i += 1
      continue
    }
    if (char === '/' && next === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i += 1
      i += 1
      continue
    }
    out += char
  }
  return out.replace(/,(\s*[}\]])/g, '$1')
}

// 新旧 server 名都算「用户已配置」，避免改名后在用户配置里重复注入。
function jsonHasMcpServer(text, workDir) {
  if (text == null || text === '') return false
  let data
  try {
    data = JSON.parse(stripJsonConfig(text))
  } catch {
    return false
  }
  if (!data || typeof data !== 'object' || Array.isArray(data)) return false
  if (serverMapHasAny(data.mcpServers, SERVER_NAMES) || serverMapHasAny(data.mcp_servers, SERVER_NAMES)) {
    return true
  }
  const project = projectEntry(data.projects, workDir)
  return Boolean(project) && (
    serverMapHasAny(project.mcpServers, SERVER_NAMES) || serverMapHasAny(project.mcp_servers, SERVER_NAMES)
  )
}

function mcpConfigDocument(command) {
  return JSON.stringify({
    mcpServers: {
      [SERVER_NAME]: {
        type: 'stdio',
        command,
        args: ['mcp'],
      },
    },
  })
}

function withMcpChannel(prompt, channel) {
  if (channel !== 'mcp' || String(prompt).includes('回传通道：')) return prompt
  return `${prompt}\n${CHANNEL_MCP}`
}

async function prepareJsonMcp({ backendPath, workDir, files }) {
  for (const file of files.filter(Boolean)) {
    const text = await readConfig(file)
    if (text == null || jsonHasMcpServer(text, workDir)) {
      return { channel: 'script', prefixArgs: [] }
    }
  }
  const command = mcpBackendPath(backendPath)
  if (command === '') return { channel: 'script', prefixArgs: [] }
  return {
    channel: 'mcp',
    prefixArgs: ['--mcp-config', mcpConfigDocument(command)],
  }
}

module.exports = {
  CHANNEL_MCP,
  SERVER_NAME,
  LEGACY_SERVER_NAMES,
  SERVER_NAMES,
  jsonHasMcpServer,
  mcpBackendPath,
  mcpConfigDocument,
  prepareJsonMcp,
  readConfig,
  withMcpChannel,
}
