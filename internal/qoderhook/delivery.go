package qoderhook

import (
	"context"
	"errors"
	"os"

	"goteams-client/internal/taskskillclient"
)

type taskClient interface {
	ReadTask(context.Context, string, string) (taskskillclient.TaskSnapshot, error)
	BindSession(context.Context, string, string, taskskillclient.SessionBinding) error
	ReportActivity(context.Context, string, string, taskskillclient.Activity) error
}

func (h *handler) bindState(ctx context.Context, file, sessionID string, state *qoderState) error {
	// Revalidate the original generation; never refresh it from a newer task.
	err := h.client.BindSession(ctx, state.TaskUUID, "", taskskillclient.SessionBinding{
		ExecutionIndex: state.ExecutionIndex, Type: "qoder_ide", ThreadID: sessionID,
	})
	if err != nil && terminalQoderError(err) {
		state.Disabled = true
		if saveErr := saveQoderState(file, state); saveErr != nil {
			return saveErr
		}
	}
	return err
}

func (h *handler) flushPending(ctx context.Context, file, sessionID string, state *qoderState, limit int) (int, error) {
	sent := 0
	for ; sent < limit; sent++ {
		item, ok := state.nextActivity()
		if !ok {
			break
		}
		err := h.client.ReportActivity(ctx, state.TaskUUID, "", taskskillclient.Activity{
			ExecutionIndex: state.ExecutionIndex, EventID: item.ID, Type: item.Type, Content: item.Content, Tool: "qoder_ide", ThreadID: sessionID,
		})
		if err != nil {
			if terminalQoderError(err) {
				state.Disabled = true
				if saveErr := saveQoderState(file, state); saveErr != nil {
					return sent, saveErr
				}
			}
			// 暂时失败不耗尽事件的重试资格；下次 Hook 复用原 ID，当前立即停止。
			return sent, err
		}
		state.acknowledgeActivity()
		if err := saveQoderState(file, state); err != nil {
			return sent + 1, err
		}
	}
	return sent, nil
}

func (h *handler) verifyTask(ctx context.Context, binding qoderBinding, cwd string) error {
	task, err := h.client.ReadTask(ctx, binding.TaskUUID, "")
	if err != nil {
		return err
	}
	if task.Mode != "vibe_coding" || task.Tool != "qoder_ide" || task.Index != binding.ExecutionIndex {
		return errors.New("Qoder task binding is stale or uses another execution tool")
	}
	for _, dir := range task.Dirs {
		if sameQoderDirectory(cwd, dir) {
			return nil
		}
	}
	return errors.New("Qoder workspace differs from the task; open the task workspace and resend")
}
func sameQoderDirectory(left, right string) bool {
	l, e1 := os.Stat(left)
	r, e2 := os.Stat(right)
	return e1 == nil && e2 == nil && l.IsDir() && r.IsDir() && os.SameFile(l, r)
}
func terminalQoderError(err error) bool {
	return taskskillclient.HasCode(err, "conflict", "task_not_found", "forbidden", "bind_rejected")
}
