package localserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/i18n"
	"goteams-client/internal/workflow"

	dingtalkchatbot "github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingtalkclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

// 钉钉通道 transport 实现。
//
// 计划 §十六 B8 / §四 钉钉章节：
//   - Stream 模式长连接订阅 chatbot 回调（/v1.0/im/bot/messages/get）；
//   - 入站：chatbot handler → 去重 → EnqueueInbox → commands.Handle 同步处理；
//   - 出站：即时 ack 走 sessionWebhook + ChatbotReplier.SimpleReplyText；
//     长任务结果走 access_token + robot 主动发送（access_token 缓存 + 提前刷新）；
//   - §十一 风险点：sessionWebhook 有时效，长任务必须走主动发送。
type dingtalkChannel struct {
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
	streamClient   *dingtalkclient.StreamClient
	commandService *RemoteCommandService

	// access_token 缓存（用于主动发送）
	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt time.Time
	httpClient     *http.Client
}

func newDingtalkRemote(
	db *sql.DB,
	store *channelStateStore,
	orchestrator *workflow.Orchestrator,
	tasks *TasksHandler,
	taskRoot string,
) *dingtalkChannel {
	return &dingtalkChannel{
		db:           db,
		store:        store,
		orchestrator: orchestrator,
		tasks:        tasks,
		taskRoot:     taskRoot,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ChannelType implements RemoteChannel.
func (d *dingtalkChannel) ChannelType() string { return ChannelTypeDingtalk }

// Status implements RemoteChannel.
func (d *dingtalkChannel) Status(ctx context.Context) ChannelStatus {
	state, err := d.store.ReadState(ctx, ChannelTypeDingtalk)
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

// AttachCommandService 由 server.go 在装配完成后注入 core。
func (d *dingtalkChannel) AttachCommandService(svc *RemoteCommandService) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commandService = svc
}

// Start implements RemoteChannel.
func (d *dingtalkChannel) Start(ctx context.Context) {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.serverCtx = ctx // 记录长生命周期 ctx，供 connect() 重启时使用
	d.started = true
	d.rootCtx, d.cancel = context.WithCancel(ctx)
	// 各循环按下述 ctx 启动并绑定，后续 connect() 重建时旧 ctx 被 cancel，旧循环据此退出。
	loopCtx := d.rootCtx
	d.mu.Unlock()

	// 启动 outbox 投递循环
	go d.outboxDeliveryLoop(loopCtx)
	// 启动结果回收循环：把 inbox 中 status='dispatched' 的消息对应的 CLI session 结果
	// 写回 outbox，由 outboxDeliveryLoop 主动投到通道。钉钉 / 飞书 / 企微 三个 transport
	// 之前没有 workLoop，CLI 兜底任务完成后的 final_result 永远没人回收，
	// 表现为「任务跑完了但通道没回」。该循环与 outboxDeliveryLoop 互不依赖。
	go d.collectResultsLoop(loopCtx)

	state, err := d.store.ReadState(loopCtx, ChannelTypeDingtalk)
	if err != nil {
		return
	}
	if string(state.Status) == "disabled" || state.BotID == "" {
		return
	}
	go d.connectLoop(loopCtx)
}

// Stop implements RemoteChannel.
func (d *dingtalkChannel) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	if d.streamClient != nil {
		d.streamClient.Close()
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
}

// RegisterRoutes implements RemoteChannel.
func (d *dingtalkChannel) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/dingtalk", d.getStatus)
	r.POST("/dingtalk/verify", d.verifyCredentials)
	r.POST("/dingtalk/connect", d.connect)
	// disconnect 与 disable 的差异已收敛：disable 仅改 status、保留凭据与连接状态，
	// 取消单独的 disconnect 入口避免误清空凭据。
	r.POST("/dingtalk/disable", d.disable)
	r.GET("/dingtalk/cli-config", d.getCLIConfig)
	r.PUT("/dingtalk/cli-config", d.saveCLIConfig)
}

// connectLoop 处理带指数退避的建连重试。
//
// ctx 由调用方（Start / connect 重建）传入并绑定本次循环的运行周期：重建时会先 cancel 旧 ctx
// 再换新的，旧循环只认自己这份 ctx 便能立即退出；若继续读字段上的 d.rootCtx，旧循环会读到
// 已经换新的 ctx 而多跑一轮 connectStream，出现两条 Stream 长连接。
func (d *dingtalkChannel) connectLoop(ctx context.Context) {
	backoff := time.Second
	for i := 0; i < 10; i++ {
		if ctx.Err() != nil {
			return
		}
		state, err := d.store.ReadState(ctx, ChannelTypeDingtalk)
		if err == nil && string(state.Status) == "disabled" {
			applog.Info("钉钉通道已停用，停止重连", "channel", ChannelTypeDingtalk)
			return
		}
		if err := d.connectStream(ctx); err != nil {
			applog.Warn("钉钉连接失败，尝试重连", "attempt", i+1, "error", err)
			_ = d.store.UpdateStatus(ctx, ChannelTypeDingtalk, string(ChannelStatusError))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = time.Second
		// 阻塞到 ctx done（Stream SDK 内部已做重连）
		<-ctx.Done()
		return
	}
}

// connectStream 建立 Stream 长连接。
func (d *dingtalkChannel) connectStream(ctx context.Context) error {
	clientID, clientSecret, err := d.loadCredentials(ctx)
	if err != nil {
		return err
	}
	_ = d.store.UpdateStatus(ctx, ChannelTypeDingtalk, string(ChannelStatusConnecting))

	cred := dingtalkclient.NewAppCredentialConfig(clientID, clientSecret)
	cli := dingtalkclient.NewStreamClient(
		dingtalkclient.WithAppCredential(cred),
		dingtalkclient.WithAutoReconnect(true),
	)
	cli.RegisterChatBotCallbackRouter(d.onChatbotMessage)
	// 关键：connected 状态必须在 Stream 真正建连成功之后才写入。
	// StreamClient.Start 内部会先用 client_id/client_secret 取连接网关端点（凭据校验）
	// 再完成 ws 握手，失败会同步返回错误；旧实现把 UpdateConnectedAt 放在 Start 之前，
	// 凭据错误（例如复制时带入空格）时前端 2 秒后读到的仍是 connected，
	// 表现为「连接失败却提示连接成功」。Start 成功后由 SDK 内部自动重连。
	if err := cli.Start(ctx); err != nil {
		_ = d.store.UpdateStatus(ctx, ChannelTypeDingtalk, string(ChannelStatusError))
		return fmt.Errorf("钉钉 Stream 建连失败: %w", err)
	}
	d.mu.Lock()
	d.streamClient = cli
	d.mu.Unlock()
	if err := d.store.UpdateConnectedAt(ctx, ChannelTypeDingtalk, time.Now().UnixMilli(), string(ChannelStatusConnected)); err != nil {
		return err
	}
	applog.Info("钉钉通道已连接", "channel", ChannelTypeDingtalk, "client_id", clientID)
	go d.sendOnlineNotification(ctx)
	return nil
}

func (d *dingtalkChannel) loadCredentials(ctx context.Context) (clientID, clientSecret string, err error) {
	state, err := d.store.ReadState(ctx, ChannelTypeDingtalk)
	if err != nil {
		return "", "", fmt.Errorf("读取钉钉通道状态失败: %w", err)
	}
	if state.BotID == "" {
		return "", "", errors.New("未配置钉钉 ClientID")
	}
	clientSecret, err = d.store.ReadCredential(ctx, ChannelTypeDingtalk)
	if err != nil || clientSecret == "" {
		return "", "", errors.New("未配置钉钉 ClientSecret")
	}
	return state.BotID, clientSecret, nil
}

// onChatbotMessage 是 chatbot 回调入口。返回 []byte 作为 DataFrameResponse.Data。
func (d *dingtalkChannel) onChatbotMessage(ctx context.Context, data *dingtalkchatbot.BotCallbackDataModel) ([]byte, error) {
	if data == nil {
		return nil, nil
	}
	msgID := data.MsgId
	if msgID == "" {
		return nil, nil
	}
	if data.Msgtype != "text" {
		applog.Info("钉钉暂仅处理 text 消息", "msg_type", data.Msgtype, "msg_id", msgID)
		return nil, nil
	}
	content := strings.TrimSpace(data.Text.Content)
	if content == "" {
		return nil, nil
	}
	inboxKey, err := EnqueueInbox(d.rootCtx, d.db, ChannelTypeDingtalk, msgID, data.SenderStaffId, content, data.SessionWebhook)
	if err != nil {
		applog.Warn("钉钉消息入队失败", "msg_id", msgID, "error", err)
		return nil, nil
	}
	if d.commandService == nil {
		return nil, nil
	}
	inbound := RemoteInboundMessage{
		ChannelType: ChannelTypeDingtalk,
		MessageID:   msgID,
		InboxKey:    inboxKey,
		SenderID:    data.SenderStaffId,
		ChatID:      data.ConversationId,
		ReplyTarget: data.SessionWebhook,
		Content:     content,
	}
	reply, taskUUID, sessionUUID, err := d.commandService.Handle(d.rootCtx, inbound)
	// 即时 ack：先于落库，避免 outbox 投递延迟时用户长时间看不到反馈。
	// 即时 ack 失败不影响最终入箱；FinishInbound 会确保兜底回复一定写入 outbox。
	if reply != "" && data.SessionWebhook != "" {
		replier := dingtalkchatbot.NewChatbotReplier()
		if ackErr := replier.SimpleReplyText(d.rootCtx, data.SessionWebhook, []byte(reply)); ackErr != nil {
			applog.Warn("钉钉即时回复失败", "msg_id", msgID, "error", ackErr)
		}
	}
	// 收尾：更新 inbox 状态 + 写入 outbox（含兜底回复）。
	// 必须在 ack 之后调用，避免 err 还没翻译就提前落入 outbox。
	// inboxKey 来自 EnqueueInbox 返回值（uuid+":"+msgID），用原 msgID 会因主键前缀不一致找不到行，
	// 表现为「inbox 永远停在 pending、ProcessInbox / FinishInbound 全部无效」。
	if finishErr := d.commandService.FinishInbound(d.rootCtx, ChannelTypeDingtalk, inboxKey, taskUUID, sessionUUID, reply, dingtalkReplyContext(data), err); finishErr != nil {
		applog.Warn("钉钉消息收尾失败", "msg_id", msgID, "error", finishErr)
	}
	return nil, nil
}

// dingtalkReplyContext 决定主动发送的目标。
// 群聊/单聊都用 sessionWebhook 优先（ChatbotReplier 同样支持）；
// 但长任务需要 OAPI 主动发（仅群聊支持），单聊/群聊统一走 "sessionWebhook:URL"。
func dingtalkReplyContext(data *dingtalkchatbot.BotCallbackDataModel) string {
	if data == nil {
		return ""
	}
	if data.SessionWebhook != "" {
		return "session_webhook:" + data.SessionWebhook
	}
	if data.SenderStaffId != "" {
		return "user_id:" + data.SenderStaffId
	}
	return ""
}

// outboxDeliveryLoop 周期读取 outbox pending 并投递。
// ctx 绑定本次循环：连接重建时旧循环随旧 ctx 退出，避免新旧两个循环同时投递 outbox。
func (d *dingtalkChannel) outboxDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.commandService == nil {
				continue
			}
			if err := d.commandService.DeliverOutbox(ctx, ChannelTypeDingtalk,
				OutboxDeliverFuncAdapter{
					Revision: func(_ context.Context) int64 {
						state, err := d.store.ReadState(ctx, ChannelTypeDingtalk)
						if err != nil {
							return -1
						}
						return state.ContextRevision
					},
					Send: d.sendOutbox,
				}); err != nil {
				applog.Warn("钉钉 outbox 投递失败", "error", err)
			}
		}
	}
}

