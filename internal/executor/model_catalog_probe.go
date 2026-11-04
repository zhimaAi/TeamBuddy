package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ModelCatalogResolver 只投影模型标识和可选择能力，不返回 provider 凭据等原始配置。
type ModelCatalogResolver func(context.Context, string, string, string) (CLIModelCatalog, error)

var catalogResolvers = map[string]ModelCatalogResolver{
	CLITypeOpenCode: resolveOpenCodeCatalog,
	CLITypePi:       resolvePiCatalog,
}

// RegisterModelCatalogResolver 由独立适配包在初始化阶段注册，避免父包导入子包。
func RegisterModelCatalogResolver(cliType string, resolver ModelCatalogResolver) {
	catalogResolvers[cliType] = resolver
}

func modelCatalogKey(cliType, workDir string) string {
	home, _ := os.UserHomeDir()
	if workDir == "" {
		workDir = home
	}
	configNames := map[string][]string{
		CLITypeClaude:    {".claude/settings.json", ".claude/settings.local.json", ".claude/.credentials.json", ".claude.json"},
		CLITypeCodeBuddy: {".codebuddy/settings.json", ".codebuddy/models.json", ".codebuddy.json"},
		CLITypeCodex:     {".codex/config.toml", ".codex/auth.json"},
		CLITypeOpenCode:  {".config/opencode/opencode.json", ".config/opencode/opencode.jsonc", ".local/share/opencode/auth.json"},
		CLITypePi:        {".pi/agent/models.json", ".pi/agent/auth.json", ".pi/agent/settings.json"},
		CLITypeQoder:     {".qoder/settings.json"}, CLITypeQoderCN: {".qoder-cn/settings.json"},
		CLITypeCursor: {".cursor/cli-config.json"}, CLITypeCopilot: {".copilot/config.json", ".copilot/hosts.json"},
		CLITypeKimi:        {".kimi-code/config.toml", ".kimi-code/plugins/installed.json"},
		CLITypeQwen:        {".qwen/settings.json", ".qwen/oauth_creds.json"},
		CLITypeAntigravity: {".gemini/antigravity-cli/settings.json"},
	}
	var paths []string
	for _, name := range configNames[cliType] {
		paths = append(paths, filepath.Join(home, name))
	}
	envNames := map[string][]string{
		CLITypeCodex: {"CODEX_HOME"}, CLITypeOpenCode: {"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "XDG_CONFIG_HOME"},
		CLITypePi: {"PI_CODING_AGENT_DIR"}, CLITypeClaude: {"CLAUDE_CONFIG_DIR"}, CLITypeCodeBuddy: {"CODEBUDDY_CONFIG_DIR"},
		CLITypeCursor: {"CURSOR_CONFIG_DIR", "XDG_CONFIG_HOME"}, CLITypeCopilot: {"COPILOT_HOME"},
		CLITypeKimi: {"KIMI_CODE_HOME"}, CLITypeQwen: {"QWEN_HOME", "QWEN_CODE_SYSTEM_SETTINGS_PATH", "QWEN_CODE_SYSTEM_DEFAULTS_PATH"},
	}
	for _, name := range envNames[cliType] {
		// Only configuration locations and file metadata; never credential contents.
		if path := os.Getenv(name); path != "" {
			paths = append(paths, path)
			for _, file := range []string{"config.toml", "models.json", "auth.json", "settings.json", "opencode.json", "opencode.jsonc", "cli-config.json", "config.json"} {
				paths = append(paths, filepath.Join(path, file))
			}
		}
	}
	if cliType == CLITypeCursor && os.Getenv("CURSOR_CONFIG_DIR") == "" && os.Getenv("XDG_CONFIG_HOME") != "" {
		paths = append(paths, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "cursor", "cli-config.json"))
	}
	projectNames := map[string][]string{
		CLITypeOpenCode: {"opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc"},
		CLITypePi:       {".pi/settings.json", ".pi/models.json"}, CLITypeCodex: {".codex/config.toml"},
		CLITypeClaude: {".claude/settings.json", ".claude/settings.local.json"}, CLITypeCodeBuddy: {".codebuddy/settings.json", ".codebuddy/models.json"},
		CLITypeQoder: {".qoder/settings.json", ".qoder/settings.local.json"}, CLITypeQoderCN: {".qoder-cn/settings.json", ".qoder-cn/settings.local.json"},
		CLITypeCursor: {".cursor/cli.json", ".cursor/cli-config.json"}, CLITypeCopilot: {".copilot/config.json"},
		CLITypeKimi: {".kimi-code/config.toml"}, CLITypeQwen: {".qwen/settings.json"},
	}
	for dir := filepath.Clean(workDir); dir != "."; dir = filepath.Dir(dir) {
		for _, name := range projectNames[cliType] {
			paths = append(paths, filepath.Join(dir, name))
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	hash := sha256.New()
	fmt.Fprintln(hash, workDir)
	for _, path := range paths {
		fmt.Fprintln(hash, path)
		if info, err := os.Stat(path); err == nil {
			// Directory mtimes include unrelated native caches/atomic rewrites;
			// only the explicitly tracked configuration files invalidate a catalog.
			if info.IsDir() {
				continue
			}
			if cliType == CLITypeCopilot && filepath.Base(path) == "config.json" && info.Mode().IsRegular() && info.Size() <= maxCLIProbeOutput {
				// Copilot rewrites byte-identical JSONC on metadata startup. mtime
				// would miss the cache on every read. Hash bounded bytes only;
				// never parse, project or retain configuration/credential values.
				if file, err := os.Open(path); err == nil {
					fingerprint := sha256.New()
					n, readErr := io.Copy(fingerprint, io.LimitReader(file, maxCLIProbeOutput+1))
					file.Close()
					if readErr == nil && n <= maxCLIProbeOutput {
						fmt.Fprintln(hash, n, hex.EncodeToString(fingerprint.Sum(nil)))
						continue
					}
				}
			}
			fmt.Fprintln(hash, info.Size(), info.ModTime().UnixNano())
		}
	}
	return cliType + "\x00" + hex.EncodeToString(hash.Sum(nil))
}

func discoverModelCatalog(ctx context.Context, cliType, execPath, workDir, model string) (CLIModelCatalog, error) {
	if execPath == "" {
		return unknownCatalog(nil), ErrThinkingUnavailable
	}
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	if resolver := catalogResolvers[cliType]; resolver != nil {
		return resolver(ctx, execPath, workDir, model)
	}
	catalog := unknownCatalog(DiscoverCLIModels(ctx, cliType))
	// 旧 resolver 的静态兜底只提供名字，不提供模型级能力。
	if len(catalog.Options) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	return catalog, nil
}

func ThinkingFromLevels(kind string, values []string, defaultLevel string) ModelThinking {
	t := ModelThinking{Status: "unsupported", Kind: kind, Source: "cli_catalog", State: "fresh", UpdatedAt: time.Now()}
	seen := map[string]bool{}
	for _, value := range values {
		if !thinkingToken.MatchString(value) || seen[value] {
			continue
		}
		seen[value] = true
		t.Levels = append(t.Levels, ThinkingOption{Value: value})
	}
	if len(t.Levels) > 0 {
		t.Status = "supported"
	}
	if len(values) > 0 && len(t.Levels) == 0 {
		t.Status, t.State, t.Source = "unknown", "unknown", "client_token_limit"
	}
	if seen[defaultLevel] {
		t.DefaultLevel = defaultLevel
	}
	return t
}

func resolveOpenCodeCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := NewCLIProbeCommand(ctx, execPath, "models", "--verbose")
	if err != nil {
		return unknownCatalog(nil), err
	}
	cmd.Dir, cmd.Stderr = workDir, io.Discard
	output := probeOutput{cancel: cancel}
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return unknownCatalog(nil), err
	}
	return parseOpenCodeCatalog(output.buffer.Bytes())
}

