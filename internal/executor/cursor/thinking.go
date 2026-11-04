package cursor

import (
	"context"
	"strings"

	"goteams-client/internal/executor"
)

func prepareThinkingInvocation(ctx context.Context, opts executor.RunOptions, resume string) (executor.Invocation, func(), error) {
	if opts.ThinkingLevel == "" {
		return buildInvocation(opts, resume), nil, nil
	}
	thinking, err := executor.ResolveModelThinking(ctx, executor.CLITypeCursor, opts.ModelProfile, opts.WorkDir, opts.ThinkingLevel)
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	if thinking.Parameter == "" {
		return executor.Invocation{}, nil, executor.ErrThinkingUnavailable
	}
	model, err := modelWithThinking(opts.ModelProfile, thinking.Parameter, opts.ThinkingLevel)
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	opts.ModelProfile = model
	invocation := buildInvocation(opts, resume)
	env, cleanup, err := executor.PrepareCursorConfig(opts.WorkDir, opts.EnvVars)
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	invocation.EnvVars = env
	return invocation, cleanup, nil
}

func modelWithThinking(selector, parameter, level string) (string, error) {
	model, parameters, err := executor.SplitCLIModelSelector(selector)
	if err != nil {
		return "", err
	}
	replaced := false
	for i, pair := range parameters {
		key, _, _ := strings.Cut(pair, "=")
		if key == parameter {
			parameters[i], replaced = key+"="+level, true
		}
	}
	if !replaced {
		parameters = append(parameters, parameter+"="+level)
	}
	return model + "[" + strings.Join(parameters, ",") + "]", nil
}
