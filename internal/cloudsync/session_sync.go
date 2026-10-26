package cloudsync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
)

// SessionSyncService captures and retries task projections and complete step
// conversations, scoped to the active cloud account. It never uploads terminal
// contents from external Vibe tools.
type SessionSyncService struct {
	db       *sql.DB
	client   func() *cloud.Client
	interval time.Duration
}

// maxStepPushesPerTaskPerRound bounds how many step sessions one task may push in a
// single round, protecting the loop from pathological backlogs.
const maxStepPushesPerTaskPerRound = 200

// syncErrorMaxRunes bounds the failure reason written back to the session rows.
const syncErrorMaxRunes = 256

// NewSessionSyncService creates the session sync service. The client getter returns
// nil while no cloud account session is logged in, in which case rounds are skipped.
func NewSessionSyncService(db *sql.DB, client func() *cloud.Client) *SessionSyncService {
	return &SessionSyncService{db: db, client: client, interval: 10 * time.Second}
}

// Start launches the periodic sync loop; it stops when ctx is canceled.
func (s *SessionSyncService) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			s.syncRound(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// syncRound also visits tasks without CLI sessions: Vibe reports and task status
// changes need the same durable retry path as direct execution.
func (s *SessionSyncService) syncRound(ctx context.Context) {
	if s.db == nil || s.client == nil {
		return
	}
	client := s.client()
	if client == nil || client.UserID() == "" {
		return
	}
	keys, err := pendingTaskKeys(ctx, s.db, client)
	if err != nil {
		applog.Warn("[CloudSync] read pending tasks failed", "error", err)
		return
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return
		}
		if err := defaultPusher.PushTaskSync(ctx, s.db, client, key); err != nil {
			applog.Warn("[CloudSync] sync task failed", "task_uuid", key, "error", err)
		}
	}
}

