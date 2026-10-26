package localserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/apimanager"
	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/cloudscope"
	"goteams-client/internal/cloudsync"
	"goteams-client/internal/command"
	"goteams-client/internal/config"
	"goteams-client/internal/configcenter"
	"goteams-client/internal/expertgroup"
	"goteams-client/internal/i18n"
	"goteams-client/internal/identity"
	"goteams-client/internal/knowledge"
	"goteams-client/internal/localauth"
	"goteams-client/internal/pipeline"
	"goteams-client/internal/secrets"
	"goteams-client/internal/storage"
	"goteams-client/internal/tools"
	"goteams-client/internal/workflow"
)

// AccountManagerInterface account manager interface (implemented by bootstrap.AccountManager)
type AccountManagerInterface interface {
	LoginAndCreateSession(ctx context.Context, adminID, userID, userName, displayName, token string, opts LoginOptions) (*SessionInfo, error)
	Logout()
	IsLoggedIn() bool
}

// LoginOptions configures the optional cloud session. It never changes local storage.
type LoginOptions struct {
	// CloudClient Customize the cloud address client; pass nil for official login (use the default client).
	CloudClient *cloud.Client
	// PersistLastLogin Whether to persist the last login index for automatic session recovery after restart.
	// Both official and custom logins are persisted (when the custom login is restored, the client is rebuilt according to the ServerURL and renewed to the cloud).
	PersistLastLogin      bool
	AllowLegacyCredential bool
	// ServerType login target: official | custom. Required only for restore, used to select cloud client.
	ServerType string
	// ServerURL Custom cloud address (after normalization); custom is filled in when logging in, official is empty.
	ServerURL string
}

// Config local service configuration
type Config struct {
	DataDir          string
	ConfigDir        string
	RuntimeDir       string
	TaskRoot         string
	SkillsRoot       string
	CodexSkillReady  bool
	CodexSkillReason string
	VibeSkills       []VibeSkillStatus
	// VibeImBotSkills 记录每个 Vibe Coding 工具对应的 teambuddy-im-bot-cli Skill 安装状态。
	// 微信通道兜底流程通过 markdown 本地路径让 CLI 加载此 Skill，路径由 wechatRemote 按 cliType 查询。
	VibeImBotSkills map[string]VibeImBotSkillStatus
	BaseProfileDir  string
	BootstrapStore  secrets.Store
	SecretStore     *secrets.DelegatingStore
	CloudConfig     *config.CloudConfig
	BrowserTicket   string
	DesktopToken    string
	APIToken        string
	APITokenFromEnv bool
	Shutdown        func()
	CloudClient     *cloud.Client
	AccountMgr      AccountManagerInterface
	TaskDB          *sql.DB
	Orchestrator    *workflow.Orchestrator
	WSHub           *WSHub
}

// SessionInfo session information of the current login account
type SessionInfo struct {
	DeviceMgr *identity.DeviceManager
	JWTMgr    *localauth.JWTManager
	// CloudClient The cloud client actually used by the current session (custom login is an independent client).
	// It must be used to proxy cloud requests (task/work item/device registration, etc.), and the static official client cannot be used directly.
	CloudClient *cloud.Client
	AdminID     string
	UserID      string
	UserName    string
	DisplayName string
	// ServerURL 登录目标云端地址（normalized）。官方登录用它校验会话与当前配置的
	// 远程域名一致，防止共享本地数据目录的多个实例互相串用登录态；custom 登录的
	// 地址由用户主动选择，不受内置配置约束。
	ServerURL string
	// ServerType 登录目标类型（official | custom）。官方会话恢复与校验要求域名与
	// 当前配置一致；custom 会话以自身地址为目标，不做域名一致性校验。
	ServerType string
}

// sessionHolder thread-safely holds the current account session
type sessionHolder struct {
	mu      sync.RWMutex
	current *SessionInfo
}

func (h *sessionHolder) get() *SessionInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

func (h *sessionHolder) set(s *SessionInfo) {
	h.mu.Lock()
	h.current = s
	h.mu.Unlock()
}

func (h *sessionHolder) clear() {
	h.mu.Lock()
	h.current = nil
	h.mu.Unlock()
}

