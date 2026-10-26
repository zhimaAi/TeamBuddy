'use strict'

const { syncVersion } = require('../tools/build/sync-version.cjs')

syncVersion(true)

module.exports = {
  extends: './electron-builder.yml',
  mac: {
    // 继承配置会合并资源数组；只在此处选择来源，避免旧后端和发行后端写入同一目标。
    extraResources: [{
      from: process.env.GOTEAMS_MAC_RELEASE_BACKEND || 'resources/backend',
      to: 'backend',
      filter: ['client'],
    }],
  },
}
