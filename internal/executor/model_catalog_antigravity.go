package executor

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

var antigravityCatalogSlots = make(chan struct{}, 4)

func init() { RegisterModelCatalogResolver(CLITypeAntigravity, resolveAntigravityCatalog) }

func resolveAntigravityCatalog(ctx context.Context, execPath, _, model string) (CLIModelCatalog, error) {
	catalog := unknownCatalog(nil)
	select {
	case antigravityCatalogSlots <- struct{}{}:
		defer func() { <-antigravityCatalogSlots }()
	default:
		return catalog, ErrThinkingUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	version := detectVersion(ctx, execPath)
	if version == "" {
		return catalog, ErrThinkingUnavailable
	}
	if !supportsAntigravityCatalogACP(version) {
		catalog.Meta.Reason = "unsupported_version"
		return catalog, ErrThinkingUnavailable
	}
	// Metadata only: never load a task workspace or invoke the interactive
	// sign-in, prompt or /effort paths.
	dir, err := os.MkdirTemp("", "teambuddy-agy-catalog-")
	if err != nil {
		return catalog, err
	}
	defer os.RemoveAll(dir)
	rpc, err := startCatalogRPC(ctx, execPath, dir, false, "acp", "--disable-telemetry", "--include-default-customizations=false")
	if err != nil {
		return catalog, err
	}
	defer rpc.close()
	if err := initializeCatalogACP(rpc); err != nil {
		return catalog, err
	}
	var session struct {
		SessionID string `json:"sessionId"`
		Models    *struct {
			Available []struct {
				ID string `json:"modelId"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if err := rpc.call("session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &session); err != nil {
		return catalog, err
	}
	if session.SessionID == "" || session.Models == nil {
		return catalog, ErrThinkingUnavailable
	}
	defer func() { _ = rpc.call("session/close", map[string]string{"sessionId": session.SessionID}, nil) }()
	var names []string
	for _, native := range session.Models.Available {
		if native.ID != "" && !slices.Contains(names, native.ID) {
			names = append(names, native.ID)
		}
	}
	if len(names) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	catalog = unknownCatalog(names)
	if model == "" {
		return catalog, nil
	}
	index := slices.Index(names, model)
	if index < 0 {
		return catalog, ErrThinkingUnavailable
	}
	var selected acpCatalogSession
	if err := rpc.call("session/set_config_option", map[string]string{
		"sessionId": session.SessionID, "configId": "model", "value": model,
	}, &selected); err != nil {
		return catalog, err
	}
	var currentModel string
	thinking := ThinkingFromLevels("effort", nil, "")
	thinking.Source = "antigravity_acp"
	for _, item := range selected.Options {
		switch item.ID {
		case "model":
			currentModel = item.Current
		case "thought_level":
			thinking = thinkingFromACP(item, "effort", "antigravity_acp")
			if thinking.Status != "supported" || thinking.DefaultLevel == "" {
				return catalog, ErrThinkingUnavailable
			}
		}
	}
	// agy reports the model family in the menu and the variant in thought_level.
	// Verify the exact requested model instead of accepting another model's options.
	if currentModel == "" || (currentModel != model &&
		(thinking.DefaultLevel == "" || currentModel+"-"+thinking.DefaultLevel != model)) {
		return catalog, ErrThinkingUnavailable
	}
	catalog.Options[index].Thinking = thinking
	return catalog, nil
}

func supportsAntigravityCatalogACP(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	var numbers [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || strconv.Itoa(n) != part || n < 0 {
			return false
		}
		numbers[i] = n
	}
	return slices.Compare(numbers[:], []int{1, 5, 17}) >= 0
}
