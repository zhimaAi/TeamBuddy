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

	"goteams-client/internal/applog"
	"goteams-client/internal/i18n"
	"goteams-client/internal/workflow"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// 飞书通道 transport 实现。
//
// 计划 §十六 B8 / §四 飞书章节：
//   - larkws 长连接（无需公网回调）订阅 im.message.receive_v1；
//   - 入站：EnqueueInbox → commands.Handle 同步处理 → FinishInbound 收尾（含兜底回复 + outbox）；
//   - 出站：outbox worker 周期性读取 pending，调用 larkim.Message.Create 主动发送；
//   - 长任务结果与短回复都走"主动发送"路径（§十一 风险点：飞书事件回调要求秒级 200）。
type feishuChannel struct {
	db           *sql.DB
	store        *channelStateStore
	orchestrator *workflow.Orchestrator
	tasks        *TasksHandler
	taskRoot     string

	mu             sync.Mutex
	started        bool
	stopped        bool
	rootCtx        context.Context
	cancel         context.CancelFunc
	serverCtx      context.Context // 长生命周期父 ctx（来自 server.StartRemoteChannels），connect() 重建时用作 rootCtx 的父节点
	apiClient      *lark.Client    // 懒加载：首次发送 / verifyCredentials 时构造
	commandService *RemoteCommandService
}

func newFeishuRemote(
	db *sql.DB,
	store *channelStateStore,
	orchestrator *workflow.Orchestrator,
	tasks *TasksHandler,
	taskRoot string,
) *feishuChannel {
	return &feishuChannel{
		db:           db,
		store:        store,
		orchestrator: orchestrator,
		tasks:        tasks,
		taskRoot:     taskRoot,
	}
}

// ChannelType implements RemoteChannel.
func (f *feishuChannel) ChannelType() string { return ChannelTypeFeishu }

// Status implements RemoteChannel.
func (f *feishuChannel) Status(ctx context.Context) ChannelStatus {
	state, err := f.store.ReadState(ctx, ChannelTypeFeishu)
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

// AttachCommandService 由 server.go 在装配完成后注入 core（RemoteCommandService）。
func (f *feishuChannel) AttachCommandService(svc *RemoteCommandService) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commandService = svc
}

// Start implements RemoteChannel。
// 仅在用户通过 /feishu/connect 触发后才真正建立 larkws 长连接；Start 仅做状态校验。
func (f *feishuChannel) Start(ctx context.Context) {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return
	}
	f.serverCtx = ctx // 记录长生命周期 ctx，供 connect() 重启时使用
	f.started = true
	f.rootCtx, f.cancel = context.WithCancel(ctx)
	// 各循环按下述 ctx 启动并绑定，后续 connect() 重建时旧 ctx 被 cancel，旧循环据此退出。
	loopCtx := f.rootCtx
	f.mu.Unlock()

	// 启动 outbox 投递循环（核心层 outbox 与 transport 解耦）
	go f.outboxDeliveryLoop(loopCtx)
	// 启动结果回收循环：把 inbox 中 status='dispatched' 的消息对应的 CLI session 结果
	// 写回 outbox，由 outboxDeliveryLoop 主动投到通道。详见 dingtalkChannel.collectResultsLoop。
	go f.collectResultsLoop(loopCtx)

	state, err := f.store.ReadState(loopCtx, ChannelTypeFeishu)
	if err != nil {
		return
	}
	if string(state.Status) == "disabled" || state.BotID == "" {
		return
	}
	// 已配置凭据则尝试建连；失败由 connectLoop 重试。
	go f.connectLoop(loopCtx)
}

// Stop implements RemoteChannel。
func (f *feishuChannel) Stop() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	f.stopped = true
	if f.cancel != nil {
		f.cancel()
	}
	f.mu.Unlock()
}

