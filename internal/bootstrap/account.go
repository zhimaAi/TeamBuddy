package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/identity"
	"goteams-client/internal/localauth"
	"goteams-client/internal/localserver"
	"goteams-client/internal/secrets"
)

// AccountSession contains only the optional cloud-login lifetime state.
type AccountSession struct {
	AdminID     string
	UserID      string
	UserName    string
	DisplayName string
	DeviceMgr   *identity.DeviceManager
	JWTMgr      *localauth.JWTManager
	// CloudClient The actual cloud client used in this session: the official login is the default client,
	// Custom address login is an independent client built based on domain name.
	// The HTTP proxy layer (task/work item, etc.) must use it to avoid sending the JWT of the custom cloud to the official cloud.
	CloudClient *cloud.Client
	// ServerURL 登录目标云端地址（normalized），随登录索引写入。官方登录按域名
	// 隔离恢复；custom 登录地址由用户主动选择，不受当前配置约束。
	ServerURL string
	// ServerType 登录类型（official/custom），随会话保存，登出时用于决定是否清理 custom 登录索引。
	ServerType  string
	cancelFunc  context.CancelFunc
	credentials *secrets.SessionStore
}

// AccountManager manages the life cycle of account sessions
type AccountManager struct {
	transitionMu    sync.Mutex
	mu              sync.RWMutex
	current         *AccountSession
	cloudClient     *cloud.Client
	secretStore     *secrets.DelegatingStore
	bootstrapStore  secrets.Store // Boot storage (configDir), only carries account index last_login
	configDir       string
	localAPIBaseURL string
	localRuntime    *LocalRuntime
	// officialServerURL 当前配置的官方云端地址（normalized）。写入索引时按"最后
	// 登录"语义互斥清理：官方登录清除 custom 固定索引，custom 登录清除官方
	// per-address 索引，避免切换登录方式后重启恢复错误会话。
	officialServerURL string
}

// SetOfficialServerURL sets the normalized official cloud address of the current
// configuration, used by the last-login index rules in writeLastLogin/Logout.
func (m *AccountManager) SetOfficialServerURL(normalized string) {
	m.mu.Lock()
	m.officialServerURL = normalized
	m.mu.Unlock()
}

// accountLastLogin persists to the last login account index in the boot store (field is compatible with localserver.lastLoginInfo)
type accountLastLogin struct {
	AdminID     string `json:"admin_id"`
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	DisplayName string `json:"display_name"`
	// ServerType login target: official | custom. Select the cloud client accordingly when restarting and restoring.
	ServerType string `json:"server_type,omitempty"`
	// ServerURL Custom cloud address (after normalization); custom is filled in when logging in, official is empty.
	ServerURL         string `json:"server_url,omitempty"`
	CredentialVersion int    `json:"credential_version,omitempty"`
}

// NewAccountManager creates an account manager
func NewAccountManager(
	cloudClient *cloud.Client,
	secretStore *secrets.DelegatingStore,
	bootstrapStore secrets.Store,
	configDir string,
	localRuntime *LocalRuntime,
) *AccountManager {
	return &AccountManager{
		cloudClient:    cloudClient,
		secretStore:    secretStore,
		bootstrapStore: bootstrapStore,
		configDir:      configDir,
		localRuntime:   localRuntime,
	}
}

// SetLocalAPIBaseURL sets the loopback request address that the current process actually listens to.
func (m *AccountManager) SetLocalAPIBaseURL(baseURL string) {
	m.mu.Lock()
	m.localAPIBaseURL = baseURL
	m.mu.Unlock()
}

// Current returns the current account session (returns nil when not logged in)
func (m *AccountManager) Current() *AccountSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// IsLoggedIn whether logged in
func (m *AccountManager) IsLoggedIn() bool {
	return m.Current() != nil
}

