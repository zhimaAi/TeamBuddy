import { computed, ref } from 'vue'
import apiClient, { ApiError } from '@/api/client'
import { applySession, fetchSession } from '@/composables/useCloudSession'
import { t } from '@/i18n'
import { ipcErrorMessage } from '@/utils/ipcError'

/** 浏览器登录：客户端申请会话 → 系统浏览器完成登录 → 深链接回注登录态。 */
interface BrowserLoginStartResponse {
  state: string
  browser_url: string
  expires_in: number
}

type BrowserLoginBackendStatus = 'pending' | 'succeeded' | 'failed' | 'expired' | 'cancelled'

interface BrowserLoginStatusResponse {
  status: BrowserLoginBackendStatus
  user?: { id: string; username: string; display_name?: string; avatar?: string; role?: string }
  admin_id?: string
  error?: string
}

// 轮询间隔：浏览器里的登录/注册需要用户操作，1.5 秒足够及时又不打扰后端。
const POLL_INTERVAL_MS = 1500
const POLL_TIMEOUT_MS = 10 * 60 * 1000

// 官方和自部署服务共用同一条浏览器登录链路。
export interface BrowserLoginOptions { serverType?: 'official' | 'custom'; serverUrl?: string }

// 侧边栏账号入口与团队工作登录引导卡片共用同一次登录流程，所以状态放在模块级单例，
// 而不是每个组件各起一份轮询。
const pending = ref(false)
const errorMessage = ref('')
const state = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null
let deadline = 0
let generation = 0
let pollInFlight: number | null = null
let desktopUnsubscribe: (() => void) | null = null
let settleResolvers: Array<(ok: boolean) => void> = []

function stopPolling() {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  desktopUnsubscribe?.()
  desktopUnsubscribe = null
}

function settle(ok: boolean, message = '') {
  generation += 1
  stopPolling()
  state.value = ''
  pending.value = false
  errorMessage.value = ok ? '' : message
  const resolvers = settleResolvers
  settleResolvers = []
  for (const resolve of resolvers) resolve(ok)
}

function waitForSettle(): Promise<boolean> {
  return new Promise((resolve) => {
    settleResolvers.push(resolve)
  })
}

// Only the requested login state can complete a switch. The previous account
// may remain online while authentication is pending, so a generic session probe is insufficient.
async function pollOnce() {
  if (!state.value) return
  const requestGeneration = generation
  const requestedState = state.value
  if (Date.now() > deadline) {
    settle(false, t('teamwork.login.expired'))
    return
  }
  if (pollInFlight === requestGeneration) return
  pollInFlight = requestGeneration
  try {
    const result = await apiClient.get<BrowserLoginStatusResponse>('/auth/browser-login/status', { state: requestedState })
    if (requestGeneration !== generation || requestedState !== state.value) return
    if (result.status === 'pending') return
    if (result.status === 'succeeded') {
      const session = await fetchSession()
      if (requestGeneration === generation && requestedState === state.value) {
        applySession(session)
        settle(true)
      }
      return
    }
    settle(false, result.status === 'failed' ? result.error || t('teamwork.login.failed') : t('teamwork.login.expired'))
  } catch (error) {
    if (requestGeneration === generation && error instanceof ApiError && error.status === 404) {
      settle(false, t('teamwork.login.expired'))
    }
  } finally {
    if (pollInFlight === requestGeneration) pollInFlight = null
  }
}

function startPolling() {
  stopPolling()
  deadline = Date.now() + POLL_TIMEOUT_MS
  pollTimer = setInterval(() => {
    void pollOnce()
  }, POLL_INTERVAL_MS)
  void pollOnce()

  // Electron 主进程收到 teamsboard:// 深链接并完成回注后会主动推送，界面无需等下一次轮询。
  // 推送只是提示：登录态以会话接口为准，避免重复投递或时序竞争导致误报成功。
  desktopUnsubscribe = window.goteamsDesktop?.onBrowserLoginResult?.(() => {
    if (pending.value) void pollOnce()
  }) ?? null
}

/**
 * 发起浏览器登录。返回是否登录成功，便于调用方在成功后继续下一步。
 * 重复调用时复用进行中的流程。
 */
async function startBrowserLogin(options: BrowserLoginOptions = {}): Promise<boolean> {
  if (pending.value) return waitForSettle()
  errorMessage.value = ''
  pending.value = true
  const completion = waitForSettle()
  const startGeneration = generation
  let createdState = ''
  let browserUrl = ''
  try {
    const desktop = window.goteamsDesktop
    if (desktop?.startBrowserLogin) {
      const result = await desktop.startBrowserLogin(options)
      createdState = result.state
    } else {
      const result = await apiClient.post<BrowserLoginStartResponse>('/auth/browser-login/start', {
        server_type: options.serverType,
        server_url: options.serverUrl,
      })
      createdState = result.state
      browserUrl = result.browser_url
    }
    if (startGeneration !== generation) {
      try { await apiClient.post('/auth/browser-login/cancel', { state: createdState }) } catch { /* The server also enforces expiration. */ }
      return completion
    }
    state.value = createdState
    if (browserUrl) window.open(browserUrl, '_blank', 'noopener')
    startPolling()
  } catch (error) {
    if (startGeneration === generation) settle(false, ipcErrorMessage(error, t('teamwork.login.failed')))
  }
  return completion
}

/** 取消进行中的浏览器登录（关闭弹窗、切换页面等）。 */
async function cancelBrowserLogin(): Promise<void> {
  const current = state.value
  const requestGeneration = generation
  if (!current) {
    if (pending.value) settle(false)
    return
  }
  try {
    const result = await apiClient.post<{ status?: BrowserLoginBackendStatus }>('/auth/browser-login/cancel', { state: current })
    if (requestGeneration !== generation || current !== state.value) return
    if (result.status === 'succeeded') {
      const session = await fetchSession()
      if (requestGeneration === generation && current === state.value) {
        applySession(session)
        settle(true)
      }
      return
    }
    if (result.status === 'pending') {
      // 深链接已开始兑换，继续等回注结果。
      return
    }
  } catch {
    // 取消失败不影响本地收尾，会话会在后端超时后自动过期。
  }
  if (requestGeneration === generation) settle(false)
}

export function useBrowserLogin() {
  return {
    pending: computed(() => pending.value),
    errorMessage: computed(() => errorMessage.value),
    startBrowserLogin,
    cancelBrowserLogin,
  }
}