func parseOpenCodeCatalog(output []byte) (CLIModelCatalog, error) {
	catalog := unknownCatalog(nil)
	rest := strings.TrimSpace(stripANSI(string(output)))
	for rest != "" {
		line, remaining, _ := strings.Cut(rest, "\n")
		rest = strings.TrimSpace(remaining)
		id := strings.TrimSpace(line)
		if !strings.Contains(id, "/") || !strings.HasPrefix(rest, "{") {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(rest))
		var raw struct {
			Variants json.RawMessage `json:"variants"`
		}
		if err := decoder.Decode(&raw); err != nil {
			return catalog, err
		}
		rest = strings.TrimSpace(rest[decoder.InputOffset():])
		option := unknownCatalog([]string{id}).Options[0]
		if len(raw.Variants) > 0 && !bytes.Equal(raw.Variants, []byte("null")) {
			var variants map[string]json.RawMessage
			if err := json.Unmarshal(raw.Variants, &variants); err != nil {
				return catalog, err
			}
			values := make([]string, 0, len(variants))
			for value := range variants {
				values = append(values, value)
			}
			sort.Strings(values)
			option.Thinking = ThinkingFromLevels("variant", values, "")
			if option.Thinking.Source != "client_token_limit" {
				option.Thinking.Source = "opencode_models_verbose"
			}
		}
		catalog.Options = append(catalog.Options, option)
	}
	if len(catalog.Options) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	return catalog, nil
}