// collectResultsLoop 周期性调用 commands.CollectResults 回收 inbox 中 status='dispatched'
// 的消息对应的 CLI session 结果，把 final_result 写入 outbox 后由 outboxDeliveryLoop 投递。
//
// 钉钉 / 飞书 / 企微 三个 transport 之前没有这个循环，CLI 兜底任务完成后的 final_result
// 永远没人来收，表现为「任务跑完了但通道没回」。该循环与 outboxDeliveryLoop 互不依赖，
// 但都绑定同一个 ctx，随 transport Stop / 连接重建一起退出。
//
// commandService 可能晚于 Start 注入（server.go 在装配完成后调 AttachCommandService），
// 这里每轮检查 nil 跳过；装配完成前的 pending 消息由后续循环自然处理。
func (d *dingtalkChannel) collectResultsLoop(ctx context.Context) {
	ticker := time.NewTicker(workLoopFallbackInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.commandService == nil {
				continue
			}
			if err := d.commandService.CollectResults(ctx, ChannelTypeDingtalk); err != nil {
				applog.Warn("收集钉钉任务结果失败", "error", err)
			}
		}
	}
}

// sendOutbox 通过 access_token + robot OAPI 主动发送（用于长任务结果回传）。
func (d *dingtalkChannel) sendOutbox(ctx context.Context, content, replyContext string) error {
	if replyContext == "" {
		return errors.New("钉钉 outbox 缺少 reply_context")
	}
	kind, target := splitReplyContext(replyContext)
	if target == "" {
		return errors.New("钉钉 outbox reply_context 格式错误")
	}
	if kind != "session_webhook" && kind != "user_id" {
		return fmt.Errorf("钉钉 outbox reply_context kind=%q 不支持", kind)
	}
	// access_token 缓存
	tok, err := d.getAccessToken(ctx)
	if err != nil {
		return err
	}
	url := "https://oapi.dingtalk.com/robot/send?access_token=" + tok
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": content},
	}
	if kind == "session_webhook" {
		// 群聊：走机器人 Webhook 主动发，需 sessionWebhook 字段
		payload["sessionWebhook"] = target
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("钉钉 OAPI 主动发失败: status=%d body=%s", resp.StatusCode, string(raw))
	}
	// 钉钉 OAPI 错误码：errcode != 0 表示失败
	var r struct {
		ErrCode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &r); err == nil && r.ErrCode != 0 {
		return fmt.Errorf("钉钉 OAPI 主动发失败: errcode=%d errmsg=%s", r.ErrCode, r.Errmsg)
	}
	return nil
}

