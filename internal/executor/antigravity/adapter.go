// Package antigravity 提供 Antigravity CLI（agy）的执行适配。
package antigravity

import (
	"encoding/json"
	"strconv"
	"strings"

	"goteams-client/internal/executor"
)

const displayName = "Antigravity CLI"

// printTimeout 盖过旧版本 agy 默认的 5 分钟单轮上限。
// 客户端自己的空闲与工具时限仍然生效；这里只避免 CLI 先把还在工作的一轮掐掉。
const printTimeout = "60m"

// NewAdapter 创建 Antigravity CLI 适配器。
//
// 直接执行使用官方 headless 的 stdin stream-json：一行 user 事件进，
// init / step_update / result 事件出。-p 在这种输入格式下会被丢掉，所以提示词只走 stdin。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:      displayName,
		SupportsThinking: true,
		BuildInvocation:  buildInvocation,
		NewDecoder: func(name string) executor.Decoder {
			return NewDecoder(name)
		},
	})
}

// buildInvocation 构造一次无头执行。
//
//	agy --input-format stream-json --output-format stream-json
//	    --dangerously-skip-permissions --print-timeout 60m
//	    [--add-dir <workdir>] [--conversation <id>] [--model <slug>]
//
// stdin 写入一行 {"event":"user","message":{"content":...}} 后由进程运行器关闭。
// 关闭 stdin 后，当前轮的 result 到达，进程退出。--add-dir 让文件改动落在任务目录，
// 而不是 agy 的 scratch。--dangerously-skip-permissions 与其他直接执行 CLI 一样跳过确认。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--print-timeout", printTimeout,
	}
	if dir := strings.TrimSpace(opts.WorkDir); dir != "" {
		args = append(args, "--add-dir", dir)
	}
	if resumeSession != "" {
		args = append(args, "--conversation", resumeSession)
	}
	if model := strings.TrimSpace(opts.ModelProfile); model != "" && model != "auto" {
		args = append(args, "--model", model)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--effort", opts.ThinkingLevel)
	}
	args = append(args, opts.ExtraArgs...)

	payload, _ := json.Marshal(map[string]any{
		"event": "user",
		"message": map[string]string{
			"content": opts.Prompt,
		},
	})
	return executor.Invocation{Args: args, StdinText: string(payload) + "\n"}
}

// agyEvent 是 stream-json 的一行。
type agyEvent struct {
	Event          string     `json:"event"`
	ConversationID string     `json:"conversation_id"`
	StepUpdate     *agyStep   `json:"step_update"`
	Result         *agyResult `json:"result"`
}

type agyStep struct {
	ConversationID string          `json:"conversation_id"`
	StepIndex      int             `json:"step_index"`
	State          string          `json:"state"`
	StepType       string          `json:"step_type"`
	ToolName       string          `json:"tool_name"`
	TextDelta      string          `json:"text_delta"`
	Usage          agyUsage        `json:"usage"`
	ToolInfo       *agyToolInfo    `json:"tool_info"`
	SubagentInfo   json.RawMessage `json:"subagent_info"`
}

type agyToolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Output     json.RawMessage `json:"output"`
	Error      *agyToolError   `json:"error"`
}

type agyToolError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type agyResult struct {
	ConversationID string   `json:"conversation_id"`
	Status         string   `json:"status"`
	Response       string   `json:"response"`
	Error          string   `json:"error"`
	Usage          agyUsage `json:"usage"`
}

type agyUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type agySubagentInfo struct {
	Subagents []struct {
		TypeName string `json:"type_name"`
		Role     string `json:"role"`
	} `json:"subagents"`
}

// Decoder 解码 Antigravity CLI 的 stream-json。
type Decoder struct {
	executor.BaseDecoder

	responseBuf strings.Builder
	thinkingBuf strings.Builder
	// streamed 是已经发给界面的助手文本，用来避免 result.response 再发一遍。
	streamed     strings.Builder
	openedTools  map[int]struct{}
	openedAgents map[string]struct{}
	stepUsageIn  int
	stepUsageOut int
	resultSeen   bool
	unparsed     executor.DiagnosticTail
}

// NewDecoder 创建解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{
		BaseDecoder:  executor.NewBaseDecoder(name),
		openedTools:  map[int]struct{}{},
		openedAgents: map[string]struct{}{},
	}
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event agyEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil || event.Event == "" {
		d.unparsed.AppendLine(line)
		return
	}
	if event.ConversationID != "" {
		d.SetSession(event.ConversationID)
	}

	switch event.Event {
	case "init":
		return
	case "step_update":
		if event.StepUpdate != nil {
			d.decodeStep(*event.StepUpdate, emit)
		}
	case "result":
		if event.Result != nil {
			d.decodeResult(*event.Result, emit)
		}
	}
}

