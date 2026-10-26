'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { randomUUID, createHash } = require('node:crypto')

async function readRegular(file, missing = '') {
  try {
    const info = await fs.lstat(file)
    if (!info.isFile() || info.size > 1024 * 1024) throw new Error('configuration_conflict')
    return await fs.readFile(file, 'utf8')
  } catch (error) {
    if (error.code === 'ENOENT') return missing
    throw error
  }
}

function digest(text) {
  return createHash('sha256').update(text).digest('hex')
}

function hookStateDir(workDir, stateRoot) {
  if (!stateRoot || !path.isAbsolute(stateRoot)) throw new Error('configuration_conflict')
  return path.join(stateRoot, 'qoder-hooks', digest(workDir))
}

async function writeAtomic(file, content) {
  const temp = `${file}.${randomUUID()}.tmp`
  try {
    await fs.writeFile(temp, content, { flag: 'wx', mode: 0o600 })
    await fs.rename(temp, file)
  } finally { await fs.unlink(temp).catch(() => {}) }
}

// 配置事务与引导确认共用同一把锁；不自动删除残留锁。
async function acquireInstallationLock(stateDir) {
  const file = path.join(stateDir, 'install.lock')
  const lock = await fs.open(file, 'wx', 0o600).catch(error => {
    if (error.code === 'EEXIST') throw new Error('configuration_locked')
    throw error
  })
  return async () => {
    await lock.close()
    await fs.unlink(file)
  }
}

async function markRestartGuidance(stateDir) {
  await writeAtomic(path.join(stateDir, 'restart-pending'), randomUUID())
}

async function readRestartGuidance(stateDir) {
  const restartToken = await readRegular(path.join(stateDir, 'restart-pending'))
  return { restartRequired: Boolean(restartToken), restartToken }
}

// 仅在确认 IDE 未运行并完成冷启动请求后确认；旧请求不能清除新配置的记录。
async function acknowledgeQoderRestart(workDir, stateRoot, restartToken) {
  const dir = hookStateDir(workDir, stateRoot)
  const release = await acquireInstallationLock(dir)
  try {
    const file = path.join(dir, 'restart-pending')
    if (await readRegular(file) === restartToken) await fs.unlink(file)
  } finally { await release() }
}

module.exports = {
  readRegular, writeAtomic, digest, hookStateDir, acquireInstallationLock,
  markRestartGuidance, readRestartGuidance, acknowledgeQoderRestart,
}