// sendOnlineNotification 向最近一次发起对话的人发送上线通知。
// 上线通知目标 = 该通道 inbox 中 sender_id 非空的最新一行（按 received_at 倒序），
// 反映"上一个跟我对话的人"。无历史行时跳过通知，避免在用户从未使用过的通道上打扰他人。
//
// 钉钉单聊主动推送限制：旧版 /robot/oapi 端点只认 sessionWebhook（群机器人 Webhook），
// 单聊需要走新版 oToMessages/batchSend（需要 robotCode）。ChatbotReplier 路径只能复用
// chatbot 回调时带下来的 sessionWebhook（5-30 分钟时效）。
//
// 发送顺序（越靠前越快、越优先）：
//  1. ChatbotReplier：inbox 最近一条消息保留了 chatbot 回调时的 sessionWebhook，
//     重连后几秒到几分钟内多数情况下仍有效；走与即时回复同一通道，零额外配置。
//  2. oToMessages/batchSend：上一条 sessionWebhook 失效或不存在时，
//     改用 access_token + robotCode + staff_id 主动发送；需要用户在「连接」时填写 robotCode。
//  3. 都没有：只 warn，不影响通道状态。
func (d *dingtalkChannel) sendOnlineNotification(ctx context.Context) {
	senderID, sessionWebhook, err := LatestInboxSenderWithWebhook(ctx, d.db, ChannelTypeDingtalk)
	if err != nil || senderID == "" {
		return
	}
	state, stateErr := d.store.ReadState(ctx, ChannelTypeDingtalk)
	if stateErr != nil {
		return
	}
	content := i18n.WithLocale(state.Locale, "remote_channel_ai_online_notification")

	// 1) 优先：sessionWebhook 仍在时效内 → 复用 ChatbotReplier（与即时回复同通道，最快）。
	if sessionWebhook != "" {
		replier := dingtalkchatbot.NewChatbotReplier()
		if err := replier.SimpleReplyText(ctx, sessionWebhook, []byte(content)); err == nil {
			applog.Info("钉钉上线通知已发送（sessionWebhook）", "sender_id", senderID)
			return
		} else {
			applog.Warn("钉钉上线通知 sessionWebhook 已失效，回退 robotCode", "sender_id", senderID, "error", err)
		}
	}

	// 2) 兜底：access_token + robotCode + oToMessages/batchSend（永久有效，需要配置 robotCode）。
	if state.RobotCode == "" {
		applog.Warn("钉钉上线通知未发送：sessionWebhook 不可用且未配置 robotCode",
			"sender_id", senderID, "hint", "在连接配置中填写 robotCode 后重连")
		return
	}
	if err := d.sendDingtalkBatchSend(ctx, state.RobotCode, senderID, content); err != nil {
		applog.Warn("钉钉上线通知发送失败（robotCode）", "sender_id", senderID, "robot_code", state.RobotCode, "error", err)
		return
	}
	applog.Info("钉钉上线通知已发送（robotCode）", "sender_id", senderID)
}

