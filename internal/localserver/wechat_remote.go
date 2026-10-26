package localserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"

	"goteams-client/internal/applog"
	"goteams-client/internal/i18n"
	"goteams-client/internal/secrets"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/workflow"
)

const (
	wechatBotTokenKey        = "goteams:remote_wechat_bot_token"
	wechatContextTokenKey    = "goteams:remote_wechat_context_token"
	wechatQRStatusTimeout    = 5 * time.Second
	wechatUpdatesPollTimeout = 10 * time.Second
	// workLoopFallbackInterval 是 workLoop 在没有事件唤醒时的兜底轮询间隔，
	// 仅用于 CollectResults（长任务结果回传）这类无法被事件触发的路径。
	// 入站消息处理由 pollLoop 通过 w.signal() 即时唤醒，不再受此间隔限制——
	// 这是微信通道回复延迟从"≤1s"降到"即时"的关键。
	workLoopFallbackInterval = 1 * time.Second
)

type wechatState struct {
	BotID                 string
	SenderID              string
	Locale                string
	BaseURL               string
	Cursor                string
	BoundTaskUUID         string
	PendingCancelTaskUUID string
	PendingResumeTaskUUID string
	PendingDoneTaskUUID   string
	ContextRevision       int64
	ConnectedAt           int64
	Status                string // Channel status: disconnected/connecting/connected/error/disabled
}

type wechatLoginAttempt struct {
	ID           string
	QRCode       string
	ImageDataURL string
	Status       string
	VerifyCode   string
	Error        string
	BaseURL      string
	Locale       string
	CreatedAt    time.Time
	Cancel       context.CancelFunc
	Wake         chan struct{}
}

type wechatPendingFinish struct {
	id          string
	status      string
	taskUUID    string
	sessionUUID string
	reply       string
}

type wechatRemote struct {
	db              *sql.DB
	store           secrets.Store
	orchestrator    *workflow.Orchestrator
	tasks           *TasksHandler
	taskRoot        string
	vibeImBotSkills map[string]VibeImBotSkillStatus
	// skillSurface 暴露 lookup / write 等共用能力（计划 §四）。
	// nil 时 wechat 通道仍可继续工作，仅本地快路径指令会被引导到 CLI 兜底。
	skillSurface *SkillSurface
	// accountSession 提供当前云登录态的账号会话（与 validateSession 同一回退口径）。
	accountSession func() *browserSession
	// api 是微信通道与 iLink 服务端通讯的唯一 seam（见 wechatTransport）。
	// 字段类型用接口而非具体 *wechatAPI，便于后续替换底层实现（如换 SDK）而不动上层逻辑。
	api wechatTransport
	// commands 通道无关业务核心（A-9 装配；workLoop 调它的 ProcessInbox/CollectResults/DeliverOutbox）。
	commands *RemoteCommandService
	mu       sync.Mutex
	// A disconnect waits for an accepted command to finish its local handoff.
	dispatchMu            sync.RWMutex
	attempt               *wechatLoginAttempt
	pendingFinish         *wechatPendingFinish
	codexMonitors         map[string]struct{}
	lastError             string
	replyMessageIDMissing bool
	pollCancel            context.CancelFunc
	sendCancel            context.CancelFunc
	wake                  chan struct{}
	started               bool
	rootCtx               context.Context
	qrStatusTimeout       time.Duration
	updatesPollTimeout    time.Duration
}

func newWeChatRemote(db *sql.DB, store secrets.Store, orchestrator *workflow.Orchestrator, tasks *TasksHandler, taskRoot string, vibeImBotSkills map[string]VibeImBotSkillStatus) *wechatRemote {
	return &wechatRemote{db: db, store: store, orchestrator: orchestrator, tasks: tasks, taskRoot: taskRoot,
		vibeImBotSkills: vibeImBotSkills, api: newWeChatAPI(),
		codexMonitors: make(map[string]struct{}), wake: make(chan struct{}, 1), qrStatusTimeout: wechatQRStatusTimeout,
		updatesPollTimeout: wechatUpdatesPollTimeout}
}

// ChannelType implements RemoteChannel interface
func (w *wechatRemote) ChannelType() string {
	return ChannelTypeWechat
}

// Status implements RemoteChannel interface
func (w *wechatRemote) Status(ctx context.Context) ChannelStatus {
	state, err := w.readState(ctx)
	if err != nil {
		return ChannelStatusDisconnected
	}
	// Check if disabled
	if state.Status == "disabled" {
		return ChannelStatusDisabled
	}
	if state.ConnectedAt > 0 {
		return ChannelStatusConnected
	}
	return ChannelStatusDisconnected
}

// attachSkillSurface 在 server.go 完成 SkillSurface 装配后注入，避免反向依赖。
func (w *wechatRemote) attachSkillSurface(s *SkillSurface) {
	if w == nil || s == nil {
		return
	}
	w.skillSurface = s
}

// attachAccountSession 把账号会话 getter 透传给 SkillSurface，替代永久为空的
// wechatSessionKey —— 后者全仓无任何写入点，导致 /tt 等依赖会话的快路径恒提示未登录。
// getter 由 server.go 用 currentSession() + sessionMatchesConfig() 合成，与 validateSession 同一回退口径。
func (w *wechatRemote) attachAccountSession(getter func() *browserSession) {
	if w == nil {
		return
	}
	w.accountSession = getter
}

// attachCommandService 把通道无关业务核心注入 wechatRemote（A-9）。
// workLoop 改为调用 commands.ProcessInbox/CollectResults/DeliverOutbox，
// 替换原 wechatRemote.handleMessage/processInbox/dispatchTask 等旧实现路径。
func (w *wechatRemote) attachCommandService(commands *RemoteCommandService) {
	if w == nil {
		return
	}
	w.commands = commands
}

// 下列方法是 wechat_codex.go 中残余 Codex 路径仍在用的 transport 委托（codex 搬迁见阶段 A-11）。
// 它们通过 commands 走通道中性实现；core.Handle 的 channelType 固定为 ChannelTypeWechat。
func (w *wechatRemote) taskByUUID(ctx context.Context, taskUUID string) (wechatTask, error) {
	if w.commands == nil {
		return wechatTask{}, errors.New("command service not attached")
	}
	return w.commands.taskByUUID(ctx, taskUUID)
}

func (w *wechatRemote) finishDispatched(ctx context.Context, id, taskUUID, reply string) error {
	if w.commands == nil {
		return errors.New("command service not attached")
	}
	return w.commands.finishDispatched(ctx, ChannelTypeWechat, id, taskUUID, reply)
}

func (w *wechatRemote) processInbox(ctx context.Context) error {
	if w.commands == nil {
		return nil
	}
	return w.commands.ProcessInbox(ctx, ChannelTypeWechat)
}

func (w *wechatRemote) collectResults(ctx context.Context) error {
	if w.commands == nil {
		return nil
	}
	return w.commands.CollectResults(ctx, ChannelTypeWechat)
}

// imBotCLISkillPath 返回当前 cliType 对应的 teambuddy-im-bot-cli Skill 绝对路径。
// 未安装或 cliType 未知时返回空字符串，调用方在 prompt 中按名称引导 CLI 加载。
func (w *wechatRemote) imBotCLISkillPath(cliType string) string {
	if w.vibeImBotSkills == nil {
		return ""
	}
	status, ok := w.vibeImBotSkills[strings.ToLower(strings.TrimSpace(cliType))]
	if !ok || !status.Ready {
		return ""
	}
	return filepath.Join(status.Root, builtinskills.ImBotCLISkillName, "SKILL.md")
}

// RegisterRoutes implements RemoteChannel
func (w *wechatRemote) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/wechat", w.getStatus)
	r.GET("/wechat/uncertain", w.listUncertain)
	r.POST("/wechat/uncertain/ack", w.ackUncertain)
	r.POST("/wechat/login", w.startLogin)
	r.GET("/wechat/login/:id", w.getLogin)
	r.POST("/wechat/login/:id/verify", w.verifyLogin)
	r.DELETE("/wechat", w.disconnect)
	// reload：在已保存凭据的情况下直接重新连接（跳过扫码）
	r.POST("/wechat/reload", w.reload)
	// S-BE001: CLI 配置 API 路由
	r.GET("/wechat/cli-config", w.getCLIConfig)
	r.PUT("/wechat/cli-config", w.saveCLIConfig)
}

