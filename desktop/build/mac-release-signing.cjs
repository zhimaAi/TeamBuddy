'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { execFileSync } = require('node:child_process')
const { DEFAULT_CONFIG_PATH } = require('./release-signing.cjs')

function execute(command, args) {
  return execFileSync(command, args, {
    encoding: 'utf8', timeout: 20 * 60 * 1000, maxBuffer: 4 * 1024 * 1024,
  })
}

function loadMacSigningConfig(configPath = process.env.GOTEAMS_SIGNING_CONFIG || DEFAULT_CONFIG_PATH) {
  let config
  try {
    config = JSON.parse(fs.readFileSync(configPath, 'utf8')).mac
  } catch (error) {
    throw new Error(`无法读取 macOS 发布签名配置 ${configPath}：${error.message}`)
  }
  const identity = typeof config?.identity === 'string' ? config.identity.trim() : ''
  const keychainProfile = typeof config?.keychainProfile === 'string' ? config.keychainProfile.trim() : ''
  if (!/^Developer ID Application: .+ \([A-Z0-9]{10}\)$/.test(identity)) {
    throw new Error('mac.identity 必须是完整的 Developer ID Application 签名身份。')
  }
  if (!keychainProfile || /[\r\n\0]/.test(keychainProfile)) {
    throw new Error('mac.keychainProfile 必须是已保存到钥匙串的公证凭据名称。')
  }
  return Object.freeze({ identity, keychainProfile })
}

function checkMacRelease(config, run = execute, platform = process.platform) {
  if (platform !== 'darwin') throw new Error('macOS 正式发行包必须在 Mac 上构建。')
  const identities = run('/usr/bin/security', ['find-identity', '-v', '-p', 'codesigning'])
  if (!String(identities).includes(`"${config.identity}"`)) {
    throw new Error(`找不到有效的签名身份：${config.identity}；请将证书和私钥导入当前用户钥匙串。`)
  }
  run('/usr/bin/xcrun', ['notarytool', 'history', '--keychain-profile', config.keychainProfile, '--output-format', 'json'])
}

function submitForNotarization(file, config, run = execute) {
  const result = JSON.parse(run('/usr/bin/xcrun', [
    'notarytool', 'submit', file, '--keychain-profile', config.keychainProfile,
    '--wait', '--timeout', '15m', '--output-format', 'json',
  ]))
  if (result.status !== 'Accepted') {
    throw new Error(`Apple 公证未通过：${result.status || '未知状态'}；提交 ID：${result.id || '未知'}。可使用 notarytool log 查询原因。`)
  }
}

async function notarizeApp({ appPath, keychainProfile }) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'teambuddy-notary-'))
  try {
    const archive = path.join(temporary, 'application.zip')
    execute('/usr/bin/ditto', ['-c', '-k', '--keepParent', appPath, archive])
    submitForNotarization(archive, { keychainProfile })
    execute('/usr/bin/xcrun', ['stapler', 'staple', appPath])
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true })
  }
}

async function notarizeAndVerify(context, config, dependencies = {}) {
  const run = dependencies.execute || execute
  const appPath = path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`)
  // 验证整包和额外 Go 可执行文件，签名失败时不得上传公证。
  run('/usr/bin/codesign', ['--verify', '--deep', '--strict', '--verbose=2', appPath])
  run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', path.join(appPath, 'Contents/Resources/backend/client')])
  await (dependencies.notarize || notarizeApp)({ appPath, keychainProfile: config.keychainProfile })
  run('/usr/bin/xcrun', ['stapler', 'validate', appPath])
  run('/usr/sbin/spctl', ['--assess', '--type', 'execute', '--verbose=2', appPath])
}

function notarizeDmg(file, config, run = execute) {
  run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', file])
  submitForNotarization(file, config, run)
  run('/usr/bin/xcrun', ['stapler', 'staple', file])
  run('/usr/bin/xcrun', ['stapler', 'validate', file])
}

module.exports = { loadMacSigningConfig, checkMacRelease, notarizeAndVerify, notarizeDmg, submitForNotarization }
