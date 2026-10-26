-- +goose Up
-- 远程通道 inbox 增加 session_webhook 列：用于在连接断开重连时，向最近一次对话的人发送上线通知。
-- 仅钉钉 Stream 模式会写入该字段（chatbot 回调携带的临时 webhook URL，钉钉侧有 5-30 分钟时效）；
-- 企微/飞书不使用该字段，默认空字符串。
-- 重新连接时 sendOnlineNotification 优先用 sessionWebhook 走 ChatbotReplier 发送（最快速路径），
-- 失败（webhook 已过期）时再回退到 robotCode + oToMessages/batchSend。
ALTER TABLE gt_remote_wechat_inbox ADD COLUMN session_webhook TEXT NOT NULL DEFAULT '';

-- +goose Down
-- 不处理 goose down