// launchLoops 幂等启动三个后台循环（拉取/处理/Codex 恢复）。
// 抽成单一入口，避免 start/completeLogin/reload 三处重复拉起 goroutine 与
// "started 死锁规避"舞步（completeLoginLocked 持锁时不能再调 start()）。
// 本函数自身在锁内设置 started/rootCtx，因此可从任意已释放锁的调用点安全调用。
func (w *wechatRemote) launchLoops(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.rootCtx = ctx
	w.mu.Unlock()
	go w.pollLoop(ctx)
	go w.workLoop(ctx)
	go w.codexRecoveryLoop(ctx)
}

func (w *wechatRemote) start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.rootCtx = ctx
	w.mu.Unlock()

	// 通道已被用户停用时跳过自动启动；w.started 保持 false，
	// 后续手动重连（completeLogin）会再次调用 launchLoops 重启后台循环。
	state, err := w.readState(ctx)
	if err == nil && state.Status == "disabled" {
		applog.Info("微信通道已停用，跳过自动启动", "channel", ChannelTypeWechat)
		return
	}

	// An interrupted handoff has an unknown external outcome. Do not replay it.
	// Use channel_type filter for inbox queries
	rows, err := w.db.QueryContext(ctx, `SELECT message_id FROM gt_remote_wechat_inbox WHERE channel_type='wechat' AND status='processing'`)
	if err == nil {
		var uncertain []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				uncertain = append(uncertain, id)
			}
		}
		_ = rows.Close()
		for _, id := range uncertain {
			_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status='uncertain', updated_at=? WHERE message_id=? AND channel_type='wechat' AND status='processing'`, time.Now().UnixMilli(), id)
			_ = w.enqueue(ctx, "uncertain:"+id, "", w.tr(ctx, "remote_wechat_uncertain", nil))
		}
	}
	w.launchLoops(ctx)
}

func (w *wechatRemote) codexRecoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := w.recoverCodexResults(ctx); err != nil && ctx.Err() == nil {
			applog.Warn("恢复微信 Codex 执行结果失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *wechatRemote) stop() {
	w.mu.Lock()
	if w.attempt != nil {
		w.attempt.Cancel()
	}
	if w.pollCancel != nil {
		w.pollCancel()
	}
	if w.sendCancel != nil {
		w.sendCancel()
	}
	w.mu.Unlock()
}

func (w *wechatRemote) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// firstInboundContextToken 从一批更新里挑出第一条"用户私聊消息"的 context_token，
// 与 saveUpdates 的过滤口径一致（FromUserID==SenderID、无群组、消息类型为用户、token 非空）。
// 用于入站时触发"输入中"指示；无匹配时返回空串。
func (w *wechatRemote) firstInboundContextToken(updates wechatUpdates, state wechatState) string {
	for _, msg := range updates.Messages {
		if msg.FromUserID == state.SenderID && msg.GroupID == "" && msg.MessageType == 1 && msg.ContextToken != "" {
			return msg.ContextToken
		}
	}
	return ""
}

// sendTypingIndicator 向指定用户发送/取消"输入中"指示（typing=true 开始，false 结束）。
// 纯最佳努力：任何失败（未连接、缺凭据、getconfig 拿不到 ticket）都只记 warn，不影响主流程。
// 端点与 corespeed-io/wechatbot 的 SendTyping 对齐（/ilink/bot/sendtyping）。
func (w *wechatRemote) sendTypingIndicator(ctx context.Context, userID, contextToken string, typing bool) {
	if userID == "" || contextToken == "" {
		return
	}
	state, err := w.readState(ctx)
	if !isConnectedState(state, err) {
		return
	}
	token, err := w.store.Get(wechatBotTokenKey)
	if err != nil || token == "" {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticket, err := w.api.getConfig(callCtx, state.BaseURL, token, userID, contextToken)
	if err != nil || ticket == "" {
		applog.Warn("微信获取输入中票据失败（已忽略）", "error", err, "user_id", userID)
		return
	}
	status := 1
	if !typing {
		status = 2
	}
	if err := w.api.sendTyping(callCtx, state.BaseURL, token, userID, ticket, status); err != nil {
		applog.Warn("微信发送输入中指示失败（已忽略）", "error", err, "user_id", userID, "status", status)
	}
}

func (w *wechatRemote) readState(ctx context.Context) (wechatState, error) {
	var state wechatState
	// First try the new unified table
	err := w.db.QueryRowContext(ctx, `
		SELECT bot_id, sender_id, locale, base_url, cursor, bound_task_uuid,
		pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
		context_revision, connected_at, status
		FROM gt_remote_channel_state WHERE channel_type='wechat'`).Scan(
		&state.BotID, &state.SenderID, &state.Locale, &state.BaseURL, &state.Cursor, &state.BoundTaskUUID,
		&state.PendingCancelTaskUUID, &state.PendingResumeTaskUUID, &state.PendingDoneTaskUUID,
		&state.ContextRevision, &state.ConnectedAt, &state.Status)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return wechatState{}, err
	}

	// Fallback to old single-row table for backwards compatibility
	var oldState struct {
		BotID, SenderID, Locale, BaseURL, Cursor, BoundTaskUUID string
		ContextRevision, ConnectedAt                            int64
	}
	err = w.db.QueryRowContext(ctx, `
		SELECT bot_id, sender_id, locale, base_url, cursor, bound_task_uuid,
		context_revision, connected_at FROM gt_remote_wechat_state WHERE id=1`).Scan(
		&oldState.BotID, &oldState.SenderID, &oldState.Locale, &oldState.BaseURL, &oldState.Cursor,
		&oldState.BoundTaskUUID, &oldState.ContextRevision, &oldState.ConnectedAt)
	if err != nil {
		return wechatState{}, err
	}

	// Convert old state to new format
	state = wechatState{
		BotID:           oldState.BotID,
		SenderID:        oldState.SenderID,
		Locale:          oldState.Locale,
		BaseURL:         oldState.BaseURL,
		Cursor:          oldState.Cursor,
		BoundTaskUUID:   oldState.BoundTaskUUID,
		ContextRevision: oldState.ContextRevision,
		ConnectedAt:     oldState.ConnectedAt,
		Status:          "connected",
	}
	if state.ConnectedAt == 0 {
		state.Status = "disconnected"
	}
	return state, nil
}

// isConnectedState 判断微信通道是否已真正连接。
// 必须同时校验 BotID 与 ConnectedAt，避免仅有空 state 行（未连接时被创建）时误判为已连接。
func isConnectedState(state wechatState, err error) bool {
	if err != nil {
		return false
	}
	// If status is explicitly set, use it
	if state.Status == "connected" {
		return true
	}
	// Legacy fallback: check connected_at
	return state.BotID != "" && state.ConnectedAt > 0
}

// remoteExecutionBlock 守卫：同一任务任意远程通道有进行中会话时，拒绝本地发起变更。
// 故意不限定 channel_type：本地修改需要考虑企微通道的有源会话。
func remoteExecutionBlock(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskUUID string) (string, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM gt_remote_wechat_inbox
		WHERE task_uuid=? AND status IN ('processing','dispatched','uncertain')
		ORDER BY CASE WHEN status='uncertain' THEN 1 ELSE 0 END, received_at LIMIT 1`, taskUUID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

func guardRemoteExecutionMutation(c *gin.Context, db *sql.DB, taskUUID string) bool {
	status, err := remoteExecutionBlock(c.Request.Context(), db, taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return false
	}
	if status == "uncertain" {
		i18n.Error(c, http.StatusConflict, "remote_wechat_uncertain_action_required", "wechat_uncertain_action_required")
		return false
	}
	if status != "" {
		i18n.Error(c, http.StatusConflict, "localserver_active_sessions", "task_has_active_sessions")
		return false
	}
	return true
}

func (w *wechatRemote) tr(ctx context.Context, key string, params i18n.Params) string {
	state, err := w.readState(ctx)
	if err != nil {
		return i18n.FormatWithLocale("zh-CN", key, params)
	}
	return i18n.FormatWithLocale(state.Locale, key, params)
}

// S-BE001: getStatus 扩展，返回 CLI 默认配置
func (w *wechatRemote) getStatus(c *gin.Context) {
	state, err := w.readState(c.Request.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	w.mu.Lock()
	attempt := w.attempt
	lastError := w.lastError
	replyMessageIDMissing := w.replyMessageIDMissing
	loginID, loginStatus := "", ""
	if attempt != nil {
		loginID, loginStatus = attempt.ID, attempt.Status
	}
	w.mu.Unlock()
	status := "disconnected"
	if err == nil && state.ConnectedAt > 0 {
		status = "connected"
	} else if loginID != "" && loginStatus != "expired" && loginStatus != "error" {
		status = "connecting"
	}
	// 检查 secrets store 中是否有凭据（bot token），用于前端判断是否可以直接重连
	hasCredential := false
	if token, tokenErr := w.store.Get(wechatBotTokenKey); tokenErr == nil && token != "" {
		hasCredential = true
	}
	// S-BE001: 返回 CLI 默认配置，从独立 cli_config 表读取，不依赖 state 连接状态
	cliCfg := w.readChannelCLIConfig(c.Request.Context(), "wechat")
	c.JSON(http.StatusOK, gin.H{
		"status":                   status,
		"connected_at":             state.ConnectedAt,
		"login_id":                 loginID,
		"last_error":               lastError,
		"reply_message_id_missing": status == "connected" && replyMessageIDMissing,
		"has_credential":           hasCredential,
		"cli_default_type":         cliCfg.CLICType,
		"cli_default_model":        cliCfg.CLIModel,
	})
}

func (w *wechatRemote) listUncertain(c *gin.Context) {
	rows, err := w.db.QueryContext(c.Request.Context(), `SELECT i.message_id, i.task_uuid, COALESCE(t.title,''), i.received_at
		FROM gt_remote_wechat_inbox i LEFT JOIN gt_tasks t ON t.uuid=i.task_uuid
		WHERE i.channel_type='wechat' AND i.status='uncertain' ORDER BY i.received_at, i.rowid`)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var messageID, taskUUID, title string
		var receivedAt int64
		if err := rows.Scan(&messageID, &taskUUID, &title, &receivedAt); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
			return
		}
		items = append(items, gin.H{"message_id": messageID, "task_uuid": taskUUID, "task_title": title, "received_at": receivedAt})
	}
	if rows.Err() != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (w *wechatRemote) ackUncertain(c *gin.Context) {
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.MessageID) == "" {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "")
		return
	}
	result, err := w.db.ExecContext(c.Request.Context(), `UPDATE gt_remote_wechat_inbox SET status='acknowledged', updated_at=?
		WHERE message_id=? AND channel_type='wechat' AND status='uncertain'`, time.Now().UnixMilli(), body.MessageID)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	updated, err := result.RowsAffected()
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	if updated == 0 {
		i18n.Error(c, http.StatusConflict, "remote_wechat_uncertain_ack_missing", "wechat_uncertain_ack_missing")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "acknowledged"})
}