// sendOfflineNotification 与 sendOnlineNotification 对齐，给最近对话过的用户发"AI员工已下线"。
// 触发点：用户主动 disable 与程序退出前的兜底广播；通道从未收过消息或 state 读取失败时跳过。
// 发送失败只记 warn，不影响 disable / Close 流程。
func (d *dingtalkChannel) sendOfflineNotification(ctx context.Context) {
	senderID, sessionWebhook, err := LatestInboxSenderWithWebhook(ctx, d.db, ChannelTypeDingtalk)
	if err != nil || senderID == "" {
		return
	}
	state, stateErr := d.store.ReadState(ctx, ChannelTypeDingtalk)
	if stateErr != nil {
		return
	}
	content := i18n.WithLocale(state.Locale, "remote_channel_ai_offline_notification")

	// 1) 优先：sessionWebhook 仍在时效内 → 复用 ChatbotReplier
	if sessionWebhook != "" {
		replier := dingtalkchatbot.NewChatbotReplier()
		if err := replier.SimpleReplyText(ctx, sessionWebhook, []byte(content)); err == nil {
			applog.Info("钉钉下线通知已发送（sessionWebhook）", "sender_id", senderID)
			return
		}
		applog.Warn("钉钉下线通知 sessionWebhook 已失效，回退 robotCode", "sender_id", senderID, "error", err)
	}

	// 2) 兜底：access_token + robotCode + oToMessages/batchSend
	if state.RobotCode == "" {
		applog.Warn("钉钉下线通知未发送：sessionWebhook 不可用且未配置 robotCode",
			"sender_id", senderID, "hint", "在连接配置中填写 robotCode 后重连")
		return
	}
	if err := d.sendDingtalkBatchSend(ctx, state.RobotCode, senderID, content); err != nil {
		applog.Warn("钉钉下线通知发送失败（robotCode）", "sender_id", senderID, "robot_code", state.RobotCode, "error", err)
		return
	}
	applog.Info("钉钉下线通知已发送（robotCode）", "sender_id", senderID)
}