// RegisterRoutes implements RemoteChannel。
func (f *feishuChannel) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/feishu", f.getStatus)
	r.POST("/feishu/verify", f.verifyCredentials)
	r.POST("/feishu/connect", f.connect)
	// disconnect 与 disable 的差异已收敛：disable 仅改 status、保留凭据与连接状态，
	// 取消单独的 disconnect 入口避免误清空凭据。
	r.POST("/feishu/disable", f.disable)
	r.GET("/feishu/cli-config", f.getCLIConfig)
	r.PUT("/feishu/cli-config", f.saveCLIConfig)
}

// connectLoop 处理带指数退避的建连重试。
//
// ctx 由调用方（Start / connect 重建）传入并绑定本次循环的运行周期：重建时会先 cancel 旧 ctx
// 再换新的，旧循环只认自己这份 ctx 便能立即退出；若继续读字段上的 f.rootCtx，旧循环会读到
// 已经换新的 ctx 而多跑一轮 connectWS，出现两条 larkws 长连接。
// disable 状态在每次迭代顶部检测，避免用户在停用后仍持续重连。
// 仅在 Stop()/ctx 取消时退出。
func (f *feishuChannel) connectLoop(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		state, err := f.store.ReadState(ctx, ChannelTypeFeishu)
		if err == nil && string(state.Status) == "disabled" {
			applog.Info("飞书通道已停用，停止重连", "channel", ChannelTypeFeishu)
			return
		}
		if err := f.connectWS(ctx); err != nil {
			applog.Warn("飞书连接失败，准备重连", "error", err)
			_ = f.store.UpdateStatus(ctx, ChannelTypeFeishu, string(ChannelStatusError))
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
		// 建连成功后 ws.Start 会一直阻塞；断开后由本循环进入下一轮重连。
		backoff = time.Second
		// 等待 ws 断开或 ctx 取消
		<-ctx.Done()
		return
	}
}

// connectWS 建立 larkws 长连接。larkws.Client.Start 在内部做重连，单次调用阻塞。
func (f *feishuChannel) connectWS(ctx context.Context) error {
	appID, secret, err := f.loadCredentials(ctx)
	if err != nil {
		return err
	}
	_ = f.store.UpdateStatus(ctx, ChannelTypeFeishu, string(ChannelStatusConnecting))

	// 关键：connected_at / status=connected 只能在 larkws 真正握手成功（SDK 的 OnReady）后写入。
	// 旧实现在 cli.Start 之前就乐观写入 connected，导致凭据错误、网关不可达时
	// 前端 2 秒后读到的仍是 connected，表现为「连接失败却提示连接成功」。
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(f.onMessageReceive)
	cli := larkws.NewClient(appID, secret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithOnReady(func() {
			if err := f.store.UpdateConnectedAt(ctx, ChannelTypeFeishu, time.Now().UnixMilli(), string(ChannelStatusConnected)); err != nil {
				applog.Warn("飞书通道写入连接状态失败", "channel", ChannelTypeFeishu, "error", err)
				return
			}
			applog.Info("飞书通道已连接", "channel", ChannelTypeFeishu, "app_id", appID)
			go f.sendOnlineNotification(ctx)
		}),
		larkws.WithOnError(func(cbErr error) {
			applog.Warn("飞书长连接异常", "channel", ChannelTypeFeishu, "error", cbErr)
		}),
	)
	f.mu.Lock()
	f.apiClient = lark.NewClient(appID, secret)
	f.mu.Unlock()
	cli.Start(ctx) // 阻塞直到 ctx 取消或 SDK 内部不可恢复错误
	// Start 返回意味着长连接已终结（ctx 取消或不可恢复错误）：清掉 connected_at，
	// 避免「进程内已断开但状态仍是 connected」。
	if rootErr := ctx.Err(); rootErr == nil {
		_ = f.store.UpdateConnectedAt(ctx, ChannelTypeFeishu, 0, string(ChannelStatusError))
	}
	return errors.New("larkws 连接退出")
}