func (w *wechatRemote) startLogin(c *gin.Context) {
	// 不再拒绝已连接：标题栏「重新扫码」入口需要在已连接时也能生成新二维码，
	// completeLogin 会在保存新凭据前清掉旧连接。等同于一次受控的「断 + 重连」。
	if _, err := w.readState(c.Request.Context()); err != nil && !errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	qr, err := w.api.getQR(c.Request.Context())
	if err != nil {
		i18n.Error(c, http.StatusBadGateway, "remote_wechat_qr_error", "wechat_qr_error")
		return
	}
	png, err := qrcode.Encode(qr.ImageContent, qrcode.Medium, 224)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_qr_render_error", "wechat_qr_error")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	locale := i18n.Normalize(c.GetHeader("lang"))
	attempt := &wechatLoginAttempt{ID: uuid.NewString(), QRCode: qr.QRCode, ImageDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), Status: "waiting", BaseURL: wechatDefaultBaseURL, Locale: locale, CreatedAt: time.Now(), Cancel: cancel, Wake: make(chan struct{}, 1)}
	w.mu.Lock()
	// 标题栏「重新扫码」入口：已连接时也允许生成新二维码。completeLogin 在保存
	// 新凭据前会先清掉旧连接，避免两个连接并存。
	if _, err := w.readState(c.Request.Context()); err != nil && !errors.Is(err, sql.ErrNoRows) {
		w.mu.Unlock()
		cancel()
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	if w.attempt != nil {
		w.attempt.Cancel()
	}
	w.attempt = attempt
	w.lastError = ""
	w.mu.Unlock()
	go w.pollLogin(ctx, attempt)
	c.JSON(http.StatusCreated, gin.H{"id": attempt.ID, "status": attempt.Status, "qr_image": attempt.ImageDataURL})
}

func (w *wechatRemote) getLogin(c *gin.Context) {
	w.mu.Lock()
	attempt := w.attempt
	if attempt == nil || attempt.ID != c.Param("id") {
		w.mu.Unlock()
		i18n.Error(c, http.StatusNotFound, "remote_wechat_login_not_found", "wechat_login_not_found")
		return
	}
	response := gin.H{"id": attempt.ID, "status": attempt.Status, "qr_image": attempt.ImageDataURL, "error": attempt.Error}
	w.mu.Unlock()
	c.JSON(http.StatusOK, response)
}

func (w *wechatRemote) verifyLogin(c *gin.Context) {
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(strings.TrimSpace(body.Code)) < 4 || len(strings.TrimSpace(body.Code)) > 16 {
		i18n.Error(c, http.StatusBadRequest, "remote_wechat_verify_invalid", "wechat_verify_invalid")
		return
	}
	w.mu.Lock()
	attempt := w.attempt
	if attempt == nil || attempt.ID != c.Param("id") || attempt.Status != "need_verifycode" {
		w.mu.Unlock()
		i18n.Error(c, http.StatusConflict, "remote_wechat_login_not_waiting", "wechat_login_not_waiting")
		return
	}
	attempt.VerifyCode = strings.TrimSpace(body.Code)
	attempt.Status = "verifying"
	select {
	case attempt.Wake <- struct{}{}:
	default:
	}
	w.mu.Unlock()
	c.JSON(http.StatusAccepted, gin.H{"status": "verifying"})
}

func (w *wechatRemote) pollLogin(ctx context.Context, attempt *wechatLoginAttempt) {
	defer attempt.Cancel()
	loginCtx, cancelDeadline := context.WithDeadline(ctx, attempt.CreatedAt.Add(5*time.Minute))
	defer cancelDeadline()
loginPoll:
	for loginCtx.Err() == nil {
		w.mu.Lock()
		if w.attempt != attempt {
			w.mu.Unlock()
			return
		}
		base, code := attempt.BaseURL, attempt.VerifyCode
		w.mu.Unlock()
		// The upstream endpoint can hold an unchanged status for a long poll. A
		// shorter deadline makes a fresh request observe a scan confirmation
		// without waiting for the old long poll to finish.
		requestCtx, cancelRequest := context.WithTimeout(loginCtx, w.qrStatusTimeout)
		status, err := w.api.qrStatus(requestCtx, base, attempt.QRCode, code)
		requestTimedOut := errors.Is(requestCtx.Err(), context.DeadlineExceeded)
		cancelRequest()
		if ctx.Err() != nil {
			return
		}
		if loginCtx.Err() != nil {
			break
		}
		if err != nil && requestTimedOut {
			continue
		}
		if err != nil {
			select {
			case <-loginCtx.Done():
				if ctx.Err() != nil {
					return
				}
				break loginPoll
			case <-time.After(2 * time.Second):
				continue
			}
		}
		w.mu.Lock()
		if w.attempt != attempt {
			w.mu.Unlock()
			return
		}
		if code != "" {
			attempt.VerifyCode = ""
		}
		switch status.Status {
		case "confirmed":
			w.mu.Unlock()
			w.completeLogin(attempt, status)
			return
		case "scaned_but_redirect":
			if status.RedirectHost != "" {
				redirect := status.RedirectHost
				if !strings.HasPrefix(redirect, "https://") {
					redirect = "https://" + redirect
				}
				if next, err := validWeChatBase(redirect); err == nil {
					attempt.BaseURL = next
				}
			}
			attempt.Status = "scanned"
		case "scaned":
			attempt.Status = "scanned"
		case "need_verifycode":
			attempt.Status = "need_verifycode"
		case "verify_code_blocked", "expired":
			attempt.Status = "expired"
			w.mu.Unlock()
			return
		case "binded_redirect":
			attempt.Status = "error"
			attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
			w.mu.Unlock()
			return
		default:
			attempt.Status = "waiting"
		}
		needVerify := attempt.Status == "need_verifycode"
		w.mu.Unlock()
		if needVerify {
			select {
			case <-loginCtx.Done():
				if ctx.Err() != nil {
					return
				}
				break loginPoll
			case <-attempt.Wake:
			}
		} else {
			select {
			case <-loginCtx.Done():
				if ctx.Err() != nil {
					return
				}
				break loginPoll
			case <-time.After(time.Second):
			}
		}
	}
	w.mu.Lock()
	if w.attempt == attempt {
		attempt.Status = "expired"
	}
	w.mu.Unlock()
}

