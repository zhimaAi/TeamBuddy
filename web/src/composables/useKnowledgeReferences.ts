import {
  getKnowledgeDocument,
  getKnowledgeDocumentPath,
  spillTaskReferenceFile,
} from '@/api/knowledge'
import type { KnowledgeDocument, KnowledgeFolderNode } from '@/api/knowledge'
import type {
  KnowledgeReferenceFragment,
  KnowledgeReferenceValidation,
  ReferenceSpillResult,
} from '@/types/knowledge-reference'

/**
 * 知识库引用的内容获取、有效性校验、码点计数、落盘调用与引用块拼装。
 *
 * 这里是唯一的实现点：引用块的文本格式、长度口径与错误处理都在此收口，
 * 避免在 ChatComposer 里重复实现（02/output_design.md §3.5.4、§3.7）。
 */

/** CF-9：引用内容超过该码点数时降级为产出文件 */
export const KNOWLEDGE_REFERENCE_CHAR_LIMIT = 10000

/** 引用标签与提交内容共用真实文件名，路径缺失时用标题和扩展名补齐。 */
export function knowledgeDocumentName(document: Pick<KnowledgeDocument, 'file_path' | 'title' | 'ext'>) {
  const name = document.file_path.replace(/\\/g, '/').split('/').filter(Boolean).pop()
  if (name) return name
  const ext = document.ext.replace(/^\./, '')
  if (!ext || document.title.toLowerCase().endsWith(`.${ext.toLowerCase()}`)) return document.title
  return `${document.title}.${ext}`
}

/** CF-9：长度统一按 Unicode 码点计数（不是 UTF-16 码元长度） */
export function countCharacters(text: string): number {
  return Array.from(text).length
}

export interface KnowledgeReferenceBlock {
  /** 含扩展名的文件名 */
  name: string
  /** 所属文件夹路径，根为 teamsboard */
  folderPath: string
  /** 选中片段；null 表示整篇文档 */
  fragment: KnowledgeReferenceFragment | null
  /** 引用正文（片段或整篇） */
  content: string
  /** 落盘后的产出文件绝对路径；不传表示未触发降级 */
  spilledPath?: string
}

/**
 * S-IN-09：引用有效性判定。
 * 文档已移入回收站或磁盘文件不存在时 `path` 接口返回 `exists=false`；
 * 接口异常（404 / 网络）同样按失效处理，避免把读不到的内容静默塞进消息。
 */
export async function validateKnowledgeReference(
  uuid: string,
): Promise<KnowledgeReferenceValidation> {
  try {
    const result = await getKnowledgeDocumentPath(uuid)
    if (result.exists) return { valid: true, reason: '' }
    return {
      valid: false,
      reason: result.absolute_path ? 'knowledge file missing on disk' : 'knowledge document missing',
    }
  } catch (error) {
    return {
      valid: false,
      reason: error instanceof Error ? error.message : 'knowledge document validation failed',
    }
  }
}

/**
 * 取引用内容：有选中片段时用片段（S-UI-13 / D3-H2），否则取整篇正文
 * （「从资料库中选择」整篇引用，S-UI-19/20）。
 */
export async function loadKnowledgeReferenceContent(
  uuid: string,
  fragment: KnowledgeReferenceFragment | null,
): Promise<string> {
  if (fragment) return fragment.text
  const document = await getKnowledgeDocument(uuid)
  return document.content || ''
}

/**
 * S-IN-08 / S-DA-12：超长引用内容落盘为产出文件。
 * 失败时向上抛出真实错误，由调用方提示并中止提交（边界 E13：不静默丢失引用内容）。
 */
export function spillKnowledgeReference(
  taskUuid: string,
  fileName: string,
  content: string,
): Promise<ReferenceSpillResult> {
  return spillTaskReferenceFile(taskUuid, { file_name: fileName, content })
}

/**
 * S-IN-11：由既有 `/knowledge/folders` 树派生「文件夹 id → 位置路径」。
 * 选择器的「位置」列与引用块的来源路径共用同一份派生逻辑。
 *
 * 节点 name 为空字符串（如数据中夹带的占位节点）会被跳过，避免链路中
 * 留下 ` / ` 痕迹、从根一路放大成 `知识库 / TeamBuddy /  / 文件名`。
 */
export function buildKnowledgeFolderPaths(
  nodes: KnowledgeFolderNode[],
  parents: string[] = [],
  result = new Map<number, string>(),
): Map<number, string> {
  for (const node of nodes) {
    if (!node.name.trim()) continue
    const chain = [...parents, node.name]
    result.set(node.id, chain.join(' / '))
    buildKnowledgeFolderPaths(node.children || [], chain, result)
  }
  return result
}

/** 范围描述：整篇 / 选中片段（带 DOM 级前后文定位片段） */
function describeRange(fragment: KnowledgeReferenceFragment | null) {
  if (!fragment) return '整篇文档'
  const parts: string[] = []
  if (fragment.prefix) parts.push(`前文："${fragment.prefix}"`)
  if (fragment.suffix) parts.push(`后文："${fragment.suffix}"`)
  return parts.length ? `选中片段（${parts.join('，')}）` : '选中片段'
}

/**
 * S-IN-12：folderPath 中可能夹杂空段（根目录 folder_id=0 时折叠成「知识库 / 」，
 * 或 buildKnowledgeFolderPaths 链路中存在空名称），拼装前过滤空段，
 * 避免 Agent 看到「知识库 / TeamBuddy /  / 文件名」这种带空目录痕迹的来源路径。
 */
function compactFolderPath(path: string): string {
  return path
    .split(' / ')
    .filter((segment) => segment.trim().length > 0)
    .join(' / ')
}

/**
 * 拼装引用块（02/output_design.md §3.5.4 规定的文本格式）。
 *
 * 注意：该文本进的是提交给 Agent 的 `content`，不是会话记录里展示的 `display_content`，
 * 因此沿用与「开始处理」等既有 prompt 文案一致的固定中文结构；
 * 面向用户的降级说明另由 `referenceSpillNote` 词条写入 `display_content`（S-UI-23 可感知）。
 */
export function buildKnowledgeReferenceBlock(input: KnowledgeReferenceBlock): string {
  const folderPath = compactFolderPath(input.folderPath)
  const sourceSegments = [folderPath, input.name].filter((segment) => segment.length > 0)
  const lines = [
    `【知识库引用】${input.name}`,
    `来源：${sourceSegments.join(' / ')}`,
    `范围：${input.spilledPath ? describeRange(null) : describeRange(input.fragment)}`,
  ]
  if (input.spilledPath) {
    lines.push('说明：内容超过 10000 字，已转存为产出文件', `文件：${input.spilledPath}`)
    return lines.join('\n')
  }
  lines.push('--- 引用内容开始 ---', input.content, '--- 引用内容结束 ---')
  return lines.join('\n')
}
