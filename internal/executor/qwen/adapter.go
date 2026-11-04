// Package qwen 提供 Qwen Code CLI 的执行适配。
// 该 CLI 与 Claude 同属 stream-json 协议族，解码器完全复用公共实现，
// 本包只声明命令行差异（--yolo 免授权、-o stream-json、-p 从 stdin 读取 Prompt）。
package qwen

import (
	"goteams-client/internal/executor"
)

const displayName = "Qwen Code"
const fullPermissionArg = "--yolo"

// NewAdapter 创建 Qwen Code 适配器。
func NewAdapter() executor.Adapter {
	return executor.NewStreamAdapter(executor.AdapterSpec{
		DisplayName:       displayName,
		SupportsThinking:  true,
		PrepareInvocation: prepareThinkingInvocation,
		BuildInvocation:   buildInvocation,
		NewDecoder:        executor.NewStreamJSONDecoder,
	})
}

// buildInvocation 构造 qwen 命令行，Prompt 走 stdin。
func buildInvocation(opts executor.RunOptions, resumeSession string) executor.Invocation {
	args := []string{"-p"}
	if resumeSession != "" {
		args = append(args, "--resume", resumeSession)
	}
	args = append(args, fullPermissionArg, "-o", "stream-json")
	if opts.ModelProfile != "" {
		args = append(args, "-m", opts.ModelProfile)
	}
	args = append(args, opts.ExtraArgs...)
	return executor.Invocation{Args: args, StdinText: opts.Prompt}
}