// Login creates or switches account sessions
//
// token is the JWT returned for cloud login: non-empty means explicit login (passed in by the login processor); empty means
// Restore login state - at this time, the persisted JWT is read from the current Profile key storage and renewed to the cloud.
//
// opts supports custom cloud address login: use an independent CloudClient to request the address.
// Local databases and task execution are application-scoped and never change here.
func (m *AccountManager) Login(ctx context.Context, adminID, userID, userName, displayName, token string, opts localserver.LoginOptions) (*AccountSession, error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()

	// Select the cloud client. Local task storage is application-scoped and is
	// never selected, created, or migrated as part of login.
	cloudClient := m.cloudClient
	if opts.CloudClient != nil {
		cloudClient = opts.CloudClient
	}

	if m.localRuntime == nil || m.localRuntime.DB == nil {
		return nil, fmt.Errorf("本地任务运行时未初始化")
	}

	if cloudClient == nil || !cloudClient.IsConfigured() || adminID == "" || userID == "" {
		return nil, fmt.Errorf("云端账号信息不完整")
	}
	scope := localserver.AccountScope(cloudClient.BaseURL(), adminID, userID)
	accountStore := secrets.NewScopedStore(m.secretStore, scope)

	if token == "" {
		persisted, getErr := accountStore.Get(secrets.KeyJWT)
		if getErr != nil && !errors.Is(getErr, secrets.ErrNotFound) {
			return nil, getErr
		}
		// master 会重新写公共凭证及旧版索引。旧版索引存在时优先使用该凭证，
		// 避免切回旧版登录后仍拿以前迁移过的过时凭证恢复。
		if opts.AllowLegacyCredential {
			legacy, legacyErr := m.secretStore.Get(secrets.KeyJWT)
			if legacyErr != nil && !errors.Is(legacyErr, secrets.ErrNotFound) {
				return nil, legacyErr
			}
			if legacyErr == nil && legacy != "" {
				claims, claimsErr := localauth.ParseClaims(legacy)
				// 官方历史凭证包含不透明的单段令牌，无法在本地解析身份。
				// 仅旧索引允许将它交给该索引定位的原云端验证；下方必须核对刷新返回的账号。
				opaqueLegacy := !strings.Contains(legacy, ".")
				if opaqueLegacy || (claimsErr == nil && claims.AdminID == adminID && claims.UserID == userID) {
					persisted, getErr = legacy, nil
				} else {
					return nil, fmt.Errorf("无可用登录凭证（JWT 缺失且未提供）")
				}
			}
		}
		if getErr != nil || persisted == "" {
			return nil, fmt.Errorf("无可用登录凭证（JWT 缺失且未提供）")
		}
		refreshed, rErr := cloudClient.RefreshToken(ctx, persisted)
		if rErr != nil || refreshed == nil || refreshed.Token == "" {
			// 网络、存储及服务端异常保留凭证和索引，避免一次启动失败破坏升级恢复入口。
			var apiErr *cloud.APIError
			if ctx.Err() != nil || !errors.As(rErr, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
				// 不输出云端原始错误体，刷新响应可能包含凭据。
				return nil, fmt.Errorf("恢复登录态失败，保留登录信息供下次启动重试")
			}
			// 只有刷新接口明确拒绝认证时才清理本账号；旧公共 JWT 不影响其他账号。
			if dErr := accountStore.Delete(secrets.KeyJWT); dErr != nil {
				applog.Warn("[Account] 清除 JWT 失败", "error", dErr)
			}
			if dErr := m.bootstrapStore.Delete(localserver.LastLoginKey(opts.ServerURL)); dErr != nil {
				applog.Warn("[Account] 清除上次登录信息失败", "error", dErr)
			}
			if opts.ServerType == "custom" {
				if dErr := m.bootstrapStore.Delete(secrets.KeyLastLoginCustom); dErr != nil {
					applog.Warn("[Account] 清除自定义登录索引失败", "error", dErr)
				}
			}
			return nil, localserver.ErrLoginCredentialRejected
		}
		if refreshed.AdminID != adminID || refreshed.User.ID != userID {
			return nil, fmt.Errorf("云端恢复的账号与保存的账号不一致")
		}
		token = refreshed.Token
		if refreshed.User.Username != "" {
			userName = refreshed.User.Username
		}
		if refreshed.User.DisplayName != "" {
			displayName = refreshed.User.DisplayName
		}
	}
	// Persist once under the previous session's write barrier. A failed write
	// keeps that session alive, including when signing into the same account again.
	persist := func() error { return accountStore.Set(secrets.KeyJWT, token) }
	m.mu.RLock()
	previous := m.current
	m.mu.RUnlock()
	var persistErr error
	if previous != nil && previous.credentials != nil {
		persistErr = previous.credentials.RevokeAfter(persist)
	} else {
		persistErr = persist()
	}
	if persistErr != nil {
		return nil, fmt.Errorf("保存 JWT 失败: %w", persistErr)
	}
	m.closeSession()
	db := m.localRuntime.DB

	//Initialize JWT manager
	credentials := secrets.NewSessionStore(accountStore, token)
	jwtMgr := localauth.NewJWTManager(credentials)

	//Initialize device manager
	deviceMgr := identity.NewDeviceManager(m.configDir, db, credentials)

	//The account session must be independent of this HTTP login request.
	// Gin will cancel the request context after the request returns; long-term tasks are only explicitly canceled by Logout/application exit.
	sessionCtx, cancel := context.WithCancel(context.Background())
	cloudClient = cloudClient.ForAccount(sessionCtx, credentials, userID)

	session := &AccountSession{
		AdminID:     adminID,
		UserID:      userID,
		UserName:    userName,
		DisplayName: displayName,
		DeviceMgr:   deviceMgr,
		JWTMgr:      jwtMgr,
		CloudClient: cloudClient,
		ServerURL:   opts.ServerURL,
		ServerType:  opts.ServerType,
		cancelFunc:  cancel,
		credentials: credentials,
	}

	//Re-register the same device UUID every time a cloud account session is created.
	// The registration interface is idempotent, so there is no need to manually log out, log back in, or restart after the server rebuilds data or the credentials become invalid.
	if deviceUUID, deviceErr := deviceMgr.GetOrCreateUUID(); deviceErr != nil {
		applog.Warn("准备设备注册失败", "error", deviceErr)
	} else if deviceCred, registerErr := cloudClient.RegisterDevice(
		sessionCtx, deviceUUID, "",
	); registerErr != nil {
		applog.Warn("设备注册失败，后续请求可能无法通过设备认证", "error", registerErr)
	} else if saveErr := deviceMgr.SaveRegistration(
		adminID, deviceUUID, deviceCred.Credential,
	); saveErr != nil {
		applog.Warn("保存设备凭据失败，后续请求可能无法通过设备认证", "error", saveErr)
	}

	// The cloud token is a standard JWT, which is automatically refreshed before expiration driven by exp.
	if token, tokenErr := credentials.Get(secrets.KeyJWT); tokenErr == nil {
		if claims, claimsErr := localauth.ParseClaims(token); claimsErr == nil && claims.Exp > 0 {
			if saveErr := jwtMgr.SaveToken(token, "", claims.Exp); saveErr == nil {
				jwtMgr.StartRefreshLoop(sessionCtx, cloudClient, func() {
					applog.Warn("云端登录态已过期，请重新登录")
				})
			}
		}
	}

	// Persist the last login account index (written to boot storage, independent of the Profile key) for session restoration after restart.
	// Both official and custom logins are persistent: when the custom login is restored, rebuild the client according to the ServerURL and renew it to the cloud.
	if opts.PersistLastLogin {
		m.writeLastLogin(accountLastLogin{
			AdminID:           adminID,
			UserID:            userID,
			UserName:          userName,
			DisplayName:       displayName,
			ServerType:        opts.ServerType,
			ServerURL:         opts.ServerURL,
			CredentialVersion: 1,
		})
	}

	m.mu.Lock()
	m.current = session
	m.mu.Unlock()

	return session, nil
}

