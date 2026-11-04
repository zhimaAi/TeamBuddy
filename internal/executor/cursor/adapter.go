// Package cursor 提供官方 Cursor CLI 的执行适配。安装命令是 agent，不是 IDE 的 cursor，也不是兼容名 cursor-agent。
//
// 协议（--output-format stream-json，逐行 JSON）：
//
//	{"type":"system","subtype":"init","session_id":...}                会话初始化
//	{"type":"thinking","subtype":"delta","text":"..."}                 思考增量文本
//	{"type":"thinking","subtype":"completed"}                          思考段结束
//	{"type":"assistant","message":{"content":[{"type":"text",...}]}}   助手消息
//	{"type":"tool_call","subtype":"started","call_id":...,
//	   "tool_call":{"readToolCall":{"args":{...}}}}                     工具调用开始
//	{"type":"tool_call","subtype":"completed","call_id":...,
//	   "tool_call":{"readToolCall":{"args":{...},"result":{"success":{...}}}}} 工具结果
//	{"type":"result","subtype":"success","usage":{...}}                终态与用量
//
// 思考以 delta 增量下发，需累积到 completed 再作为一条思考事件发出；
// 工具调用与结果用外层 call_id 配对（该字段可能含换行符，统一规范化）。
package cursor

import (
	"encoding/json"
	"strings"

	"goteams-client/internal/executor"
)

const displayName = "Cursor CLI"

// NewAdapter 创建 Cursor Agent 适配器。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:       displayName,
		SupportsThinking:  true,
		PrepareInvocation: prepareThinkingInvocation,
		BuildInvocation:   buildInvocation,
		NewDecoder: func(name string) executor.Decoder {
			return NewDecoder(name)
		},
	})
}

// buildInvocation 构造命令行。
//
//	新会话: agent -p --force --output-format stream-json [--model <id>] [extraArgs] <prompt>
//	续聊:   agent -p --force --output-format stream-json --resume <chatId> [--model <id>] [extraArgs] <prompt>
//
// -p/--print 进入非交互模式（否则会启动 TUI 并等待终端交互）；
// --force 免审批执行工具调用（等价 --yolo）；prompt 作为位置参数传递。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := make([]string, 0, 8)
	args = append(args, "-p", "--force", "--output-format", "stream-json")
	if resumeSession != "" {
		args = append(args, "--resume", resumeSession)
	}
	if opts.ModelProfile != "" {
		args = append(args, "--model", opts.ModelProfile)
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, opts.Prompt)
	return executor.Invocation{Args: args}
}

// toolArgKeys 是各工具调用的代表参数：取该字段作为调用摘要，最能表达意图。
var toolArgKeys = map[string]string{
	"readToolCall":      "path",
	"editToolCall":      "path",
	"shellToolCall":     "command",
	"grepToolCall":      "pattern",
	"searchToolCall":    "query",
	"listToolCall":      "path",
	"deleteToolCall":    "path",
	"webSearchToolCall": "query",
	"webFetchToolCall":  "url",
	"fetchToolCall":     "url",
}

// Decoder 解码 Cursor Agent 的 stream-json 输出。
type Decoder struct {
	executor.BaseDecoder

	// thinking 累积一段思考的 delta 文本，completed 时整段发出。
	thinking strings.Builder
	// calls 记录已发出调用事件的工具 ID，避免在结果行重复补发。
	calls map[string]bool
	// unparsed 收集无法解析为 JSON 的行，作为失败诊断细节。
	unparsed executor.DiagnosticTail
}

// NewDecoder 创建 Cursor 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{
		BaseDecoder: executor.NewBaseDecoder(name),
		calls:       make(map[string]bool),
	}
}

// cursorEvent 是 Cursor stream-json 的单行事件。
type cursorEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
	// CallID 是工具调用配对 ID，协议侧可能夹带换行符（形如 "<id>-0\n<id>_0"）。
	CallID  string          `json:"call_id"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error"`
	Message cursorMessage   `json:"message"`
	ToolMap map[string]any  `json:"tool_call"`
	Usage   cursorUsageInfo `json:"usage"`
}

type cursorMessage struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// cursorUsageInfo 的字段与协议一致，使用驼峰命名。
type cursorUsageInfo struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event cursorEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		d.unparsed.AppendLine(line)
		return
	}
	if event.Type == "user" {
		// 用户输入回显，不作为执行过程事件下发。
		return
	}

	switch event.Type {
	case "system":
		if event.Subtype == "init" && event.SessionID != "" {
			d.SetSession(event.SessionID)
		}

	case "thinking":
		switch event.Subtype {
		case "delta":
			d.thinking.WriteString(event.Text)
		case "completed":
			d.flushThinking(emit)
		}

	case "assistant":
		d.Message(messageText(event.Message), emit)

	case "tool_call":
		d.decodeToolCall(event, emit)

	case "result":
		if event.SessionID != "" {
			d.SetSession(event.SessionID)
		}
		d.MarkTerminal()
		if event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 {
			d.SetUsage(event.Usage.InputTokens, event.Usage.OutputTokens)
		}
		if event.IsError || event.Subtype != "success" {
			detail := strings.TrimSpace(event.Result)
			if detail == "" {
				detail = d.DisplayName + " 执行失败"
			}
			d.Fail(detail)
		}
	}
}

