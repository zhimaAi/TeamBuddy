package localserver

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/secrets"

	"github.com/google/uuid"
)

// Channel type constants
const (
	ChannelTypeWechat   = "wechat"
	ChannelTypeWecom    = "wecom"
	ChannelTypeFeishu   = "feishu"
	ChannelTypeDingtalk = "dingtalk"
)

// IsValidChannelType 返回 channel_type 是否在已知通道集合内。
// CLI skill / 前端通过 channel_type 字段指定入队通道；未知值按 wechat 兜底以保持兼容。
func IsValidChannelType(channelType string) bool {
	switch channelType {
	case "", ChannelTypeWechat, ChannelTypeWecom, ChannelTypeFeishu, ChannelTypeDingtalk:
		return true
	}
	return false
}

// NormalizeChannelType 返回归一化后的 channelType：未知值/空值回退为 wechat，
// 以保证 CLI skill 等不知道新通道的下游仍能工作。
func NormalizeChannelType(channelType string) string {
	if IsValidChannelType(channelType) {
		if channelType == "" {
			return ChannelTypeWechat
		}
		return channelType
	}
	return ChannelTypeWechat
}

// Channel status type and constants
type ChannelStatus string

const (
	ChannelStatusDisconnected ChannelStatus = "disconnected"
	ChannelStatusConnecting   ChannelStatus = "connecting"
	ChannelStatusConnected    ChannelStatus = "connected"
	ChannelStatusError        ChannelStatus = "error"
	ChannelStatusDisabled     ChannelStatus = "disabled"
)

// Channel state in database
type ChannelState struct {
	ChannelType           string
	BotID                 string
	SenderID              string
	RobotCode             string // 钉钉机器人 Code：sessionWebhook 失效时用于 oToMessages/batchSend 主动推送
	Locale                string
	BaseURL               string
	Cursor                string
	BoundTaskUUID         string
	PendingCancelTaskUUID string
	PendingResumeTaskUUID string
	PendingDoneTaskUUID   string
	ContextRevision       int64
	ConnectedAt           int64
	Status                string // disconnected/connecting/connected/error/disabled
	UpdatedAt             int64
}

// Table names for inbox/outbox
const (
	remoteInboxTable  = "gt_remote_wechat_inbox"
	remoteOutboxTable = "gt_remote_wechat_outbox"
)

// RemoteChannel is the interface that all channel transports must implement.
// Each channel (WeChat, WeCom, Feishu, DingTalk) implements this interface.
type RemoteChannel interface {
	// ChannelType returns the channel type identifier (wechat/wecom/feishu/dingtalk)
	ChannelType() string

	// Start starts the channel transport (connection/heartbeat/reconnection)
	Start(ctx context.Context)

	// Stop stops the channel transport
	Stop()

	// Status returns the current channel status
	Status(ctx context.Context) ChannelStatus

	// RegisterRoutes registers the channel-specific HTTP routes
	RegisterRoutes(r *gin.RouterGroup)
}

// channelStateStore provides database access for channel state
type channelStateStore struct {
	db    *sql.DB
	store secrets.Store
	mu    sync.RWMutex

	// broadcast：状态写入成功后向前端推送（装配阶段由 server.go 注入 wsHub 广播，
	// 未注入时为 nil，静默跳过）。
	broadcast func(channelType, status string)
	// lastBroadcast：各通道最后一次已广播的状态，用于去重。
	// WriteState 还会被指令处理等高业务路径调用（只改 bound_task_uuid / cursor 等），
	// 只有 status 真正变化才推送，避免前端被无意义的刷新打爆。
	broadcastMu   sync.Mutex
	lastBroadcast map[string]string
}

// SetStatusBroadcaster 注入通道状态广播函数，由 server.go 在装配阶段调用一次。
// 未注入时所有状态写入照常执行，只是不推送（保持 channelStateStore 可独立测试）。
func (cs *channelStateStore) SetStatusBroadcaster(fn func(channelType, status string)) {
	cs.mu.Lock()
	cs.broadcast = fn
	cs.mu.Unlock()
}

