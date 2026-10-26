package bootstrap

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/cloudsync"
	"goteams-client/internal/config"
	"goteams-client/internal/executor"
	"goteams-client/internal/localserver"
	"goteams-client/internal/secrets"
	"goteams-client/internal/storage"
	"goteams-client/internal/workspace"
)

// App is the main structure of the client application
type App struct {
	dataDir          string
	configDir        string
	runtimeDir       string
	taskDir          string
	skillsDir        string
	codexSkillReady  bool
	codexSkillReason string
	vibeSkills       []localserver.VibeSkillStatus
	// vibeImBotSkills 记录每个 Vibe Coding 工具对应的 teambuddy-im-bot-cli Skill 安装状态。
	// teambuddy-im-bot-cli 与 teambuddy 共用 VibeSkillRoot，按工具分别安装到 ~/.codex/skills、~/.claude/skills 等。
	// 微信通道兜底任务的 prompt 直接通过 markdown 本地路径引用此 Skill，CLI 启动后自行加载。
	vibeImBotSkills map[string]localserver.VibeImBotSkillStatus
	cloud           *config.CloudConfig
	secretStore     *secrets.DelegatingStore
	bootstrapStore  secrets.Store
	server          *localserver.Server
	listener        net.Listener
	browserTicket   string
	apiToken        string
	apiTokenFromEnv bool
	lock            interface{}

	//Basic components (not dependent on database)
	cloudClient  *cloud.Client
	accountMgr   *AccountManager
	localRuntime *LocalRuntime
}

// New creates and initializes the application instance
func New(ctx context.Context) (*App, error) {
	app := &App{}

	// Initialize the directory and logger before reading any runtime settings so
	// even a malformed config.ini is captured in the file log.
	if err := app.initDirs(); err != nil {
		return nil, fmt.Errorf("初始化目录失败: %w", err)
	}
	// File logging must be ready before any database is opened. Previously the
	// logger was only configured for an account directory (and was never called),
	// which left ~/.goteams/logs empty and discarded sidecar stderr in desktop use.
	if err := applog.InitLogger(filepath.Join(filepath.Dir(app.dataDir), "logs")); err != nil {
		return nil, fmt.Errorf("初始化应用日志失败: %w", err)
	}
	applog.Info("正在初始化 GoTeams 客户端", "data_dir", app.dataDir)

	// Load cloud connection configuration (from INI file, does not rely on database).
	// configDir 已经由 initDirs 内的 workspace.ResolveDataRoot 解析过，搬迁场景下
	// 指向新数据根，磁盘配置文件必须从新位置读取。
	cloudCfg, err := loadCloudConfig(app.configDir)
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	app.cloud = cloudCfg

	// Single instance lock.
	if err := app.acquireLockImpl(); err != nil {
		return nil, fmt.Errorf("获取实例锁失败: %w", err)
	}
	app.codexSkillReady, app.codexSkillReason, app.vibeSkills = app.installBuiltinSkills()
	app.vibeImBotSkills = installImBotCLISkills(app.vibeSkills)

	// 4. Initialize SecretStore.
	store := secrets.NewDelegating()
	app.secretStore = store

	// Boot storage: carries the account index last_login, which always points to configDir.
	// In this way, RestoreSession can restore the official account last logged in before logging in, which is isolated from the Profile key.
	bootstrapStore, bErr := secrets.New(app.configDir)
	if bErr != nil {
		return nil, fmt.Errorf("初始化引导密钥存储失败: %w", bErr)
	}
	app.bootstrapStore = bootstrapStore
	store.SetBackend(bootstrapStore)

	// 5. Generate browser Ticket
	app.browserTicket = strings.TrimSpace(os.Getenv("GOTEAMS_BROWSER_TICKET"))
	if app.browserTicket == "" {
		app.browserTicket = generateTicket()
	}

	// Generate the startup API token: the Electron main process injects it via
	// GOTEAMS_API_TOKEN; when running without Electron, self-generate so the
	// token enforcement is always on and the frontend can read it from /auth/session.
	app.apiToken = strings.TrimSpace(os.Getenv("GOTEAMS_API_TOKEN"))
	app.apiTokenFromEnv = app.apiToken != ""
	if app.apiToken == "" {
		app.apiToken = generateTicket()
	}

	// 6. Initialize the cloud client (does not rely on database)
	app.cloudClient = cloud.NewClient(cloudCfg, nil)

	// 7. Initialize the application-level local task runtime. This is deliberately
	// independent from cloud login and remains alive across logout/account switches.
	app.localRuntime, err = NewLocalRuntime(ctx, app.dataDir, app.cloudClient)
	if err != nil {
		app.releaseLockImpl()
		return nil, fmt.Errorf("初始化本地任务运行时失败: %w", err)
	}

	// Establish the base.db baseline without deleting historical files or tables.
	baseManager, err := storage.NewManagerForRole(app.dataDir, storage.DatabaseBase)
	if err != nil {
		app.localRuntime.Close()
		app.releaseLockImpl()
		return nil, fmt.Errorf("打开基础数据库失败: %w", err)
	}
	if err := baseManager.Migrate(ctx); err != nil {
		baseManager.Close()
		app.localRuntime.Close()
		app.releaseLockImpl()
		return nil, fmt.Errorf("基础数据库迁移失败: %w", err)
	}
	baseManager.Close()
	// 8. Initialize the optional cloud account manager.
	app.accountMgr = NewAccountManager(
		app.cloudClient, app.secretStore, bootstrapStore,
		app.configDir, app.localRuntime,
	)
	app.localRuntime.WSHub.RegisterAccountPushFunc(func(client *cloud.Client) (string, interface{}) {
		notifications := localserver.NewNotificationsHandler(func() *sql.DB { return app.localRuntime.DB }, func() *cloud.Client { return client })
		unread, err := notifications.UnreadCount(ctx)
		if err != nil {
			return "", nil
		}
		return "app.notifications.unread", map[string]interface{}{"count": unread}
	})

	return app, nil
}