// flushThinking 把已累积的思考文本作为一条事件发出。
func (d *Decoder) flushThinking(emit executor.EmitFunc) {
	text := strings.TrimSpace(d.thinking.String())
	d.thinking.Reset()
	if text != "" {
		d.Thinking(text, emit)
	}
}

// decodeToolCall 处理工具调用的开始与结束行。
func (d *Decoder) decodeToolCall(event cursorEvent, emit executor.EmitFunc) {
	toolKey, payload := findToolPayload(event.ToolMap)
	callID := normalizeCallID(event.CallID)
	if toolKey == "" {
		// 结构未知时至少透出结果原文，避免执行过程缺环。
		if event.Subtype == "completed" {
			d.ToolResult(callID, strings.TrimSpace(event.Result), emit)
		}
		return
	}
	name := buildToolName(toolKey)
	args := extractToolArgs(payload)

	switch event.Subtype {
	case "started":
		d.calls[callID] = true
		d.ToolCallKind(callID, name, summarizeArgs(toolKey, args), cursorToolKind(toolKey), emit)
	case "completed":
		// 个别工具可能只在结果行出现，补发调用事件保证过程完整。
		if !d.calls[callID] {
			d.calls[callID] = true
			d.ToolCallKind(callID, name, summarizeArgs(toolKey, args), cursorToolKind(toolKey), emit)
		}
		d.ToolResult(callID, summarizeResult(payload), emit)
	}
}

// findToolPayload 从 tool_call 对象里取出工具类型与其 payload
// （协议形如 {"readToolCall": {"args": {...}, "result": {...}}}）。
func findToolPayload(toolMap map[string]any) (string, map[string]any) {
	for key, value := range toolMap {
		if !strings.HasSuffix(key, "ToolCall") {
			continue
		}
		payload, ok := value.(map[string]any)
		if !ok {
			continue
		}
		return key, payload
	}
	return "", nil
}

// extractToolArgs 取出工具参数表。
func extractToolArgs(payload map[string]any) map[string]any {
	if args, ok := payload["args"].(map[string]any); ok {
		return args
	}
	return map[string]any{}
}

// buildToolName 把协议的工具键转成可读名：readToolCall -> Read。
func cursorToolKind(toolKey string) string {
	if toolKey == "shellToolCall" {
		return executor.ToolKindShell
	}
	return ""
}

func buildToolName(raw string) string {
	name := strings.TrimSuffix(raw, "ToolCall")
	if name == "" {
		return raw
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// summarizeArgs 生成参数摘要：优先取该工具最具代表性的字段，取不到时回退到 JSON。
func summarizeArgs(toolKey string, args map[string]any) string {
	if field, ok := toolArgKeys[toolKey]; ok {
		if value, ok := args[field].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	filtered := make(map[string]any, len(args))
	for key, value := range args {
		if key == "result" || key == "toolCallId" {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return ""
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// summarizeResult 生成工具结果摘要：
// read 取文件内容、shell 取 stdout（非零退出附 stderr）、edit 取变更说明。
func summarizeResult(payload map[string]any) string {
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return ""
	}
	success, ok := result["success"].(map[string]any)
	if !ok {
		if failure, ok := result["error"].(map[string]any); ok {
			return stringifyAny(failure)
		}
		return stringifyAny(result)
	}
	if content, ok := success["content"].(string); ok && strings.TrimSpace(content) != "" {
		return content
	}
	if stdout, ok := success["stdout"].(string); ok {
		code, _ := success["exitCode"].(float64)
		if code != 0 {
			if stderr, ok := success["stderr"].(string); ok && strings.TrimSpace(stderr) != "" {
				return strings.TrimSpace(stdout + "\n[stderr] " + stderr)
			}
		}
		if strings.TrimSpace(stdout) != "" {
			return stdout
		}
	}
	if code, ok := success["exitCode"].(float64); ok && code != 0 {
		if stderr, ok := success["stderr"].(string); ok && strings.TrimSpace(stderr) != "" {
			return stderr
		}
	}
	for _, field := range []string{"message", "diffString"} {
		if value, ok := success[field].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return stringifyAny(success)
}

// messageText 拼接助手消息中的文本片段。
func messageText(message cursorMessage) string {
	var builder strings.Builder
	for _, part := range message.Content {
		if part.Type != "" && part.Type != "text" {
			continue
		}
		builder.WriteString(part.Text)
	}
	return strings.TrimSpace(builder.String())
}

// normalizeCallID 规范化工具调用 ID（协议侧可能夹带换行符）。
func normalizeCallID(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "\n", "")
}

// stringifyAny 把任意 JSON 值渲染成字符串，供摘要使用。
func stringifyAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// Done 在输出结束后校验终态事件。
func (d *Decoder) Done(emit executor.EmitFunc) {
	// 进程可能在思考段中间结束，先把已累积的思考补发。
	d.flushThinking(emit)
	if output := d.unparsed.String(); output != "" {
		d.AddDetail("未解析的 " + d.DisplayName + " 输出:\n" + output)
	}
	d.FailMissingTerminal("结束")
}
