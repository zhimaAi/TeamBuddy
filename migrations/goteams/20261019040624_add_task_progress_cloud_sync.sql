-- +goose Up
-- 每条沟通记录自己的上报状态。
-- CLI 会话可以把上报状态挂在 gt_cli_sessions 上（一段会话一个水位），但 Vibe 手动模式
-- 没有受管会话，记录行必须自己保存「是否已上报 / 失败原因 / 上报时间」，界面才能逐条展示。
ALTER TABLE gt_task_progress ADD COLUMN cloud_sync_status TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_task_progress ADD COLUMN cloud_sync_error TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_task_progress ADD COLUMN cloud_synced_at INTEGER NOT NULL DEFAULT 0;

-- 回填历史数据：当前序号的快照已整体投递成功时，快照里的沟通记录视为已上报，
-- 时间取该次上报的实际成功时间，避免升级后老记录一律显示「待上报」。
UPDATE gt_task_progress
SET cloud_sync_status = 'success',
    cloud_synced_at = COALESCE((
        SELECT o.last_success_at FROM gt_cloud_sync_outbox o
        JOIN gt_tasks t ON t.uuid = o.task_uuid AND t.execution_index = o.execution_index
        WHERE o.task_uuid = gt_task_progress.task_uuid AND o.delivered_revision >= o.revision
        ORDER BY o.last_success_at DESC LIMIT 1), 0)
WHERE cloud_sync_status = ''
  AND execution_mode = 'vibe_coding'
  AND external_session_id <> ''
  AND activity_kind IN ('user_message', 'final_response');
