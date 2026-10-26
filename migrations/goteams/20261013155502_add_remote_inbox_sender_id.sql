-- +goose Up
-- 远程通道 inbox 增加 sender_id 列：用于上线通知自动选择通知目标。
-- sender_id 是平台侧用户标识：企微/飞书为 open_id，钉钉为 staff_id。
-- 历史行无 sender_id，留空字符串以维持兼容（上线通知在无历史行时会跳过）。
ALTER TABLE gt_remote_wechat_inbox ADD COLUMN sender_id TEXT NOT NULL DEFAULT '';

-- +goose Down
-- 不处理 goose down