// sendOnlineNotification 向最近一次发起对话的人发送上线通知。
// 上线通知目标 = 该通道 inbox 中 sender_id 非空的最新一行（按 received_at 倒序），
// 反映"上一个跟我对话的人"。无历史行时跳过通知，避免在用户从未使用过的通道上打扰他人。
func (f *feishuChannel) sendOnlineNotification(ctx context.Context) {
	senderID, err := LatestInboxSender(ctx, f.db, ChannelTypeFeishu)
	if err != nil || senderID == "" {
		return
	}

	f.mu.Lock()
	apiClient := f.apiClient
	f.mu.Unlock()
	if apiClient == nil {
		// apiClient 由 connectWS 创建；若还未建连则跳过本次通知
		return
	}

	state, stateErr := f.store.ReadState(ctx, ChannelTypeFeishu)
	if stateErr != nil {
		return
	}

	content := i18n.WithLocale(state.Locale, "remote_channel_ai_online_notification")
	contentJSON, _ := json.Marshal(map[string]string{"text": content})
	msgType := "text"
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("open_id").
		Body(&larkim.CreateMessageReqBody{
			ReceiveId: stringPtr(senderID),
			MsgType:   &msgType,
			Content:   stringPtr(string(contentJSON)),
		}).
		Build()
	resp, err := apiClient.Im.V1.Message.Create(ctx, req)
	if err != nil {
		applog.Warn("飞书上架通知发送失败", "error", err)
	} else if !resp.Success() {
		applog.Warn("飞书上架通知发送失败", "code", resp.Code, "msg", resp.Msg)
	} else {
		applog.Info("飞书上架通知已发送", "sender_id", senderID)
	}
}

// sendOfflineNotification 与 sendOnlineNotification 对齐，给最近对话过的用户发"AI员工已下线"。
// 触发点：用户主动 disable 与程序退出前的兜底广播；apiClient 未初始化或通道未连接时跳过。
// 发送失败只记 warn，不影响 disable / Close 流程。
func (f *feishuChannel) sendOfflineNotification(ctx context.Context) {
	senderID, err := LatestInboxSender(ctx, f.db, ChannelTypeFeishu)
	if err != nil || senderID == "" {
		return
	}

	f.mu.Lock()
	apiClient := f.apiClient
	f.mu.Unlock()
	if apiClient == nil {
		return
	}

	state, stateErr := f.store.ReadState(ctx, ChannelTypeFeishu)
	if stateErr != nil {
		return
	}

	content := i18n.WithLocale(state.Locale, "remote_channel_ai_offline_notification")
	contentJSON, _ := json.Marshal(map[string]string{"text": content})
	msgType := "text"
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("open_id").
		Body(&larkim.CreateMessageReqBody{
			ReceiveId: stringPtr(senderID),
			MsgType:   &msgType,
			Content:   stringPtr(string(contentJSON)),
		}).
		Build()
	resp, err := apiClient.Im.V1.Message.Create(ctx, req)
	if err != nil {
		applog.Warn("飞书下线通知发送失败", "error", err)
		return
	}
	if !resp.Success() {
		applog.Warn("飞书下线通知发送失败", "code", resp.Code, "msg", resp.Msg)
		return
	}
	applog.Info("飞书下线通知已发送", "sender_id", senderID)
}

func (f *feishuChannel) loadCredentials(ctx context.Context) (appID, secret string, err error) {
	state, err := f.store.ReadState(ctx, ChannelTypeFeishu)
	if err != nil {
		return "", "", fmt.Errorf("读取飞书通道状态失败: %w", err)
	}
	if state.BotID == "" {
		return "", "", errors.New("未配置飞书 AppID")
	}
	secret, err = f.store.ReadCredential(ctx, ChannelTypeFeishu)
	if err != nil || secret == "" {
		return "", "", errors.New("未配置飞书 AppSecret")
	}
	return state.BotID, secret, nil
}

