package cloudsync

import (
	"context"
	"strings"

	"goteams-client/internal/cloud"
)

// Vibe has no managed CLI process. Its public conversation consists only of
// Skill-reported user messages and final answers, identified by native sessions.
func buildVibeSession(ctx context.Context, db syncQueryer, taskUUID, tool string, index int) (*cloud.PipelineTaskSessionSync, error) {
	result := &cloud.PipelineTaskSessionSync{
		LocalTaskUUID: taskUUID, ExecutionIndex: index,
		StepKey: "vibe_coding", StepName: "Vibe Coding", CliType: tool,
		Conversation: cloud.PipelineTaskConversationContent{Rounds: make([]cloud.PipelineTaskRound, 0)},
	}
	if err := db.QueryRowContext(ctx, `SELECT status, created_at, updated_at FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&result.Status, &result.CreatedAt, &result.ClientUpdatedAt); err != nil {
		return nil, err
	}
	switch result.Status {
	case "active":
		result.Status = "running"
	case "done":
		result.Status = "success"
	}
	rows, err := db.QueryContext(ctx, `SELECT external_session_id, user_prompt, final_result, created_at
		FROM gt_task_progress WHERE task_uuid=? AND execution_mode='vibe_coding'
		AND external_session_id<>'' AND activity_kind IN ('user_message', 'final_response')
		ORDER BY created_at, rowid`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var threadID, question, answer string
		var at int64
		if err := rows.Scan(&threadID, &question, &answer, &at); err != nil {
			return nil, err
		}
		if strings.TrimSpace(question) == "" && strings.TrimSpace(answer) == "" {
			continue
		}
		rounds := &result.Conversation.Rounds
		if question != "" || len(*rounds) == 0 || (*rounds)[len(*rounds)-1].SessionUUID != threadID {
			*rounds = append(*rounds, cloud.PipelineTaskRound{
				RoundNo: len(*rounds) + 1, SessionUUID: threadID, Status: "running", UserMessage: question,
				AssistantMessages: make([]cloud.PipelineTaskMessage, 0), StartedAt: unixMillisToRFC3339(at),
			})
		}
		round := &(*rounds)[len(*rounds)-1]
		if answer != "" {
			round.AssistantMessages = append(round.AssistantMessages, cloud.PipelineTaskMessage{
				Sequence: len(round.AssistantMessages) + 1, Role: "assistant", Content: answer,
			})
			round.Status, round.FinishedAt = "success", unixMillisToRFC3339(at)
			result.FinalResult = answer
		}
		if result.StartedAt == 0 {
			result.StartedAt = at
		}
		if result.Status == "success" {
			result.FinishedAt = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result.Conversation.RoundCount = len(result.Conversation.Rounds)
	result.ConversationRounds = result.Conversation.RoundCount
	// Inspect untruncated replies before applying the transport limits.
	if !isCodexVibe(result) {
		fitSessionSyncPayload(result)
	}
	return result, nil
}
