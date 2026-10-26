package localserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/config"
	"goteams-client/internal/i18n"
	"goteams-client/internal/secrets"
)

// sessionValidity Validity period of local browser session and login state (30 days)
const sessionValidity = 30 * 24 * time.Hour

// 无地址的旧版官方登录凭证只归属于此服务，不能随当前配置改变目标。
const legacyOfficialServerURL = "https://goteams.cn"

// ErrLoginCredentialRejected 仅表示云端刷新接口明确拒绝认证，供恢复流程精确清理索引。
var ErrLoginCredentialRejected = errors.New("云端登录态已失效，请重新登录")

// lastLoginInfo The account information of the last successful login, persisted to SecretStore to restore the session after restart.
type lastLoginInfo struct {
	indexKey    string // 实际读取的索引，清理时不猜测旧版索引的位置。
	AdminID     string `json:"admin_id"`
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar,omitempty"`
	Role        string `json:"role,omitempty"`
	// ServerType login target: official | custom. Used to select the correct cloud client after restarting.
	ServerType string `json:"server_type,omitempty"`
	// ServerURL Custom cloud address (after normalization); custom is filled in when logging in, official is empty.
	ServerURL         string `json:"server_url,omitempty"`
	CredentialVersion int    `json:"credential_version,omitempty"`
}

// sessionManager local browser session management
type sessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*browserSession
}

// browserSession local browser session
type browserSession struct {
	ID                string
	UserID            string
	UserName          string
	DisplayName       string
	Avatar            string
	Role              string
	AdminID           string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	AccountScope      string
	SessionGeneration string
}

var sessionMgr = &sessionManager{
	sessions: make(map[string]*browserSession),
}

// browserSessionsFile browser session (including cloud login user information) persistence file,
// Located in ConfigDir (~/.goteams/config/). Browser session cookies are valid for 30 days and survive
// On the browser side, the cookie is still valid after the process is restarted; after clearing sessionMgr.sessions in the memory, restart
// Old cookies carried by the browser can be remapped to the session, eliminating the need to log in again.
func (s *Server) browserSessionsFile() string {
	return filepath.Join(s.config.ConfigDir, "browser_sessions.json")
}

// persistBrowserSessions writes the current in-memory browser session to disk.
// The session ID is the Bearer token issued by the server, and the file is protected with 0600 permissions.
func (s *Server) persistBrowserSessions() {
	if s.config.ConfigDir == "" {
		return
	}
	sessionMgr.mu.RLock()
	data, err := json.Marshal(sessionMgr.sessions)
	sessionMgr.mu.RUnlock()
	if err != nil {
		applog.Warn("[LocalServer] 序列化浏览器会话失败", "error", err)
		return
	}
	if err := os.WriteFile(s.browserSessionsFile(), data, 0600); err != nil {
		applog.Warn("[LocalServer] 持久化浏览器会话失败", "error", err)
	}
}

// reconcileBrowserSessions 把内存中「没有用户信息」的浏览器会话补全为当前云端账号。
// 这类会话产生于启动早期：Electron 用 ticket 打开窗口时 s.lastLogin 还没恢复，
// handleBrowserTicket 只能建出空会话并被持久化；它的 Cookie 有效期 30 天，
// 期间用它创建关联线上的任务就会把 cloud_user_id 写成空值（线上永远收不到沟通记录）。
func (s *Server) reconcileBrowserSessions() {
	client := s.currentCloudClient()
	if client == nil || client.UserID() == "" {
		return
	}
	userID := client.UserID()
	adminID := ""
	if s.lastLogin != nil {
		adminID = strings.TrimSpace(s.lastLogin.AdminID)
	}
	changed := false
	sessionMgr.mu.Lock()
	for _, sess := range sessionMgr.sessions {
		if sess == nil || strings.TrimSpace(sess.UserID) != "" {
			continue
		}
		sess.UserID = userID
		if sess.AdminID == "" && adminID != "" {
			sess.AdminID = adminID
		}
		changed = true
	}
	sessionMgr.mu.Unlock()
	if !changed {
		return
	}
	applog.Info("[LocalServer] 已补全浏览器会话的用户信息", "user_id", userID)
	s.persistBrowserSessions()
}

// loadBrowserSessions Restore browser sessions from disk on startup (automatically skipping expired entries).
func (s *Server) loadBrowserSessions() {
	if s.config.ConfigDir == "" {
		return
	}
	data, err := os.ReadFile(s.browserSessionsFile())
	if err != nil {
		if !os.IsNotExist(err) {
			applog.Warn("[LocalServer] 读取浏览器会话失败", "error", err)
		}
		return
	}
	var loaded map[string]*browserSession
	if err := json.Unmarshal(data, &loaded); err != nil {
		applog.Warn("[LocalServer] 解析浏览器会话失败", "error", err)
		return
	}
	now := time.Now()
	count := 0
	sessionMgr.mu.Lock()
	for id, sess := range loaded {
		if sess == nil || now.After(sess.ExpiresAt) {
			continue
		}
		sessionMgr.sessions[id] = sess
		count++
	}
	sessionMgr.mu.Unlock()
	if count > 0 {
		applog.Info("[LocalServer] 已从磁盘恢复浏览器会话", "count", count)
	}
}