func (w *wechatRemote) completeLogin(attempt *wechatLoginAttempt, status wechatQRStatus) {
	needStart, startCtx := w.completeLoginLocked(attempt, status)
	if needStart {
		if startCtx == nil {
			// 兜底：start() 一次都没跑过（理论上 server.StartRemoteChannels
			// 会先调一次），避免给 nil ctx 拉 goroutine。
			startCtx = context.Background()
		}
		// launchLoops 在锁内原子设置 started/rootCtx 并启动三个后台循环，
		// 解决了「completeLoginLocked 持锁时不能再调 start()」的死锁问题——现在直接走统一入口即可。
		w.launchLoops(startCtx)
	}
}

// completeLoginLocked 在持锁状态下保存新凭据、状态。
// 返回值 needStart/startCtx 供外层 completeLogin 在锁外调用 launchLoops 原子翻 started 并启动
// pollLoop / workLoop / codexRecoveryLoop（不再直接调 start()，避免与 completeLoginLocked 的持锁死锁）。
func (w *wechatRemote) completeLoginLocked(attempt *wechatLoginAttempt, status wechatQRStatus) (bool, context.Context) {
	if status.BotID == "" || status.UserID == "" || status.BotToken == "" {
		w.loginFailed(attempt, "remote_wechat_connect_failed")
		return false, nil
	}
	base := status.BaseURL
	if base == "" {
		base = wechatDefaultBaseURL
	}
	trusted, err := validWeChatBase(base)
	if err != nil {
		w.loginFailed(attempt, "remote_wechat_connect_failed")
		return false, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.attempt != attempt {
		return false, nil
	}
	// 重新扫码场景：若已连接，先清掉旧连接（停止轮询、清除凭据、清队列）
	// 再继续保存新凭据。isConnectedState 同时校验 BotID 与 ConnectedAt，
	// 避免对未连接的空 state 行误判。
	//
	// clearConnection 内部需要 w.mu 锁，当前 goroutine 已持 w.mu，会立即死锁。
	// 改为：在 goroutine 中延迟清理（先保存新凭据、让 start() 接管后台循环，
	// 再异步停止旧连接）。代价是旧连接在短时间内可能仍在轮询，但不会卡死主流程。
	if state, err := w.readState(context.Background()); isConnectedState(state, err) {
		go func() {
			// 让新连接先完成保存与启动，再清理旧连接。
			time.Sleep(200 * time.Millisecond)
			if _, clearErr := w.clearConnection(context.Background(), nil, true, "", true); clearErr != nil {
				applog.Warn("清理旧微信连接失败", "error", clearErr)
			}
		}()
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	previousBotToken, botErr := w.store.Get(wechatBotTokenKey)
	if botErr != nil && !errors.Is(botErr, secrets.ErrNotFound) {
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	previousContextToken, contextErr := w.store.Get(wechatContextTokenKey)
	if contextErr != nil && !errors.Is(contextErr, secrets.ErrNotFound) {
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	restoreCredentials := func() {
		if botErr == nil {
			_ = w.store.Set(wechatBotTokenKey, previousBotToken)
		} else {
			_ = w.store.Delete(wechatBotTokenKey)
		}
		if contextErr == nil {
			_ = w.store.Set(wechatContextTokenKey, previousContextToken)
		}
	}
	if err = w.store.Set(wechatBotTokenKey, status.BotToken); err != nil {
		restoreCredentials()
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	if err = w.store.Delete(wechatContextTokenKey); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		restoreCredentials()
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	now := time.Now().UnixMilli()
	// 旧表 gt_remote_wechat_state.id 是 INTEGER PRIMARY KEY（ROWID 别名），
	// clearConnection 用 UPDATE 清空、不删除行；首次连接后 id=1 永远存在。
	// 用 ON CONFLICT(id) DO UPDATE 兜底重连场景，否则第二次扫码会因
	// UNIQUE constraint failed 走到 remote_wechat_connect_failed 错误分支。
	_, err = w.db.Exec(`INSERT INTO gt_remote_wechat_state
		(id, bot_id, sender_id, locale, base_url, cursor, bound_task_uuid, context_revision, connected_at, updated_at)
		VALUES (1, ?, ?, ?, ?, '', '', 0, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			bot_id=excluded.bot_id,
			sender_id=excluded.sender_id,
			locale=excluded.locale,
			base_url=excluded.base_url,
			cursor='',
			bound_task_uuid='',
			context_revision=0,
			connected_at=excluded.connected_at,
			updated_at=excluded.updated_at`,
		status.BotID, status.UserID, attempt.Locale, trusted, now, now)
	if err != nil {
		restoreCredentials()
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	// 同步更新统一通道状态表，置为 connected 并写入连接信息。
	// 旧表 gt_remote_wechat_state 仍保留以兼容历史代码；状态接口以新表为准。
	if _, err = w.db.Exec(`INSERT INTO gt_remote_channel_state
		(channel_type, bot_id, sender_id, locale, base_url, cursor, bound_task_uuid,
		 pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
		 context_revision, connected_at, status, updated_at)
		VALUES ('wechat', ?, ?, ?, ?, '', '', '', '', '', 0, ?, 'connected', ?)
		ON CONFLICT(channel_type) DO UPDATE SET
			bot_id=excluded.bot_id,
			sender_id=excluded.sender_id,
			locale=excluded.locale,
			base_url=excluded.base_url,
			cursor=excluded.cursor,
			bound_task_uuid=excluded.bound_task_uuid,
			pending_cancel_task_uuid='',
			pending_resume_task_uuid='',
			pending_done_task_uuid='',
			context_revision=0,
			connected_at=excluded.connected_at,
			status='connected',
			updated_at=excluded.updated_at`,
		status.BotID, status.UserID, attempt.Locale, trusted, now, now); err != nil {
		restoreCredentials()
		_, _ = w.db.Exec(`DELETE FROM gt_remote_wechat_state WHERE id=1`)
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, "remote_wechat_connect_failed")
		return false, nil
	}
	attempt.Status = "connected"
	w.replyMessageIDMissing = false
	// 之前若处于 disabled 状态，start() 会跳过后台循环的拉起；
	// 连接恢复后需要重新启动循环，才能正常收发消息。
	//
	// 不能再本函数内直接翻 w.started=true 并交给外层 start()：
	// completeLoginLocked 通过 defer 持有 w.mu，start() 入口会再次 w.mu.Lock()，
	// 先翻标志会让 start() 的 w.started guard 直接早退，pollLoop/workLoop/
	// codexRecoveryLoop 都不启动——「disabled → 扫码成功 → UI 显示 connected 但
	// 永远不拉消息」的 bug 就是这么来的。改为只把 needStart 与 startCtx 返回，
	// 由外层 completeLogin 在锁外直接拉起三个长循环（与 reload() 同款）。
	startCtx := w.rootCtx
	needStart := !w.started
	// signal 在锁内执行（select+default，非阻塞，耗时微秒级）。
	w.signal()
	return needStart, startCtx
}

func (w *wechatRemote) loginFailed(attempt *wechatLoginAttempt, key string) {
	w.mu.Lock()
	if w.attempt == attempt {
		attempt.Status = "error"
		attempt.Error = i18n.WithLocale(attempt.Locale, key)
		w.lastError = key
	}
	w.mu.Unlock()
}

func (w *wechatRemote) disconnect(c *gin.Context) {
	// clearCredentials=true：用户主动停止时清除凭据，下次重连需要重新扫码（等同完全解绑）
	notified, err := w.clearConnection(c.Request.Context(), nil, true, "", true)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_disconnect_failed", "wechat_disconnect_error")
		return
	}
	// clearConnection 在 notifyStop=true 时已把统一状态表 status 置为 disabled，
	// 这里同步推送 disabled，避免前端停留在 connected 触发轮询错误提示。
	w.broadcastConnectionStatus("disabled")
	c.JSON(http.StatusOK, gin.H{"status": "disabled", "wechat_notified": notified})
}

// reload 在已保存凭据的情况下直接重新连接（跳过扫码）。
// 用于 disabled 状态下点击"重新连接"按钮。
func (w *wechatRemote) reload(c *gin.Context) {
	ctx := c.Request.Context()

	// 检查是否有凭据
	token, tokenErr := w.store.Get(wechatBotTokenKey)
	if tokenErr != nil || token == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_wechat_no_credential", "wechat_no_credential")
		return
	}

	// 读取当前状态，确保是 disabled 状态
	state, stateErr := w.readState(ctx)
	if stateErr == nil && state.Status != "disabled" {
		// 通道不是 disabled 状态，可能已连接或有其他问题
		i18n.Error(c, http.StatusConflict, "remote_wechat_not_disabled", "wechat_not_disabled")
		return
	}

	// 直接使用 SQL 更新状态为 connecting
	now := time.Now().UnixMilli()
	_, err := w.db.ExecContext(ctx, `
		INSERT INTO gt_remote_channel_state
			(channel_type, bot_id, sender_id, locale, base_url, cursor, bound_task_uuid,
			 pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
			 context_revision, connected_at, status, updated_at)
			VALUES ('wechat', '', '', '', '', '', '', '', '', '', 0, 0, 'connecting', ?)
			ON CONFLICT(channel_type) DO UPDATE SET
				status='connecting',
				updated_at=?`,
		now, now)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_update_failed", "wechat_state_update_failed")
		return
	}

	// 通知前端状态变化
	w.broadcastConnectionStatus("connecting")

	// 触发重新连接：如果后台循环未启动则统一启动，否则 signal() 唤醒重连逻辑
	if !w.started {
		w.launchLoops(ctx)
	} else {
		w.signal()
	}

	c.JSON(http.StatusOK, gin.H{"status": "connecting"})
}

func (w *wechatRemote) broadcastConnectionStatus(status string) {
	if w.tasks == nil || w.tasks.wsHub == nil {
		return
	}
	w.tasks.wsHub.BroadcastToAll("remote.wechat.status", map[string]string{"status": status})
}

// clearConnection stops local work for one connection. The expected snapshot
// prevents a late invalid-token response from clearing a newer QR login.
// clearCredentials: true=清除凭据（重新扫码场景），false=保留凭据（用户断开后重新连接）
//
// 锁结构：dispatchMu 保护 dispatch 暂停，w.mu 保护共享状态。两者都必须在 HTTP
// 通知之前释放，避免 sendOfflineNotification / notifyLifecycle 同步 HTTP
// 调用阻塞 pollLoop / getStatus 等需要锁的操作。
func (w *wechatRemote) clearConnection(ctx context.Context, expected *wechatState, notifyStop bool, reason string, clearCredentials bool) (bool, error) {
	w.dispatchMu.Lock()
	w.mu.Lock()
	state, stateErr := w.readState(ctx)
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		w.mu.Unlock()
		w.dispatchMu.Unlock()
		return false, stateErr
	}
	if expected != nil && (stateErr != nil || state.BotID != expected.BotID || state.ConnectedAt != expected.ConnectedAt) {
		w.mu.Unlock()
		w.dispatchMu.Unlock()
		return false, nil
	}
	if w.attempt != nil {
		w.attempt.Cancel()
		w.attempt = nil
	}
	if w.pollCancel != nil {
		w.pollCancel()
		w.pollCancel = nil
	}
	if w.sendCancel != nil {
		w.sendCancel()
		w.sendCancel = nil
	}
	notified := errors.Is(stateErr, sql.ErrNoRows)

	// 业务消息必须先发再清理：
	// 1) sendOfflineNotification 内部的 w.tr 需要 readState 拿 locale，
	//    事务一旦提交 state 表的 locale 已被清空，会强制降级到 zh-CN；
	// 2) notifyLifecycle 也依赖 state.BaseURL，提前发避免空 base_url 调用；
	// 3) 业务通知先于状态变更，让用户在微信侧能感知到「AI员工已下线」。
	// 外层锁 w.mu / dispatchMu 仍持有，发送期间不会与 pollLoop 竞争；
	// offlineCtx 给 5s 与 sendOnlineNotification 对齐，避免 iLink 偶发慢响应
	// 把超时搞得很紧。
	if notifyStop && stateErr == nil {
		offlineCtx, offlineCancel := context.WithTimeout(ctx, 5*time.Second)
		w.sendOfflineNotification(offlineCtx, state)
		offlineCancel()
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		w.mu.Unlock()
		w.dispatchMu.Unlock()
		return notified, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bot_id='', sender_id='', cursor='', bound_task_uuid='',
		context_revision=0, connected_at=0, base_url='', locale='' WHERE id=1`); err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_outbox SET status='cancelled' WHERE channel_type='wechat' AND status='pending'`)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status='cancelled', updated_at=? WHERE channel_type='wechat' AND status='pending'`, time.Now().UnixMilli())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status='uncertain', updated_at=? WHERE channel_type='wechat' AND status IN ('processing','dispatched')`, time.Now().UnixMilli())
	}
	// 同步更新统一通道状态表。notifyStop=true 表示用户主动停用，
	// 否则（pollLoop 凭据失效、上游返回 401/403）只标记 disconnected，
	// 避免凭据过期被错误地标记为「用户停用」、屏蔽后续重连。
	newStatus := "disconnected"
	if notifyStop {
		newStatus = "disabled"
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_remote_channel_state
			(channel_type, bot_id, sender_id, locale, base_url, cursor, bound_task_uuid,
			 pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
			 context_revision, connected_at, status, updated_at)
			VALUES ('wechat', '', '', '', '', '', '', '', '', '', 0, 0, ?, ?)
			ON CONFLICT(channel_type) DO UPDATE SET
				bot_id='', sender_id='', locale='', base_url='', cursor='', bound_task_uuid='',
				pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='',
				context_revision=0, connected_at=0, status=excluded.status, updated_at=excluded.updated_at`,
			newStatus, time.Now().UnixMilli())
	}
	if err != nil {
		w.mu.Unlock()
		w.dispatchMu.Unlock()
		return notified, err
	}
	if err = tx.Commit(); err != nil {
		w.mu.Unlock()
		w.dispatchMu.Unlock()
		return notified, err
	}

	// 在锁内更新 lastError / replyMessageIDMissing，避免与其他读路径竞态。
	w.lastError = reason
	w.replyMessageIDMissing = false

	// 先释放锁再做 HTTP 通知 + 删凭据。
	// 短超时（3 秒）确保即使网络异常也不会长时间阻塞 disconnect。
	// 顺序：先发 iLink 通知（需要 token），再删凭据（避免通知时拿不到 token）。
	w.mu.Unlock()
	w.dispatchMu.Unlock()

	if notifyStop && stateErr == nil {
		offlineToken := ""
		if tok, tokErr := w.store.Get(wechatBotTokenKey); tokErr == nil && tok != "" {
			offlineToken = tok
		}

		// 通知 iLink 上游（必须在删凭据之前，否则拿不到 token）
		if offlineToken != "" {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			notifyErr := w.api.notifyLifecycle(callCtx, state.BaseURL, offlineToken, false)
			cancel()
			notified = notifyErr == nil
			if notifyErr != nil {
				applog.Warn("微信通道停止通知失败", "error", notifyErr)
			}
		}
	}

	// 锁外删除凭据（store 本身是线程安全），不影响其他操作。
	if clearCredentials {
		botErr := w.store.Delete(wechatBotTokenKey)
		contextErr := w.store.Delete(wechatContextTokenKey)
		if (botErr != nil && !errors.Is(botErr, secrets.ErrNotFound)) ||
			(contextErr != nil && !errors.Is(contextErr, secrets.ErrNotFound)) {
			applog.Warn("删除微信通道凭据失败", "error", errors.Join(botErr, contextErr))
		}
	}

	// signal 在锁外调用
	w.signal()
	return notified, nil
}

func (w *wechatRemote) pollLoop(ctx context.Context) {
	announcedConnection := ""
	announcedOnlineNotification := ""
	for ctx.Err() == nil {
		// S-BE001-fix: 用 isConnectedState 判断连接状态，避免仅有空 state 行（未连接时
		// saveCLIConfig upsert 产生的占位行）时反复 setError 推送凭据缺失提示。
		state, err := w.readState(ctx)
		if !isConnectedState(state, err) {
			announcedConnection = ""
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
				continue
			case <-time.After(3 * time.Second):
				continue
			}
		}
		token, err := w.store.Get(wechatBotTokenKey)
		if err != nil || token == "" {
			w.setError("wechat_credential_missing")
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
				continue
			case <-time.After(5 * time.Second):
				continue
			}
		}
		connectionID := fmt.Sprintf("%s:%d", state.BotID, state.ConnectedAt)
		if announcedConnection != connectionID {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			notifyErr := w.api.notifyLifecycle(callCtx, state.BaseURL, token, true)
			cancel()
			if notifyErr != nil {
				applog.Warn("微信通道启动通知失败", "error", notifyErr)
			} else {
				announcedConnection = connectionID
				// 上线用户通知（与 feishu/wecom/dingtalk 一致）：iLink 主动 sendmessage 通知
				// 上一个对话过的人。失败仅记 warn，不影响连接状态。
				if announcedOnlineNotification != connectionID {
					if ctx.Err() == nil {
						w.sendOnlineNotification(ctx)
					}
					announcedOnlineNotification = connectionID
				}
			}
		}
		// A bounded long poll periodically rechecks token validity even when the
		// upstream server keeps an old request open after a WeChat-side change.
		pollCtx, cancel := context.WithTimeout(ctx, w.updatesPollTimeout)
		w.mu.Lock()
		w.pollCancel = cancel
		w.mu.Unlock()
		updates, err := w.api.getUpdates(pollCtx, state.BaseURL, token, state.Cursor)
		pollErr := pollCtx.Err()
		cancel()
		w.mu.Lock()
		w.pollCancel = nil
		w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var httpErr *wechatHTTPError
			if errors.Is(err, errWeChatSessionExpired) || (errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403)) {
				// 会话失效时清除凭据，下次需要重新扫码
				if _, clearErr := w.clearConnection(ctx, &state, false, "wechat_session_expired", true); clearErr != nil {
					applog.Warn("清理失效微信连接失败", "error", clearErr)
				} else if clearedState, stateErr := w.readState(ctx); stateErr == nil && clearedState.ConnectedAt == 0 {
					w.broadcastConnectionStatus("disconnected")
				}
				announcedConnection = ""
				announcedOnlineNotification = ""
				continue
			}
			if errors.Is(pollErr, context.DeadlineExceeded) {
				continue
			}
			if pollErr == nil {
				w.setError("wechat_poll_error")
			}
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
				continue
			case <-time.After(3 * time.Second):
				continue
			}
		}
		w.setError("")
		if err = w.saveUpdates(ctx, state, updates); err != nil {
			applog.Warn("保存微信消息失败", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		} else {
			// 入站消息落库后立即唤醒 workLoop 处理（消除秒级 tick 延迟），
			// 并向用户发送"输入中"指示，体验对齐钉钉的即时反馈。
			if ct := w.firstInboundContextToken(updates, state); ct != "" {
				w.sendTypingIndicator(ctx, state.SenderID, ct, true)
			}
			w.signal()
		}
	}
}

func (w *wechatRemote) setError(message string) {
	w.mu.Lock()
	w.lastError = message
	w.mu.Unlock()
}

func (w *wechatRemote) setReplyMessageIDMissing(ctx context.Context, state wechatState, missing bool) {
	w.mu.Lock()
	current, err := w.readState(ctx)
	if err != nil || current.BotID != state.BotID || current.ConnectedAt != state.ConnectedAt {
		w.mu.Unlock()
		return
	}
	changed := w.replyMessageIDMissing != missing
	w.replyMessageIDMissing = missing
	w.mu.Unlock()
	if changed {
		w.broadcastConnectionStatus("connected")
	}
}

func (w *wechatRemote) saveUpdates(ctx context.Context, state wechatState, updates wechatUpdates) error {
	// 校验和轻量预查放锁内；写操作（EnqueueInbox + cursor/context_revision 推进）放锁外单事务。
	// 之前在锁内做了多个 BeginTx/EnqueueInbox 且用 defer tx.Rollback() 捕获变量名导致
	// 第一个事务永远不被关闭，长时间持有 SQLite 读锁阻塞后续写入，表现为"消息收到后卡死"。
	w.mu.Lock()
	var currentBotID string
	if err := w.db.QueryRowContext(ctx, `SELECT bot_id FROM gt_remote_wechat_state WHERE id=1`).Scan(&currentBotID); err != nil {
		w.mu.Unlock()
		return err
	}
	if currentBotID != state.BotID {
		w.mu.Unlock()
		return errors.New("微信连接已更换")
	}
	// The credential-like context token is written before advancing the cursor.
	// store 本身线程安全，可在锁内直接写。
	//
	// iLink 协议里 client_id 只是发送侧的幂等标记，参考 corespeed-io/wechatbot 每次发送都
	// 生成新的 UUID；持久化入站 client_id 反而会因会话窗口不匹配被 iLink 静默丢弃。
	// 因此 saveUpdates 不再单独存 client_id；outbox 投递时也无需再传（wechatAPI.sendText 内部 newUUID）。
	for _, msg := range updates.Messages {
		if msg.FromUserID == state.SenderID && msg.GroupID == "" && msg.ContextToken != "" {
			if err := w.store.Set(wechatContextTokenKey, msg.ContextToken); err != nil {
				w.mu.Unlock()
				return err
			}
		}
	}
	w.mu.Unlock()

	// 锁外单事务：校验 bot_id → 写 inbox → 推进 cursor/context_revision。
	// 整个事务内不再调用 w.db 直连，避免之前 EnqueueInbox 写在事务外的脏写。
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var botID string
	if err = tx.QueryRowContext(ctx, `SELECT bot_id FROM gt_remote_wechat_state WHERE id=1`).Scan(&botID); err != nil {
		return err
	}
	if botID != state.BotID {
		return errors.New("微信连接已更换")
	}
	now := time.Now().UnixMilli()
	var needsContextRevisionBump bool
	for _, msg := range updates.Messages {
		// 过滤非私聊消息、空 ID（含零值 "0"）和空内容
		msgID := msg.id()
		if msg.FromUserID != state.SenderID || msg.GroupID != "" || msg.MessageType != 1 || msgID == "" || msgID == "0" || msg.text() == "" {
			continue
		}
		// 在事务内 INSERT inbox，保证与 cursor 推进的原子性：
		// 消息写成功才推进 cursor，崩溃恢复不会丢消息也不会重复入队。
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO `+remoteInboxTable+`
			(message_id, text_content, status, task_uuid, session_uuid, next_recovery_at, recovery_attempts,
			 received_at, updated_at, channel_type, sender_id, session_webhook)
			VALUES (?, ?, 'pending', '', '', 0, 0, ?, ?, ?, ?, ?)`,
			state.BotID+":"+msgID, msg.text(), now, now, ChannelTypeWechat, state.SenderID, ""); err != nil {
			return err
		}
		if msg.ContextToken != "" {
			needsContextRevisionBump = true
		}
	}
	if needsContextRevisionBump {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET context_revision=context_revision+1 WHERE id=1`); err != nil {
			return err
		}
	}
	if updates.Cursor != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET cursor=?, updated_at=? WHERE id=1`, updates.Cursor, now); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (w *wechatRemote) workLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		// 计划 §七：workLoop 仅做调度，不持有 per-message 状态；
		// 业务逻辑交给 RemoteCommandService（wechat 走 channelType='wechat'）。
		// commands 未注入时退回到旧 processInbox/collectResults/deliverOutbox，避免装配顺序错乱时启动失败。
		if w.commands != nil {
			if err := w.commands.ProcessInbox(ctx, ChannelTypeWechat); err != nil {
				applog.Warn("处理微信指令失败", "error", err)
			}
			if err := w.commands.CollectResults(ctx, ChannelTypeWechat); err != nil {
				applog.Warn("收集微信任务结果失败", "error", err)
			}
			if err := w.commands.DeliverOutbox(ctx, ChannelTypeWechat,
				OutboxDeliverFuncAdapter{
					Revision: func(_ context.Context) int64 {
						state, err := w.readState(ctx)
						if err != nil {
							return -1
						}
						return state.ContextRevision
					},
					Send: func(sendCtx context.Context, content, replyContext string) error {
						return w.sendOutboxContent(sendCtx, content, replyContext)
					},
				}); err != nil {
				applog.Warn("发送微信消息失败", "error", err)
			}
		} else {
			if err := w.processInbox(ctx); err != nil {
				applog.Warn("处理微信指令失败", "error", err)
			}
			if err := w.collectResults(ctx); err != nil {
				applog.Warn("收集微信任务结果失败", "error", err)
			}
			if err := w.deliverOutbox(ctx); err != nil {
				applog.Warn("发送微信消息失败", "error", err)
			}
		}
		// 事件驱动：pollLoop 收到用户消息会通过 w.signal() 立即唤醒本循环，
		// 让 ProcessInbox→DeliverOutbox 在消息落库后立刻执行（不再等 1 秒 tick）。
		// 兜底间隔仅保障 CollectResults（长任务结果回传）等无法被事件触发的路径仍能定期推进。
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-time.After(workLoopFallbackInterval):
		}
	}
}

