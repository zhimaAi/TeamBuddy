package executor

import (
	"context"
	"time"
)

// CLI type constant
const (
	CLITypeCodex       = "codex"
	CLITypeClaude      = "claude"
	CLITypeCodeBuddy   = "codebuddy"
	CLITypeOpenCode    = "opencode"
	CLITypeCursor      = "cursor"
	CLITypeCopilot     = "copilot"
	CLITypeGrok        = "grok"
	CLITypeHermes      = "hermes"
	CLITypeKimi        = "kimi"
	CLITypeQoder       = "qoder"
	CLITypeQoderCN     = "qoder-cn"
	CLITypeQwen        = "qwen"
	CLITypeOpenClaw    = "openclaw"
	CLITypePi          = "pi"
	CLITypeAntigravity = "antigravity"
)

// Event type constant
const (
	EventStart      = "start"
	EventMessage    = "message"
	EventThinking   = "thinking"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventPermission = "permission_request"
	EventComplete   = "complete"
	EventError      = "error"
	EventUsage      = "usage"
	// EventActivity 只广播到执行过程汇总行，不写入 gt_session_events。
	EventActivity = "activity"
	// EventHeartbeat 只刷新看门狗，不进入事件通道。
	EventHeartbeat = "heartbeat"
)

// 活动文案用固定短词，前端按词映射成句子。
const (
	ActivityReconnecting = "reconnecting"
	ActivityRetrying     = "retrying"
	ActivityQuiet        = "quiet"
	ActivityLongTool     = "long_tool"
	ActivityCompacting   = "compacting"
	ActivityResumed      = "resumed"
	ActivitySuspect      = "suspect"
	ActivitySlowLaunch   = "slow_launch"
	// ActivityCallingModel 表示 CLI 已经开始一次模型请求，静默只是还没有 token。
	ActivityCallingModel = "calling_model"
	// ActivityModelLost 表示这次模型请求的外部连接已经不在，且 CLI 没有宣布重试。
	ActivityModelLost = "model_lost"
)

// ActivityInfo 是重连、重试和卡死提示附带的结构化信息。
type ActivityInfo struct {
	Kind      string `json:"kind,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Max       int    `json:"max,omitempty"`
	DelayMs   int64  `json:"delay_ms,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Escalated bool   `json:"escalated,omitempty"`
}

// PermissionRequest CLI rejects and returns a tool permission request in non-interactive mode.
type PermissionRequest struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	ToolInput string `json:"tool_input,omitempty"`
}

// ExecutorEvent CLI executor generated events
type ExecutorEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	SessionID string `json:"session_id,omitempty"` // External session ID (thread_id / session_id)
	// ToolUseID 是协议侧的工具调用 ID，用于把 tool_result 精确归属到对应的 tool_call。
	// 并行工具调用的结果并不按调用顺序返回（实测 pi 会乱序回传），仅靠事件顺序无法配对。
	// 协议未提供该字段时为空，此时不得按顺序猜测归属。
	ToolUseID string `json:"tool_use_id,omitempty"`
	// ToolResultPartial 表示增量输出；只有最终 tool_result 才结束工具执行计时。
	ToolResultPartial bool `json:"tool_result_partial,omitempty"`
	// ToolKind 是协议侧的工具种类。shell 表示这条调用会拉起子进程。空字符串表示未知。
	ToolKind string `json:"tool_kind,omitempty"`
	// ToolResultInterrupted 表示进程中断时这条工具还没有终态结果，由编排器补发。
	ToolResultInterrupted bool `json:"tool_result_interrupted,omitempty"`
	// InterruptedAttempt 是被中断的那次执行序号，从 1 开始。
	InterruptedAttempt int `json:"interrupted_attempt,omitempty"`
	// Activity 附在 type=activity 的事件上。
	Activity *ActivityInfo `json:"activity,omitempty"`
	// ExitClass 附在终态事件上，编排器据此决定续跑。
	ExitClass          string              `json:"exit_class,omitempty"`
	InputTokens        int                 `json:"input_tokens,omitempty"`
	OutputTokens       int                 `json:"output_tokens,omitempty"`
	Error              string              `json:"error,omitempty"`
	PermissionRequests []PermissionRequest `json:"permission_requests,omitempty"`
	Timestamp          int64               `json:"timestamp"`
}

// RunOptions New dialog execution options
type RunOptions struct {
	ExecPath      string            // CLI executable file path
	Prompt        string            // prompt word
	WorkDir       string            // working directory
	ModelProfile  string            //Model name
	ThinkingLevel string            // 模型目录预检后的原生档位，空值不覆盖
	ExtraArgs     []string          // Additional command line parameters
	EnvVars       map[string]string // environment variables
	// PresetSessionID 是首轮由调用方指定的会话 ID。续跑仍走 ResumeOptions.ExternalSessionID。
	PresetSessionID string
	// Watchdog 为空时使用 DefaultWatchdogConfig。
	Watchdog WatchdogConfig
}

// ResumeOptions Continue dialog execution options
type ResumeOptions struct {
	RunOptions
	ExternalSessionID string // External session ID (codex thread_id / claude/codebuddy session_id)
}

// Adapter CLI adapter interface.
// The concrete implementation lives in the internal/executor/<cli> sub-packages.
// The construction entry is NewAdapter() of each sub-package, registered into the
// workflow adapter registry at bootstrap and looked up by CLI type at run time;
// most adapters are assembled from executor.NewStreamAdapter with an AdapterSpec.
// CLI version and model detection are unified through DiscoverCLIs, and are not implemented repeatedly on the adapter.
type Adapter interface {
	// NewConversation new conversation
	NewConversation(ctx context.Context, opts RunOptions) (<-chan ExecutorEvent, error)
	// ResumeConversation continues the conversation
	ResumeConversation(ctx context.Context, opts ResumeOptions) (<-chan ExecutorEvent, error)
	// Stop stops execution
	Stop() error
}

// NewEvent creates event
func NewEvent(eventType string) ExecutorEvent {
	return ExecutorEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
	}
}

// NewCompleteEvent creates a completion event
func NewCompleteEvent(sessionID string, inputTokens, outputTokens int) ExecutorEvent {
	return ExecutorEvent{
		Type:         EventComplete,
		SessionID:    sessionID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Timestamp:    time.Now().UnixMilli(),
	}
}
