package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"goteams-client/internal/config"
	"goteams-client/internal/protocol"
	"goteams-client/internal/secrets"
)

// Cloud Client cloud HTTP client
type Client struct {
	baseURL      string
	httpClient   *http.Client
	transfer     *http.Client // 大文件上传专用（超时更长），与 httpClient 共用 Transport
	secretStore  secrets.Store
	cloudCfg     *config.CloudConfig
	sessionScope string
	userID       string // immutable owner of an authenticated account client
}

// NewClient creates a cloud HTTP client
func NewClient(cfg *config.CloudConfig, store secrets.Store) *Client {
	// Custom Transport: Bypass the system proxy for the cloud API host to avoid returning EOF when the proxy is unavailable.
	//The remaining hosts still follow the default proxy policy.
	var cloudHost string
	if u, err := url.Parse(cfg.APIBaseURL); err == nil {
		cloudHost = u.Host
	}
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if cloudHost != "" && req.URL.Host == cloudHost {
				return nil, nil // Connect directly to the cloud without going through a proxy
			}
			return http.ProxyFromEnvironment(req)
		},
	}

	return &Client{
		baseURL: strings.TrimRight(cfg.APIBaseURL, "/"),
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		// 20MB 单文件在慢速上行下会超过 30s；产出文档上传单独给 90s 预算。
		transfer: &http.Client{
			Timeout:   90 * time.Second,
			Transport: transport,
		},
		secretStore: store,
		cloudCfg:    cfg,
	}
}

// BaseURL returns the cloud API base address
func (c *Client) BaseURL() string {
	return c.baseURL
}

// CloudConfig returns cloud configuration
func (c *Client) CloudConfig() *config.CloudConfig {
	return c.cloudCfg
}

// IsConfigured checks whether the cloud address has been configured
func (c *Client) IsConfigured() bool {
	return c.baseURL != ""
}

// getJWT reads JWT from SecretStore
func (c *Client) getJWT() string {
	if c.secretStore == nil {
		return ""
	}
	token, err := c.secretStore.Get(secrets.KeyJWT)
	if err != nil {
		return ""
	}
	return token
}

// getDeviceCredential reads device credentials from SecretStore
func (c *Client) getDeviceCredential() string {
	if c.secretStore == nil {
		return ""
	}
	cred, err := c.secretStore.Get(secrets.KeyDeviceCredential)
	if err != nil {
		return ""
	}
	return cred
}

// getDeviceUUID Read device UUID from SecretStore
func (c *Client) getDeviceUUID() string {
	if c.secretStore == nil {
		return ""
	}
	uuid, err := c.secretStore.Get(secrets.KeyPrefix + "device_uuid")
	if err != nil {
		return ""
	}
	return uuid
}

// do general request method, automatically injects authentication headers
func (c *Client) do(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	if !c.IsConfigured() {
		return fmt.Errorf("云端 API 地址未配置")
	}

	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	//Inject authentication header
	c.injectAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求云端失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if result != nil && len(respBody) > 0 {
		if err := decodeAPIResponse(resp.StatusCode, respBody, result); err != nil {
			return err
		}
	}

	return nil
}

// decodeAPIResponse is compatible with both cloud unified response {code, message, data} and legacy direct response.
// The cloud success code is currently 200, and some old interfaces use 0.
func decodeAPIResponse(statusCode int, body []byte, result interface{}) error {
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Code) > 0 {
		// If the code is null, it is regarded as "the error code is not provided by the cloud". Press success to continue parsing the data.
		// The old implementation ignores decoding errors and directly fmt.Sprint(nil), it will get "<nil>" and replace
		// The originally successful response was misjudged as APIError.
		if codeRaw := string(bytes.TrimSpace(envelope.Code)); codeRaw != "null" {
			var code interface{}
			if err := json.Unmarshal(envelope.Code, &code); err != nil {
				return fmt.Errorf("解析云端响应 code 失败: %w, body: %s", err, truncateBody(body))
			}
			codeText := fmt.Sprint(code)
			if codeText != "0" && codeText != "200" {
				return &APIError{
					StatusCode: statusCode,
					Code:       codeText,
					Message:    envelope.Message,
					Body:       truncateBody(body),
				}
			}
		}
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return nil
		}
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("解析云端响应 data 失败: %w, body: %s", err, truncateBody(body))
		}
		return nil
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}

// doRaw sends the request but does not parse the response body and returns the original response (used in special scenarios such as device registration)
func (c *Client) doRaw(ctx context.Context, method, path string, body interface{}, headers map[string]string) (*http.Response, error) {
	if !c.IsConfigured() {
		return nil, fmt.Errorf("云端 API 地址未配置")
	}

	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	//Inject additional headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	//Inject authentication header
	c.injectAuthHeaders(req)

	return c.httpClient.Do(req)
}

// doMultipart 以 multipart/form-data 发送一个文件加上若干文本字段。
// Content-Length 可预期（内存一次组装），不走 chunked：部分网关对 chunked 上传有额外限制。
// 使用独立的 transfer 客户端（90s），避免 20MB 文件被 30s 通用超时截断。
func (c *Client) doMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName string, content []byte) (*http.Response, error) {
	if !c.IsConfigured() {
		return nil, fmt.Errorf("云端 API 地址未配置")
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, fmt.Errorf("组装上传字段失败: %w", err)
		}
	}
	part, err := writer.CreateFormFile(fileField, fileName)
	if err != nil {
		return nil, fmt.Errorf("组装上传文件失败: %w", err)
	}
	if _, err = part.Write(content); err != nil {
		return nil, fmt.Errorf("写入上传内容失败: %w", err)
	}
	if err = writer.Close(); err != nil {
		return nil, fmt.Errorf("结束上传组装失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.injectAuthHeaders(req)

	client := c.transfer
	if client == nil {
		client = c.httpClient
	}
	return client.Do(req)
}

// injectAuthHeaders inject authentication request headers
func (c *Client) injectAuthHeaders(req *http.Request) {
	jwt := c.getJWT()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}

	deviceUUID := c.getDeviceUUID()
	if deviceUUID != "" {
		req.Header.Set("X-GoTeams-Device-ID", deviceUUID)
	}

	deviceCred := c.getDeviceCredential()
	if deviceCred != "" {
		req.Header.Set("X-GoTeams-Device-Credential", deviceCred)
		req.Header.Set("X-GoTeams-Device-Credential-Version", "1")
	}

	if c.cloudCfg != nil && c.cloudCfg.ClientVersion != "" {
		req.Header.Set("X-GoTeams-Client-Version", c.cloudCfg.ClientVersion)
	}
	req.Header.Set("X-GoTeams-Protocol-Version", fmt.Sprintf("%d", protocol.ProtocolVersion))
}

// injectBootstrapHeaders injects boot key headers (only for device registration)
func (c *Client) injectBootstrapHeaders(req *http.Request, bootstrapKey string) {
	jwt := c.getJWT()
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	if bootstrapKey != "" {
		req.Header.Set("X-Bootstrap-Key", bootstrapKey)
	}
	if c.cloudCfg != nil && c.cloudCfg.ClientVersion != "" {
		req.Header.Set("X-GoTeams-Client-Version", c.cloudCfg.ClientVersion)
	}
	req.Header.Set("X-GoTeams-Protocol-Version", fmt.Sprintf("%d", protocol.ProtocolVersion))
}
