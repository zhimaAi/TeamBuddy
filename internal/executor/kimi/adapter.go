// Package kimi provides Kimi Code CLI execution adaptation.
package kimi

import (
	"encoding/json"
	"strings"

	"goteams-client/internal/executor"
)

const displayName = "Kimi Code"

// NewAdapter creates the Kimi Code adapter.
//
// Kimi's `--output-format stream-json` uses a role-based JSONL contract
// (assistant / tool / meta) that differs from Claude's type-based stream-json,
// so it gets its own streaming parser. The prompt is delivered as the `-p`
// argument (not stdin), so the process never blocks waiting for input.
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

// buildInvocation 构造 kimi 命令行。
//
//	新会话: kimi -p <prompt> --output-format stream-json [-m <model>] [extraArgs]
//	续聊:   kimi -S <session-id> -p <prompt> --output-format stream-json [-m <model>] [extraArgs]
//
// prompt 作为 -p 参数传递，不占用 stdin（Invocation.StdinText 留空）。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := make([]string, 0, 8)
	if resumeSession != "" {
		args = append(args, "-S", resumeSession)
	}
	args = append(args, "-p", opts.Prompt, "--output-format", "stream-json")
	if opts.ModelProfile != "" {
		args = append(args, "-m", opts.ModelProfile)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args}
}

// Decoder 解码 Kimi Code stream-json 的 role-based JSONL 输出。
type Decoder struct {
	executor.BaseDecoder

	// unparsed 收集无法解析为 JSON 的行，作为失败诊断细节。
	unparsed executor.DiagnosticTail
}

// NewDecoder 创建 Kimi 解码器。
func NewDecoder(name string) executor.Decoder {
	if strings.TrimSpace(name) == "" {
		name = displayName
	}
	return &Decoder{BaseDecoder: executor.NewBaseDecoder(name)}
}

// kimiEvent 是 Kimi Code stream-json 的单行事件（role-based）。
type kimiEvent struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	// 思考字段兼容多种 CLI 输出命名（Kimi CLI 思考字段名未在本机验证，未安装 kimi），
	// 故同时识别 reasoning_content / reasoning / thinking / reasoningContent，取第一个非空值。
	ReasoningContent    string         `json:"reasoning_content"`
	Reasoning           string         `json:"reasoning"`
	Thinking            string         `json:"thinking"`
	ReasoningContentAlt string         `json:"reasoningContent"`
	ToolCalls           []kimiToolCall `json:"tool_calls"`
}

type kimiToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Decode 解析一行输出。
func (d *Decoder) Decode(raw string, emit executor.EmitFunc) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	var event kimiEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		d.unparsed.AppendLine(line)
		return
	}
	// 原 sawEvent 兜底：只要解析到任意事件即视为已输出（等价 FailMissingTerminal 语义）。
	d.MarkTerminal()

	switch event.Role {
	case "assistant":
		if thinking := extractThinking(event); thinking != "" {
			d.Thinking(thinking, emit)
		}
		if content := strings.TrimSpace(event.Content); content != "" {
			d.Message(content, emit)
		}
		for _, tc := range event.ToolCalls {
			name := strings.TrimSpace(tc.Function.Name)
			args := strings.TrimSpace(tc.Function.Arguments)
			d.ToolCall(tc.ID, name, args, emit)
		}

	case "tool":
		// kimi 的工具结果行不带调用 ID，仅在唯一候选时归属。
		d.ToolResult("", strings.TrimSpace(event.Content), emit)

	case "meta":
		// session.resume_hint meta 行携带续聊所需 session id（kimi -S <id>）。
		if event.Type == "session.resume_hint" && event.SessionID != "" {
			d.SetSession(event.SessionID)
		}
	}
}

// Done 在输出结束后校验终态事件。
func (d *Decoder) Done(emit executor.EmitFunc) {
	if output := d.unparsed.String(); output != "" {
		d.AddDetail("未解析的 " + d.DisplayName + " 输出:\n" + output)
	}
	d.FailMissingTerminal("结束")
}

// extractThinking 提取 assistant 消息中的思考文本，兼容多种字段命名。
// Kimi CLI 思考字段名无法本机验证，故同时识别 reasoning_content / reasoning /
// thinking / reasoningContent，取第一个非空值（已在外层 TrimSpace）。
func extractThinking(event kimiEvent) string {
	return strings.TrimSpace(firstNonEmpty(
		event.ReasoningContent,
		event.Reasoning,
		event.Thinking,
		event.ReasoningContentAlt,
	))
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// LooseTerminal 表示本解码器的 TerminalSeen 只代表「有过输出」，不能据此提前结束本轮。
func (d *Decoder) LooseTerminal() bool { return true }