// onMessageReceive 是 im.message.receive_v1 事件回调。
// 飞书事件要求秒级 200（§十一）；同步处理但必须轻量。
func (f *feishuChannel) onMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	messageID := larkcore.StringValue(event.Event.Message.MessageId)
	if messageID == "" {
		return nil
	}
	msgType := larkcore.StringValue(event.Event.Message.MessageType)
	if msgType != "text" {
		applog.Info("飞书暂仅处理 text 消息", "msg_type", msgType, "msg_id", messageID)
		return nil
	}
	// content 是 JSON 字符串：{"text":"..."}
	rawContent := larkcore.StringValue(event.Event.Message.Content)
	plain := extractFeishuText(rawContent)
	if plain == "" {
		return nil
	}
	var senderID, chatID, chatType string
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = larkcore.StringValue(event.Event.Sender.SenderId.OpenId)
	}
	chatID = larkcore.StringValue(event.Event.Message.ChatId)
	chatType = larkcore.StringValue(event.Event.Message.ChatType)

	inboxKey, err := EnqueueInbox(f.rootCtx, f.db, ChannelTypeFeishu, messageID, senderID, plain, "")
	if err != nil {
		applog.Warn("飞书消息入队失败", "msg_id", messageID, "error", err)
		return nil
	}
	if f.commandService == nil {
		return nil
	}
	inbound := RemoteInboundMessage{
		ChannelType: ChannelTypeFeishu,
		MessageID:   messageID,
		InboxKey:    inboxKey,
		SenderID:    senderID,
		ChatID:      chatID,
		ReplyTarget: feishuReplyTarget(chatType, senderID, chatID),
		Content:     plain,
	}
	reply, taskUUID, sessionUUID, err := f.commandService.Handle(f.rootCtx, inbound)
	// 收尾：更新 inbox 状态 + 写入 outbox（含兜底回复）。
	// reply_context 始终传给 outbox（feishuReplyContext 在单聊时用 open_id，群聊时用 chat_id），
	// 即 reply 为空（被兜底回复填充）也要写入，确保 outbox delivery 能定位目标。
	// inboxKey 来自 EnqueueInbox 返回值（uuid+":"+messageID），用原 messageID 会因主键前缀
	// 不一致找不到行，表现为「inbox 永远停在 pending、FinishInbound 全部无效」。
	if finishErr := f.commandService.FinishInbound(f.rootCtx, ChannelTypeFeishu, inboxKey, taskUUID, sessionUUID, reply, feishuReplyContext(chatType, senderID, chatID), err); finishErr != nil {
		applog.Warn("飞书消息收尾失败", "msg_id", messageID, "error", finishErr)
	}
	return nil
}

// extractFeishuText 解析 content 的 JSON，提取 text 字段。
func extractFeishuText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var wrapper struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return raw
	}
	return wrapper.Text
}

func feishuReplyTarget(chatType, senderID, chatID string) string {
	if chatType == "p2p" || chatType == "single" || chatType == "direct" {
		return senderID
	}
	return chatID
}

func feishuReplyContext(chatType, senderID, chatID string) string {
	if chatType == "p2p" || chatType == "single" || chatType == "direct" {
		return "open_id:" + senderID
	}
	return "chat_id:" + chatID
}

// outboxDeliveryLoop 周期读取 outbox pending 并投递。
// ctx 绑定本次循环：连接重建时旧循环随旧 ctx 退出，避免新旧两个循环同时投递 outbox。
func (f *feishuChannel) outboxDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if f.commandService == nil {
				continue
			}
			if err := f.commandService.DeliverOutbox(ctx, ChannelTypeFeishu,
				OutboxDeliverFuncAdapter{
					Revision: func(_ context.Context) int64 {
						state, err := f.store.ReadState(ctx, ChannelTypeFeishu)
						if err != nil {
							return -1
						}
						return state.ContextRevision
					},
					Send: f.sendOutbox,
				}); err != nil {
				applog.Warn("飞书 outbox 投递失败", "error", err)
			}
		}
	}
}

