package localserver

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"goteams-client/internal/cloudsync"
	"goteams-client/internal/taskruntime"
)

// deleteTaskRows 在调用方事务内清理任务关联记录，供单任务和工作空间删除复用。
func deleteTaskRows(ctx context.Context, tx *sql.Tx, taskUUID string) ([]string, error) {
	if err := cloudsync.CaptureTaskExecutionTx(ctx, tx, taskUUID); err != nil {
		return nil, err
	}
	sessionUUIDs, err := taskruntime.DeleteTaskSessionRowsTx(ctx, tx, taskUUID)
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`DELETE FROM gt_task_project_links WHERE task_uuid = ?`,
		`DELETE FROM gt_task_notifications WHERE task_uuid = ?`,
		`DELETE FROM gt_task_execution_history WHERE task_uuid = ?`,
		`DELETE FROM gt_cli_sessions WHERE task_uuid = ?`,
		`DELETE FROM gt_task_progress WHERE task_uuid = ?`,
		`DELETE FROM gt_task_steps WHERE task_uuid = ?`,
		`DELETE FROM gt_task_work_dirs WHERE task_uuid = ?`,
		`DELETE FROM gt_task_pipeline_snapshots WHERE task_uuid = ?`,
		`DELETE FROM gt_task_expert_group_snapshots WHERE task_uuid = ?`,
		// 产出文档上报状态随任务级联清理；线上文档由云端任务级清理负责
		`DELETE FROM gt_task_document_uploads WHERE task_uuid = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, taskUUID); err != nil {
			return nil, err
		}
	}
	return sessionUUIDs, nil
}

// cleanupTaskDirectory 仅允许清理受管任务专属目录，不触碰项目工作目录。
func (h *TasksHandler) cleanupTaskDirectory(taskDir string) bool {
	root, rootErr := filepath.EvalSymlinks(h.taskRoot)
	target, targetErr := filepath.Abs(taskDir)
	if strings.TrimSpace(taskDir) == "" || rootErr != nil || targetErr != nil {
		return false
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return true
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return os.RemoveAll(target) == nil
}