// Run starts the local HTTP service and blocks until the context is canceled
func (a *App) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// Start the loopback HTTP service (address from GOTEAMS_LISTEN_ADDR, default 127.0.0.1:18900)
	listener, err := net.Listen("tcp", a.cloud.ListenAddr)
	if err != nil {
		return fmt.Errorf("启动监听失败: %w", err)
	}
	a.listener = listener

	addr := listener.Addr().String()
	localAPIBaseURL := "http://" + addr
	applog.Info("GoTeams 客户端已启动",
		"addr", localAPIBaseURL,
		"cloud_api", a.cloud.APIBaseURL,
		"config", a.cloud.ConfigSource(),
	)
	a.accountMgr.SetLocalAPIBaseURL(localAPIBaseURL)

	//Write to runtime.json
	if err := a.writeRuntimeInfo(addr); err != nil {
		return fmt.Errorf("写入运行时信息失败: %w", err)
	}

	//Create local HTTP service
	a.server = localserver.New(localserver.Config{
		DataDir:          a.dataDir,
		ConfigDir:        a.configDir,
		RuntimeDir:       a.runtimeDir,
		TaskRoot:         a.taskDir,
		SkillsRoot:       a.skillsDir,
		CodexSkillReady:  a.codexSkillReady,
		CodexSkillReason: a.codexSkillReason,
		VibeSkills:       a.vibeSkills,
		VibeImBotSkills:  a.vibeImBotSkills,
		BaseProfileDir:   a.dataDir,
		BootstrapStore:   a.bootstrapStore,
		SecretStore:      a.secretStore,
		CloudConfig:      a.cloud,
		BrowserTicket:    a.browserTicket,
		DesktopToken:     strings.TrimSpace(os.Getenv("GOTEAMS_DESKTOP_TOKEN")),
		APIToken:         a.apiToken,
		APITokenFromEnv:  a.apiTokenFromEnv,
		Shutdown:         cancelRun,
		CloudClient:      a.cloudClient,
		AccountMgr:       a.accountMgr,
		TaskDB:           a.localRuntime.DB,
		Orchestrator:     a.localRuntime.Orchestrator,
		WSHub:            a.localRuntime.WSHub,
	})

	srv := &http.Server{
		Handler: a.server.Handler(),
	}

	//Restore the account session from the persistent login state (no need to log in again after restart)
	a.server.RestoreSession(runCtx)
	a.server.StartRemoteChannels(runCtx)

	// Start the cloud session sync service: it periodically pushes the created/updated
	// CLI session records of cloud tasks to the cloud, in per-task creation order.
	// It uses the current account session's cloud client and skips rounds while logged out.
	cloudsync.NewSessionSyncService(a.localRuntime.DB, func() *cloud.Client {
		if sess := a.accountMgr.Current(); sess != nil {
			return sess.CloudClient
		}
		return nil
	}).Start(runCtx)

	// 产出文档上报是独立旁路：独立表、独立协程、独立重试，挂在任务投影之后，
	// 不进入任务主链路。关闭开关时 worker 空转但不扫描，文件树也不返回上报状态。
	cloudsync.SetDocumentUploadConfig(a.cloud.DocumentUpload)
	documentSync := cloudsync.NewDocumentSyncService(a.localRuntime.DB, func() *cloud.Client {
		if sess := a.accountMgr.Current(); sess != nil {
			return sess.CloudClient
		}
		return nil
	}, a.cloud.DocumentUpload.Interval())
	cloudsync.SetDefaultDocumentSyncService(documentSync)
	documentSync.Start(runCtx)
	applog.Info("产出文档上报已初始化",
		"enabled", a.cloud.DocumentUpload.Enabled,
		"interval", a.cloud.DocumentUpload.Interval().String(),
		"max_file_mb", a.cloud.DocumentUpload.MaxFileMB,
		"max_files_per_task", a.cloud.DocumentUpload.MaxFilesPerTask)

	// 会话进入终态后，把这一轮的执行过程和执行结果推到云端。这里不改本地任务泳道，
	// 也不改云端需求状态；两边的状态各自保留。专家团路由中的中间态只推任务投影。
	a.localRuntime.Orchestrator.AddStateListener(func(taskUUID, stepKey, sessionUUID, status string) {
		var client *cloud.Client
		if sess := a.accountMgr.Current(); sess != nil {
			client = sess.CloudClient
		}
		if client == nil {
			return
		}
		go func() {
			timeout := 30 * time.Second
			if sessionStatusUploadsExecution(status) {
				timeout = 2 * time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if sessionStatusUploadsExecution(status) {
				if err := cloudsync.FlushTaskExecution(ctx, a.localRuntime.DB, client, taskUUID); err != nil {
					applog.Warn("[CloudSync] 会话结束后同步执行记录失败", "task_uuid", taskUUID, "status", status, "error", err.Error())
				}
				return
			}
			if err := cloudsync.DefaultPusher().PushTaskSync(ctx, a.localRuntime.DB, client, taskUUID); err != nil {
				applog.Warn("[CloudSync] 会话结束后推送任务结果失败", "task_uuid", taskUUID, "error", err.Error())
			}
		}()
	})

	// Preload and refresh the CLI/model discovery cache in the background (see
	// executor.StartCLIDiscoveryRefresh). It re-detects installed CLIs and their
	// available models right after startup and every few minutes, persists the
	// results to config/clis.json and config/models.json, and the "选择执行 CLI"
	// popup reads those snapshots via /tasks/cli-discovery and /tasks/cli-models
	// without spawning each CLI on every request.
	executor.StartCLIDiscoveryRefresh(runCtx, a.configDir)

	// Wait for exit
	go func() {
		<-runCtx.Done()
		applog.Info("正在关闭服务...")
		// Release the resources of the current account session (close database, etc.)
		// without clearing the last login index, so that the login state
		// can be restored automatically on the next startup.
		if a.accountMgr != nil {
			a.accountMgr.Close()
		}
		if a.localRuntime != nil {
			a.localRuntime.Close()
		}
		if a.server != nil {
			a.server.Close()
		}
		srv.Close()
		a.releaseLockImpl()
		applog.Info("GoTeams 客户端已关闭")
		applog.CloseLogger()
	}()

	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// sessionStatusUploadsExecution reports whether this callback is a finished CLI
// session. Expert-group routing uses other status names and must not upload the
// whole execution body on every hop. "failed" is shared; uploading once there is
// still the execution result, not a status change on either side.
func sessionStatusUploadsExecution(status string) bool {
	switch status {
	case "success", "failed", "stopped", "interrupted":
		return true
	default:
		return false
	}
}

// initDirs initializes the data-root directory structure. The data root
// defaults to ~/.goteams but may have been relocated by the workspace
// setting; ResolveDataRoot resolves the pointer file left in HOME.
func (a *App) initDirs() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	root, err := workspace.ResolveDataRoot()
	if err != nil {
		return err
	}
	if err := workspace.MarkDataRootStructure(root); err != nil {
		return err
	}
	a.dataDir = filepath.Join(root, "data")
	a.configDir = filepath.Join(root, "config")
	a.runtimeDir = filepath.Join(root, "runtime")
	a.taskDir = filepath.Join(root, "task")
	a.skillsDir = filepath.Join(home, ".codex", "skills")

	dirs := []string{
		a.dataDir,
		a.configDir,
		a.runtimeDir,
		a.taskDir,
		filepath.Join(root, "logs"),
		filepath.Join(a.runtimeDir, "sessions"),
		filepath.Join(a.runtimeDir, "processes"),
		filepath.Join(a.runtimeDir, "temp"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	return nil
}

// writeRuntimeInfo writes runtime information. External skills always read the
// stable path under the default data root. After a workspace relocation the
// process also keeps its own copy beside the relocated data, so both paths
// publish the same live loopback address.
func (a *App) writeRuntimeInfo(addr string) error {
	clientPath, err := currentClientPath()
	if err != nil {
		return err
	}
	info := map[string]interface{}{
		"address":      addr,
		"client_path":  clientPath,
		"pid":          os.Getpid(),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"api_base_url": a.cloud.APIBaseURL,
		"started_at":   time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return writeRuntimeFiles(runtimeInfoPaths(a.runtimeDir), data)
}

func runtimeInfoPaths(runtimeDir string) []string {
	primary := filepath.Join(runtimeDir, "runtime.json")
	stable := filepath.Join(workspace.DefaultDataRoot(), "runtime", "runtime.json")
	if filepath.Clean(primary) == filepath.Clean(stable) {
		return []string{primary}
	}
	return []string{primary, stable}
}

func writeRuntimeFiles(paths []string, data []byte) error {
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("创建运行时目录失败: %w", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("写入运行时信息失败: %w", err)
		}
	}
	return nil
}

func currentClientPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("读取客户端程序路径失败: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(absolute) {
		return "", fmt.Errorf("客户端程序路径不是绝对路径")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("客户端程序不可访问: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("客户端程序路径不是文件")
	}
	return absolute, nil
}

// generateTicket generates a one-time browser Ticket
func generateTicket() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
