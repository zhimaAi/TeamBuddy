package localserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"goteams-client/internal/applog"
	"goteams-client/internal/i18n"
	"goteams-client/internal/workflow"
)

const (
	wecomWSEndpoint   = "wss://openws.work.weixin.qq.com"
	wecomPingInterval = 30 * time.Second
	wecomReadTimeout  = 75 * time.Second
)

// errWecomNotConfigured 表示企业微信通道尚未完成配置（缺状态行 / BotID / Secret）。
// 这属于「正常未配置」，不是连接故障：connectLoop 遇到它必须直接退出，
// 不打 WARN、不写 error 状态、不做指数退避重连，否则未配置的用户会看到
// 「企业微信连接失败，准备重连 error="读取通道状态失败: sql: no rows in result set"」的持续日志。
var errWecomNotConfigured = errors.New("企业微信通道未配置")

// WeCom frame types
const (
	wecomCmdSubscribe   = "aibot_subscribe"
	wecomCmdUnsubscribe = "aibot_unsubscribe"
	wecomCmdPing        = "ping"
	wecomCmdPong        = "pong"
	wecomCmdMessage     = "aibot_msg_callback"
	wecomCmdRespondMsg  = "aibot_respond_msg"
	wecomCmdSendMsg     = "aibot_send_msg"
)

// WeComMessage represents an incoming WeCom aibot message.
// 与企业微信 aibot 协议对齐：text/image/file/voice 等字段是嵌套对象，
// 内容从 `text.content` / `image.url` / `file.file_name` 等子字段读取，
// 而不是顶层 `content` 字段（之前定义错了，导致 Content 永远是空串）。
// 参见 chatclaw wecom_adapter.go 的 WeComMessageBody。
type WeComMessage struct {
	MsgID       string      `json:"msgid"`
	AIBotID     string      `json:"aibotid"`
	ChatID      string      `json:"chatid"`
	ChatType    string      `json:"chattype"` // single / group
	From        WeComFrom   `json:"from"`
	MsgType     string      `json:"msgtype"`
	CreateTime  int64       `json:"create_time"`
	ResponseURL string      `json:"response_url"`
	Text        *WeComText  `json:"text,omitempty"`
	Image       *WeComImage `json:"image,omitempty"`
	File        *WeComFile  `json:"file,omitempty"`
	Voice       *WeComVoice `json:"voice,omitempty"`
	Mixed       *WeComMixed `json:"mixed,omitempty"`
	Event       *WeComEvent `json:"event,omitempty"`
}

// WeComFrom 嵌套 from 对象
type WeComFrom struct {
	UserID string `json:"userid"`
}

// WeComText 文本消息内容
type WeComText struct {
	Content string `json:"content"`
}

// WeComImage 图片消息
type WeComImage struct {
	URL    string `json:"url"`
	AESKey string `json:"aeskey"`
}

// WeComFile 文件消息
type WeComFile struct {
	URL      string `json:"url"`
	AESKey   string `json:"aeskey"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

// WeComVoice 语音消息（含 ASR 文本）
type WeComVoice struct {
	URL    string `json:"url"`
	AESKey string `json:"aeskey"`
	Text   string `json:"text"`
}

// WeComMixed 混合消息
type WeComMixed struct {
	Items []WeComMixedItem `json:"items"`
}

// WeComMixedItem 混合消息子项
type WeComMixedItem struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content"`
}

// WeComEvent 事件
type WeComEvent struct {
	EventType string `json:"event_type"`
}

// extractTextContent 按 msgtype 抽取可路由的文本。
// text/voice → 纯文本；image/file/mixed → JSON 摘要（让自然语言兜底有内容处理）；
// 空/未知 → 空串。
func (m *WeComMessage) extractTextContent() string {
	switch m.MsgType {
	case "text":
		if m.Text != nil {
			return m.Text.Content
		}
	case "voice":
		if m.Voice != nil {
			if t := strings.TrimSpace(m.Voice.Text); t != "" {
				return t
			}
			// 语音无 ASR 文本时降级为 JSON 描述
			if data, err := json.Marshal(m.Voice); err == nil {
				return string(data)
			}
		}
	case "image":
		if m.Image != nil {
			if data, err := json.Marshal(m.Image); err == nil {
				return string(data)
			}
		}
	case "file":
		if m.File != nil {
			if data, err := json.Marshal(m.File); err == nil {
				return string(data)
			}
		}
	case "mixed":
		if m.Mixed != nil {
			if data, err := json.Marshal(m.Mixed); err == nil {
				return string(data)
			}
		}
	}
	return ""
}

