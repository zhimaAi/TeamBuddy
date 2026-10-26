package workflow

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrTaskBusy            = errors.New("task has an active session")
	ErrStepRuntimeRequired = errors.New("step requires CLI and model")
	ErrPipelineTaskDone    = errors.New("reopen task before starting an unstarted step")
	ErrRequestConflict     = errors.New("request ID was already used for another message")
)

func sessionUUIDForRequest(taskUUID, requestID string) string {
	if strings.TrimSpace(requestID) == "" {
		return uuid.NewString()
	}
	// 复用会话主键实现跨重启幂等，不额外存储消息正文或请求令牌。
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(taskUUID+":"+requestID)).String()
}

func (o *Orchestrator) existingRequest(ctx context.Context, taskUUID, stepUUID, requestID, displayPrompt, cliType, model string) (string, error) {
	if strings.TrimSpace(requestID) == "" {
		return "", nil
	}
	cliType = strings.TrimSpace(cliType)
	model = strings.TrimSpace(model)
	id := sessionUUIDForRequest(taskUUID, requestID)
	var storedStep, storedPrompt, storedCLI, storedModel string
	err := o.db.QueryRowContext(ctx, `SELECT step_uuid, cli_type, model_name,
		COALESCE((SELECT user_prompt FROM gt_task_progress WHERE session_uuid=s.uuid AND record_type='user_question' LIMIT 1),'')
		FROM gt_cli_sessions s WHERE uuid=? AND task_uuid=?`, id, taskUUID).Scan(&storedStep, &storedCLI, &storedModel, &storedPrompt)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if storedStep != stepUUID || strings.TrimSpace(storedPrompt) != strings.TrimSpace(displayPrompt) ||
		(cliType != "" && cliType != storedCLI) || (model != "" && model != storedModel) {
		return "", ErrRequestConflict
	}
	return id, nil
}

func initializePipelineProgress(ctx context.Context, tx *sql.Tx, taskUUID string, now int64) error {
	var current, first string
	if err := tx.QueryRowContext(ctx, `SELECT current_step_uuid FROM gt_tasks WHERE uuid=? AND execution_mode='pipeline'`, taskUUID).Scan(&current); err != nil {
		return err
	}
	if current != "" {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT uuid FROM gt_task_steps WHERE task_uuid=? ORDER BY step_order,rowid LIMIT 1`, taskUUID).Scan(&first); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gt_task_steps SET status='active',updated_at=? WHERE task_uuid=? AND uuid=? AND status='pending'`, now, taskUUID, first); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET current_step_uuid=?,current_step_completed=0 WHERE uuid=? AND current_step_uuid=''`, first, taskUUID)
	return err
}
