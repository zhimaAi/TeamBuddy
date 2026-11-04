import fallbackLogo from '@/assets/icons/pipeline-agent-model.svg'
import claudeLogo from '@/assets/icons/vibe-claude.svg'
import kimiLogo from '@/assets/icons/cli-kimi.svg'
import qwenLogo from '@/assets/icons/cli-qwen.svg'
import openaiLogo from '@/assets/icons/model-openai.svg'
import deepseekLogo from '@/assets/icons/model-deepseek.svg'
import geminiLogo from '@/assets/icons/model-gemini.svg'
import minimaxLogo from '@/assets/icons/model-minimax.svg'
import zaiLogo from '@/assets/icons/model-zai.svg'
import grokLogo from '@/assets/icons/model-grok.svg'
import moonshotLogo from '@/assets/icons/model-moonshot.svg'

const MODEL_LOGOS: ReadonlyArray<readonly [RegExp, string]> = [
  [/^(?:gpt-|chatgpt-|o[1-9](?:-|$))/, openaiLogo],
  [/^claude(?:-|$)/, claudeLogo],
  [/^deepseek(?:-|$)/, deepseekLogo],
  [/^gemini(?:-|$)/, geminiLogo],
  [/^qwen(?:\d|-|$)/, qwenLogo],
  [/^kimi(?:-|$)/, kimiLogo],
  [/^moonshot(?:-|$)/, moonshotLogo],
  [/^glm(?:\d|-|$)/, zaiLogo],
  [/^minimax(?:-|$)/, minimaxLogo],
  [/^grok(?:-|$)/, grokLogo],
]

export function modelLogo(model: string, cliType?: string) {
  // 仅标识已知家族，不据此推断实际提供方或思考能力；自定义名始终有默认图标。
  const name = model.trim().toLowerCase().split('/').at(-1) || ''
  if (cliType === 'claude' && /^(?:opus|sonnet|haiku)(?:-|\[|$)/.test(name)) return claudeLogo
  return MODEL_LOGOS.find(([pattern]) => pattern.test(name))?.[1] || fallbackLogo
}
