package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"goteams-client/internal/executor"
)

// This file implements model discovery for the Codex CLI through the official
// `codex app-server` JSON-RPC endpoint (method: model/list).
//
// Codex itself already exposes the available model catalog via model/list, so we
// must NOT scrape the interactive `/model` TUI or hand-maintain a model-name list.
// model/list also supports pagination through `nextCursor`, which we follow until
// exhausted so the full catalog is returned.
//
// Important: the app-server is a JSON-RPC 2.0 server that REQUIRES an `initialize`
// handshake (with a `clientInfo` field) before any other method may be called;
// otherwise every request is rejected with "Not initialized". It also pushes
// unsolicited notifications (e.g. remoteControl/status/changed) that carry no `id`,
// so callers must skip those and read until the matching response `id` is seen.

const (
	appServerStartupTimeout = 30 * time.Second
	appServerMaxModels      = 1000 // safety cap for pathological pagination
	appServerMaxPages       = 100
	appServerClientName     = "goteams"
	appServerClientVersion  = "0.1.0"
)

// RPCRequest is a JSON-RPC 2.0 request sent to the Codex app-server over stdin.
type RPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	ID      int         `json:"id"`
	Params  interface{} `json:"params,omitempty"`
}

// ModelInfo is a single model entry returned by model/list.
type ModelInfo struct {
	ID                        string `json:"id"`
	Model                     string `json:"model,omitempty"`
	DisplayName               string `json:"displayName,omitempty"`
	Hidden                    bool   `json:"hidden,omitempty"`
	IsDefault                 bool   `json:"isDefault,omitempty"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort string `json:"defaultReasoningEffort"`
}

// ModelListResult is the result payload of model/list (paginated).
type ModelListResult struct {
	Data       []ModelInfo `json:"data"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

// RPCError is a JSON-RPC error object returned by the app-server.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex app-server rpc error %d: %s", e.Code, e.Message)
}

// RPCResponse is a JSON-RPC response read from the app-server stdout.
type RPCResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// notification is used only to distinguish unsolicited notifications (which carry
// a `method` field and no response `id`) from real responses.
type notification struct {
	Method string `json:"method"`
}

// AppServerSession holds a `codex app-server` process and its stdin/stdout
// JSON-RPC pipes. The session is initialized on creation so callers can issue
// model/list (and other) requests immediately.
type AppServerSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	lines     chan []byte
	stop      chan struct{}
	readDone  chan struct{}
	readErr   error // lines 关闭后可读；关闭通道同步读循环的写入。
	mu        sync.Mutex
	nextID    int
	closeOnce sync.Once
	closeErr  error
}

// StartAppServer launches `codex app-server`, performs the required `initialize`
// handshake, and returns a session for issuing JSON-RPC requests. The caller must
// call Close to terminate the process.
func StartAppServer(ctx context.Context, execPath string) (*AppServerSession, error) {
	return startAppServer(ctx, execPath, "")
}

func startAppServer(ctx context.Context, execPath, workDir string) (*AppServerSession, error) {
	cmd, err := executor.NewCLIProbeCommand(ctx, execPath, "app-server")
	if err != nil {
		return nil, err
	}

	cmd.Dir = workDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Discard stderr so the process never blocks on a full stderr pipe.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 codex app-server 失败: %w", err)
	}

	session := &AppServerSession{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		lines:    make(chan []byte, 16),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	// Scanner 在独立读循环中工作，RPC 等待能立即响应取消，
	// 不再等到 Scan 读到下一行才检查 context。
	go func() {
		defer close(session.readDone)
		defer close(session.lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case session.lines <- line:
			case <-ctx.Done():
				return
			case <-session.stop:
				return
			}
		}
		session.readErr = scanner.Err()
	}()

	// Perform the mandatory initialize handshake before any other method.
	if err := session.initialize(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}

	return session, nil
}

// Close terminates the app-server process and frees its resources.
func (s *AppServerSession) Close() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.stdin.Close()
		_ = s.stdout.Close()
		<-s.readDone
		terminateErr := terminateProcessTreeForPID(s.cmd.Process.Pid)
		s.closeErr = s.cmd.Wait()
		if terminateErr != nil {
			s.closeErr = terminateErr
		}
	})
	return s.closeErr
}

// initialize performs the JSON-RPC `initialize` handshake required by the
// app-server before any other request may be issued.
func (s *AppServerSession) initialize(ctx context.Context) error {
	var raw json.RawMessage
	if err := s.call(ctx, "initialize", map[string]interface{}{
		"clientInfo": map[string]interface{}{
			"name":    appServerClientName,
			"version": appServerClientVersion,
		},
	}, &raw); err != nil {
		return fmt.Errorf("codex app-server 初始化失败: %w", err)
	}
	return json.NewEncoder(s.stdin).Encode(map[string]interface{}{"method": "initialized"})
}

// call sends a single JSON-RPC request and blocks until a response with the
// matching id is read. Unsolicited notifications (lines carrying a `method` field
// and no response `id`) are skipped, as are non-JSON/non-matching lines.
func (s *AppServerSession) call(ctx context.Context, method string, params interface{}, out *json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// 取消也关闭 stdin，避免服务端不读请求时 Encode 卡住。
	stopCancel := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stopCancel()
	s.nextID++
	id := s.nextID
	req := RPCRequest{JSONRPC: "2.0", Method: method, ID: id, Params: params}
	if err := json.NewEncoder(s.stdin).Encode(req); err != nil {
		return fmt.Errorf("发送 %s 请求失败: %w", method, err)
	}

	for {
		var line []byte
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s 请求超时: %w", method, ctx.Err())
		case <-s.stop:
			return fmt.Errorf("读取 %s 响应失败: 连接已关闭", method)
		case next, ok := <-s.lines:
			if !ok {
				if s.readErr != nil {
					return fmt.Errorf("读取 %s 响应失败: %w", method, s.readErr)
				}
				return fmt.Errorf("读取 %s 响应失败: 连接已关闭", method)
			}
			line = next
		}

		// Skip unsolicited notifications (method present, no response id).
		var note notification
		if err := json.Unmarshal(line, &note); err == nil && note.Method != "" {
			continue
		}

		var resp RPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// Skip non-JSON lines such as startup banners.
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return resp.Error
		}
		*out = resp.Result
		return nil
	}
}

