package localserver

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/i18n"
)

// browserLoginTTL 一次浏览器登录会话的有效期。用户需要在浏览器里完成登录或注册，
// 因此留出比一般验证码更宽裕的窗口；超时后前端需要重新发起。
const browserLoginTTL = 10 * time.Minute

// 浏览器登录会话的状态机。pending 之外的终态只用于前端展示与错误提示。
const (
	browserLoginPending   = "pending"
	browserLoginSucceeded = "succeeded"
	browserLoginFailed    = "failed"
	browserLoginExpired   = "expired"
	browserLoginCancelled = "cancelled"
)

// browserLoginSession 一次「在浏览器里登录并回注客户端」的会话。
// 只保存回注登录态所必需的信息，不保存任何凭据。
type browserLoginSession struct {
	State      string
	ServerType string
	ServerURL  string
	ExpiresAt  time.Time
	Status     string
	// Claimed 表示已有一个回调在兑换票据。深链接可能被系统重复投递，
	// 用它保证同一 state 只会兑换一次。
	Claimed bool
	// User/AdminID 仅在成功后有值，且只包含展示字段。
	User     *cloud.UserInfo
	AdminID  string
	ErrorKey string // 失败时的 i18n key
}

// browserLoginSnapshot 是会话的只读快照。handler 只持有快照，避免在锁外
// 读取会被登录回调改写的字段。
type browserLoginSnapshot struct {
	ServerType string
	ServerURL  string
	Status     string
	User       *cloud.UserInfo
	AdminID    string
	ErrorKey   string
}

func (s *browserLoginSession) snapshot() *browserLoginSnapshot {
	return &browserLoginSnapshot{
		ServerType: s.ServerType,
		ServerURL:  s.ServerURL,
		Status:     s.Status,
		User:       s.User,
		AdminID:    s.AdminID,
		ErrorKey:   s.ErrorKey,
	}
}

// browserLoginManager 持有进行中的浏览器登录会话。进程内内存态即可：
// 会话本身只有 10 分钟生命期，重启客户端后重新发起即可。
type browserLoginManager struct {
	mu       sync.Mutex
	sessions map[string]*browserLoginSession
}

func newBrowserLoginManager() *browserLoginManager {
	return &browserLoginManager{sessions: make(map[string]*browserLoginSession)}
}

// create 登记一次新的浏览器登录会话，并顺带清理已过期的旧会话。
func (m *browserLoginManager) create(sess *browserLoginSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Only the latest user-selected target may establish a session.
	clear(m.sessions)
	m.sessions[sess.State] = sess
}

// get 读取会话；已过期时返回终态 expired，避免前端一直轮询。
func (m *browserLoginManager) get(state string) *browserLoginSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[state]
	if !ok {
		return nil
	}
	if sess.Status == browserLoginPending && time.Now().After(sess.ExpiresAt) {
		sess.Status = browserLoginExpired
	}
	return sess.snapshot()
}

// claim 认领一次回调处理。只有第一个回调拿到 ok=true，重复投递的深链接
// 会拿到当前快照并按状态直接返回，不会重复兑换票据。
//
// 用户取消或重新选择服务后，旧网页的回调不得覆盖新的连接。
func (m *browserLoginManager) claim(state string) (snap *browserLoginSnapshot, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, found := m.sessions[state]
	if !found {
		return nil, false
	}
	if sess.Status == browserLoginPending && time.Now().After(sess.ExpiresAt) {
		sess.Status = browserLoginExpired
	}
	if sess.Claimed || sess.Status != browserLoginPending {
		return sess.snapshot(), false
	}
	sess.Status = browserLoginPending
	sess.Claimed = true
	return sess.snapshot(), true
}

// succeed 写入成功终态。票据兑换期间会话可能已被取消或过期，此时不覆盖已有终态。
func (m *browserLoginManager) succeed(state string, user cloud.UserInfo, adminID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[state]
	if !ok || sess.Status != browserLoginPending {
		return
	}
	sess.Status = browserLoginSucceeded
	sess.User = &user
	sess.AdminID = adminID
}

// fail 写入失败终态，规则与 succeed 相同。
func (m *browserLoginManager) fail(state, errorKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[state]
	if !ok || sess.Status != browserLoginPending {
		return
	}
	sess.Status = browserLoginFailed
	sess.ErrorKey = errorKey
}

// cancel 主动结束一次尚未被深链接认领的会话。
// 票据兑换已经开始时不能改终态：浏览器里用户已经点了确认，客户端应继续完成回注。
func (m *browserLoginManager) cancel(state string) *browserLoginSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[state]
	if !ok {
		return nil
	}
	if sess.Status == browserLoginPending && !sess.Claimed {
		sess.Status = browserLoginCancelled
	}
	return sess.snapshot()
}

