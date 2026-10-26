package cloudsync

import (
	"context"
	"database/sql"
	"fmt"

	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

// 上报状态对外取值（任务级 / 会话级共用）。
const (
	SyncStatusSuccess = "success"
	SyncStatusPending = "pending"
	SyncStatusFailed  = "failed"
)

// TaskSyncStatus 是「整个绑定是否已上报线上」的聚合状态。
//   - success：当前 revision 已全部投递（任务投影 + 全部步骤会话）
//   - failed：最近一次推送失败，last_error 为失败原因，等待重试或手动重报
//   - pending：有本地变更尚未投递，或还没有产生过 outbox 快照
type TaskSyncStatus struct {
	SourceType        string `json:"source_type"`
	Status            string `json:"status"`
	Attempts          int    `json:"attempts"`
	LastError         string `json:"last_error"`
	LastSuccessAt     int64  `json:"last_success_at"`
	Revision          int64  `json:"revision"`
	DeliveredRevision int64  `json:"delivered_revision"`
	// PendingSessions 是尚未上报成功的会话条数（含失败）。
	PendingSessions int `json:"pending_sessions"`
	// PendingExecutions 是尚未投递成功的执行序号数量（含当前序号与历史序号）。
	PendingExecutions int `json:"pending_executions"`
}

// TaskOwnedByClient 判断任务是否归属当前登录账号（账号或云端地址切换后不匹配）。
// 不匹配的任务既不会被定时轮询推送，也无法手动重报，界面应直接说明原因。
func TaskOwnedByClient(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) (bool, error) {
	if db == nil || client == nil || client.UserID() == "" {
		return false, nil
	}
	return ownsTask(ctx, db, client, taskUUID)
}

// BackfillTaskOwner 把当前云端环境下「归属缺失」的历史任务回填给当前账号。
// 登录后调用：这类任务既不会被轮询上报，也无法手动重报，回填后才会进入上报队列。
// 只处理 cloud_source_key 与当前云端地址一致的记录，不会跨环境误改归属。
func BackfillTaskOwner(ctx context.Context, db *sql.DB, client *cloud.Client) (int64, error) {
	if db == nil || client == nil || client.UserID() == "" {
		return 0, nil
	}
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	result, err := db.ExecContext(ctx, `UPDATE gt_tasks SET cloud_user_id=?
		WHERE source_type='cloud' AND COALESCE(cloud_user_id,'')='' AND cloud_source_key IN (?, ?)`,
		client.UserID(), primary, alternate)
	if err != nil {
		return 0, fmt.Errorf("回填任务归属失败: %w", err)
	}
	updated, _ := result.RowsAffected()
	// outbox 行也要一起回填，否则已排队的历史快照仍然匹配不上投递条件。
	if _, err = db.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET cloud_user_id=?
		WHERE COALESCE(cloud_user_id,'')='' AND cloud_source_key IN (?, ?)`,
		client.UserID(), primary, alternate); err != nil {
		return updated, fmt.Errorf("回填上报队列归属失败: %w", err)
	}
	return updated, nil
}

// LoadTaskSyncStatus 从 outbox 与 gt_cli_sessions 派生任务级上报状态。
// 不产生任何写入，可安全用于轮询接口。
func LoadTaskSyncStatus(ctx context.Context, db *sql.DB, taskUUID string) (TaskSyncStatus, error) {
	status := TaskSyncStatus{Status: SyncStatusPending}
	if db == nil {
		return status, fmt.Errorf("task database is not ready")
	}
	var executionIndex int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(source_type,''), COALESCE(execution_index,0)
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&status.SourceType, &executionIndex)
	if err == sql.ErrNoRows {
		return status, err
	}
	if err != nil {
		return status, err
	}
	if status.SourceType != "cloud" {
		return status, nil
	}

	var revision, delivered int64
	err = db.QueryRowContext(ctx, `SELECT revision, delivered_revision, attempts, COALESCE(last_error,''),
		COALESCE(last_success_at,0) FROM gt_cloud_sync_outbox WHERE task_uuid=? AND execution_index=?`,
		taskUUID, executionIndex).Scan(&revision, &delivered, &status.Attempts, &status.LastError, &status.LastSuccessAt)
	if err != nil && err != sql.ErrNoRows {
		return status, err
	}
	status.Revision, status.DeliveredRevision = revision, delivered

	// 一个任务每次执行都会单独产生一条 outbox 行（按 execution_index 区分）。
	// 只盯当前序号会漏掉历史序号里从未投递成功的执行记录，这正是「线上丢沟通」的来源。
	var pendingExecutions, failedExecutions int
	if err = db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN delivered_revision < revision THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN delivered_revision < revision AND attempts>0 AND last_error<>'' THEN 1 ELSE 0 END),0)
		FROM gt_cloud_sync_outbox WHERE task_uuid=?`,
		taskUUID).Scan(&pendingExecutions, &failedExecutions); err != nil {
		return status, err
	}
	status.PendingExecutions = pendingExecutions
	// 当前序号还没投递时，它的失败原因优先；否则取历史序号里最近的一条失败原因。
	if failedExecutions > 0 && (status.Attempts == 0 || status.LastError == "") {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(attempts,0), COALESCE(last_error,'')
			FROM gt_cloud_sync_outbox WHERE task_uuid=? AND delivered_revision < revision
			AND attempts>0 AND last_error<>'' ORDER BY attempts DESC, revision DESC LIMIT 1`,
			taskUUID).Scan(&status.Attempts, &status.LastError)
	}

	// 待上报会话数：失败算待上报；成功但水位落后于 updated_at 也算有新变更。
	// 运行中的会话内容还在变，不计入，否则整个执行期间状态会在待上报/已上报之间抖动。
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN s.cloud_sync_status<>'success'
			OR s.cloud_synced_at < s.updated_at THEN 1 ELSE 0 END),0)
		FROM gt_cli_sessions s WHERE s.task_uuid=?
		AND COALESCE(s.status,'') NOT IN ('running','created')
		AND EXISTS (SELECT 1 FROM gt_tasks t WHERE t.uuid=s.task_uuid AND t.execution_index=?)`,
		taskUUID, executionIndex).Scan(&status.PendingSessions); err != nil {
		return status, err
	}

	switch {
	case failedExecutions > 0 && status.LastError != "":
		status.Status = SyncStatusFailed
	case pendingExecutions > 0 || status.PendingSessions > 0:
		status.Status = SyncStatusPending
	case revision > 0 && delivered >= revision:
		status.Status = SyncStatusSuccess
	default:
		status.Status = SyncStatusPending
	}
	return status, nil
}
