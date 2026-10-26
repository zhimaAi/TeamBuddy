package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"

	"goteams-client/internal/protocol"
)

// UserInfo cloud user information
type UserInfo struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	Role        string `json:"role"`
}

// LoginResponse login response
type LoginResponse struct {
	Token        string   `json:"token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresAt    int64    `json:"expires_at"`
	User         UserInfo `json:"user"`
	AdminID      string   `json:"admin_id"`
	TenantID     string   `json:"tenant_id"`
}

// DeviceCredential Credentials returned by device registration
type DeviceCredential struct {
	Credential string `json:"credential"`
	DeviceUUID string `json:"device_uuid"`
}

// loginPayload 云端下发登录态的统一结构（账号密码登录与浏览器登录票据兑换同构）。
// 当前云端是扁平结构；User/RefreshToken 字段用于兼容后续版本。
type loginPayload struct {
	Token        string   `json:"token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresAt    int64    `json:"expires_at"`
	AdminID      string   `json:"admin_id"`
	TenantID     string   `json:"tenant_id"`
	ID           string   `json:"id"`
	Username     string   `json:"username"`
	Name         string   `json:"name"`
	Avatar       string   `json:"avatar"`
	RoleID       string   `json:"role_id"`
	User         UserInfo `json:"user"`
}

// toLoginResponse 归一化云端返回：补齐扁平结构中的用户字段，并校验关键字段非空。
// fallbackUsername 仅在云端未回传用户名时使用（浏览器登录没有用户输入的账号，传空）。
func (p loginPayload) toLoginResponse(fallbackUsername string) (*LoginResponse, error) {
	if p.User.ID == "" {
		p.User = UserInfo{
			ID:          p.ID,
			Username:    p.Username,
			DisplayName: p.Name,
			Avatar:      p.Avatar,
			Role:        p.RoleID,
		}
	}
	if p.User.Username == "" {
		p.User.Username = fallbackUsername
	}
	if p.Token == "" || p.AdminID == "" || p.User.ID == "" {
		return nil, fmt.Errorf("云端登录响应缺少 token、admin_id 或用户 ID")
	}
	return &LoginResponse{
		Token:        p.Token,
		RefreshToken: p.RefreshToken,
		ExpiresAt:    p.ExpiresAt,
		User:         p.User,
		AdminID:      p.AdminID,
		TenantID:     p.TenantID,
	}, nil
}

// Login calls the cloud login interface
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResponse, error) {
	c = c.WithoutCredentials()
	body := map[string]string{
		"username": username,
		"password": password,
	}

	var payload loginPayload
	if err := c.do(ctx, http.MethodPost, "/api/auth/login", body, &payload); err != nil {
		return nil, err
	}
	return payload.toLoginResponse(username)
}

// RefreshToken refresh JWT
func (c *Client) RefreshToken(ctx context.Context, token string) (*LoginResponse, error) {
	c = c.WithoutCredentials()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/user/refreshToken", nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	c.injectAuthHeaders(req)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("刷新令牌失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError(resp)
	}

	var payload loginPayload
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if err := decodeAPIResponse(resp.StatusCode, respBody, &payload); err != nil {
		return nil, err
	}
	return payload.toLoginResponse("")
}

// GetMe gets the current user information
func (c *Client) GetMe(ctx context.Context) (*UserInfo, error) {
	var result struct {
		ID       string `json:"user_id"`
		Username string `json:"username"`
		Name     string `json:"name"`
		Avatar   string `json:"avatar"`
		Role     int    `json:"role_id"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/client/me", nil, &result); err != nil {
		return nil, err
	}
	return &UserInfo{
		ID:          result.ID,
		Username:    result.Username,
		DisplayName: result.Name,
		Avatar:      result.Avatar,
		Role:        strconv.Itoa(result.Role),
	}, nil
}

// RegisterDevice registers the device (using the boot key)
func (c *Client) RegisterDevice(ctx context.Context, deviceUUID, bootstrapKey string) (*DeviceCredential, error) {
	deviceName, _ := os.Hostname()
	body := map[string]interface{}{
		"device_uuid":      deviceUUID,
		"device_name":      deviceName,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"client_version":   c.cloudCfg.ClientVersion,
		"protocol_version": protocol.ProtocolVersion,
	}

	url := c.baseURL + "/api/client/devices/register"
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.injectBootstrapHeaders(req, bootstrapKey)
	req.Header.Set("X-GoTeams-Device-ID", deviceUUID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("设备注册请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError(resp)
	}

	var result DeviceCredential
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if err := decodeAPIResponse(resp.StatusCode, respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetCompatibility checks client version compatibility
func (c *Client) GetCompatibility(ctx context.Context) (bool, error) {
	var result struct {
		Compatible      bool   `json:"compatible"`
		LatestVersion   string `json:"latest_version"`
		UpgradeRequired bool   `json:"upgrade_required"`
		Message         string `json:"message"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/client/compatibility", nil, &result); err != nil {
		return false, err
	}
	return result.Compatible, nil
}
