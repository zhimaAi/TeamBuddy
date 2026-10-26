// 保留旧入口供已有调用方迁移；通用 Vibe Coding 逻辑统一维护在 useTaskVibeTool。
export {
  assignTaskToCodex,
  copyTaskVibeToolPrompt as copyTaskCodexPrompt,
  injectVibeToolPrompt,
  loadTaskVibeToolContext as loadTaskCodexContext,
  loadVibeToolsCapability as loadCodexCapability,
  openTaskVibeTool,
  openTaskVibeTool as openTaskInCodex,
} from './useTaskVibeTool'
export type {
  VibeToolCapability,
  TaskVibeToolContext as TaskCodexContext,
  VibeToolInjectResult,
  VibeToolsCapability as CodexCapability,
  VibeToolOpenResult as CodexOpenResult,
} from './useTaskVibeTool'
