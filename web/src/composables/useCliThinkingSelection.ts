import { watch, type Ref } from 'vue'
import { useCLIThinkingPreferencesStore } from '@/stores/cli-thinking-preferences'

// 表单草稿只在本入口保留，只有灯泡中的用户选择写入跨入口偏好。
export function useCliThinkingSelection(cliType: () => string, model: () => string, level: Ref<string>) {
  const preferences = useCLIThinkingPreferencesStore()
  const drafts = new Map<string, string>()
  const key = (cli: string, name: string) => JSON.stringify([cli, name])

  watch([cliType, model], ([cli, name], [oldCLI, oldModel]) => {
    if (oldCLI && oldModel) drafts.set(key(oldCLI, oldModel), level.value)
    level.value = cli && name ? drafts.get(key(cli, name)) ?? preferences.get(cli, name) ?? '' : ''
  }, { flush: 'sync' })
  watch(preferences.scope, () => {
    drafts.clear()
    level.value = ''
  }, { flush: 'sync' })

  // 初始化/取消回填后重置草稿；显式业务配置（包括空值）优先，不写用户偏好。
  return { resetThinkingDrafts: () => drafts.clear() }
}
