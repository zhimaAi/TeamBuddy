export {}

declare global {
  interface DesktopUpdateState {
    currentVersion: string
    status: 'idle' | 'checking' | 'current' | 'available' | 'downloading' | 'downloaded' | 'installing' | 'error'
    release: null | {
      version: string
      publishedAt: string
      notes: string
      asset: null | { name: string; size: number }
    }
    progress: null | { received: number; total: number; bytesPerSecond: number }
    error: string
    installSupported: boolean
    platform: string
  }

  interface DesktopTaskNotificationPayload {
    title: string
    body: string
    taskUuid: string
    stepUuid?: string
  }

  interface DesktopBrowserLoginStartResult {
    state: string
    browserUrl: string
    expiresIn: number
  }

  interface DesktopBrowserLoginResult {
    status: 'ok' | 'failed'
    user?: { id: string; username: string; display_name: string; avatar: string; role: string } | null
    adminId?: string
    error?: string
  }

  interface Window {
    goteamsDesktop?: {
      readonly platform: string
      selectDirectories(options?: { defaultPath?: string; multiple?: boolean }): Promise<string[]>
      openDirectory(directoryPath: string): Promise<{ opened: true }>
      revealTaskFile?(payload: {
        taskUuid: string
        fileRef: string
        sessionUuid?: string
        locale?: 'zh-CN' | 'en-US'
      }): Promise<{ revealed: true }>
      openTerminal(directoryPath: string): Promise<{ opened: true }>
      openVibeCli(payload: {
        tool: string
        execPath: string
        directoryPath: string
        prompt: string
        threadId?: string
        modelName?: string
      }): Promise<{ opened: true }>
/** 手动模式：可复制的 MCP 配置文本，仅用于展示，不写入用户配置 */
      getVibeMcpConfig?(tool?: string): Promise<{
        available: boolean
        config: string
        configPath: string
        command: string
      }>
      openQoderIDE?(payload: {
        directoryPath: string
        prompt: string
        threadId?: string
      }): Promise<{
        opened: boolean
        sessionLocated: false
        promptDelivery: 'clipboard' | 'deeplink' | 'none'
        promptCopied: boolean
        requiresManualStart: boolean
        requiresManualSessionSelection: boolean
        hooksUpdated: boolean
        requiresRestart?: boolean
      }>
      openCodexThread(payload: {
        directoryPath: string
        prompt: string
        threadId?: string
      }): Promise<{ opened: true }>
      /** 本次启动的本地 API 鉴权 token（由 Electron 主进程生成） */
      getApiToken(): Promise<string>
      getCloseBehavior(): Promise<{ closeBehavior: 'hide' | 'quit' }>
      setCloseBehavior(closeBehavior: 'hide' | 'quit'): Promise<{ closeBehavior: 'hide' | 'quit' }>
      getUpdateState(): Promise<DesktopUpdateState>
      checkForUpdates(manual?: boolean): Promise<DesktopUpdateState & { showPrompt: boolean }>
      deferUpdate(): Promise<void>
      downloadUpdate(): Promise<DesktopUpdateState>
      cancelUpdateDownload(): Promise<void>
      installUpdate(): Promise<{ started: boolean; manual: boolean }>
      onUpdateState(listener: (state: DesktopUpdateState) => void): () => void
      /** 把当前应用语言同步给主进程，用于托盘与原生对话框 */
      setLocale(locale: string): Promise<{ locale: string }>
      setTrayStatus(status?: { runningTaskCount?: number }): void
      showTaskNotification(
        payload: DesktopTaskNotificationPayload,
      ): Promise<{ shown: boolean; reason?: string; message?: string }>
      onOpenTaskConversation(
        listener: (payload: { taskUuid: string; stepUuid?: string }) => void,
      ): () => void
      /** 浏览器登录：由主进程申请登录会话并打开系统浏览器 */
      startBrowserLogin(options?: {
        serverType?: 'official' | 'custom'
        serverUrl?: string
      }): Promise<DesktopBrowserLoginStartResult>
      /** 订阅深链接回注结果，返回取消订阅函数 */
      onBrowserLoginResult(listener: (payload: DesktopBrowserLoginResult) => void): () => void
      /** 重新拉起桌面进程，使已写好的工作空间指针在新进程里生效 */
      relaunchApp(): Promise<{ restarting: true }>
    }
  }
}
