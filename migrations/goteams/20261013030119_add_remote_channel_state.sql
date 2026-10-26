-- +goose Up
-- 多渠道扩展：统一通道状态表 + 队列通道列
--
-- 通道状态表（替代 gt_remote_wechat_state 单例）
-- status 取值：disconnected | connecting | connected | error | disabled
-- - disconnected：未连接（默认）
-- - connecting：正在建连
-- - connected：已连接
-- - error：上次连接失败
-- - disabled：用户手动停用——应用启动时跳过自动建连
CREATE TABLE IF NOT EXISTS gt_remote_channel_state (
    channel_type          TEXT PRIMARY KEY,  -- wechat/wecom/feishu/dingtalk
    bot_id                TEXT NOT NULL DEFAULT '',  -- 应用标识：微信 bot_id / 企微 BotID / 飞书 AppID / 钉钉 ClientID
    sender_id             TEXT NOT NULL DEFAULT '',
    locale                TEXT NOT NULL DEFAULT 'zh-CN',
    base_url              TEXT NOT NULL DEFAULT '',
    cursor                TEXT NOT NULL DEFAULT '',
    bound_task_uuid       TEXT NOT NULL DEFAULT '',
    pending_cancel_task_uuid TEXT NOT NULL DEFAULT '',
    pending_resume_task_uuid TEXT NOT NULL DEFAULT '',
    pending_done_task_uuid   TEXT NOT NULL DEFAULT '',
    context_revision      INTEGER NOT NULL DEFAULT 0,
    connected_at          INTEGER NOT NULL DEFAULT 0,
    status                TEXT NOT NULL DEFAULT 'disconnected',  -- disconnected/connecting/connected/error/disabled
    updated_at            INTEGER NOT NULL DEFAULT 0
);

-- inbox/outbox 增加 channel_type 列（旧数据默认 wechat）
ALTER TABLE gt_remote_wechat_inbox  ADD COLUMN channel_type TEXT NOT NULL DEFAULT 'wechat';
ALTER TABLE gt_remote_wechat_outbox ADD COLUMN channel_type TEXT NOT NULL DEFAULT 'wechat';

-- outbox 增加 reply_context（通道私有回复上下文）
ALTER TABLE gt_remote_wechat_outbox ADD COLUMN reply_context TEXT NOT NULL DEFAULT '';

-- 通道级索引
CREATE INDEX IF NOT EXISTS idx_remote_inbox_channel_pending
    ON gt_remote_wechat_inbox(channel_type, status, received_at);
CREATE INDEX IF NOT EXISTS idx_remote_outbox_channel_pending
    ON gt_remote_wechat_outbox(channel_type, status, next_attempt_at, created_at);

-- source_id 唯一约束改为 (channel_type, source_id) 复合（多通道同一 sourceID 不冲突）
-- SQLite 不支持 DROP CONSTRAINT，需重建表
CREATE TABLE IF NOT EXISTS gt_remote_wechat_outbox_new (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    channel_type TEXT NOT NULL DEFAULT 'wechat',
    task_uuid TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    blocked_context_revision INTEGER NOT NULL DEFAULT -1,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    delivered_at INTEGER NOT NULL DEFAULT 0,
    reply_context TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX "idx_type_source_id" ON "gt_remote_wechat_outbox" ("channel_type","source_id");
INSERT INTO gt_remote_wechat_outbox_new (id, source_id, channel_type, task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, delivered_at, reply_context, updated_at)
SELECT id, source_id, 'wechat', task_uuid, content, status, blocked_context_revision, attempts, next_attempt_at, created_at, delivered_at, '', 0 FROM gt_remote_wechat_outbox;
DROP TABLE gt_remote_wechat_outbox;
ALTER TABLE gt_remote_wechat_outbox_new RENAME TO gt_remote_wechat_outbox;

-- 迁移已有微信状态（有行则搬，无行不建）
-- 根据 connected_at 计算 status：>0 → connected，否则 → disconnected
INSERT INTO gt_remote_channel_state (channel_type, bot_id, sender_id, locale, base_url, cursor,
        bound_task_uuid, pending_cancel_task_uuid, pending_resume_task_uuid, pending_done_task_uuid,
        context_revision, connected_at, status, updated_at)
SELECT 
    'wechat', 
    bot_id, sender_id, locale, base_url, cursor,
    bound_task_uuid, '', '', '',  -- pending 列在原表不存在，迁移后置空
    context_revision, connected_at,
    CASE WHEN connected_at > 0 THEN 'connected' ELSE 'disconnected' END,
    updated_at
FROM gt_remote_wechat_state WHERE id=1
ON CONFLICT(channel_type) DO NOTHING;

-- +goose Down
-- 不处理 goose down
