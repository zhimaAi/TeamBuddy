package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
)

var (
	ErrThinkingInvalid     = errors.New("cli_thinking_invalid")
	ErrThinkingUnavailable = errors.New("cli_thinking_unavailable")
	thinkingToken          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ThinkingLevelInput 区分局部更新缺省与显式清空；null 不是合法选择。
type ThinkingLevelInput struct {
	Value   string
	Present bool
}

func (v *ThinkingLevelInput) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrThinkingInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ErrThinkingInvalid
	}
	if value != "" && !thinkingToken.MatchString(value) {
		return ErrThinkingInvalid
	}
	v.Value, v.Present = value, true
	return nil
}

func (v ThinkingLevelInput) MarshalJSON() ([]byte, error) { return json.Marshal(v.Value) }

// ResolveThinkingLevel 不跨 CLI 或模型继承强度，空值仅移除客户端覆盖。
func ResolveThinkingLevel(input ThinkingLevelInput, cli, model, previousCLI, previousModel, previousLevel string) string {
	if input.Present {
		return input.Value
	}
	if cli == previousCLI && model == previousModel {
		return previousLevel
	}
	return ""
}

type ThinkingOption struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type ModelThinking struct {
	Status       string           `json:"status"`
	Kind         string           `json:"kind,omitempty"`
	Parameter    string           `json:"parameter,omitempty"` // CLI 原生控制名，不是模型服务端参数声明
	Levels       []ThinkingOption `json:"levels,omitempty"`
	DefaultLevel string           `json:"default_level,omitempty"`
	Source       string           `json:"source"`
	State        string           `json:"state"`
	UpdatedAt    time.Time        `json:"updated_at,omitempty"`
}

type CLIModelOption struct {
	ID       string        `json:"id"`
	Aliases  []string      `json:"aliases,omitempty"`
	Thinking ModelThinking `json:"thinking"`
}

type CatalogMeta struct {
	State      string    `json:"state"`
	CLIVersion string    `json:"cli_version,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// CLIModelCatalog 是目录唯一快照；旧 models 字段始终由选项投影。
type CLIModelCatalog struct {
	Options []CLIModelOption `json:"model_options"`
	Meta    CatalogMeta      `json:"catalog_meta"`
}

func (c CLIModelCatalog) Models() []string {
	models := make([]string, 0, len(c.Options))
	for _, option := range c.Options {
		models = append(models, option.ID)
	}
	return models
}

func (c CLIModelCatalog) MarshalJSON() ([]byte, error) {
	type catalog CLIModelCatalog
	return json.Marshal(struct {
		catalog
		Models []string `json:"models"`
	}{catalog(c), c.Models()})
}

func unknownCatalog(models []string) CLIModelCatalog {
	catalog := CLIModelCatalog{Meta: CatalogMeta{State: "unknown"}, Options: []CLIModelOption{}}
	for _, id := range models {
		catalog.Options = append(catalog.Options, CLIModelOption{ID: id, Thinking: ModelThinking{Status: "unknown", Source: "unavailable", State: "unknown"}})
	}
	return catalog
}

func appendCatalogNames(catalog CLIModelCatalog, names []string) CLIModelCatalog {
	seen := make(map[string]bool, len(catalog.Options))
	for _, option := range catalog.Options {
		if option.ID != "" {
			seen[option.ID] = true
		}
		for _, alias := range option.Aliases {
			seen[alias] = true
		}
	}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		catalog.Options = append(catalog.Options, unknownCatalog([]string{name}).Options[0])
	}
	return catalog
}

func cloneCatalog(c CLIModelCatalog) CLIModelCatalog {
	c.Options = append([]CLIModelOption{}, c.Options...)
	for i := range c.Options {
		c.Options[i].Aliases = append([]string{}, c.Options[i].Aliases...)
		c.Options[i].Thinking.Levels = append([]ThinkingOption{}, c.Options[i].Thinking.Levels...)
	}
	return c
}

func catalogView(c CLIModelCatalog) CLIModelCatalog {
	c = cloneCatalog(c)
	if !c.Meta.UpdatedAt.IsZero() && time.Since(c.Meta.UpdatedAt) >= cliDiscoveryRefreshInterval {
		c.Meta.State = "stale"
	}
	for i := range c.Options {
		t := &c.Options[i].Thinking
		if t.State == "fresh" && time.Since(t.UpdatedAt) >= cliDiscoveryRefreshInterval {
			t.State = "stale"
		}
	}
	return c
}

func catalogOption(catalog CLIModelCatalog, cliType, model string) *CLIModelOption {
	if cliType == CLITypeCursor {
		if base, _, err := SplitCLIModelSelector(model); err == nil {
			model = base
		}
	}
	for i := range catalog.Options {
		if catalog.Options[i].ID == model {
			return &catalog.Options[i]
		}
	}
	for i := range catalog.Options {
		if slices.Contains(catalog.Options[i].Aliases, model) {
			return &catalog.Options[i]
		}
	}
	return nil
}

func ValidateThinkingLevel(ctx context.Context, cliType, model, workDir, level string) error {
	if level == "" {
		return nil
	}
	_, err := ResolveModelThinking(ctx, cliType, model, workDir, level)
	return err
}

// ResolveModelThinking reuses validation when an adapter needs the CLI's native control name.
func ResolveModelThinking(ctx context.Context, cliType, model, workDir, level string) (ModelThinking, error) {
	if !thinkingToken.MatchString(level) {
		return ModelThinking{}, ErrThinkingInvalid
	}
	catalog := GetCLIModelCatalog(ctx, cliType, workDir, model, false)
	option := catalogOption(catalog, cliType, model)
	if option == nil || option.Thinking.State != "fresh" {
		return ModelThinking{}, ErrThinkingUnavailable
	}
	thinking := option.Thinking
	if thinking.Status == "unsupported" {
		return ModelThinking{}, ErrThinkingInvalid
	}
	if thinking.Status != "supported" {
		return ModelThinking{}, ErrThinkingUnavailable
	}
	for _, allowed := range thinking.Levels {
		if allowed.Value == level {
			return thinking, nil
		}
	}
	return ModelThinking{}, ErrThinkingInvalid
}