// sendOutboxContent 把单条 outbox 内容实际发出，是 RemoteCommandService.DeliverOutbox 的适配层。
// replyContext 由 buildReplyContext(ChannelTypeWechat) 写入 outbox，格式为 "sender_id:USERID"，
// 来自触发本次回复的入站消息对应的 gt_remote_wechat_inbox.sender_id。
// 优先使用 reply_context 中的 sender_id 作为 to_user_id；为空时回退到 state.SenderID
// （兜底旧 outbox 行 / 通道从未收过消息的场景）。
//
// contextToken 仍从 secrets.Store 读取——单 bot 单聊的 wechat 通道上，全局 contextToken
// 等价于最近一次入站的 token；如需 per-message 隔离，再迁到 inbox/context_token 列即可。
func (w *wechatRemote) sendOutboxContent(ctx context.Context, content, replyContext string) error {
	state, err := w.readState(ctx)
	if !isConnectedState(state, err) {
		return errors.New("微信通道未连接")
	}
	if err != nil {
		return err
	}
	token, err := w.store.Get(wechatBotTokenKey)
	if err != nil {
		return err
	}
	contextToken, err := w.store.Get(wechatContextTokenKey)
	if errors.Is(err, secrets.ErrNotFound) || contextToken == "" {
		return errors.New("微信上下文令牌缺失")
	}
	if err != nil {
		return err
	}
	// 解析 reply_context（splitReplyContext 通用解析：prefix:value）；
	// 缺它时回退到全局 state.SenderID（兼容未填充 reply_context 的旧 outbox 行）。
	_, recipient := splitReplyContext(replyContext)
	if recipient == "" {
		recipient = state.SenderID
	}
	// 发送：复用 wechatAPI 既有方法（sendText）；失败重试由 RemoteCommandService.MarkOutboxFailed 负责。
	// 调试日志：按 chunk 记录请求参数、发送内容和接口返回结果，便于排查是否发出 / 内容是否正确。
	runes := []rune(content)
	total := (len(runes) + 1499) / 1500
	for i := 0; i < total; i++ {
		start := i * 1500
		end := min(start+1500, len(runes))
		chunk := string(runes[start:end])
		// 捕获 messageIDPresent：服务端返回 ret=0 但 message_id 异常（空/0/null）时，
		// 整条 outbox 被标 delivered 但实际没推到微信；改为返回错误触发重试。
		hasMsgID, err := w.api.sendText(ctx, state.BaseURL, token, recipient, contextToken, chunk)
		if err != nil {
			return err
		}
		if !hasMsgID {
			return errors.New("微信消息发送成功但未返回 message_id，结果状态不确定")
		}
	}
	// 回复已发出，取消"输入中"指示（最佳努力，失败忽略）。
	w.sendTypingIndicator(ctx, recipient, contextToken, false)
	return nil
}

