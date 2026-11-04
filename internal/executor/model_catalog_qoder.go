package executor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

func init() {
	RegisterModelCatalogResolver(CLITypeQoder, resolveQoderCatalog)
	RegisterModelCatalogResolver(CLITypeQoderCN, resolveQoderCatalog)
}

func resolveQoderCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	// 两个版本各自查询当前账号；bare 避免自动资源，控制帧不触发模型回合。
	output, err := probeStreamJSONMetadata(ctx, execPath, workDir, "{\"type\":\"control_request\",\"request_id\":\"init\",\"request\":{\"subtype\":\"initialize\",\"hooks\":{}}}\n{\"type\":\"control_request\",\"request_id\":\"models\",\"request\":{\"subtype\":\"get_models\",\"fetchStrategy\":\"cache\"}}\n", "--bare")
	if err != nil {
		return unknownCatalog(resolveQoderModels(ctx, execPath)), err
	}
	catalog, err := parseQoderCatalog(output)
	// 既有 BYOK 名称仅保留供选择/回显；bare 未核验的本地提供方不借用云端能力。
	return appendCatalogNames(catalog, qoderSettingsModelIDs()), err
}

func parseQoderCatalog(output []byte) (CLIModelCatalog, error) {
	for _, line := range strings.Split(string(output), "\n") {
		var frame struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Response  struct {
					Models []struct {
						Value            string   `json:"value"`
						ModelID          string   `json:"modelId"`
						DisplayName      string   `json:"displayName"`
						Source           string   `json:"source"`
						Enabled          *bool    `json:"isEnabled"`
						Efforts          []string `json:"efforts"`
						DefaultEffort    string   `json:"defaultEffort"`
						SupportsDisabled bool     `json:"supportsDisabled"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Type != "control_response" || frame.Response.RequestID != "models" || frame.Response.Subtype != "success" {
			continue
		}
		catalog := unknownCatalog(nil)
		seen := map[string]bool{}
		for _, model := range frame.Response.Response.Models {
			if model.Enabled != nil && !*model.Enabled {
				continue
			}
			id := model.ModelID
			if id == "" {
				id = model.Value
			}
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			option := CLIModelOption{ID: id, Thinking: ModelThinking{Status: "unknown", State: "unknown", Source: "qoder_get_models"}}
			// --list-models 旧列表使用系统模型展示名；保留旧值，并把目录中的原生 ID 作为别名。
			if model.Source != "custom" && model.DisplayName != "" {
				option.ID = model.DisplayName
			}
			for _, alias := range []string{id, model.Value} {
				if alias != "" && alias != option.ID && !slices.Contains(option.Aliases, alias) {
					option.Aliases = append(option.Aliases, alias)
				}
			}
			if model.Efforts != nil || model.SupportsDisabled {
				levels := make([]string, 0, len(model.Efforts)+1)
				for _, level := range model.Efforts {
					// ultracode 带额外工作流语义，不把它作为普通强度开放。
					if !strings.EqualFold(level, "ultracode") {
						levels = append(levels, level)
					}
				}
				if model.SupportsDisabled {
					levels = append(levels, "none")
				}
				option.Thinking = ThinkingFromLevels("effort", levels, model.DefaultEffort)
				if option.Thinking.Source != "client_token_limit" {
					option.Thinking.Source = "qoder_get_models"
				}
			}
			catalog.Options = append(catalog.Options, option)
		}
		if len(catalog.Options) > 0 {
			return catalog, nil
		}
	}
	return unknownCatalog(nil), ErrThinkingUnavailable
}