// Server local HTTP service
type Server struct {
	authTransitionMu sync.RWMutex
	config           Config
	router           *gin.Engine
	remoteWeChat     *wechatRemote
	remoteChannels   map[string]RemoteChannel // channel_type -> channel
	tasksHandler     *TasksHandler
	sessions         *sessionHolder
	dbRef            *storage.DBRef
	lastLogin        *lastLoginInfo
	profile          *LocalProfile
	profileMu        sync.Mutex
	profileDir       string
	ticketMu         sync.Mutex
	ticket           string
	browserLogins    *browserLoginManager
}

// New creates a local service instance
func New(config Config) *Server {
	gin.SetMode(gin.ReleaseMode)

	s := &Server{
		config:        config,
		sessions:      &sessionHolder{},
		dbRef:         storage.NewDBRef(),
		profile:       NewLocalProfile(),
		ticket:        config.BrowserTicket,
		browserLogins: newBrowserLoginManager(),
	}
	if config.WSHub != nil {
		config.WSHub.SetAPIToken(config.APIToken)
	}
	s.router = s.buildRouter()

	// Restore the browser session from disk: the browser-side 30-day cookie remains valid across reboots,
	// The memory session table is reloaded here so that old cookies can continue to be mapped to the session without re-logging in.
	s.loadBrowserSessions()

	// Open the default (base) local Profile library: interface/configuration/command/knowledge base, which can be used when not logged in.
	//The data falls in <BaseProfileDir>/base.db, and the key backend switches to the secrets.json of this Profile.
	baseDir := config.BaseProfileDir
	if baseDir == "" {
		baseDir = config.DataDir
	}
	if err := s.switchProfile(context.Background(), baseDir, baseDir); err != nil {
		// Failure to open the Profile library does not block service startup: the relevant page will report an error before logging in.
		// But basic capabilities such as health check/login/configuration of cloud address are still available.
		s.profileMu.Lock()
		s.profileDir = baseDir
		s.profileMu.Unlock()
	}
	return s
}

// SetSession sets the current account session (called when logging in).
// Local functions (interface/configuration/command/knowledge base) use an independent Profile library and do not switch dbRef with the session.
func (s *Server) SetSession(info *SessionInfo) {
	scope := ""
	if info != nil && info.CloudClient != nil {
		scope = info.CloudClient.SessionScope()
	}
	pipeline.DefaultStore().Activate(scope)
	expertgroup.DefaultCloudStore().Activate(scope)
	s.sessions.set(info)
	if s.config.WSHub != nil {
		var client *cloud.Client
		if info != nil {
			client = info.CloudClient
		}
		s.config.WSHub.ActivateAccount(client)
	}
}

// ClearSession clears the current account session (called when logging out)
func (s *Server) ClearSession() {
	s.sessions.clear()
	pipeline.DefaultStore().Activate("")
	expertgroup.DefaultCloudStore().Activate("")
	if s.config.WSHub != nil {
		s.config.WSHub.ActivateAccount(nil)
	}
}

// currentSession gets the current session
func (s *Server) currentSession() *SessionInfo {
	return s.sessions.get()
}

// currentAccountID returns the account ID of the current login account.
// Note: Local function keys are isolated by Profile and no longer isolated by account (see noopAccountID).
func (s *Server) currentAccountID() (string, error) {
	sess := s.sessions.get()
	if sess == nil {
		return "", fmt.Errorf("当前未登录")
	}
	return sess.UserID, nil
}

// accountBrowserSession 返回当前账号会话（云登录态）的浏览器会话视图，供 SkillSurface /
// wechatRemote 注入 ctx 使用。回退口径与 validateSession 完全一致：只有会话域名与当前配置
// 匹配（官方登录）或 custom 会话自带地址时才成立，避免共享数据目录时串用其他实例的登录态；
// 不成立时返回 nil，调用方按「未登录」处理。
func (s *Server) accountBrowserSession() func() *browserSession {
	return func() *browserSession {
		cur := s.currentSession()
		if !s.sessionMatchesConfig(cur) {
			return nil
		}
		return &browserSession{
			ID:        "account",
			UserID:    cur.UserID,
			UserName:  cur.UserName,
			AdminID:   cur.AdminID,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(sessionValidity),
		}
	}
}

