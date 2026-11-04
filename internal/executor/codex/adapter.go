package codex

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"goteams-client/internal/executor"
)

const fullPermissionArg = "--dangerously-bypass-approvals-and-sandbox"
const displayName = "Codex"

// NewAdapter 创建 Codex 适配器。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		SupportsThinking: true,
		DisplayName:      displayName,
		BuildInvocation:  buildInvocation,
		NewDecoder:       NewDecoder,
	})
}

// buildInvocation 构造 codex exec 命令行，prompt 走 stdin。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{"exec"}
	if resumeSession != "" {
		args = append(args, "resume", fullPermissionArg, resumeSession, "--json", "-")
	} else {
		args = append(args, fullPermissionArg, "-", "--json")
	}
	if opts.ModelProfile != "" {
		args = append(args, "-m", opts.ModelProfile)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(opts.ThinkingLevel))
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}

// Decoder 解码 codex exec --json 的 JSONL 输出。
type Decoder struct {
	executor.BaseDecoder

	// token 有两套上报标准：过程用量（usage/token_usage）与轮次汇总（turn.completed）。
	// 两者可能同时出现，分别累计后取较大值，既避免重复累加也不丢失统计。
	deltaInput  int
	deltaOutput int
	turnInput   int
	turnOutput  int
	unparsed    executor.DiagnosticTail
	// toolCallItems 记录已产出工具调用的 item id。
	// 部分 codex 版本只发 item.completed 不发 item.started，
	// 据此在完成阶段补发，避免工具调用记录缺失。
	toolCallItems map[string]bool
}

// NewDecoder 创建 Codex 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{
		BaseDecoder:   executor.NewBaseDecoder(name),
		toolCallItems: make(map[string]bool),
	}
}

// codexEvent 是 codex exec --json 的单行事件。
type codexEvent struct {
	Type         string          `json:"type"`
	Content      json.RawMessage `json:"content"`
	ThreadID     string          `json:"thread_id"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	Item         codexItem       `json:"item"`
	Usage        codexUsage      `json:"usage"`
	Error        struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// codexItem 是 thread item，item.type 决定语义。
type codexItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Query            string          `json:"query"`
	Name             string          `json:"name"`
	Server           string          `json:"server"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Message          string          `json:"message"`
	Status           string          `json:"status"`
	Changes          []codexChange   `json:"changes"`
}

type codexChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event codexEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		d.unparsed.AppendLine(line)
		return
	}
	d.SetSession(event.ThreadID)

	switch event.Type {
	case "thread.started":
		d.SetSession(event.ThreadID)

	case "turn.started":
		d.Heartbeat(emit)

	case "message", "assistant", "response":
		d.Message(executor.RawText(event.Content), emit)

	case "tool_call", "function_call":
		d.ToolCall("", executor.RawText(event.Content), "", emit)

	case "tool_result", "function_call_output":
		d.ToolResult("", executor.RawText(event.Content), emit)

	case "item.started", "item.updated":
		// 工具开始执行：reasoning / todo 类 item 在这一阶段没有可展示内容
		if label := event.Item.toolLabel(); label != "" {
			d.ToolCallKind(event.Item.ID, label, "", event.Item.toolKind(), emit)
			d.markToolCall(event.Item)
		}

	case "item.completed":
		d.decodeCompletedItem(event.Item, emit)

	case "usage", "token_usage":
		d.deltaInput += event.InputTokens
		d.deltaOutput += event.OutputTokens
		d.Usage(event.InputTokens, event.OutputTokens, emit)

	case "turn.completed":
		d.MarkTerminal()
		d.turnInput += event.Usage.InputTokens
		d.turnOutput += event.Usage.OutputTokens
		d.Usage(event.Usage.InputTokens, event.Usage.OutputTokens, emit)

	case "turn.failed":
		d.MarkTerminal()
		message := d.errorText(event, "Codex 执行失败")
		d.Fail(message)
		if executor.LooksFatalUpstream(message) {
			d.SetExitHint(executor.ExitFatalUpstream)
		} else {
			d.SetExitHint(executor.ExitFatalCLI)
		}

	case "error":
		message := d.errorText(event, "")
		if kind, attempt, max, ok := classifyCodexNotice(message); ok {
			emit(executor.ExecutorEvent{
				Type:    executor.EventActivity,
				Content: kind,
				Activity: &executor.ActivityInfo{
					Kind:    kind,
					Attempt: attempt,
					Max:     max,
				},
			})
			return
		}
		d.Fail(message)
		if executor.LooksFatalUpstream(message) {
			d.SetExitHint(executor.ExitFatalUpstream)
		}

	case "complete", "done", "finished":
		d.deltaInput += event.InputTokens
		d.deltaOutput += event.OutputTokens
	}
}

