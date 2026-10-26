/** * API request encapsulation * baseURL: /api/local */

import { getApiToken, invalidateApiToken } from './token'
import { getActivePinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { getCurrentLocale, t, type AppLocale } from '@/i18n'

const BASE_URL = import.meta.env.VITE_API_BASE_URL || '/api/local'

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  headers?: Record<string, string>
  params?: Record<string, string | number | boolean | undefined>
  signal?: AbortSignal
}

export interface ApiResponse<T = unknown> {
  code: number
  message: string
  warning?: string
  data: T
}

export interface ApiFeedback {
  kind: 'warning'
  message: string
  requestLocale: AppLocale
  responseLocale?: AppLocale
  staleLocale: boolean
}

type ApiFeedbackHandler = (feedback: ApiFeedback) => void

let apiFeedbackHandler: ApiFeedbackHandler | undefined
let accountScopeChangedHandler: (() => Promise<void>) | undefined
let accountScopeRecovery: Promise<void> | undefined

export function setAccountScopeChangedHandler(handler: () => Promise<void>) {
  accountScopeChangedHandler = handler
}

export function setApiFeedbackHandler(handler?: ApiFeedbackHandler) {
  apiFeedbackHandler = handler
}

export class ApiError extends Error {
  status: number
  code?: number | string
  /** 错误响应原样体：供 conflicts / rollback 等结构化错误字段渲染（S-IN-07） */
  payload?: unknown
  requestLocale?: AppLocale
  responseLocale?: AppLocale
  staleLocale: boolean

  constructor(
    message: string,
    status: number,
    code?: number | string,
    language?: {
      requestLocale: AppLocale
      responseLocale?: AppLocale
      staleLocale: boolean
    },
    payload?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.payload = payload
    this.requestLocale = language?.requestLocale
    this.responseLocale = language?.responseLocale
    this.staleLocale = language?.staleLocale ?? false
  }
}

function readResponseLocale(response: Response): AppLocale | undefined {
  const locale = response.headers.get('Content-Language')
  return locale === 'zh-CN' || locale === 'en-US' ? locale : undefined
}

function getLanguageContext(response: Response, requestLocale: AppLocale) {
  const responseLocale = readResponseLocale(response)
  return {
    requestLocale,
    responseLocale,
    staleLocale: getCurrentLocale() !== (responseLocale ?? requestLocale),
  }
}

function readRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null
    ? (value as Record<string, unknown>)
    : undefined
}

function readMessage(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function readCode(value: unknown): number | string | undefined {
  return typeof value === 'number' || typeof value === 'string' ? value : undefined
}

function emitWarning(warning: unknown, language: ReturnType<typeof getLanguageContext>) {
  const message = readMessage(warning)
  if (!message || !apiFeedbackHandler) return

  try {
    apiFeedbackHandler({
      kind: 'warning',
      message: language.staleLocale ? t('common.feedback.completedAfterLanguageChange') : message,
      ...language,
    })
  } catch {
    // warning 属于部分成功提示，展示失败不能把已成功的业务操作改判为失败。
  }
}

async function createApiError(response: Response, requestLocale: AppLocale): Promise<ApiError> {
  const language = getLanguageContext(response, requestLocale)
  let errorMessage = `HTTP ${response.status}: ${response.statusText}`
  let errorCode: number | string | undefined
  let errorPayload: Record<string, unknown> | undefined

  try {
    const errorBody = readRecord(await response.json())
    errorMessage = readMessage(errorBody?.error) || readMessage(errorBody?.message) || errorMessage
    errorCode = readCode(errorBody?.code)
    errorPayload = errorBody
  } catch {
    // 非 JSON 错误响应沿用 HTTP 状态文案。
  }

  if (language.staleLocale) {
    errorMessage = t('common.feedback.languageChangedRetry')
  }

  return new ApiError(errorMessage, response.status, errorCode, language, errorPayload)
}

function buildUrl(path: string, params?: RequestOptions['params']): string {
  const url = `${BASE_URL}${path.startsWith('/') ? path : `/${path}`}`
  if (!params) return url
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null) {
      search.append(key, String(value))
    }
  }
  const qs = search.toString()
  return qs ? `${url}?${qs}` : url
}

// Capture before awaiting token discovery: a delayed request must not silently
// become an operation on the account selected in the meantime.
function accountRequestHeaders(): Record<string, string> {
  if (!getActivePinia()) return {}
  const user = useAuthStore().cloudUser
  return {
    'X-GoTeams-Account-Scope': user?.accountScope || 'local',
    'X-GoTeams-Session-Generation': user?.sessionGeneration || 'local',
  }
}

