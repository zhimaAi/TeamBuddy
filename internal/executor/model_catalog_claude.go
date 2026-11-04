package executor

import (
	"context"
	"encoding/json"
	"strings"
)

func init() { RegisterModelCatalogResolver(CLITypeClaude, resolveClaudeCatalog) }

func resolveClaudeCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	names := resolveClaudeModels(ctx, execPath)
	output, err := probeStreamJSONMetadata(ctx, execPath, workDir, "{\"type\":\"control_request\",\"request_id\":\"init\",\"request\":{\"subtype\":\"initialize\",\"hooks\":{}}}\n", "--verbose")
	if err != nil {
		return unknownCatalog(names), err
	}
	catalog, err := parseClaudeCatalog(output)
	return appendCatalogNames(catalog, names), err
}

func parseClaudeCatalog(output []byte) (CLIModelCatalog, error) {
	for _, line := range strings.Split(string(output), "\n") {
		var frame struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Response  struct {
					Models []struct {
						Value          string   `json:"value"`
						ResolvedModel  string   `json:"resolvedModel"`
						SupportsEffort *bool    `json:"supportsEffort"`
						Levels         []string `json:"supportedEffortLevels"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Type != "control_response" || frame.Response.RequestID != "init" || frame.Response.Subtype != "success" {
			continue
		}
		catalog := unknownCatalog(nil)
		for _, model := range frame.Response.Response.Models {
			thinking := ModelThinking{Status: "unknown", Source: "claude_initialize", State: "unknown"}
			if model.SupportsEffort != nil && (!*model.SupportsEffort || model.Levels != nil) {
				levels := model.Levels
				if !*model.SupportsEffort {
					levels = nil
				}
				thinking = ThinkingFromLevels("effort", levels, "")
				if thinking.Source != "client_token_limit" {
					thinking.Source = "claude_initialize"
				}
			}
			for _, id := range []string{model.Value, model.ResolvedModel} {
				if id == "" {
					continue
				}
				exists := false
				for _, option := range catalog.Options {
					if option.ID == id {
						exists = true
						break
					}
				}
				if !exists {
					catalog.Options = append(catalog.Options, CLIModelOption{ID: id, Thinking: thinking})
				}
			}
		}
		if len(catalog.Options) > 0 {
			return catalog, nil
		}
	}
	return unknownCatalog(nil), ErrThinkingUnavailable
}