// WeComFrame represents a WeCom WebSocket frame
type WeComFrame struct {
	Command string            `json:"cmd"`
	Seq     int64             `json:"seq"`
	Headers WeComFrameHeaders `json:"headers"`
	ErrCode int               `json:"errcode"` // 顶层 errcode（如 aibot_subscribe 响应无 cmd 时用此判断）
	ErrMsg  string            `json:"errmsg"`  // 顶层 errmsg
	Body    json.RawMessage   `json:"body"`
}

// WeComFrameHeaders contains request headers
type WeComFrameHeaders struct {
	ReqID string `json:"req_id"`
	BotID string `json:"bot_id"`
}

// wecomChannel implements RemoteChannel for WeCom
type wecomChannel struct {
	db             *sql.DB
	store          *channelStateStore
	taskDB         *sql.DB
	orchestrator   *workflow.Orchestrator
	tasks          *TasksHandler
	taskRoot       string
	commandService *RemoteCommandService

	mu        sync.Mutex
	conn      *websocket.Conn
	started   bool
	stopped   bool
	rootCtx   context.Context
	cancel    context.CancelFunc
	serverCtx context.Context // 长生命周期父 ctx（来自 server.StartRemoteChannels），connect() 重建时用作 rootCtx 的父节点

	// Pending operations tracking
	pendingResp map[string]chan WeComFrame // req_id -> response channel
}

// newWeComRemote creates a new WeCom channel instance
func newWeComRemote(
	db *sql.DB,
	store *channelStateStore,
	taskDB *sql.DB,
	orchestrator *workflow.Orchestrator,
	tasks *TasksHandler,
	taskRoot string,
) *wecomChannel {
	return &wecomChannel{
		db:           db,
		store:        store,
		taskDB:       taskDB,
		orchestrator: orchestrator,
		tasks:        tasks,
		taskRoot:     taskRoot,
		pendingResp:  make(map[string]chan WeComFrame),
	}
}

// ChannelType implements RemoteChannel
func (w *wecomChannel) ChannelType() string {
	return ChannelTypeWecom
}

// Status implements RemoteChannel
func (w *wecomChannel) Status(ctx context.Context) ChannelStatus {
	state, err := w.store.ReadState(ctx, ChannelTypeWecom)
	if err != nil {
		return ChannelStatusDisconnected
	}
	if string(state.Status) == "disabled" {
		return ChannelStatusDisabled
	}
	if state.ConnectedAt > 0 {
		return ChannelStatusConnected
	}
	return ChannelStatusDisconnected
}

// Start implements RemoteChannel
func (w *wecomChannel) Start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.serverCtx = ctx // 记录长生命周期 ctx，供 connect() 重启时使用
	w.rootCtx, w.cancel = context.WithCancel(ctx)
	w.started = true
	// 各循环按下述 ctx 启动并绑定，后续 connect() 重建时旧 ctx 被 cancel，旧循环据此退出。
	loopCtx := w.rootCtx
	w.mu.Unlock()

	go w.outboxDeliveryLoop(loopCtx)
	// 启动结果回收循环：把 inbox 中 status='dispatched' 的消息对应的 CLI session 结果
	// 写回 outbox，由 outboxDeliveryLoop 主动投到通道。详见 dingtalkChannel.collectResultsLoop。
	go w.collectResultsLoop(loopCtx)

	// 未配置（无状态行 / 已停用 / 无 BotID）时不再拉起 connectLoop。
	// 与 dingtalk / feishu 的 Start 对齐：旧实现无条件启动 connectLoop，
	// 未配置的机器上 ReadState 返回 sql.ErrNoRows，被 connectLoop 当成连接故障，
	// 反复打 WARN + 指数退避重连，日志里持续出现「读取通道状态失败: sql: no rows in result set」。
	// 用户在配置页点「连接」时由 connect() 重新拉起全部循环，因此这里提前返回不影响后续配置。
	state, err := w.store.ReadState(loopCtx, ChannelTypeWecom)
	if err != nil || string(state.Status) == "disabled" || state.BotID == "" {
		return
	}
	go w.connectLoop(loopCtx)
}

// Stop implements RemoteChannel
func (w *wecomChannel) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	if w.cancel != nil {
		w.cancel()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	w.mu.Unlock()
}

// RegisterRoutes implements RemoteChannel
func (w *wecomChannel) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/wecom", w.getStatus)
	r.POST("/wecom/verify", w.verifyCredentials)
	r.POST("/wecom/connect", w.connect)
	r.POST("/wecom/disable", w.disable)
	r.GET("/wecom/cli-config", w.getCLIConfig)
	r.PUT("/wecom/cli-config", w.saveCLIConfig)
}

// AttachCommandService 由 server.go 在装配完成后调用，注入 core（RemoteCommandService）。
// 不通过构造函数注入是为避免新增 channel 还要改构造签名的连锁改动；装配顺序见 server.go。
func (w *wecomChannel) AttachCommandService(svc *RemoteCommandService) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commandService = svc
}