// switchProfile opens the fixed base profile and selects its credential store.
func (s *Server) switchProfile(ctx context.Context, dbDir, secretDir string) error {
	s.profileMu.Lock()
	defer s.profileMu.Unlock()

	if dbDir == "" {
		return fmt.Errorf("Profile 数据目录为空")
	}
	if secretDir == "" {
		return fmt.Errorf("密钥存储目录为空")
	}

	db, err := s.profile.Open(ctx, dbDir)
	if err != nil {
		return err
	}

	store, err := secrets.New(secretDir)
	if err != nil {
		return fmt.Errorf("初始化密钥存储失败: %w", err)
	}
	if s.config.SecretStore != nil {
		s.config.SecretStore.SetBackend(store)
	}

	s.dbRef.Set(db)
	s.profileDir = dbDir
	return nil
}

// currentProfileDir returns the current local Profile data directory (empty when not initialized).
func (s *Server) currentProfileDir() string {
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	return s.profileDir
}

// currentSessionDB returns the application-scoped local task database.
func (s *Server) currentSessionDB() *sql.DB {
	return s.config.TaskDB
}

// noopAccountID local function key is isolated by Profile (not account): the key reference does not have the account ID,
// Ensure that the same Profile library has consistent references and no aliasing in the logged in/unlogged state.
func noopAccountID() (string, error) {
	return "", nil
}

// Handler returns HTTP Handler
func (s *Server) Handler() http.Handler {
	return s.router
}

// StartRemoteChannels starts local-device channels after the database is ready.
func (s *Server) StartRemoteChannels(ctx context.Context) {
	// Start WeChat channel
	if s.remoteWeChat != nil {
		s.remoteWeChat.start(ctx)
	}
	// Start other channels from the map
	for _, ch := range s.remoteChannels {
		chType := ch.ChannelType()
		// Skip if already started or disabled
		state, err := s.readChannelState(ctx, chType)
		if err == nil && state.Status == "disabled" {
			applog.Info("通道已停用，跳过自动启动", "channel", chType)
			continue
		}
		ch.Start(ctx)
	}
}

// readChannelState reads channel state from database
func (s *Server) readChannelState(ctx context.Context, channelType string) (ChannelState, error) {
	var state ChannelState
	db := s.config.TaskDB
	err := db.QueryRowContext(ctx, `
		SELECT channel_type, bot_id, sender_id, locale, base_url, cursor,
		       bound_task_uuid, pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
		       context_revision, connected_at, status, updated_at
		FROM gt_remote_channel_state WHERE channel_type=?`, channelType).Scan(
		&state.ChannelType, &state.BotID, &state.SenderID, &state.Locale, &state.BaseURL, &state.Cursor,
		&state.BoundTaskUUID, &state.PendingCancelTaskUUID, &state.PendingResumeTaskUUID, &state.PendingDoneTaskUUID,
		&state.ContextRevision, &state.ConnectedAt, &state.Status, &state.UpdatedAt)
	return state, err
}

// Close releases server-owned base profile resources. Application-level task
// runtime resources are owned and closed by bootstrap.App.
func (s *Server) Close() {
	// 程序退出时给所有还连接的远程通道补发"AI员工已下线"通知：
	// 1) 用独立 ctx（bootstrap 触发时 runCtx 已 done）；
	// 2) 顺序：先发通知（依赖 ws / stream / apiClient），再 Stop 关 transport；
	// 3) 通知失败只记 warn，不阻塞退出；
	// 4) 已 disconnected / disabled 的 channel 由 sendOfflineNotification 自身跳过。
	offlineCtx, offlineCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if s.remoteWeChat != nil {
		state, _ := s.remoteWeChat.readState(offlineCtx)
		s.remoteWeChat.sendOfflineNotification(offlineCtx, state)
	}
	for _, ch := range s.remoteChannels {
		switch c := ch.(type) {
		case *wecomChannel:
			c.sendOfflineNotification(offlineCtx)
		case *feishuChannel:
			c.sendOfflineNotification(offlineCtx)
		case *dingtalkChannel:
			c.sendOfflineNotification(offlineCtx)
		}
	}
	offlineCancel()

	if s.remoteWeChat != nil {
		s.remoteWeChat.stop()
	}
	// Stop all channels from the map
	for _, ch := range s.remoteChannels {
		ch.Stop()
	}
	if s.profile != nil {
		s.profile.Close()
	}
}

