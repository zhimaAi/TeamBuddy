// Package opencode provides OpenCode CLI execution adaptation.
package opencode

import (
	"encoding/json"
	"strings"

	"goteams-client/internal/executor"
)

const displayName = "OpenCode"

// NewAdapter creates the OpenCode adapter.
//
// OpenCode is run headless with `opencode run --format json --auto`, the prompt
// is streamed over stdin (Windows-safe; avoids cmd.exe mangling multi-line
// prompts), and each step finishes with a terminal "step_finish"/"result" event.
// OpenCode ships two event schemas: a legacy top-level layout
// (content/message/parts) and, since >=1.18, a single "part" payload on every
// event. Both are supported below.
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

// buildInvocation 构造 opencode 命令行，prompt 走 stdin。
//
//	opencode run --format json --auto [-m <model>] [--session <id>] [extraArgs]   (prompt on stdin)
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{"run", "--format", "json", "--auto"}
	if resumeSession != "" {
		args = append(args, "--session", resumeSession)
	}
	if opts.ModelProfile != "" {
		args = append(args, "-m", opts.ModelProfile)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--variant", opts.ThinkingLevel)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}

// Decoder 解码 opencode --format json 的 JSONL 输出。
type Decoder struct {
	executor.BaseDecoder

	inputTokens  int
	outputTokens int
	unparsed     executor.DiagnosticTail
}

// NewDecoder 创建 OpenCode 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{BaseDecoder: executor.NewBaseDecoder(name)}
}

// opencodeEvent OpenCode JSON 事件。
type opencodeEvent struct {
	Type         string          `json:"type"`
	Content      json.RawMessage `json:"content"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	SessionID    string          `json:"session_id"`
	SessionIDAlt string          `json:"sessionID"`
	Error        *opencodeError  `json:"error"`
	IsError      bool            `json:"is_error"`
	Message      struct {
		Content []opencodeContentBlock `json:"content"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	// opencode may use the parts structure
	Parts []opencodePart `json:"parts"`
	// opencode >= 1.18 emits every event with a single "part" payload:
	// {"type":"text","part":{"type":"text","text":"..."}}
	// {"type":"tool_use","part":{"type":"tool","tool":"read","state":{...}}}
	// {"type":"step_finish","part":{"reason":"stop",...}}
	Part *opencodePart `json:"part"`
}

// opencodeState carries the execution state of a tool part.
type opencodeState struct {
	Status string          `json:"status"` // pending | completed | error
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
}

