'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { ipcMain } = require('electron')
const channels = require('../../shared/channels.cjs')
const { t } = require('../desktop-i18n.cjs')
const { isTrustedURL } = require('../security.cjs')
const { normalizeDirectoryPath } = require('./path-validation.cjs')
const { openVibeCli } = require('../vibe-cli.cjs')
const { mcpBackendPath, mcpConfigDocument } = require('../vibe-mcp/common.cjs')

// 手动模式各外部工具推荐的 MCP 配置写入路径，与 sidecar 侧的 vibeMCPConfigPath 保持
// 一致：这里只是在 sidecar 未就绪（开发态）时的兜底通道，不读写用户配置。
// DSH 不在此表中——它的 MCP 由 profile 管理，只引导用可视化插件添加。
const MANUAL_VIBE_MCP_PATHS = {
  cursor: ['.cursor', 'mcp.json'],
  workbuddy: ['.workbuddy', 'mcp.json'],
}

function manualVibeMcpConfigPath(homeDir, tool) {
  const segments = MANUAL_VIBE_MCP_PATHS[String(tool || '').trim().toLowerCase()]
  return segments ? path.join(homeDir, ...segments) : ''
}

function registerVibeCliIPC(allowedOrigins, options = {}) {
  ipcMain.handle(channels.OPEN_VIBE_CLI, async (event, payload = {}) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted Vibe CLI request')
    }
    try {
      const requestedPath = normalizeDirectoryPath(payload.directoryPath)
      const realPath = await fs.realpath(requestedPath)
      const info = await fs.stat(realPath)
      if (!info.isDirectory()) throw new Error('not a directory')
      if (typeof payload.execPath !== 'string' || !path.isAbsolute(payload.execPath)) {
        throw new Error('not a file')
      }
      const resolvedExec = await fs.realpath(payload.execPath)
      const execInfo = await fs.stat(resolvedExec)
      if (!execInfo.isFile()) throw new Error('not a file')
      const backendPath = typeof options.backendPath === 'function' ? options.backendPath() : ''
      await openVibeCli(realPath, payload.tool, payload.execPath, payload.prompt, payload.threadId, process.platform, undefined, {
        backendPath,
        workDir: realPath,
        homeDir: os.homedir(),
        codexHome: process.env.CODEX_HOME,
      }, payload.modelName)
      return { opened: true }
    } catch {
      throw new Error(t('error.vibeCli.openFailed'))
    }
  })
  // 手动模式不启动任何进程：这里只把可复制的 MCP 配置交给渲染层，由用户自行
  // 决定是否写入外部工具的 mcp.json。不读取、不修改、也不校验用户已有配置。
  ipcMain.handle(channels.GET_VIBE_MCP_CONFIG, async (event, payload = {}) => {
    if (!event.senderFrame || !isTrustedURL(event.senderFrame.url, allowedOrigins)) {
      throw new Error('Untrusted Vibe MCP config request')
    }
    const backendPath = typeof options.backendPath === 'function' ? options.backendPath() : ''
    const command = mcpBackendPath(backendPath)
    if (!command) return { available: false, config: '', configPath: '', command: '' }
    return {
      available: true,
      config: JSON.stringify(JSON.parse(mcpConfigDocument(command)), null, 2),
      configPath: manualVibeMcpConfigPath(os.homedir(), payload.tool),
      command,
    }
  })
  return () => {
    ipcMain.removeHandler(channels.OPEN_VIBE_CLI)
    ipcMain.removeHandler(channels.GET_VIBE_MCP_CONFIG)
  }
}

module.exports = { registerVibeCliIPC }