// LoginAndCreateSession Log in and create local service session information (implement localserver.AccountManagerInterface)
func (m *AccountManager) LoginAndCreateSession(ctx context.Context, adminID, userID, userName, displayName, token string, opts localserver.LoginOptions) (*localserver.SessionInfo, error) {
	session, err := m.Login(ctx, adminID, userID, userName, displayName, token, opts)
	if err != nil {
		return nil, err
	}
	return &localserver.SessionInfo{
		DeviceMgr:   session.DeviceMgr,
		JWTMgr:      session.JWTMgr,
		CloudClient: session.CloudClient,
		AdminID:     session.AdminID,
		UserID:      session.UserID,
		UserName:    session.UserName,
		DisplayName: session.DisplayName,
		ServerURL:   session.ServerURL,
		ServerType:  opts.ServerType,
	}, nil
}

// Close releases the resources of the current account session.
// Called when the program exits: it keeps the last login index
// so that the login state can be restored on the next startup.
func (m *AccountManager) Close() {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	m.closeSession()
}

func (m *AccountManager) closeSession() {
	m.mu.Lock()
	session := m.current
	m.current = nil
	m.mu.Unlock()

	if session == nil {
		return
	}

	// Revoke writes before a subsequent login can replace shared persisted keys.
	if session.credentials != nil {
		session.credentials.Close()
	}

	// Stop JWT refresh
	if session.JWTMgr != nil {
		session.JWTMgr.Stop()
	}

	// Cancel session context
	if session.cancelFunc != nil {
		session.cancelFunc()
	}

	// The DB, Orchestrator, WSHub and task services are application-owned and
	// intentionally continue running after cloud logout.
}

