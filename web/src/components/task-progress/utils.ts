import type { TaskProgress } from '@/types/pipeline'
import { normalizeClipboardText } from '@/utils/clipboard'
import { t } from '@/i18n'

const TASK_IMAGE_MARKER_RE = /\[\[TASK_IMAGE_[^\]]+\]\]/g
// 历史消息在引入 display_content 前会持久化执行用的绝对路径。仅识别
// 我们生成的内部附件路径，避免复制/展示时泄露实现细节。
const TASK_IMAGE_ATTACHMENT_PATH_RE =
  /^\s*(?:[-*]\s*)?.*[/\\]\.goteams-attachments[/\\][0-9a-f-]+\.(?:png|jpe?g|webp|gif)\s*$/gim

export function isUserMessage(item: TaskProgress) {
  return item.record_type === 'user_question' || Boolean(item.user_prompt || item.question)
}

export function initials(name?: string) {
  return (name || 'A').trim().slice(0, 1).toUpperCase()
}

export function resultText(item: TaskProgress) {
  return item.final_result || item.result || (item.status === 'running' || item.status === 'created'
    ? t('components.taskProgress.running')
    : t('components.taskProgress.noResult'))
}

export function sanitizeUserMessageText(value: string) {
  return normalizeClipboardText(
    value
      .replace(TASK_IMAGE_MARKER_RE, '')
      .replace(TASK_IMAGE_ATTACHMENT_PATH_RE, t('components.taskProgress.pastedImage')),
  )
}

export function userMessageText(item: TaskProgress) {
  return sanitizeUserMessageText(item.user_prompt || item.question || '')
}

// CLI 类型 → 展示名。执行过程汇总行与执行控制台共用同一份映射，避免两处漂移。
const CLI_DISPLAY_NAMES: Record<string, string> = {
  claude: 'Claude Code',
  codebuddy: 'CodeBuddy Code',
  codex: 'Codex CLI',
  cursor: 'Cursor CLI',
  opencode: 'OpenCode',
  copilot: 'GitHub Copilot CLI',
  grok: 'Grok CLI',
  hermes: 'Hermes',
  kimi: 'Kimi Code',
  qoder: 'Qoder CLI',
  qoder_ide: 'Qoder IDE',
  'qoder-cn': 'Qoder CLI (CN)',
  qwen: 'Qwen Code',
  openclaw: 'OpenClaw',
  pi: 'Pi Agent',
  antigravity: 'Antigravity CLI',
}

export function cliDisplayName(cliType?: string) {
  const key = (cliType || '').trim()
  if (!key) return 'CLI'
  if (key === 'generic') return t('workflows.task.assign.vibeTools.generic')
  return CLI_DISPLAY_NAMES[key] || key.toUpperCase() || 'CLI'
}
