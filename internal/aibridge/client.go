// Package aibridge 访问本机 AiBridge 扩展（MobAI aibridge-extension）的 HTTP 接口。
// 该扩展运行在 VS Code / Cursor / Windsurf 内，监听一个 loopback 端口，可把文本
// 直接注入编辑器的 AI 对话，因此装了它的用户不需要手动复制粘贴提示词。
// 客户端只做一次只读探测和一次注入，不读写扩展配置，也不依赖它的持久状态。
package aibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL 是扩展的默认监听地址。扩展可通过 aibridge.host / aibridge.port
	// 改地址，改过之后无法自动发现，只能由用户手动复制提示词。
	DefaultBaseURL = "http://127.0.0.1:9999"
	// BaseURLEnv 允许部署侧或用户覆盖探测地址。
	BaseURLEnv = "GOTEAMS_AIBRIDGE_BASE_URL"

	probeTimeout  = 2 * time.Second
	injectTimeout = 10 * time.Second
	maxBodyBytes  = 64 * 1024
)

// Health 对应 GET /health 的响应。
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	// Editor 由扩展在运行时探测，取值 vscode / cursor / windsurf。
	Editor string `json:"editor"`
}

// BaseURL 返回本次请求使用的地址。
func BaseURL() string {
	if value := strings.TrimSpace(os.Getenv(BaseURLEnv)); value != "" {
		return strings.TrimRight(value, "/")
	}
	return DefaultBaseURL
}

// newClient 构造不使用任何代理的客户端：AiBridge 只在本机 loopback 上。
func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			DisableKeepAlives: true,
		},
	}
}

// Probe 检测本机 AiBridge 是否可用。只有 /health 明确返回 ok 才算可用；
// 连接被拒、超时、响应异常或状态不是 ok 都按不可用处理，调用方据此退回手动复制。
func Probe(ctx context.Context) (Health, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL()+"/health", nil)
	if err != nil {
		return Health{}, false
	}
	response, err := newClient(probeTimeout).Do(request)
	if err != nil {
		return Health{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Health{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return Health{}, false
	}
	var health Health
	if json.Unmarshal(raw, &health) != nil || health.Status != "ok" {
		return Health{}, false
	}
	return health, true
}

// Inject 把文本排队注入编辑器的 AI 对话。sync 为真时等待扩展处理完成再返回，
// 便于把失败如实反馈给用户；队列本身由扩展负责，客户端不重试、不补发。
func Inject(ctx context.Context, text string, sync bool) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("注入内容为空")
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	target := BaseURL() + "/inject"
	if sync {
		target += "?sync=true"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := newClient(injectTimeout).Do(request)
	if err != nil {
		return fmt.Errorf("连接 AiBridge 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("AiBridge 返回 %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