// sendOnlineNotification 向最近一次发起对话的人发送"AI 员工已成功链接"通知。
// 上线通知目标 = 该通道 gt_remote_wechat_inbox 中 sender_id 非空的最新一行（按 received_at 倒序）；
// 反映"上一个跟我对话的人"。无历史行时跳过通知，避免在用户从未使用过的通道上打扰他人。
//
// 与 wecom/feishu/dingtalk 的 sendOnlineNotification 行为对齐：连接建立后由 pollLoop
// 触发，每个 connectionID 只发一次。
//
// 失败原因（iLink 上下文令牌过期、sendmessage 返回 ret/errcode）一律只记 warn 日志，
// 不影响通道状态——通知不是关键路径。
func (w *wechatRemote) sendOnlineNotification(ctx context.Context) {
	senderID, err := LatestInboxSender(ctx, w.db, ChannelTypeWechat)
	if err != nil || senderID == "" {
		// sql.ErrNoRows 或 sender_id 为空：通道从未收过消息，跳过通知
		return
	}

	state, stateErr := w.readState(ctx)
	if stateErr != nil || !isConnectedState(state, nil) {
		return
	}

	token, err := w.store.Get(wechatBotTokenKey)
	if err != nil || token == "" {
		return
	}
	contextToken, err := w.store.Get(wechatContextTokenKey)
	if errors.Is(err, secrets.ErrNotFound) || contextToken == "" {
		// 上下文令牌缺失（用户从未发起过有效对话）→ 跳过通知
		return
	}
	if err != nil {
		return
	}

	content := w.tr(ctx, "remote_channel_ai_online_notification", nil)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, sendErr := w.api.sendText(callCtx, state.BaseURL, token, senderID, contextToken, content); sendErr != nil {
		applog.Warn("微信上线通知发送失败", "error", sendErr, "sender_id", senderID)
		return
	}
	applog.Info("微信上线通知已发送", "sender_id", senderID)
}

