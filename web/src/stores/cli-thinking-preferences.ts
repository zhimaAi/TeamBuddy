import { defineStore } from 'pinia'
import { useAuthStore } from '@/stores/auth'

const storageKey = 'goteams.cli-thinking.preferences.v1'
const pairKey = (cliType: string, model: string) => JSON.stringify([cliType, model])

// 用户偏好不是能力证明；恢复失效档位后仍由灯泡提示、后端校验。
export const useCLIThinkingPreferencesStore = defineStore('cli-thinking-preferences', () => {
  const auth = useAuthStore()
  const scope = () => auth.cloudUser?.accountScope || 'local'
  const accounts = new Map<string, Record<string, string>>()

  function preferences() {
    const account = scope()
    let values = accounts.get(account)
    if (!values) {
      values = {}
      try {
        const raw: unknown = JSON.parse(localStorage.getItem(`${storageKey}:${encodeURIComponent(account)}`) || '{}')
        if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
          for (const [key, value] of Object.entries(raw)) {
            const pair: unknown = JSON.parse(key)
            if (Array.isArray(pair) && pair.length === 2 && pair.every(part => typeof part === 'string' && part.length > 0 && part.length <= 4096)
              && typeof value === 'string' && value.length <= 64) values[key] = value
          }
        }
      } catch { /* 存储不可用或旧数据损坏时，本次选择仍可使用。 */ }
      accounts.set(account, values)
    }
    return values
  }

  function get(cliType: string, model: string): string | undefined {
    return cliType && model ? preferences()[pairKey(cliType, model)] : undefined
  }

  function remember(cliType: string, model: string, level: string) {
    if (!cliType || !model) return
    const values = preferences()
    values[pairKey(cliType, model)] = level
    try {
      localStorage.setItem(`${storageKey}:${encodeURIComponent(scope())}`, JSON.stringify(values))
    } catch { /* 写入失败不影响本次选择和当前进程内记忆。 */ }
  }

  return { scope, get, remember }
})
