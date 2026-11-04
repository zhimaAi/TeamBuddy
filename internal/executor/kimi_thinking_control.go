package executor

import (
	"context"
	"os"
	"time"
)

// PrepareKimiThinkingSession configures only the target native session, then hands it
// back to the existing headless adapter. It never writes global Kimi preferences.
// A forced-effort environment variable alone cannot turn an already-off session on.
func PrepareKimiThinkingSession(ctx context.Context, opts RunOptions, sessionID string) (string, error) {
	if !thinkingToken.MatchString(opts.ThinkingLevel) {
		return "", ErrThinkingInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	workDir := opts.WorkDir
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	rpc, err := startCatalogRPCWithEnv(ctx, opts.ExecPath, workDir, false, opts.EnvVars, "acp")
	if err != nil {
		return "", err
	}
	defer rpc.close()
	if err = initializeCatalogACP(rpc); err != nil {
		return "", err
	}
	var session acpCatalogSession
	if sessionID == "" {
		err = rpc.call("session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}}, &session)
		sessionID = session.SessionID
	} else {
		if !thinkingToken.MatchString(sessionID) {
			return "", ErrThinkingInvalid
		}
		// resume avoids replaying the conversation into a metadata/control client.
		err = rpc.call("session/resume", map[string]any{"sessionId": sessionID, "cwd": workDir, "mcpServers": []any{}}, &session)
	}
	if err != nil {
		return "", err
	}
	if !thinkingToken.MatchString(sessionID) {
		return "", ErrThinkingUnavailable
	}
	closed := false
	defer func() {
		if !closed {
			_ = rpc.call("session/close", map[string]string{"sessionId": sessionID}, nil)
		}
	}()
	for _, option := range session.Options {
		if option.Category == "model" && opts.ModelProfile != "" && option.Current != opts.ModelProfile {
			if err = rpc.call("session/set_config_option", map[string]string{"sessionId": sessionID, "configId": option.ID, "value": opts.ModelProfile}, &session); err != nil {
				return "", ErrThinkingInvalid
			}
			break
		}
	}
	var control string
	for _, option := range session.Options {
		if option.Category == "thought_level" {
			for _, item := range option.Options {
				if item.Value == opts.ThinkingLevel {
					control = option.ID
					break
				}
			}
		}
	}
	if control == "" {
		return "", ErrThinkingInvalid
	}
	if err = rpc.call("session/set_config_option", map[string]string{"sessionId": sessionID, "configId": control, "value": opts.ThinkingLevel}, &session); err != nil {
		return "", ErrThinkingInvalid
	}
	for _, option := range session.Options {
		if option.ID == control && option.Current == opts.ThinkingLevel {
			if err = rpc.call("session/close", map[string]string{"sessionId": sessionID}, nil); err != nil {
				return "", ErrThinkingUnavailable
			}
			closed = true
			return sessionID, nil
		}
	}
	return "", ErrThinkingUnavailable
}
