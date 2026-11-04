package claude

import (
	"os"
	"strings"

	"goteams-client/internal/executor"
)

const fullPermissionArg = "--dangerously-skip-permissions"
const defaultDisplayName = "Claude Code"

// NewAdapter 创建 Claude Code 适配器。
func NewAdapter() executor.Adapter {
	return NewStreamJSONAdapter(defaultDisplayName, true)
}

// NewStreamJSONAdapter 创建 stream-json 协议族适配器。
// codebuddy 与本 CLI 协议一致，直接复用本函数，只需传入自己的显示名。
func NewStreamJSONAdapter(displayName string, supportsThinking bool) executor.Adapter {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = defaultDisplayName
	}
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:      displayName,
		SupportsThinking: supportsThinking,
		BuildInvocation: func(opts executor.RunOptions, resumeSession string) executor.Invocation {
			// Prompt 必须走 stdin：Windows 下 claude 通常由 claude.cmd 包装，
			// 多行 Prompt 放进命令行参数会被 cmd.exe 按换行与特殊字符重新解释。
			// 等号形式只用于已核实的 Claude Code。CodeBuddy 复用这个适配器，仍用空格形式。
			equals := displayName == defaultDisplayName
			args := []string{"-p"}
			if resumeSession != "" {
				args = append(args, executor.SessionArg(equals, "--resume", resumeSession)...)
			} else if opts.PresetSessionID != "" {
				args = append(args, executor.SessionArg(equals, "--session-id", opts.PresetSessionID)...)
			}
			args = append(args, fullPermissionArg, "--verbose", "--output-format", "stream-json")
			if os.Getenv("GOTEAMS_CLAUDE_PARTIAL_HEARTBEAT") == "1" {
				args = append(args, "--include-partial-messages")
			}
			if opts.ModelProfile != "" {
				args = append(args, "--model", opts.ModelProfile)
			}
			if opts.ThinkingLevel != "" {
				args = append(args, "--effort", opts.ThinkingLevel)
			}
			args = append(args, opts.ExtraArgs...)
			return executor.Invocation{Args: args, StdinText: opts.Prompt}
		},
		NewDecoder: executor.NewStreamJSONDecoder,
	})
}