// notifyStatus 在状态写入成功后推送一次 remote.<channel>.status。
// 前端 RemoteChannels.vue 用 useLocalWS 订阅了 wechat/wecom/feishu/dingtalk 四个事件，
// 之前只有微信通道广播过，其余三通道的状态变化只能等 10s 轮询，用户点「连接」后
// 看不到即时反馈。这里统一补齐，且同一状态重复写入不会重复推送。
func (cs *channelStateStore) notifyStatus(channelType, status string) {
	cs.mu.RLock()
	fn := cs.broadcast
	cs.mu.RUnlock()
	if fn == nil {
		return
	}
	cs.broadcastMu.Lock()
	if cs.lastBroadcast == nil {
		cs.lastBroadcast = make(map[string]string)
	}
	if prev, ok := cs.lastBroadcast[channelType]; ok && prev == status {
		cs.broadcastMu.Unlock()
		return
	}
	cs.lastBroadcast[channelType] = status
	cs.broadcastMu.Unlock()
	fn(channelType, status)
}

// TrimChannelStateFields 归一化通道状态里的用户可输入字段：去掉首尾空白。
// 用户在配置页复制粘贴 app_id / bot_id / robot_code 时容易带上空格，
// 平台侧（钉钉 gettoken 拼 URL、企微 aibot_subscribe body、飞书 bootstrap 请求）
// 对空白敏感，会导致「凭据看着没错但连不上」。
//
// 读写两个方向都要处理：
//   - 写入方向（WriteState）：新提交的配置立即归一化，避免脏数据落库；
//   - 读取方向（ReadState）：兼容历史已落库的带空格数据，不需要迁移脚本。
func TrimChannelStateFields(state *ChannelState) {
	state.BotID = strings.TrimSpace(state.BotID)
	state.SenderID = strings.TrimSpace(state.SenderID)
	state.RobotCode = strings.TrimSpace(state.RobotCode)
	state.Locale = strings.TrimSpace(state.Locale)
	state.BaseURL = strings.TrimSpace(state.BaseURL)
	state.Cursor = strings.TrimSpace(state.Cursor)
}

// newChannelStateStore creates a new channel state store
func newChannelStateStore(db *sql.DB, store secrets.Store) *channelStateStore {
	return &channelStateStore{db: db, store: store}
}

// ReadState reads the channel state from database
func (cs *channelStateStore) ReadState(ctx context.Context, channelType string) (ChannelState, error) {
	var state ChannelState
	err := cs.db.QueryRowContext(ctx, `
		SELECT channel_type, bot_id, sender_id, robot_code, locale, base_url, cursor,
		       bound_task_uuid, pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
		       context_revision, connected_at, status, updated_at
		FROM gt_remote_channel_state WHERE channel_type=?`, channelType).Scan(
		&state.ChannelType, &state.BotID, &state.SenderID, &state.RobotCode, &state.Locale, &state.BaseURL, &state.Cursor,
		&state.BoundTaskUUID, &state.PendingCancelTaskUUID, &state.PendingResumeTaskUUID, &state.PendingDoneTaskUUID,
		&state.ContextRevision, &state.ConnectedAt, &state.Status, &state.UpdatedAt)
	if err != nil {
		return ChannelState{}, err
	}
	TrimChannelStateFields(&state)
	return state, nil
}

