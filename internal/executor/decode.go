package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"goteams-client/internal/applog"
)

// EmitFunc 是解码器的事件出口。解码器只负责翻译协议，不关心事件如何被消费。
type EmitFunc func(ExecutorEvent)

// Outcome 汇总解码过程中收集到的协议侧执行结果。
type Outcome struct {
	SessionID       string
	InputTokens     int
	OutputTokens    int
	UsageSeen       bool
	FailureReason   string   // 非空表示协议层判定本次执行失败
	ExtraDetails    []string // 协议特有诊断信息（如未解析输出），拼进失败原因
	ExitHint        ExitClass
	MissingTerminal bool
	MissingDetail   string
	ProtocolSeen    bool
	TerminalSeen    bool
}

// Decoder 把一个 CLI 的原始输出翻译成统一事件流。
// 实现者只处理协议差异：进程生命周期、stderr、退出码、错误归并都由运行器与适配器骨架负责。
type Decoder interface {
	// Decode 处理一段原始输出（通常是 stdout 的一行）。
	Decode(raw string, emit EmitFunc)
	// Done 在输出读取结束后调用一次，用于缓冲型协议（单信封、纯文本）的收尾解析。
	Done(emit EmitFunc)
	// Outcome 返回协议侧执行结果。
	Outcome() Outcome
}

// ContentBlock 是 stream-json 类 CLI 通用的内容块结构。
// claude / codebuddy / qoder / qwen 属于同一协议族，共用这一份定义与解码逻辑。
type ContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	Result   json.RawMessage `json:"result"`
	// 工具调用 ID：tool_use 块自己带 id，tool_result 块用 tool_use_id 指回所属调用。
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

// BaseDecoder 提供各 CLI 解码器共用的状态收集与事件发射逻辑。
// 各解码器内嵌它，只实现协议特有的分支判断。
type BaseDecoder struct {
	DisplayName string

	sessionID       string
	inputTokens     int
	outputTokens    int
	usageSeen       bool
	terminalSeen    bool
	failureReason   string
	details         []string
	exitHint        ExitClass
	missingTerminal bool
	missingDetail   string
	protocolSeen    bool
}

// NewBaseDecoder 创建基础解码器。
func NewBaseDecoder(displayName string) BaseDecoder {
	return BaseDecoder{DisplayName: displayName}
}

// SessionID 返回已识别的外部会话 ID。
func (b *BaseDecoder) SessionID() string {
	return b.sessionID
}

// SetSession 记录外部会话 ID，空值不覆盖已有值。
func (b *BaseDecoder) SetSession(id string) {
	if id != "" {
		b.sessionID = id
		b.protocolSeen = true
	}
}

// SetExitHint 记录解码器已经能确定的退出分类。空值不覆盖。
func (b *BaseDecoder) SetExitHint(class ExitClass) {
	if class != "" && b.exitHint == "" {
		b.exitHint = class
	}
}

// ClearExitHint 在后续成功终态到来时去掉早先的失败提示。
func (b *BaseDecoder) ClearExitHint() { b.exitHint = "" }

// Heartbeat 通知看门狗有协议活动，不产生可展示事件。
func (b *BaseDecoder) Heartbeat(emit EmitFunc) {
	b.protocolSeen = true
	if emit != nil {
		emit(ExecutorEvent{Type: EventHeartbeat})
	}
}

// SetUsage 覆盖本次执行的 token 用量（终态事件携带的是总量）。
func (b *BaseDecoder) SetUsage(input, output int) {
	if input > 0 {
		b.inputTokens = input
	}
	if output > 0 {
		b.outputTokens = output
	}
	if input > 0 || output > 0 {
		b.usageSeen = true
	}
}

// AccumulateUsage 累加过程中的 token 用量（仅作为终态缺失时的兜底）。
func (b *BaseDecoder) AccumulateUsage(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
}

// TerminalSeen 报告是否已收到协议终态事件。
func (b *BaseDecoder) TerminalSeen() bool {
	return b.terminalSeen
}

// MarkTerminal 标记已收到协议终态事件。
func (b *BaseDecoder) MarkTerminal() {
	b.terminalSeen = true
}

// Fail 记录协议层失败原因，首个原因生效。
func (b *BaseDecoder) Fail(reason string) {
	if b.failureReason == "" && reason != "" {
		b.failureReason = reason
		b.protocolSeen = true
	}
}

