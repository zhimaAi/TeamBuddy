package executor

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PrepareCursorConfig snapshots the native configuration into a private process
// directory: Cursor may normalize/persist --model during startup, even in ACP.
// Never copy model selection back to the user's global configuration.
func PrepareCursorConfig(workDir string, env map[string]string) (map[string]string, func(), error) {
	lookup := func(key string) string {
		if value, exists := env[key]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(os.Getenv(key))
	}
	directory := lookup("CURSOR_CONFIG_DIR")
	if directory == "" {
		if xdg := lookup("XDG_CONFIG_HOME"); xdg != "" {
			directory = filepath.Join(xdg, "cursor")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, nil, err
			}
			directory = filepath.Join(home, ".cursor")
		}
	}
	if !filepath.IsAbs(directory) && workDir != "" {
		directory = filepath.Join(workDir, directory)
	}
	file, err := os.Open(filepath.Join(directory, "cli-config.json"))
	var raw []byte
	if err == nil {
		raw, err = io.ReadAll(io.LimitReader(file, maxCLIProbeOutput+1))
		file.Close()
		if err != nil || len(raw) > maxCLIProbeOutput {
			return nil, nil, ErrThinkingUnavailable
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, ErrThinkingUnavailable
	}
	path, err := os.MkdirTemp("", "teambuddy-cursor-config-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(path) }
	if len(raw) > 0 {
		if err = os.WriteFile(filepath.Join(path, "cli-config.json"), raw, 0600); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	return map[string]string{"CURSOR_CONFIG_DIR": path}, cleanup, nil
}
