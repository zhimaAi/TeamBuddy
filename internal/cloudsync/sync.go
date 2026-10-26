package cloudsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

// Pusher serializes capture and delivery for each task. Snapshots, revisions and
// retry state live in SQLite; restarting the app never loses a pending upload.
type Pusher struct{ locks [32]chan struct{} }

func NewPusher() *Pusher {
	p := &Pusher{}
	for i := range p.locks {
		p.locks[i] = make(chan struct{}, 1)
	}
	return p
}

var defaultPusher = NewPusher()

func DefaultPusher() *Pusher { return defaultPusher }

func (p *Pusher) PushTaskSync(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	return p.pushTaskSync(ctx, db, client, taskUUID, false)
}

// RetryTaskSync 手动重报一个任务：先清掉退避等待，再强制推进 revision 推一次。
// 不清退避时，失败后的 next_attempt_at 会让手动点击没有任何效果；不强制推进
// revision 时，内容哈希未变会被 CaptureTaskExecution 直接短路，同样没有上报动作。
func (p *Pusher) RetryTaskSync(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	if db == nil || client == nil || client.UserID() == "" {
		return fmt.Errorf("云端账号未就绪，无法上报")
	}
	// 先接管空归属的历史任务，再判断归属：否则这类任务会被直接拒绝，连重报都做不到。
	if err := adoptTaskOwner(ctx, db, client, taskUUID); err != nil {
		return err
	}
	owned, err := ownsTask(ctx, db, client, taskUUID)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("该任务不属于当前登录的云端账号（cloud_source_key 或 cloud_user_id 与云端地址不匹配），无法上报")
	}
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	if _, err = db.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET next_attempt_at=0
		WHERE task_uuid=? AND cloud_source_key IN (?, ?) AND cloud_user_id=?`,
		taskUUID, primary, alternate, client.UserID()); err != nil {
		return err
	}
	return p.pushTaskSync(ctx, db, client, taskUUID, true)
}

func (p *Pusher) pushTaskSync(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string, force bool) error {
	if db == nil || client == nil || client.UserID() == "" {
		return fmt.Errorf("cloud account is not ready")
	}
	sum := sha256.Sum256([]byte(taskUUID))
	lock := p.locks[int(sum[0])%len(p.locks)]
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock }()
	if err := adoptTaskOwner(ctx, db, client, taskUUID); err != nil {
		return err
	}
	owned, err := ownsTask(ctx, db, client, taskUUID)
	if err != nil {
		return err
	}
	if owned {
		if err = captureTaskExecution(ctx, db, taskUUID, force); err != nil {
			return err
		}
	}
	return deliverTaskSnapshots(ctx, db, client, taskUUID)
}

// Local deletion does not remove already queued team audit records.
func (p *Pusher) ForgetTask(taskUUID string) {}

// BuildTaskSync aggregates the task's incremental sync payload from the local database.
// Returns nil when the task is not a cloud task (or is already gone).
func BuildTaskSync(ctx context.Context, db syncQueryer, taskUUID string, sinceCreatedAt int64) (*cloud.PipelineTaskSync, error) {
	base, err := loadTaskBase(ctx, db, taskUUID)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, nil
	}
	// Preserve incomplete legacy metadata in the local outbox as well. Delivery
	// can repair the work-item link when the matching account is online again.
	steps, err := loadTaskSteps(ctx, db, taskUUID)
	if err != nil {
		return nil, err
	}
	payload := cloud.PipelineTaskSync{
		LocalTaskUUID:      taskUUID,
		ExecutionIndex:     base.executionIndex,
		LocalRevision:      time.Now().UnixMilli(),
		ProjectionHash:     projectionHash(base, steps),
		WorkItem:           base.workItem,
		Pipeline:           base.pipeline,
		TaskStatus:         base.taskStatus,
		ExecutionStatus:    base.executionStatus,
		CurrentStepKey:     base.currentStepKey,
		ExecutionSummary:   base.executionSummary,
		StartedAt:          base.startedAt,
		FinishedAt:         base.finishedAt,
		LastLocalUpdatedAt: base.lastLocalUpdatedAt,
		Usage:              base.usage,
		Steps:              steps,
		DeletedStepKeys:    []string{},
	}
	return &payload, nil
}

// ======================== Task base aggregation ========================

type taskBase struct {
	workItem           cloud.PipelineTaskWorkItem
	pipeline           cloud.PipelineTaskPipeline
	taskStatus         string
	executionStatus    string
	currentStepKey     string
	executionSummary   string
	startedAt          *string
	finishedAt         *string
	lastLocalUpdatedAt string
	usage              cloud.PipelineTaskUsage
	executionIndex     int
}

func loadTaskBase(ctx context.Context, db syncQueryer, taskUUID string) (*taskBase, error) {
	var sourceType, workItemType, workItemID, workspaceID, title string
	var taskStatus, execStatus, currentStepUUID, cliType, executionMode string
	var pipelineID, pipelineName, expertSourceUUID, expertName, executionTool string
	var inputTokens, outputTokens, totalTokens, conversationRounds, startedAt, finishedAt, updatedAt int64
	var executionIndex int
	err := db.QueryRowContext(ctx, `
		SELECT t.source_type, COALESCE(NULLIF(t.work_item_type,''), t.cloud_work_item_type, ''),
		       COALESCE(NULLIF(t.work_item_id,''), t.cloud_work_item_id, ''),
		       COALESCE(t.workspace_id, 0), t.title, t.status, t.execution_status,
		       t.current_step_uuid, COALESCE(t.cli_type,''), t.execution_mode, COALESCE(s.source_pipeline_id,''),
		       COALESCE(s.name,''), COALESCE(egs.source_expert_group_uuid,''), COALESCE(egs.name,''),
		       COALESCE(t.execution_tool,''),
		       COALESCE(t.input_tokens,0), COALESCE(t.output_tokens,0), COALESCE(t.total_tokens,0),
		       COALESCE(t.conversation_rounds,0), COALESCE(t.started_at,0), COALESCE(t.finished_at,0), t.updated_at,
		       COALESCE(t.execution_index,0)
		 FROM gt_tasks t LEFT JOIN gt_task_pipeline_snapshots s ON s.uuid = t.pipeline_snapshot_uuid
		 LEFT JOIN gt_task_expert_group_snapshots egs ON egs.uuid=t.expert_group_snapshot_uuid
		 WHERE t.uuid = ?`, taskUUID).Scan(
		&sourceType, &workItemType, &workItemID, &workspaceID, &title, &taskStatus, &execStatus,
		&currentStepUUID, &cliType, &executionMode, &pipelineID, &pipelineName, &expertSourceUUID, &expertName, &executionTool,
		&inputTokens, &outputTokens, &totalTokens, &conversationRounds, &startedAt, &finishedAt, &updatedAt, &executionIndex)
	if err == sql.ErrNoRows {
		applog.Warn("[CloudSync] 任务投影跳过推送：本地任务不存在（可能已被删除）", "task_uuid", taskUUID)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取任务主信息失败: %w", err)
	}
	if sourceType != "cloud" {
		// Only cloud-sourced tasks are mirrored to the cloud.
		applog.Info("[CloudSync] 任务投影跳过推送：仅 cloud 来源任务才镜像到云端", "task_uuid", taskUUID, "source_type", sourceType)
		return nil, nil
	}
	title = clipRunes(taskUUID, "title", title, cloudTitleMaxRunes)
	workItemIDInt, _ := strconv.ParseInt(workItemID, 10, 64)
	workspaceIDInt, _ := strconv.ParseInt(workspaceID, 10, 64)
	pipelineIDInt, _ := strconv.ParseInt(pipelineID, 10, 64)
	if executionMode == "expert_group" && expertSourceUUID != "" {
		sum := sha256.Sum256([]byte("expert-group:" + expertSourceUUID))
		// Postgres pipeline_tasks.pipeline_id is an INTEGER (signed 32-bit: 1..2147483647);
		// a 64-bit mask causes "integer out of range" which the cloud surfaces as 400.
		pseudoID := int64(binary.BigEndian.Uint32(sum[:4]) & 0x7fffffff)
		if pseudoID <= 0 {
			pseudoID = 1
		}
		pipelineIDInt = pseudoID
		pipelineName = expertName
		_ = db.QueryRowContext(ctx, `SELECT cli_type FROM gt_task_steps WHERE task_uuid=? AND member_role='leader' LIMIT 1`, taskUUID).Scan(&cliType)
	}
	// Cloud tasks executed without a pipeline snapshot (CLI direct / Vibe Coding) still
	// need a cloud projection: synthesize a stable pseudo pipeline id per task, the same
	// trick expert groups use, so the cloud's pipeline_id>0 check passes and the task log
	// shows a readable execution-mode label instead of silently skipping the push.
	if pipelineIDInt <= 0 {
		namespace := "ad-hoc"
		switch executionMode {
		case "cli":
			namespace = "cli-direct"
			if cliType == "" {
				cliType = executionTool
			}
			if pipelineName == "" {
				pipelineName = "CLI"
				if cliType != "" {
					pipelineName = "CLI · " + cliType
				}
			}
		case "vibe_coding":
			namespace = "vibe-coding"
			if pipelineName == "" {
				pipelineName = "Vibe Coding"
			}
		default:
			if pipelineName == "" {
				pipelineName = "Direct"
			}
		}
		sum := sha256.Sum256([]byte(namespace + ":" + taskUUID))
		pseudoID := int64(binary.BigEndian.Uint32(sum[:4]) & 0x7fffffff)
		if pseudoID <= 0 {
			pseudoID = 1
		}
		pipelineIDInt = pseudoID
	}
	pipelineName = clipRunes(taskUUID, "pipeline_name", pipelineName, cloudPipelineNameMaxRunes)
	cliType = clipRunes(taskUUID, "cli_type", cliType, cloudCLITypeMaxRunes)
	currentStepKey := ""
	if currentStepUUID != "" {
		_ = db.QueryRowContext(ctx,
			`SELECT step_key FROM gt_task_steps WHERE task_uuid = ? AND uuid = ?`, taskUUID, currentStepUUID).Scan(&currentStepKey)
	}
	currentStepKey = clipRunes(taskUUID, "current_step_key", currentStepKey, cloudStepKeyMaxRunes)
	taskStatus = clipRunes(taskUUID, "task_status", taskStatus, cloudStatusMaxRunes)
	execStatus = clipRunes(taskUUID, "execution_status", execStatus, cloudStatusMaxRunes)
	base := &taskBase{
		workItem: cloud.PipelineTaskWorkItem{
			Type:        workItemType,
			ID:          workItemIDInt,
			WorkspaceID: workspaceIDInt,
			Title:       title,
		},
		pipeline: cloud.PipelineTaskPipeline{
			ID:              pipelineIDInt,
			Name:            pipelineName,
			Color:           "", // 本地快照无颜色字段；云端 pipeline_color_snapshot 为 VARCHAR(20)，不可传头像 URL 以免超长被拒
			CliType:         cliType,
			WorkflowVersion: 1,
		},
		taskStatus:         taskStatus,
		executionStatus:    execStatus,
		currentStepKey:     currentStepKey,
		executionSummary:   "",
		startedAt:          unixMillisToRFC3339(startedAt),
		finishedAt:         unixMillisToRFC3339(finishedAt),
		lastLocalUpdatedAt: time.UnixMilli(updatedAt).UTC().Format(time.RFC3339Nano),
		usage: cloud.PipelineTaskUsage{
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			TotalTokens:        totalTokens,
			ConversationRounds: int(conversationRounds),
		},
		executionIndex: executionIndex,
	}
	return base, nil
}

// ======================== Steps aggregation ========================

func loadTaskSteps(ctx context.Context, db syncQueryer, taskUUID string) ([]cloud.PipelineTaskStep, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT step_key, name, status, execution_status, prompt_snapshot, output_summary, error_summary,
		       input_tokens, output_tokens, total_tokens, conversation_rounds,
		       started_at, finished_at, duration_ms, step_order
		 FROM gt_task_steps WHERE task_uuid = ? ORDER BY step_order`, taskUUID)
	if err != nil {
		return nil, fmt.Errorf("查询任务步骤失败: %w", err)
	}
	defer rows.Close()
	steps := make([]cloud.PipelineTaskStep, 0)
	for rows.Next() {
		var stepKey, name, status, execStatus, promptSnapshot, outputSummary, errorSummary string
		var inputTokens, outputTokens, totalTokens, conversationRounds, startedAt, finishedAt, durationMs, sortOrder int64
		if err := rows.Scan(&stepKey, &name, &status, &execStatus, &promptSnapshot, &outputSummary, &errorSummary,
			&inputTokens, &outputTokens, &totalTokens, &conversationRounds,
			&startedAt, &finishedAt, &durationMs, &sortOrder); err != nil {
			return nil, fmt.Errorf("读取任务步骤失败: %w", err)
		}
		stepKey = clipRunes(taskUUID, "step_key", stepKey, cloudStepKeyMaxRunes)
		name = clipRunes(taskUUID, "step_name", name, cloudStepNameMaxRunes)
		steps = append(steps, cloud.PipelineTaskStep{
			StepKey:       stepKey,
			StepName:      name,
			SortOrder:     int(sortOrder),
			Status:        clipRunes(taskUUID, "step_status", cloudStepStatus(status, execStatus), cloudStatusMaxRunes),
			PromptSummary: truncateRunes(strings.TrimSpace(promptSnapshot), 512),
			OutputSummary: outputSummary,
			ErrorSummary:  errorSummary,
			Usage: cloud.PipelineTaskUsage{
				InputTokens:        inputTokens,
				OutputTokens:       outputTokens,
				TotalTokens:        totalTokens,
				ConversationRounds: int(conversationRounds),
			},
			StartedAt:  unixMillisToRFC3339(startedAt),
			FinishedAt: unixMillisToRFC3339(finishedAt),
			DurationMs: durationMs,
		})
	}
	return steps, rows.Err()
}