// connectLoop handles connection with exponential backoff.
//
// ctx 由调用方（Start / connect 重建）传入并绑定本次循环的运行周期：connect() 重建连接时
// 会先 cancel 旧 ctx 再换新的，旧循环只认自己这份 ctx 便能立刻退出；若继续读字段上的
// w.rootCtx，旧循环会读到已经换新的 ctx 而多跑一轮 connectWS，出现两条长连接。
func (w *wecomChannel) connectLoop(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		// Check if disabled / unconfigured
		state, err := w.store.ReadState(ctx, ChannelTypeWecom)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && state.BotID == "") {
			applog.Info("企业微信通道未配置，停止重连", "channel", ChannelTypeWecom)
			return
		}
		if err == nil && string(state.Status) == "disabled" {
			applog.Info("企业微信通道已停用，停止重连", "channel", ChannelTypeWecom)
			return
		}

		if err := w.connectWS(ctx); err != nil {
			// 配置缺失不是连接故障：直接退出本循环（重新配置由 connect() 拉起重连），
			// 不写 error 状态、不打 WARN，避免未配置机器上刷屏。
			if errors.Is(err, errWecomNotConfigured) {
				applog.Info("企业微信通道未配置，停止重连", "channel", ChannelTypeWecom, "reason", err)
				return
			}
			applog.Warn("企业微信连接失败，准备重连", "error", err)
			_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusError))

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Connected successfully, wait for ws to disconnect.
		// readLoop 关闭 conn 后会触发 Stop 重启，这里只需阻塞到 ctx 取消。
		<-ctx.Done()
		return
	}
}

// connectWS establishes WebSocket connection
func (w *wecomChannel) connectWS(ctx context.Context) error {
	// 三类"未配置"都返回 errWecomNotConfigured，由 connectLoop 判定为正常状态并停止重连：
	// 无 Secret / 无状态行（sql.ErrNoRows）/ 有状态行但 BotID 为空。
	// Credentials
	secret, err := w.store.ReadCredential(ctx, ChannelTypeWecom)
	if err != nil || secret == "" {
		return fmt.Errorf("%w: 缺少 AppSecret", errWecomNotConfigured)
	}

	state, err := w.store.ReadState(ctx, ChannelTypeWecom)
	if err != nil {
		// 状态行缺失是"未配置"；其他 DB 错误仍按连接故障处理，保留原有 WARN + 退避重试语义，
		// 避免一次偶发查询失败就把通道永久停掉。
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: 缺少通道状态行", errWecomNotConfigured)
		}
		return fmt.Errorf("读取通道状态失败: %w", err)
	}

	botID := state.BotID
	if botID == "" {
		return fmt.Errorf("%w: 缺少 BotID", errWecomNotConfigured)
	}

	// Update status to connecting
	_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusConnecting))

	// Dial WebSocket
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, wecomWSEndpoint, nil)
	if err != nil {
		_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusError))
		return fmt.Errorf("WebSocket 连接失败: %w", err)
	}

	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	// 关键：必须先启动 readLoop 读消息，再等订阅响应。
	// 订阅响应到达 WS 缓冲区后必须由 readLoop 读出并通过 handleFrame
	// 路由到 respCh，否则 select 永远等不到 → "订阅超时"。
	// chatclaw wecom_adapter.Connect 采用同一顺序：dial → go readMessagesUntilAuth → select authResult。
	go w.readLoop(ctx, conn)

	// Subscribe to bot events
	reqID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	respCh := make(chan WeComFrame, 1)
	w.mu.Lock()
	w.pendingResp[reqID] = respCh
	w.mu.Unlock()

	// Send subscribe frame
	subBody := map[string]string{"bot_id": botID, "secret": secret}
	subBodyJSON, _ := json.Marshal(subBody)
	frame := WeComFrame{
		Command: wecomCmdSubscribe,
		Seq:     1,
		Headers: WeComFrameHeaders{ReqID: reqID, BotID: botID},
		Body:    subBodyJSON,
	}
	if err := w.sendFrame(frame); err != nil {
		w.mu.Lock()
		delete(w.pendingResp, reqID)
		w.mu.Unlock()
		_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusError))
		return fmt.Errorf("发送订阅请求失败: %w", err)
	}

	// Wait for subscribe response
	select {
	case resp := <-respCh:
		w.mu.Lock()
		delete(w.pendingResp, reqID)
		w.mu.Unlock()
		// 企微 aibot_subscribe 成功响应：errcode==0，
		// 响应帧可能带 cmd=pong，也可能仅返回顶层 errcode+errmsg（无 cmd 字段）。
		if resp.ErrCode == 0 || resp.Command == wecomCmdPong {
			// 订阅成功
		} else {
			_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusError))
			return fmt.Errorf("订阅失败: %s", resp.ErrMsg)
		}
	case <-time.After(10 * time.Second):
		w.mu.Lock()
		delete(w.pendingResp, reqID)
		w.mu.Unlock()
		_ = w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusError))
		return errors.New("订阅超时")
	case <-ctx.Done():
		return ctx.Err()
	}

	// Update connected status
	_ = w.store.UpdateConnectedAt(ctx, ChannelTypeWecom, time.Now().UnixMilli(), string(ChannelStatusConnected))
	applog.Info("企业微信通道已连接", "channel", ChannelTypeWecom, "bot_id", botID)

	// Send online notification to admin
	go w.sendOnlineNotification(ctx)

	// Start heartbeat loop (readLoop already running above)
	go w.heartbeatLoop(ctx)

	return nil
}

