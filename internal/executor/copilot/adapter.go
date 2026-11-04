// Package copilot provides GitHub Copilot CLI execution adaptation.
package copilot

import (
	"encoding/json"
	"strings"

	"goteams-client/internal/executor"
)

const defaultDisplayName = "GitHub Copilot CLI"

// NewAdapter creates the Copilot adapter.
//
// The Copilot CLI is run headless with --output-format json (JSONL), every tool
// is auto-approved (--allow-all, the non-interactive requirement), autonomous
// mode disables ask_user (--no-ask-user) and the prompt is streamed over stdin
// so multi-line prompts survive the Windows npm wrapper. Text output arrives as
// "assistant.message" events, tool activity as "tool.execution_start" /
// "tool.execution_complete", errors as "session.error"/"model.call_failure" and
// the run ends with a terminal "result" or "session.shutdown" event. The
// session id from the final "result" event is used to resume later turns.
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:      defaultDisplayName,
		SupportsThinking: true,
		BuildInvocation:  buildInvocation,
		NewDecoder: func(name string) executor.Decoder {
			return NewDecoder(name)
		},
	})
}

// buildInvocation 构造 copilot 命令行，prompt 走 stdin。
//
//	copilot --output-format json --stream off --allow-all --no-ask-user
//	        --no-auto-update --no-color [--resume <session_id>]
//	        [--model <model>] [extraArgs]   (prompt on stdin)
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{
		"--output-format", "json",
		"--stream", "off",
		"--allow-all",
		"--no-ask-user",
		"--no-auto-update",
		"--no-color",
	}
	if resumeSession != "" {
		args = append(args, executor.SessionArg(true, "--resume", resumeSession)...)
	} else if opts.PresetSessionID != "" {
		args = append(args, executor.SessionArg(true, "--session-id", opts.PresetSessionID)...)
	}
	if opts.ModelProfile != "" && opts.ModelProfile != "auto" {
		args = append(args, "--model", opts.ModelProfile)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--reasoning-effort", opts.ThinkingLevel)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}

// Decoder 解码 copilot --output-format json 的 JSONL 输出。
type Decoder struct {
	executor.BaseDecoder

	inputTokens     int
	outputTokens    int
	reasoningBuffer string
	// messageBuffers 按 messageId 累积 assistant.message_delta 的增量文本，
	// 等完整消息事件到达时整条发出，避免把一句话拆成多个消息事件。
	messageBuffers map[string]string
	unparsed       executor.DiagnosticTail
}

// NewDecoder 创建 Copilot 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = defaultDisplayName
	}
	return &Decoder{
		BaseDecoder:    executor.NewBaseDecoder(name),
		messageBuffers: map[string]string{},
	}
}

// copilotEvent 是 `copilot --output-format json` 的单个 JSONL 事件。
// 字段扁平地放在 "data" 中；result 等事件把 sessionId 放在顶层，
// 因此两层都要读取。未知字段被忽略，以便新版本事件格式优雅降级。
type copilotEvent struct {
	Type      string      `json:"type"`
	SessionID string      `json:"sessionId"`
	Data      copilotData `json:"data"`
}

// copilotData 承载适配器引用的负载字段。未知字段被忽略以便降级。
type copilotData struct {
	SessionID    string `json:"sessionId"`
	MessageID    string `json:"messageId"`
	ToolCallID   string `json:"toolCallId"`
	Content      string `json:"content"`
	DeltaContent string `json:"deltaContent"`
	// DeltaContentAlt 兼容部分版本的 snake_case 命名。
	DeltaContentAlt string          `json:"delta_content"`
	Summary         string          `json:"summary"`
	Message         string          `json:"message"`
	ErrorMessage    string          `json:"errorMessage"`
	ErrorType       string          `json:"errorType"`
	ErrorReason     string          `json:"errorReason"`
	ShutdownType    string          `json:"shutdownType"`
	ExitCode        int             `json:"exitCode"`
	InputTokens     int             `json:"inputTokens"`
	OutputTokens    int             `json:"outputTokens"`
	ToolName        string          `json:"toolName"`
	Arguments       json.RawMessage `json:"arguments"`
	Result          copilotResult   `json:"result"`
}

// copilotResult 承载已完成工具的实验结果。
type copilotResult struct {
	Content         string `json:"content"`
	DetailedContent string `json:"detailedContent"`
}