// FailMissingTerminal 标记没有协议终态。这不是 fatal_cli，续跑判定会另做分类。
func (b *BaseDecoder) FailMissingTerminal(terminalName string) {
	if b.terminalSeen || b.failureReason != "" {
		return
	}
	b.missingTerminal = true
	b.missingDetail = fmt.Sprintf("%s 未输出有效的 %s 结束事件", b.DisplayName, terminalName)
}

// AddDetail 追加协议特有诊断信息，仅在判定失败时拼进错误详情。
func (b *BaseDecoder) AddDetail(detail string) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return
	}
	b.details = append(b.details, detail)
}

// Outcome 返回协议侧执行结果。
func (b *BaseDecoder) Outcome() Outcome {
	return Outcome{
		SessionID:       b.sessionID,
		InputTokens:     b.inputTokens,
		OutputTokens:    b.outputTokens,
		UsageSeen:       b.usageSeen,
		FailureReason:   b.failureReason,
		ExtraDetails:    b.details,
		ExitHint:        b.exitHint,
		MissingTerminal: b.missingTerminal,
		MissingDetail:   b.missingDetail,
		ProtocolSeen:    b.protocolSeen,
		TerminalSeen:    b.terminalSeen,
	}
}

// Message 发射一条面向用户的输出事件，空内容自动跳过。
func (b *BaseDecoder) Message(content string, emit EmitFunc) {
	b.emitContent(EventMessage, content, emit)
}

// Thinking 发射一条思考事件，空内容自动跳过。
func (b *BaseDecoder) Thinking(content string, emit EmitFunc) {
	b.emitContent(EventThinking, content, emit)
}

// ToolResult 发射最终工具结果。即使内容为空，也必须保留完成信号。
func (b *BaseDecoder) ToolResult(toolUseID, content string, emit EmitFunc) {
	b.emitToolResult(toolUseID, content, false, emit)
}

// ToolProgress 只更新工具输出，不结束执行预算。
func (b *BaseDecoder) ToolProgress(toolUseID, content string, emit EmitFunc) {
	b.emitToolResult(toolUseID, content, true, emit)
}

func (b *BaseDecoder) emitToolResult(id, content string, partial bool, emit EmitFunc) {
	emit(ExecutorEvent{Type: EventToolResult, ToolUseID: strings.TrimSpace(id),
		Content: strings.TrimSpace(content), ToolResultPartial: partial,
		SessionID: b.sessionID, Timestamp: NowMillis()})
}

// ToolCall 发射一条工具调用事件。内容统一为「工具名 参数摘要」，与前端解析约定一致。
// toolUseID 是协议侧的工具调用 ID，协议未提供时传空字符串。
func (b *BaseDecoder) ToolCall(toolUseID, name, args string, emit EmitFunc) {
	b.ToolCallKind(toolUseID, name, args, "", emit)
}

// ToolCallKind 与 ToolCall 相同，并带上协议侧工具种类。
func (b *BaseDecoder) ToolCallKind(toolUseID, name, args, kind string, emit EmitFunc) {
	name = strings.TrimSpace(name)
	args = OneLine(args)
	content := name
	if args != "" {
		if content == "" {
			content = args
		} else {
			content = content + " " + args
		}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	b.protocolSeen = true
	emit(ExecutorEvent{
		Type:      EventToolCall,
		Content:   content,
		SessionID: b.sessionID,
		ToolUseID: strings.TrimSpace(toolUseID),
		ToolKind:  kind,
		Timestamp: NowMillis(),
	})
}

// Permission 发射权限请求事件。
func (b *BaseDecoder) Permission(content string, requests []PermissionRequest, emit EmitFunc) {
	if content == "" && len(requests) == 0 {
		return
	}
	emit(ExecutorEvent{
		Type:               EventPermission,
		Content:            content,
		SessionID:          b.sessionID,
		PermissionRequests: requests,
		Timestamp:          NowMillis(),
	})
}

// Usage 发射 token 用量事件。
func (b *BaseDecoder) Usage(input, output int, emit EmitFunc) {
	emit(ExecutorEvent{
		Type:         EventUsage,
		SessionID:    b.sessionID,
		InputTokens:  input,
		OutputTokens: output,
		Timestamp:    NowMillis(),
	})
}

func (b *BaseDecoder) emitContent(eventType, content string, emit EmitFunc) {
	b.emitContentWithID(eventType, "", content, emit)
}

// emitContentWithID 与 emitContent 一致，额外携带工具调用 ID。
func (b *BaseDecoder) emitContentWithID(eventType, toolUseID, content string, emit EmitFunc) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	emit(ExecutorEvent{
		Type:      eventType,
		Content:   content,
		SessionID: b.sessionID,
		ToolUseID: strings.TrimSpace(toolUseID),
		Timestamp: NowMillis(),
	})
}

