'use strict'

const path = require('node:path')

const LOCATE_TIMEOUT_MS = 15000
const MAX_FILE_REFERENCE_LENGTH = 4096

function normalizeFileReference(value) {
  if (typeof value !== 'string') throw new TypeError('File reference must be a string')
  const fileRef = value.trim()
  if (!fileRef || fileRef.length > MAX_FILE_REFERENCE_LENGTH || fileRef.includes('\0')) {
    throw new Error('File reference is invalid')
  }
  return fileRef
}

function normalizeTaskUUID(value) {
  if (typeof value !== 'string' || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)) {
    throw new Error('Task UUID is invalid')
  }
  return value
}

function normalizeLocale(value) {
  return value === 'en-US' ? 'en-US' : 'zh-CN'
}

/**
 * @param {{ baseURL: string, apiToken: string, taskUuid: string, fileRef: string, sessionUuid?: string, locale?: string, fetchImpl?: (url: URL, options: { method: string, headers: Record<string, string>, body: string, signal: AbortSignal }) => Promise<{ ok: boolean, status: number, text: () => Promise<string> }> }} input
 */
async function locateTaskOutputFile(input) {
  const { baseURL, apiToken, taskUuid, fileRef, sessionUuid, locale } = input
  const fetchImpl = input.fetchImpl || fetch
  if (!baseURL || !apiToken) throw new Error('Go sidecar is not running')
  const taskID = normalizeTaskUUID(taskUuid)
  const reference = normalizeFileReference(fileRef)
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), LOCATE_TIMEOUT_MS)
  try {
    const response = await fetchImpl(new URL(`/api/local/tasks/${encodeURIComponent(taskID)}/files/locate`, baseURL), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-GoTeams-Api-Token': apiToken,
        'X-GoTeams-Lang': normalizeLocale(locale),
      },
      body: JSON.stringify({ path: reference, session_uuid: typeof sessionUuid === 'string' ? sessionUuid : '' }),
      signal: controller.signal,
    })
    const text = await response.text()
    let parsed = null
    try {
      parsed = text ? JSON.parse(text) : null
    } catch {
      parsed = null
    }
    if (!response.ok) {
      const message = parsed && typeof parsed.error === 'string' ? parsed.error : ''
      throw new Error(message || `HTTP ${response.status}`)
    }
    const located = parsed && typeof parsed.path === 'string' ? path.normalize(parsed.path) : ''
    if (!path.isAbsolute(located) || located.length > MAX_FILE_REFERENCE_LENGTH || located.includes('\0')) {
      throw new Error('Located file path is invalid')
    }
    return located
  } finally {
    clearTimeout(timer)
  }
}

module.exports = {
  locateTaskOutputFile,
  normalizeFileReference,
  normalizeLocale,
  normalizeTaskUUID,
}
