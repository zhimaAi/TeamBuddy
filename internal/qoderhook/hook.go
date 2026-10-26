// Package qoderhook handles native Qoder IDE events independently of MCP.
package qoderhook

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"goteams-client/internal/taskskillclient"
)

type handler struct{ client taskClient }

// Run uses the shared loopback transport without starting the desktop client.
func Run(ctx context.Context, input io.Reader, output io.Writer) error {
	client := taskskillclient.New(2 * time.Second)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	root := filepath.Join(filepath.Dir(client.RuntimePath()), "qoder-ide")
	return (&handler{client: client}).handle(ctx, root, input, output)
}

func (h *handler) handle(ctx context.Context, root string, input io.Reader, output io.Writer) (hookErr error) {
	event, err := readEvent(input)
	if err != nil {
		return err
	}
	promptSaved := false
	rejectUnsavedPrompt := event.Name == "UserPromptSubmit"
	defer func() {
		// 输入未落盘时必须阻断 IDE，避免继续执行后丢失问题及对应答复。
		if hookErr != nil && rejectUnsavedPrompt && !promptSaved {
			hookErr = &qoderPromptRejectedError{hookErr}
		}
	}()
	if event.Name != "SessionStart" && event.Name != "UserPromptSubmit" && event.Name != "Stop" {
		return nil
	}
	if !qoderSessionPattern.MatchString(event.SessionID) || !filepath.IsAbs(event.CWD) {
		return errors.New("invalid Qoder session or directory")
	}
	var marker *qoderBinding
	if event.Name == "UserPromptSubmit" {
		marker, err = parseQoderMarker(event.Prompt)
		if err != nil {
			return err
		}
	}
	session, err := openSession(root, event.SessionID, marker != nil)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	defer session.release()
	file, state := session.file, session.state
	if state.Disabled {
		// 旧绑定只停用同步，普通聊天以非阻断错误告知；显式绑定请求仍拒绝，
		// 避免在失效会话中重新执行启动词或接管其他任务。
		rejectUnsavedPrompt = marker != nil
		return errors.New("Qoder task synchronization was disabled after a stale binding or deleted task")
	}
	if marker != nil && state.TaskUUID != "" && state.qoderBinding != *marker {
		return errors.New("Qoder session is already bound to another task or execution generation")
	}
	cwd, err := filepath.EvalSymlinks(event.CWD)
	if err != nil {
		return errors.New("Qoder workspace unavailable")
	}
	if state.TaskUUID == "" {
		if marker == nil {
			return nil
		}
		if err := h.verifyTask(ctx, *marker, cwd); err != nil {
			if terminalQoderError(err) {
				state.qoderBinding, state.CWD, state.Disabled = *marker, cwd, true
				if saveErr := saveQoderState(file, &state); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
		state.qoderBinding, state.CWD = *marker, cwd
		if err := saveQoderState(file, &state); err != nil {
			return err
		}
	}
	if !sameQoderDirectory(cwd, state.CWD) {
		return errors.New("Qoder workspace differs from the bound task; open the task workspace and resend")
	}
	activity, stopHash, err := prepareActivity(event, state, marker != nil)
	if err != nil {
		return err
	}
	remaining := maxReportsPerHook
	bound := false
	// 旧版满队列的答复必须先补传，后续问题不能越过应急答复槽。
	if event.Name == "UserPromptSubmit" && state.OverflowReply != nil {
		if err := h.bindState(ctx, file, event.SessionID, &state); err != nil {
			return err
		}
		bound = true
		sent, err := h.flushPending(ctx, file, event.SessionID, &state, remaining)
		remaining -= sent
		if err != nil {
			return err
		}
		if state.OverflowReply != nil {
			return errors.New("Qoder callback queue is full; restore TeamBuddy before continuing synchronization")
		}
	}
	for {
		next, accepted, err := state.enqueue(event, activity, stopHash)
		if err != nil {
			return err
		}
		if accepted {
			state = next
			break
		}
		if remaining == 0 || len(state.Pending) == 0 {
			return errors.New("Qoder callback queue is full; restore TeamBuddy before continuing synchronization")
		}
		if !bound {
			if err := h.bindState(ctx, file, event.SessionID, &state); err != nil {
				return err
			}
			bound = true
		}
		// 按原顺序逐条补传，空间足够后立即保存当前事件；所有补传共用八条预算。
		sent, err := h.flushPending(ctx, file, event.SessionID, &state, 1)
		remaining -= sent
		if err != nil {
			return err
		}
	}
	// Persist the ID and original text before sending. Ambiguous responses reuse it.
	if err := saveQoderState(file, &state); err != nil {
		return err
	}
	promptSaved = true
	if !bound {
		if err := h.bindState(ctx, file, event.SessionID, &state); err != nil {
			return err
		}
	}
	_, reportErr := h.flushPending(ctx, file, event.SessionID, &state, remaining)
	// 只有暂时的传输失败可降级；绑定失效或本地保存失败仍须返回错误。
	deferredReport := reportErr != nil && taskskillclient.HasCode(reportErr, "teambuddy_unavailable")
	if reportErr != nil && !deferredReport {
		return reportErr
	}
	if event.Name == "Stop" {
		return reportErr
	}
	// 非零退出码会使 IDE 丢弃 additionalContext；已落盘的暂时失败通过上下文告知。
	return writeContext(output, event, state, reportErr != nil)
}
