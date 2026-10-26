-- +goose Up
-- 远程通道 state 增加 robot_code 列：钉钉 Stream 模式下，sessionWebhook 失效时通过新版
-- oToMessages/batchSend 端点主动推送"AI员工已成功链接"通知到指定 staff_id。
-- robotCode 在钉钉开发者后台「机器人」配置中查看，对应具体机器人身份。
-- 仅钉钉通道使用；企微/飞书不读该字段，默认空字符串。
-- 配置后钉钉 sendOnlineNotification 流程：优先 sessionWebhook（ChatbotReplier）→ 失败回退 robotCode（oToMessages/batchSend）。
ALTER TABLE gt_remote_channel_state ADD COLUMN robot_code TEXT NOT NULL DEFAULT '';

-- +goose Down
-- 不处理 goose down
