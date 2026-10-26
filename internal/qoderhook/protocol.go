package qoderhook

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"goteams-client/internal/taskskillclient"
)

const (
	qoderMarker     = "TeamBuddy-Qoder-IDE: "
	maxInputBytes   = 2 << 20
	maxContentRunes = taskskillclient.MaxActivityContentRunes
)

var qoderSessionPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,254}[A-Za-z0-9])?$`)

type qoderPromptRejectedError struct{ error }

func (e *qoderPromptRejectedError) Unwrap() error { return e.error }

// ExitCode distinguishes an unsaved prompt from a non-blocking callback error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var rejected *qoderPromptRejectedError
	if errors.As(err, &rejected) {
		return 2
	}
	return 1
}

type qoderEvent struct {
	SessionID  string `json:"session_id"`
	CWD        string `json:"cwd"`
	Name       string `json:"hook_event_name"`
	Prompt     string `json:"prompt"`
	Answer     string `json:"last_assistant_message"`
	StopActive bool   `json:"stop_hook_active"`
}
type qoderBinding struct {
	TaskUUID       string `json:"task_uuid"`
	ExecutionIndex int    `json:"execution_index"`
}

func readEvent(input io.Reader) (qoderEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil || len(raw) > maxInputBytes || !utf8.Valid(raw) {
		return qoderEvent{}, errors.New("invalid Qoder hook input")
	}
	var event qoderEvent
	if json.Unmarshal(raw, &event) != nil {
		return qoderEvent{}, errors.New("invalid Qoder hook JSON")
	}
	return event, nil
}

func parseQoderMarker(prompt string) (*qoderBinding, error) {
	var result *qoderBinding
	for _, line := range strings.Split(prompt, "\n") {
		if !strings.HasPrefix(line, qoderMarker) {
			continue
		}
		if result != nil {
			return nil, errors.New("duplicate Qoder task binding marker")
		}
		var raw struct {
			TaskUUID string `json:"task_uuid"`
			Index    *int   `json:"execution_index"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, qoderMarker)), &raw) != nil || raw.Index == nil || *raw.Index < 0 {
			return nil, errors.New("invalid Qoder task binding marker")
		}
		id, err := taskskillclient.NormalizeTaskID(raw.TaskUUID)
		if err != nil {
			return nil, err
		}
		result = &qoderBinding{TaskUUID: id, ExecutionIndex: *raw.Index}
	}
	return result, nil
}
func prepareActivity(event qoderEvent, state qoderState, hasMarker bool) (*qoderActivity, string, error) {
	var stopHash string
	needsActivity := event.Name == "UserPromptSubmit" && !hasMarker
	if event.Name == "Stop" && strings.TrimSpace(event.Answer) != "" {
		stopHash = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(event.Answer))))
		// 只观察答复，不阻断 Stop；同一轮质量门禁后的修正答复也应同步。
		// 旧状态缺少哈希时保留普通 Stop 去重，但允许门禁修正答复首次回传。
		needsActivity = state.StopTurn != state.Turn || state.StopHash != "" && state.StopHash != stopHash || state.StopHash == "" && event.StopActive
	}
	if event.Name == "UserPromptSubmit" && !hasMarker && (strings.TrimSpace(event.Prompt) == "" || utf8.RuneCountInString(event.Prompt) > maxContentRunes) {
		return nil, "", errors.New("Qoder prompt is empty or too long")
	}
	if event.Name == "Stop" && needsActivity && utf8.RuneCountInString(event.Answer) > maxContentRunes {
		return nil, "", errors.New("Qoder final reply is too long to synchronize")
	}
	if !needsActivity {
		return nil, stopHash, nil
	}
	item := qoderActivity{ID: uuid.NewString(), Type: "question", Content: event.Prompt}
	if event.Name == "Stop" {
		item.Type, item.Content = "result", event.Answer
	}
	return &item, stopHash, nil
}

func writeContext(output io.Writer, event qoderEvent, state qoderState, deferred bool) error {
	contextText := fmt.Sprintf("TEAMBUDDY_QODER_SESSION_ID=%s\nTeamBuddy task_uuid=%s; execution_index=%d; tool=qoder_ide. User messages and final replies are reported by native Hooks only. Use the teambuddy Skill to read task/history and update business status; do not report activities again. A Stop event does not mean the task is done.", event.SessionID, state.TaskUUID, state.ExecutionIndex)
	if deferred {
		contextText += "\nTask callback failed; do not claim synchronization succeeded. Pending events remain local."
	}
	return json.NewEncoder(output).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event.Name, "additionalContext": contextText}})
}
