import type { SkillSource } from '@/api/skills'

export function detectSkillSource(value: string): SkillSource {
  try {
    const url = new URL(value.trim())
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return 'github'
    const host = url.hostname.toLowerCase().replace(/\.$/, '')
    switch (host) {
      case 'skillhub.cn':
      case 'www.skillhub.cn':
      case 'skillhub.tencent.com':
        return 'skillhub'
      case 'clawhub.ai':
      case 'www.clawhub.ai':
        return 'clawhub'
    }
  } catch { /* 地址合法性由表单校验，来源展示默认使用 GitHub。 */ }
  return 'github'
}

export function skillSourceLabel(source: SkillSource) {
  return { github: 'GitHub', skillhub: 'SkillHub', clawhub: 'ClawHub' }[source]
}

export function skillSourceColor(source: SkillSource) {
  return { github: 'default', skillhub: 'blue', clawhub: 'purple' }[source]
}