func (d *Decoder) decodeStep(step agyStep, emit executor.EmitFunc) {
	d.SetSession(step.ConversationID)
	d.trackUsage(step.Usage)

	switch step.StepType {
	case "user_input", "checkpoint":
		return
	case "agent_response":
		d.responseBuf.WriteString(step.TextDelta)
		if stepDone(step.State) {
			d.flushResponse(emit)
		}
	case "thinking", "reasoning", "agent_thinking":
		d.thinkingBuf.WriteString(step.TextDelta)
		if stepDone(step.State) {
			d.flushThinking(emit)
		}
	case "tool":
		d.decodeTool(step, emit)
	default:
		if step.ToolInfo != nil {
			d.decodeTool(step, emit)
			return
		}
		if len(step.SubagentInfo) > 0 && string(step.SubagentInfo) != "null" {
			d.decodeSubagents(step, emit)
		}
	}
}

func (d *Decoder) decodeTool(step agyStep, emit executor.EmitFunc) {
	name := strings.TrimSpace(step.ToolName)
	args := ""
	if step.ToolInfo != nil {
		if toolName := strings.TrimSpace(step.ToolInfo.Name); toolName != "" {
			name = toolName
		}
		args = executor.FormatToolArgs(step.ToolInfo.Parameters)
	}
	if _, opened := d.openedTools[step.StepIndex]; !opened {
		d.ToolCall(stepID(step.StepIndex), name, args, emit)
		d.openedTools[step.StepIndex] = struct{}{}
	}
	if !stepDone(step.State) {
		return
	}
	d.ToolResult(stepID(step.StepIndex), toolOutput(step.ToolInfo), emit)
}

func (d *Decoder) decodeSubagents(step agyStep, emit executor.EmitFunc) {
	if !stepDone(step.State) {
		return
	}
	var info agySubagentInfo
	if json.Unmarshal(step.SubagentInfo, &info) != nil || len(info.Subagents) == 0 {
		key := stepID(step.StepIndex)
		if _, opened := d.openedAgents[key]; opened {
			return
		}
		d.openedAgents[key] = struct{}{}
		d.ToolCall(key, "subagent", executor.OneLine(executor.RawText(step.SubagentInfo)), emit)
		d.ToolResult(key, "", emit)
		return
	}
	for i, agent := range info.Subagents {
		key := stepID(step.StepIndex) + "-" + strconv.Itoa(i)
		if _, opened := d.openedAgents[key]; opened {
			continue
		}
		d.openedAgents[key] = struct{}{}
		name := strings.TrimSpace(agent.TypeName)
		if name == "" {
			name = "subagent"
		}
		d.ToolCall(key, name, strings.TrimSpace(agent.Role), emit)
		d.ToolResult(key, "", emit)
	}
}

func (d *Decoder) decodeResult(result agyResult, emit executor.EmitFunc) {
	d.resultSeen = true
	d.SetSession(result.ConversationID)
	d.flushThinking(emit)
	d.flushResponse(emit)
	if result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0 {
		d.SetUsage(result.Usage.InputTokens, result.Usage.OutputTokens)
	}

	status := strings.ToUpper(strings.TrimSpace(result.Status))
	switch status {
	case "", "SUCCESS":
		if strings.TrimSpace(result.Error) != "" && status == "" {
			d.Fail(strings.TrimSpace(result.Error))
			d.MarkTerminal()
			return
		}
		d.MarkTerminal()
		if strings.TrimSpace(d.streamed.String()) == "" {
			d.Message(result.Response, emit)
		}
	default:
		d.MarkTerminal()
		reason := strings.TrimSpace(result.Error)
		if reason == "" {
			reason = status
		}
		d.Fail(reason)
	}
}

// Done 在输出结束后补发未完成的文本，并要求见到 result。
func (d *Decoder) Done(emit executor.EmitFunc) {
	d.flushThinking(emit)
	d.flushResponse(emit)
	if !d.resultSeen {
		d.SetUsage(d.stepUsageIn, d.stepUsageOut)
	}
	if output := d.unparsed.String(); output != "" {
		d.AddDetail("未解析的 " + d.DisplayName + " 输出:\n" + output)
	}
	d.FailMissingTerminal("result")
}

func (d *Decoder) flushResponse(emit executor.EmitFunc) {
	text := d.responseBuf.String()
	d.responseBuf.Reset()
	if strings.TrimSpace(text) == "" {
		return
	}
	d.streamed.WriteString(text)
	d.Message(text, emit)
}

func (d *Decoder) flushThinking(emit executor.EmitFunc) {
	text := d.thinkingBuf.String()
	d.thinkingBuf.Reset()
	d.Thinking(text, emit)
}

func (d *Decoder) trackUsage(usage agyUsage) {
	if usage.InputTokens > d.stepUsageIn {
		d.stepUsageIn = usage.InputTokens
	}
	if usage.OutputTokens > d.stepUsageOut {
		d.stepUsageOut = usage.OutputTokens
	}
}

func stepDone(state string) bool {
	state = strings.TrimSpace(state)
	return state == "" || strings.EqualFold(state, "DONE")
}

func stepID(index int) string {
	return "step-" + strconv.Itoa(index)
}

func toolOutput(info *agyToolInfo) string {
	if info == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if info.Error != nil {
		if message := strings.TrimSpace(info.Error.Message); message != "" {
			parts = append(parts, message)
		}
	}
	if text := executor.RawText(info.Output); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}
