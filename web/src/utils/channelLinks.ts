/**
 * 远程通道（企微 / 飞书 / 钉钉）的开放平台与文档链接。
 *
 * - platformUrl 用于跳转平台官方入口；
 * - docsUrl 用于跳转 goteams 配置指南（当前暂用 chatclaw 文档占位，后续会替换为 goteams.cn 下路径）。
 *
 * 桌面端 Electron 通过 `setWindowOpenHandler` 自动拦截 `window.open` 并改走 `shell.openExternal`，
 * 浏览器端则由浏览器自行处理新标签页。两种环境下都不需要在此区分。
 */

type ChannelPlatform = 'wecom' | 'feishu' | 'dingtalk'

const PLATFORM_URLS: Record<ChannelPlatform, string> = {
  wecom: 'https://work.weixin.qq.com/',
  feishu: 'https://open.feishu.cn/',
  dingtalk: 'https://open.dingtalk.com/',
}

// 暂沿用 chatclaw 的文档路径占位，后续替换为 goteams.cn 下的指南链接。
const DOCS_URLS: Record<ChannelPlatform, string> = {
  wecom: 'https://docs.ichatclaw.com/docs/chatClaw-access-to-work-weixin-robot',
  feishu: 'https://docs.ichatclaw.com/docs/chatClaw-access-to-feishu',
  dingtalk: 'https://docs.ichatclaw.com/docs/chatClaw-access-to-dingtalk-robot',
}

export function getChannelPlatformUrl(platform: ChannelPlatform): string {
  return PLATFORM_URLS[platform]
}

export function getChannelDocsUrl(platform: ChannelPlatform): string {
  return DOCS_URLS[platform]
}

/**
 * 在浏览器新标签页打开外部链接；桌面端由 Electron 自动转交给系统默认浏览器。
 *
 * 仅接受 http/https 协议，避免被 `javascript:` 等危险 scheme 注入。
 */
export function openExternalLink(url: string): void {
  const safe = /^(https?:)\/\//i.test(url) ? url : ''
  if (!safe || typeof window === 'undefined') return
  window.open(safe, '_blank', 'noopener,noreferrer')
}