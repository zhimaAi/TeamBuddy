import MarkdownIt from 'markdown-it'
import apiClient from '@/api/client'
import type { TaskFileContentResponse } from '@/types/task-files'

const markdown = new MarkdownIt({ html: true })
const imageMimeSubtypes: Record<string, string> = { jpg: 'jpeg', svg: 'svg+xml', ico: 'x-icon' }

function cloudImageKey(src: string) {
  try {
    const url = new URL(src)
    if (!['http:', 'https:'].includes(url.protocol)) return ''
    return `${url.origin}${url.pathname}`
  } catch {
    return ''
  }
}

function signedImage(src: string) {
  try {
    return [...new URL(src).searchParams.keys()].some((key) => key.toLowerCase() === 'x-amz-signature')
  } catch {
    return false
  }
}

function descriptionImages(content: string) {
  const images = new Map<string, string>()
  const remember = (src: string) => {
    const key = cloudImageKey(src)
    if (key) images.set(key, src)
  }
  const tokens = markdown.parse(content, {})
  for (const token of tokens.flatMap((block) => [block, ...(block.children || [])])) {
    if (token.type === 'image') remember(token.attrGet('src') || '')
    if (token.type === 'html_block' || token.type === 'html_inline') {
      const document = new DOMParser().parseFromString(token.content, 'text/html')
      document.querySelectorAll('img[src]').forEach((image) => remember(image.getAttribute('src') || ''))
    }
  }
  return images
}

export function taskDocumentImagePath(src: string, documentPath: string, taskDirectory: string) {
  if (/^[a-z][a-z\d+.-]*:/i.test(src) && !/^[a-z]:[\\/]/i.test(src)) return ''
  let path: string
  try {
    path = decodeURIComponent(src.replace(/[?#].*$/, '')).replace(/\\/g, '/')
  } catch {
    return ''
  }
  const root = taskDirectory.replace(/\\/g, '/').replace(/\/$/, '')
  if (root && path.startsWith(`${root}/`)) {
    path = path.slice(root.length + 1)
  } else {
    if (/^(?:[a-z][a-z\d+.-]*:|\/)/i.test(path)) return ''
    const directory = documentPath.replace(/\\/g, '/').split('/').slice(0, -1)
    path = [...directory, path].join('/')
  }
  // 只在当前任务目录内解析，禁止向上越出根目录或使用其他本机绝对路径。
  const parts: string[] = []
  for (const part of path.split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      if (!parts.length) return ''
      parts.pop()
    } else {
      parts.push(part)
    }
  }
  return parts.join('/')
}

export function createTaskDocumentImageResolver(taskUuid: string, documentPath: string, taskDirectory: string) {
  const requests = new Map<string, Promise<string>>()
  let freshImages: Promise<Map<string, string>> | undefined
  const taskPath = `/tasks/${encodeURIComponent(taskUuid)}`
  return (src: string): Promise<string> => {
    const existing = requests.get(src)
    if (existing) return existing
    const request = (async () => {
      if (signedImage(src)) {
        // 任务详情接口会刷新云端图片签名；只匹配同源同对象地址，不用描述覆盖文档。
        freshImages ||= apiClient.get<{ description: string }>(taskPath)
          .then((task) => descriptionImages(task.description || ''))
        return (await freshImages).get(cloudImageKey(src)) || src
      }
      const path = taskDocumentImagePath(src, documentPath, taskDirectory)
      if (!path) return src
      const file = await apiClient.get<TaskFileContentResponse>(`${taskPath}/files/content`, { path })
      if (file.encoding !== 'base64') throw new Error('Expected base64 image content')
      const type = file.file_type.replace(/^\./, '').toLowerCase()
      const mime = imageMimeSubtypes[type] || type
      return `data:image/${mime};base64,${file.content}`
    })()
    requests.set(src, request)
    return request
  }
}
