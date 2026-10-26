package cloudsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

type syncQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type executionSnapshot struct {
	Task  cloud.PipelineTaskSync `json:"task"`
	Steps []stepSnapshot         `json:"steps"`
	// ProgressWatermark 是本快照覆盖到的 Vibe 沟通记录最大 rowid，投递成功 / 失败时
	// 据此把结果回写到每条记录自己的上报状态。老快照没有该字段时为 0，不做回写。
	ProgressWatermark int64 `json:"progress_watermark,omitempty"`
}

type stepSnapshot struct {
	Payload   cloud.PipelineTaskSessionSync `json:"payload"`
	StepUUID  string                        `json:"step_uuid"`
	Watermark int64                         `json:"watermark"`
}

func ownsTask(ctx context.Context, db syncQueryer, client *cloud.Client, taskUUID string) (bool, error) {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	var count int
	// cloud_user_id 为空的历史任务不构成归属冲突：本地库单账号独占，空值只说明当初
	// 没写进账号，应允许当前账号接管，否则这类任务永远进不了上报队列（上报黑洞）。
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_tasks
		WHERE uuid=? AND source_type='cloud' AND cloud_source_key IN (?, ?)
		AND (cloud_user_id=? OR COALESCE(cloud_user_id,'')='')`,
		taskUUID, primary, alternate, client.UserID()).Scan(&count)
	return count == 1, err
}

// adoptTaskOwner 把没有记录云端归属的历史任务接管给当前账号：任务行与已有 outbox 行
// 一并回填 cloud_user_id，之后定时轮询与手动重报才能匹配到它们。
// 只处理 cloud_source_key 与当前云端地址一致的任务，不会跨环境误接管。
func adoptTaskOwner(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	if db == nil || client == nil || client.UserID() == "" {
		return nil
	}
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	if _, err := db.ExecContext(ctx, `UPDATE gt_tasks SET cloud_user_id=?
		WHERE uuid=? AND COALESCE(cloud_user_id,'')='' AND cloud_source_key IN (?, ?)`,
		client.UserID(), taskUUID, primary, alternate); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET cloud_user_id=?
		WHERE task_uuid=? AND COALESCE(cloud_user_id,'')='' AND cloud_source_key IN (?, ?)`,
		client.UserID(), taskUUID, primary, alternate); err != nil {
		return err
	}
	return nil
}

