-- +goose Up
-- last_success_at：最近一次任务投影 + 步骤会话整体上报成功的实际时间（毫秒）。
-- 与 delivered_revision / revision 配合，供界面展示「整个绑定是否已上报线上」。
ALTER TABLE gt_cloud_sync_outbox ADD COLUMN last_success_at INTEGER NOT NULL DEFAULT 0;

-- cloud_sync_at：最近一次成功上报的实际时间（毫秒）。
-- 保留 cloud_synced_at 原有的「会话 updated_at 水位」语义，不用它承担展示职责。
ALTER TABLE gt_cli_sessions ADD COLUMN cloud_sync_at INTEGER NOT NULL DEFAULT 0;
