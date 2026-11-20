import type MarkdownIt from 'markdown-it'

const MAX_FILE_REFERENCE_LENGTH = 4096
const LINE_CITATION = /(?::\d+){1,2}(?:\s*[-–—]\s*\d+)?$/
const LINE_FRAGMENT = /#L?\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?$/i
const FILE_NAME = /^[^\\/:*?"<>|]+\.[A-Za-z0-9]{1,16}$/

export const LOCAL_FILE_LINK_ATTRIBUTE = 'data-local-file-ref'
export const LOCAL_FILE_SESSION_ATTRIBUTE = 'data-local-file-session'

export function allowLocalFileLinks(markdown: MarkdownIt) {
  const validateLink = markdown.validateLink
  markdown.validateLink = (url: string) => /^file:/i.test(url.trim()) || validateLink(url)
  // Windows 路径里的反斜杠不是 Markdown 转义符，必须在解析链接目标时保留。
  const parseDestination = markdown.helpers.parseLinkDestination
  Object.assign(markdown, { helpers: { ...markdown.helpers, parseLinkDestination: (text: string, start: number, max: number) => {
    const result = parseDestination(text, start, max)
    if (result.ok) {
      const angle = text[start] === '<'
      const raw = text.slice(start + Number(angle), result.pos - Number(angle))
      if (/^[A-Za-z]:[\\/]/.test(raw)) result.str = raw
    }
    return result
  } } })
}

export function taskFileRoots(task?: {
  task_dir?: string
  work_dir?: string
  work_dirs?: string[]
} | null) {
  if (!task) return []
  return [task.task_dir, task.work_dir, ...(task.work_dirs || [])]
    .map((value) => value?.trim() || '')
    .filter(Boolean)
}

// 仅处理原文已有的 Markdown 链接，URL 解码一次；普通文本和行内代码不生成文件链接。
export function localFileTarget(raw: string) {
  const source = raw.trim()
  if (!source || source.length > MAX_FILE_REFERENCE_LENGTH || source.includes('\0') || /[\r\n]/.test(source)) {
    return null
  }
  let value = source
  if (/^file:/i.test(source)) {
    value = fileURLPath(source)
    if (!value) return null
  } else {
    try { value = decodeURIComponent(source.replace(LINE_FRAGMENT, '').replace(LINE_CITATION, '')) } catch { return null }
  }
  if (!value || /[\0\r\n]/.test(value)) return null
  if (value.startsWith('\\\\') || value.startsWith('//') || value.includes('://')) return null
  if (isAbsolutePath(value)) return FILE_NAME.test(value.split(/[\\/]/).pop() || '') ? value : null
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(value)) return null
  return isFileLikeRelative(value) ? value : null
}

function fileURLPath(value: string) {
  if (!/^file:/i.test(value)) return ''
  try {
    const url = new URL(value)
    if (url.protocol !== 'file:' || (url.hostname && url.hostname !== 'localhost') || url.search || url.hash && !LINE_FRAGMENT.test(url.hash)) return ''
    let path = decodeURIComponent(url.pathname.replace(LINE_CITATION, ''))
    if (/^\/[A-Za-z]:\//.test(path)) path = path.slice(1)
    return path && path !== '/' ? path : ''
  } catch {
    return ''
  }
}

function isAbsolutePath(value: string) {
  return value.startsWith('/') || /^[A-Za-z]:[\\/]/.test(value)
}

function isFileLikeRelative(value: string) {
  if (value === '.' || value === '..' || value.includes('*')) return false
  const parts = value.split(/[\\/]/)
  if (parts.includes('..')) return false
  const base = parts.pop() || ''
  return FILE_NAME.test(base)
}