// buildStepSessionPayload aggregates one step's latest run metadata with its full
// conversation body into the step-level session sync payload.
func buildStepSessionPayload(ctx context.Context, db syncQueryer, taskUUID, stepUUID string) (*cloud.PipelineTaskSessionSync, int64, error) {
	var stepKey, stepName string
	var executionIndex int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(step_key,''), COALESCE(name,'') FROM gt_task_steps WHERE uuid = ? AND task_uuid = ?`,
		stepUUID, taskUUID).Scan(&stepKey, &stepName)
	if err != nil {
		return nil, 0, fmt.Errorf("读取步骤信息失败: %w", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(execution_index,0) FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&executionIndex); err != nil {
		return nil, 0, fmt.Errorf("读取执行序号失败: %w", err)
	}

	// Latest run row: status/model/prompt/result/error of the most recently updated run.
	var status, cliType, modelName, promptSnapshot, resultStatus, finalResult, errorCode, errorMessage string
	var exitCode int
	runErr := db.QueryRowContext(ctx, `
		SELECT COALESCE(s.status,''), COALESCE(s.cli_type,''), COALESCE(s.model_name,''),
		       COALESCE(s.prompt_snapshot,''), COALESCE(s.result_status,''), COALESCE(s.final_result,''),
		       COALESCE(s.exit_code,0), COALESCE(s.error_code,''), COALESCE(s.error_message,'')
		 FROM gt_cli_sessions s
		 WHERE s.task_uuid = ? AND s.step_uuid = ?
		 ORDER BY s.updated_at DESC, s.run_no DESC, s.rowid DESC
		 LIMIT 1`, taskUUID, stepUUID).Scan(
		&status, &cliType, &modelName, &promptSnapshot, &resultStatus, &finalResult,
		&exitCode, &errorCode, &errorMessage)
	if runErr != nil && runErr != sql.ErrNoRows {
		return nil, 0, fmt.Errorf("读取最近运行信息失败: %w", runErr)
	}

	// Whole-step rollups and the sync watermark (max session updated_at).
	var startedAt, finishedAt, durationMs, maxUpdatedAt, minCreatedAt int64
	var toolCallCount, filesChanged, linesAdded, linesDeleted int
	aggErr := db.QueryRowContext(ctx, `
		SELECT COALESCE(MIN(NULLIF(s.started_at,0)),0), COALESCE(MAX(COALESCE(s.finished_at,0)),0),
		       COALESCE(SUM(COALESCE(s.duration_ms,0)),0), COALESCE(MAX(s.updated_at),0),
		       COALESCE(MIN(s.created_at),0),
		       COALESCE(SUM(COALESCE(s.tool_call_count,0)),0), COALESCE(SUM(COALESCE(s.files_changed,0)),0),
		       COALESCE(SUM(COALESCE(s.lines_added,0)),0), COALESCE(SUM(COALESCE(s.lines_deleted,0)),0)
		 FROM gt_cli_sessions s
		 WHERE s.task_uuid = ? AND s.step_uuid = ?`, taskUUID, stepUUID).Scan(
		&startedAt, &finishedAt, &durationMs, &maxUpdatedAt, &minCreatedAt,
		&toolCallCount, &filesChanged, &linesAdded, &linesDeleted)
	if aggErr != nil {
		return nil, 0, fmt.Errorf("读取步骤会话汇总失败: %w", aggErr)
	}

	conv, err := buildStepConversation(ctx, db, taskUUID, stepUUID)
	if err != nil {
		return nil, 0, err
	}
	payload := &cloud.PipelineTaskSessionSync{
		LocalTaskUUID:      taskUUID,
		ExecutionIndex:     executionIndex,
		StepKey:            stepKey,
		StepName:           stepName,
		Status:             status,
		CliType:            cliType,
		ModelName:          modelName,
		PromptSnapshot:     promptSnapshot,
		ResultStatus:       resultStatus,
		FinalResult:        finalResult,
		ExitCode:           exitCode,
		ErrorCode:          errorCode,
		ErrorMessage:       errorMessage,
		InputTokens:        conv.inputTokens,
		OutputTokens:       conv.outputTokens,
		TotalTokens:        conv.totalTokens,
		ConversationRounds: conv.rounds,
		Conversation:       conv.content,
		ToolCallCount:      toolCallCount,
		FilesChanged:       filesChanged,
		LinesAdded:         linesAdded,
		LinesDeleted:       linesDeleted,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		DurationMs:         durationMs,
		CreatedAt:          minCreatedAt,
		ClientUpdatedAt:    maxUpdatedAt,
	}
	fitSessionSyncPayload(payload)
	return payload, maxUpdatedAt, nil
}

// 这些上限与云端校验、pipeline_task_sessions 列长一致。超限会让整次会话推送返回 400，
// 执行过程和执行结果都到不了 Agent 页。按字节截断，并保持 UTF-8 完整。
const (
	cloudPromptSnapshotMaxBytes   = 128 * 1024
	cloudFinalResultMaxBytes      = 64 * 1024
	cloudErrorMessageMaxBytes     = 64 * 1024
	cloudMessageMaxBytes          = 256 * 1024
	cloudSessionStepNameMaxBytes  = 128
	cloudSessionStepKeyMaxBytes   = 64
	cloudSessionCLITypeMaxBytes   = 32
	cloudSessionModelMaxBytes     = 64
	cloudSessionStatusMaxBytes    = 32
	cloudSessionErrorCodeMaxBytes = 64
)

func fitSessionSyncPayload(payload *cloud.PipelineTaskSessionSync) {
	if payload == nil {
		return
	}
	payload.StepKey = truncateUTF8Bytes(payload.StepKey, cloudSessionStepKeyMaxBytes)
	payload.StepName = truncateUTF8Bytes(payload.StepName, cloudSessionStepNameMaxBytes)
	payload.Status = truncateUTF8Bytes(payload.Status, cloudSessionStatusMaxBytes)
	payload.CliType = truncateUTF8Bytes(payload.CliType, cloudSessionCLITypeMaxBytes)
	payload.ModelName = truncateUTF8Bytes(payload.ModelName, cloudSessionModelMaxBytes)
	payload.ResultStatus = truncateUTF8Bytes(payload.ResultStatus, cloudSessionStatusMaxBytes)
	payload.ErrorCode = truncateUTF8Bytes(payload.ErrorCode, cloudSessionErrorCodeMaxBytes)
	payload.PromptSnapshot = truncateUTF8Bytes(payload.PromptSnapshot, cloudPromptSnapshotMaxBytes)
	payload.FinalResult = truncateUTF8Bytes(payload.FinalResult, cloudFinalResultMaxBytes)
	payload.ErrorMessage = truncateUTF8Bytes(payload.ErrorMessage, cloudErrorMessageMaxBytes)
	for i := range payload.Conversation.Rounds {
		round := &payload.Conversation.Rounds[i]
		round.UserMessage = truncateUTF8Bytes(round.UserMessage, cloudMessageMaxBytes)
		for j := range round.AssistantMessages {
			round.AssistantMessages[j].Content = truncateUTF8Bytes(round.AssistantMessages[j].Content, cloudMessageMaxBytes)
		}
	}
}

// FlushTaskExecution captures before attempting network delivery. Offline or
// failed uploads remain in SQLite and are retried by the periodic service.
func FlushTaskExecution(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	if err := CaptureTaskExecution(ctx, db, taskUUID); err != nil {
		return err
	}
	// 任务完成收尾时先把产出目录扫一遍再触发上报：最后一批产出文件不能依赖
	// 下一次任务上报才被发现。扫描失败只记日志，不影响执行记录的上报。
	if _, err := ScanTaskDocuments(ctx, db, taskUUID); err != nil {
		applog.Warn("[DocSync] 任务完成前扫描产出文档失败", "task_uuid", taskUUID, "error", err.Error())
	}
	DefaultDocumentSyncService().Kick(taskUUID)
	if client == nil {
		return nil
	}
	return defaultPusher.PushTaskSync(ctx, db, client, taskUUID)
}

// truncateRunes shortens a string to at most max runes.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