// sendDingtalkBatchSend 通过新版 oToMessages/batchSend 端点向单个 staff_id 主动发送文本。
// 仅在 sessionWebhook 不可用时作为兜底通道使用。错误返回调用方处理。
func (d *dingtalkChannel) sendDingtalkBatchSend(ctx context.Context, robotCode, staffID, content string) error {
	tok, err := d.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 access_token 失败: %w", err)
	}
	msgParam, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return fmt.Errorf("序列化 msgParam 失败: %w", err)
	}
	payload := map[string]any{
		"robotCode": robotCode,
		"userIds":   []string{staffID},
		"msgKey":    "sampleText",
		"msgParam":  string(msgParam),
	}
	body, _ := json.Marshal(payload)
	url := "https://api.dingtalk.com/v1.0/robot/oToMessages/batchSend"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", tok)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("oToMessages/batchSend HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var r struct {
		ProcessQueryKey           string   `json:"processQueryKey"`
		InvalidStaffIdList        []string `json:"invalidStaffIdList"`
		FlowControlledStaffIdList []string `json:"flowControlledStaffIdList"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("解析响应失败: %w (body=%s)", err, string(raw))
	}
	if len(r.InvalidStaffIdList) > 0 {
		return fmt.Errorf("staffId 无效: %v", r.InvalidStaffIdList)
	}
	if len(r.FlowControlledStaffIdList) > 0 {
		return fmt.Errorf("staffId 被限流: %v", r.FlowControlledStaffIdList)
	}
	return nil
}

// getAccessToken 从缓存读取，必要时刷新。提前 5 分钟过期则重取。
func (d *dingtalkChannel) getAccessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.accessToken != "" && time.Until(d.tokenExpiresAt) > 5*time.Minute {
		return d.accessToken, nil
	}
	clientID, clientSecret, err := d.loadCredentials(ctx)
	if err != nil {
		return "", err
	}
	url := "https://oapi.dingtalk.com/gettoken?appkey=" + clientID + "&appsecret=" + clientSecret
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		Errmsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if r.ErrCode != 0 || r.AccessToken == "" {
		return "", fmt.Errorf("钉钉 gettoken 失败: errcode=%d errmsg=%s", r.ErrCode, r.Errmsg)
	}
	d.accessToken = r.AccessToken
	if r.ExpiresIn > 0 {
		d.tokenExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	} else {
		d.tokenExpiresAt = time.Now().Add(2 * time.Hour)
	}
	return d.accessToken, nil
}

// getStatus returns the current channel status。
func (d *dingtalkChannel) getStatus(c *gin.Context) {
	state, err := d.store.ReadState(c.Request.Context(), ChannelTypeDingtalk)
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
	cliCfg := d.readChannelCLIConfig(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{
		"status":       string(status),
		"connected_at": state.ConnectedAt,
		"bot_id":       state.BotID,
		"robot_code":   state.RobotCode,
		"cli_type":     cliCfg.CLICType,
		"cli_model":    cliCfg.CLIModel,
	})
}

// verifyCredentials 通过 gettoken 校验钉钉凭据。
// 入参 TrimSpace：clientID/clientSecret 会拼进 URL 查询串，
// 带空格时钉钉直接返回 400（表现为「凭据填了却验证失败」）。
func (d *dingtalkChannel) verifyCredentials(c *gin.Context) {
	var req struct {
		ClientID     string `json:"client_id" binding:"required"`
		ClientSecret string `json:"client_secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	clientID := strings.TrimSpace(req.ClientID)
	clientSecret := strings.TrimSpace(req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	url := "https://oapi.dingtalk.com/gettoken?appkey=" + neturl.QueryEscape(clientID) + "&appsecret=" + neturl.QueryEscape(clientSecret)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := d.httpClient.Do(httpReq)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
		Errmsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.ErrCode != 0 || r.AccessToken == "" {
		applog.Warn("钉钉凭据校验失败", "errcode", r.ErrCode, "errmsg", r.Errmsg)
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_invalid", "credential_invalid")
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true})
}