// sendFrame sends a WebSocket frame
func (w *wecomChannel) sendFrame(frame WeComFrame) error {
	w.mu.Lock()
	conn := w.conn
	w.mu.Unlock()
	if conn == nil {
		return errors.New("连接未建立")
	}
	return conn.WriteJSON(frame)
}

// sendAIBotTextMessage 通过 aibot_send_msg 发送一条文本消息并等待服务端响应。
// 同时被 sendOutbox（任务结果回传）和 sendOnlineNotification（连接上线通知）复用。
//
// body 格式严格按企业微信官方文档 https://developer.work.weixin.qq.com/document/path/101837：
//
//	{
//	  "chatid": "<会话 id，单聊=userid，群聊=回调中的 chatid>",
//	  "chat_type": 1,            // 1 单聊 / 2 群聊
//	  "msgtype": "markdown",     // 文本走 markdown：文档支持列表只有
//	  "markdown": {"content": "..."}   // markdown/template_card/file/image/voice/video，text 不在列
//	}
//
// 旧实现用 body["open_id"] / body["chat_id"] + msgtype=text，与文档 schema 不一致，
// 服务端校验失败 → 静默拒收，所有长任务结果都堆积在 outbox pending 反复重试。
func (w *wecomChannel) sendAIBotTextMessage(ctx context.Context, content, target, kind string) error {
	if content == "" {
		return errors.New("企业微信 aibot_send_msg 内容为空")
	}
	if target == "" {
		return errors.New("企业微信 aibot_send_msg 目标为空")
	}
	if kind != "single" && kind != "group" {
		return fmt.Errorf("企业微信 aibot_send_msg 不支持的目标 kind=%q", kind)
	}
	w.mu.Lock()
	conn := w.conn
	state, _ := w.store.ReadState(ctx, ChannelTypeWecom)
	botID := state.BotID
	w.mu.Unlock()
	if conn == nil || botID == "" {
		return errors.New("企业微信通道未连接")
	}
	chatType := 1
	if kind == "group" {
		chatType = 2
	}
	body := map[string]any{
		"chatid":    target,
		"chat_type": chatType,
		"msgtype":   "markdown",
		"markdown":  map[string]string{"content": content},
	}
	bodyJSON, _ := json.Marshal(body)
	reqID := fmt.Sprintf("send-%d", time.Now().UnixNano())
	respCh := make(chan WeComFrame, 1)
	w.mu.Lock()
	w.pendingResp[reqID] = respCh
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pendingResp, reqID)
		w.mu.Unlock()
	}()
	frame := WeComFrame{
		Command: wecomCmdSendMsg,
		Seq:     1,
		Headers: WeComFrameHeaders{ReqID: reqID, BotID: botID},
		Body:    bodyJSON,
	}
	if err := w.sendFrame(frame); err != nil {
		return fmt.Errorf("企业微信 aibot_send_msg 发送失败: %w", err)
	}
	select {
	case resp := <-respCh:
		if resp.ErrCode != 0 {
			return fmt.Errorf("企业微信 aibot_send_msg 服务端拒绝: errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg)
		}
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("企业微信 aibot_send_msg 响应超时")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendOnlineNotification 向最近一次发起对话的人发送上线通知。
// 上线通知目标 = 该通道 inbox 中 sender_id 非空的最新一行（按 received_at 倒序），
// 反映"上一个跟我对话的人"。无历史行时跳过通知，避免在用户从未使用过的通道上打扰他人。
func (w *wecomChannel) sendOnlineNotification(ctx context.Context) {
	senderID, err := LatestInboxSender(ctx, w.taskDB, ChannelTypeWecom)
	if err != nil || senderID == "" {
		// sql.ErrNoRows 或 sender_id 为空：通道从未收过消息，跳过通知
		return
	}

	state, stateErr := w.store.ReadState(ctx, ChannelTypeWecom)
	if stateErr != nil {
		return
	}

	content := i18n.WithLocale(state.Locale, "remote_channel_ai_online_notification")
	if err := w.sendAIBotTextMessage(ctx, content, senderID, "single"); err != nil {
		applog.Warn("企业微信上线通知发送失败", "error", err, "sender_id", senderID)
		return
	}
	applog.Info("企业微信上线通知已发送", "sender_id", senderID)
}

// sendOfflineNotification 与 sendOnlineNotification 对齐，给最近对话过的用户发"AI员工已下线"通知。
// 触发点：用户主动 disable 与程序退出前的兜底广播；通道未连接或 ws 已关闭时直接跳过。
// 发送失败只记 warn，不影响 disable / Close 流程。
func (w *wecomChannel) sendOfflineNotification(ctx context.Context) {
	senderID, err := LatestInboxSender(ctx, w.taskDB, ChannelTypeWecom)
	if err != nil || senderID == "" {
		return
	}

	state, stateErr := w.store.ReadState(ctx, ChannelTypeWecom)
	if stateErr != nil {
		return
	}

	content := i18n.WithLocale(state.Locale, "remote_channel_ai_offline_notification")
	if err := w.sendAIBotTextMessage(ctx, content, senderID, "single"); err != nil {
		applog.Warn("企业微信下线通知发送失败", "error", err, "sender_id", senderID)
		return
	}
	applog.Info("企业微信下线通知已发送", "sender_id", senderID)
}

// readLoop handles incoming messages.
// conn 由 connectWS 显式传入：本循环只负责自己这一条连接，退出时也只关闭它，
// 避免连接重建后旧 readLoop 的清理逻辑误关新连接（旧实现读字段 w.conn）。
func (w *wecomChannel) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		w.mu.Lock()
		if w.conn == conn {
			w.conn = nil
		}
		w.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		if ctx.Err() != nil {
			return
		}

		conn.SetReadDeadline(time.Now().Add(wecomReadTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				applog.Warn("企业微信读取消息失败", "error", err)
			}
			return
		}

		var frame WeComFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			applog.Warn("企业微信解析消息失败", "error", err)
			continue
		}

		w.handleFrame(frame)
	}
}

