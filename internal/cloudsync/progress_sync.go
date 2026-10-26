package cloudsync

import (
	"context"
	"database/sql"

	"goteams-client/internal/applog"
)

// Vibe 沟通记录：只有这两类 activity_kind 会作为会话轮次上报给线上，
// 其余（如 skill_activity）不参与上报，不能回写上报状态。
const (
	vibeActivityUserMessage   = "user_message"
	vibeActivityFinalResponse = "final_response"
)

// progressWatermark 返回本次快照覆盖到的 Vibe 沟通记录最大 rowid。
// rowid 单调递增，投递结果可以据此精确回写到「快照里包含的那几条记录」，
// 不依赖 created_at 时钟，也不会误标快照之后新到的记录。
func progressWatermark(ctx context.Context, q syncQueryer, taskUUID string) (int64, error) {
	var watermark int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM gt_task_progress
		WHERE task_uuid=? AND execution_mode='vibe_coding' AND external_session_id<>''
		AND activity_kind IN (?, ?)`,
		taskUUID, vibeActivityUserMessage, vibeActivityFinalResponse).Scan(&watermark)
	return watermark, err
}

// markProgressSynced 把本次投递成功的 Vibe 记录标记为已上报。
// 只回写还不是 success 的行：已成功的记录保留各自首次上报时间，
// 不会被后续每次推送刷新成最新的时间。
func markProgressSynced(ctx context.Context, tx *sql.Tx, snapshot executionSnapshot, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE gt_task_progress SET cloud_sync_status='success',
		cloud_sync_error='', cloud_synced_at=?
		WHERE task_uuid=? AND rowid<=? AND cloud_sync_status<>'success'
		AND execution_mode='vibe_coding' AND external_session_id<>''
		AND activity_kind IN (?, ?)`,
		now, snapshot.Task.LocalTaskUUID, snapshot.ProgressWatermark,
		vibeActivityUserMessage, vibeActivityFinalResponse)
	return err
}

// markProgressSyncFailed 把本次没有投递成功的 Vibe 记录标记为上报失败。
// 已成功的记录不动：它们在更早的推送里已经落到云端了。
func markProgressSyncFailed(ctx context.Context, db *sql.DB, snapshot executionSnapshot, cause error) {
	if db == nil || snapshot.ProgressWatermark <= 0 {
		return
	}
	reason := truncateRunes(cause.Error(), syncErrorMaxRunes)
	if _, err := db.ExecContext(ctx, `UPDATE gt_task_progress SET cloud_sync_status='failed', cloud_sync_error=?
		WHERE task_uuid=? AND rowid<=? AND cloud_sync_status<>'success'
		AND execution_mode='vibe_coding' AND external_session_id<>''
		AND activity_kind IN (?, ?)`,
		reason, snapshot.Task.LocalTaskUUID, snapshot.ProgressWatermark,
		vibeActivityUserMessage, vibeActivityFinalResponse); err != nil {
		applog.Warn("[CloudSync] 写入沟通记录上报失败状态失败",
			"task_uuid", snapshot.Task.LocalTaskUUID, "error", err.Error())
	}
}
