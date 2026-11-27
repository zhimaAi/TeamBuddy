'use strict'

const { execFile } = require('node:child_process')
const fs = require('node:fs/promises')
const path = require('node:path')
const { fileURLToPath } = require('node:url')
const { readRegular } = require('./qoder-hook-store.cjs')

const PROJECT_FILE = /^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.json$/

/** @returns {Promise<{status: 'found', projectId: string} | {status: 'missing' | 'ambiguous' | 'unavailable'}>} */
async function findAntigravityAppProject(home, workDir, signal) {
  const gemini = path.join(home, '.gemini')
  const config = path.join(gemini, 'config')
  const projects = path.join(config, 'projects')
  let matched
  let widerProject = false
  try {
    for (const dir of [gemini, config, projects]) {
      try {
        if (!(await fs.lstat(dir)).isDirectory()) return { status: 'unavailable' }
      } catch (error) {
        if (error.code === 'ENOENT') return { status: 'missing' }
        throw error
      }
    }
    // This is local metadata, not an App API; never guess on an incomplete scan.
    let remaining = 512
    for await (const entry of await fs.opendir(projects)) {
      signal.throwIfAborted()
      if (--remaining < 0) return { status: 'unavailable' }
      const id = PROJECT_FILE.exec(entry.name)?.[1]
      if (!id) continue
      if (!entry.isFile()) return { status: 'unavailable' }
      const project = JSON.parse(await readRegular(path.join(projects, entry.name)))
      if (project?.id !== id) return { status: 'unavailable' }
      // App records store false; agy omits the field for regular projects.
      if (project.isWorkspaceOnly === true) continue
      if (project.isWorkspaceOnly !== undefined && project.isWorkspaceOnly !== false) return { status: 'unavailable' }
      const resources = project.projectResources?.resources ?? []
      if (!Array.isArray(resources)) return { status: 'unavailable' }
      for (const resource of resources) {
        const folderUri = resource?.gitFolder?.folderUri ?? resource?.folderUri
        if (typeof folderUri !== 'string') continue
        const uri = new URL(folderUri)
        if (uri.protocol !== 'file:' || uri.hostname || uri.search || uri.hash) continue
        let folder
        try { folder = await fs.realpath(fileURLToPath(uri)) }
        catch (error) {
          if (error.code === 'ENOENT' || error.code === 'ENOTDIR') continue
          throw error
        }
        if (folder !== workDir) continue
        if (resources.length !== 1) { widerProject = true; break }
        if (matched) return { status: 'ambiguous' }
        matched = id
      }
    }
  } catch {
    signal.throwIfAborted()
    return { status: 'unavailable' }
  }
  return matched ? { status: 'found', projectId: matched } : { status: widerProject ? 'ambiguous' : 'missing' }
}

/** Run only the project setup with EOF; never pass a task prompt or CLI binding ticket. */
async function createAntigravityAppProject(runtime, workDir, signal) {
  const env = { ...process.env }
  delete env.TEAMBUDDY_AGY_LAUNCH
  await new Promise((resolve, reject) => {
    const child = execFile(runtime, ['--new-project', '--input-format', 'stream-json', '--output-format', 'stream-json'], {
      cwd: workDir, env, timeout: 15000, killSignal: 'SIGKILL', maxBuffer: 64 * 1024, windowsHide: true, signal,
    }, error => error ? reject(error) : resolve(undefined))
    child.stdin?.on('error', () => {})
    child.stdin?.end()
  })
}

module.exports = { findAntigravityAppProject, createAntigravityAppProject }