// buildRouter builds the route
func (s *Server) buildRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		applog.Error("本地 HTTP 服务发生未处理异常",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"error", recovered,
		)
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "server_error")
	}))
	r.Use(noStoreAPI())
	r.Use(i18n.Middleware())
	r.Use(logAllRequests())
	r.Use(logFailedRequest())
	r.Use(s.requireAPIToken())
	r.Use(s.pinAccountRequest())
	r.Use(s.validateSession())

	// Health check (no Session required)
	r.GET("/api/local/health", s.handleHealth)

	//Authentication routing group
	auth := r.Group("/api/local/auth")
	auth.GET("/ticket", s.handleBrowserTicket)
	auth.GET("/session", s.handleSessionStatus)
	auth.POST("/login", s.handleLogin)
	auth.POST("/logout", s.handleLogout)
	// 浏览器登录：客户端发起 → 系统浏览器完成登录 → 深链接回注登录态。
	// callback 由 Electron 主进程调用（系统把 teamsboard:// 交给主进程），
	// start/status/cancel 由渲染进程调用。
	auth.POST("/browser-login/start", s.startBrowserLogin)
	auth.GET("/browser-login/status", s.browserLoginStatus)
	auth.POST("/browser-login/callback", s.browserLoginCallback)
	auth.POST("/browser-login/cancel", s.browserLoginCancel)

	if s.config.DesktopToken != "" && s.config.Shutdown != nil {
		r.POST("/api/local/desktop/shutdown", s.handleDesktopShutdown)
	}

	// GoTeams status
	r.GET("/api/local/goteams/status", s.handleGoTeamsStatus)

	// WebSocket endpoint
	r.GET("/api/local/ws", s.handleWebSocket)

	//Configuration center
	configHandler := configcenter.NewHandler(s.dbRef, s.config.SecretStore, s.config.CloudConfig, noopAccountID)
	// Cloud connection configuration requires no login (access is required when starting for the first time and not logging in)
	configHandler.RegisterCloudRoutes(r.Group("/api/local/config"))
	// Workspace (data root) relocation is a pre-login machine-level setting:
	// it must be reachable without a cloud session because the data root gates login.
	configHandler.RegisterWorkspaceRoutes(r.Group("/api/local/config"))

	// The following local function routing groups can be used without cloud login, and the data is uniformly read and written to the local general Profile library (data/base.db, shared by all accounts);
	// Configuration center (Git/SSH/Docker/database/model), interface management, command center, knowledge base.
	// Cloud credentials use account-scoped namespaces inside the application secret backend.
	local := r.Group("/api/local")
	configHandler.RegisterRoutes(local.Group("/config"))

	//Interface management
	apisGroup := local.Group("/apis")
	apimanager.RegisterRoutes(apisGroup, s.dbRef, noopAccountID, s.config.SecretStore)

	// command center
	commandGroup := local.Group("/commands")
	command.RegisterRoutes(commandGroup, s.dbRef, s.config.SecretStore)

	//Knowledge base (root directory KR: real disk folder, default <profileDir>/knowledge/root)
	knowledgeGroup := local.Group("/knowledge")
	knowledge.RegisterRoutes(knowledgeGroup, s.dbRef,
		func() (string, error) {
			if dir := s.currentProfileDir(); dir == "" {
				return "", fmt.Errorf("本地 Profile 未初始化")
			} else {
				return knowledge.GetRootDir(s.dbRef.Get(), filepath.Join(dir, "knowledge", "root")), nil
			}
		},
		func() string {
			dir := s.currentProfileDir()
			if dir == "" {
				return ""
			}
			return filepath.Join(dir, "knowledge", "root")
		})

	// Tool Center
	toolsHandler := tools.NewHandler(s.dbRef, s.config.SecretStore)
	toolsGroup := local.Group("/tools")
	toolsHandler.RegisterRoutes(toolsGroup)
	loggedIn := r.Group("", s.requireLogin())

	// Local task management and CLI execution are available without cloud login.
	// Individual cloud-import endpoints validate the cloud session in their handler.
	tasksHandler := NewTasksHandler(s.currentSessionDB, s.currentOrchestrator, s.currentCloudClient, s.config.WSHub,
		s.config.TaskRoot, s.config.SkillsRoot, s.config.CodexSkillReady, s.config.CodexSkillReason, s.config.VibeSkills)
	s.tasksHandler = tasksHandler
	// 快照投递成功后回写的上报状态落在会话 / 沟通记录行上，任务页只靠 task.changed 刷新：
	// 不广播的话，界面会一直停在「待上报」直到下一次本地变更。
	if hub := s.config.WSHub; hub != nil {
		cloudsync.SetTaskDeliveryListener(hub.BroadcastTaskChanged)
	}
	tasksGroup := local.Group("/tasks")
	tasksHandler.RegisterRoutes(tasksGroup)
	tasksHandler.RegisterSkillRoutes(local.Group("/task-skill", requireTaskSkillCaller()))

	// Initialize channel state store
	channelStateStore := newChannelStateStore(s.config.TaskDB, s.config.BootstrapStore)
	// 通道状态变化即时推送到前端：RemoteChannels.vue 已经用 useLocalWS 订阅了
	// remote.wecom.status / remote.feishu.status / remote.dingtalk.status，
	// 但此前只有微信通道会广播，另外三个通道的状态只能等 10s 轮询——
	// 用户点「连接」后长时间看不到 connecting → connected/error 的变化。
	// 这里在 store 层统一注入，覆盖三个通道全部状态写入路径（连接/失败/停用）。
	channelStateStore.SetStatusBroadcaster(func(channelType, status string) {
		if tasksHandler == nil || tasksHandler.wsHub == nil {
			return
		}
		tasksHandler.wsHub.BroadcastToAll("remote."+channelType+".status", map[string]string{"status": status})
	})

	// Initialize WeChat channel
	s.remoteWeChat = newWeChatRemote(s.config.TaskDB, s.config.BootstrapStore, s.config.Orchestrator, tasksHandler, s.config.TaskRoot, s.config.VibeImBotSkills)
	s.remoteWeChat.RegisterRoutes(local.Group("/remote-channels"))

	// Initialize other channels map
	s.remoteChannels = make(map[string]RemoteChannel)

	// Initialize WeCom channel (企业微信通道)
	s.remoteChannels[ChannelTypeWecom] = newWeComRemote(
		s.config.TaskDB, channelStateStore, s.config.TaskDB,
		s.config.Orchestrator, tasksHandler, s.config.TaskRoot,
	)
	// 计划 §三 / §十六 B8：飞书 + 钉钉 transport 骨架（P3 待实接 SDK）。
	s.remoteChannels[ChannelTypeFeishu] = newFeishuRemote(
		s.config.TaskDB, channelStateStore,
		s.config.Orchestrator, tasksHandler, s.config.TaskRoot,
	)
	s.remoteChannels[ChannelTypeDingtalk] = newDingtalkRemote(
		s.config.TaskDB, channelStateStore,
		s.config.Orchestrator, tasksHandler, s.config.TaskRoot,
	)

	// Register routes for all channels.
	// 路由只在这里注册一次：先前在写入 map 后又单独 RegisterRoutes 过一次，
	// 导致 gin 对 /api/local/remote-channels/wecom 重复注册而 panic。
	for _, ch := range s.remoteChannels {
		ch.RegisterRoutes(local.Group("/remote-channels"))
	}

	// 账号会话 getter：与 validateSession 完全一致的回退口径（有 cookie 会话优先，否则用
	// currentSession() 且会话域名与当前配置匹配）。SkillSurface 与 wechatRemote 共用同一份，
	// 不新开 token 通道、不落盘。
	accountSession := s.accountBrowserSession()
	s.remoteWeChat.attachAccountSession(accountSession)
	// S-BE006: 注册 CLI Skill 路由（使用独立的 /cli-skill 路径避免与 task-skill 冲突）。
	// 计划 §四：唯一业务实现位于 SkillSurface，handler 与微信快路径共用，避免漂移。
	// 装配顺序：NewTasksHandler → RegisterRoutes/RegisterSkillRoutes → newWeChatRemote →
	// attachAccountSession → NewSkillSurface → attachSkillSurface → RegisterCLISkillRoutes。
	// wechatRemote 以 wechatEnqueuer 窄接口注入，装配顺序不再是正确性前提。
	cliSkillSurface := NewSkillSurface(s.config.TaskDB, s.currentCloudClient, tasksHandler, s.remoteWeChat,
		s.currentOrchestrator, s.config.VibeSkills, s.config.SkillsRoot,
		s.config.CodexSkillReady, s.config.CodexSkillReason, accountSession, channelStateStore)
	cliSkillSurface.RegisterCLISkillRoutes(local.Group("/cli-skill", requireTaskSkillCaller()))
	// 把 SkillSurface 注入 wechatRemote，让 handleMessage 能复用 lookup / write。
	s.remoteWeChat.attachSkillSurface(cliSkillSurface)

	// 计划 §四：通道无关业务核心（RemoteCommandService）由 wechatRemote 与 wecomChannel 共享。
	// orchestratorGetter 即 Server.currentOrchestrator 方法值，运行时实时取。
	remoteCommands := NewRemoteCommandService(
		s.config.TaskDB, channelStateStore, cliSkillSurface, tasksHandler,
		s.currentOrchestrator, s.config.TaskRoot, s.remoteWeChat,
	)
	remoteCommands.pinAccount = s.pinAccountOperation
	// 计划 §七：wechatRemote.workLoop 改为调 commands.ProcessInbox/CollectResults/DeliverOutbox。
	if s.remoteWeChat != nil {
		s.remoteWeChat.attachCommandService(remoteCommands)
	}
	// 让 wecomChannel 能用 core 解析指令；它本身也实现 RemoteEnqueuer 风格接口（待扩展）。
	if wecom, ok := s.remoteChannels[ChannelTypeWecom].(*wecomChannel); ok && wecom != nil {
		wecom.AttachCommandService(remoteCommands)
	}
	// 飞书/钉钉同样接入 core（P3 SDK 实接后即生效）。
	if feishu, ok := s.remoteChannels[ChannelTypeFeishu].(*feishuChannel); ok && feishu != nil {
		feishu.AttachCommandService(remoteCommands)
	}
	if dingtalk, ok := s.remoteChannels[ChannelTypeDingtalk].(*dingtalkChannel); ok && dingtalk != nil {
		dingtalk.AttachCommandService(remoteCommands)
	}

	iconStore := NewIconStore(s.config.DataDir)
	local.GET("/assets/icons/:filename", iconStore.Serve)
	pipelinesHandler := NewPipelinesHandler(s.currentSessionDB, iconStore, s.currentCloudClient)
	pipelinesHandler.RegisterRoutes(local.Group("/pipelines"))
	expertGroupsHandler := NewExpertGroupsHandler(s.currentSessionDB, iconStore, s.currentCloudClient)
	expertGroupsHandler.RegisterRoutes(local.Group("/expert-groups"))
	NewProjectsHandler(s.currentSessionDB, iconStore).RegisterRoutes(local.Group("/projects"))
	NewNotificationsHandler(s.currentSessionDB, s.currentCloudClient).RegisterRoutes(local.Group("/notifications"))
	tasksHandler.RegisterTeamRoutes(loggedIn.Group("/api/local/team"))
	pipelinesHandler.RegisterCloudRoutes(loggedIn.Group("/api/local/team"))
	pipelinesHandler.RegisterCloudRoutes(loggedIn.Group("/api/local"))
	expertGroupsHandler.RegisterCloudRoutes(loggedIn.Group("/api/local/team"))
	expertGroupsHandler.RegisterCloudRoutes(loggedIn.Group("/api/local"))

	registerWebUI(r)
	return r
}

