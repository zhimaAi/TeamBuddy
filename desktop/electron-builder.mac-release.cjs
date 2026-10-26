'use strict'

const { loadMacSigningConfig, checkMacRelease, notarizeAndVerify } = require('./build/mac-release-signing.cjs')

const signing = loadMacSigningConfig()
let verified = false

module.exports = {
  extends: './electron-builder.version.cjs',
  forceCodeSigning: true,
  mac: {
    identity: signing.identity.replace(/^Developer ID Application: /, ''),
    hardenedRuntime: true,
    binaries: ['Contents/Resources/backend/client'],
    notarize: false, // 使用显式钥匙串凭据公证，禁止环境变量缺失时静默跳过。
  },
  ...(process.env.GOTEAMS_MAC_RELEASE_OUTPUT ? { directories: { output: process.env.GOTEAMS_MAC_RELEASE_OUTPUT } } : {}),
  dmg: { sign: true },
  beforePack: () => checkMacRelease(signing),
  afterSign: async context => {
    await notarizeAndVerify(context, signing)
    verified = true
  },
  afterAllArtifactBuild: () => {
    if (!verified) throw new Error('macOS 发行包未完成签名、公证和 Gatekeeper 验证。')
    return []
  },
}