// heartbeatLoop sends periodic ping frames
func (w *wecomChannel) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(wecomPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frame := WeComFrame{Command: wecomCmdPing}
			if err := w.sendFrame(frame); err != nil {
				applog.Warn("企业微信心跳失败", "error", err)
				return
			}
		}
	}
}

// handleFrame handles incoming WebSocket frames
func (w *wecomChannel) handleFrame(frame WeComFrame) {
	// Handle response for pending requests
	if frame.Headers.ReqID != "" {
		w.mu.Lock()
		if ch, ok := w.pendingResp[frame.Headers.ReqID]; ok {
			ch <- frame
		}
		w.mu.Unlock()
	}

	// Handle message callbacks
	if frame.Command == wecomCmdMessage {
		var msg WeComMessage
		if err := json.Unmarshal(frame.Body, &msg); err != nil {
			applog.Warn("企业微信解析消息体失败", "error", err)
			return
		}

		// Process message
		w.onIncoming(msg)
	}
}

// onIncoming processes an incoming message
func (w *wecomChannel) onIncoming(msg WeComMessage) {
	ctx := w.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}

	// 按 msgtype 抽取真正的文本/摘要；aibot 协议的文本不在顶层 content 字段。
	content := msg.extractTextContent()

	// Determine reply target based on chat type
	replyTarget := msg.ChatID
	if msg.ChatType == "single" {
		replyTarget = msg.From.UserID
	}

	// Create inbound message
	inbound := RemoteInboundMessage{
		ChannelType: ChannelTypeWecom,
		MessageID:   msg.MsgID,
		SenderID:    msg.From.UserID,
		ChatID:      msg.ChatID,
		ReplyTarget: replyTarget,
		Content:     content,
	}

	// Enqueue to inbox
	inboxKey, err := EnqueueInbox(ctx, w.taskDB, ChannelTypeWecom, msg.MsgID, msg.From.UserID, content, "")
	if err != nil {
		applog.Warn("企业微信消息入队失败", "msg_id", msg.MsgID, "error", err)
		return
	}
	inbound.InboxKey = inboxKey

	// Process message if we have command service
	var reply string
	if w.commandService != nil {
		var (
			taskUUID, sessionUUID string
			handleErr             error
		)
		reply, taskUUID, sessionUUID, handleErr = w.commandService.Handle(ctx, inbound)
		// 收尾：更新 inbox 状态 + 写入 outbox（含兜底回复）。
		// 兜底翻译在 FinishInbound 内部完成；err != nil 不再提前 return，
		// 否则会出现「inbox 已 enqueue 但 outbox 为空、用户收不到反馈」且 ws ack 也丢失的复合故障。
		// reply_context 用 "single:userid" / "group:chatid" 前缀格式（与 feishu/dingtalk 对齐），
		// outbox 投递按 kind 选择 aibot_send_msg 的 body 字段。
		// inboxKey 来自 EnqueueInbox 返回值（uuid+":"+msgID），用原 msgID 会因主键前缀不一致
		// 找不到行，表现为「inbox 永远停在 pending、FinishInbound 全部无效」。
		if finishErr := w.commandService.FinishInbound(ctx, ChannelTypeWecom, inboxKey, taskUUID, sessionUUID, reply, wecomReplyContext(msg.ChatType, msg.From.UserID, msg.ChatID), handleErr); finishErr != nil {
			applog.Warn("企业微信消息收尾失败", "msg_id", msg.MsgID, "error", finishErr)
		}
	}

	// Acknowledge receipt immediately
	ackBody := map[string]string{"req_id": msg.MsgID, "errcode": "0"}
	ackBodyJSON, _ := json.Marshal(ackBody)
	ackFrame := WeComFrame{
		Command: wecomCmdRespondMsg,
		Headers: WeComFrameHeaders{BotID: msg.From.UserID},
		Body:    ackBodyJSON,
	}
	_ = w.sendFrame(ackFrame)
}