// 探测并发有界，不复用业务会话，也不通过 prompt 验证各档。
var piCatalogSlots = make(chan struct{}, 4)

func resolvePiSelectedCatalog(ctx context.Context, execPath, workDir, model string) (CLIModelCatalog, error) {
	select {
	case piCatalogSlots <- struct{}{}:
		defer func() { <-piCatalogSlots }()
	default:
		return unknownCatalog(nil), ErrThinkingUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"--mode", "rpc", "--no-session", "--offline", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-tools"}
	// 启动时指定模型，不依赖裸模型名的模糊匹配或切换命令的持久化行为。
	if model != "" {
		provider, id, ok := strings.Cut(model, "/")
		if !ok || provider == "" || id == "" {
			return unknownCatalog(nil), ErrThinkingUnavailable
		}
		args = append(args, "--provider", provider, "--model", id)
	}
	cmd, err := NewCLIProbeCommand(ctx, execPath, args...)
	if err != nil {
		return unknownCatalog(nil), err
	}
	cmd.Dir, cmd.Stderr = workDir, io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return unknownCatalog(nil), err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return unknownCatalog(nil), err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return unknownCatalog(nil), err
	}
	defer func() {
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
			cancel()
			<-done
		}
	}()
	lines := make(chan []byte, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(io.LimitReader(stdout, maxCLIProbeOutput+1))
		scanner.Buffer(make([]byte, 4096), maxCLIProbeOutput)
		total := 0
		for scanner.Scan() {
			total += len(scanner.Bytes()) + 1
			if total > maxCLIProbeOutput {
				cancel()
				return
			}
			line := append([]byte{}, scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			cancel()
		}
	}()
	call := func(command string, out any) error {
		if err := json.NewEncoder(stdin).Encode(map[string]string{"id": command, "type": command}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case line, ok := <-lines:
				if !ok {
					return ErrThinkingUnavailable
				}
				var response struct {
					ID      string          `json:"id"`
					Success bool            `json:"success"`
					Data    json.RawMessage `json:"data"`
				}
				if json.Unmarshal(line, &response) != nil || response.ID != command {
					continue
				}
				if !response.Success {
					return ErrThinkingUnavailable
				}
				return json.Unmarshal(response.Data, out)
			}
		}
	}
	var models struct {
		Models []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"models"`
	}
	if err = call("get_available_models", &models); err != nil {
		return unknownCatalog(nil), err
	}
	var names []string
	for _, item := range models.Models {
		if item.Provider != "" && item.ID != "" {
			names = append(names, item.Provider+"/"+item.ID)
		}
	}
	catalog := unknownCatalog(names)
	if len(names) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	if model == "" {
		return catalog, nil
	}
	var state struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
	}
	if err = call("get_state", &state); err != nil {
		return catalog, err
	}
	if state.Model.Provider+"/"+state.Model.ID != model {
		return catalog, ErrThinkingUnavailable
	}
	var levels struct {
		Levels []string `json:"levels"`
	}
	if err = call("get_available_thinking_levels", &levels); err != nil {
		return catalog, err
	}
	if len(levels.Levels) == 0 {
		return catalog, ErrThinkingUnavailable
	}
	for i := range catalog.Options {
		if catalog.Options[i].ID == model {
			catalog.Options[i].Thinking = ThinkingFromLevels("effort", levels.Levels, "")
			catalog.Options[i].Thinking.Source = "pi_rpc"
			// 非推理模型只有固定 off，不把它显示成可调强度。
			if len(levels.Levels) == 1 && levels.Levels[0] == "off" {
				catalog.Options[i].Thinking.Status = "unsupported"
			}
		}
	}
	return catalog, nil
}
