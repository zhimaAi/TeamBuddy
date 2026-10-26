package localserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"goteams-client/internal/executor"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"
)

const (
	taskActivityPreviewLimit = 300

	activityStatusRunning   = "running"
	activityStatusCompleted = "completed"
	activityStatusError     = "error"

	activityActorUser   = "user"
	activityActorTool   = "tool"
	activityActorSystem = "system"

	activityKindAssignment         = "assignment"
	activityKindUserMessage        = "user_message"
	activityKindFinalResponse      = "final_response"
	activityKindError              = "error"
	activityKindSystemNotification = "system_notification"
)

type taskActivityTarget struct {
	UUID          string
	Status        string
	ExecutionMode string
}

type taskLatestActivity struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Actor      string `json:"actor"`
	Kind       string `json:"kind"`
	Preview    string `json:"preview"`
	OccurredAt int64  `json:"occurred_at"`
}

type taskActivityDetail struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Actor      string `json:"actor"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	OccurredAt int64  `json:"occurred_at"`
}

func vibeCodingAssignmentProgressUUID(taskUUID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("vibe-coding-assignment:"+taskUUID)).String()
}

// isTeambuddyKickoffPrompt reports the launch instruction TeamBuddy sends to the tool.
// That text is not the user's task input. The legacy "teamsboard" markers are still
// matched so activity records written before the rename keep resolving to the launch
// prompt instead of being shown as the user's own words.
func isTeambuddyKickoffPrompt(text string) bool {
	text = strings.TrimSpace(text)
	for _, marker := range []string{"teambuddy", "teamsboard"} {
		if strings.Contains(text, "[$"+marker+"]") ||
			strings.Contains(text, "请使用 "+marker+" Skill") ||
			strings.Contains(text, "Use the "+marker+" Skill") {
			return true
		}
	}
	return false
}

// vibeBoardUserText keeps the board card on the user's own words.
// A tool reply or the skill launch prompt is not shown in that slot.
func vibeBoardUserText(recordType, kind, prompt, result, userQuestion, snapshot, title string) string {
	if (recordType == "user_question" || kind == activityKindUserMessage) && !isTeambuddyKickoffPrompt(prompt) {
		return prompt
	}
	if text := strings.TrimSpace(userQuestion); text != "" && !isTeambuddyKickoffPrompt(text) {
		return text
	}
	if text := strings.TrimSpace(snapshot); text != "" {
		return text
	}
	if text := strings.TrimSpace(title); text != "" {
		return text
	}
	if recordType == "user_question" || kind == activityKindUserMessage {
		return prompt
	}
	return result
}

func activityPreview(content string) string {
	return executor.Truncate(executor.OneLine(strings.TrimSpace(content)), taskActivityPreviewLimit)
}

func newTaskLatestActivity(id, status, actor, kind, content string, occurredAt int64) *taskLatestActivity {
	return &taskLatestActivity{
		ID: id, Status: status, Actor: actor, Kind: kind,
		Preview: activityPreview(content), OccurredAt: occurredAt,
	}
}

func activityStatusForSession(status string) string {
	switch status {
	case workflow.SessionStatusCreated, workflow.SessionStatusRunning, workflow.SessionStatusWaiting, workflow.SessionStatusStopRequested:
		return activityStatusRunning
	case workflow.SessionStatusSuccess:
		return activityStatusCompleted
	case workflow.SessionStatusFailed, workflow.SessionStatusInterrupted, workflow.SessionStatusStopped:
		return activityStatusError
	default:
		return ""
	}
}

func isBoardProcessEvent(eventType string) bool {
	switch eventType {
	case executor.EventThinking, executor.EventMessage, executor.EventToolCall,
		executor.EventToolResult, executor.EventPermission:
		return true
	default:
		return false
	}
}

func valuePlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("(?),", count), ",")
}

func taskUUIDArgs(targets []taskActivityTarget) []interface{} {
	args := make([]interface{}, 0, len(targets))
	for _, target := range targets {
		args = append(args, target.UUID)
	}
	return args
}

func loadLatestTaskActivities(ctx context.Context, db *sql.DB, targets []taskActivityTarget) (map[string]*taskLatestActivity, error) {
	activities := make(map[string]*taskLatestActivity, len(targets))
	if len(targets) == 0 {
		return activities, nil
	}
	targetByUUID := make(map[string]taskActivityTarget, len(targets))
	cliTargets := make([]taskActivityTarget, 0, len(targets))
	vibeTargets := make([]taskActivityTarget, 0, len(targets))
	for _, target := range targets {
		targetByUUID[target.UUID] = target
		if target.ExecutionMode == taskruntime.ExecutionModeVibeCoding {
			vibeTargets = append(vibeTargets, target)
		} else {
			cliTargets = append(cliTargets, target)
		}
	}
	if len(cliTargets) > 0 {
		if err := loadLatestCLITaskActivities(ctx, db, cliTargets, targetByUUID, activities); err != nil {
			return nil, err
		}
	}
	if len(vibeTargets) > 0 {
		if err := loadLatestVibeTaskActivities(ctx, db, vibeTargets, targetByUUID, activities); err != nil {
			return nil, err
		}
	}
	return activities, nil
}

func loadLatestCLITaskActivities(
	ctx context.Context,
	db *sql.DB,
	targets []taskActivityTarget,
	targetByUUID map[string]taskActivityTarget,
	activities map[string]*taskLatestActivity,
) error {
	query := fmt.Sprintf(`WITH requested(task_uuid) AS (VALUES %s)
		SELECT s.uuid, s.task_uuid, s.status, substr(s.final_result, 1, ?),
		s.created_at, s.started_at, s.finished_at, s.updated_at,
		COALESCE(e.sequence, 0), COALESCE(e.event_type, ''),
		CASE WHEN json_valid(e.payload_json) THEN substr(COALESCE(json_extract(e.payload_json, '$.content'), ''), 1, ?) ELSE '' END,
		COALESCE(e.created_at, 0)
		FROM requested r
		JOIN gt_cli_sessions s ON s.rowid = (
			SELECT s2.rowid FROM gt_cli_sessions s2
			WHERE s2.task_uuid=r.task_uuid
			ORDER BY s2.created_at DESC, s2.rowid DESC LIMIT 1
		)
		LEFT JOIN gt_session_events e ON e.id = (
			SELECT e2.id FROM gt_session_events e2
			WHERE e2.session_uuid=s.uuid
			  AND e2.event_type IN ('thinking','message','tool_call','tool_result','permission_request')
			ORDER BY e2.sequence DESC LIMIT 1
		)`, valuePlaceholders(len(targets)))
	args := taskUUIDArgs(targets)
	args = append(args, taskActivityPreviewLimit, taskActivityPreviewLimit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("查询任务最近 CLI 动态失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var createdAt, startedAt, finishedAt, updatedAt, eventAt int64
		var sequence int
		var sessionUUID, taskUUID, sessionStatus, finalResult, eventType, eventContent string
		if err := rows.Scan(&sessionUUID, &taskUUID, &sessionStatus, &finalResult,
			&createdAt, &startedAt, &finishedAt, &updatedAt,
			&sequence, &eventType, &eventContent, &eventAt); err != nil {
			return fmt.Errorf("读取任务最近 CLI 动态失败: %w", err)
		}
		if targetByUUID[taskUUID].ExecutionMode == taskruntime.ExecutionModeVibeCoding {
			continue
		}
		status := activityStatusForSession(sessionStatus)
		if status == "" {
			continue
		}
		var candidate *taskLatestActivity
		if status != activityStatusRunning {
			occurredAt := finishedAt
			if occurredAt == 0 {
				occurredAt = updatedAt
			}
			kind := activityKindFinalResponse
			if status == activityStatusError {
				kind = activityKindError
			}
			candidate = newTaskLatestActivity("final:"+sessionUUID, status, activityActorTool, kind,
				finalResult, occurredAt)
		} else if sequence > 0 && isBoardProcessEvent(eventType) {
			candidate = newTaskLatestActivity(fmt.Sprintf("event:%s:%d", sessionUUID, sequence), status,
				activityActorTool, eventType, eventContent, eventAt)
		} else {
			occurredAt := startedAt
			if occurredAt == 0 {
				occurredAt = createdAt
			}
			candidate = newTaskLatestActivity("session:"+sessionUUID, status, activityActorTool,
				executor.EventThinking, "", occurredAt)
		}
		activities[taskUUID] = candidate
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历任务最近 CLI 动态失败: %w", err)
	}
	return nil
}

func loadLatestVibeTaskActivities(
	ctx context.Context,
	db *sql.DB,
	targets []taskActivityTarget,
	targetByUUID map[string]taskActivityTarget,
	activities map[string]*taskLatestActivity,
) error {
	query := fmt.Sprintf(`WITH requested(task_uuid) AS (VALUES %s)
		SELECT p.uuid, p.task_uuid, p.record_type,
		substr(p.user_prompt, 1, ?), substr(p.final_result, 1, ?),
		p.status, p.activity_actor, p.activity_kind, p.created_at, p.finished_at,
		substr(COALESCE((
			SELECT q.user_prompt FROM gt_task_progress q
			WHERE q.task_uuid=r.task_uuid AND q.record_type='user_question'
			ORDER BY q.created_at DESC, q.rowid DESC LIMIT 1
		), ''), 1, ?),
		substr(COALESCE(t.content_snapshot, ''), 1, ?),
		substr(COALESCE(t.title, ''), 1, ?)
		FROM requested r
		JOIN gt_tasks t ON t.uuid=r.task_uuid
		JOIN gt_task_progress p ON p.rowid = (
			SELECT p2.rowid FROM gt_task_progress p2
			WHERE p2.task_uuid=r.task_uuid AND p2.execution_mode='vibe_coding'
			ORDER BY p2.created_at DESC, p2.rowid DESC LIMIT 1
		)`, valuePlaceholders(len(targets)))
	args := taskUUIDArgs(targets)
	args = append(args, taskActivityPreviewLimit, taskActivityPreviewLimit, taskActivityPreviewLimit, taskActivityPreviewLimit, taskActivityPreviewLimit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("查询 Vibe Coding 最近动态失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var createdAt, finishedAt int64
		var progressUUID, taskUUID, recordType, prompt, result, progressStatus, actor, kind string
		var userQuestion, snapshot, title string
		if err := rows.Scan(&progressUUID, &taskUUID, &recordType, &prompt, &result,
			&progressStatus, &actor, &kind, &createdAt, &finishedAt, &userQuestion, &snapshot, &title); err != nil {
			return fmt.Errorf("读取 Vibe Coding 最近动态失败: %w", err)
		}
		if targetByUUID[taskUUID].ExecutionMode != taskruntime.ExecutionModeVibeCoding {
			continue
		}
		actor, kind = normalizeVibeActivitySemantics(taskUUID, progressUUID, recordType, progressStatus, actor, kind)
		content := vibeBoardUserText(recordType, kind, prompt, result, userQuestion, snapshot, title)
		occurredAt := finishedAt
		if recordType == "user_question" || kind == activityKindUserMessage {
			occurredAt = createdAt
		} else if occurredAt == 0 {
			occurredAt = createdAt
		}
		status := vibeActivityStatus(targetByUUID[taskUUID].Status, progressStatus, actor, kind)
		activities[taskUUID] = newTaskLatestActivity("progress:"+progressUUID, status, actor, kind, content, occurredAt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 Vibe Coding 最近动态失败: %w", err)
	}
	return nil
}

func normalizeVibeActivitySemantics(taskUUID, progressUUID, recordType, progressStatus, actor, kind string) (string, string) {
	if actor != "" && kind != "" {
		return actor, kind
	}
	if recordType == "user_question" {
		return activityActorUser, activityKindUserMessage
	}
	if progressUUID == vibeCodingAssignmentProgressUUID(taskUUID) {
		return activityActorSystem, activityKindAssignment
	}
	switch progressStatus {
	case workflow.SessionStatusCreated, workflow.SessionStatusRunning, workflow.SessionStatusWaiting, workflow.SessionStatusStopRequested:
		return activityActorTool, executor.EventThinking
	case workflow.SessionStatusFailed, workflow.SessionStatusInterrupted, workflow.SessionStatusStopped:
		return activityActorTool, activityKindError
	default:
		return activityActorTool, activityKindFinalResponse
	}
}

func vibeActivityStatus(taskStatus, progressStatus, actor, kind string) string {
	if taskStatus == "blocked" || kind == activityKindError {
		return activityStatusError
	}
	switch progressStatus {
	case workflow.SessionStatusFailed, workflow.SessionStatusInterrupted, workflow.SessionStatusStopped:
		return activityStatusError
	case workflow.SessionStatusSuccess:
		if actor == activityActorTool && kind == activityKindFinalResponse {
			return activityStatusCompleted
		}
	}
	return activityStatusRunning
}

func loadTaskActivityDetail(ctx context.Context, db *sql.DB, taskUUID, activityID string) (*taskActivityDetail, error) {
	switch {
	case strings.HasPrefix(activityID, "session:"):
		return loadPendingSessionActivity(ctx, db, taskUUID, activityID)
	case strings.HasPrefix(activityID, "event:"):
		return loadSessionEventActivity(ctx, db, taskUUID, activityID)
	case strings.HasPrefix(activityID, "final:"):
		return loadSessionFinalActivity(ctx, db, taskUUID, activityID)
	case strings.HasPrefix(activityID, "progress:"):
		return loadProgressActivity(ctx, db, taskUUID, activityID)
	default:
		return nil, sql.ErrNoRows
	}
}

func loadPendingSessionActivity(ctx context.Context, db *sql.DB, taskUUID, activityID string) (*taskActivityDetail, error) {
	sessionUUID := strings.TrimPrefix(activityID, "session:")
	if sessionUUID == "" || strings.Contains(sessionUUID, ":") {
		return nil, sql.ErrNoRows
	}
	var sessionStatus string
	var createdAt, startedAt int64
	err := db.QueryRowContext(ctx, `SELECT status, created_at, started_at FROM gt_cli_sessions
		WHERE task_uuid=? AND uuid=?`, taskUUID, sessionUUID).Scan(&sessionStatus, &createdAt, &startedAt)
	if err != nil {
		return nil, err
	}
	if startedAt == 0 {
		startedAt = createdAt
	}
	return &taskActivityDetail{ID: activityID, Status: activityStatusForSession(sessionStatus), Actor: activityActorTool,
		Kind: executor.EventThinking, Content: "", OccurredAt: startedAt}, nil
}

func loadSessionEventActivity(ctx context.Context, db *sql.DB, taskUUID, activityID string) (*taskActivityDetail, error) {
	parts := strings.Split(activityID, ":")
	if len(parts) != 3 {
		return nil, sql.ErrNoRows
	}
	sequence, err := strconv.Atoi(parts[2])
	if err != nil || sequence <= 0 {
		return nil, sql.ErrNoRows
	}
	var sessionStatus, eventType, payload string
	var occurredAt int64
	err = db.QueryRowContext(ctx, `SELECT s.status, e.event_type, e.payload_json, e.created_at
		FROM gt_session_events e JOIN gt_cli_sessions s ON s.uuid=e.session_uuid
		WHERE s.task_uuid=? AND s.uuid=? AND e.sequence=?`, taskUUID, parts[1], sequence).
		Scan(&sessionStatus, &eventType, &payload, &occurredAt)
	if err != nil || !isBoardProcessEvent(eventType) {
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, err
	}
	var event executor.ExecutorEvent
	if json.Unmarshal([]byte(payload), &event) != nil {
		return nil, sql.ErrNoRows
	}
	return &taskActivityDetail{ID: activityID, Status: activityStatusForSession(sessionStatus), Actor: activityActorTool,
		Kind: eventType, Content: strings.TrimSpace(event.Content), OccurredAt: occurredAt}, nil
}

func loadSessionFinalActivity(ctx context.Context, db *sql.DB, taskUUID, activityID string) (*taskActivityDetail, error) {
	sessionUUID := strings.TrimPrefix(activityID, "final:")
	if sessionUUID == "" || strings.Contains(sessionUUID, ":") {
		return nil, sql.ErrNoRows
	}
	var sessionStatus, content string
	var finishedAt, updatedAt int64
	err := db.QueryRowContext(ctx, `SELECT status, final_result, finished_at, updated_at FROM gt_cli_sessions
		WHERE task_uuid=? AND uuid=?`, taskUUID, sessionUUID).Scan(&sessionStatus, &content, &finishedAt, &updatedAt)
	if err != nil || strings.TrimSpace(content) == "" {
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, err
	}
	status := activityStatusForSession(sessionStatus)
	kind := activityKindFinalResponse
	if status == activityStatusError {
		kind = activityKindError
	}
	if finishedAt == 0 {
		finishedAt = updatedAt
	}
	return &taskActivityDetail{ID: activityID, Status: status, Actor: activityActorTool,
		Kind: kind, Content: strings.TrimSpace(content), OccurredAt: finishedAt}, nil
}

func loadProgressActivity(ctx context.Context, db *sql.DB, taskUUID, activityID string) (*taskActivityDetail, error) {
	progressUUID := strings.TrimPrefix(activityID, "progress:")
	if progressUUID == "" || strings.Contains(progressUUID, ":") {
		return nil, sql.ErrNoRows
	}
	var taskStatus, recordType, prompt, result, progressStatus, actor, kind string
	var createdAt, finishedAt int64
	err := db.QueryRowContext(ctx, `SELECT t.status, p.record_type, p.user_prompt, p.final_result, p.status,
		p.activity_actor, p.activity_kind, p.created_at, p.finished_at
		FROM gt_task_progress p JOIN gt_tasks t ON t.uuid=p.task_uuid
		WHERE p.task_uuid=? AND p.uuid=? AND p.execution_mode='vibe_coding'`, taskUUID, progressUUID).
		Scan(&taskStatus, &recordType, &prompt, &result, &progressStatus, &actor, &kind, &createdAt, &finishedAt)
	if err != nil {
		return nil, err
	}
	actor, kind = normalizeVibeActivitySemantics(taskUUID, progressUUID, recordType, progressStatus, actor, kind)
	var userQuestion, snapshot, title string
	if err = db.QueryRowContext(ctx, `SELECT COALESCE((
			SELECT q.user_prompt FROM gt_task_progress q
			WHERE q.task_uuid=? AND q.record_type='user_question'
			ORDER BY q.created_at DESC, q.rowid DESC LIMIT 1
		), ''), COALESCE(content_snapshot, ''), COALESCE(title, '')
		FROM gt_tasks WHERE uuid=?`, taskUUID, taskUUID).Scan(&userQuestion, &snapshot, &title); err != nil {
		userQuestion, snapshot, title = "", "", ""
	}
	content := vibeBoardUserText(recordType, kind, prompt, result, userQuestion, snapshot, title)
	occurredAt := finishedAt
	if recordType == "user_question" || kind == activityKindUserMessage {
		occurredAt = createdAt
	} else if occurredAt == 0 {
		occurredAt = createdAt
	}
	return &taskActivityDetail{ID: activityID, Status: vibeActivityStatus(taskStatus, progressStatus, actor, kind),
		Actor: actor, Kind: kind, Content: strings.TrimSpace(content), OccurredAt: occurredAt}, nil
}
