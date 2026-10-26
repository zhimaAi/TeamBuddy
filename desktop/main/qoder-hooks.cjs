'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { randomUUID } = require('node:crypto')
const {
  readRegular, writeAtomic, digest, hookStateDir, acquireInstallationLock,
  markRestartGuidance, readRestartGuidance,
} = require('./qoder-hook-store.cjs')
const EVENTS = ['SessionStart', 'UserPromptSubmit', 'Stop']

function hookCommand(backendPath, platform) {
  if (!path.isAbsolute(backendPath) || /[\0\r\n]/.test(backendPath)) throw new Error('backend_unavailable')
  if (platform === 'win32') {
    if (/["%&|<>^]/.test(backendPath)) throw new Error('backend_unavailable')
    return `"${backendPath}" qoder-hook`
  }
  return `'${backendPath.replace(/'/g, "'\\''")}' qoder-hook`
}

function backupPaths(file, manifestFile, backup) {
  if (!/^\.teambuddy-backup-[a-f0-9-]{36}$/.test(backup)) throw new Error('configuration_conflict')
  return {
    dir: path.join(path.dirname(file), backup),
    displaced: path.join(path.dirname(file), backup, 'settings.local.json'),
    archived: path.join(path.dirname(manifestFile), `settings-backup-${backup.slice('.teambuddy-backup-'.length)}.json`),
  }
}

async function readBackup(file, manifestFile, backup) {
  const paths = backupPaths(file, manifestFile, backup)
  try {
    const info = await fs.lstat(paths.dir)
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('configuration_conflict')
  } catch (error) { if (error.code !== 'ENOENT') throw error }
  const displaced = await readRegular(paths.displaced, null)
  return { ...paths, content: displaced ?? await readRegular(paths.archived, null) }
}

async function restoreBackup(file, displaced) {
  // 排他恢复，不覆盖其他程序在移走原文件后创建的新配置。
  await fs.link(displaced, file).catch(error => {
    if (!['EEXIST', 'ENOENT'].includes(error.code)) throw error
  })
}

async function archiveBackup(file, manifestFile, backup) {
  const paths = await readBackup(file, manifestFile, backup)
  if (await readRegular(paths.displaced, null) !== null) {
    try {
      // 硬链接保留原文件 inode，编辑器尚未关闭的句柄写入也不会丢失。
      await fs.link(paths.displaced, paths.archived)
    } catch (error) {
      // 跨卷不能保留 inode 时，原文件继续留在项目的私有备份目录。
      if (error.code === 'EXDEV') return
      if (error.code === 'EEXIST') {
        const [left, right] = await Promise.all([fs.stat(paths.displaced), fs.stat(paths.archived)])
        if (left.dev !== right.dev || left.ino !== right.ino) throw new Error('configuration_conflict')
      } else if (error.code !== 'ENOENT') throw error
    }
    await fs.unlink(paths.displaced).catch(error => { if (error.code !== 'ENOENT') throw error })
  }
  await fs.rmdir(paths.dir).catch(error => { if (error.code !== 'ENOENT') throw error })
}

async function recoverInstallation(file, manifestFile, journalFile) {
  const raw = await readRegular(journalFile)
  if (!raw) return false
  let journal
  try { journal = JSON.parse(raw) } catch { throw new Error('configuration_conflict') }
  if (![1, 2].includes(journal?.version) || typeof journal.manifest !== 'string'
      || ![journal.original, journal.settings, journal.oldManifest].every(value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value))) {
    throw new Error('configuration_conflict')
  }
  let settings = await readRegular(file, null)
  if (journal.version === 2) {
    const backup = await readBackup(file, manifestFile, journal.backup)
    if (settings === null && backup.content !== null) {
      // 配置移走后发生中断：先排他恢复原文件，再按哈希核对事务。
      const source = await readRegular(backup.displaced, null) !== null ? backup.displaced : backup.archived
      await restoreBackup(file, source)
      settings = await readRegular(file, null)
    }
    if (backup.content !== null && digest(backup.content) !== journal.original) throw new Error('configuration_conflict')
  }
  settings ??= ''
  const manifest = await readRegular(manifestFile)
  const originalMatches = digest(settings) === journal.original
  const installedMatches = digest(settings) === journal.settings
  if ((!originalMatches && !installedMatches)
      || (digest(manifest) !== journal.oldManifest && manifest !== journal.manifest)) {
    throw new Error('configuration_conflict')
  }
  // 只完成原事务已安装的配置；用户修改任何一份文件时保留日志并拒绝覆盖。
  if (installedMatches) {
    if (manifest !== journal.manifest) await writeAtomic(manifestFile, journal.manifest)
  } else if (digest(manifest) !== journal.oldManifest) {
    throw new Error('configuration_conflict')
  }
  if (journal.version === 2) await archiveBackup(file, manifestFile, journal.backup)
  await fs.unlink(journalFile)
  return installedMatches
}

// 仅合并自己的事件组，保留现有规则和 Hooks；已被用户修改的受管组不覆盖。
async function prepareQoderHooks(workDir, backendPath, platform = process.platform, stateRoot = undefined) {
  const stateDir = hookStateDir(workDir, stateRoot)
  const command = hookCommand(backendPath, platform)
  if (!(await fs.stat(backendPath)).isFile()) throw new Error('backend_unavailable')
  const dir = path.join(workDir, '.qoder')
  await fs.mkdir(dir, { recursive: true })
  const dirInfo = await fs.lstat(dir)
  if (!dirInfo.isDirectory() || dirInfo.isSymbolicLink()) throw new Error('configuration_conflict')
  const file = path.join(dir, 'settings.local.json')
  await fs.mkdir(stateDir, { recursive: true, mode: 0o700 })
  const manifestFile = path.join(stateDir, 'manifest.json')
  const journalFile = path.join(stateDir, 'install-journal.json')
  const release = await acquireInstallationLock(stateDir)
  try {
    const recovered = await recoverInstallation(file, manifestFile, journalFile)
    // 只有文件确实不存在才允许重装；空文件及现有配置缺少受管组仍拒绝覆盖。
    const originalContent = await readRegular(file, null)
    const missingSettings = originalContent === null
    const original = originalContent ?? ''
    if (!missingSettings && !original.trim()) throw new Error('configuration_conflict')
    const oldManifest = await readRegular(manifestFile)
    let config, manifest
    try {
      config = original ? JSON.parse(original) : {}
      manifest = oldManifest ? JSON.parse(oldManifest) : null
    } catch { throw new Error('configuration_conflict') }
    if (!config || Array.isArray(config) || typeof config !== 'object') throw new Error('configuration_conflict')
    if (config.hooks !== undefined && (!config.hooks || typeof config.hooks !== 'object' || Array.isArray(config.hooks))) throw new Error('configuration_conflict')
    const group = { hooks: [{ type: 'command', command, timeout: 10 }] }
    config.hooks ||= {}
    let changed = false
    for (const event of EVENTS) {
      const groups = config.hooks[event] || []
      if (!Array.isArray(groups)) throw new Error('configuration_conflict')
      const oldGroup = missingSettings ? undefined : manifest?.groups?.[event]
      const ownIndex = oldGroup ? groups.findIndex(value => JSON.stringify(value) === JSON.stringify(oldGroup)) : -1
      if (oldGroup && ownIndex < 0) throw new Error('configuration_modified')
      // 未受管的同名命令属于用户配置，不重复安装或猜测所有权。
      if (ownIndex < 0 && groups.some(value => value?.hooks?.some(hook => String(hook.command || '').includes('qoder-hook')))) throw new Error('configuration_conflict')
      if (ownIndex >= 0 && JSON.stringify(groups[ownIndex]) === JSON.stringify(group)) continue
      if (ownIndex >= 0) groups[ownIndex] = group
      else groups.push(group)
      config.hooks[event] = groups
      changed = true
    }
    if (!changed) {
      // 兼容旧安装日志恢复：即使配置没有再次变化，仍须给出重启引导。
      if (recovered) await markRestartGuidance(stateDir)
      return { updated: recovered, ...await readRestartGuidance(stateDir) }
    }
    const freshManifest = JSON.stringify({ groups: Object.fromEntries(EVENTS.map(event => [event, group])) }, null, 2) + '\n'
    const settings = JSON.stringify(config, null, 2) + '\n'
    if (Buffer.byteLength(settings, 'utf8') > 1024 * 1024) throw new Error('configuration_conflict')
    if (await readRegular(file, null) !== originalContent || await readRegular(manifestFile) !== oldManifest) throw new Error('configuration_conflict')
    const stamp = randomUUID()
    const backup = `.teambuddy-backup-${stamp}`
    const backupDir = path.join(dir, backup)
    const displaced = path.join(backupDir, 'settings.local.json')
    const probe = path.join(backupDir, 'link-probe')
    const temp = path.join(dir, `.teambuddy-settings-${stamp}.tmp`)
    try {
      await fs.writeFile(temp, settings, { flag: 'wx', mode: 0o600 })
      if (!missingSettings) {
        await fs.mkdir(backupDir, { mode: 0o700 })
        // 不支持硬链接的卷在移走用户配置前失败。
        await fs.link(temp, probe)
        await fs.unlink(probe)
      }
      // 先持久保存恢复依据，再替换配置；清单原子写入，避免失败留下半份 JSON。
      // 在配置事务之前保存，引导不能因后续启动失败或客户端退出而丢失。
      await markRestartGuidance(stateDir)
      await writeAtomic(journalFile, JSON.stringify({
        version: missingSettings ? 1 : 2, backup: missingSettings ? undefined : backup,
        original: digest(original), settings: digest(settings),
        oldManifest: digest(oldManifest), manifest: freshManifest,
      }) + '\n')
      if (await readRegular(file, null) !== originalContent || await readRegular(manifestFile) !== oldManifest) throw new Error('configuration_conflict')
      if (!missingSettings) {
        // 移走实际文件后核对，捕获检查与 rename 之间的并发保存。
        // 不对目标执行覆盖式 rename；原 inode 保留到私有备份中。
        await fs.rename(file, displaced)
        if (await readRegular(displaced) !== original) throw new Error('configuration_conflict')
      }
      await fs.link(temp, file).catch(error => {
        if (error.code === 'EEXIST') throw new Error('configuration_conflict')
        throw error
      })
      if (!missingSettings && await readRegular(displaced) !== original) throw new Error('configuration_conflict')
      if (await readRegular(file, null) !== settings) throw new Error('configuration_conflict')
      await writeAtomic(manifestFile, freshManifest)
      if (!missingSettings) await archiveBackup(file, manifestFile, backup)
      await fs.unlink(journalFile)
    } catch (error) {
      if (!missingSettings) await restoreBackup(file, displaced)
      throw error
    } finally {
      await fs.unlink(temp).catch(() => {})
      if (!missingSettings) {
        await fs.unlink(probe).catch(() => {})
        // 仅清理空目录，冲突或跨卷留下的原文件备份必须保留。
        await fs.rmdir(backupDir).catch(() => {})
      }
    }
    return { updated: true, ...await readRestartGuidance(stateDir) }
  } finally {
    await release()
  }
}

module.exports = { prepareQoderHooks, hookCommand }
