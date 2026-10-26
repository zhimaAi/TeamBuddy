// undefined 表示外部 URL 或页内锚点；null 表示非法路径或越过工作区边界。
export function resolveWorkspaceReference(source: string, documentPath: string): string | null | undefined {
  if (!source || source.startsWith('#') || source.startsWith('//') || /^[a-z][a-z\d+.-]*:/i.test(source)) {
    return undefined
  }
  let path: string
  try {
    path = decodeURIComponent(source.split(/[?#]/, 1)[0] || '').replace(/\\/g, '/')
  } catch {
    return null
  }
  if (path.includes('\0') || /^[a-z][a-z\d+.-]*:/i.test(path) || path.startsWith('//')) return null
  const parts = path.startsWith('/') ? [] : documentPath.split('/').slice(0, -1)
  for (const part of path.split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      if (!parts.length) return null
      parts.pop()
    } else {
      parts.push(part)
    }
  }
  return parts.join('/') || null
}