// DecodeContentBlocks 把 stream-json 内容块翻译为统一事件。
// claude / codebuddy / qoder / qwen 共用：这些 CLI 只在内容块类型的支持范围上有差异。
func DecodeContentBlocks(blocks []ContentBlock, base *BaseDecoder, emit EmitFunc) {
	for _, block := range blocks {
		switch block.Type {
		case "thinking", "reasoning":
			base.Thinking(block.Thinking, emit)
		case "text":
			base.Message(block.Text, emit)
		case "tool_use", "tool_call":
			kind := ""
			switch strings.ToLower(strings.TrimSpace(block.Name)) {
			case "bash", "shell":
				kind = ToolKindShell
			}
			base.ToolCallKind(block.ID, block.Name, FormatToolArgs(block.Input), kind, emit)
		case "tool_result":
			content := RawText(block.Content)
			if content == "" {
				content = RawText(block.Result)
			}
			base.ToolResult(block.ToolUseID, content, emit)
		}
	}
}

// FormatToolArgs 把工具参数压成单行摘要。
// 字符串参数原样返回，对象/数组返回紧凑 JSON 单行。
func FormatToolArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	text := RawText(raw)
	if text == "" {
		return ""
	}
	return OneLine(text)
}

// Invocation 描述一次 CLI 调用的命令行与 prompt 投递方式。
// 由各 CLI 自行决定 prompt 走标准输入、命令行参数还是临时文件。
type Invocation struct {
	Args       []string
	StdinText  string
	PromptFile *PromptFileSpec
	EnvVars    map[string]string // 本次调用的环境覆盖，不修改进程或全局 CLI 配置
}

// AdapterSpec 声明一个 CLI 适配器：如何构造命令行、如何解码输出。
// 新增 CLI 只需要提供这两项，进程管理与错误归并复用 StreamAdapter。
type AdapterSpec struct {
	DisplayName      string
	SupportsThinking bool
	// BuildInvocation 根据执行选项构造命令行；resumeSession 非空表示续聊既有会话。
	BuildInvocation func(opts RunOptions, resumeSession string) Invocation
	// PrepareInvocation 可执行有界的会话级配置或创建临时配置；cleanup 随进程树回收。
	PrepareInvocation func(context.Context, RunOptions, string) (Invocation, func(), error)
	// NewDecoder 构造本次执行的协议解码器。
	NewDecoder func(displayName string) Decoder
}

// StreamAdapter 是用 AdapterSpec 驱动的通用适配器实现。
type StreamAdapter struct {
	spec AdapterSpec

	mu   sync.Mutex
	proc *Process
}

// NewStreamAdapter 创建通用适配器。
func NewStreamAdapter(spec AdapterSpec) Adapter {
	return &StreamAdapter{spec: spec}
}

// NewConversation 开始新会话。
func (a *StreamAdapter) NewConversation(ctx context.Context, opts RunOptions) (<-chan ExecutorEvent, error) {
	return a.run(ctx, opts, "")
}

// ResumeConversation 续聊既有会话。
func (a *StreamAdapter) ResumeConversation(ctx context.Context, opts ResumeOptions) (<-chan ExecutorEvent, error) {
	return a.run(ctx, opts.RunOptions, opts.ExternalSessionID)
}

// Stop 终止当前进程树。
func (a *StreamAdapter) Stop() error {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return nil
	}
	return proc.Stop()
}