// WriteState writes the channel state to database
func (cs *channelStateStore) WriteState(ctx context.Context, state ChannelState) error {
	TrimChannelStateFields(&state)
	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO gt_remote_channel_state
			(channel_type, bot_id, sender_id, robot_code, locale, base_url, cursor, bound_task_uuid,
			 pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
			 context_revision, connected_at, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_type) DO UPDATE SET
			bot_id=excluded.bot_id,
			sender_id=excluded.sender_id,
			robot_code=excluded.robot_code,
			locale=excluded.locale,
			base_url=excluded.base_url,
			cursor=excluded.cursor,
			bound_task_uuid=excluded.bound_task_uuid,
			pending_cancel_task_uuid=excluded.pending_cancel_task_uuid,
			pending_resume_task_uuid=excluded.pending_resume_task_uuid,
			pending_done_task_uuid=excluded.pending_done_task_uuid,
			context_revision=excluded.context_revision,
			connected_at=excluded.connected_at,
			status=excluded.status,
			updated_at=excluded.updated_at`,
		state.ChannelType, state.BotID, state.SenderID, state.RobotCode, state.Locale, state.BaseURL, state.Cursor,
		state.BoundTaskUUID, state.PendingCancelTaskUUID, state.PendingResumeTaskUUID, state.PendingDoneTaskUUID,
		state.ContextRevision, state.ConnectedAt, state.Status, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	cs.notifyStatus(state.ChannelType, state.Status)
	return nil
}

// UpdateStatus updates only the status field
func (cs *channelStateStore) UpdateStatus(ctx context.Context, channelType, status string) error {
	_, err := cs.db.ExecContext(ctx, `
		UPDATE gt_remote_channel_state SET status=?, updated_at=? WHERE channel_type=?`,
		status, time.Now().UnixMilli(), channelType)
	if err != nil {
		return err
	}
	cs.notifyStatus(channelType, status)
	return nil
}

// UpdateConnectedAt updates connected_at and status
func (cs *channelStateStore) UpdateConnectedAt(ctx context.Context, channelType string, connectedAt int64, status string) error {
	_, err := cs.db.ExecContext(ctx, `
		UPDATE gt_remote_channel_state SET connected_at=?, status=?, updated_at=? WHERE channel_type=?`,
		connectedAt, status, time.Now().UnixMilli(), channelType)
	if err != nil {
		return err
	}
	cs.notifyStatus(channelType, status)
	return nil
}

// ClearConnection clears connection-related fields
func (cs *channelStateStore) ClearConnection(ctx context.Context, channelType string) error {
	_, err := cs.db.ExecContext(ctx, `
		UPDATE gt_remote_channel_state SET 
			connected_at=0, status='disconnected', base_url='', cursor='', bot_id='', sender_id='',
			updated_at=? WHERE channel_type=?`,
		time.Now().UnixMilli(), channelType)
	if err != nil {
		return err
	}
	cs.notifyStatus(channelType, string(ChannelStatusDisconnected))
	return nil
}

// GetSubscribedChannels returns all channels that have bound to a specific task
func (cs *channelStateStore) GetSubscribedChannels(ctx context.Context, taskUUID string) ([]ChannelState, error) {
	rows, err := cs.db.QueryContext(ctx, `
		SELECT channel_type, bot_id, sender_id, robot_code, locale, base_url, cursor,
		       bound_task_uuid, pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
		       context_revision, connected_at, status, updated_at
		FROM gt_remote_channel_state
		WHERE bound_task_uuid=? AND status='connected'`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []ChannelState
	for rows.Next() {
		var state ChannelState
		if err := rows.Scan(
			&state.ChannelType, &state.BotID, &state.SenderID, &state.RobotCode, &state.Locale, &state.BaseURL, &state.Cursor,
			&state.BoundTaskUUID, &state.PendingCancelTaskUUID, &state.PendingResumeTaskUUID, &state.PendingDoneTaskUUID,
			&state.ContextRevision, &state.ConnectedAt, &state.Status, &state.UpdatedAt); err != nil {
			return nil, err
		}
		TrimChannelStateFields(&state)
		channels = append(channels, state)
	}
	return channels, rows.Err()
}

// CredentialKey returns the secrets store key for a channel's app_secret
func CredentialKey(channelType string) string {
	return fmt.Sprintf("goteams:remote_%s_secret", channelType)
}

// ReadCredential reads the credential (app_secret) from secrets store
// 返回前 trim：与 WriteCredential 对称，兼容历史落库的带空格凭据。
func (cs *channelStateStore) ReadCredential(_ context.Context, channelType string) (string, error) {
	secret, err := cs.store.Get(CredentialKey(channelType))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(secret), nil
}

// WriteCredential writes the credential (app_secret) to secrets store
// 写入前 trim：用户从平台复制 AppSecret 时容易带首尾空格，
// 空格会随请求体/签名一起发给平台导致校验失败，这里统一归一化。
func (cs *channelStateStore) WriteCredential(_ context.Context, channelType, secret string) error {
	return cs.store.Set(CredentialKey(channelType), strings.TrimSpace(secret))
}

// DeleteCredential deletes the credential from secrets store
func (cs *channelStateStore) DeleteCredential(_ context.Context, channelType string) error {
	return cs.store.Delete(CredentialKey(channelType))
}

// LatestInboxSenderWithWebhook returns the most recent non-empty sender_id and its session_webhook
// for the channel.
// 用于钉钉 sendOnlineNotification：先尝试用 sessionWebhook（ChatbotReplier）发送，失效时再回退 robotCode。
// 历史行 sender_id 为空时跳过；通道从未收到消息时返回 ("", "", sql.ErrNoRows)。
func LatestInboxSenderWithWebhook(ctx context.Context, db *sql.DB, channelType string) (senderID, sessionWebhook string, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT sender_id, session_webhook FROM `+remoteInboxTable+`
		WHERE channel_type=? AND sender_id<>''
		ORDER BY received_at DESC, rowid DESC LIMIT 1`, channelType).Scan(&senderID, &sessionWebhook)
	return senderID, sessionWebhook, err
}

