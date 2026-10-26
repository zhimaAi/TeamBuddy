'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { createHash } = require('node:crypto')
const { createRequire } = require('node:module')
const { spawnSync } = require('node:child_process')
const { loadMacSigningConfig, checkMacRelease, notarizeDmg } = require('../../desktop/build/mac-release-signing.cjs')

const root = path.resolve(__dirname, '../..')
const desktopRequire = createRequire(path.join(root, 'desktop/package.json'))

function parseOptions(args) {
  const values = { config: '', arch: 'all' }
  for (const arg of args) {
    const match = /^--(config|arch)=(.+)$/.exec(arg)
    if (!match) throw new Error(`未知参数：${arg}`)
    values[match[1]] = match[2]
  }
  if (!['cn', 'global'].includes(values.config)) throw new Error('CONFIG 必须为 cn 或 global。')
  if (!['all', 'x64', 'amd64', 'arm64'].includes(values.arch)) throw new Error('ARCH 必须为 all、x64（或 amd64）、arm64。')
  return { config: values.config, arches: values.arch === 'all' ? ['x64', 'arm64'] : [values.arch === 'amd64' ? 'x64' : values.arch] }
}

function run(command, args, env = process.env) {
  console.log(`==> ${command} ${args.join(' ')}`)
  const result = spawnSync(command, args, { cwd: root, env, stdio: 'inherit', timeout: 30 * 60 * 1000 })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`${command} 执行失败（${result.signal || result.status}）。`)
}

async function writeChecksums(directory, names) {
  const lines = []
  for (const name of names) {
    const hash = createHash('sha256')
    for await (const chunk of fs.createReadStream(path.join(directory, name))) hash.update(chunk)
    lines.push(`${hash.digest('hex')}  ${name}`)
  }
  fs.writeFileSync(path.join(directory, 'SHA256SUMS'), `${lines.join('\n')}\n`, { flag: 'wx' })
}

async function main(args = process.argv.slice(2)) {
  const options = parseOptions([
    `--config=${process.env.MAC_RELEASE_CONFIG || 'cn'}`,
    `--arch=${process.env.MAC_RELEASE_ARCH || 'all'}`, ...args,
  ])
  if (process.platform !== 'darwin') throw new Error('macOS 正式发行包必须在 Mac 上构建。')
  const signing = loadMacSigningConfig()
  checkMacRelease(signing)
  const { version } = desktopRequire('./package.json')
  if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error('产品版本必须为主版本.次版本.修订号。')
  // 所有正式 macOS 发行共用前端目录；持锁期间禁止另一个发行任务改写它。
  const buildRoot = path.join(root, 'dist')
  fs.mkdirSync(buildRoot, { recursive: true })
  const lock = path.join(buildRoot, '.mac-release.lock')
  let descriptor
  try { descriptor = fs.openSync(lock, 'wx') } catch (error) {
    if (error.code === 'EEXIST') throw new Error(`已有 macOS 发行任务或上次异常退出留下的锁：${lock}；确认没有构建进程后再移除。`)
    throw error
  }
  try {
    fs.writeFileSync(descriptor, `${process.pid}\n`)
    const parent = path.join(buildRoot, 'releases', version, options.config)
    fs.mkdirSync(parent, { recursive: true })
    const directory = fs.mkdtempSync(path.join(parent, `${new Date().toISOString().replace(/[:.]/g, '-')}-`))
    run(process.execPath, [path.join(__dirname, 'sync-version.cjs')])
    run('npm', ['--prefix', 'web', 'run', 'build'])
    const builder = desktopRequire.resolve('electron-builder/cli.js')
    const names = []
    for (const arch of options.arches) {
      const staging = path.join(directory, '.work', arch)
      const backend = path.join(staging, 'backend')
      const output = path.join(staging, 'electron')
      run('go', ['run', './tools/build', '-goos=darwin', `-goarch=${arch === 'x64' ? 'amd64' : 'arm64'}`,
        '-cmd=./cmd/client', `-output=${path.join(backend, 'client')}`, `-config=${options.config}`])
      // 每个架构独立进程，签名验证状态不得跨架构复用；禁止自动上传发布。
      run(process.execPath, [builder, '--projectDir', path.join(root, 'desktop'),
        '--config', path.join(root, 'desktop/electron-builder.mac-release.cjs'),
        '--mac', `--${arch}`, '--publish', 'never'], {
        ...process.env, GOTEAMS_MAC_RELEASE_BACKEND: backend, GOTEAMS_MAC_RELEASE_OUTPUT: output,
      })
      const name = `TeamBuddy-macos-${arch}.dmg`
      const file = path.join(output, name)
      notarizeDmg(file, signing)
      fs.copyFileSync(file, path.join(directory, name), fs.constants.COPYFILE_EXCL)
      names.push(name)
      console.log(`==> ${arch}：签名、公证及票据校验通过`)
    }
    await writeChecksums(directory, names)
    fs.writeFileSync(path.join(directory, 'release.json'), JSON.stringify({
      version, config: options.config, arches: options.arches, artifacts: names,
      verified: true, completedAt: new Date().toISOString(),
    }, null, 2) + '\n', { flag: 'wx' })
    console.log(`==> macOS 发行完成：${directory}\n版本：${version}；配置：${options.config}；架构：${options.arches.join(', ')}\n${names.join('\n')}\nSHA256SUMS / release.json`)
  } finally {
    fs.closeSync(descriptor)
    fs.unlinkSync(lock)
  }
}

if (require.main === module) main().catch(error => { console.error(`发行失败：${error.message}`); process.exitCode = 1 })
module.exports = { parseOptions, writeChecksums }
