'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { jsonHasMcpServer, mcpBackendPath, readConfig, SERVER_NAME } = require('./common.cjs')

function piConfigFiles(homeDir, workDir) {
  const files = [path.join(homeDir, '.pi', 'agent', 'mcp.json')]
  if (workDir) files.push(path.join(workDir, '.pi', 'mcp.json'))
  return files
}

function extensionSource(command) {
  return `export default function (pi) {
  pi.registerMcpServer("${SERVER_NAME}", {
    command: ${JSON.stringify(command)},
    args: ["mcp"],
  })
}
`
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string }} [options] */
async function preparePi({ backendPath, workDir, homeDir = os.homedir() } = {}) {
  const files = piConfigFiles(homeDir, workDir)
  for (const file of files) {
    if (jsonHasMcpServer(await readConfig(file), workDir)) {
      return { channel: 'script', prefixArgs: [] }
    }
  }
  const command = mcpBackendPath(backendPath)
  if (command === '') return { channel: 'script', prefixArgs: [] }
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'goteams-pi-mcp-'))
  const extensionPath = path.join(directory, `${SERVER_NAME}.ts`)
  try {
    await fs.writeFile(extensionPath, extensionSource(command), { encoding: 'utf8', mode: 0o600 })
  } catch (error) {
    await fs.rm(directory, { recursive: true, force: true })
    throw error
  }
  return {
    channel: 'mcp',
    prefixArgs: ['--extension', extensionPath],
    cleanupDir: directory,
  }
}

module.exports = { preparePi, piConfigFiles, extensionSource }