// startBrowserLogin 生成 state 并向云端登记登录会话，返回需要在系统浏览器中打开的地址。
// 这是「浏览器登录」链路的第一步，此时客户端还没有任何云端凭据。
func (s *Server) startBrowserLogin(c *gin.Context) {
	var body struct {
		ServerType string `json:"server_type"` // official | custom
		ServerURL  string `json:"server_url"`
	}
	// 允许空 body：默认按官方云端登录。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
			return
		}
	}

	serverType := strings.TrimSpace(body.ServerType)
	if serverType == "" {
		serverType = "official"
	}

	client, normalized, key := s.resolveLoginTarget(serverType, body.ServerURL)
	if key != "" {
		i18n.Error(c, http.StatusBadRequest, key, strings.TrimPrefix(strings.TrimPrefix(key, "auth_"), "common_"))
		return
	}

	state := generateSessionID()
	remote, err := client.CreateBrowserLoginSession(c.Request.Context(), state)
	if err != nil {
		applog.Warn("发起浏览器登录失败", "error", err.Error(), "server_type", serverType)
		i18n.Error(c, http.StatusBadGateway, "auth_browser_login_start_failed", "browser_login_start_failed")
		return
	}

	now := time.Now()
	s.browserLogins.create(&browserLoginSession{
		State:      remote.State,
		ServerType: serverType,
		ServerURL:  normalized,
		ExpiresAt:  now.Add(browserLoginTTL),
		Status:     browserLoginPending,
	})

	c.JSON(http.StatusOK, gin.H{
		"state":       remote.State,
		"browser_url": remote.LoginURL,
		"expires_in":  int(browserLoginTTL.Seconds()),
	})
}

// browserLoginStatus 前端轮询用：只暴露状态和展示用用户信息。
func (s *Server) browserLoginStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		i18n.Error(c, http.StatusBadRequest, "auth_browser_login_state_required", "browser_login_state_required")
		return
	}
	sess := s.browserLogins.get(state)
	if sess == nil {
		i18n.Error(c, http.StatusNotFound, "auth_browser_login_session_missing", "browser_login_session_missing")
		return
	}

	payload := gin.H{"status": sess.Status}
	if sess.Status == browserLoginSucceeded {
		payload["user"] = sess.User
		payload["admin_id"] = sess.AdminID
	}
	if sess.Status == browserLoginFailed && sess.ErrorKey != "" {
		payload["error"] = i18n.T(c, sess.ErrorKey)
		payload["code"] = sess.ErrorKey
	}
	c.JSON(http.StatusOK, payload)
}

// browserLoginCallback 由 Electron 主进程在收到 teamsboard:// 深链接后调用。
// 这里用一次性 ticket 向云端兑换登录态，然后走与账号密码登录相同的本地登录收尾。
func (s *Server) browserLoginCallback(c *gin.Context) {
	s.authTransitionMu.Lock()
	defer s.authTransitionMu.Unlock()
	var body struct {
		State  string `json:"state"`
		Ticket string `json:"ticket"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	state := strings.TrimSpace(body.State)
	ticket := strings.TrimSpace(body.Ticket)
	if state == "" || ticket == "" {
		i18n.Error(c, http.StatusBadRequest, "auth_browser_login_callback_invalid", "browser_login_callback_invalid")
		return
	}

	sess, claimed := s.browserLogins.claim(state)
	if sess == nil {
		i18n.Error(c, http.StatusNotFound, "auth_browser_login_session_missing", "browser_login_session_missing")
		return
	}
	if !claimed {
		// 深链接可能被系统重复投递：已处理完的会话按当前状态返回，正在处理的返回 pending，
		// 渲染层以会话接口为准，不会据此误判登录结果。
		c.JSON(http.StatusOK, gin.H{"status": sess.Status})
		return
	}

	client := s.customCloudClient(sess.ServerURL)
	if client == nil || !client.IsConfigured() {
		s.browserLogins.fail(state, "auth_official_url_missing")
		i18n.Error(c, http.StatusBadRequest, "auth_official_url_missing", "official_url_missing")
		return
	}

	loginResp, err := client.ExchangeBrowserLoginTicket(c.Request.Context(), state, ticket)
	if err != nil {
		applog.Warn("浏览器登录票据兑换失败", "error", err.Error())
		s.browserLogins.fail(state, "auth_browser_login_exchange_failed")
		i18n.Error(c, http.StatusUnauthorized, "auth_browser_login_exchange_failed", "browser_login_exchange_failed")
		return
	}

	deviceRegistered, err := s.completeCloudLoginLocked(c, client, sess.ServerType, sess.ServerURL, loginResp)
	if err != nil {
		applog.Warn("浏览器登录写入本地会话失败", "error", err.Error())
		s.browserLogins.fail(state, "auth_session_create_failed")
		writeLoginStageError(c, err)
		return
	}

	s.browserLogins.succeed(state, loginResp.User, loginResp.AdminID)

	applog.Info("浏览器登录完成",
		"admin_id", loginResp.AdminID,
		"user_name", loginResp.User.Username,
		"device_registered", deviceRegistered,
	)
	c.JSON(http.StatusOK, gin.H{
		"status":            browserLoginSucceeded,
		"user":              loginResp.User,
		"admin_id":          loginResp.AdminID,
		"device_registered": deviceRegistered,
	})
}

// browserLoginCancel 前端主动取消一次进行中的浏览器登录。
func (s *Server) browserLoginCancel(c *gin.Context) {
	var body struct {
		State string `json:"state"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	state := strings.TrimSpace(body.State)
	sess := s.browserLogins.cancel(state)
	if state == "" || sess == nil {
		i18n.Error(c, http.StatusNotFound, "auth_browser_login_session_missing", "browser_login_session_missing")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": sess.Status})
}

func (m *browserLoginManager) invalidate() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.sessions)
}