// cloudStepStatus maps the local step lifecycle to the cloud step status vocabulary
// (pending/running/success/failed/stopped/idle).
func cloudStepStatus(status, execStatus string) string {
	// 用户已确认完成的步骤按业务完成态上报；失败、停止等原始执行结果仍保留在会话中。
	if status == "completed" {
		return "success"
	}
	if execStatus != "" && execStatus != "idle" {
		return execStatus
	}
	switch status {
	case "active":
		return "running"
	case "pending":
		return "pending"
	default:
		return "idle"
	}
}

// ======================== Step conversation aggregation ========================

type roundRow struct {
	sessionUUID      string
	roundNo          int
	userMessage      string
	assistantMessage string
	status           string
	cliType          string
	errorSummary     string
	inputTokens      int64
	outputTokens     int64
	totalTokens      int64
	durationMs       int64
	createdAt        int64
	startedAt        int64
	finishedAt       int64
}

// stepConversationResult is the aggregated full conversation body of one step plus
// its usage/time rollups, carried by the step-level session sync payload.
type stepConversationResult struct {
	content      cloud.PipelineTaskConversationContent
	rounds       int
	inputTokens  int64
	outputTokens int64
	totalTokens  int64
	durationMs   int64
	startedAt    int64
	finishedAt   int64
}

