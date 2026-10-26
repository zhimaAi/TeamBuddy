'use strict'

const fs = require('node:fs')
const path = require('node:path')
const { randomUUID } = require('node:crypto')

// 现场排障日志：每次打开独立编号，最多保留当前文件及一个 1 MiB 备份。
// 调用方只传选定字段，不传提示词、完整环境或第三方错误正文。
function createQoderDiagnostics(stateRoot) {
  const requestId = randomUUID()
  const file = path.join(stateRoot, 'logs', 'qoder-ide-debug.log')
  let warned = false
  return (event, details = {}) => {
    try {
      const line = JSON.stringify({ time: new Date().toISOString(), requestId, event, details }) + '\n'
      if (Buffer.byteLength(line) > 32 * 1024) return
      fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 })
      let size = 0
      try { size = fs.statSync(file).size } catch (error) { if (error.code !== 'ENOENT') throw error }
      if (size + Buffer.byteLength(line) > 1024 * 1024) {
        try { fs.unlinkSync(file + '.1') } catch (error) { if (error.code !== 'ENOENT') throw error }
        fs.renameSync(file, file + '.1')
      }
      fs.appendFileSync(file, line, { encoding: 'utf8', mode: 0o600 })
    } catch {
      // 排障日志不可改变启动结果；磁盘错误只提示一次，不递归写日志。
      if (!warned) { warned = true; console.warn('Qoder IDE diagnostic log could not be written') }
    }
  }
}

module.exports = { createQoderDiagnostics }