func (a *StreamAdapter) run(ctx context.Context, opts RunOptions, resumeSession string) (<-chan ExecutorEvent, error) {
	displayName := a.displayName()
	if opts.ThinkingLevel != "" {
		if !a.spec.SupportsThinking || !thinkingToken.MatchString(opts.ThinkingLevel) {
			return nil, ErrThinkingInvalid
		}
		for _, arg := range opts.ExtraArgs {
			flag, _, _ := strings.Cut(arg, "=")
			switch flag {
			case "--thinking", "--thinking-budget", "--variant", "--effort", "--reasoning-effort", "--model", "--auth-type", "--provider", "--session", "--resume", "--conversation", "-S", "-m":
				return nil, ErrThinkingInvalid
			}
			if strings.Contains(arg, "model_reasoning_effort") || (strings.HasPrefix(arg, "-m") && !strings.HasPrefix(arg, "--")) {
				return nil, ErrThinkingInvalid
			}
		}
	}
	var invocation Invocation
	var cleanup func()
	if a.spec.PrepareInvocation != nil {
		var err error
		invocation, cleanup, err = a.spec.PrepareInvocation(ctx, opts, resumeSession)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, err
		}
	} else {
		invocation = a.spec.BuildInvocation(opts, resumeSession)
	}
	env := opts.EnvVars
	if len(invocation.EnvVars) > 0 {
		env = make(map[string]string, len(opts.EnvVars)+len(invocation.EnvVars))
		for key, value := range opts.EnvVars {
			env[key] = value
		}
		for key, value := range invocation.EnvVars {
			env[key] = value
		}
	}
	decoder := a.spec.NewDecoder(displayName)

	proc, err := StartProcess(ctx, RunSpec{
		ExecPath:   opts.ExecPath,
		Args:       invocation.Args,
		WorkDir:    opts.WorkDir,
		EnvVars:    env,
		StdinText:  invocation.StdinText,
		PromptFile: invocation.PromptFile,
		Watchdog:   opts.Watchdog,
	})
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("启动 %s 失败: %w", displayName, err)
	}

	a.mu.Lock()
	a.proc = proc
	a.mu.Unlock()

	eventCh := make(chan ExecutorEvent, 100)
	go func() {
		defer close(eventCh)
		defer a.clearProcess(proc)
		if cleanup != nil {
			defer cleanup()
		}

		emit := func(event ExecutorEvent) {
			if event.Type == EventHeartbeat {
				proc.ObserveHeartbeat()
				return
			}
			if event.Timestamp == 0 {
				event.Timestamp = NowMillis()
			}
			proc.ObserveEvent(event)
			select {
			case eventCh <- event:
			case <-ctx.Done():
				// 消费者取消后仍读完 stdout，但不能在满事件通道上永久阻塞。
				select {
				case eventCh <- event:
				default:
				}
			}
		}

		emit(NewEvent(EventStart))
		lines := proc.Lines()
		activities := proc.Activities()
		rootExited := proc.RootExited()
		var drain <-chan time.Time
		var drainTimer *time.Timer
		startDrain := func() {
			if drainTimer == nil {
				drainTimer = time.NewTimer(proc.DrainAfter())
				drain = drainTimer.C
			}
		}
		exited := false
		terminalHandled := false
		completedEarly := false
		for {
			select {
			case event := <-activities:
				if !completedEarly {
					emit(event)
				}
			case line, ok := <-lines:
				if !ok {
					lines = nil
					if exited || completedEarly {
						goto finish
					}
					continue
				}
				if completedEarly {
					// 终态已上报，剩余输出只排空，不再产生事件。
					continue
				}
				decoder.Decode(line, emit)
				if !terminalHandled && decoderTurnEnded(decoder) {
					terminalHandled = true
					// 终态事件之后不再等 stdout EOF / 根进程退出：
					// MCP 常驻、后台命令的孙进程占着 stdout 都会让那两个条件迟迟不满足。
					// 成功终态立即展示缓冲答复，执行完成仍等待进程收尾；失败终态最多再等一个排空窗口。
					completedEarly = a.completeEarly(decoder, proc, emit)
					startDrain()
				}
			case <-rootExited:
				rootExited = nil // A closed channel stays ready; disable it while draining.
				exited = true
				if lines == nil || completedEarly {
					goto finish
				}
				startDrain()
			case <-drain:
				goto finish
			}
		}
	finish:
		if drainTimer != nil {
			drainTimer.Stop()
		}
		reapErr := proc.ReapGroup(proc.wdCfg.TermGrace)
		// Stop inherited writers first, then drain the pipe. Time spent persisting
		// events is not evidence of a stuck writer and must not discard the tail.
		drainComplete := true
		if lines != nil {
			drainComplete = proc.drainStdout(ctx, func(line string) {
				if !completedEarly {
					decoder.Decode(line, emit)
				}
			})
		}
		if reapErr != nil {
			applog.Warn("[Executor] process cleanup failed", "cli", displayName, "pid", proc.Pid(), "error", reapErr)
		}
		if reapErr != nil {
			// Keep the task occupied until the tree is empty. Do not repeatedly
			// kill it: the user can retry Stop while this bounded-rate check waits.
			emit(ExecutorEvent{Type: EventActivity, Content: ActivitySuspect,
				Activity: &ActivityInfo{Kind: ActivitySuspect, Detail: "process_cleanup"}})
			ticker := time.NewTicker(time.Second)
			for reapErr != nil && ctx.Err() == nil {
				select {
				case <-ctx.Done():
				case <-ticker.C:
					if proc.GroupEmpty() {
						reapErr = nil
					}
				}
			}
			ticker.Stop()
		}
		if !completedEarly {
			decoder.Done(emit)
		}
		result := proc.WaitFor()
		outcome := decoder.Outcome()
		class := ClassifyExit(ExitFacts{
			TerminalSeen:    outcome.TerminalSeen,
			FailureReason:   outcome.FailureReason,
			ExitHint:        outcome.ExitHint,
			MissingTerminal: outcome.MissingTerminal,
			IdleStop:        proc.IdleStopMessage(),
			StallKilled:     proc.StallKilled(),
			UserStopped:     proc.UserStopped(),
			ExitCode:        result.ExitCode,
			WaitErr:         result.WaitErr,
			ScanErr:         result.ScanErr,
			StdinErr:        result.StdinErr,
			Stderr:          result.Stderr,
			SawProtocol:     outcome.ProtocolSeen || proc.SawProtocol(),
		})
		if reapErr != nil || (!drainComplete && !decoderTurnEnded(decoder) && !proc.UserStopped()) {
			// Never auto-resume while the previous process tree may still be alive.
			// Truncated output can also hide an unconfirmed tool operation. Loose
			// terminal adapters (e.g. Kimi) do not prove a turn actually ended.
			class = ExitFatalCLI
		}
		if class == ExitCompleted {
			event := NewCompleteEvent(outcome.SessionID, outcome.InputTokens, outcome.OutputTokens)
			event.ExitClass = string(ExitCompleted)
			emit(event)
			return
		}
		reason := outcome.FailureReason
		if msg := proc.IdleStopMessage(); msg != "" {
			reason = msg
		} else if reason == "" && outcome.MissingDetail != "" {
			reason = outcome.MissingDetail
		} else if reason == "" && result.StdinErr != nil {
			reason = "写入 " + displayName + " Prompt 失败: " + result.StdinErr.Error()
		}
		emit(ExecutorEvent{
			Type:      EventError,
			Error:     FormatExecutionError(displayName, reason, result.Stderr, outcome.ExtraDetails, nil, result.WaitErr),
			SessionID: outcome.SessionID,
			ExitClass: string(class),
			Timestamp: NowMillis(),
		})
	}()

	return eventCh, nil
}