// ResultContent 返回已完成工具的可读结果。
func (d copilotData) ResultContent() string {
	if strings.TrimSpace(d.Result.DetailedContent) != "" {
		return strings.TrimSpace(d.Result.DetailedContent)
	}
	if strings.TrimSpace(d.Result.Content) != "" {
		return strings.TrimSpace(d.Result.Content)
	}
	return strings.TrimSpace(d.Content)
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event copilotEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		d.unparsed.AppendLine(line)
		return
	}
	d.SetSession(event.Data.SessionID)
	if event.SessionID != "" {
		d.SetSession(event.SessionID)
	}

	switch event.Type {
	case "assistant.message":
		// 助手消息以完整文本下发；若该版本只给了 delta，用累积文本兜底。
		d.flushReasoning(emit)
		content := strings.TrimSpace(event.Data.Content)
		if content == "" {
			content = strings.TrimSpace(d.messageBuffers[event.Data.MessageID])
		}
		delete(d.messageBuffers, event.Data.MessageID)
		if event.Data.OutputTokens > 0 {
			d.outputTokens += event.Data.OutputTokens
		}
		d.Message(content, emit)

	case "assistant.message_delta":
		// 增量文本先累积，等完整的 assistant.message 事件一起发出，避免碎片化。
		if delta := event.Data.DeltaContent + event.Data.DeltaContentAlt; delta != "" {
			d.messageBuffers[event.Data.MessageID] += delta
		}

	case "assistant.reasoning":
		if c := strings.TrimSpace(event.Data.Content); c != "" {
			d.reasoningBuffer += c
		}

	case "assistant.reasoning_delta":
		delta := strings.TrimSpace(event.Data.DeltaContent)
		if delta == "" {
			delta = strings.TrimSpace(event.Data.DeltaContentAlt)
		}
		d.reasoningBuffer += delta

	case "assistant.usage", "usage", "usage_info":
		if event.Data.InputTokens > 0 {
			d.inputTokens += event.Data.InputTokens
		}
		if event.Data.OutputTokens > 0 {
			d.outputTokens += event.Data.OutputTokens
		}

	case "tool.execution_start":
		// toolCallId 与 tool.execution_complete 一一对应，用于前端精确配对。
		d.ToolCall(event.Data.ToolCallID, strings.TrimSpace(event.Data.ToolName),
			executor.RawText(event.Data.Arguments), emit)

	case "tool.execution_complete", "tool.result":
		content := event.Data.ResultContent()
		if content == "" && event.Data.ErrorMessage != "" {
			content = "错误: " + event.Data.ErrorMessage
		}
		d.ToolResult(event.Data.ToolCallID, content, emit)

	case "session.error":
		// "routine" 之外的关闭即失败。
		msg := strings.TrimSpace(event.Data.Message)
		if msg == "" {
			msg = event.Data.ErrorType
		}
		if msg != "" {
			d.Fail(msg)
		}
		d.MarkTerminal()

	case "model.call_failure":
		msg := strings.TrimSpace(event.Data.ErrorMessage)
		if msg == "" {
			msg = event.Data.Message
		}
		if msg != "" {
			d.Fail(msg)
		}
		d.MarkTerminal()

	case "session.shutdown":
		// "routine" = 正常结束；"error" = 致命失败。
		if event.Data.ShutdownType == "routine" {
			d.MarkTerminal()
		} else if event.Data.ShutdownType == "error" {
			msg := strings.TrimSpace(event.Data.ErrorReason)
			if msg != "" {
				d.Fail(msg)
			}
			d.MarkTerminal()
		}

	case "result", "session.task_complete", "session.done":
		d.MarkTerminal()
		d.SetSession(event.Data.SessionID)
		if event.Data.ExitCode != 0 {
			if msg := strings.TrimSpace(event.Data.Message); msg != "" {
				d.Fail(msg)
			} else {
				d.Fail(d.DisplayName + " 返回错误结果，但未提供错误详情")
			}
		}
		if event.Data.Summary != "" && event.Type == "session.task_complete" {
			d.Message(event.Data.Summary, emit)
		}
	}
}

// Done 在输出结束后归并 token 并校验终态事件。
func (d *Decoder) Done(emit executor.EmitFunc) {
	d.flushReasoning(emit)
	d.flushMessages(emit)
	d.SetUsage(d.inputTokens, d.outputTokens)
	if !d.TerminalSeen() {
		if output := d.unparsed.String(); output != "" {
			d.AddDetail("未解析的 " + d.DisplayName + " 输出:\n" + output)
		}
		d.FailMissingTerminal("result")
	}
}

// flushMessages 补发只收到增量、未收到完整消息事件的助手文本。
func (d *Decoder) flushMessages(emit executor.EmitFunc) {
	for id, buffered := range d.messageBuffers {
		if text := strings.TrimSpace(buffered); text != "" {
			d.Message(text, emit)
		}
		delete(d.messageBuffers, id)
	}
}

// flushReasoning 输出已累积的推理内容（如有）。
func (d *Decoder) flushReasoning(emit executor.EmitFunc) {
	if text := strings.TrimSpace(d.reasoningBuffer); text != "" {
		d.Thinking(text, emit)
	}
	d.reasoningBuffer = ""
}
