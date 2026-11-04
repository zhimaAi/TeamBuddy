// Package pi 提供 Pi CLI 的执行适配。
package pi

import (
	"encoding/json"
	"strings"

	"goteams-client/internal/executor"
)

const displayName = "Pi Agent"

// NewAdapter 创建 Pi CLI 适配器。
//
// Pi CLI 的 `--mode json` 输出 type-based JSONL 事件：
// session / agent_start / agent_end / agent_settled / turn_start / turn_end /
// message_start / message_update / message_end /
// tool_execution_start / tool_execution_update / tool_execution_end。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		SupportsThinking: true,
		DisplayName:      displayName,
		BuildInvocation:  buildInvocation,
		NewDecoder: func(name string) executor.Decoder {
			return NewDecoder(name)
		},
	})
}

// buildInvocation 构造 pi 命令行。Prompt 走 stdin，避免 Windows 下 cmd.exe 重新解释多行内容。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := make([]string, 0, 8)
	if resumeSession != "" {
		args = append(args, executor.SessionArg(false, "--session", resumeSession)...)
	} else if opts.PresetSessionID != "" {
		args = append(args, executor.SessionArg(false, "--session-id", opts.PresetSessionID)...)
	}
	args = append(args, "--mode", "json")
	args = append(args, modelArgs(opts.ModelProfile)...)
	if opts.ThinkingLevel != "" {
		args = append(args, "--thinking", opts.ThinkingLevel)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}

// modelArgs 把 "provider/model" 或裸模型名转换为 --provider / --model 参数。
// "auto" 是 pi 的默认值，无需显式传递。
func modelArgs(modelProfile string) []string {
	if modelProfile == "" || modelProfile == "auto" {
		return nil
	}
	if strings.Contains(modelProfile, "/") {
		parts := strings.SplitN(modelProfile, "/", 2)
		return []string{"--provider", parts[0], "--model", parts[1]}
	}
	return []string{"--model", modelProfile}
}