// connect 接收凭据并启动/重建连接。
// ClientSecret 为空时视为"保留原 Secret"，仅更新 ClientID / RobotCode / 状态。
// 上线通知不再依赖用户填写的 admin_id，改为自动从 inbox 最近一条 sender_id 推断。
// robotCode 可选：用于 sessionWebhook 失效时回退到 oToMessages/batchSend 端点向指定 staff_id 主动推送。
//
// 入参一律 TrimSpace：client_id/robot_code 带首尾空格会导致钉钉侧鉴权/推送失败。
// 等待首轮建连后若状态已是 error，返回 4xx 让前端展示失败，不再返回 200 造成假成功。
func (d *dingtalkChannel) connect(c *gin.Context) {
	var req struct {
		ClientID     string `json:"client_id" binding:"required"`
		ClientSecret string `json:"client_secret"`
		RobotCode    string `json:"robot_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	ctx := c.Request.Context()
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		i18n.Error(c, http.StatusBadRequest, "remote_channel_credential_required", "credential_required")
		return
	}
	// 仅当 ClientSecret 非空时覆盖凭据存储，留空表示保留原有 Secret
	if secret := strings.TrimSpace(req.ClientSecret); secret != "" {
		if err := d.store.WriteCredential(ctx, ChannelTypeDingtalk, secret); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "remote_channel_credential_save_failed", "credential_save_failed")
			return
		}
	}
	state, _ := d.store.ReadState(ctx, ChannelTypeDingtalk)
	state.ChannelType = ChannelTypeDingtalk
	state.BotID = clientID
	state.RobotCode = strings.TrimSpace(req.RobotCode)
	if state.RobotCode == "" {
		state.RobotCode = clientID // robotCode 与 ClientID 相同，未填写时默认取 ClientID
	}
	state.Status = string(ChannelStatusConnecting)
	if err := d.store.WriteState(ctx, state); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_state_save_failed", "state_save_failed")
		return
	}
	// 重置 access_token 缓存
	d.tokenMu.Lock()
	d.accessToken = ""
	d.tokenExpiresAt = time.Time{}
	d.tokenMu.Unlock()
	// 关键：rootCtx 必须派生自长生命周期父 ctx（Start 注入的 serverCtx 或回退 background），
	// 而非 c.Request.Context()。HTTP 请求结束时 ctx 被取消，会连带把 connectLoop、
	// outboxDeliveryLoop、stream 长连接一起杀掉。
	//
	// started=true 路径必须 Stop+重建，否则手动「点击连接」只是把 status 改写为 connecting，
	// 实际旧的 connectLoop 已退出、新的又没启动——UI 提示「连接成功」但状态长时间不变，
	// 与只有「重启程序」才能恢复一致。修复对齐 wecomChannel.connect 的写法。
	d.mu.Lock()
	parent := d.serverCtx
	if parent == nil {
		parent = context.Background()
	}
	if !d.started {
		d.serverCtx = parent
		d.rootCtx, d.cancel = context.WithCancel(parent)
		d.started = true
		d.stopped = false
		loopCtx := d.rootCtx
		d.mu.Unlock()
		go d.connectLoop(loopCtx)
		go d.outboxDeliveryLoop(loopCtx)
		go d.collectResultsLoop(loopCtx)
	} else {
		d.mu.Unlock()
		d.Stop()
		d.mu.Lock()
		d.rootCtx, d.cancel = context.WithCancel(parent)
		d.started = true
		d.stopped = false
		loopCtx := d.rootCtx
		d.mu.Unlock()
		go d.connectLoop(loopCtx)
		go d.outboxDeliveryLoop(loopCtx)
		go d.collectResultsLoop(loopCtx)
	}
	time.Sleep(2 * time.Second)
	state, _ = d.store.ReadState(ctx, ChannelTypeDingtalk)
	// 首轮建连已明确失败（status 在连接前被置为 connecting，error 只可能来自本轮）：
	// 返回 4xx，避免前端提示「连接成功」而实际连不上。
	if state.Status == string(ChannelStatusError) {
		applog.Warn("钉钉连接失败，返回失败状态", "client_id", clientID)
		i18n.Error(c, http.StatusBadRequest, "remote_channel_connect_failed", "connect_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       state.Status,
		"connected_at": state.ConnectedAt,
	})
}

// disable 停用通道但保留凭据。
func (d *dingtalkChannel) disable(c *gin.Context) {
	ctx := c.Request.Context()
	// 主动停用前先给最近对话过的用户发"AI员工已下线"通知，
	// 顺序：业务消息先发（依赖 sessionWebhook / robotCode），再 UpdateStatus + Stop。
	// 通知失败仅记 warn，不影响停用流程。
	d.sendOfflineNotification(ctx)
	if err := d.store.UpdateStatus(ctx, ChannelTypeDingtalk, string(ChannelStatusDisabled)); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_disable_failed", "disable_failed")
		return
	}
	d.Stop()
	c.JSON(http.StatusOK, gin.H{"status": string(ChannelStatusDisabled)})
}

type dingtalkCLIConfigResult struct {
	CLICType  string
	CLIModel  string
	Workspace string
	Enabled   bool
}

func (d *dingtalkChannel) readChannelCLIConfig(ctx context.Context) dingtalkCLIConfigResult {
	var cfg dingtalkCLIConfigResult
	_ = d.db.QueryRowContext(ctx, `
		SELECT cli_type, cli_model, workspace, enabled FROM gt_remote_channel_cli_configs WHERE channel_type=?`,
		ChannelTypeDingtalk).Scan(&cfg.CLICType, &cfg.CLIModel, &cfg.Workspace, &cfg.Enabled)
	if !cfg.Enabled {
		cfg.Enabled = true
	}
	return cfg
}

func (d *dingtalkChannel) getCLIConfig(c *gin.Context) {
	cfg := d.readChannelCLIConfig(c.Request.Context())
	workspace := strings.TrimSpace(cfg.Workspace)
	if workspace == "" {
		workspace = d.taskRoot + "/dingtalk_workspace"
	}
	// 显式使用 snake_case 字段名与前端 CLIConfig 契约对齐
	c.JSON(http.StatusOK, gin.H{
		"cli_type":  cfg.CLICType,
		"cli_model": cfg.CLIModel,
		"workspace": workspace,
		"enabled":   cfg.Enabled,
	})
}

func (d *dingtalkChannel) saveCLIConfig(c *gin.Context) {
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
	_, err := d.db.ExecContext(c.Request.Context(), `
		INSERT INTO gt_remote_channel_cli_configs (channel_type, cli_type, cli_model, workspace, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_type) DO UPDATE SET
			cli_type=excluded.cli_type,
			cli_model=excluded.cli_model,
			workspace=excluded.workspace,
			enabled=excluded.enabled,
			updated_at=excluded.updated_at`,
		ChannelTypeDingtalk, req.CLICType, req.CLIModel, req.Workspace, enabledInt, now)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "remote_channel_cli_config_save_failed", "channel_cli_config_save_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