// logAllRequests records every local API request so connectivity issues between
// the front-end (Vite proxy) and the back-end are visible in the application log.
func logAllRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		//start := time.Now()
		c.Next()
		//applog.Info("本地 HTTP 请求",
		//	"method", c.Request.Method,
		//	"path", c.Request.URL.Path,
		//	"status", c.Writer.Status(),
		//	"cost_ms", time.Since(start).Milliseconds(),
		//	"remote", c.Request.RemoteAddr,
		//)
	}
}

// noStoreAPI marks every loopback API response as non-cacheable. A stale
// cached response (e.g. index.html) previously served to /api/local/* masked
// the real session state in the Electron renderer.
func noStoreAPI() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// logFailedRequest records every server-side HTTP error in the application log.
// Database query failures eventually returned by handlers are therefore visible
// even when Electron owns (and does not display) the sidecar's stderr.
func logFailedRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		writer := &errorResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()
		if c.Writer.Status() >= http.StatusInternalServerError {
			applog.Error("本地 HTTP 请求失败",
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"status", c.Writer.Status(),
				// Handlers return a stable localized error response. Capture only that
				// response for request correlation; request bodies and raw SQL errors
				// must not be recorded here.
				"response", strings.TrimSpace(writer.body.String()),
			)
		}
	}
}