// 只恢复仍与失败请求同代的窗口；迟到错误不能覆盖用户刚完成的新登录。
async function recoverAccountScope(error: ApiError, headers: Record<string, string>) {
  if (error.status !== 409 || error.code !== 'account_scope_changed' || !accountScopeChangedHandler) return
  const current = accountRequestHeaders()
  if (headers['X-GoTeams-Account-Scope'] !== current['X-GoTeams-Account-Scope'] ||
      headers['X-GoTeams-Session-Generation'] !== current['X-GoTeams-Session-Generation']) return
  if (!accountScopeRecovery) {
    accountScopeRecovery = accountScopeChangedHandler().catch(() => {
      // 恢复失败仍返回原业务错误，下次范围冲突可以重新探测。
    }).finally(() => { accountScopeRecovery = undefined })
  }
  await accountScopeRecovery
}

export async function request<T = unknown>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = 'GET', body, headers = {}, params, signal } = options

  const url = buildUrl(path, params)
  console.log('[api] ' + method + ' ' + url)

  // 页面可先挂载，但业务请求须等首次握手完成，不能把未知身份当成本地账号。
  if (!path.startsWith('/auth/') && getActivePinia() && !useAuthStore().sessionReady) await useAuthStore().waitForSessionReady()
  const isFormData = body instanceof FormData
  const finalHeaders: Record<string, string> = { ...headers, ...accountRequestHeaders() }
  if (!isFormData) finalHeaders['Content-Type'] = 'application/json'
  // /auth/* 是后端放行的握手/登录入口，不携带 token，避免引导请求依赖 token 就绪
  const needsToken = !path.startsWith('/auth/')
  if (needsToken) {
    const token = await getApiToken()
    if (token) finalHeaders['X-GoTeams-Api-Token'] = token
  }
  const requestLocale = getCurrentLocale()
  finalHeaders.lang = requestLocale

  const response = await fetchWithTokenRetry(url, {
    method,
    headers: finalHeaders,
    body: body ? (isFormData ? body : JSON.stringify(body)) : undefined,
    credentials: 'same-origin',
    signal,
    // 会话接口必须拿到实时状态；Electron 磁盘缓存曾缓存过本地后端的过期响应
    //（如 index.html），命中后请求根本不发出，导致登录态判断永远错误。
    cache: 'no-store',
  })

  if (!response.ok) {
    const error = await createApiError(response, requestLocale)
    await recoverAccountScope(error, finalHeaders)
    throw error
  }

  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) {
    return (await response.text()) as unknown as T
  }

  const result: ApiResponse<T> = await response.json()
  const language = getLanguageContext(response, requestLocale)

  // Only unified responses with code are considered envelopes; business objects of native APIs may also legally contain data fields.
  if (result.code !== undefined) {
    if (result.code !== 0 && result.code !== 200) {
      throw new ApiError(
        language.staleLocale
          ? t('common.feedback.languageChangedRetry')
          : result.message || 'Request failed',
        response.status,
        result.code,
        language,
      )
    }
    emitWarning(result.warning, language)
    return result.data !== undefined ? result.data : (result as unknown as T)
  }

  emitWarning(readRecord(result)?.warning, language)
  return result as unknown as T
}

async function requestBlob(path: string, params?: RequestOptions['params']): Promise<Blob> {
  const url = buildUrl(path, params)
  if (getActivePinia() && !useAuthStore().sessionReady) await useAuthStore().waitForSessionReady()
  const headers: Record<string, string> = accountRequestHeaders()
  const token = await getApiToken()
  if (token) headers['X-GoTeams-Api-Token'] = token
  const requestLocale = getCurrentLocale()
  headers.lang = requestLocale
  const response = await fetchWithTokenRetry(url, {
    method: 'GET',
    headers,
    credentials: 'same-origin',
    cache: 'no-store',
  })
  if (!response.ok) {
    const error = await createApiError(response, requestLocale)
    await recoverAccountScope(error, headers)
    throw error
  }
  return response.blob()
}

// 后端重启后 token 会失效（dev 模式常见）：收到 403 时清除缓存、重取 token 并重试一次。
async function fetchWithTokenRetry(url: string, init: RequestInit): Promise<Response> {
  let response = await fetch(url, init)
  const sentToken = (init.headers as Record<string, string>)['X-GoTeams-Api-Token']
  if (response.status !== 403 || !sentToken) return response

  invalidateApiToken()
  const newToken = await getApiToken()
  if (!newToken || newToken === sentToken) return response

  const retryHeaders = { ...(init.headers as Record<string, string>) }
  retryHeaders['X-GoTeams-Api-Token'] = newToken
  response = await fetch(url, { ...init, headers: retryHeaders })
  return response
}

export const apiClient = {
  get: <T = unknown>(path: string, params?: RequestOptions['params'], signal?: AbortSignal) =>
    request<T>(path, { method: 'GET', params, signal }),

  getBlob: (path: string, params?: RequestOptions['params']) => requestBlob(path, params),

  post: <T = unknown>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),

  put: <T = unknown>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body }),

  delete: <T = unknown>(path: string, body?: unknown) =>
    request<T>(path, { method: 'DELETE', body }),
}

export default apiClient