// opencodeError OpenCode JSON error event payload.
// OpenCode emits errors as {"type":"error","error":{"name":...,"message":...,"data":{"message":...}}}
type opencodeError struct {
	Name    string         `json:"name"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

type opencodeContentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

type opencodePart struct {
	Type   string         `json:"type"`
	Text   string         `json:"text"`
	Tool   string         `json:"tool"`
	CallID string         `json:"callID"`
	State  *opencodeState `json:"state"`
	Reason string         `json:"reason"`
	// step_finish 事件把本步 token 用量挂在 part 上：
	// {"type":"step_finish","part":{"reason":"stop","tokens":{"input":6343,"output":3,...}}}
	Tokens opencodeTokens `json:"tokens"`
}

// opencodeTokens 是 opencode 在 step_finish 里上报的 token 用量。
// 注意字段名是 input/output（而非 input_tokens/output_tokens）。
type opencodeTokens struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Total  int `json:"total"`
}

// getSessionID 返回会话 ID，兼容 OpenCode 的 snake_case 与 camelCase 字段名。
func (e opencodeEvent) getSessionID() string {
	if e.SessionID != "" {
		return e.SessionID
	}
	return e.SessionIDAlt
}

// getCallID 返回工具调用 ID。只有 part 形态携带 callID；旧的顶层形态没有可信 ID，返回空字符串。
func (e opencodeEvent) getCallID() string {
	if e.Part == nil {
		return ""
	}
	return strings.TrimSpace(e.Part.CallID)
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event opencodeEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		d.unparsed.AppendLine(line)
		return
	}
	if sid := event.getSessionID(); sid != "" {
		d.SetSession(sid)
	}

	// >=1.18 reasoning token：直接作为思考事件输出。
	if event.Part != nil && event.Part.Type == "reasoning" {
		text := event.Part.Text
		if text == "" && event.Part.State != nil {
			text = event.Part.State.Output
		}
		d.Thinking(text, emit)
		return
	}

	switch event.Type {
	case "assistant", "text", "message":
		d.decodeContent(event, emit)

	case "tool_use", "tool_call":
		d.decodeToolCall(event, emit)

	case "tool_result":
		d.ToolResult(event.getCallID(), executor.RawText(event.Content), emit)

	case "step_finish":
		// opencode >=1.18 以 type=step_finish 结束每一步；reason="stop" 表示整个运行结束。
		if event.Part != nil && event.Part.Reason == "stop" {
			d.MarkTerminal()
		}
		if event.Part != nil && event.Part.Reason == "error" {
			d.Fail("OpenCode 返回错误事件")
			d.MarkTerminal()
		}
		// 本步 token 用量挂在 part.tokens 上（字段名是 input/output）。
		// 按步累加，Done() 时统一 SetUsage 上报。
		if event.Part != nil {
			d.inputTokens += event.Part.Tokens.Input
			d.outputTokens += event.Part.Tokens.Output
		}

	case "result", "complete", "done":
		d.MarkTerminal()
		if sid := event.getSessionID(); sid != "" {
			d.SetSession(sid)
		}
		if event.Usage.InputTokens > 0 {
			d.inputTokens = event.Usage.InputTokens
		}
		if event.Usage.OutputTokens > 0 {
			d.outputTokens = event.Usage.OutputTokens
		}
		if event.IsError {
			msg := extractErrorMessage(event)
			if msg != "" {
				d.Fail(msg)
			} else {
				d.Fail("OpenCode 返回错误结果")
			}
		}

	case "error":
		msg := extractErrorMessage(event)
		if msg != "" {
			d.Fail(msg)
		} else {
			d.Fail("OpenCode 返回错误事件")
		}
		d.MarkTerminal()
	}
}

// decodeContent 处理 assistant/text/message 事件，分离文本与推理内容。
func (d *Decoder) decodeContent(event opencodeEvent, emit executor.EmitFunc) {
	message, thinking := extractMessageContent(event)
	if thinking != "" {
		d.Thinking(thinking, emit)
	}
	if message != "" {
		d.Message(message, emit)
	}
}

// decodeToolCall 处理 tool_use/tool_call 事件，兼容新（part）与旧（顶层）两种 schema。
func (d *Decoder) decodeToolCall(event opencodeEvent, emit executor.EmitFunc) {
	if event.Part != nil {
		callID := strings.TrimSpace(event.Part.CallID)
		name := event.Part.Tool
		if name == "" {
			name = event.Name
		}
		input := ""
		if event.Part.State != nil {
			input = executor.RawText(event.Part.State.Input)
		}
		if input == "" {
			input = executor.RawText(event.Input)
		}
		callContent := strings.TrimSpace(name + " " + input)
		if event.Part.State != nil && event.Part.State.Status == "completed" {
			// 同一事件既携带调用也携带已完成的结果，两者都输出
			if callContent != "" {
				d.ToolCall(callID, callContent, "", emit)
			}
			d.ToolResult(callID, event.Part.State.Output, emit)
			return
		}
		d.ToolCall(callID, callContent, "", emit)
		if event.Part.State != nil && event.Part.State.Error != "" {
			d.ToolResult(callID, "错误: "+event.Part.State.Error, emit)
		}
		return
	}

	// Legacy format: name/input at the top level.
	content := strings.TrimSpace(event.Name + " " + executor.RawText(event.Input))
	d.ToolCall("", content, "", emit)
}

// Done 在输出结束后归并 token 并校验终态事件。
func (d *Decoder) Done(emit executor.EmitFunc) {
	d.SetUsage(d.inputTokens, d.outputTokens)
	if !d.TerminalSeen() {
		if output := d.unparsed.String(); output != "" {
			d.AddDetail("未解析的 " + d.DisplayName + " 输出:\n" + output)
		}
		d.FailMissingTerminal("result")
	}
}

// extractMessageContent 从 assistant/text/message 事件中提取文本与推理内容。
// 兼容新（单 part）与旧（message.content / parts / content）两种 schema，
// 旧 schema 除 text 块外也识别 reasoning 块以产出思考事件。
func extractMessageContent(event opencodeEvent) (message, thinking string) {
	// 新格式（>=1.18）：文本/推理都在单个 "part" 字段中。
	if event.Part != nil {
		switch event.Part.Type {
		case "text":
			return event.Part.Text, ""
		case "reasoning":
			text := event.Part.Text
			if text == "" && event.Part.State != nil {
				text = event.Part.State.Output
			}
			return "", text
		}
		return "", ""
	}

	// 旧格式：从 message.content 与 parts 中提取。
	var texts, reasonings []string
	for _, block := range event.Message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case "reasoning":
			if block.Text != "" {
				reasonings = append(reasonings, block.Text)
			}
		}
	}
	for _, part := range event.Parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case "reasoning":
			if part.Text != "" {
				reasonings = append(reasonings, part.Text)
			}
		}
	}

	if len(texts) == 0 {
		if t := executor.RawText(event.Content); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n"), strings.Join(reasonings, "\n")
}

// extractErrorMessage 从 OpenCode error 事件中提取可读错误信息。
// OpenCode 将 message 嵌套在 error.message 或 error.data.message，也可能携带纯 content 负载。
func extractErrorMessage(event opencodeEvent) string {
	if event.Error != nil {
		if msg := strings.TrimSpace(event.Error.Message); msg != "" {
			return msg
		}
		if event.Error.Data != nil {
			if m, ok := event.Error.Data["message"].(string); ok && strings.TrimSpace(m) != "" {
				return strings.TrimSpace(m)
			}
		}
		if name := strings.TrimSpace(event.Error.Name); name != "" {
			return name
		}
	}
	if content := strings.TrimSpace(executor.RawText(event.Content)); content != "" {
		return content
	}
	return ""
}