func pendingTaskKeys(ctx context.Context, db *sql.DB, client *cloud.Client) ([]string, error) {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	rows, err := db.QueryContext(ctx, `SELECT uuid FROM gt_tasks WHERE source_type='cloud'
		AND cloud_source_key IN (?, ?) AND (cloud_user_id=? OR COALESCE(cloud_user_id,'')='')
		UNION SELECT task_uuid FROM gt_cloud_sync_outbox WHERE cloud_source_key IN (?, ?)
		AND (cloud_user_id=? OR COALESCE(cloud_user_id,'')='') AND delivered_revision < revision ORDER BY 1`,
		primary, alternate, client.UserID(), primary, alternate, client.UserID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// CaptureTaskExecution persists a consistent snapshot before doing any I/O to
// the cloud. The switch transaction calls the Tx variant before deleting rows.
func CaptureTaskExecution(ctx context.Context, db *sql.DB, taskUUID string) error {
	return captureTaskExecution(ctx, db, taskUUID, false)
}

// captureTaskExecution 在写入云端前持久化一份一致的快照；force 用于手动重报，
// 即使内容哈希未变也推进 revision，让上报动作真实发生。
func captureTaskExecution(ctx context.Context, db *sql.DB, taskUUID string, force bool) error {
	if db == nil {
		return fmt.Errorf("task database is not ready")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = captureTaskExecutionTx(ctx, tx, taskUUID, force); err != nil {
		return err
	}
	return tx.Commit()
}

func CaptureTaskExecutionTx(ctx context.Context, tx *sql.Tx, taskUUID string) error {
	return captureTaskExecutionTx(ctx, tx, taskUUID, false)
}

func captureTaskExecutionTx(ctx context.Context, tx *sql.Tx, taskUUID string, force bool) error {
	var source, sourceKey, userID, mode, tool string
	err := tx.QueryRowContext(ctx, `SELECT source_type, cloud_source_key, cloud_user_id,
		execution_mode, execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&source, &sourceKey, &userID, &mode, &tool)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if source != "cloud" {
		return nil
	}
	task, err := BuildTaskSync(ctx, tx, taskUUID, 0)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("任务投影缺少必要字段，无法生成上报快照")
	}
	snapshot := executionSnapshot{Task: *task, Steps: make([]stepSnapshot, 0)}
	// Timestamps used solely for transport versions are assigned after hashing.
	snapshot.Task.LocalRevision = 0
	rows, err := tx.QueryContext(ctx, `SELECT st.uuid FROM gt_task_steps st WHERE st.task_uuid=?
		AND EXISTS (SELECT 1 FROM gt_cli_sessions s WHERE s.task_uuid=st.task_uuid AND s.step_uuid=st.uuid)
		ORDER BY st.step_order, st.rowid`, taskUUID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		payload, watermark, err := buildStepSessionPayload(ctx, tx, taskUUID, id)
		if err != nil {
			return err
		}
		snapshot.Steps = append(snapshot.Steps, stepSnapshot{Payload: *payload, StepUUID: id, Watermark: watermark})
	}
	if mode == "vibe_coding" {
		payload, err := buildVibeSession(ctx, tx, taskUUID, tool, task.ExecutionIndex)
		if err != nil {
			return err
		}
		// 本次快照包含到哪几条沟通记录：投递后按它回写每条记录的上报状态。
		if snapshot.ProgressWatermark, err = progressWatermark(ctx, tx, taskUUID); err != nil {
			return err
		}
		// Vibe has no managed process to update task execution counters. Export
		// the Skill conversation's completed state and timeline with its body.
		if payload.Status == "running" || payload.Status == "success" {
			snapshot.Task.ExecutionStatus = payload.Status
		}
		snapshot.Task.Pipeline.CliType = tool
		snapshot.Task.Usage.ConversationRounds = payload.ConversationRounds
		snapshot.Task.StartedAt = unixMillisToRFC3339(payload.StartedAt)
		snapshot.Task.FinishedAt = unixMillisToRFC3339(payload.FinishedAt)
		snapshot.Steps = append(snapshot.Steps, stepSnapshot{Payload: *payload})
	}
	content, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	var previousHash string
	var previousRevision int64
	err = tx.QueryRowContext(ctx, `SELECT content_hash, revision FROM gt_cloud_sync_outbox
		WHERE task_uuid=? AND execution_index=?`, taskUUID, task.ExecutionIndex).Scan(&previousHash, &previousRevision)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if previousHash == hash && !force {
		return nil
	}
	revision := max(time.Now().UnixMilli(), previousRevision+1)
	snapshot.Task.LocalRevision = revision
	for i := range snapshot.Steps {
		snapshot.Steps[i].Payload.ClientUpdatedAt = revision
	}
	content, err = json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_cloud_sync_outbox
		(task_uuid, execution_index, cloud_source_key, cloud_user_id, revision, content_hash, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_uuid, execution_index) DO UPDATE SET
		revision=excluded.revision, content_hash=excluded.content_hash, payload_json=excluded.payload_json`,
		taskUUID, task.ExecutionIndex, sourceKey, userID, revision, hash, string(content))
	return err
}

func deliverTaskSnapshots(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	for count := 0; count < maxStepPushesPerTaskPerRound; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var raw string
		var index, attempts int
		var revision, nextAttempt int64
		err := db.QueryRowContext(ctx, `SELECT execution_index, revision, payload_json, attempts, next_attempt_at
			FROM gt_cloud_sync_outbox WHERE task_uuid=? AND cloud_source_key IN (?, ?) AND cloud_user_id=?
			AND delivered_revision < revision ORDER BY execution_index LIMIT 1`,
			taskUUID, primary, alternate, client.UserID()).Scan(&index, &revision, &raw, &attempts, &nextAttempt)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if nextAttempt > time.Now().UnixMilli() {
			return nil
		}
		var snapshot executionSnapshot
		if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return err
		}
		pushedSteps, err := pushSnapshot(ctx, client, snapshot)
		if err != nil {
			delay := min(300, 10<<min(attempts, 5))
			_, saveErr := db.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET attempts=attempts+1,
				next_attempt_at=?, last_error=? WHERE task_uuid=? AND execution_index=? AND revision=?`,
				time.Now().Add(time.Duration(delay)*time.Second).UnixMilli(), truncateRunes(err.Error(), syncErrorMaxRunes), taskUUID, index, revision)
			if saveErr != nil {
				return fmt.Errorf("upload failed: %v; save retry state: %w", err, saveErr)
			}
			// 会话级失败状态：本次没有推送成功的步骤全部标记 failed，
			// 界面据此显示「上报失败 + 原因」并提供重新上报。
			markSessionsSyncFailed(ctx, db, snapshot, pushedSteps, err)
			markProgressSyncFailed(ctx, db, snapshot, err)
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = acknowledgeSnapshot(ctx, tx, snapshot, revision); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return err
		}
		// 状态已回写到会话 / 沟通记录行：通知任务页重新拉取进度
		notifyTaskDelivered(taskUUID)
	}
	return nil
}

func pushSnapshot(ctx context.Context, client *cloud.Client, snapshot executionSnapshot) (int, error) {
	if snapshot.Task.WorkItem.ID <= 0 {
		return 0, fmt.Errorf("任务未关联云端工作项（work_item_id 为空），无法上报")
	}
	if snapshot.Task.WorkItem.WorkspaceID <= 0 || (snapshot.Task.WorkItem.Type != "requirement" && snapshot.Task.WorkItem.Type != "defect") {
		work, err := client.GetMyWork(ctx)
		if err != nil {
			return 0, err
		}
		workspace, kind, found := cloud.LookupMyWorkItem(work.List, fmt.Sprint(snapshot.Task.WorkItem.ID))
		if !found || workspace <= 0 {
			return 0, fmt.Errorf("云端未找到该工作项，任务绑定关系无法解析")
		}
		snapshot.Task.WorkItem.WorkspaceID, snapshot.Task.WorkItem.Type = workspace, kind
		content, err := json.Marshal(snapshot.Task)
		if err != nil {
			return 0, err
		}
		sum := sha256.Sum256(content)
		snapshot.Task.ProjectionHash = hex.EncodeToString(sum[:])
	}
	result, err := client.PushPipelineTaskSync(ctx, snapshot.Task)
	if err != nil {
		return 0, err
	}
	if result.Result != "accepted" && result.Result != "duplicate" {
		return 0, fmt.Errorf("云端拒绝接收任务投影（result=%s）", result.Result)
	}
	for i, step := range snapshot.Steps {
		if err = client.PushPipelineTaskSession(ctx, step.Payload); err != nil {
			return i, err
		}
	}
	return len(snapshot.Steps), nil
}

// markSessionsSyncFailed 把本次没有推送成功的步骤会话标记为上报失败。
// pushedSteps 之前的步骤已经在云端落库，不会被覆盖成失败。
func markSessionsSyncFailed(ctx context.Context, db *sql.DB, snapshot executionSnapshot, pushedSteps int, cause error) {
	if db == nil || len(snapshot.Steps) == 0 {
		return
	}
	reason := truncateRunes(cause.Error(), syncErrorMaxRunes)
	for i := max(pushedSteps, 0); i < len(snapshot.Steps); i++ {
		step := snapshot.Steps[i]
		if step.StepUUID == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, `UPDATE gt_cli_sessions SET cloud_sync_status='failed', cloud_sync_error=?
			WHERE task_uuid=? AND step_uuid=? AND updated_at<=? AND cloud_sync_status<>'failed'
			AND EXISTS (SELECT 1 FROM gt_tasks t WHERE t.uuid=gt_cli_sessions.task_uuid AND t.execution_index=?)`,
			reason, snapshot.Task.LocalTaskUUID, step.StepUUID, step.Watermark, snapshot.Task.ExecutionIndex); err != nil {
			applog.Warn("[CloudSync] 写入会话上报失败状态失败",
				"task_uuid", snapshot.Task.LocalTaskUUID, "step_uuid", step.StepUUID, "error", err.Error())
		}
	}
}

func acknowledgeSnapshot(ctx context.Context, tx *sql.Tx, snapshot executionSnapshot, revision int64) error {
	now := time.Now().UnixMilli()
	_, err := tx.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET delivered_revision=?, payload_json='',
		attempts=0, next_attempt_at=0, last_error='', last_success_at=? WHERE task_uuid=? AND execution_index=? AND revision=?`,
		revision, now, snapshot.Task.LocalTaskUUID, snapshot.Task.ExecutionIndex, revision)
	if err != nil {
		return err
	}
	for _, step := range snapshot.Steps {
		if step.StepUUID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `UPDATE gt_cli_sessions SET cloud_sync_status='success', cloud_sync_error='',
			cloud_synced_at=updated_at, cloud_sync_at=? WHERE task_uuid=? AND step_uuid=? AND updated_at<=?
			AND EXISTS (SELECT 1 FROM gt_tasks t WHERE t.uuid=gt_cli_sessions.task_uuid AND t.execution_index=?)`,
			now, snapshot.Task.LocalTaskUUID, step.StepUUID, step.Watermark, snapshot.Task.ExecutionIndex)
		if err != nil {
			return err
		}
	}
	if snapshot.ProgressWatermark > 0 {
		if err = markProgressSynced(ctx, tx, snapshot, now); err != nil {
			return err
		}
	}
	return nil
}
