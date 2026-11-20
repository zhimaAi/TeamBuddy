package codexhook

import (
	"path/filepath"

	"github.com/google/uuid"
)

// A generic skill request cannot claim native ownership without the local
// launch that the callback has already claimed. No chat content leaves here.
func ClaimedSession(task, thread string, index int) (claimed, explicitPrivacy bool) {
	u, err := uuid.Parse(thread)
	if err != nil || u.String() != thread {
		return false, false
	}
	var s state
	root := callbackRoot()
	if read(filepath.Join(root, "session-"+thread+".json"), &s) != nil || s.TaskUUID != task || s.Index != index || s.SessionID != thread || s.Stale {
		return false, false
	}
	id, err := uuid.Parse(s.LaunchID)
	if err != nil || id.String() != s.LaunchID {
		return false, false
	}
	var l launch
	claimed = read(filepath.Join(root, "launch-"+s.LaunchID+".json"), &l) == nil && l.TaskUUID == task && l.Index == index && l.SessionID == thread
	return claimed, claimed && l.ExplicitPrivacy && s.ExplicitPrivacy
}
func DeliveryWarning(task, thread string, index int) bool {
	u, err := uuid.Parse(thread)
	if err != nil || u.String() != thread {
		return true
	}
	var s state
	return read(filepath.Join(callbackRoot(), "session-"+thread+".json"), &s) != nil || s.TaskUUID != task || s.Index != index || s.Stale || s.Gap
}

// BoundWorkspace exposes only a claimed task's workspace, including Codex-created Git worktrees.
func BoundWorkspace(task, thread string, index int) string {
	claimed, _ := ClaimedSession(task, thread, index)
	if !claimed {
		return ""
	}
	var s state
	if read(filepath.Join(callbackRoot(), "session-"+thread+".json"), &s) != nil || s.TaskUUID != task || s.SessionID != thread || s.Index != index || s.Stale {
		return ""
	}
	if s.ChatDir != "" {
		return s.ChatDir
	}
	return s.CWD
}
