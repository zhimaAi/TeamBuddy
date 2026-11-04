package executor

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func init() {
	RegisterModelCatalogResolver(CLITypeCursor, resolveCursorCatalog)
	RegisterModelCatalogResolver(CLITypeCopilot, resolveCopilotCatalog)
	RegisterModelCatalogResolver(CLITypeKimi, resolveKimiCatalog)
	RegisterModelCatalogResolver(CLITypeQwen, resolveQwenCatalog)
}

func resolveCursorCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env, cleanup, err := PrepareCursorConfig(workDir, nil)
	if err != nil {
		return unknownCatalog(nil), err
	}
	defer cleanup()
	rpc, err := startCatalogRPCWithEnv(ctx, execPath, workDir, false, env, "acp")
	if err != nil {
		return unknownCatalog(nil), err
	}
	defer rpc.close()
	if err = initializeCatalogACP(rpc); err != nil {
		return unknownCatalog(nil), err
	}
	// Cursor's directory service is initialized by session/new on current versions.
	// No session/prompt or preference-setting RPC is sent.
	var session acpCatalogSession
	if err = rpc.call("session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}}, &session); err != nil {
		return unknownCatalog(nil), err
	}
	var directory struct {
		Models []struct {
			Model   string              `json:"value"`
			Options *[]acpCatalogOption `json:"configOptions"`
		} `json:"models"`
	}
	if err = rpc.call("cursor/list_available_models", map[string]any{}, &directory); err != nil {
		return unknownCatalog(nil), err
	}
	catalog := unknownCatalog(nil)
	for _, model := range directory.Models {
		if model.Model == "" {
			continue
		}
		option := unknownCatalog([]string{model.Model}).Options[0]
		if model.Options != nil {
			// Only an explicitly returned parameter list proves a fixed model.
			option.Thinking = ThinkingFromLevels("effort", nil, "")
			option.Thinking.Source = "cursor_parameterized_catalog"
			for _, config := range *model.Options {
				if config.Category == "thought_level" && thinkingToken.MatchString(config.ID) {
					option.Thinking = thinkingFromACP(config, "effort", "cursor_parameterized_catalog")
					break
				}
			}
		}
		if model.Model == "default" {
			option.Aliases = []string{"auto"}
		}
		catalog.Options = append(catalog.Options, option)
	}
	if len(catalog.Options) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	return catalog, nil
}

func resolveCopilotCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	rpc, err := startCatalogRPC(ctx, execPath, workDir, true, "--headless", "--stdio", "--no-auto-update", "--disable-builtin-mcps", "--no-custom-instructions")
	if err != nil {
		return unknownCatalog(nil), err
	}
	defer rpc.close()
	// --no-auto-login would disable the user's existing login in this protocol.
	if err = rpc.call("connect", map[string]any{"supportedTaskKinds": []string{"agent", "client", "shell"}}, nil); err != nil {
		return unknownCatalog(nil), err
	}
	var result json.RawMessage
	if err = rpc.call("models.list", map[string]any{}, &result); err != nil {
		return unknownCatalog(nil), err
	}
	return parseCopilotCatalog(result)
}

func parseCopilotCatalog(raw []byte) (CLIModelCatalog, error) {
	var directory struct {
		Models []struct {
			ID           string `json:"id"`
			Capabilities struct {
				Supports struct {
					ReasoningEffort *bool `json:"reasoningEffort"`
				} `json:"supports"`
			} `json:"capabilities"`
			Levels  []string `json:"supportedReasoningEfforts"`
			Default string   `json:"defaultReasoningEffort"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &directory); err != nil {
		return unknownCatalog(nil), err
	}
	catalog := unknownCatalog(nil)
	for _, model := range directory.Models {
		if model.ID == "" {
			continue
		}
		option := unknownCatalog([]string{model.ID}).Options[0]
		support := model.Capabilities.Supports.ReasoningEffort
		if support != nil && (!*support || len(model.Levels) > 0) {
			values := model.Levels
			if !*support {
				values = nil
			}
			option.Thinking = ThinkingFromLevels("effort", values, model.Default)
			option.Thinking.Source = "copilot_models_list"
		}
		catalog.Options = append(catalog.Options, option)
	}
	if len(catalog.Options) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	return catalog, nil
}

func resolveKimiCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	return resolveGenericACPCatalog(ctx, execPath, workDir, CLITypeKimi)
}

func resolveQwenCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	return resolveGenericACPCatalog(ctx, execPath, workDir, CLITypeQwen)
}

func resolveGenericACPCatalog(ctx context.Context, execPath, workDir, cliType string) (CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"--acp", "--safe-mode"}
	if cliType == CLITypeKimi {
		args = []string{"acp"}
	}
	rpc, err := startCatalogRPC(ctx, execPath, workDir, false, args...)
	if err != nil {
		return unknownCatalog(nil), err
	}
	defer rpc.close()
	if err = initializeCatalogACP(rpc); err != nil {
		return unknownCatalog(nil), err
	}
	var session acpCatalogSession
	if err = rpc.call("session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}}, &session); err != nil {
		return unknownCatalog(nil), err
	}
	catalog := genericACPCatalog(session, cliType)
	if cliType == CLITypeKimi && session.SessionID != "" {
		_ = rpc.call("session/close", map[string]string{"sessionId": session.SessionID}, nil)
	}
	if len(catalog.Options) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	return catalog, nil
}

// These are CLI-processed menus, deliberately not advertised as exact provider tiers.
func genericACPCatalog(session acpCatalogSession, cliType string) CLIModelCatalog {
	var names []string
	var currentModel string
	var thinking ModelThinking
	for _, config := range session.Options {
		if config.Category == "model" {
			currentModel = config.Current
			for _, item := range config.Options {
				names = append(names, item.Value)
			}
		}
		if config.Category == "thought_level" {
			kind := "cli"
			if cliType == CLITypeKimi && len(config.Options) <= 2 {
				kind = "toggle"
			}
			thinking = thinkingFromACP(config, kind, cliType+"_acp_cli_menu")
		}
	}
	catalog := unknownCatalog(names)
	for i := range catalog.Options {
		if thinking.Status != "" {
			catalog.Options[i].Thinking = thinking
		}
	}
	if cliType == CLITypeQwen {
		// ACP qualifies IDs with auth type; older saved selections use a bare ID.
		// Only the current auth route and an unambiguous ID can safely share it.
		_, currentAuth := qwenACPModelParts(currentModel)
		counts := map[string]int{}
		for _, id := range names {
			base, auth := qwenACPModelParts(id)
			if auth != "" {
				counts[base]++
			}
		}
		for i := range catalog.Options {
			base, auth := qwenACPModelParts(catalog.Options[i].ID)
			if auth != "" && auth == currentAuth && counts[base] == 1 {
				catalog.Options[i].Aliases = []string{base}
			}
		}
	}
	return catalog
}

func qwenACPModelParts(id string) (string, string) {
	index := strings.LastIndex(id, "(")
	if index <= 0 || !strings.HasSuffix(id, ")") {
		return id, ""
	}
	auth := id[index+1 : len(id)-1]
	switch auth {
	case "openai", "openai-responses", "anthropic", "qwen-oauth", "gemini", "vertex-ai":
		return id[:index], auth
	}
	return id, ""
}