// LatestInboxSender returns the most recent non-empty sender_id for the channel.
// 用于 sendOnlineNotification 自动选择通知目标：上次主动找我对话的人 = 上线通知发送对象。
// 历史行 sender_id 为空时跳过；通道从未收到消息时返回 ("", sql.ErrNoRows)。
func LatestInboxSender(ctx context.Context, db *sql.DB, channelType string) (string, error) {
	senderID, _, err := LatestInboxSenderWithWebhook(ctx, db, channelType)
	return senderID, err
}

// Inbox operations

// EnqueueInbox inserts a new message into the inbox
// senderID 是平台侧用户标识：企微/飞书为 open_id，钉钉为 staff_id；用于上线通知自动选择目标。
// sessionWebhook 仅钉钉 Stream 模式有意义（chatbot 回调携带的临时 webhook URL，5-30 分钟时效）；
// 企微/飞书不读该字段，传入空字符串即可。
//
// 返回 inboxKey：写入 gt_remote_wechat_inbox.message_id 的实际值，由 uuid + ":" + 原 messageID 拼成。
// inbox 表主键为单列 message_id，给不同通道/平台的消息加 UUID 前缀可以彻底避免跨通道
// 命名空间撞键（钉钉 MsgId、企微 MsgID 都是纯数字 ID），也避免微信事务内直写
// （`botID+":"+msgID`）与其他 transport 入库格式不一致。inboxKey 必须一路透传到
// Handle / FinishInbound 等以 messageID 寻址 inbox 的接口，调用方不能再用原 messageID。
//
// 微信 saveUpdates 走事务内直写 inbox（`botID+":"+msgID`），与本函数格式不同但都已带通道前缀
// 隔离，跨通道不会撞键；如果未来要统一为 UUID 前缀，需要同步改为事务内调用 + msgID→inboxKey
// session 内缓存来维持 INSERT OR IGNORE 去重语义。
func EnqueueInbox(ctx context.Context, db *sql.DB, channelType, messageID, senderID, content, sessionWebhook string) (string, error) {
	now := time.Now().UnixMilli()
	inboxKey := uuid.NewString() + ":" + messageID
	res, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO `+remoteInboxTable+`
			(message_id, text_content, status, task_uuid, session_uuid, next_recovery_at, recovery_attempts, received_at, updated_at, channel_type, sender_id, session_webhook)
		VALUES (?, ?, 'pending', '', '', 0, 0, ?, ?, ?, ?, ?)`,
		inboxKey, content, now, now, channelType, senderID, sessionWebhook)
	if err != nil {
		return "", err
	}
	// INSERT OR IGNORE 在主键冲突时退化为 no-op，不返回错误。
	// 当前 inboxKey = uuid + ":" + messageID，正常路径下 UUID 唯一，必然能插入新行；
	// 一旦 RowsAffected = 0，说明发生了隐性冲突（例如未来 transport 改成不带 UUID 前缀，
	// 或 DB schema 出现意外的共享命名空间），下游 FinishInbound 会把当前消息的 task_uuid/
	// session_uuid 写到错误的消息行上。这里显式抛错，使异常状态可观测、可定位。
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("inbox 入队被跳过（主键冲突或 INSERT OR IGNORE no-op）：channel=%s message_id=%s", channelType, messageID)
	}
	return inboxKey, nil
}

// ClaimInbox claims a pending message for processing
func ClaimInbox(ctx context.Context, db *sql.DB, channelType string) (messageID, content string, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT message_id, text_content FROM `+remoteInboxTable+`
		WHERE channel_type=? AND status='pending' 
		ORDER BY received_at LIMIT 1`,
		channelType).Scan(&messageID, &content)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}

	// Mark as processing
	now := time.Now().UnixMilli()
	_, err = db.ExecContext(ctx, `
		UPDATE `+remoteInboxTable+` SET status='processing', updated_at=? 
		WHERE message_id=? AND channel_type=? AND status='pending'`,
		now, messageID, channelType)
	if err != nil {
		return "", "", err
	}
	return messageID, content, nil
}