// sendOfflineNotification 向最近一次发起对话的人发送"AI员工已下线"通知。
// 与 sendOnlineNotification 行为对齐：通知目标 = 该通道 inbox 中 sender_id 非空的最新一行；
// 通道从未收过消息时直接跳过；发送失败只记 warn，不影响 stop/disable 流程。
//
// state 必须传入事务提交前的已连接状态（在 clearConnection 中，connected_at 被清零前已读取）。
// 调用方需保证此时 store 中仍有 token、contextToken（client_id 已不再需要）。
func (w *wechatRemote) sendOfflineNotification(ctx context.Context, state wechatState) {
	senderID, err := LatestInboxSender(ctx, w.db, ChannelTypeWechat)
	if err != nil || senderID == "" {
		// 通道从未收过消息，跳过通知
		return
	}

	token, err := w.store.Get(wechatBotTokenKey)
	if err != nil || token == "" {
		return
	}
	contextToken, err := w.store.Get(wechatContextTokenKey)
	if errors.Is(err, secrets.ErrNotFound) || contextToken == "" {
		// 上下文令牌缺失 → 跳过通知
		return
	}
	if err != nil {
		return
	}

	content := w.tr(ctx, "remote_channel_ai_offline_notification", nil)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, sendErr := w.api.sendText(callCtx, state.BaseURL, token, senderID, contextToken, content); sendErr != nil {
		applog.Warn("微信下线通知发送失败", "error", sendErr, "sender_id", senderID, "content", content, "base_url", state.BaseURL, "token", token, "context_token", contextToken)
		return
	}
	applog.Info("微信下线通知已发送", "sender_id", senderID, "content", content, "base_url", state.BaseURL, "token", token, "context_token", contextToken)

}

