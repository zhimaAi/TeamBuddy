import { message, Modal } from 'ant-design-vue'
import { t } from '@/i18n'
import type { VibeToolOpenResult } from '@/composables/useTaskVibeTool'

// 各创建、指派和详情入口共享反馈顺序，避免工具分支在页面中扩散。
export function showVibeToolOpenResult(result: VibeToolOpenResult, toolName: string): void {
  if (result.manual) {
    message.info(t('workflows.task.detail.manualPackHint', { tool: toolName }))
    return
  }
  if (result.requiresIDEExit && result.retry) {
    const retry = result.retry
    let retrying = false
    let closed = false
    const dialog = Modal.confirm({
      title: t('workflows.task.detail.qoderIDESetupTitle'),
      content: t('workflows.task.detail.qoderIDESetupDescription'),
      okText: t('workflows.task.detail.qoderIDEContinue'),
      cancelText: t('workflows.task.detail.qoderIDELater'),
      keyboard: false,
      maskClosable: false,
      onCancel() { closed = true },
      onOk(close: () => void) {
        if (retrying) return
        retrying = true
        dialog.update({ okButtonProps: { loading: true }, cancelButtonProps: { disabled: true } })
        // 使用显式关闭回调，运行中保留弹窗，不以拒绝 Promise 制造未处理异常。
        void retry().then(next => {
          if (next.requiresIDEExit) {
            message.warning(t('workflows.task.detail.qoderIDEStillRunning'))
            return
          }
          closed = true
          close()
          showVibeToolOpenResult(next, toolName)
        }).catch(error => {
          message.error(error instanceof Error ? error.message : t('workflows.task.detail.toolOpenFailed', { tool: toolName }))
        }).finally(() => {
          retrying = false
          if (!closed) dialog.update({ okButtonProps: { loading: false }, cancelButtonProps: { disabled: false } })
        })
      },
    })
    return
  }
  if (result.guidanceKey === 'workflows.task.detail.genericPromptCopied') {
    Modal.info({
      title: t('workflows.task.detail.genericPromptCopiedTitle'),
      content: t(result.guidanceKey),
      okText: t('common.actions.confirm'),
      width: 480,
      keyboard: false,
      maskClosable: false,
    })
  } else if (result.guidanceKey) message.info(t(result.guidanceKey))
  else if (result.error && result.copied) {
    message.warning(result.error.message + ' ' + t('workflows.task.detail.qoderIDEPromptCopied'))
  } else if (result.opened) message.success(t('workflows.task.detail.toolOpened', { tool: toolName }))
  else if (result.copied) message.warning(t('workflows.task.detail.toolCopiedFallback', { tool: toolName }))
  else if (result.error) message.error(result.error.message)
  else message.warning(t('workflows.task.detail.toolUnavailable', { tool: toolName }))
}