// buildStepConversation loads every conversation round of one step (ordered by
// created_at) and aggregates them into the FULL step conversation body. Rounds are
// renumbered to be contiguous 1..N: the cloud upserts the conversation as a whole,
// so a truncated round list would overwrite earlier rounds and follow-up questions
// would never appear on the cloud.
func buildStepConversation(ctx context.Context, db syncQueryer, taskUUID, stepUUID string) (*stepConversationResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.uuid, COALESCE(cs.round_no, s.run_no),
		       COALESCE((
		         SELECT p.user_prompt FROM gt_task_progress p
		         WHERE p.session_uuid = s.uuid AND p.record_type = 'user_question'
		         ORDER BY p.created_at, p.rowid LIMIT 1
		       ), ''),
		       COALESCE(NULLIF(trim((
		         SELECT p.final_result FROM gt_task_progress p
		         WHERE p.session_uuid = s.uuid AND p.record_type <> 'user_question'
		         ORDER BY p.finished_at DESC, p.rowid DESC LIMIT 1
		       )), ''), NULLIF(trim(s.final_result), ''), ''),
		       COALESCE(cs.response_summary, ''),
		       COALESCE(s.input_tokens,0), COALESCE(s.output_tokens,0), COALESCE(s.total_tokens,0),
		       COALESCE(cs.token_input,0), COALESCE(cs.token_output,0), COALESCE(cs.token_total,0),
		       COALESCE(s.duration_ms,0), COALESCE(cs.duration_ms,0), s.created_at,
		       COALESCE(s.status,''), COALESCE(s.cli_type,''), COALESCE(s.error_message,''),
		       COALESCE(s.started_at,0), COALESCE(s.finished_at,0)
		 FROM gt_cli_sessions s
		 LEFT JOIN gt_conversation_summaries cs ON cs.session_uuid = s.uuid
		 WHERE s.task_uuid = ? AND s.step_uuid = ?
		 ORDER BY s.created_at, s.run_no, s.rowid`, taskUUID, stepUUID)
	if err != nil {
		return nil, fmt.Errorf("查询步骤会话摘要失败: %w", err)
	}
	defer rows.Close()
	rounds := make([]roundRow, 0)
	for rows.Next() {
		var r roundRow
		var userPrompt, finalResult, summaryResponse string
		var sessionInput, sessionOutput, sessionTotal, summaryInput, summaryOutput, summaryTotal int64
		var sessionDuration, summaryDuration int64
		if err := rows.Scan(&r.sessionUUID, &r.roundNo, &userPrompt, &finalResult, &summaryResponse,
			&sessionInput, &sessionOutput, &sessionTotal, &summaryInput, &summaryOutput, &summaryTotal,
			&sessionDuration, &summaryDuration, &r.createdAt,
			&r.status, &r.cliType, &r.errorSummary, &r.startedAt, &r.finishedAt); err != nil {
			return nil, fmt.Errorf("读取步骤会话摘要失败: %w", err)
		}
		// 用户气泡只上传客户端展示的 user_prompt。prompt_snapshot 是发给 CLI 的内部提示词，
		// 客户端对话里不显示，不能再当成用户消息。
		r.userMessage = strings.TrimSpace(userPrompt)
		r.assistantMessage = strings.TrimSpace(finalResult)
		if r.assistantMessage == "" {
			r.assistantMessage = strings.TrimSpace(summaryResponse)
		}
		r.inputTokens = preferPositive(sessionInput, summaryInput)
		r.outputTokens = preferPositive(sessionOutput, summaryOutput)
		r.totalTokens = preferPositive(sessionTotal, summaryTotal)
		r.durationMs = preferPositive(sessionDuration, summaryDuration)
		rounds = append(rounds, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	eventsBySession, truncated, err := loadStepProcessEvents(ctx, db, taskUUID, stepUUID)
	if err != nil {
		return nil, err
	}
	if truncated {
		applog.Warn("[CloudSync] 执行过程过长，已截断后上传", "task_uuid", taskUUID, "step_uuid", stepUUID)
	}
	result := &stepConversationResult{
		content: cloud.PipelineTaskConversationContent{
			Rounds: make([]cloud.PipelineTaskRound, 0, len(rounds)),
		},
	}
	for index, r := range rounds {
		assistant := strings.TrimSpace(r.assistantMessage)
		messages := make([]cloud.PipelineTaskMessage, 0, 1)
		if assistant != "" {
			messages = append(messages, cloud.PipelineTaskMessage{Sequence: 1, Role: "assistant", Content: assistant})
		}
		result.content.Rounds = append(result.content.Rounds, cloud.PipelineTaskRound{
			RoundNo:           index + 1, // renumbered to be contiguous
			SessionUUID:       r.sessionUUID,
			Status:            r.status,
			UserMessage:       r.userMessage,
			AssistantMessages: messages,
			Events:            eventsBySession[r.sessionUUID],
			ErrorSummary:      r.errorSummary,
			Usage: cloud.PipelineTaskUsage{
				InputTokens:        r.inputTokens,
				OutputTokens:       r.outputTokens,
				TotalTokens:        r.totalTokens,
				ConversationRounds: 1,
			},
			StartedAt:  unixMillisToRFC3339(r.startedAt),
			FinishedAt: unixMillisToRFC3339(r.finishedAt),
			DurationMs: r.durationMs,
		})
		result.inputTokens += r.inputTokens
		result.outputTokens += r.outputTokens
		result.totalTokens += r.totalTokens
		result.durationMs += r.durationMs
		result.startedAt = minNonZero(result.startedAt, r.startedAt)
		if r.finishedAt > result.finishedAt {
			result.finishedAt = r.finishedAt
		}
	}
	result.content.RoundCount = len(result.content.Rounds)
	result.rounds = len(result.content.Rounds)
	return result, nil
}

// Keep these limits identical to the cloud session validator. They sit above the
// text the client itself keeps (final reply is 64K runes; process events are stored
// in full) so a normal run uploads unchanged. Only a pathological tool dump is cut.
const (
	maxProcessEventsPerRound      = 5000
	maxProcessEventContentBytes   = 8 * 1024 * 1024
	maxProcessEventsBytesPerRound = 32 * 1024 * 1024
)

func preferPositive(primary, fallback int64) int64 {
	if primary > 0 {
		return primary
	}
	return fallback
}

type storedProcessEvent struct {
	Type       string `json:"type"`
	Content    string `json:"content"`
	ToolUseID  string `json:"tool_use_id"`
	Timestamp  int64  `json:"timestamp"`
	Permission []struct {
		ToolName  string `json:"tool_name"`
		ToolInput string `json:"tool_input"`
	} `json:"permission_requests"`
}

// loadStepProcessEvents reads the local execution timeline (thinking, output,
// tool calls, tool results, permission requests) for every session of the step.
func loadStepProcessEvents(ctx context.Context, db syncQueryer, taskUUID, stepUUID string) (map[string][]cloud.PipelineTaskProcessEvent, bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.session_uuid, e.sequence, e.event_type, e.payload_json, e.created_at
		 FROM gt_session_events e
		 JOIN gt_cli_sessions s ON s.uuid = e.session_uuid
		 WHERE s.task_uuid = ? AND s.step_uuid = ?
		   AND e.event_type IN ('thinking', 'message', 'tool_call', 'tool_result', 'permission_request')
		 ORDER BY e.session_uuid, e.sequence`, taskUUID, stepUUID)
	if err != nil {
		return nil, false, fmt.Errorf("查询步骤执行过程失败: %w", err)
	}
	defer rows.Close()

	grouped := make(map[string][]cloud.PipelineTaskProcessEvent)
	totals := make(map[string]int)
	truncated := false
	for rows.Next() {
		var sessionUUID, eventType, payload string
		var sequence int
		var createdAt int64
		if err := rows.Scan(&sessionUUID, &sequence, &eventType, &payload, &createdAt); err != nil {
			return nil, false, fmt.Errorf("读取步骤执行过程失败: %w", err)
		}
		event, ok, eventTruncated := decodeProcessEvent(sequence, eventType, payload, createdAt)
		if !ok {
			continue
		}
		if eventTruncated {
			truncated = true
		}
		if len(grouped[sessionUUID]) >= maxProcessEventsPerRound {
			truncated = true
			continue
		}
		if eventSequenceUsed(grouped[sessionUUID], event.Sequence) {
			truncated = true
			continue
		}
		fitted, ok := fitProcessContent(event.Content, totals[sessionUUID])
		if !ok {
			truncated = true
			continue
		}
		if fitted != event.Content {
			truncated = true
			event.Content = fitted
		}
		totals[sessionUUID] += len(event.Content)
		grouped[sessionUUID] = append(grouped[sessionUUID], event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return grouped, truncated, nil
}

func decodeProcessEvent(sequence int, eventType, payload string, createdAt int64) (cloud.PipelineTaskProcessEvent, bool, bool) {
	if sequence <= 0 {
		return cloud.PipelineTaskProcessEvent{}, false, false
	}
	var stored storedProcessEvent
	if strings.TrimSpace(payload) != "" && json.Unmarshal([]byte(payload), &stored) != nil {
		return cloud.PipelineTaskProcessEvent{}, false, false
	}
	eventType = strings.TrimSpace(eventType)
	if stored.Type != "" {
		eventType = stored.Type
	}
	switch eventType {
	case "thinking", "message", "tool_call", "tool_result", "permission_request":
	default:
		return cloud.PipelineTaskProcessEvent{}, false, false
	}
	content := strings.TrimSpace(stored.Content)
	if content == "" && eventType == "permission_request" {
		content = permissionEventContent(stored)
	}
	if content == "" && eventType != "tool_call" {
		return cloud.PipelineTaskProcessEvent{}, false, false
	}
	timestamp := stored.Timestamp
	if timestamp <= 0 {
		timestamp = createdAt
	}
	content, truncated := trimProcessContent(content, maxProcessEventContentBytes)
	toolUseID := strings.TrimSpace(stored.ToolUseID)
	if len(toolUseID) > 128 {
		toolUseID = toolUseID[:128]
		truncated = true
	}
	return cloud.PipelineTaskProcessEvent{
		Sequence:  sequence,
		Type:      eventType,
		Content:   content,
		ToolUseID: toolUseID,
		Timestamp: timestamp,
	}, true, truncated
}

func permissionEventContent(stored storedProcessEvent) string {
	parts := make([]string, 0, len(stored.Permission))
	for _, request := range stored.Permission {
		piece := strings.TrimSpace(strings.TrimSpace(request.ToolName) + " " + strings.TrimSpace(request.ToolInput))
		if piece != "" {
			parts = append(parts, piece)
		}
	}
	return strings.Join(parts, "；")
}

func eventSequenceUsed(events []cloud.PipelineTaskProcessEvent, sequence int) bool {
	for _, event := range events {
		if event.Sequence == sequence {
			return true
		}
	}
	return false
}

func truncateUTF8Bytes(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	cut := value[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func fitProcessContent(content string, used int) (string, bool) {
	remaining := maxProcessEventsBytesPerRound - used
	if remaining <= 0 {
		return "", false
	}
	if len(content) <= remaining {
		return content, true
	}
	trimmed, _ := trimProcessContent(content, remaining)
	return trimmed, true
}

func trimProcessContent(content string, limit int) (string, bool) {
	if len(content) <= limit {
		return content, false
	}
	const suffix = "\n…(已截断)"
	budget := limit - len(suffix)
	if budget < 1 {
		budget = limit
	}
	cut := content
	if len(cut) > budget {
		cut = cut[:budget]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if budget >= len(suffix) && len(content) > limit {
		return cut + suffix, true
	}
	return cut, true
}

// assistantMessageContent returns the round's visible AI reply, falling back to the
// error summary or a status placeholder: the cloud rejects empty assistant messages,
// and a single empty row would permanently block the whole task's sync.
func assistantMessageContent(r roundRow) string {
	if content := strings.TrimSpace(r.assistantMessage); content != "" {
		return r.assistantMessage
	}
	if content := strings.TrimSpace(r.errorSummary); content != "" {
		return content
	}
	return "CLI 执行" + r.status
}

// Cloud pipeline_tasks columns are short VARCHAR values. A single over-long name,
// CLI id, or step key makes Postgres reject the insert, and the cloud reports that
// as HTTP 400 "参数无效". The local task can still be done while the requirement
// Agent tab never receives a row. Clip to the column character limits before sending.
const (
	cloudTitleMaxRunes        = 500
	cloudPipelineNameMaxRunes = 100
	cloudCLITypeMaxRunes      = 20
	cloudStatusMaxRunes       = 20
	cloudStepKeyMaxRunes      = 64
	cloudStepNameMaxRunes     = 100
)

func clipRunes(taskUUID, field, value string, max int) string {
	clipped := truncateRunes(value, max)
	if clipped != value {
		applog.Warn("[CloudSync] 任务投影字段超过云端列长度，已截断后推送",
			"task_uuid", taskUUID, "field", field, "max_runes", max)
	}
	return clipped
}

// repairCloudWorkItemLink fills a cloud task's workspace and work-item type from
// 「我的工作」 before the projection is built. workspace_id<=0 or a type other than
// requirement/defect makes the client skip the push, so the Agent tab stays empty
// even after the local task is marked done. The import list cannot be used here:
// it labels defects as requirements and only returns the newest page.
func repairCloudWorkItemLink(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	var sourceType, workItemType, workItemID string
	var workspaceID int64
	err := db.QueryRowContext(ctx, `
		SELECT t.source_type,
		       COALESCE(NULLIF(t.work_item_type, ''), t.cloud_work_item_type, ''),
		       COALESCE(NULLIF(t.work_item_id, ''), t.cloud_work_item_id, ''),
		       COALESCE(t.workspace_id, 0)
		FROM gt_tasks t WHERE t.uuid = ?`, taskUUID).Scan(&sourceType, &workItemType, &workItemID, &workspaceID)
	if err == sql.ErrNoRows || sourceType != "cloud" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取任务工作项关联失败: %w", err)
	}
	workItemType = strings.TrimSpace(workItemType)
	workItemID = strings.TrimSpace(workItemID)
	typeValid := workItemType == "requirement" || workItemType == "defect"
	if workspaceID > 0 && typeValid {
		return nil
	}
	if _, parseErr := strconv.ParseInt(workItemID, 10, 64); parseErr != nil || workItemID == "" || workItemID == "0" {
		return nil
	}
	result, err := client.GetMyWork(ctx)
	if err != nil {
		return err
	}
	foundWorkspace, itemType, found := cloud.LookupMyWorkItem(result.List, workItemID)
	if !found || foundWorkspace <= 0 {
		applog.Warn("[CloudSync] 我的工作中没有这条工作项，无法补全 workspace_id，任务投影仍会跳过",
			"task_uuid", taskUUID, "work_item_id", workItemID, "work_item_type", workItemType)
		return nil
	}
	if itemType != "requirement" && itemType != "defect" {
		itemType = workItemType
	}
	res, err := db.ExecContext(ctx, `
		UPDATE gt_tasks
		SET workspace_id = CASE WHEN COALESCE(workspace_id, 0) > 0 THEN workspace_id ELSE ? END,
		    work_item_type = CASE
		      WHEN work_item_type IN ('requirement', 'defect') THEN work_item_type
		      WHEN ? IN ('requirement', 'defect') THEN ?
		      ELSE work_item_type
		    END,
		    updated_at = ?
		WHERE uuid = ? AND source_type = 'cloud'
		  AND (COALESCE(workspace_id, 0) <= 0 OR IFNULL(work_item_type, '') NOT IN ('requirement', 'defect'))`,
		foundWorkspace, itemType, itemType, time.Now().UnixMilli(), taskUUID)
	if err != nil {
		return fmt.Errorf("写回工作项关联失败: %w", err)
	}
	if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows > 0 {
		applog.Info("[CloudSync] 已补全云端工作项关联",
			"task_uuid", taskUUID, "workspace_id", foundWorkspace, "work_item_type", itemType)
	}
	return nil
}

// ======================== Helpers ========================

func projectionHash(base *taskBase, steps []cloud.PipelineTaskStep) string {
	hasher := sha256.New()
	hasher.Write([]byte(strconv.Itoa(base.executionIndex)))
	hasher.Write([]byte(base.taskStatus))
	hasher.Write([]byte(base.executionStatus))
	hasher.Write([]byte(base.currentStepKey))
	hasher.Write([]byte(base.lastLocalUpdatedAt))
	for _, step := range steps {
		hasher.Write([]byte(step.StepKey + step.Status + step.OutputSummary + step.ErrorSummary))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// unixMillisToRFC3339 converts local Unix-millisecond timestamps to cloud RFC3339 strings.
func unixMillisToRFC3339(ms int64) *string {
	if ms <= 0 {
		return nil
	}
	value := time.UnixMilli(ms).UTC().Format(time.RFC3339)
	return &value
}

func parseMillis(rfc3339 string) int64 {
	if rfc3339 == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, rfc3339)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func minNonZero(current, candidate int64) int64 {
	if candidate <= 0 {
		return current
	}
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
}
