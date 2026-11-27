package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
)

// HookInspection contains only discovery metadata, never the user's config or
// credentials. The desktop host compares these definitions with its manifest.
type HookInspection struct {
	Hooks           []hookDefinition `json:"hooks"`
	CWD             string           `json:"cwd"`
	DiscoveryOK     bool             `json:"discovery_ok"`
	ProjectDisabled bool             `json:"project_disabled"`
	HooksDisabled   bool             `json:"hooks_disabled"`
}

type hookDefinition struct {
	Event       string `json:"eventName"`
	Enabled     bool   `json:"enabled"`
	TrustStatus string `json:"trustStatus"`
	CommandHash string `json:"commandHash"`
}

// Use the same lexical path used to start the App, not another symlink that
// points at the same inode: Codex can assign different trust keys to aliases.
func sameHookPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

// InspectHooks reuses the bounded, cancellable App Server transport. It does not
// start a chat, invoke a model, run hooks, or change native trust settings.
func InspectHooks(ctx context.Context, executable, cwd string, userSource ...string) (HookInspection, error) {
	session, err := startAppServer(ctx, executable, cwd)
	if err != nil {
		return HookInspection{}, err
	}
	defer session.Close()
	var raw json.RawMessage
	if err := session.call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": true}, &raw); err != nil {
		return HookInspection{}, err
	}
	var config struct {
		Config struct {
			Features map[string]json.RawMessage `json:"features"`
		} `json:"config"`
		Layers []struct {
			Name struct {
				Type   string `json:"type"`
				Folder string `json:"dotCodexFolder"`
			} `json:"name"`
			DisabledReason *string `json:"disabledReason"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return HookInspection{}, err
	}
	result := HookInspection{Hooks: []hookDefinition{}}
	for _, layer := range config.Layers {
		if layer.Name.Type == "project" && sameHookPath(layer.Name.Folder, filepath.Join(cwd, ".codex")) && layer.DisabledReason != nil && *layer.DisabledReason != "" {
			result.ProjectDisabled = true
		}
	}
	if raw, ok := config.Config.Features["hooks"]; ok {
		var enabled bool
		if json.Unmarshal(raw, &enabled) == nil {
			result.HooksDisabled = !enabled
		}
	}
	if _, ok := config.Config.Features["hooks"]; !ok {
		if raw, ok := config.Config.Features["codex_hooks"]; ok {
			var enabled bool
			if json.Unmarshal(raw, &enabled) == nil {
				result.HooksDisabled = !enabled
			}
		}
	}
	result.CWD = cwd
	file := filepath.Join(cwd, ".codex", "hooks.json")
	if len(userSource) > 0 && userSource[0] != "" {
		file = userSource[0]
		result.ProjectDisabled = false
	}
	if result.ProjectDisabled || result.HooksDisabled {
		return result, nil
	}
	var listed struct {
		Data []struct {
			CWD    string            `json:"cwd"`
			Errors []json.RawMessage `json:"errors"`
			Hooks  []struct {
				Event       string `json:"eventName"`
				Enabled     bool   `json:"enabled"`
				TrustStatus string `json:"trustStatus"`
				Type        string `json:"handlerType"`
				SourcePath  string `json:"sourcePath"`
				Command     string `json:"command"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := session.call(ctx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &raw); err != nil {
		return HookInspection{}, err
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return HookInspection{}, err
	}
	for _, row := range listed.Data {
		if !sameHookPath(row.CWD, cwd) || len(row.Errors) > 0 {
			continue
		}
		result.DiscoveryOK = true
		for _, h := range row.Hooks {
			if h.Type != "command" || !sameHookPath(h.SourcePath, file) {
				continue
			}
			// Other hooks can contain secrets in their command line. Never pass
			// raw commands, the user's full config, or discovery errors to stdout.
			hash := sha256.Sum256([]byte(h.Command))
			result.Hooks = append(result.Hooks, hookDefinition{Event: h.Event, Enabled: h.Enabled, TrustStatus: h.TrustStatus, CommandHash: hex.EncodeToString(hash[:])})
		}
	}
	return result, nil
}
