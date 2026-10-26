import codexAppLogo from '@/assets/icons/codex-logo.svg'
import genericLogo from '@/assets/icons/knowledge-settings.svg'
import antigravityLogo from '@/assets/icons/vibe-antigravity.svg'
import { cliLogo } from '@/composables/useCliLogo'

export const VIBE_CODING_TOOLS = [
  { id: 'codex', cliType: '', launch: 'app' },
  { id: 'qoder_ide', cliType: '', launch: 'app' },
  { id: 'generic', cliType: '', launch: 'clipboard' },
  { id: 'codex_cli', cliType: 'codex', launch: 'terminal' },
  { id: 'pi', cliType: 'pi', launch: 'terminal' },
  { id: 'antigravity', cliType: 'antigravity', launch: 'terminal' },
  { id: 'qoder', cliType: 'qoder', launch: 'terminal' },
  { id: 'codebuddy', cliType: 'codebuddy', launch: 'terminal' },
  { id: 'claude', cliType: 'claude', launch: 'terminal' },
  // 手动模式：客户端不启动进程，只生成可复制包（MCP 配置 + Skill 路径 + 任务提示词），
  // 由用户在外部工具（Cursor / WorkBuddy / DSH）中粘贴执行。
  { id: 'cursor', cliType: '', launch: 'manual' },
  { id: 'workbuddy', cliType: '', launch: 'manual' },
  { id: 'dsh', cliType: '', launch: 'manual' },
] as const

export type VibeCodingToolId = (typeof VIBE_CODING_TOOLS)[number]['id']

const RECENT_VIBE_TOOL_KEY = 'goteams.vibe-coding.recent-tool'

export function supportsVibeModel(tool: string) {
  return ['codex_cli', 'pi', 'antigravity', 'qoder', 'codebuddy'].includes(tool)
}

export function vibeToolDefinition(tool: string) {
  return VIBE_CODING_TOOLS.find(item => item.id === tool)
}

export function isVibeCodingTool(tool: string): tool is VibeCodingToolId {
  return Boolean(vibeToolDefinition(tool))
}

/**
 * 手动模式工具：客户端不启动任何进程，只生成可复制包（MCP 配置 + Skill 路径 + 任务提示词），
 * 由用户在外部工具中粘贴执行。因此不参与可执行文件探测，也不由客户端打开终端。
 */
export function isManualVibeTool(tool: string) {
  return VIBE_CODING_TOOLS.some((item) => item.id === tool && item.launch === 'manual')
}

/**
 * 手动模式下能走 AiBridge 扩展下发提示词的工具：只有宿主为编辑器的 Cursor。
 * WorkBuddy / DSH 没有对应扩展，面板不出现「发送到 Cursor」，也不提这条捷径。
 * 与后端 vibeSupportsAiBridge 的判定保持一致。
 */
export function vibeToolSupportsAiBridge(tool: string) {
  return tool === 'cursor'
}

/**
 * 手动模式各工具的 MCP 配置接入方式。
 * - `file`：把 mcpServers JSON 写进工具自己的 mcp.json（Cursor / WorkBuddy）。
 * - `panel`：不碰配置文件，改用工具自带的可视化插件添加服务器（DSH 的配置由 profile
 *   下的 cordis.patch.yml 管理，手改易与插件互相覆盖，因此只引导走插件界面）。
 */
export type VibeMcpSetup = 'file' | 'panel'

const MCP_SETUP: Partial<Record<VibeCodingToolId, VibeMcpSetup>> = {
  dsh: 'panel',
}

export function vibeToolMcpSetup(tool: string): VibeMcpSetup {
  return isVibeCodingTool(tool) ? MCP_SETUP[tool] || 'file' : 'file'
}

/**
 * 手动模式各工具推荐的 MCP 配置写入路径，仅用于展示与复制。
 * sidecar 返回 mcp_config_path 时以后者为准，这里只是本地兜底默认值。
 * 走可视化插件的工具（DSH）没有可写路径，不在此表中。
 */
const MANUAL_MCP_PATHS: Partial<Record<VibeCodingToolId, string>> = {
  cursor: '~/.cursor/mcp.json',
  workbuddy: '~/.workbuddy/mcp.json',
}

export function vibeToolMcpConfigPath(tool: string) {
  return isVibeCodingTool(tool) ? MANUAL_MCP_PATHS[tool] || '' : ''
}

export function vibeToolLabelKey(tool: string) {
  return isVibeCodingTool(tool)
    ? `workflows.task.assign.vibeTools.${tool}`
    : 'workflows.task.assign.vibeCodingMode'
}

export function vibeToolDisplayLabel(tool: string, label: string, name?: string) {
  return tool === 'generic' && name?.trim() ? `${label} · ${name.trim()}` : label
}

// Codex 应用保留彩色应用标识；Cursor / DSH / WorkBuddy 使用各自官方图标（DSH 为品牌蓝
// 矢量鲸鱼，WorkBuddy 为官方方形应用图标），其余工具与 CLI 选择器共用同一套厂家图标。
const VIBE_TOOL_LOGOS: Record<VibeCodingToolId, string> = {
  codex: codexAppLogo,
  generic: genericLogo,
  codex_cli: cliLogo('codex'),
  pi: cliLogo('pi'),
  antigravity: antigravityLogo,
  qoder: cliLogo('qoder'),
  qoder_ide: cliLogo('qoder'),
  codebuddy: cliLogo('codebuddy'),
  claude: cliLogo('claude'),
  cursor: cliLogo('cursor'),
  workbuddy: cliLogo('workbuddy'),
  dsh: cliLogo('dsh'),
}

export function vibeToolLogo(tool: string) {
  return isVibeCodingTool(tool) ? VIBE_TOOL_LOGOS[tool] : codexAppLogo
}

export function readRecentVibeTool() {
  try {
    const value = localStorage.getItem(RECENT_VIBE_TOOL_KEY) || ''
    return isVibeCodingTool(value) ? value : ''
  } catch {
    return ''
  }
}

export function writeRecentVibeTool(tool: string) {
  if (!isVibeCodingTool(tool)) return
  try {
    localStorage.setItem(RECENT_VIBE_TOOL_KEY, tool)
  } catch {
    // 最近使用只影响角标，存储失败时保持当前选择可用。
  }
}