// wecomReplyContext 把消息目标编码为 "single:userid" / "group:chatid" 前缀格式，
// 与 feishu/dingtalk 对齐；outbox 投递按 kind 选择 aibot_send_msg 的 body 字段。
// 旧版用 "open_id:xxx" / "chat_id:xxx"，但官方 body 不认这两个字段，正确的字段是
// chatid（必填）+ chat_type（1 单聊 / 2 群聊），详见 sendAIBotTextMessage。
func wecomReplyContext(chatType, fromUser, chatID string) string {
	if chatType == "single" {
		return "single:" + fromUser
	}
	return "group:" + chatID
}

// outboxDeliveryLoop 周期读取 outbox pending 并通过 aibot_send_msg 投递。
// ctx 绑定本次循环：连接重建时旧循环随旧 ctx 退出，避免新旧两个循环同时投递 outbox。
func (w *wecomChannel) outboxDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.commandService == nil {
				continue
			}
			if err := w.commandService.DeliverOutbox(ctx, ChannelTypeWecom,
				OutboxDeliverFuncAdapter{
					Revision: func(_ context.Context) int64 {
						state, err := w.store.ReadState(ctx, ChannelTypeWecom)
						if err != nil {
							return -1
						}
						return state.ContextRevision
					},
					Send: w.sendOutbox,
				}); err != nil {
				applog.Warn("企业微信 outbox 投递失败", "error", err)
			}
		}
	}
}

// collectResultsLoop 周期性调用 commands.CollectResults 回收 inbox 中 status='dispatched'
// 的消息对应的 CLI session 结果。详见 dingtalkChannel.collectResultsLoop 同名函数说明。
func (w *wecomChannel) collectResultsLoop(ctx context.Context) {
	ticker := time.NewTicker(workLoopFallbackInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.commandService == nil {
				continue
			}
			if err := w.commandService.CollectResults(ctx, ChannelTypeWecom); err != nil {
				applog.Warn("收集企业微信任务结果失败", "error", err)
			}
		}
	}
}

// sendOutbox 通过 aibot_send_msg 把 outbox 一条消息投递到企业微信。
// reply_context 由 wecomReplyContext 编码为 "single:userid" / "group:chatid"，
// 实际发送走 sendAIBotTextMessage：body 严格按官方文档使用 chatid + chat_type +
// msgtype=markdown，并通过 req_id + pendingResp 等待响应帧，根据 errcode 决定
// 返回 nil / error，让 DeliverOutbox 正确把失败消息置回 pending 走重试退避。
func (w *wecomChannel) sendOutbox(ctx context.Context, content, replyContext string) error {
	if replyContext == "" {
		return errors.New("企业微信 outbox 缺少 reply_context")
	}
	kind, target := splitReplyContext(replyContext)
	if target == "" || (kind != "single" && kind != "group") {
		return fmt.Errorf("企业微信 outbox reply_context 格式错误: %q", replyContext)
	}
	return w.sendAIBotTextMessage(ctx, content, target, kind)
}

// getStatus returns the current channel status
func (w *wecomChannel) getStatus(c *gin.Context) {
	state, err := w.store.ReadState(c.Request.Context(), ChannelTypeWecom)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_state_error", "channel_state_error")
		return
	}

	status := ChannelStatusDisconnected
	if err == nil {
		if state.Status == "disabled" {
			status = ChannelStatusDisabled
		} else if state.Status == "connected" || (state.Status == "" && state.ConnectedAt > 0) {
			status = ChannelStatusConnected
		} else if state.Status == "connecting" {
			status = ChannelStatusConnecting
		} else if state.Status == "error" {
			status = ChannelStatusError
		}
	}

	cliCfg := w.readChannelCLIConfig(c.Request.Context(), ChannelTypeWecom)
	c.JSON(http.StatusOK, gin.H{
		"status":       string(status),
		"connected_at": state.ConnectedAt,
		"bot_id":       state.BotID,
		"cli_type":     cliCfg.CLICType,
		"cli_model":    cliCfg.CLIModel,
	})
}

