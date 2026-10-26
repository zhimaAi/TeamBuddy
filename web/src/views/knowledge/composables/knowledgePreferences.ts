export type KnowledgeEditorMode = 'visual' | 'source'
export const KNOWLEDGE_EDITOR_MODE_KEY = 'knowledge-editor-mode-v1'

// 编辑偏好独立于已打开的标签页保存，关闭文档或切换知识库后仍可沿用。
export function readKnowledgeEditorMode(): KnowledgeEditorMode {
  try {
    return localStorage.getItem(KNOWLEDGE_EDITOR_MODE_KEY) === 'source' ? 'source' : 'visual'
  } catch {
    return 'visual'
  }
}

export type KnowledgeAppearance = { fontSize: number; lineHeight: number; textColor: string }
export const DEFAULT_KNOWLEDGE_APPEARANCE: KnowledgeAppearance = {
  fontSize: 15, lineHeight: 1.2, textColor: '#262626',
}

export const KNOWLEDGE_APPEARANCE_LIMITS = {
  fontSize: { min: 10, max: 48, step: 1 },
  lineHeight: { min: 0, max: 4, step: 0.05 },
} as const

export function isKnowledgeFontSize(value: unknown): value is number {
  const { min, max } = KNOWLEDGE_APPEARANCE_LIMITS.fontSize
  return typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max
}

export function isKnowledgeLineHeight(value: unknown): value is number {
  const { min, max } = KNOWLEDGE_APPEARANCE_LIMITS.lineHeight
  return typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max
}

export function normalizeKnowledgeTextColor(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const color = value.trim().toLowerCase()
  if (/^#[0-9a-f]{6}$/.test(color)) return color
  if (/^#[0-9a-f]{3}$/.test(color)) {
    return `#${[...color.slice(1)].map((character) => character.repeat(2)).join('')}`
  }
  return null
}

export function readKnowledgeAppearance(): KnowledgeAppearance {
  try {
    const value = JSON.parse(localStorage.getItem('knowledge-appearance-v1') || 'null')
    return {
      fontSize: isKnowledgeFontSize(value?.fontSize)
        ? value.fontSize : DEFAULT_KNOWLEDGE_APPEARANCE.fontSize,
      lineHeight: isKnowledgeLineHeight(value?.lineHeight)
        ? value.lineHeight : DEFAULT_KNOWLEDGE_APPEARANCE.lineHeight,
      textColor: normalizeKnowledgeTextColor(value?.textColor) ?? DEFAULT_KNOWLEDGE_APPEARANCE.textColor,
    }
  } catch {
    return { ...DEFAULT_KNOWLEDGE_APPEARANCE }
  }
}

// v1 仅保存文件标识和界面状态；正文每次从当前知识库读取。根目录变化时自然隔离旧状态。
export type KnowledgeWorkspacePreference = {
  version: 1
  documents: Array<{ uuid: string }>
  activeUUID: string
  expandedKeys: string[]
}
export function workspacePreferenceKey(root: string) {
  return `knowledge-workspace-v1:${root}`
}
export function readWorkspacePreference(root: string): KnowledgeWorkspacePreference | null {
  try {
    const value = JSON.parse(localStorage.getItem(workspacePreferenceKey(root)) || 'null')
    if (value?.version !== 1 || !Array.isArray(value.documents)) return null
    const seen = new Set<string>()
    const documents: KnowledgeWorkspacePreference['documents'] = []
    for (const item of value.documents) {
      if (typeof item?.uuid !== 'string' || !item.uuid || seen.has(item.uuid)) continue
      seen.add(item.uuid)
      documents.push({ uuid: item.uuid })
    }
    return {
      version: 1, documents,
      activeUUID: typeof value.activeUUID === 'string' ? value.activeUUID : '',
      expandedKeys: Array.isArray(value.expandedKeys)
        ? value.expandedKeys.filter((key: unknown): key is string => typeof key === 'string') : [],
    }
  } catch {
    return null
  }
}
