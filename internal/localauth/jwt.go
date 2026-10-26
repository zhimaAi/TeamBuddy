package localauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/secrets"
)

// JWTClaims Claims in JWT payload
type JWTClaims struct {
	AdminID  string `json:"admin_id"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Exp      int64  `json:"exp"`
}

// JWTManager JWT token management
type JWTManager struct {
	store      secrets.Store
	mu         sync.RWMutex
	cancelFunc context.CancelFunc
	expiry     int64
}

// NewJWTManager creates a JWT manager
func NewJWTManager(store secrets.Store) *JWTManager {
	return &JWTManager{
		store: store,
	}
}

// SaveToken saves JWT and refresh token to SecretStore
func (m *JWTManager) SaveToken(token, refreshToken string, expiresAt int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return fmt.Errorf("SecretStore 未初始化")
	}

	if err := m.store.Set(secrets.KeyJWT, token); err != nil {
		return fmt.Errorf("保存 JWT 失败: %w", err)
	}

	if refreshToken != "" {
		if err := m.store.Set(secrets.KeyPrefix+"jwt_refresh", refreshToken); err != nil {
			return fmt.Errorf("保存 refresh token 失败: %w", err)
		}
	}

	//Also save the expiration time
	if expiresAt > 0 {
		if err := m.store.Set(secrets.KeyPrefix+"jwt_expires_at", fmt.Sprintf("%d", expiresAt)); err != nil {
			return fmt.Errorf("保存过期时间失败: %w", err)
		}
	}

	m.expiry = expiresAt
	return nil
}

// GetToken reads JWT from SecretStore
func (m *JWTManager) GetToken() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.store == nil {
		return "", fmt.Errorf("SecretStore 未初始化")
	}
	return m.store.Get(secrets.KeyJWT)
}

// GetRefreshToken reads refresh token from SecretStore
func (m *JWTManager) GetRefreshToken() (string, error) {
	if m.store == nil {
		return "", fmt.Errorf("SecretStore 未初始化")
	}
	return m.store.Get(secrets.KeyPrefix + "jwt_refresh")
}

// ParseClaims parses JWT payload (no signature verification, only base64 decode)
func ParseClaims(token string) (*JWTClaims, error) {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return nil, fmt.Errorf("无效的 JWT 格式")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("解码 JWT payload 失败: %w", err)
	}

	// 历史云端可能把数字 ID 编码为 JSON number；保留整数精度再转为内部字符串。
	// 不推断其他字段作为用户身份，迁移仍须同时匹配 admin_id 和 user_id。
	var raw struct {
		AdminID  json.RawMessage `json:"admin_id"`
		UserID   json.RawMessage `json:"user_id"`
		TenantID json.RawMessage `json:"tenant_id"`
		Exp      int64           `json:"exp"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("解析 JWT claims 失败: %w", err)
	}
	claims := &JWTClaims{Exp: raw.Exp}
	for _, field := range []struct {
		raw json.RawMessage
		dst *string
	}{{raw.AdminID, &claims.AdminID}, {raw.UserID, &claims.UserID}, {raw.TenantID, &claims.TenantID}} {
		if len(field.raw) == 0 || string(field.raw) == "null" {
			continue
		}
		if err := json.Unmarshal(field.raw, field.dst); err == nil {
			continue
		}
		var number json.Number
		if err := json.Unmarshal(field.raw, &number); err != nil {
			return nil, fmt.Errorf("无效的 JWT 身份字段")
		}
		if _, err := strconv.ParseUint(number.String(), 10, 64); err != nil {
			return nil, fmt.Errorf("无效的 JWT 身份字段")
		}
		*field.dst = number.String()
	}
	return claims, nil
}

// splitJWT splits JWT into three parts
func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

// IsExpired checks whether the JWT has expired
func (m *JWTManager) IsExpired() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.expiry == 0 {
		return true
	}
	return time.Now().Unix() >= m.expiry
}

// StartRefreshLoop starts the JWT automatic refresh loop
func (m *JWTManager) StartRefreshLoop(ctx context.Context, cloudClient *cloud.Client, onExpired func()) {
	innerCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancelFunc = cancel
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-innerCtx.Done():
				return
			case <-ticker.C:
				m.checkAndRefresh(innerCtx, cloudClient, onExpired)
			}
		}
	}()
}

// checkAndRefresh Check and refresh JWT
func (m *JWTManager) checkAndRefresh(ctx context.Context, cloudClient *cloud.Client, onExpired func()) {
	m.mu.RLock()
	expiry := m.expiry
	m.mu.RUnlock()

	if expiry == 0 {
		return
	}

	// Refresh 60 seconds before expiration
	now := time.Now().Unix()
	if now < expiry-60 {
		return
	}

	token, err := m.GetToken()
	if err != nil || token == "" {
		if onExpired != nil {
			onExpired()
		}
		return
	}

	resp, err := cloudClient.RefreshToken(ctx, token)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		// Refresh failed, clear if expired
		if now >= expiry {
			m.Clear()
			if onExpired != nil {
				onExpired()
			}
		}
		return
	}

	expiresAt := resp.ExpiresAt
	if expiresAt <= 0 {
		if claims, parseErr := ParseClaims(resp.Token); parseErr == nil {
			expiresAt = claims.Exp
		}
	}

	// Atomic replacement
	if err := m.SaveToken(resp.Token, resp.RefreshToken, expiresAt); err != nil {
		//Clear if saving fails
		m.Clear()
		if onExpired != nil {
			onExpired()
		}
	}
}

// Stop stops the JWT refresh loop
func (m *JWTManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelFunc != nil {
		m.cancelFunc()
		m.cancelFunc = nil
	}
}

// Clear clears all JWT related data
func (m *JWTManager) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.expiry = 0
	if m.store == nil {
		return nil
	}

	if err := m.store.Delete(secrets.KeyJWT); err != nil {
		applog.Warn("[LocalAuth] 清除 JWT 失败", "error", err)
	}
	if err := m.store.Delete(secrets.KeyPrefix + "jwt_refresh"); err != nil {
		applog.Warn("[LocalAuth] 清除 JWT Refresh 失败", "error", err)
	}
	if err := m.store.Delete(secrets.KeyPrefix + "jwt_expires_at"); err != nil {
		applog.Warn("[LocalAuth] 清除 JWT 过期时间失败", "error", err)
	}
	return nil
}

// HasToken checks if there is already a JWT
func (m *JWTManager) HasToken() bool {
	token, err := m.GetToken()
	return err == nil && token != ""
}
