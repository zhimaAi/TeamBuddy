'use strict'

const fs = require('node:fs/promises')
const path = require('node:path')
const { randomUUID, createHash } = require('node:crypto')
const { promisify } = require('node:util')
const { execFile } = require('node:child_process')
const runFile = promisify(execFile)

/**
 * 开发态 go run 的临时程序不能写入持久 Hook。编译独立回调程序到宿主
 * 用户数据目录，发布态则继续使用打包 sidecar；不改变正在运行的服务。
 * @param {{isPackaged: boolean, resourcesPath: string, repoRoot: string, stateRoot: string, platform: string, signal?: AbortSignal}} options
 * @param {(file: string, args: string[], options: object) => Promise<{stdout?: string | Buffer}>} [run]
 */
async function resolveQoderBackend(options, run = runFile) {
  const { isPackaged, resourcesPath, repoRoot, stateRoot, platform, signal } = options
  const executable = platform === 'win32' ? 'client.exe' : 'client'
  if (isPackaged) return path.join(resourcesPath, 'backend', executable)
  if (!path.isAbsolute(repoRoot) || !path.isAbsolute(stateRoot)) throw new Error('backend_unavailable')
  const dir = path.join(stateRoot, 'qoder-backend')
  await fs.mkdir(dir, { recursive: true, mode: 0o700 })
  const target = path.join(dir, executable)
  const cacheFile = path.join(dir, 'build-cache.json')
  const temporary = path.join(dir, `${randomUUID()}-${executable}`)
  const cacheTemp = `${cacheFile}.${randomUUID()}.tmp`
  // 查询、编译和复核共用原有的 120 秒期限，退出取消也传到全部子进程。
  const deadline = AbortSignal.timeout(120000)
  const buildSignal = signal ? AbortSignal.any([signal, deadline]) : deadline
  const commandOptions = { cwd: repoRoot, timeout: 120000, maxBuffer: 256 * 1024, signal: buildSignal }
  const fingerprint = async () => {
    buildSignal.throwIfAborted()
    // Go 自己计算源码、依赖和 embed 文件的 BuildID，避免手工维护文件扫描规则。
    const result = await run('go', ['list', '-export', '-buildvcs=false', '-f', '{{.BuildID}}', './cmd/client'], commandOptions)
    const id = String(result.stdout || '').trim()
    if (!id || id.length > 1024 || /\s/.test(id)) throw new Error('backend_unavailable')
    // 纯链接参数不一定改变包 BuildID，另外纳入 Go 的有效构建参数。
    const environment = await run('go', ['env', '-json', 'GOFLAGS', 'CGO_LDFLAGS'], commandOptions)
    return createHash('sha256').update(JSON.stringify([repoRoot, platform, id, String(environment.stdout)])).digest('hex')
  }
  try {
    const key = await fingerprint()
    try {
      const cache = JSON.parse(await fs.readFile(cacheFile, 'utf8'))
      const info = await fs.stat(target)
      if (cache.version === 1 && cache.key === key && info.isFile()
          && cache.size === info.size && cache.mtimeMs === info.mtimeMs
          && (platform === 'win32' || (info.mode & 0o111) !== 0)) {
        buildSignal.throwIfAborted()
        return target
      }
    } catch (error) {
      // 缓存缺失或损坏时重新编译；取消不能降级成继续启动。
      buildSignal.throwIfAborted()
    }
    buildSignal.throwIfAborted()
    await run('go', ['build', '-buildvcs=false', '-o', temporary, './cmd/client'], commandOptions)
    if (!(await fs.stat(temporary)).isFile()) throw new Error('backend_unavailable')
    const freshKey = await fingerprint()
    buildSignal.throwIfAborted()
    await fs.rename(temporary, target)
    if (freshKey === key) {
      const info = await fs.stat(target)
      await fs.writeFile(cacheTemp, JSON.stringify({ version: 1, key, size: info.size, mtimeMs: info.mtimeMs }) + '\n', { flag: 'wx', mode: 0o600 })
      await fs.rename(cacheTemp, cacheFile)
    } else {
      // 编译期间源码改变时不发布缓存标记，下次重新核对与构建。
      await fs.unlink(cacheFile).catch(error => { if (error.code !== 'ENOENT') throw error })
    }
    return target
  } catch {
    // 不把编译日志或工程路径带进用户提示。
    throw new Error('backend_unavailable')
  } finally {
    await fs.unlink(temporary).catch(() => {})
    await fs.unlink(cacheTemp).catch(() => {})
  }
}

module.exports = { resolveQoderBackend }