// FinishInboxProcessing marks an inbox message as dispatched
func FinishInboxProcessing(ctx context.Context, db *sql.DB, channelType, messageID, taskUUID, sessionUUID string) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteInboxTable+` 
		SET status='dispatched', task_uuid=?, session_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type=?`,
		taskUUID, sessionUUID, now, messageID, channelType)
	return err
}

// FinishDispatched marks a dispatched inbox message as done
func FinishDispatched(ctx context.Context, db *sql.DB, channelType, messageID string) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteInboxTable+` SET status='done', updated_at=?
		WHERE message_id=? AND channel_type=? AND status='dispatched'`,
		now, messageID, channelType)
	return err
}

// CollectDispatchedMessages collects dispatched messages for result recovery
func CollectDispatchedMessages(ctx context.Context, db *sql.DB, channelType string, beforeTime int64, limit int) ([]struct {
	MessageID   string
	TaskUUID    string
	SessionUUID string
}, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT message_id, task_uuid, session_uuid FROM `+remoteInboxTable+`
		WHERE channel_type=? AND status='dispatched' AND session_uuid NOT LIKE 'codex:%'
		  AND next_recovery_at<=? ORDER BY next_recovery_at LIMIT ?`,
		channelType, beforeTime, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []struct {
		MessageID   string
		TaskUUID    string
		SessionUUID string
	}
	for rows.Next() {
		var msg struct {
			MessageID   string
			TaskUUID    string
			SessionUUID string
		}
		if err := rows.Scan(&msg.MessageID, &msg.TaskUUID, &msg.SessionUUID); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// MarkProcessingTask marks an inbox message as processing with task info
func MarkProcessingTask(ctx context.Context, db *sql.DB, channelType, messageID, taskUUID string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteInboxTable+` SET status='processing', task_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type=? AND status='pending'`,
		taskUUID, time.Now().UnixMilli(), messageID, channelType)
	return err
}

// DeferCLIResult marks an inbox message for CLI result recovery
func DeferCLIResult(ctx context.Context, db *sql.DB, channelType, messageID string, nextRecoveryAt int64) error {
	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteInboxTable+` 
		SET next_recovery_at=?, recovery_attempts=recovery_attempts+1, updated_at=?
		WHERE message_id=? AND channel_type=?`,
		nextRecoveryAt, time.Now().UnixMilli(), messageID, channelType)
	return err
}

// Outbox operations

// EnqueueOutbox inserts a message into the outbox
func EnqueueOutbox(ctx context.Context, db *sql.DB, channelType, sourceID, taskUUID, content, replyContext string) error {
	now := time.Now().UnixMilli()
	// id 用 UUID（与 source_id 完全独立）；source_id 在投递幂等/回复去重时由 caller 决定。
	_, err := db.ExecContext(ctx, `
		INSERT INTO `+remoteOutboxTable+`
			(id, source_id, channel_type, task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, reply_context)
		VALUES (?, ?, ?, ?, ?, 'pending', -1, 0, ?, ?, ?)`,
		uuid.NewString(), sourceID, channelType, taskUUID, content, now, now, replyContext)
	return err
}

// ClaimOutbox claims a pending outbox message for delivery
func ClaimOutbox(ctx context.Context, db *sql.DB, channelType string, blockedRevision int64) (id, content, replyContext string, attempts int, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT id, content, reply_context, attempts FROM `+remoteOutboxTable+`
		WHERE channel_type=? AND status='pending' AND next_attempt_at<=? AND blocked_context_revision<>?
		ORDER BY created_at LIMIT 1`,
		channelType, time.Now().UnixMilli(), blockedRevision).Scan(&id, &content, &replyContext, &attempts)
	if err == sql.ErrNoRows {
		return "", "", "", 0, nil
	}
	return id, content, replyContext, attempts, err
}

