import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

type SendShortcut = 'enter' | 'ctrl+enter'

const STORAGE_KEY = 'goteams.composer.send-shortcut'

function readSendShortcut(): SendShortcut {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'ctrl+enter' ? 'ctrl+enter' : 'enter'
  } catch {
    return 'enter'
  }
}

export const useComposerPreferencesStore = defineStore('composer-preferences', () => {
  // 本机交互偏好长期保留，直到用户重新选择；缺失或旧版非法值回退 Enter，无需迁移。
  const shortcut = ref<SendShortcut>(readSendShortcut())
  const sendShortcut = computed(() => shortcut.value)

  function setSendShortcut(value: string | number) {
    if (value !== 'enter' && value !== 'ctrl+enter') return
    shortcut.value = value
    try {
      localStorage.setItem(STORAGE_KEY, value)
    } catch {
      // 存储不可用时仍在当前应用会话内共享选择。
    }
  }

  function shouldSend(event: KeyboardEvent): boolean {
    if (event.key !== 'Enter' || event.isComposing || event.keyCode === 229 || event.repeat) return false
    if (event.shiftKey || event.altKey || event.metaKey) return false
    return event.ctrlKey === (shortcut.value === 'ctrl+enter')
  }

  return { sendShortcut, setSendShortcut, shouldSend }
})