// decodeCompletedItem 处理 item.completed：
// 输出与思考直接映射，工具类 item 补发工具调用后输出执行结果。
func (d *Decoder) decodeCompletedItem(item codexItem, emit executor.EmitFunc) {
	switch item.Type {
	case "agent_message":
		d.Message(item.Text, emit)
	case "reasoning":
		// Codex 的思考摘要在 reasoning item 的 text 字段
		d.Thinking(item.Text, emit)
	case "error":
		d.AddDetail(item.Message)
	default:
		if label := item.toolLabel(); label != "" && !d.toolCallItems[item.ID] {
			d.ToolCallKind(item.ID, label, "", item.toolKind(), emit)
			d.markToolCall(item)
		}
		d.ToolResult(item.ID, item.resultText(), emit)
	}
}

// Done 在输出结束后归并 token 并校验终态事件。
func (d *Decoder) Done(emit executor.EmitFunc) {
	d.SetUsage(maxInt(d.deltaInput, d.turnInput), maxInt(d.deltaOutput, d.turnOutput))
	if !d.TerminalSeen() {
		if output := d.unparsed.String(); output != "" {
			d.AddDetail("未解析的 Codex 输出:\n" + output)
		}
	}
	d.FailMissingTerminal("turn.completed")
}

func (d *Decoder) markToolCall(item codexItem) {
	if item.ID != "" {
		d.toolCallItems[item.ID] = true
	}
}

func (d *Decoder) errorText(event codexEvent, fallback string) string {
	if text := strings.TrimSpace(event.Error.Message); text != "" {
		return text
	}
	if text := executor.RawText(event.Content); text != "" {
		return text
	}
	return fallback
}

// toolLabel 返回工具调用的展示标签：工具名 + 关键参数。
func (item codexItem) toolLabel() string {
	switch item.Type {
	case "command_execution":
		return item.Command
	case "mcp_tool_call":
		name := item.Name
		if item.Server != "" {
			name = item.Server + "." + name
		}
		return strings.TrimSpace(name + " " + executor.FormatToolArgs(item.Arguments))
	case "web_search":
		return strings.TrimSpace("web_search " + item.Query)
	case "file_change":
		paths := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			if path := strings.TrimSpace(change.Path); path != "" {
				paths = append(paths, path)
			}
		}
		if len(paths) == 0 {
			return "file_change"
		}
		return "file_change " + strings.Join(paths, ", ")
	}
	return ""
}

// resultText 返回工具执行结果文本。
func (item codexItem) resultText() string {
	switch item.Type {
	case "command_execution":
		output := item.AggregatedOutput
		if item.ExitCode != nil && *item.ExitCode != 0 {
			output = strings.TrimSpace(output + "\n退出码 " + executor.FormatExitCode(*item.ExitCode))
		}
		if output != "" {
			return output
		}
		return item.Status
	case "mcp_tool_call":
		if result := executor.RawText(item.Result); result != "" {
			return result
		}
		return executor.RawText(item.Error)
	case "file_change":
		if item.Text != "" {
			return item.Text
		}
		kinds := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			entry := strings.TrimSpace(change.Path)
			if kind := strings.TrimSpace(change.Kind); kind != "" {
				entry = strings.TrimSpace(kind + " " + entry)
			}
			if entry != "" {
				kinds = append(kinds, entry)
			}
		}
		if len(kinds) > 0 {
			return strings.Join(kinds, "\n")
		}
		return item.Status
	}
	return ""
}

func (item codexItem) toolKind() string {
	if item.Type == "command_execution" {
		return executor.ToolKindShell
	}
	return ""
}

var codexRetryRe = regexp.MustCompile(`(?i)^(reconnecting|retrying)\b.*?(\d+)\s*/\s*(\d+)`)

// classifyCodexNotice 只认重连或重试开头的文案，避免把 “not retrying” 当成活动。
func classifyCodexNotice(message string) (kind string, attempt, max int, ok bool) {
	text := strings.TrimSpace(message)
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "reconnecting"):
		kind = executor.ActivityReconnecting
	case strings.HasPrefix(lower, "retrying"):
		kind = executor.ActivityRetrying
	default:
		return "", 0, 0, false
	}
	if groups := codexRetryRe.FindStringSubmatch(text); groups != nil {
		attempt, _ = strconv.Atoi(groups[2])
		max, _ = strconv.Atoi(groups[3])
	}
	return kind, attempt, max, true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
