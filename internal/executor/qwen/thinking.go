package qwen

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"goteams-client/internal/executor"
)

// Qwen's /effort writes user preferences; a private process-only system layer
// supplies the same native model.reasoningEffort without touching the user file.
func prepareThinkingInvocation(_ context.Context, opts executor.RunOptions, resume string) (executor.Invocation, func(), error) {
	invocation := buildInvocation(opts, resume)
	if opts.ThinkingLevel == "" {
		return invocation, nil, nil
	}
	lookup := func(key string) string {
		if value, exists := opts.EnvVars[key]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(os.Getenv(key))
	}
	originalPath := lookup("QWEN_CODE_SYSTEM_SETTINGS_PATH")
	if originalPath == "" {
		switch runtime.GOOS {
		case "darwin":
			originalPath = "/Library/Application Support/QwenCode/settings.json"
		case "windows":
			originalPath = `C:\ProgramData\qwen-code\settings.json`
		default:
			originalPath = "/etc/qwen-code/settings.json"
		}
	}
	if !filepath.IsAbs(originalPath) && opts.WorkDir != "" {
		originalPath = filepath.Join(opts.WorkDir, originalPath)
	}
	defaultsPath := lookup("QWEN_CODE_SYSTEM_DEFAULTS_PATH")
	if defaultsPath == "" {
		defaultsPath = filepath.Join(filepath.Dir(originalPath), "system-defaults.json")
	}
	policy := map[string]json.RawMessage{}
	file, err := os.Open(originalPath)
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		file.Close()
		// Do not discard unreadable/JSONC policies or invent a lossy JSONC parser.
		if readErr != nil || len(raw) > 4<<20 || json.Unmarshal(raw, &policy) != nil || policy == nil {
			return executor.Invocation{}, nil, executor.ErrThinkingUnavailable
		}
	} else if !os.IsNotExist(err) {
		return executor.Invocation{}, nil, executor.ErrThinkingUnavailable
	}
	model := map[string]json.RawMessage{}
	if raw, exists := policy["model"]; exists {
		if json.Unmarshal(raw, &model) != nil || model == nil {
			return executor.Invocation{}, nil, executor.ErrThinkingUnavailable
		}
	}
	if raw, managed := model["reasoningEffort"]; managed {
		var value string
		if json.Unmarshal(raw, &value) != nil || value != opts.ThinkingLevel {
			return executor.Invocation{}, nil, executor.ErrThinkingInvalid
		}
	}
	model["reasoningEffort"], _ = json.Marshal(opts.ThinkingLevel)
	policy["model"], _ = json.Marshal(model)
	raw, err := json.Marshal(policy)
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	file, err = os.CreateTemp("", "teambuddy-qwen-thinking-*.json")
	if err != nil {
		return executor.Invocation{}, nil, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err = file.Write(raw); err != nil {
		file.Close()
		cleanup()
		return executor.Invocation{}, nil, err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return executor.Invocation{}, nil, err
	}
	invocation.EnvVars = map[string]string{
		"QWEN_CODE_SYSTEM_SETTINGS_PATH": path,
		// Overriding the system path must not relocate the original defaults layer.
		"QWEN_CODE_SYSTEM_DEFAULTS_PATH": defaultsPath,
	}
	return invocation, cleanup, nil
}