// MarkOutboxDelivered marks an outbox message as delivered
func MarkOutboxDelivered(ctx context.Context, db *sql.DB, channelType, id string) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteOutboxTable+` SET status='delivered', delivered_at=?, updated_at=?
		WHERE id=? AND channel_type=?`,
		now, now, id, channelType)
	return err
}

// MarkOutboxFailed marks an outbox message delivery as failed
func MarkOutboxFailed(ctx context.Context, db *sql.DB, channelType, id string, attempts int) error {
	now := time.Now().UnixMilli()
	// Exponential backoff: 5s, 15s, 45s, 2m, 5m, 15m
	backoffMs := []int64{5000, 15000, 45000, 120000, 300000, 900000}
	delayIdx := attempts
	if delayIdx >= len(backoffMs) {
		delayIdx = len(backoffMs) - 1
	}
	nextAttempt := now + backoffMs[delayIdx]

	_, err := db.ExecContext(ctx, `
		UPDATE `+remoteOutboxTable+` SET attempts=?, next_attempt_at=?, updated_at=?
		WHERE id=? AND channel_type=?`,
		attempts, nextAttempt, now, id, channelType)
	return err
}

// GetOutboxByID gets an outbox message by ID
func GetOutboxByID(ctx context.Context, db *sql.DB, channelType, id string) (content, replyContext string, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT content, reply_context FROM `+remoteOutboxTable+`
		WHERE id=? AND channel_type=?`, id, channelType).Scan(&content, &replyContext)
	return content, replyContext, err
}

// GetOutboxPendingCount returns the number of pending messages in outbox
func GetOutboxPendingCount(ctx context.Context, db *sql.DB, channelType string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM `+remoteOutboxTable+`
		WHERE channel_type=? AND status='pending'`, channelType).Scan(&count)
	return count, err
}

// EnqueueNotifyToChannel enqueues a notification to a specific channel
// Used for broadcasting task completion to subscribed channels
func EnqueueNotifyToChannel(ctx context.Context, db *sql.DB, channelType, senderID, taskUUID, content, sessionUUID string) error {
	// source_id format: notify:<taskUUID>:<sessionUUID>
	sourceID := fmt.Sprintf("notify:%s:%s", taskUUID, sessionUUID)
	return EnqueueOutbox(ctx, db, channelType, sourceID, taskUUID, content, senderID)
}

// GetChannelLocale returns the locale for a channel
func GetChannelLocale(ctx context.Context, db *sql.DB, channelType string) string {
	var locale string
	err := db.QueryRowContext(ctx, `
		SELECT locale FROM gt_remote_channel_state WHERE channel_type=?`, channelType).Scan(&locale)
	if err != nil {
		return "zh-CN"
	}
	return locale
}

// IsChannelConnected checks if a channel is connected
func IsChannelConnected(ctx context.Context, db *sql.DB, channelType string) bool {
	var connectedAt int64
	err := db.QueryRowContext(ctx, `
		SELECT connected_at FROM gt_remote_channel_state WHERE channel_type=?`, channelType).Scan(&connectedAt)
	if err != nil {
		applog.Warn("检查通道连接状态失败", "channel", channelType, "error", err)
		return false
	}
	return connectedAt > 0
}
