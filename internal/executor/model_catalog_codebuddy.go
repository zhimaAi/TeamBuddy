package executor

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

func init() { RegisterModelCatalogResolver(CLITypeCodeBuddy, resolveCodeBuddyCatalog) }

func resolveCodeBuddyCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	output, err := probeStreamJSONMetadata(ctx, execPath, workDir, "{\"type\":\"control_request\",\"request_id\":\"init\",\"request\":{\"subtype\":\"initialize\",\"hooks\":{}}}\n{\"type\":\"control_request\",\"request_id\":\"models\",\"request\":{\"subtype\":\"get_available_models\"}}\n", "--verbose")
	if err != nil {
		return unknownCatalog(resolveCodeBuddyModels(ctx, execPath)), err
	}
	catalog, err := parseCodeBuddyCatalog(output)
	return appendCatalogNames(catalog, codebuddySettingsModelIDs()), err
}

// stream-json 元数据传输由各 CLI 显式传入其启动差异；不发送 prompt，不持久化业务会话。
func probeStreamJSONMetadata(ctx context.Context, execPath, workDir, requests string, cliArgs ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	settings, err := os.CreateTemp("", "teambuddy-cli-probe-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(settings.Name())
	if _, err = settings.WriteString(`{"disableAllHooks":true}`); err != nil {
		settings.Close()
		return nil, err
	}
	if err = settings.Close(); err != nil {
		return nil, err
	}
	mcp, err := os.CreateTemp("", "teambuddy-cli-mcp-probe-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(mcp.Name())
	if _, err = mcp.WriteString(`{"mcpServers":{}}`); err != nil {
		mcp.Close()
		return nil, err
	}
	if err = mcp.Close(); err != nil {
		return nil, err
	}
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--no-session-persistence", "--settings", settings.Name(), "--strict-mcp-config", "--mcp-config", mcp.Name()}
	cmd, err := NewCLIProbeCommand(ctx, execPath, append(args, cliArgs...)...)
	if err != nil {
		return nil, err
	}
	cmd.Dir, cmd.Stderr = workDir, io.Discard
	// 只发送元数据控制帧；不发送 user/prompt，也不创建业务会话。
	cmd.Stdin = strings.NewReader(requests)
	output := probeOutput{cancel: cancel}
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return output.buffer.Bytes(), nil
}

func parseCodeBuddyCatalog(output []byte) (CLIModelCatalog, error) {
	for _, line := range strings.Split(string(output), "\n") {
		var frame struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Response  struct {
					AvailableModels []struct {
						ID string `json:"modelId"`
					} `json:"availableModels"`
					RawModels []struct {
						ID        string `json:"id"`
						Reasoning struct {
							SupportedEfforts []string `json:"supportedEfforts"`
							DefaultEffort    string   `json:"defaultEffort"`
						} `json:"reasoning"`
					} `json:"rawModels"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Type != "control_response" || frame.Response.RequestID != "models" || frame.Response.Subtype != "success" {
			continue
		}
		var names []string
		for _, item := range frame.Response.Response.AvailableModels {
			if item.ID != "" {
				names = append(names, item.ID)
			}
		}
		catalog := unknownCatalog(names)
		for i := range catalog.Options {
			for _, raw := range frame.Response.Response.RawModels {
				if raw.ID == catalog.Options[i].ID && raw.Reasoning.SupportedEfforts != nil {
					catalog.Options[i].Thinking = ThinkingFromLevels("effort", raw.Reasoning.SupportedEfforts, raw.Reasoning.DefaultEffort)
					if catalog.Options[i].Thinking.Source != "client_token_limit" {
						catalog.Options[i].Thinking.Source = "codebuddy_raw_models"
					}
					break
				}
			}
		}
		if len(names) > 0 {
			return catalog, nil
		}
	}
	return unknownCatalog(nil), ErrThinkingUnavailable
}