// ListModels pages through model/list (following nextCursor) and returns the
// full model catalog. When includeHidden is true, hidden models are included.
func (s *AppServerSession) ListModels(ctx context.Context, includeHidden bool) ([]ModelInfo, error) {
	var all []ModelInfo
	cursor := ""
	seenCursors := make(map[string]bool)
	for page := 0; page < appServerMaxPages; page++ {
		params := map[string]interface{}{"includeHidden": includeHidden}
		if cursor != "" {
			params["cursor"] = cursor
		}

		var raw json.RawMessage
		if err := s.call(ctx, "model/list", params, &raw); err != nil {
			return nil, err
		}

		var res ModelListResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("解析 model/list 结果失败: %w", err)
		}

		all = append(all, res.Data...)
		if len(all) > appServerMaxModels {
			return nil, fmt.Errorf("model/list 超过模型数量限制")
		}
		if res.NextCursor == "" {
			return all, nil
		}
		if seenCursors[res.NextCursor] {
			return nil, fmt.Errorf("model/list 返回重复分页游标")
		}
		seenCursors[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("model/list 超过分页数量限制")
}

// ResolveModels returns the list of models available to the Codex CLI.
// It prefers the authoritative model/list endpoint (via the app-server JSON-RPC
// protocol); on any failure (app-server unavailable, uninitialized, model/list
// unsupported, or no provider configured) it falls back to parsing
// ~/.codex/config.toml so existing behavior is preserved.
func ResolveModels(ctx context.Context, execPath string) []string {
	catalog, _ := resolveCatalog(ctx, execPath, "", "")
	return catalog.Models()
}

func resolveCatalog(ctx context.Context, execPath, workDir, _ string) (executor.CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, appServerStartupTimeout)
	defer cancel()
	configured := resolveCodexModelsFromConfig()
	catalog := executor.CLIModelCatalog{Meta: executor.CatalogMeta{State: "unknown"}, Options: []executor.CLIModelOption{}}
	for _, id := range configured {
		catalog.Options = append(catalog.Options, executor.CLIModelOption{ID: id, Thinking: executor.ModelThinking{Status: "unknown", Source: "unavailable", State: "unknown"}})
	}
	session, err := startAppServer(ctx, execPath, workDir)
	if err != nil {
		return catalog, err
	}
	defer session.Close()
	models, err := session.ListModels(ctx, false)
	if err != nil {
		return catalog, err
	}
	for _, m := range models {
		name := m.ID
		if name == "" {
			name = m.Model
		}
		if name == "" {
			continue
		}
		thinking := executor.ModelThinking{Status: "unknown", Source: "unavailable", State: "unknown"}
		if m.SupportedReasoningEfforts != nil {
			values := []string{}
			for _, effort := range m.SupportedReasoningEfforts {
				// ultra 包含自动委派，不是普通强度，不在本功能开放。
				if effort.ReasoningEffort != "ultra" {
					values = append(values, effort.ReasoningEffort)
				}
			}
			thinking = executor.ThinkingFromLevels("effort", values, m.DefaultReasoningEffort)
			for i := range thinking.Levels {
				for _, effort := range m.SupportedReasoningEfforts {
					if thinking.Levels[i].Value == effort.ReasoningEffort {
						thinking.Levels[i].Description = effort.Description
					}
				}
			}
		}
		found := false
		for i := range catalog.Options {
			if catalog.Options[i].ID == name {
				catalog.Options[i].Thinking = thinking
				found = true
				break
			}
		}
		if !found {
			catalog.Options = append(catalog.Options, executor.CLIModelOption{ID: name, Thinking: thinking})
		}
	}
	if len(models) == 0 {
		return catalog, executor.ErrThinkingUnavailable
	}
	return catalog, nil
}

// resolveCodexModelsFromConfig reads configured models from ~/.codex/config.toml.
// It is a fallback used when the app-server model/list endpoint cannot be reached.
func resolveCodexModelsFromConfig() []string {
	configDir := os.Getenv("CODEX_HOME")
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return []string{}
		}
		configDir = filepath.Join(homeDir, ".codex")
	}

	configPath := filepath.Join(configDir, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return []string{}
	}

	var models []string
	// Match model = "xxx" or model = 'xxx'
	re := regexp.MustCompile(`(?m)^\s*model\s*=\s*["']([^"']+)["']`)
	matches := re.FindAllStringSubmatch(string(data), -1)
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			models = append(models, match[1])
		}
	}

	// profile 的名称不是模型 ID；只读取各 profile 内实际声明的 model。

	return dedupStrings(models)
}

// dedupStrings removes duplicate entries while preserving order.
func dedupStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

// init registers the Codex model resolver with the executor package. This avoids
// an import cycle (executor cannot import codex, but codex can import executor).
func init() {
	executor.RegisterCodexModelResolver(ResolveModels)
	executor.RegisterModelCatalogResolver(executor.CLITypeCodex, resolveCatalog)
}