// Logout actively logs out the current account session.
// In addition to releasing resources, it clears the account index in the boot
// storage to avoid automatic recovery on the next startup.
// Only call it when the user actively logs out or switches accounts.
func (m *AccountManager) Logout() {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	m.logout()
}

func (m *AccountManager) logout() {
	m.mu.RLock()
	current := m.current
	serverURL := ""
	serverType := ""
	officialURL := m.officialServerURL
	if current != nil {
		serverURL = current.ServerURL
		serverType = current.ServerType
	}
	m.mu.RUnlock()

	m.closeSession()
	if current != nil && current.CloudClient != nil {
		store := secrets.NewScopedStore(m.secretStore, localserver.AccountScope(current.CloudClient.BaseURL(), current.AdminID, current.UserID))
		for _, key := range []string{secrets.KeyJWT, secrets.KeyDeviceCredential, secrets.KeyPrefix + "jwt_refresh", secrets.KeyPrefix + "jwt_expires_at"} {
			if err := store.Delete(key); err != nil {
				applog.Warn("[Account] 清除当前账号凭据失败", "error", err)
			}
		}
	}

	// Actively log out/switch accounts: clear the account index in the boot storage to avoid automatic recovery by mistake at next startup.
	// 清除当前会话索引、custom 固定索引与官方索引，登出后重启不恢复任何会话；
	// 共享目录下其他配置实例的索引不受影响。
	if m.bootstrapStore != nil {
		if err := m.bootstrapStore.Delete(secrets.KeyLastLogin); err != nil {
			applog.Warn("[Account] 清除旧版登录索引失败", "error", err)
		}
		if err := m.bootstrapStore.Delete(localserver.LastLoginKey(serverURL)); err != nil {
			applog.Warn("[Account] 清除上次登录信息失败", "error", err)
		}
		if serverType == "custom" {
			if err := m.bootstrapStore.Delete(secrets.KeyLastLoginCustom); err != nil {
				applog.Warn("[Account] 清除自定义登录索引失败", "error", err)
			}
		}
		if officialURL != "" {
			if err := m.bootstrapStore.Delete(localserver.LastLoginKey(officialURL)); err != nil {
				applog.Warn("[Account] 清除官方登录索引失败", "error", err)
			}
		}
	}
}

// writeLastLogin writes the last login account index into the boot storage (configDir), isolated from the account key.
// The index key is derived from the normalized cloud address (LastLoginKey), so different remote
// domains keep independent indexes and never overwrite each other's login state.
func (m *AccountManager) writeLastLogin(ll accountLastLogin) {
	data, err := json.Marshal(ll)
	if err != nil {
		applog.Warn("[Account] 序列化上次登录信息失败", "error", err)
		return
	}
	if err := m.bootstrapStore.Set(localserver.LastLoginKey(ll.ServerURL), string(data)); err != nil {
		applog.Warn("[Account] 写入上次登录信息失败", "error", err)
		return
	}
	if err := m.bootstrapStore.Delete(secrets.KeyLastLogin); err != nil {
		applog.Warn("[Account] 清除旧版登录索引失败", "error", err)
	}
	m.mu.RLock()
	officialURL := m.officialServerURL
	m.mu.RUnlock()
	if ll.ServerType == "custom" {
		// custom 登录的地址由用户主动选择，额外写固定索引供恢复定位；
		// 同时清除官方索引，保证重启恢复的是最后一次登录的会话。
		if err := m.bootstrapStore.Set(secrets.KeyLastLoginCustom, string(data)); err != nil {
			applog.Warn("[Account] 写入自定义登录索引失败", "error", err)
		}
		if officialURL != "" {
			if err := m.bootstrapStore.Delete(localserver.LastLoginKey(officialURL)); err != nil {
				applog.Warn("[Account] 清除官方登录索引失败", "error", err)
			}
		}
	} else if officialURL != "" {
		// 官方登录后，最后一次登录不再是 custom，清除其固定索引。
		if err := m.bootstrapStore.Delete(secrets.KeyLastLoginCustom); err != nil {
			applog.Warn("[Account] 清除自定义登录索引失败", "error", err)
		}
	}
}