// handleBrowserTicket browser Ticket verification, create a local Session after passing the verification
func (s *Server) handleBrowserTicket(c *gin.Context) {
	ticket := c.Query("ticket")
	if ticket == "" {
		i18n.Error(c, http.StatusBadRequest, "auth_ticket_required", "ticket_required")
		return
	}

	// Consume the launch ticket atomically so it cannot be replayed by another
	// process after the desktop window has established its session.
	if !s.consumeBrowserTicket(ticket) {
		i18n.Error(c, http.StatusForbidden, "auth_ticket_invalid", "ticket_invalid")
		return
	}

	// Ticket verification passed, create a local browser session
	sessionID := generateSessionID()
	session := &browserSession{
		ID:        sessionID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(sessionValidity),
	}

	// If the server has restored the cloud account session (retaining the login status after restarting), reuse its user information,
	// Make the browser in the "cloud logged in" state without having to log in again.
	if s.lastLogin != nil {
		session.AdminID = s.lastLogin.AdminID
		session.UserID = s.lastLogin.UserID
		session.UserName = s.lastLogin.UserName
		session.DisplayName = s.lastLogin.DisplayName
		session.Avatar = s.lastLogin.Avatar
		session.Role = s.lastLogin.Role
		if cur := s.currentSession(); cur != nil && cur.CloudClient != nil {
			session.AccountScope = AccountScope(cur.ServerURL, cur.AdminID, cur.UserID)
			session.SessionGeneration = cur.CloudClient.SessionScope()
		}
	}
	// lastLogin 是异步恢复的：窗口打开得早时它还是 nil，这里必须退回云客户端取账号 ID，
	// 否则会落下一个没有用户信息的会话，后续用它创建的任务 cloud_user_id 会是空值。
	if strings.TrimSpace(session.UserID) == "" {
		if client := s.currentCloudClient(); client != nil && client.UserID() != "" {
			session.UserID = client.UserID()
			if session.AdminID == "" && s.lastLogin != nil {
				session.AdminID = s.lastLogin.AdminID
			}
		}
	}

	sessionMgr.mu.Lock()
	sessionMgr.sessions[sessionID] = session
	sessionMgr.mu.Unlock()
	s.persistBrowserSessions()

	//Set HttpOnly Cookie (valid for 30 days)
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("goteams_local_session", sessionID, int(sessionValidity.Seconds()), "/", "", false, true)

	// Redirect to the front-end home page. In development mode, this request enters through the Vite proxy, so the relative address will still
	// Return to localhost:5173; in publishing mode, return to the home page of the backend hosting.
	c.Redirect(http.StatusFound, "/")
}

func (s *Server) consumeBrowserTicket(candidate string) bool {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	if s.ticket == "" || len(candidate) != len(s.ticket) ||
		subtle.ConstantTimeCompare([]byte(candidate), []byte(s.ticket)) != 1 {
		return false
	}
	s.ticket = ""
	return true
}

func (s *Server) handleDesktopShutdown(c *gin.Context) {
	if !isLoopbackRequest(c.Request.RemoteAddr) {
		i18n.Error(c, http.StatusForbidden, "auth_desktop_loopback_required", "desktop_loopback_required")
		return
	}
	token := c.GetHeader("X-GoTeams-Desktop-Token")
	expected := s.config.DesktopToken
	if expected == "" || len(token) != len(expected) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		i18n.Error(c, http.StatusForbidden, "auth_desktop_token_invalid", "desktop_token_invalid")
		return
	}
	c.Status(http.StatusAccepted)
	go s.config.Shutdown()
}