// verifyCredentials verifies WeCom credentials without storing
// 入参统一 TrimSpace：B 端从管理后台复制 BotID/Secret 时容易带入首尾空格，
// 带空格的 bot_id 会让 aibot_subscribe 直接失败（表现为「配置看着没错但连不上」）。
func (w *wecomChannel) verifyCredentials(c *gin.Context) {
	var req struct {
		AppID     string `json:"app_id" binding:"required"`
		AppSecret string `json:"app_secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	botID := strings.TrimSpace(req.AppID)
	secret := strings.TrimSpace(req.AppSecret)
	if botID == "" || secret == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}

	// Try to establish a test connection
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(c.Request.Context(), wecomWSEndpoint, nil)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	defer conn.Close()

	// Try to subscribe with the provided credentials
	reqID := fmt.Sprintf("verify-%d", time.Now().UnixNano())
	testBody := map[string]string{"bot_id": botID, "secret": secret}
	testBodyJSON, _ := json.Marshal(testBody)
	frame := WeComFrame{
		Command: wecomCmdSubscribe,
		Seq:     1,
		Headers: WeComFrameHeaders{ReqID: reqID, BotID: botID},
		Body:    testBodyJSON,
	}
	if err := conn.WriteJSON(frame); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}

	// Read response with timeout
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}

	var respFrame struct {
		Cmd     string `json:"cmd"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(data, &respFrame); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	// 验证成功：errcode==0，或兼容带 cmd=pong 的响应
	if respFrame.ErrCode == 0 || respFrame.Cmd == wecomCmdPong {
		c.JSON(http.StatusOK, gin.H{"valid": true})
		return
	}
	// 旧实现在这里 i18n.Error 之后又补了一次 c.JSON(200, {"valid": true})，
	// 导致「凭据无效却返回 200 且 valid=true」，前端永远提示验证成功——
	// 这里必须直接返回，不能再写第二次响应。
	applog.Warn("企业微信凭据校验失败", "errcode", respFrame.ErrCode, "errmsg", respFrame.ErrMsg)
	i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
}

// connect 接收凭据并启动/重建连接。
// AppSecret 为空时视为"保留原 Secret"，仅更新 AppID / 状态。
// 上线通知不再依赖用户填写的 admin_id，改为自动从 inbox 最近一条 sender_id 推断。
//
// 入参一律 TrimSpace：bot_id 带首尾空格会让 aibot_subscribe 校验失败。
// 等待首轮建连后若状态已是 error，返回 4xx 让前端展示失败，不再返回 200 造成假成功。
func (w *wecomChannel) connect(c *gin.Context) {
	var req struct {
		AppID     string `json:"app_id" binding:"required"`
		AppSecret string `json:"app_secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}

	ctx := c.Request.Context()
	botID := strings.TrimSpace(req.AppID)
	if botID == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}

	// 仅当 AppSecret 非空时覆盖凭据存储，留空表示保留原有 Secret
	if secret := strings.TrimSpace(req.AppSecret); secret != "" {
		if err := w.store.WriteCredential(ctx, ChannelTypeWecom, secret); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "remote_channel_credential_save_failed", "credential_save_failed")
			return
		}
	}

	// Update bot_id；上线通知对象由 inbox 最近一条 sender_id 决定。
	state, _ := w.store.ReadState(ctx, ChannelTypeWecom)
	state.BotID = botID
	state.ChannelType = ChannelTypeWecom
	state.Status = string(ChannelStatusConnecting)
	if err := w.store.WriteState(ctx, state); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_state_save_failed", "state_save_failed")
		return
	}

	// Trigger connection
	// 关键：rootCtx 必须派生自长生命周期父 ctx（Start 注入的 serverCtx 或回退 background），
	// 而非 c.Request.Context()。HTTP 请求结束时 ctx 会被取消，会连带把 connectLoop、
	// outboxDeliveryLoop、ws 长连接一起杀掉，导致"context canceled"后通道完全断开。
	w.mu.Lock()
	parent := w.serverCtx
	if parent == nil {
		parent = context.Background()
	}
	if !w.started {
		w.serverCtx = parent
		w.rootCtx, w.cancel = context.WithCancel(parent)
		w.started = true
		// stopped 必须复位：Stop() 是一次性开关（stopped=true 后永远早返回），重建连接后
		// 不复位会让后续「停止本机连接」与退出时的 Stop() 变成空操作——ws 不会被关闭、
		// readLoop 继续收消息并按正常流程回复，用户看到「已停止连接」但实际还在工作。
		w.stopped = false
		loopCtx := w.rootCtx
		w.mu.Unlock()
		go w.connectLoop(loopCtx)
		go w.outboxDeliveryLoop(loopCtx)
		go w.collectResultsLoop(loopCtx)
	} else {
		// Restart connection with new credentials
		w.mu.Unlock()
		w.Stop()
		w.mu.Lock()
		w.rootCtx, w.cancel = context.WithCancel(parent)
		w.started = true
		w.stopped = false
		loopCtx := w.rootCtx
		w.mu.Unlock()
		go w.connectLoop(loopCtx)
		go w.outboxDeliveryLoop(loopCtx)
		go w.collectResultsLoop(loopCtx)
	}

	// Wait briefly for connection attempt
	time.Sleep(2 * time.Second)

	// Return current status
	state, _ = w.store.ReadState(ctx, ChannelTypeWecom)
	// 首轮建连已明确失败（status 在连接前被置为 connecting，error 只可能来自本轮）：
	// 返回 4xx，避免前端提示「连接成功」而实际连不上。
	if state.Status == string(ChannelStatusError) {
		applog.Warn("企业微信连接失败，返回失败状态", "bot_id", botID)
		i18n.Error(c, http.StatusBadRequest, "remote_channel_connect_failed", "connect_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       state.Status,
		"connected_at": state.ConnectedAt,
	})
}

// disable stops the channel without clearing credentials
func (w *wecomChannel) disable(c *gin.Context) {
	ctx := c.Request.Context()

	// 主动停用前先给最近对话过的用户发"AI员工已下线"通知，
	// 顺序：业务文本消息先发（依赖 ws 仍在线），再 UpdateStatus + Stop 关 ws。
	// ws 已断开或通知失败时仅记 warn，不影响停用流程。
	w.sendOfflineNotification(ctx)

	// Update status to disabled
	if err := w.store.UpdateStatus(ctx, ChannelTypeWecom, string(ChannelStatusDisabled)); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_disable_failed", "disable_failed")
		return
	}

	// Stop the transport
	w.Stop()

	c.JSON(http.StatusOK, gin.H{
		"status": string(ChannelStatusDisabled),
	})
}

// CLI config methods for WeCom channel

type wecomCLIConfigResult struct {
	CLICType  string
	CLIModel  string
	Workspace string
	Enabled   bool
}

func (w *wecomChannel) readChannelCLIConfig(ctx context.Context, channelType string) wecomCLIConfigResult {
	var cfg wecomCLIConfigResult
	_ = w.taskDB.QueryRowContext(ctx, `
		SELECT cli_type, cli_model, workspace, enabled FROM gt_remote_channel_cli_configs WHERE channel_type=?`, channelType).Scan(
		&cfg.CLICType, &cfg.CLIModel, &cfg.Workspace, &cfg.Enabled)
	return cfg
}

func (w *wecomChannel) getCLIConfig(c *gin.Context) {
	cfg := w.readChannelCLIConfig(c.Request.Context(), ChannelTypeWecom)
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		workspace = fmt.Sprintf("%s/wecom_workspace", w.taskRoot)
	}
	c.JSON(http.StatusOK, gin.H{
		"cli_type":  cfg.CLICType,
		"cli_model": cfg.CLIModel,
		"workspace": workspace,
		"enabled":   cfg.Enabled,
	})
}

func (w *wecomChannel) saveCLIConfig(c *gin.Context) {
	var req struct {
		CLICType  string `json:"cli_type"`
		CLIModel  string `json:"cli_model"`
		Workspace string `json:"workspace"`
		Enabled   bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_invalid_request", "invalid_request")
		return
	}
	req.CLICType = strings.TrimSpace(req.CLICType)
	req.CLIModel = strings.TrimSpace(req.CLIModel)
	req.Workspace = strings.TrimSpace(req.Workspace)

	ctx := c.Request.Context()
	now := time.Now().UnixMilli()
	enabledInt := 0
	if req.Enabled {
		enabledInt = 1
	}
	_, err := w.taskDB.ExecContext(ctx, `
		INSERT INTO gt_remote_channel_cli_configs (channel_type, cli_type, cli_model, workspace, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_type) DO UPDATE SET
			cli_type=excluded.cli_type,
			cli_model=excluded.cli_model,
			workspace=excluded.workspace,
			enabled=excluded.enabled,
			updated_at=excluded.updated_at`,
		ChannelTypeWecom, req.CLICType, req.CLIModel, req.Workspace, enabledInt, now)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_cli_config_save_failed", "cli_config_save_failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
