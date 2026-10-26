import type MarkdownIt from 'markdown-it'

const MAX_FILE_REFERENCE_LENGTH = 4096
const LINE_CITATION = /(?::\d+){1,2}(?:\s*[-–—]\s*\d+)?$/
const FILE_NAME = /^[^\\/:*?"<>|]+\.[A-Za-z0-9]{1,16}$/

export const LOCAL_FILE_LINK_ATTRIBUTE = 'data-local-file-ref'
export const LOCAL_FILE_SESSION_ATTRIBUTE = 'data-local-file-session'

export function allowLocalFileLinks(markdown: MarkdownIt) {
  const validateLink = markdown.validateLink
  markdown.validateLink = (url: string) => /^file:/i.test(url.trim()) || validateLink(url)
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

// 带扩展名的绝对路径都可以点。是否落在当前任务或其他已配置工作区，由本地 API 判断。含 .. 的相对路径、行号引用和目录不识别，避免任务目录和工作目录各算出一个位置后报错。
export function localFileTarget(raw: string, roots: string[] = []) {
  const source = raw.trim()
  if (!source || source.length > MAX_FILE_REFERENCE_LENGTH || source.includes('\0') || /[\r\n]/.test(source)) {
    return null
  }
  if (LINE_CITATION.test(source)) return null
  let value: string
  try {
    value = decodeURIComponent(source)
  } catch {
    return null
  }
  if (LINE_CITATION.test(value)) return null
  const lower = value.toLowerCase()
  if (
    lower.startsWith('http://') ||
    lower.startsWith('https://') ||
    lower.startsWith('mailto:') ||
    lower.startsWith('mention:') ||
    lower.startsWith('javascript:') ||
    lower.startsWith('data:') ||
    lower === 'file:' ||
    lower === 'file://' ||
    lower === 'file:///'
  ) {
    return null
  }
  const filePath = fileURLPath(value)
  if (filePath) {
    return fileAllowed(filePath, roots) ? source : null
  }
  if (value.startsWith('\\\\') || value.startsWith('//') || value.includes('://')) return null
  if (isAbsolutePath(value)) {
    return fileAllowed(value, roots) ? source : null
  }
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(value)) return null
  return isFileLikeRelative(value) ? source : null
}

export function renderInlineFileCode(
  markdown: MarkdownIt,
  content: string,
  roots: string[],
  title: string,
  sessionUuid = '',
) {
  const target = localFileTarget(content, roots)
  if (!target) return ''
  const escape = markdown.utils.escapeHtml
  const session = sessionUuid
    ? ` ${LOCAL_FILE_SESSION_ATTRIBUTE}="${escape(sessionUuid)}"`
    : ''
  return `<a href="#" class="markdown-file-link"${session} ${LOCAL_FILE_LINK_ATTRIBUTE}="${escape(target)}" title="${escape(title)}">${escape(content)}</a>`
}

function fileURLPath(value: string) {
  if (!/^file:/i.test(value)) return ''
  try {
    const url = new URL(value)
    if (url.protocol !== 'file:' || (url.hostname && url.hostname !== 'localhost')) return ''
    let path = decodeURIComponent(url.pathname)
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

function fileAllowed(path: string, roots: string[]) {
  const base = path.split(/[\\/]/).pop() || ''
  if (!FILE_NAME.test(base)) return false
  // 绝对路径可能跨到其他已配置工作区，前端没有那份目录清单。
  if (!roots.length || isAbsolutePath(path)) return true
  const normalized = path.replace(/\\/g, '/').replace(/\/+$/, '')
  return roots.some((root) => {
    const allowed = root.trim().replace(/\\/g, '/').replace(/\/+$/, '')
    return Boolean(allowed) && (normalized === allowed || normalized.startsWith(`${allowed}/`))
  })
}