// looseTerminal 由「解析到任意事件就 MarkTerminal」的解码器实现（Kimi、Grok），
// 它们的 TerminalSeen 不代表一轮结束，只能等 EOF 后在 Done 里收尾。
type looseTerminal interface{ LooseTerminal() bool }

type terminalReporter interface{ TerminalSeen() bool }

func decoderTurnEnded(decoder Decoder) bool {
	if loose, ok := decoder.(looseTerminal); ok && loose.LooseTerminal() {
		return false
	}
	reporter, ok := decoder.(terminalReporter)
	return ok && reporter.TerminalSeen()
}

// completeEarly flushes a successful protocol result without completing its process lifetime.
func (a *StreamAdapter) completeEarly(decoder Decoder, proc *Process, emit EmitFunc) bool {
	facts := func(outcome Outcome) ExitFacts {
		return ExitFacts{
			TerminalSeen:    outcome.TerminalSeen,
			FailureReason:   outcome.FailureReason,
			ExitHint:        outcome.ExitHint,
			MissingTerminal: outcome.MissingTerminal,
			IdleStop:        proc.IdleStopMessage(),
			StallKilled:     proc.StallKilled(),
			UserStopped:     proc.UserStopped(),
			SawProtocol:     true,
		}
	}
	if ClassifyExit(facts(decoder.Outcome())) != ExitCompleted {
		return false
	}
	// Done 只做终态缺失检查和缓冲收尾（如 OpenCode 的按步 token 合计）。
	decoder.Done(emit)
	outcome := decoder.Outcome()
	if ClassifyExit(facts(outcome)) != ExitCompleted {
		return false
	}
	// Visible message buffers are now flushed. The completion event is emitted
	// only after process cleanup, so a new turn cannot overlap surviving tools.
	return true
}

func (a *StreamAdapter) displayName() string {
	name := strings.TrimSpace(a.spec.DisplayName)
	if name == "" {
		return "CLI"
	}
	return name
}

func (a *StreamAdapter) clearProcess(proc *Process) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proc != proc {
		return
	}
	a.proc = nil
}