const maxLoggedErrorResponseBytes = 4096

// errorResponseWriter retains a bounded copy only of responses so error logs
// can carry handler diagnostics while keeping normal traffic unlogged.
type errorResponseWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *errorResponseWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *errorResponseWriter) WriteString(value string) (int, error) {
	w.capture([]byte(value))
	return w.ResponseWriter.WriteString(value)
}

func (w *errorResponseWriter) capture(data []byte) {
	remaining := maxLoggedErrorResponseBytes - w.body.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = w.body.Write(data)
	}
}

// requireAPIToken 强制除启动引导路径外的所有本地 API 请求携带本次启动的
// 鉴权 token（header X-GoTeams-Api-Token，WS 在 handler 内校验 query）。
// 浏览器无法为静态页面和 img 请求添加 header，因此非 /api/ 路径（页面与静态
// 资源）以及只读受管图标路径放行；health 由 Electron 探测、auth/* 是登录与
// 握手入口、desktop/shutdown 已有独立 token，均放行。task-skill 和 cli-skill
// 使用各自的协议标记头（X-GoTeams-Skill-Client）替代 API Token，也放行。
func (s *Server) requireAPIToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/api/local/health" ||
			path == "/api/local/desktop/shutdown" ||
			path == "/api/local/ws" ||
			(c.Request.Method == http.MethodGet && strings.HasPrefix(path, ManagedIconURLPrefix)) ||
			strings.HasPrefix(path, "/api/local/auth/") ||
			strings.HasPrefix(path, "/api/local/task-skill/") ||
			strings.HasPrefix(path, "/api/local/cli-skill/") ||
			!strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}
		expected := s.config.APIToken
		if expected == "" {
			applog.Warn("本地 API token 未配置，跳过校验", "path", path)
			c.Next()
			return
		}
		token := c.GetHeader("X-GoTeams-Api-Token")
		if len(token) != len(expected) || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			i18n.Error(c, http.StatusForbidden, "common_access_token_invalid", "access_token_invalid")
			return
		}
		c.Next()
	}
}