// collectResultsLoop 周期性调用 commands.CollectResults 回收 inbox 中 status='dispatched'
// 的消息对应的 CLI session 结果。详见 dingtalkChannel.collectResultsLoop 同名函数说明。
func (f *feishuChannel) collectResultsLoop(ctx context.Context) {
	ticker := time.NewTicker(workLoopFallbackInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if f.commandService == nil {
				continue
			}
			if err := f.commandService.CollectResults(ctx, ChannelTypeFeishu); err != nil {
				applog.Warn("收集飞书任务结果失败", "error", err)
			}
		}
	}
}

// sendOutbox 把 outbox 一条消息投递到飞书（按 reply_context 选 open_id/chat_id）。
func (f *feishuChannel) sendOutbox(ctx context.Context, content, replyContext string) error {
	state, err := f.store.ReadState(ctx, ChannelTypeFeishu)
	if err != nil || state.ConnectedAt == 0 {
		return errors.New("飞书通道未连接")
	}
	if f.apiClient == nil {
		appID, secret, err := f.loadCredentials(ctx)
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.apiClient = lark.NewClient(appID, secret)
		f.mu.Unlock()
	}
	kind, id := splitReplyContext(replyContext)
	if id == "" {
		return errors.New("飞书 outbox 缺少 reply_context")
	}
	receiveIDType := "open_id"
	if kind == "chat_id" {
		receiveIDType = "chat_id"
	}
	contentJSON, err := json.Marshal(map[string]string{"text": content})
	if err != nil {
		return err
	}
	msgType := "text"
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(&larkim.CreateMessageReqBody{
			ReceiveId: stringPtr(id),
			MsgType:   &msgType,
			Content:   stringPtr(string(contentJSON)),
		}).
		Build()
	resp, err := f.apiClient.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("飞书发送失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func stringPtr(s string) *string { return &s }

// splitReplyContext 解析 "open_id:xxx" / "chat_id:xxx"。
func splitReplyContext(ctx string) (kind, id string) {
	idx := strings.Index(ctx, ":")
	if idx < 0 {
		return "", ctx
	}
	return ctx[:idx], ctx[idx+1:]
}

// getStatus returns the current channel status。
func (f *feishuChannel) getStatus(c *gin.Context) {
	state, err := f.store.ReadState(c.Request.Context(), ChannelTypeFeishu)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_state_error", "channel_state_error")
		return
	}
	status := ChannelStatusDisconnected
	if err == nil {
		switch state.Status {
		case "disabled":
			status = ChannelStatusDisabled
		case "connected":
			status = ChannelStatusConnected
		case "connecting":
			status = ChannelStatusConnecting
		case "error":
			status = ChannelStatusError
		}
	}
	cliCfg := f.readChannelCLIConfig(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{
		"status":       string(status),
		"connected_at": state.ConnectedAt,
		"bot_id":       state.BotID,
		"cli_type":     cliCfg.CLICType,
		"cli_model":    cliCfg.CLIModel,
	})
}

// verifyFeishuCredential 校验飞书自建应用的 app_id/app_secret 是否有效。
// 走 tenant_access_token/internal：只验证凭据本身，不依赖任何业务权限点，
// 因此可以安全地用在「连接前预检」路径上（凭据错误时在建连前短路返回）。
// 所有入参在这里统一 TrimSpace，防止用户复制粘贴带入首尾空格。
func verifyFeishuCredential(ctx context.Context, appID, appSecret string) error {
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	if appID == "" {
		return errors.New("飞书 AppID 为空")
	}
	if appSecret == "" {
		return errors.New("飞书 AppSecret 为空")
	}
	cli := lark.NewClient(appID, appSecret, lark.WithReqTimeout(5*time.Second))
	resp, err := cli.GetTenantAccessTokenBySelfBuiltApp(ctx, &larkcore.SelfBuiltTenantAccessTokenReq{
		AppID:     appID,
		AppSecret: appSecret,
	})
	if err != nil {
		return fmt.Errorf("飞书凭据校验请求失败: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("飞书凭据无效: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// verifyCredentials 校验 app_id/app_secret，不落库、不建长连接。
func (f *feishuChannel) verifyCredentials(c *gin.Context) {
	var req struct {
		AppID     string `json:"app_id" binding:"required"`
		AppSecret string `json:"app_secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
	defer cancel()
	if err := verifyFeishuCredential(ctx, req.AppID, req.AppSecret); err != nil {
		applog.Warn("飞书凭据校验失败", "error", err)
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true})
}

// connect 接收凭据并启动/重建连接。
// AppSecret 为空时视为"保留原 Secret"，仅更新 AppID / 状态。
// 上线通知不再依赖用户填写的 admin_id，改为自动从 inbox 最近一条 sender_id 推断。
//
// 入参一律 TrimSpace：用户在平台复制 AppID/AppSecret 时带上的首尾空格会让飞书
// bootstrap / 事件订阅鉴权失败（表现为「凭据看着没错但连不上」）。
// 建连前先做一次租户 token 预检，凭据错误时直接返回 4xx，
// 避免直接返回 200 让前端误报「连接成功」。
func (f *feishuChannel) connect(c *gin.Context) {
	var req struct {
		AppID     string `json:"app_id" binding:"required"`
		AppSecret string `json:"app_secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	ctx := c.Request.Context()
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	// 仅当 AppSecret 非空时覆盖凭据存储，留空表示保留原有 Secret
	if secret := strings.TrimSpace(req.AppSecret); secret != "" {
		if err := f.store.WriteCredential(ctx, ChannelTypeFeishu, secret); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "remote_channel_credential_save_failed", "credential_save_failed")
			return
		}
	}
	// 预检用的 Secret：本次提交优先，留空则回退到已保存的凭据。
	effectiveSecret := strings.TrimSpace(req.AppSecret)
	if effectiveSecret == "" {
		stored, readErr := f.store.ReadCredential(ctx, ChannelTypeFeishu)
		if readErr != nil || stored == "" {
			i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
			return
		}
		effectiveSecret = stored
	}
	precheckCtx, cancelPrecheck := context.WithTimeout(ctx, 6*time.Second)
	precheckErr := verifyFeishuCredential(precheckCtx, appID, effectiveSecret)
	cancelPrecheck()
	if precheckErr != nil {
		applog.Warn("飞书凭据预检失败，拒绝建立连接", "app_id", appID, "error", precheckErr)
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	state, _ := f.store.ReadState(ctx, ChannelTypeFeishu)
	state.ChannelType = ChannelTypeFeishu
	state.BotID = appID
	state.Status = string(ChannelStatusConnecting)
	if err := f.store.WriteState(ctx, state); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_state_save_failed", "state_save_failed")
		return
	}
	// 启动 / 重建连接
	// 关键：rootCtx 必须派生自长生命周期父 ctx（Start 注入的 serverCtx 或回退 background），
	// 而非 c.Request.Context()。HTTP 请求结束时 ctx 被取消，会连带把 connectLoop、
	// outboxDeliveryLoop、larkws 长连接一起杀掉。
	// started 在进程启动时就被 Start() 置为 true，即使当时没有任何配置（BotID 为空时
	// Start() 只起 outbox/collect 两个循环）。因此不能只按 !started 判断是否需要启动协程：
	// 启动时无配置 → 之后配置并点「连接」会落到 started=true 分支、不起 connectLoop，
	// 表现为状态一直停在 connecting、必须重启程序才能连上；「停用后再点连接」同理
	// （那时 disable 已 cancel 旧 rootCtx，旧 connectLoop 已退出）。
	// 故与 wecom/dingtalk 对齐：started=true 路径统一 Stop + 重建 rootCtx + 重起三个协程。
	f.mu.Lock()
	parent := f.serverCtx
	if parent == nil {
		parent = context.Background()
	}
	if !f.started {
		f.serverCtx = parent
		f.rootCtx, f.cancel = context.WithCancel(parent)
		f.started = true
		f.stopped = false
		loopCtx := f.rootCtx
		f.mu.Unlock()
		go f.connectLoop(loopCtx)
		go f.outboxDeliveryLoop(loopCtx)
		go f.collectResultsLoop(loopCtx)
	} else {
		f.mu.Unlock()
		f.Stop()
		f.mu.Lock()
		f.rootCtx, f.cancel = context.WithCancel(parent)
		f.started = true
		f.stopped = false
		loopCtx := f.rootCtx
		f.mu.Unlock()
		go f.connectLoop(loopCtx)
		go f.outboxDeliveryLoop(loopCtx)
		go f.collectResultsLoop(loopCtx)
	}
	// 短暂等待首次连接尝试结果
	time.Sleep(2 * time.Second)
	state, _ = f.store.ReadState(ctx, ChannelTypeFeishu)
	// 首轮建连已明确失败：用 400 让前端展示失败，不再返回 200 让用户以为连上了。
	// （status 在 connect 里先被置为 connecting，写入 error 只可能来自本轮建连失败）
	if state.Status == string(ChannelStatusError) {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_connect_failed", "connect_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       state.Status,
		"connected_at": state.ConnectedAt,
	})
}

// disable 停用通道但保留凭据。
func (f *feishuChannel) disable(c *gin.Context) {
	ctx := c.Request.Context()
	// 主动停用前先给最近对话过的用户发"AI员工已下线"通知，
	// 顺序：业务消息先发（依赖 apiClient/连接），再 UpdateStatus + Stop。
	// apiClient 未初始化或通知失败时仅记 warn，不影响停用流程。
	f.sendOfflineNotification(ctx)
	if err := f.store.UpdateStatus(ctx, ChannelTypeFeishu, string(ChannelStatusDisabled)); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_disable_failed", "disable_failed")
		return
	}
	f.Stop()
	c.JSON(http.StatusOK, gin.H{"status": string(ChannelStatusDisabled)})
}

type feishuCLIConfigResult struct {
	CLICType  string
	CLIModel  string
	Workspace string
	Enabled   bool
}

func (f *feishuChannel) readChannelCLIConfig(ctx context.Context) feishuCLIConfigResult {
	var cfg feishuCLIConfigResult
	_ = f.db.QueryRowContext(ctx, `
		SELECT cli_type, cli_model, workspace, enabled FROM gt_remote_channel_cli_configs WHERE channel_type=?`,
		ChannelTypeFeishu).Scan(&cfg.CLICType, &cfg.CLIModel, &cfg.Workspace, &cfg.Enabled)
	if !cfg.Enabled {
		cfg.Enabled = true
	}
	return cfg
}

func (f *feishuChannel) getCLIConfig(c *gin.Context) {
	cfg := f.readChannelCLIConfig(c.Request.Context())
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		workspace = f.taskRoot + "/feishu_workspace"
	}
	// 显式使用 snake_case 字段名与前端 CLIConfig 契约对齐
	c.JSON(http.StatusOK, gin.H{
		"cli_type":  cfg.CLICType,
		"cli_model": cfg.CLIModel,
		"workspace": workspace,
		"enabled":   cfg.Enabled,
	})
}

func (f *feishuChannel) saveCLIConfig(c *gin.Context) {
	var req struct {
		CLICType  string `json:"cli_type"`
		CLIModel  string `json:"cli_model"`
		Workspace string `json:"workspace"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_cli_config_invalid", "channel_cli_config_invalid")
		return
	}
	req.CLICType = strings.TrimSpace(req.CLICType)
	req.CLIModel = strings.TrimSpace(req.CLIModel)
	req.Workspace = strings.TrimSpace(req.Workspace)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	now := time.Now().UnixMilli()
	_, err := f.db.ExecContext(c.Request.Context(), `
		INSERT INTO gt_remote_channel_cli_configs (channel_type, cli_type, cli_model, workspace, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_type) DO UPDATE SET
			cli_type=excluded.cli_type,
			cli_model=excluded.cli_model,
			workspace=excluded.workspace,
			enabled=excluded.enabled,
			updated_at=excluded.updated_at`,
		ChannelTypeFeishu, req.CLICType, req.CLIModel, req.Workspace, enabledInt, now)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_cli_config_save_failed", "channel_cli_config_save_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
