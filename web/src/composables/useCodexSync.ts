import { h } from 'vue'
import { Modal } from 'ant-design-vue'
import CodexSyncGuidance from '@/components/CodexSyncGuidance.vue'
import { t } from '@/i18n'
import router from '@/router'
import { configureCodexReporting } from './useDesktop'

export const syncAction = (state: CodexSyncState) => state === 'hooks_untrusted' ? t('settings.codexReporting.trust')
  : state === 'hooks_disabled' ? t('settings.codexReporting.review') : t('settings.codexReporting.enable')

export const syncDescription = (state: CodexSyncState) => state === 'ready'
  ? t('settings.codexReporting.ready') : state === 'hooks_untrusted' ? t('settings.codexReporting.awaitTrust') : t(`workflows.task.detail.codexSetup.${state}`)

const syncContent = (state: CodexSyncState) => h(CodexSyncGuidance, { state, description: syncDescription(state) })

export const syncError = (error: unknown) => error instanceof Error
  ? error.message.replace(/^Error invoking remote method '[^']+': (?:Error: )?/, '') : t('settings.readDesktopFailed')

let active = false

// 一次授权只恢复发起它的操作；另一个任务不能复用等待中的操作。
export async function ensureCodexSync(initial?: CodexSyncState, confirmed = false): Promise<boolean> {
  if (active) return false
  active = true
  const route = router.currentRoute.value.fullPath
  try {
    let state = initial ?? await configureCodexReporting('status')
    if (router.currentRoute.value.fullPath !== route) return false
    if (state === 'ready') return true
    return await new Promise<boolean>(resolve => {
      let closed = false, busy = false, awaitingReturn = false, focusPending = false
      const dialog = Modal.confirm({
        title: t('settings.codexReporting.title'), content: syncContent(state),
        okText: syncAction(state), cancelText: t('workflows.task.detail.qoderIDELater'),
        maskClosable: false,
        onOk(_close: () => void) { void run(false) },
        onCancel() { finish(false) },
        afterClose() { finish(false) },
      })
      const removeRoute = router.afterEach(() => { if (router.currentRoute.value.fullPath !== route) finish(false) })
      function finish(ok: boolean) {
        if (closed) return
        closed = true
        window.removeEventListener('focus', onFocus)
        window.removeEventListener('pagehide', onExit)
        removeRoute(); dialog.destroy(); resolve(ok)
      }
      function onExit() { finish(false) }
      function onFocus() {
        if (!awaitingReturn || closed) return
        if (busy) focusPending = true
        else void run(true)
      }
      async function run(checkOnly: boolean) {
        if (busy || closed) return
        busy = true
        dialog.update({ okButtonProps: { loading: true } })
        if (!checkOnly) awaitingReturn = true
        try {
          state = await configureCodexReporting(checkOnly ? 'status'
            : state === 'hooks_untrusted' || state === 'hooks_disabled' ? 'review' : 'enable')
          if (closed) return
          if (state === 'ready') { finish(true); return }
          dialog.update({ content: syncContent(state), okText: syncAction(state) })
        } catch (e) {
          if (!closed) dialog.update({ content: syncError(e) })
        } finally {
          busy = false
          if (!closed) {
            dialog.update({ okButtonProps: { loading: false } })
            if (focusPending) { focusPending = false; void run(true) }
          }
        }
      }
      window.addEventListener('pagehide', onExit)
      window.addEventListener('focus', onFocus)
      if (confirmed) void run(false)
    })
  } finally { active = false }
}