const (
	taskSkillClientHeader      = "X-GoTeams-Skill-Client"
	taskSkillClientValue       = "teambuddy-v1"
	taskSkillClientLegacyValue = "teamsboard-v1"
)

// isAcceptedTaskSkillClient 放行当前协议标记，并兼容重命名前已安装的 Skill 副本：
// 旧副本仍发送 teamsboard-v1，不应在新版本上被 403 拒绝。
func isAcceptedTaskSkillClient(value string) bool {
	return value == taskSkillClientValue || value == taskSkillClientLegacyValue
}

func requireTaskSkillCaller() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Origin") != "" || !isAcceptedTaskSkillClient(c.GetHeader(taskSkillClientHeader)) {
			i18n.Error(c, http.StatusForbidden, "localserver_skill_caller_forbidden", "skill_caller_forbidden")
			return
		}
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			host = c.Request.RemoteAddr
		}
		ip := net.ParseIP(strings.TrimSpace(host))
		if ip == nil || !ip.IsLoopback() {
			i18n.Error(c, http.StatusForbidden, "localserver_skill_loopback_required", "skill_loopback_required")
			return
		}
		c.Next()
	}
}

// currentOrchestratorGetter returns the current Orchestrator
func (s *Server) currentOrchestrator() *workflow.Orchestrator {
	return s.config.Orchestrator
}

// currentCloudClient returns only an authenticated account client; login uses a separate anonymous client.
// When logging in with a custom address, the session holds an independent client, and proxy cloud requests must go here.
// Otherwise, the JWT of the custom cloud will be sent to the official cloud, resulting in 401.
func (s *Server) currentCloudClient() *cloud.Client {
	sess := s.currentSession()
	if sess != nil && sess.CloudClient != nil {
		return sess.CloudClient
	}
	return nil
}

// requireLogin middleware that requires logged in
func (s *Server) requireLogin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.currentSession() == nil {
			i18n.Error(c, http.StatusUnauthorized, "common_not_authenticated", "not_authenticated")
			return
		}
		c.Next()
	}
}

// handleHealth health check
func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "goteams-client",
	})
}