func (w *wechatRemote) enqueue(ctx context.Context, sourceID, taskUUID, content string) error {
	// wechatEnqueuer 接口约束保持 5 个参数；通道类型由本方法固定为 wechat。
	if strings.TrimSpace(content) == "" {
		content = w.tr(ctx, "remote_wechat_final_empty", nil)
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := w.enqueueTx(ctx, tx, ChannelTypeWechat, sourceID, taskUUID, content); err != nil {
		return err
	}
	return tx.Commit()
}

// Enqueue 是 RemoteEnqueuer 接口的实现，把通道类型固定为 wechat 后走原 enqueue 路径。
// SkillSurface 等 core 层的回复通过本方法写入微信 outbox，再由 wechatRemote.deliverOutbox 主动发送。
func (w *wechatRemote) Enqueue(ctx context.Context, channelType, sourceID, taskUUID, content string) error {
	if channelType != "" && channelType != ChannelTypeWechat {
		return fmt.Errorf("wechatRemote 只能入队 channel_type=wechat 的消息，实际=%q", channelType)
	}
	return w.enqueue(ctx, sourceID, taskUUID, content)
}

func (w *wechatRemote) enqueueTx(ctx context.Context, tx *sql.Tx, channelType, sourceID, taskUUID, content string) error {
	if !utf8.ValidString(content) {
		return errors.New("微信回复包含无效 UTF-8")
	}
	if strings.TrimSpace(channelType) == "" {
		channelType = ChannelTypeWechat
	}
	// Keep each message small enough for the WeChat text endpoint, preserving rune boundaries.
	runes := []rune(content)
	exec := func(query string, args ...any) (sql.Result, error) {
		if tx != nil {
			return tx.ExecContext(ctx, query, args...)
		}
		return w.db.ExecContext(ctx, query, args...)
	}
	for start, index := 0, 0; start < len(runes); start, index = start+1500, index+1 {
		end := min(start+1500, len(runes))
		id := uuid.NewString()
		_, err := exec(`INSERT OR IGNORE INTO gt_remote_wechat_outbox(id, source_id, channel_type, task_uuid, content, next_attempt_at, created_at)
			VALUES (?, ?, ?, ?, ?, 0, ?)`, id, fmt.Sprintf("%s:%d", sourceID, index), channelType, taskUUID, string(runes[start:end]), time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *wechatRemote) deliverOutbox(ctx context.Context) error {
	state, err := w.readState(ctx)
	// S-BE001-fix: 仅在真正已连接时尝试发送。
	// 未连接（包括 saveCLIConfig upsert 留下的空 state 行）时直接 return，
	// 避免 workLoop 每秒调一次都报"密钥不存在"刷屏。
	if !isConnectedState(state, err) {
		return nil
	}
	token, err := w.store.Get(wechatBotTokenKey)
	if err != nil {
		return err
	}
	contextToken, err := w.store.Get(wechatContextTokenKey)
	if errors.Is(err, secrets.ErrNotFound) || contextToken == "" {
		return nil
	}
	if err != nil {
		return err
	}
	var id, content, replyContext string
	var attempts int
	err = w.db.QueryRowContext(ctx, `SELECT id, content, reply_context, attempts FROM gt_remote_wechat_outbox
		WHERE channel_type='wechat' AND status='pending' AND next_attempt_at<=? AND blocked_context_revision<>? ORDER BY created_at, rowid LIMIT 1`, time.Now().UnixMilli(), state.ContextRevision).Scan(&id, &content, &replyContext, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 与 sendOutboxContent 同样的策略：reply_context 中的 sender_id 优先，回退到 state.SenderID。
	_, recipient := splitReplyContext(replyContext)
	if recipient == "" {
		recipient = state.SenderID
	}

	// 锁外验证状态和待发送消息，减少锁的持有时间
	w.mu.Lock()
	current, stateErr := w.readState(ctx)
	if stateErr != nil || current.BotID != state.BotID || current.SenderID != state.SenderID || current.ConnectedAt != state.ConnectedAt {
		w.mu.Unlock()
		return stateErr
	}
	var pendingID string
	stateErr = w.db.QueryRowContext(ctx, `SELECT id FROM gt_remote_wechat_outbox WHERE id=? AND channel_type='wechat' AND status='pending'`, id).Scan(&pendingID)
	w.mu.Unlock()

	if errors.Is(stateErr, sql.ErrNoRows) {
		return nil
	}
	if stateErr != nil {
		return stateErr
	}

	// 锁外执行 HTTP 调用，调用前只短暂获取锁记录 sendCancel 以支持外部取消。
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	w.mu.Lock()
	w.sendCancel = cancel
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.sendCancel = nil
		w.mu.Unlock()
		cancel()
	}()

	messageIDPresent, err := w.api.sendText(callCtx, state.BaseURL, token, recipient, contextToken, content)
	if err == nil {
		_, err = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_outbox SET status='delivered', delivered_at=? WHERE id=? AND channel_type='wechat' AND status='pending'`, time.Now().UnixMilli(), id)
		if err == nil {
			w.setReplyMessageIDMissing(ctx, state, !messageIDPresent)
		}
		return err
	}
	backoff := time.Duration(1<<min(attempts, 8)) * time.Second
	blockedRevision := int64(-1)
	var httpErr *wechatHTTPError
	var sendErr *wechatSendError
	if (errors.As(err, &sendErr) && (sendErr.Ret == -14 || sendErr.ErrCode == -14)) ||
		(errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden)) {
		// 发送失败时清除凭据，下次需要重新扫码
		if _, clearErr := w.clearConnection(ctx, &state, false, "wechat_session_expired", true); clearErr != nil {
			return clearErr
		}
		if clearedState, stateErr := w.readState(ctx); stateErr == nil && clearedState.ConnectedAt == 0 {
			w.broadcastConnectionStatus("disconnected")
		}
		return err
	}
	if (errors.As(err, &sendErr) && sendErr.Ret == -2) ||
		(errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest) {
		blockedRevision = state.ContextRevision
	}
	_, updateErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_outbox SET attempts=attempts+1, next_attempt_at=?, blocked_context_revision=? WHERE id=? AND channel_type='wechat' AND status='pending'`,
		time.Now().Add(backoff).UnixMilli(), blockedRevision, id)
	if updateErr != nil {
		return updateErr
	}
	return err
}

// S-BE001: getCLIConfig 获取 CLI 配置
// @Summary 获取 CLI 配置
// @Description 获取微信通道的 CLI 默认配置
// @Tags remote-channels
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/local/remote-channels/wechat/cli-config [get]
func (w *wechatRemote) getCLIConfig(c *gin.Context) {
	// 读取独立 cli_config 表，不依赖 state 连接状态
	cfg := w.readChannelCLIConfig(c.Request.Context(), "wechat")
	// 未配置工作区时回填默认值，与 wecom/feishu/dingtalk 三通道一致，
	// 保证前端工作区输入框默认有值。
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		workspace = w.taskRoot + "/wechat_workspace"
	}
	c.JSON(http.StatusOK, gin.H{
		"cli_type":  cfg.CLICType,
		"cli_model": cfg.CLIModel,
		"workspace": workspace,
		"enabled":   cfg.Enabled,
	})
}

// channelCLIConfig 通道级别 CLI 配置，存储在独立表中，与连接状态完全解耦。
// Enabled 独立于 cli_type/cli_model 是否填写：仅在 Enabled=true 且 cli_type 非空时
// 才会在 dispatch 阶段作为兜底 CLI 启用。
type channelCLIConfig struct {
	CLICType  string
	CLIModel  string
	Workspace string
	Enabled   bool
}

// readChannelCLIConfig 读取指定通道类型的 CLI 配置。
// 记录不存在时返回空配置（Enabled 默认为 false），不会报错。
func (w *wechatRemote) readChannelCLIConfig(ctx context.Context, channelType string) channelCLIConfig {
	var cfg channelCLIConfig
	_ = w.db.QueryRowContext(ctx,
		`SELECT cli_type, cli_model, workspace, enabled FROM gt_remote_channel_cli_configs WHERE channel_type=?`,
		channelType).Scan(&cfg.CLICType, &cfg.CLIModel, &cfg.Workspace, &cfg.Enabled)
	return cfg
}

// saveChannelCLIConfig 保存指定通道类型的 CLI 配置（upsert）。
func (w *wechatRemote) saveChannelCLIConfig(ctx context.Context, channelType, cliType, cliModel, workspace string, enabled bool) error {
	now := time.Now().UnixMilli()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := w.db.ExecContext(ctx,
		`INSERT INTO gt_remote_channel_cli_configs (channel_type, cli_type, cli_model, workspace, enabled, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel_type) DO UPDATE SET
		 	cli_type = excluded.cli_type,
		 	cli_model = excluded.cli_model,
		 	workspace = excluded.workspace,
		 	enabled = excluded.enabled,
		 	updated_at = excluded.updated_at`,
		channelType, cliType, cliModel, workspace, enabledInt, now)
	return err
}

// S-BE001: saveCLIConfig 保存 CLI 配置
// @Summary 保存 CLI 配置
// @Description 保存微信通道的 CLI 默认配置和工作目录，作为通道指令消息的兜底处理选项；可在未连接时保存
// @Tags remote-channels
// @Accept json
// @Produce json
// @Param body body struct{CLICType string `json:"cli_type"`; CLIModel string `json:"cli_model"`; Workspace string `json:"workspace"`; Enabled bool `json:"enabled"`} true "CLI配置"
// @Success 200 {object} map[string]string
// @Router /api/local/remote-channels/wechat/cli-config [put]
func (w *wechatRemote) saveCLIConfig(c *gin.Context) {
	var body struct {
		CLICType  string `json:"cli_type"`
		CLIModel  string `json:"cli_model"`
		Workspace string `json:"workspace"`
		Enabled   bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "")
		return
	}
	body.CLICType = strings.TrimSpace(body.CLICType)
	body.CLIModel = strings.TrimSpace(body.CLIModel)
	body.Workspace = strings.TrimSpace(body.Workspace)
	if err := w.saveChannelCLIConfig(c.Request.Context(), "wechat", body.CLICType, body.CLIModel, body.Workspace, body.Enabled); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_wechat_state_error", "wechat_state_error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