// handleLogin creates only an optional cloud session. Local task and base data
// remain in the fixed application databases for official and custom cloud login.
func (s *Server) handleLogin(c *gin.Context) {
	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		ServerType string `json:"server_type"` // official | custom
		ServerURL  string `json:"server_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.Username == "" || body.Password == "" {
		i18n.Error(c, http.StatusBadRequest, "auth_credentials_required", "credentials_required")
		return
	}

	serverType := body.ServerType
	if serverType == "" {
		serverType = "official"
	}

	s.authTransitionMu.RLock()
	client, normalized, key := s.resolveLoginTarget(serverType, body.ServerURL)
	s.authTransitionMu.RUnlock()
	if key != "" {
		i18n.Error(c, http.StatusBadRequest, key, strings.TrimPrefix(strings.TrimPrefix(key, "auth_"), "common_"))
		return
	}

	// 2. Call the cloud to log in (any account is acceptable, as long as the password is correct)
	loginResp, err := client.Login(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": i18n.T(c, "auth_login_failed"), "code": "login_failed"})
		return
	}

	// 3-6. 云端已确认身份，完成本地登录的全部副作用。
	deviceRegistered, err := s.completeCloudLogin(c, client, serverType, normalized, loginResp)
	if err != nil {
		writeLoginStageError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"user":              loginResp.User,
		"device_registered": deviceRegistered,
		"admin_id":          loginResp.AdminID,
	})
}

// 登录链路中可区分的失败阶段。账号密码登录与浏览器登录共用 completeCloudLogin，
// 但各自把阶段映射成自己的错误文案。
var (
	errLoginProfileSwitchFailed = errors.New("login_profile_switch_failed")
	errLoginAccountManagerGone  = errors.New("login_account_manager_missing")
	errLoginSessionCreateFailed = errors.New("login_session_create_failed")
)

// writeLoginStageError 把 completeCloudLogin 的阶段错误映射成稳定的本地 API 错误响应。
func writeLoginStageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errLoginProfileSwitchFailed):
		i18n.Error(c, http.StatusInternalServerError, "auth_profile_switch_failed", "profile_switch_failed")
	case errors.Is(err, errLoginAccountManagerGone):
		i18n.Error(c, http.StatusInternalServerError, "auth_account_manager_missing", "account_manager_missing")
	default:
		i18n.Error(c, http.StatusInternalServerError, "auth_session_create_failed", "session_create_failed")
	}
}

// completeCloudLogin 在云端已经确认身份之后完成本地登录：切换 Profile、建立账号会话、
// 同步云端流水线缓存、注册设备并下发浏览器会话 Cookie。
//
// 账号密码登录（handleLogin）与浏览器登录（handleBrowserLoginCallback）共用这一段，
// 保证两条链路产生的本地登录态完全一致。client 必须是签发该登录态的云端客户端，
// serverType/serverURL 用于重启后按同一目标恢复会话。
func (s *Server) completeCloudLogin(
	c *gin.Context,
	client *cloud.Client,
	serverType, serverURL string,
	loginResp *cloud.LoginResponse,
) (bool, error) {
	s.authTransitionMu.Lock()
	defer s.authTransitionMu.Unlock()
	registered, err := s.completeCloudLoginLocked(c, client, serverType, serverURL, loginResp)
	if err == nil {
		s.browserLogins.invalidate()
	}
	return registered, err
}

func (s *Server) completeCloudLoginLocked(c *gin.Context, client *cloud.Client, serverType, serverURL string, loginResp *cloud.LoginResponse) (bool, error) {
	// 本地 Profile 与凭据文件始终是应用级的，不随账号或云端地址拆分。
	profileDir := s.config.BaseProfileDir
	if profileDir == "" {
		profileDir = s.config.DataDir
	}
	if err := s.switchProfile(c.Request.Context(), profileDir, profileDir); err != nil {
		return false, errLoginProfileSwitchFailed
	}
	if s.config.AccountMgr == nil {
		return false, errLoginAccountManagerGone
	}

	if mgr, ok := s.config.AccountMgr.(interface{ SetOfficialServerURL(string) }); ok {
		mgr.SetOfficialServerURL(s.currentConfiguredServerURL())
	}
	opts := LoginOptions{
		PersistLastLogin: true, // 两种登录都持久化最后登录索引，重启后自动恢复会话
		ServerType:       serverType,
		ServerURL:        serverURL,
	}
	opts.CloudClient = client

	sessionInfo, err := s.config.AccountMgr.LoginAndCreateSession(
		c.Request.Context(),
		loginResp.AdminID,
		loginResp.User.ID,
		loginResp.User.Username,
		loginResp.User.DisplayName,
		loginResp.Token,
		opts,
	)
	if err != nil {
		return false, errLoginSessionCreateFailed
	}

	// 4. 设置服务端当前会话，并刷新只读云流水线缓存（云端不可用不得让登录失败）。
	s.SetSession(sessionInfo)
	s.triggerCloudPipelineSync()
	s.triggerCloudExpertGroupSync()
	// 登录后立刻回填本环境历史任务的账号归属，否则归属为空的任务永远不上报。
	s.reconcileBrowserSessions()
	s.triggerTaskOwnerBackfill()

	// 5. 设备注册检查。此时服务端已经处于登录态，任何提前返回都会造成
	// 「服务端已登录、浏览器没拿到 Cookie」的割裂状态，因此设备相关失败一律降级：
	// 登录照常完成并下发 Cookie，用户可在设置页重试设备注册。
	deviceRegistered := false
	if sessionInfo.DeviceMgr != nil {
		deviceRegistered = sessionInfo.DeviceMgr.IsRegistered()
		if !deviceRegistered {
			deviceUUID, err := sessionInfo.DeviceMgr.GetOrCreateUUID()
			if err != nil {
				applog.Warn("生成设备 UUID 失败，登录继续但设备未注册",
					"admin_id", loginResp.AdminID, "error", err.Error())
			} else {
				// 必须使用会话云客户端：自定义登录时注册目标是自定义云端。
				registerClient := s.config.CloudClient
				if sessionInfo.CloudClient != nil {
					registerClient = sessionInfo.CloudClient
				}
				deviceCred, err := registerClient.RegisterDevice(c.Request.Context(), deviceUUID, "")
				if err != nil {
					applog.Warn("设备注册失败，登录继续",
						"admin_id", loginResp.AdminID, "error", err.Error())
					deviceRegistered = false
				} else if err := sessionInfo.DeviceMgr.SaveRegistration(loginResp.AdminID, deviceUUID, deviceCred.Credential); err != nil {
					applog.Warn("保存设备注册信息失败，登录继续",
						"admin_id", loginResp.AdminID, "error", err.Error())
					deviceRegistered = false
				} else {
					deviceRegistered = true
				}
			}
		}
	}

	// 6. 创建本地浏览器会话并下发 HttpOnly Cookie（有效期 30 天）。
	sessionID := generateSessionID()
	browserSess := &browserSession{
		ID:                sessionID,
		UserID:            loginResp.User.ID,
		UserName:          loginResp.User.Username,
		DisplayName:       loginResp.User.DisplayName,
		Avatar:            loginResp.User.Avatar,
		Role:              loginResp.User.Role,
		AdminID:           loginResp.AdminID,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(sessionValidity),
		AccountScope:      AccountScope(serverURL, loginResp.AdminID, loginResp.User.ID),
		SessionGeneration: sessionInfo.CloudClient.SessionScope(),
	}

	s.lastLogin = &lastLoginInfo{
		AdminID:     loginResp.AdminID,
		UserID:      loginResp.User.ID,
		UserName:    loginResp.User.Username,
		DisplayName: loginResp.User.DisplayName,
		Avatar:      loginResp.User.Avatar,
		Role:        loginResp.User.Role,
		ServerType:  serverType,
		ServerURL:   serverURL,
	}

	sessionMgr.mu.Lock()
	sessionMgr.sessions[sessionID] = browserSess
	sessionMgr.mu.Unlock()
	s.persistBrowserSessions()

	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("goteams_local_session", sessionID, int(sessionValidity.Seconds()), "/", "", false, true)

	return deviceRegistered, nil
}

// handleLogout log out
func (s *Server) handleLogout(c *gin.Context) {
	s.authTransitionMu.Lock()
	defer s.authTransitionMu.Unlock()
	s.browserLogins.invalidate()
	//Clear browser session
	sessionID, err := c.Cookie("goteams_local_session")
	if err == nil && sessionID != "" {
		sessionMgr.mu.Lock()
		delete(sessionMgr.sessions, sessionID)
		sessionMgr.mu.Unlock()
	}
	// 退出云端后仍保留独立的本地访问能力，旧 Cookie 不再有效。
	localSession := &browserSession{ID: generateSessionID(), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(sessionValidity)}
	sessionMgr.mu.Lock()
	sessionMgr.sessions[localSession.ID] = localSession
	sessionMgr.mu.Unlock()
	s.persistBrowserSessions()
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("goteams_local_session", localSession.ID, int(sessionValidity.Seconds()), "/", "", false, true)

	//Clear account session (close database, etc.)
	s.ClearSession()
	if s.config.AccountMgr != nil {
		s.config.AccountMgr.Logout()
	}

	s.lastLogin = nil

	// Return to the universal base Profile after logging out (the local function data is restored to the universal library, and the key backend is switched back to the universal directory)
	if err := s.switchProfile(c.Request.Context(), s.config.BaseProfileDir, s.config.BaseProfileDir); err != nil {
		applog.Warn("[LocalServer] 登出后切回 base Profile 失败", "error", err)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// handleSessionStatus returns the current HttpOnly browser session status for restoring the UI when the page starts.
// It does not return JWTs, device credentials, or other Secrets.
// The local loopback service is trusted: local_authenticated is always true, and the cloud login state is
// determined by the server-side account session (restored from the persistent login state on startup).
func (s *Server) handleSessionStatus(c *gin.Context) {
	cur := s.currentSession()
	if cur != nil && !s.sessionMatchesConfig(cur) {
		// 官方会话域名与当前配置不一致（共享本地数据目录的其他实例登录写入），
		// 视为未登录，避免不同远程域名之间串用登录态；custom 会话由
		// sessionMatchesConfig 放行，不在此列。
		if cur != nil && s.config.AccountMgr != nil {
			s.config.AccountMgr.Logout()
		}
		s.ClearSession()
		cur = nil
	}
	cloudLoggedIn := cur != nil
	accountScope := ""
	if cur != nil && cur.CloudClient != nil {
		accountScope = AccountScope(cur.CloudClient.BaseURL(), cur.AdminID, cur.UserID)
	}
	var user interface{}
	if cloudLoggedIn {
		// 浏览器 session 档案更完整（头像、角色等）；缺失时回退账号会话，
		// 保证即使没有浏览器 cookie，前端也能拿到可用的登录状态。
		userID, userName, displayName, avatar, role, adminID := "", "", "", "", "", ""
		if session, ok := s.browserSessionFromCookie(c); ok {
			userID = session.UserID
			userName = session.UserName
			displayName = session.DisplayName
			avatar = session.Avatar
			role = session.Role
			adminID = session.AdminID
		}
		if userID == "" {
			userID = cur.UserID
		}
		if userName == "" {
			userName = cur.UserName
		}
		if displayName == "" {
			displayName = cur.DisplayName
		}
		if displayName == "" {
			displayName = userName
		}
		if adminID == "" {
			adminID = cur.AdminID
		}
		user = gin.H{
			"id":           userID,
			"username":     userName,
			"display_name": displayName,
			"avatar":       avatar,
			"role":         role,
			"admin_id":     adminID,
		}
		applog.Info("会话状态响应-已登录",
			"user_id", userID,
			"user_name", userName,
		)
	}
	c.JSON(http.StatusOK, gin.H{
		"local_authenticated": true,
		"cloud_logged_in":     cloudLoggedIn,
		"session_generation":  activeSessionGeneration(cur),
		"account_scope":       accountScope,
		"server_url":          activeServerURL(cur),
		"server_type":         activeServerType(cur),
		"user":                user,
		// 无 Electron 注入（纯 Go 运行/浏览器直连 dev）时，前端需要从本接口
		// 获取本次启动的 API token 用于后续请求；Electron 模式经 bridge 提供，不返回。
		"api_token": apiTokenForSession(s),
	})
	applog.Info("会话状态响应",
		"cloud_logged_in", cloudLoggedIn,
	)
}

// apiTokenForSession 仅在 token 非由 Electron 注入时返回，避免 token 出现在
// Electron 模式的握手响应中（该模式下前端通过 bridge 获取）。
func apiTokenForSession(s *Server) string {
	if s.config.APITokenFromEnv {
		return ""
	}
	return s.config.APIToken
}

func (s *Server) browserSessionFromCookie(c *gin.Context) (*browserSession, bool) {
	sessionID, err := c.Cookie("goteams_local_session")
	if err != nil || sessionID == "" {
		return nil, false
	}
	sessionMgr.mu.RLock()
	session, ok := sessionMgr.sessions[sessionID]
	sessionMgr.mu.RUnlock()
	cur := s.currentSession()
	if !ok || session == nil || time.Now().After(session.ExpiresAt) {
		if ok {
			sessionMgr.mu.Lock()
			delete(sessionMgr.sessions, sessionID)
			sessionMgr.mu.Unlock()
			s.persistBrowserSessions()
		}
		return nil, false
	}
	// 本地访问会话独立于云端身份。匿名或旧账号 Cookie 仍可用于本地 WS，
	// 但不得向业务处理器提供旧账号身份。
	if cur == nil || cur.CloudClient == nil || session.AccountScope == "" ||
		session.SessionGeneration != cur.CloudClient.SessionScope() ||
		session.AccountScope != AccountScope(cur.ServerURL, cur.AdminID, cur.UserID) {
		return nil, false
	}
	return session, true
}

// validateSession 中间件：本地 loopback 服务可信，不要求浏览器 session cookie。
// ticket 偶发失败不应导致前端误判未登录；有 cookie 时优先映射浏览器 session，
// 缺失时回退账号会话（云登录态），保证本地功能能拿到用户信息。
// 回退前校验会话域名与当前配置一致（仅官方会话），防止共享数据目录时串用其他实例的登录态。
func (s *Server) validateSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Auth mutations own their boundary; application-only routes do not consume a cloud identity.
		if mutatesAccount(c.Request.URL.Path) || accountIndependentRequest(c.Request.URL.Path) {
			c.Next()
			return
		}
		if session, ok := s.browserSessionFromCookie(c); ok {
			c.Set("session", session)
		} else if cur := s.currentSession(); s.sessionMatchesConfig(cur) {
			c.Set("session", &browserSession{
				ID:          "account",
				UserID:      cur.UserID,
				UserName:    cur.UserName,
				DisplayName: cur.DisplayName,
				AdminID:     cur.AdminID,
				CreatedAt:   time.Now(),
				ExpiresAt:   time.Now().Add(sessionValidity),
			})
		}
		c.Next()
	}
}

func isLoopbackRequest(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RestoreSession restores the account session from the persistent account index (bootstrap storage) + JWT in the Profile library,
// so the user does not need to log in again after restarting the program. Official
// uses the default client; custom rebuilds a client from the persisted ServerURL.
//
// Domain consistency only constrains official logins: the session domain must match
// the cloud address of the current configuration (http/https are considered the same
// domain), so instances that share the local data directory but point to different
// remote domains never take over each other's official login state. Custom logins
// use the address chosen by the user and are restored regardless of the configured
// domain.
//
// The cloud validity verification and renewal of JWT are completed internally by Login.
func (s *Server) RestoreSession(ctx context.Context) {
	s.authTransitionMu.Lock()
	defer s.authTransitionMu.Unlock()
	if s.config.AccountMgr == nil || s.config.BootstrapStore == nil {
		return
	}

	// The account index is persisted in the boot storage (configDir) and is isolated from the Profile key.
	// 自部署连接从其固定索引恢复，不依赖官方默认地址。官方索引仍按配置地址定位。
	configured := s.currentConfiguredServerURL()

	// 告知账号管理器官方地址：索引写入按"最后登录"语义互斥清理（官方登录
	// 清除 custom 固定索引、custom 登录清除官方索引）。
	if mgr, ok := s.config.AccountMgr.(interface{ SetOfficialServerURL(string) }); ok {
		mgr.SetOfficialServerURL(configured)
	}

	// 先读取用户最后选择的自部署连接，再读取当前配置的官方索引，最后兼容旧版索引。
	configuredRaw := ""
	if s.config.CloudConfig != nil {
		configuredRaw = s.config.CloudConfig.APIBaseURL
	}
	last := readLastLogin(s.config.BootstrapStore, configuredRaw)
	if last == nil || last.AdminID == "" {
		return
	}
	if last.ServerType == "" {
		last.ServerType = "official"
	}
	last.ServerURL = strings.TrimSpace(last.ServerURL)
	if last.ServerURL == "" {
		// 仅兼容未升级的官方索引。新版或自部署索引缺少地址时不能猜测目标。
		if last.CredentialVersion != 0 || last.ServerType != "official" {
			return
		}
		last.ServerURL = legacyOfficialServerURL
	}

	// 域名一致性只约束官方登录：会话域名必须与当前配置的云端地址一致，防止
	// 共享本地数据目录的多个实例互相串用登录态。custom 登录的地址由用户主动
	// 选择，不受内置配置约束；无地址的旧版官方索引同样按固定官方地址检查。
	if last.ServerType != "custom" && !sameServerDomain(last.ServerURL, configured) {
		applog.Info("[LocalServer] 上次登录云端地址与当前配置不一致，跳过登录态恢复",
			"last_server", last.ServerURL,
			"configured", configured)
		return
	}

	normalized, normalizeErr := normalizeServerURL(last.ServerURL)
	if normalizeErr != nil {
		return
	}

	// 两类连接均按已保存的目标地址创建无凭据客户端，刷新时只显式传入该账号 JWT。
	client := s.customCloudClient(normalized)

	opts := LoginOptions{}
	profileDir := s.config.BaseProfileDir
	if profileDir == "" {
		profileDir = s.config.DataDir
	}
	secretDir := profileDir
	if normalized != "" {
		opts = LoginOptions{
			CloudClient:           client,
			PersistLastLogin:      true,
			ServerType:            last.ServerType,
			ServerURL:             normalized,
			AllowLegacyCredential: last.CredentialVersion == 0,
		}
	}

	// First switch to the corresponding Profile (general base.db + key backend by target), and then resume the session:
	// Pass the token empty, Login reads the persistent JWT from the current Profile secrets and renews it to the corresponding cloud.
	if err := s.switchProfile(ctx, profileDir, secretDir); err != nil {
		applog.Warn("恢复登录态失败：切换 Profile 失败", "error", err)
		return
	}
	sessionInfo, err := s.config.AccountMgr.LoginAndCreateSession(ctx, last.AdminID, last.UserID, last.UserName, last.DisplayName, "", opts)
	if err != nil {
		applog.Warn("恢复登录态失败", "error", err)
		// 仅明确认证失效才删除实际读取的索引；缺凭证、身份不匹配或临时异常均保留。
		if errors.Is(err, ErrLoginCredentialRejected) && last.indexKey != "" {
			if dErr := s.config.BootstrapStore.Delete(last.indexKey); dErr != nil {
				applog.Warn("[LocalServer] 清除旧版登录索引失败", "error", dErr)
			}
		}
		return
	}
	s.removeMigratedLoginIndex(last, normalized)
	s.SetSession(sessionInfo)
	s.triggerCloudPipelineSync()
	s.triggerCloudExpertGroupSync()
	// 登录后立刻回填本环境历史任务的账号归属，否则归属为空的任务永远不上报。
	s.reconcileBrowserSessions()
	s.triggerTaskOwnerBackfill()
	s.lastLogin = last
	// GetMe must use the session cloud client: user information comes from the custom cloud during custom login.
	if client := s.currentCloudClient(); client != nil {
		user, profileErr := client.GetMe(ctx)
		if profileErr == nil {
			// Only overwrite with non-empty values so that a temporarily incomplete
			// /api/client/me response cannot wipe out the persisted profile.
			if user.ID != "" {
				s.lastLogin.UserID = user.ID
			}
			if user.Username != "" {
				s.lastLogin.UserName = user.Username
			}
			if user.DisplayName != "" {
				s.lastLogin.DisplayName = user.DisplayName
				sessionInfo.DisplayName = user.DisplayName
			}
			if user.Avatar != "" {
				s.lastLogin.Avatar = user.Avatar
			}
			if user.Role != "" {
				s.lastLogin.Role = user.Role
			}
		}
	}
	applog.Info("已从持久化登录态恢复账号会话",
		"admin", last.AdminID, "user", last.UserName, "server_type", last.ServerType)
}

// readLastLogin reads the last login information from the boot storage.
// The official index is stored per server address (LastLoginKey), so different
// remote domains never share or overwrite each other's login state. The
// fixed custom index (KeyLastLoginCustom) is tried first because it records
// the user-selected server independently of configuration, followed by the
// canonical and original-case official indexes, and finally the legacy single index
// (goteams:last_login) written by older versions.
func readLastLogin(store secrets.Store, serverURL string) *lastLoginInfo {
	if store == nil {
		return nil
	}
	keys := []string{secrets.KeyLastLoginCustom}
	if normalized, err := normalizeServerURL(serverURL); err == nil {
		canonicalKey := LastLoginKey(normalized)
		keys = append(keys, canonicalKey)
		// master 的索引保留域名大小写，仅兼容原配置地址，不枚举或猜测其他账号。
		legacyURL := strings.TrimSpace(serverURL)
		if !strings.Contains(legacyURL, "://") {
			legacyURL = "https://" + legacyURL
		}
		if parsed, err := url.Parse(legacyURL); err == nil {
			legacyKey := LastLoginKey(strings.TrimRight(parsed.String(), "/"))
			if legacyKey != canonicalKey {
				keys = append(keys, legacyKey)
			}
		}
	}
	keys = append(keys, secrets.KeyLastLogin)
	for _, key := range keys {
		data, err := store.Get(key)
		if errors.Is(err, secrets.ErrNotFound) || (err == nil && data == "") {
			continue
		}
		// 读取异常时不回退到另一个账号；保留最后登录的选择供下次重试。
		if err != nil {
			return nil
		}
		var info lastLoginInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			return nil
		}
		info.indexKey = key
		return &info
	}
	return nil
}

// Delete only the index actually migrated, after verified login and successful
// persistence of the same account under the canonical key. Failures preserve it.
func (s *Server) removeMigratedLoginIndex(last *lastLoginInfo, serverURL string) {
	canonicalKey := LastLoginKey(serverURL)
	if last.indexKey == "" || last.indexKey == canonicalKey || last.indexKey == secrets.KeyLastLoginCustom {
		return
	}
	data, err := s.config.BootstrapStore.Get(canonicalKey)
	if err != nil {
		return
	}
	var saved lastLoginInfo
	if json.Unmarshal([]byte(data), &saved) != nil || saved.CredentialVersion != 1 ||
		saved.AdminID != last.AdminID || saved.UserID != last.UserID || saved.ServerURL != serverURL {
		return
	}
	if err := s.config.BootstrapStore.Delete(last.indexKey); err != nil {
		applog.Warn("[LocalServer] 清除已迁移登录索引失败", "error", err)
	}
}

// LastLoginKey derives the boot-storage key of the last-login index from the
// normalized cloud address, so login states of different remote domains are
// isolated even when they share the local data directory. Empty address keeps
// the legacy single key for backward compatibility.
func LastLoginKey(serverURL string) string {
	if serverURL == "" {
		return secrets.KeyLastLogin
	}
	return secrets.KeyLastLogin + "." + ServerProfileKey(serverURL)
}

// currentConfiguredServerURL returns the normalized cloud address of the current
// configuration (empty when no official address is configured).
func (s *Server) currentConfiguredServerURL() string {
	if s.config.CloudConfig == nil || s.config.CloudConfig.APIBaseURL == "" {
		return ""
	}
	norm, err := normalizeServerURL(s.config.CloudConfig.APIBaseURL)
	if err != nil {
		return ""
	}
	return norm
}

// sessionMatchesConfig reports whether the account session belongs to the cloud
// address of the current configuration. The official session domain must match the
// configured api_base_url (http/https are considered the same domain); a session
// without a server address or with a mismatched domain is treated as a
// cross-domain takeover and never kept. Custom sessions are exempt: the address
// was chosen by the user, so the session itself is the target and is never
// cleared by the configured domain.
func (s *Server) sessionMatchesConfig(sess *SessionInfo) bool {
	if sess == nil {
		return false
	}
	if sess.ServerType == "custom" {
		return sess.ServerURL != ""
	}
	configured := s.currentConfiguredServerURL()
	if configured == "" {
		return false
	}
	return sameServerDomain(sess.ServerURL, configured)
}

// sameServerDomain reports whether the two cloud addresses point to the same
// server host. The protocol (http/https) and the default port are ignored, so
// http://a.example.com and https://a.example.com are considered the same domain.
func sameServerDomain(a, b string) bool {
	da, err := serverDomainKey(a)
	if err != nil {
		return false
	}
	db, err := serverDomainKey(b)
	if err != nil {
		return false
	}
	return da == db
}

// serverDomainKey derives a comparable identity from a cloud address: lowercased
// host plus explicit non-default port. The protocol itself is excluded, and a
// port equal to the scheme default (http:80/https:443) is ignored, so
// http://a.example.com and https://a.example.com are the same identity.
func serverDomainKey(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("地址不能为空")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("无法解析地址: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			if port == "80" {
				port = ""
			}
		case "https":
			if port == "443" {
				port = ""
			}
		}
	}
	if port == "" {
		return host, nil
	}
	return net.JoinHostPort(host, port), nil
}

// generateSessionID generates a random session ID
func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// normalizeServerURL normalizes the custom cloud address: complete the protocol and remove the trailing slash.
func normalizeServerURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("地址不能为空")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("无法解析地址: %w", err)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("缺少主机名")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(value) > 2048 {
		return "", fmt.Errorf("服务地址不能包含凭据、查询参数或片段")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("仅支持 http/https 协议")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// customCloudClient constructs a temporary cloud client based on the custom cloud address (keeps the client version number).
func (s *Server) customCloudClient(baseURL string) *cloud.Client {
	version := ""
	if s.config.CloudConfig != nil {
		version = s.config.CloudConfig.ClientVersion
	}
	cfg := config.NewCloudConfig(baseURL, version, "")
	if s.config.CloudConfig != nil {
		cfg.ListenAddr = s.config.CloudConfig.ListenAddr
	}
	return cloud.NewClient(cfg, nil)
}

// ServerProfileKey assigns a stable and file system-safe subdirectory key from the standardized cloud address.
// Used to isolate the local Profile and account data of each cloud address into a separate directory.
// The http/https protocol is excluded from the derivation (only host + path participate), so the same
// host always maps to the same directory regardless of which protocol is used.
// Take the first 12 hexadecimal digits of SHA-256 of the address, with negligible collision probability and no special characters.
func ServerProfileKey(normalizedURL string) string {
	key := normalizedURL
	if u, err := url.Parse(normalizedURL); err == nil && u.Host != "" {
		key = u.Host + u.Path
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:12]
}

// AccountScope is the shared non-secret identity used by credentials, sessions and UI caches.
func AccountScope(serverURL, adminID, userID string) string {
	return ServerProfileKey(serverURL) + ":" + adminID + ":" + userID
}
func activeServerURL(s *SessionInfo) string {
	if s == nil {
		return ""
	}
	return s.ServerURL
}
func activeServerType(s *SessionInfo) string {
	if s == nil {
		return ""
	}
	return s.ServerType
}

func activeSessionGeneration(s *SessionInfo) string {
	if s == nil || s.CloudClient == nil {
		return ""
	}
	return s.CloudClient.SessionScope()
}

// resolveLoginTarget treats official as a preset URL, never as a second business
// client. Snapshot the address before authentication so later config edits cannot
// redirect ticket exchange or persist credentials under the wrong server.
func (s *Server) resolveLoginTarget(serverType, rawURL string) (*cloud.Client, string, string) {
	switch serverType {
	case "official":
		if s.config.CloudConfig == nil || strings.TrimSpace(s.config.CloudConfig.APIBaseURL) == "" {
			return nil, "", "auth_official_url_missing"
		}
		rawURL = s.config.CloudConfig.APIBaseURL
	case "custom":
		if strings.TrimSpace(rawURL) == "" {
			return nil, "", "auth_custom_url_required"
		}
	default:
		return nil, "", "common_request_invalid"
	}
	normalized, err := normalizeServerURL(rawURL)
	if err != nil {
		if serverType == "official" {
			return nil, "", "auth_official_url_invalid"
		}
		return nil, "", "auth_cloud_url_invalid"
	}
	return s.customCloudClient(normalized), normalized, ""
}