// handleWebSocket WebSocket endpoint
func (s *Server) handleWebSocket(c *gin.Context) {
	if s.config.WSHub == nil {
		i18n.Error(c, http.StatusServiceUnavailable, "common_runtime_unavailable", "runtime_unavailable")
		return
	}
	s.config.WSHub.HandleWebSocket(c)
}

// Keep an account-dependent HTTP operation on one account through its response.
// Authentication mutations acquire the write lock themselves; WS holds it only during handshake.
func (s *Server) pinAccountRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		mutation := mutatesAccount(path)
		if !strings.HasPrefix(path, "/api/local/") || mutation || accountIndependentRequest(path) {
			c.Next()
			return
		}
		exclusive := path == "/api/local/config/cloud" && c.Request.Method == http.MethodPut
		var unlock func()
		if exclusive {
			s.authTransitionMu.Lock()
			unlock = s.authTransitionMu.Unlock
		} else {
			s.authTransitionMu.RLock()
			unlock = s.authTransitionMu.RUnlock
			// 普通会话探测只读；确需清理失效会话时再升级，handler 在写锁下重新检查。
			if current := s.currentSession(); path == "/api/local/auth/session" && current != nil && !s.sessionMatchesConfig(current) {
				unlock()
				s.authTransitionMu.Lock()
				unlock = s.authTransitionMu.Unlock
				exclusive = true
			}
		}
		locked := true
		defer func() {
			if locked {
				unlock()
			}
		}()
		requestSession := s.currentSession()
		scope := ""
		accountScope := "local"
		generation := "local"
		if current := s.currentSession(); current != nil && current.CloudClient != nil {
			scope = current.CloudClient.SessionScope()
			accountScope = AccountScope(current.CloudClient.BaseURL(), current.AdminID, current.UserID)
			generation = scope
		}
		// Renderer requests carry the identity captured before token discovery.
		// Trusted local integrations without these optional headers remain compatible.
		if !strings.HasPrefix(path, "/api/local/auth/") && path != "/api/local/config/cloud" {
			expected := c.GetHeader("X-GoTeams-Account-Scope")
			expectedGeneration := c.GetHeader("X-GoTeams-Session-Generation")
			if (expected != "" && expected != accountScope) || (expectedGeneration != "" && expectedGeneration != generation) {
				i18n.Error(c, http.StatusConflict, "common_request_invalid", "account_scope_changed")
				c.Abort()
				return
			}
		}
		ctx := context.WithValue(c.Request.Context(), accountOperationKey{}, true)
		c.Request = c.Request.WithContext(cloudscope.With(ctx, scope))
		c.Next()
		if !exclusive && requestSession != nil && c.Writer.Status() == http.StatusUnauthorized && strings.HasPrefix(path, "/api/local/team/") {
			// A rejected upstream login must also invalidate the local account; otherwise
			// an anonymous UI could receive that account's locally cached team rows.
			unlock()
			locked = false
			s.authTransitionMu.Lock()
			defer s.authTransitionMu.Unlock()
			if s.currentSession() == requestSession {
				if s.config.AccountMgr != nil {
					s.config.AccountMgr.Logout()
				}
				s.ClearSession()
			}
		}
	}
}

// These application-level handlers use a fixed local Profile and no cloud identity.
// Keep the explicit list narrow: task/skill/remote routes still pin the account.
func accountIndependentRequest(path string) bool {
	if path == "/api/local/health" {
		return true
	}
	if path == "/api/local/config/cloud" || path == "/api/local/config/workspace" {
		return false
	}
	for _, group := range []string{"config", "commands", "apis", "knowledge", "tools"} {
		prefix := "/api/local/" + group
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

type accountOperationKey struct{}

// 远程后台指令与 HTTP 请求使用相同的切换屏障；嵌套在 HTTP 中时不重复获取读锁。
func (s *Server) pinAccountOperation(ctx context.Context) (context.Context, func()) {
	if pinned, _ := ctx.Value(accountOperationKey{}).(bool); pinned {
		return ctx, func() {}
	}
	s.authTransitionMu.RLock()
	return context.WithValue(ctx, accountOperationKey{}, true), s.authTransitionMu.RUnlock
}

func mutatesAccount(path string) bool {
	return path == "/api/local/auth/login" || path == "/api/local/auth/logout" || path == "/api/local/auth/browser-login/callback"
}
