import codeReviewIcon from '@/assets/icons/skill-code-review.svg'
import unitTestIcon from '@/assets/icons/skill-unit-test-generator.svg'
import apiDocIcon from '@/assets/icons/skill-api-doc-generator.svg'
import sqlOptimizerIcon from '@/assets/icons/skill-sql-optimizer.svg'
import dashboardIcon from '@/assets/icons/skill-dashboard-generator.svg'
import resumeIcon from '@/assets/icons/skill-resume-screening.svg'
import requirementIcon from '@/assets/icons/skill-requirement-breakdown.svg'

export const AUTO_SKILL_ICON = 'teambuddy:skill:auto'
export const SKILL_ICON_PRESETS = [
  { value: 'teambuddy:skill:code-review', image: codeReviewIcon },
  { value: 'teambuddy:skill:unit-test-generator', image: unitTestIcon },
  { value: 'teambuddy:skill:api-doc-generator', image: apiDocIcon },
  { value: 'teambuddy:skill:sql-optimizer', image: sqlOptimizerIcon },
  { value: 'teambuddy:skill:dashboard-generator', image: dashboardIcon },
  { value: 'teambuddy:skill:resume-screening', image: resumeIcon },
  { value: 'teambuddy:skill:requirement-breakdown', image: requirementIcon },
] as const

export function safeSkillURL(value: string): string | undefined {
  try {
    const url = new URL(value)
    if ((url.protocol === 'https:' || url.protocol === 'http:') && url.hostname && !url.username && !url.password) return url.href
  } catch { /* 非 HTTP(S) 来源和图标不能作为可点击地址。 */ }
  return undefined
}
