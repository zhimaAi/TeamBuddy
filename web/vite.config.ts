import { defineConfig, loadEnv, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { AntDesignVueResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'
import http from 'node:http'
import net from 'node:net'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const { resolveRuntimeJsonPath } = require('../desktop/shared/data-root.cjs') as {
  resolveRuntimeJsonPath: (home: string) => string
}

function runtimeJsonPath(): string {
  return resolveRuntimeJsonPath(os.homedir())
}

// 后端地址发现：优先环境变量显式指定（调试用），否则读取后端写入的 runtime.json，
// 按 mtime 缓存，后端重启换端口后自动刷新。
function createBackendTargetResolver(envTarget?: string): () => string | null {
  let cached: { path: string; mtimeMs: number; target: string } | null = null
  return () => {
    if (envTarget) return envTarget
    const runtimePath = runtimeJsonPath()
    try {
      const stat = fs.statSync(runtimePath)
      if (cached && cached.path === runtimePath && cached.mtimeMs === stat.mtimeMs) return cached.target
      const raw = fs.readFileSync(runtimePath, 'utf8')
      const data = JSON.parse(raw) as { address?: unknown }
      const address = typeof data.address === 'string' ? data.address.trim() : ''
      if (address) {
        cached = { path: runtimePath, mtimeMs: stat.mtimeMs, target: `http://${address}` }
        return cached.target
      }
    } catch {
      // runtime.json 尚未写入（后端未启动）或暂时不可读
    }
    return null
  }
}

function writeProxyError(res: http.ServerResponse, target: string | null) {
  const message = target
    ? `本地后端转发失败 (${target})，请确认后端已启动`
    : '本地后端未启动，请先运行 Go 后端（task dev1/dev2/cn/global）'
  res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ error: message }))
}

const VDITOR_ASSET_PATHS = [
  'js/lute',
  'js/i18n/zh_CN.js',
  'js/i18n/en_US.js',
  'js/icons/ant.js',
  'css/content-theme',
  'images',
]

function copyVditorPath(from: string, to: string) {
  const stat = fs.statSync(from)
  if (stat.isDirectory()) {
    fs.mkdirSync(to, { recursive: true })
    for (const entry of fs.readdirSync(from)) {
      copyVditorPath(path.join(from, entry), path.join(to, entry))
    }
    return
  }
  fs.mkdirSync(path.dirname(to), { recursive: true })
  fs.copyFileSync(from, to)
}

function copyVditorAssetsPlugin(): Plugin {
  const sourceRoot = path.resolve(__dirname, 'node_modules/vditor/dist')
  const targetRoot = path.resolve(__dirname, 'public/vditor/dist')

  function copyAssets() {
    for (const relativePath of VDITOR_ASSET_PATHS) {
      const from = path.join(sourceRoot, relativePath)
      if (!fs.existsSync(from)) {
        throw new Error(`缺少 Vditor 资源：${from}`)
      }
      copyVditorPath(from, path.join(targetRoot, relativePath))
    }
  }

  return {
    name: 'copy-vditor-assets',
    buildStart() {
      copyAssets()
    },
    configureServer() {
      copyAssets()
    },
  }
}

function localBackendProxyPlugin(resolveTarget: () => string | null): Plugin {
  return {
    name: 'goteams-local-backend-proxy',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url || !req.url.startsWith('/api/local')) {
          next()
          return
        }
        const target = resolveTarget()
        //console.log('[proxy] 请求到达 ' + req.method + ' ' + req.url + ' target=' + target)
        if (!target) return writeProxyError(res, null)
        let targetUrl: URL
        try {
          targetUrl = new URL(req.url || '/', target)
        } catch {
          return writeProxyError(res, target)
        }
        const proxyReq = http.request(
          {
            hostname: targetUrl.hostname,
            port: targetUrl.port,
            path: targetUrl.pathname + targetUrl.search,
            method: req.method,
            headers: { ...req.headers, host: targetUrl.host },
          },
          (proxyRes) => {
            res.writeHead(proxyRes.statusCode || 502, proxyRes.headers)
            proxyRes.pipe(res)
          },
        )
        proxyReq.on('error', () => {
          if (res.headersSent) {
            res.destroy()
          } else {
            writeProxyError(res, target)
          }
        })
        req.pipe(proxyReq)
      })

      // /api/local/ws 的 WebSocket 升级转发（其余路径交给 Vite 内置 HMR 等处理）
      server.httpServer?.on('upgrade', (req, socket, head) => {
        if (!req.url || !req.url.startsWith('/api/local')) return
        const target = resolveTarget()
        if (!target) return socket.destroy()
        let targetUrl: URL
        try {
          targetUrl = new URL(req.url, target)
        } catch {
          return socket.destroy()
        }
        const upstream = net.connect(Number(targetUrl.port), targetUrl.hostname, () => {
          const requestLine = `${req.method} ${targetUrl.pathname}${targetUrl.search} HTTP/1.1\r\n`
          const headerLines: string[] = []
          for (let i = 0; i < req.rawHeaders.length; i += 2) {
            headerLines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`)
          }
          upstream.write(`${requestLine}${headerLines.join('\r\n')}\r\n\r\n`)
          if (head.length > 0) upstream.write(head)
          socket.pipe(upstream).pipe(socket)
        })
        upstream.on('error', () => socket.destroy())
        socket.on('error', () => upstream.destroy())
        socket.on('close', () => upstream.destroy())
      })
    },
  }
}

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const port = Number(env.VITE_PORT) || 5173
  const resolveBackendTarget = createBackendTargetResolver(env.VITE_API_PROXY_TARGET)

  return {
    plugins: [
      vue(),
      Components({
        resolvers: [
          AntDesignVueResolver({
            importStyle: false, // css in js
          }),
        ],
      }),
      copyVditorAssetsPlugin(),
      localBackendProxyPlugin(resolveBackendTarget),
    ],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port,
      strictPort: true,
    },
  }
})
