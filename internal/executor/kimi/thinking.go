package kimi

import (
	"context"

	"goteams-client/internal/executor"
)

func prepareThinkingInvocation(ctx context.Context, opts executor.RunOptions, resume string) (executor.Invocation, func(), error) {
	if opts.ThinkingLevel == "" {
		return buildInvocation(opts, resume), nil, nil
	}
	sessionID, err := executor.PrepareKimiThinkingSession(ctx, opts, resume)
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	invocation := buildInvocation(opts, sessionID)
	// Prevent an inherited forced-effort variable from overriding the session selection.
	invocation.EnvVars = map[string]string{"KIMI_MODEL_THINKING_EFFORT": opts.ThinkingLevel}
	return invocation, nil, nil
}