// piEvent 是 Pi CLI --mode json 的宽容联合事件结构。
// 可能承载数组或对象的字段使用 json.RawMessage，保证单个结构体能解码全部事件类型。
type piEvent struct {
	Type           string            `json:"type"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ToolName       string            `json:"toolName"`
	ToolCallID     string            `json:"toolCallId"`
	Args           json.RawMessage   `json:"args"`
	Content        string            `json:"content"`
	Result         json.RawMessage   `json:"result"`
	Error          string            `json:"error"`
	IsError        bool              `json:"isError"`
	Message        *piMessage        `json:"message"`
	AssistantEvent *piAssistantEvent `json:"assistantMessageEvent"`
	Usage          piUsage           `json:"usage"`
	Success        *bool             `json:"success"`
	Attempt        int               `json:"attempt"`
	MaxAttempts    int               `json:"maxAttempts"`
	DelayMs        int64             `json:"delayMs"`
	FinalError     string            `json:"finalError"`
	ErrorMessage   string            `json:"errorMessage"`
	WillRetry      bool              `json:"willRetry"`
}

type piMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   piUsage         `json:"usage"`
}

type piAssistantEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
	ID           string `json:"id"`
	ToolName     string `json:"toolName"`
}

// piUsage 是 pi 上报的 token 用量。
// 注意字段名是 input/output/totalTokens，而不是 input_tokens/output_tokens。
type piUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Total  int `json:"totalTokens"`
}

// piContentBlock 是 pi 消息的内容块。
type piContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// Decoder 解码 Pi CLI 的 JSONL 输出。
type Decoder struct {
	executor.BaseDecoder

	// message_delta 是逐片输出的流式文本，缓冲到 message_end 后整体输出，
	// 避免前端逐字渲染。
	messageBuffer  string
	thinkingBuffer string

	// token 用量。pi 在 message_update 里上报的是「当前消息」的累计值，
	// 所以消息内取最大值、message_end 时并入总量，避免把增量叠加成多倍。
	usageInput   int
	usageOutput  int
	messageUsage piUsage
}

// NewDecoder 创建 Pi 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{BaseDecoder: executor.NewBaseDecoder(name)}
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event piEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return
	}

	switch event.Type {
	case "session":
		d.SetSession(event.ID)

	case "agent_start", "turn_start", "turn_end", "queue_update":
		d.Heartbeat(emit)

	case "agent_settled":
		d.MarkTerminal()

	case "compaction_start":
		emit(executor.ExecutorEvent{Type: executor.EventActivity, Content: executor.ActivityCompacting, Activity: &executor.ActivityInfo{Kind: executor.ActivityCompacting}})

	case "compaction_end":
		emit(executor.ExecutorEvent{Type: executor.EventActivity, Content: executor.ActivityResumed, Activity: &executor.ActivityInfo{Kind: executor.ActivityResumed}})

	case "auto_retry_start":
		emit(executor.ExecutorEvent{
			Type:    executor.EventActivity,
			Content: executor.ActivityRetrying,
			Activity: &executor.ActivityInfo{
				Kind:    executor.ActivityRetrying,
				Attempt: event.Attempt,
				Max:     event.MaxAttempts,
				DelayMs: event.DelayMs,
				Detail:  event.ErrorMessage,
			},
		})

	case "auto_retry_end":
		if event.Success != nil && *event.Success {
			emit(executor.ExecutorEvent{Type: executor.EventActivity, Content: executor.ActivityResumed, Activity: &executor.ActivityInfo{Kind: executor.ActivityResumed}})
			break
		}
		if event.Success != nil && !*event.Success {
			reason := strings.TrimSpace(event.FinalError)
			if reason == "" {
				reason = "Pi 自动重试失败"
			}
			d.MarkTerminal()
			d.Fail(reason)
			d.SetExitHint(executor.ExitFatalUpstream)
		}

	case "agent_end":
		// willRetry 为真时这一轮还没结束，不能当成终态。
		if event.WillRetry {
			d.Heartbeat(emit)
			break
		}
		d.MarkTerminal()

	case "message_start":
		// 新一轮消息开始，清空上一轮的流式缓冲与本条消息的用量累计。
		// 这一刻模型请求已经发出。后续没有 token 不代表连接断开。
		d.messageBuffer = ""
		d.thinkingBuffer = ""
		d.messageUsage = piUsage{}
		emit(executor.ExecutorEvent{
			Type:     executor.EventActivity,
			Content:  executor.ActivityCallingModel,
			Activity: &executor.ActivityInfo{Kind: executor.ActivityCallingModel},
		})

	case "message_update":
		d.trackUsage(event.Usage)
		d.decodeAssistantDelta(event)

	case "message_end":
		// 本条消息的用量并入整次执行
		d.usageInput += d.messageUsage.Input
		d.usageOutput += d.messageUsage.Output
		d.messageUsage = piUsage{}
		d.decodeMessageEnd(event, emit)

	case "tool_execution_start":
		kind := ""
		switch strings.ToLower(strings.TrimSpace(event.ToolName)) {
		case "bash", "shell":
			kind = executor.ToolKindShell
		}
		d.ToolCallKind(event.ToolCallID, event.ToolName, executor.FormatToolArgs(event.Args), kind, emit)

	case "tool_execution_update":
		d.ToolProgress(event.ToolCallID, event.Content, emit)

	case "tool_execution_end":
		// result 既可能是字符串，也可能是 {content:[{type,text}]} 形态的对象
		d.ToolResult(event.ToolCallID, executor.RawText(event.Result), emit)

	case "bash_execution_update":
		d.ToolProgress(event.ToolCallID, event.Content, emit)

	case "error":
		reason := strings.TrimSpace(event.Error)
		if reason == "" {
			reason = strings.TrimSpace(event.Content)
		}
		if reason == "" {
			reason = d.DisplayName + " 执行失败"
		}
		d.Fail(reason)
	}
}

// decodeAssistantDelta 处理流式增量。
// toolcall_start 不在此处产出事件：工具调用由 tool_execution_start 统一承载（含参数），
// 两处都产出会让同一次调用在界面上重复出现。
func (d *Decoder) decodeAssistantDelta(event piEvent) {
	if event.AssistantEvent == nil {
		return
	}
	switch event.AssistantEvent.Type {
	case "text_delta":
		d.messageBuffer += event.AssistantEvent.Delta
	case "thinking_delta":
		d.thinkingBuffer += event.AssistantEvent.Delta
	}
}

// decodeMessageEnd 在消息结束时输出思考与文本。
// 只处理 assistant 角色：user 与 toolResult 消息属于回显，把它们当作输出会污染动态区。
func (d *Decoder) decodeMessageEnd(event piEvent, emit executor.EmitFunc) {
	if event.Message == nil || !strings.EqualFold(event.Message.Role, "assistant") {
		return
	}

	thinking := extractBlocks(event.Message, "thinking", "thinking")
	if thinking == "" {
		thinking = strings.TrimSpace(d.thinkingBuffer)
	}
	d.thinkingBuffer = ""

	content := extractBlocks(event.Message, "text", "text")
	if content == "" {
		content = strings.TrimSpace(d.messageBuffer)
	}
	d.messageBuffer = ""

	// 思考先于文本输出，与 CLI 的实际推理顺序一致
	d.Thinking(thinking, emit)
	d.Message(content, emit)
}

// Done 在输出结束后校验终态事件。
// trackUsage 记录 pi 在本条消息上累计的用量。
// message_update 会反复上报同一消息的累计值，因此取最大值而非相加。
func (d *Decoder) trackUsage(usage piUsage) {
	if usage.Input > d.messageUsage.Input {
		d.messageUsage.Input = usage.Input
	}
	if usage.Output > d.messageUsage.Output {
		d.messageUsage.Output = usage.Output
	}
}

func (d *Decoder) Done(emit executor.EmitFunc) {
	// 收尾残留的流式缓冲（异常中断时未收到 message_end）
	d.Thinking(d.thinkingBuffer, emit)
	d.Message(d.messageBuffer, emit)
	d.thinkingBuffer = ""
	d.messageBuffer = ""
	// 异常中断时可能收不到 message_end，把尚未并入的当前消息用量补上；
	// 正常路径下 message_end 已清零，这里加的是 0，不会重复计量
	d.usageInput += d.messageUsage.Input
	d.usageOutput += d.messageUsage.Output
	d.messageUsage = piUsage{}
	d.SetUsage(d.usageInput, d.usageOutput)
	d.FailMissingTerminal("agent_end")
}

// extractBlocks 按块类型拼接内容块文本。
func extractBlocks(message *piMessage, blockType, field string) string {
	if message == nil || len(message.Content) == 0 {
		return ""
	}
	var blocks []piContentBlock
	if json.Unmarshal(message.Content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if !strings.EqualFold(block.Type, blockType) {
			continue
		}
		value := block.Text
		if field == "thinking" {
			value = block.Thinking
		}
		if text := strings.TrimSpace(value); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
