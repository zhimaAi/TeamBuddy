// Package qoder 提供 Qoder CLI 的执行适配。
// 该 CLI 与 Claude 同属 stream-json 协议族，解码器完全复用公共实现，
// 本包只声明命令行差异（--yolo 免授权、-p 从 stdin 读取 Prompt）。
package qoder

import (
	"goteams-client/internal/executor"
)

const displayName = "Qoder CLI"
const fullPermissionArg = "--yolo"

// NewAdapter 创建 Qoder CLI 适配器。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:      displayName,
		SupportsThinking: true,
		BuildInvocation:  buildInvocation,
		NewDecoder:       executor.NewStreamJSONDecoder,
	})
}

// buildInvocation 构造 qodercli 命令行，Prompt 走 stdin。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{"-p"}
	if resumeSession != "" {
		// 必须用 --resume：--session-id 表示“用该 ID 新建会话”，
		// 对已存在的 ID 会以退出码 42 失败。
		args = append(args, executor.SessionArg(false, "--resume", resumeSession)...)
	} else if opts.PresetSessionID != "" {
		args = append(args, executor.SessionArg(false, "--session-id", opts.PresetSessionID)...)
	}
	args = append(args, fullPermissionArg, "--output-format", "stream-json")
	if opts.ModelProfile != "" {
		args = append(args, "--model", opts.ModelProfile)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--reasoning-effort", opts.ThinkingLevel)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}
