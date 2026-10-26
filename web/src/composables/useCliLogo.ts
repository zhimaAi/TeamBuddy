import cliFallback from '@/assets/icons/task-composer-cli.svg'
import antigravityLogo from '@/assets/icons/vibe-antigravity.svg'
import claudeLogo from '@/assets/icons/vibe-claude.svg'
import codebuddyLogo from '@/assets/icons/cli-codebuddy.svg'
import codexLogo from '@/assets/icons/vibe-codex-cli.svg'
import copilotLogo from '@/assets/icons/cli-copilot.svg'
import cursorLogo from '@/assets/icons/cli-cursor.svg'
import dshLogo from '@/assets/icons/cli-dsh.svg'
import kimiLogo from '@/assets/icons/cli-kimi.svg'
import opencodeLogo from '@/assets/icons/cli-opencode.svg'
import piLogo from '@/assets/icons/vibe-pi.svg'
// Qoder SVG 超过默认内嵌阈值，显式内嵌以避免 Windows 系统 MIME 映射影响图标加载。
import qoderLogo from '@/assets/icons/cli-qoder.svg?inline'
import qwenLogo from '@/assets/icons/cli-qwen.svg'
// WorkBuddy 用官方应用图标（Windows Assets 下的方形 Logo），非自绘字形。
import workbuddyLogo from '@/assets/icons/cli-workbuddy.png'

const CLI_LOGOS: Record<string, string> = {
  antigravity: antigravityLogo,
  claude: claudeLogo,
  codebuddy: codebuddyLogo,
  codex: codexLogo,
  copilot: copilotLogo,
  cursor: cursorLogo,
  dsh: dshLogo,
  kimi: kimiLogo,
  opencode: opencodeLogo,
  pi: piLogo,
  qoder: qoderLogo,
  'qoder-cn': qoderLogo,
  qwen: qwenLogo,
  workbuddy: workbuddyLogo,
}

export function cliLogo(cliType?: string) {
  const key = (cliType || '').trim().toLowerCase()
  return CLI_LOGOS[key] || cliFallback
}
