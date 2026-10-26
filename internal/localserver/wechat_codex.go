package localserver

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"goteams-client/internal/applog"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

type codexRPC struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  *bufio.Scanner
	events []map[string]json.RawMessage
}

const wechatCodexRecoveryMaxFailures = 5

var errCodexBusy = errors.New("Codex 会话正在执行中")
var errCodexOutcomeUnknown = errors.New("Codex 指令下发结果不确定")

type codexRPCRemoteError struct{ detail string }

func (e *codexRPCRemoteError) Error() string { return "Codex App Server 拒绝请求: " + e.detail }

// This error means the App Server process never started.
type codexAppServerStartError struct{ cause error }

func (e *codexAppServerStartError) Error() string { return e.cause.Error() }
func (e *codexAppServerStartError) Unwrap() error { return e.cause }

func startCodexRPC(ctx context.Context) (*codexRPC, error) {
	path, err := executor.ResolveCLIExecutable(executor.CLITypeCodex)
	if err != nil {
		return nil, &codexAppServerStartError{cause: &wechatDispatchFailure{
			category: "runtime", stage: "codex_executable", key: "remote_wechat_codex_executable_unavailable"}}
	}
	cmd := exec.CommandContext(ctx, path, "app-server", "--stdio")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, &codexAppServerStartError{cause: err}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, &codexAppServerStartError{cause: err}
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, &codexAppServerStartError{cause: err}
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	rpc := &codexRPC{cmd: cmd, in: in, lines: scanner}
	if _, err = rpc.request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "teambuddy-wechat", "version": "0.1.5"}}); err != nil {
		rpc.close()
		return nil, err
	}
	if err = rpc.write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		rpc.close()
		return nil, err
	}
	return rpc, nil
}

func (r *codexRPC) close() {
	_ = r.in.Close()
	if r.cmd == nil {
		return
	}
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.cmd.Wait()
}

func (r *codexRPC) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = r.in.Write(data)
	return err
}

func (r *codexRPC) next() (map[string]json.RawMessage, error) {
	for r.lines.Scan() {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(r.lines.Bytes(), &message); err != nil {
			continue
		}
		return message, nil
	}
	if err := r.lines.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (r *codexRPC) request(id int, method string, params any) (map[string]json.RawMessage, error) {
	deadline := time.AfterFunc(15*time.Second, func() {
		if r.cmd != nil && r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
	})
	defer deadline.Stop()
	if err := r.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		message, err := r.next()
		if err != nil {
			return nil, err
		}
		var responseID int
		if json.Unmarshal(message["id"], &responseID) != nil {
			r.events = append(r.events, message)
			continue
		}
		if responseID != id {
			continue
		}
		if value := message["error"]; len(value) > 0 && string(value) != "null" {
			return nil, &codexRPCRemoteError{detail: string(value)}
		}
		var result map[string]json.RawMessage
		if err = json.Unmarshal(message["result"], &result); err != nil {
			return nil, err
		}
		return result, nil
	}
}

func parseCodexStatus(result map[string]json.RawMessage) string {
	var thread struct {
		Status struct {
			Type string `json:"type"`
		} `json:"status"`
	}
	_ = json.Unmarshal(result["thread"], &thread)
	return thread.Status.Type
}

func (w *wechatRemote) dispatchCodex(ctx context.Context, task wechatTask, messageID, content string) (string, error) {
	if w.orchestrator == nil {
		return "", errors.New("任务执行服务不可用")
	}
	lease, err := w.orchestrator.BeginTaskExecution(task.UUID)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	// The task may have switched mode while this message waited for the gate.
	task, err = w.taskByUUID(ctx, task.UUID)
	if err != nil {
		return "", err
	}
	if task.Mode != taskruntime.ExecutionModeVibeCoding || task.Tool != taskruntime.ExecutionToolCodex {
		return "", errors.New("任务执行方式已变更")
	}
	var binding vibeCodingSession
	if json.Unmarshal([]byte(task.SessionJSON), &binding) != nil || binding.Type != "codex" {
		return "", wechatConfigFailure("codex_binding", "remote_wechat_config_codex_binding")
	}
	if _, err := uuid.Parse(binding.ThreadID); err != nil {
		return "", wechatConfigFailure("codex_binding", "remote_wechat_config_codex_binding")
	}
	marked, err := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET task_uuid=?, updated_at=?
		WHERE message_id=? AND channel_type='wechat' AND status='processing'`, task.UUID, time.Now().UnixMilli(), messageID)
	if err != nil {
		return "", err
	}
	if count, err := marked.RowsAffected(); err != nil || count != 1 {
		return "", errors.New("微信指令已取消")
	}
	rootCtx := w.rootCtx
	if rootCtx == nil {
		rootCtx = ctx
	}
	rpc, err := startCodexRPC(rootCtx)
	if err != nil {
		var failure *wechatDispatchFailure
		if errors.As(err, &failure) {
			return "", err
		}
		return "", wechatRuntimeFailure("codex_start")
	}
	result, err := rpc.request(2, "thread/read", map[string]any{"threadId": binding.ThreadID, "includeTurns": false})
	if err != nil {
		rpc.close()
		return "", wechatRuntimeFailure("codex_thread_read")
	}
	if parseCodexStatus(result) == "active" {
		rpc.close()
		return "", errCodexBusy
	}
	if _, err = rpc.request(3, "thread/resume", map[string]any{"threadId": binding.ThreadID}); err != nil {
		rpc.close()
		if codexWriterConflict(err) {
			return "", &wechatDispatchFailure{category: "writer_locked", stage: "codex_thread_resume", key: "remote_wechat_codex_writer_locked"}
		}
		return "", wechatRuntimeFailure("codex_thread_resume")
	}
	result, err = rpc.request(4, "turn/start", map[string]any{"threadId": binding.ThreadID, "approvalPolicy": "never", "input": []any{map[string]string{"type": "text", "text": content}}})
	if err != nil {
		rpc.close()
		classified := classifyCodexTurnStartError(err)
		if errors.Is(classified, errCodexBusy) || errors.Is(classified, errCodexOutcomeUnknown) {
			return "", classified
		}
		return "", wechatRuntimeFailure("codex_turn_start")
	}
	var started struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err = json.Unmarshal(result["turn"], &started); err != nil || started.ID == "" {
		rpc.close()
		return "", errCodexOutcomeUnknown
	}
	w.mu.Lock()
	w.codexMonitors[messageID] = struct{}{}
	w.mu.Unlock()
	go w.monitorCodex(rpc, messageID, task, binding.ThreadID, started.ID)
	return "codex:" + binding.ThreadID + ":" + started.ID, nil
}

func codexWriterConflict(err error) bool {
	var remoteErr *codexRPCRemoteError
	return errors.As(err, &remoteErr) && strings.Contains(strings.ToLower(remoteErr.detail), "active writer")
}

func classifyCodexTurnStartError(err error) error {
	var remoteErr *codexRPCRemoteError
	if errors.As(err, &remoteErr) {
		if strings.Contains(remoteErr.detail, "active turn") {
			return errCodexBusy
		}
		return remoteErr
	}
	return fmt.Errorf("%w: %v", errCodexOutcomeUnknown, err)
}

func codexAgentText(message map[string]json.RawMessage) string {
	var method string
	_ = json.Unmarshal(message["method"], &method)
	if method != "item/completed" {
		return ""
	}
	var params struct {
		Item struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
	}
	if json.Unmarshal(message["params"], &params) != nil {
		return ""
	}
	if params.Item.Type == "agentMessage" && (params.Item.Phase == "final_answer" || params.Item.Phase == "") {
		return strings.TrimSpace(params.Item.Text)
	}
	return ""
}

func codexTurnTerminal(status string) bool {
	switch status {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func (w *wechatRemote) monitorCodex(rpc *codexRPC, messageID string, task wechatTask, threadID, turnID string) {
	defer func() {
		w.mu.Lock()
		delete(w.codexMonitors, messageID)
		w.mu.Unlock()
	}()
	defer rpc.close()
	var final string
	status := "failed"
	terminal := false
	events := rpc.events
	rpc.events = nil
	for {
		var message map[string]json.RawMessage
		var err error
		if len(events) > 0 {
			message = events[0]
			events = events[1:]
		} else {
			message, err = rpc.next()
		}
		if err != nil {
			break
		}
		if text := codexAgentText(message); text != "" {
			final = text
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		if method == "turn/completed" {
			var params struct {
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(message["params"], &params) == nil && params.Turn.ID == turnID {
				status = params.Turn.Status
				terminal = codexTurnTerminal(status)
				break
			}
		}
		if method == "item/tool/requestUserInput" {
			final = w.tr(context.Background(), "remote_wechat_codex_input", nil)
			_ = rpc.write(map[string]any{"id": 6, "method": "turn/interrupt", "params": map[string]string{"threadId": threadID, "turnId": turnID}})
		}
	}
	if w.rootCtx != nil && w.rootCtx.Err() != nil {
		return
	}
	if !terminal {
		if result, err := rpc.request(5, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}); err == nil {
			if recoveredStatus, recoveredFinal, found := codexTurnFromRead(result, turnID); found && codexTurnTerminal(recoveredStatus) {
				status, final = recoveredStatus, recoveredFinal
				terminal = true
			}
		}
	}
	if !terminal {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		var inboxStatus string
		err := w.db.QueryRowContext(ctx, `SELECT status FROM gt_remote_wechat_inbox WHERE message_id=? AND channel_type='wechat'`, messageID).Scan(&inboxStatus)
		if err == nil && inboxStatus == "dispatched" {
			break
		}
		if err == sql.ErrNoRows {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := w.finishCodexMessage(ctx, messageID, task, final, status); err != nil {
		applog.Warn("保存微信 Codex 回复失败", "error", err)
	}
}

func (w *wechatRemote) finishCodexMessage(ctx context.Context, messageID string, task wechatTask, final, status string) error {
	if final == "" {
		if status == "completed" {
			final = w.tr(ctx, "remote_wechat_codex_empty", nil)
		} else {
			final = w.tr(ctx, "remote_wechat_codex_failed", nil)
		}
	}
	body := w.tr(ctx, "remote_wechat_result", i18n.Params{"ID": task.ShortID, "Task": task.Title, "Result": final})
	return w.finishDispatched(ctx, messageID, task.UUID, body)
}

func (w *wechatRemote) recoverCodexResults(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `SELECT message_id,task_uuid,session_uuid FROM gt_remote_wechat_inbox
		WHERE channel_type='wechat' AND status='dispatched' AND session_uuid LIKE 'codex:%' AND received_at<? AND next_recovery_at<=?
		ORDER BY next_recovery_at, received_at LIMIT 8`, time.Now().Add(-time.Minute).UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	type pending struct{ id, task, session string }
	var items []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.task, &item.session); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	var firstErr error
	for _, item := range items {
		w.mu.Lock()
		_, monitored := w.codexMonitors[item.id]
		w.mu.Unlock()
		if monitored {
			if err := w.deferCodexRecovery(ctx, item.id, item.task, false); err != nil {
				return err
			}
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(item.session, "codex:"), ":", 2)
		if len(parts) != 2 {
			if err := w.finishDispatched(ctx, item.id, item.task, w.tr(ctx, "remote_wechat_result_unavailable", nil)); err != nil {
				return err
			}
			continue
		}
		task, taskErr := w.taskByUUID(ctx, item.task)
		if errors.Is(taskErr, sql.ErrNoRows) {
			if err := w.finishDispatched(ctx, item.id, item.task, w.tr(ctx, "remote_wechat_result_unavailable", nil)); err != nil {
				return err
			}
			continue
		}
		if taskErr != nil {
			return taskErr
		}
		rpc, rpcErr := startCodexRPC(ctx)
		if rpcErr != nil {
			if firstErr == nil {
				firstErr = rpcErr
			}
			if err := w.deferCodexRecovery(ctx, item.id, item.task, true); err != nil {
				return err
			}
			continue
		}
		result, readErr := rpc.request(2, "thread/read", map[string]any{"threadId": parts[0], "includeTurns": true})
		rpc.close()
		if readErr != nil {
			if firstErr == nil {
				firstErr = readErr
			}
			if err := w.deferCodexRecovery(ctx, item.id, item.task, true); err != nil {
				return err
			}
			continue
		}
		status, final, found := codexTurnFromRead(result, parts[1])
		countFailure := !found
		if found && codexTurnTerminal(status) {
			if err := w.finishCodexMessage(ctx, item.id, task, final, status); err == nil {
				continue
			} else if firstErr == nil {
				firstErr = err
			}
		}
		if err := w.deferCodexRecovery(ctx, item.id, item.task, countFailure); err != nil {
			return err
		}
	}
	return firstErr
}

func (w *wechatRemote) deferCodexRecovery(ctx context.Context, messageID, taskUUID string, countFailure bool) error {
	notice := w.tr(ctx, "remote_wechat_codex_recovery_uncertain", nil)
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var failures int
	if err = tx.QueryRowContext(ctx, `SELECT recovery_attempts FROM gt_remote_wechat_inbox
		WHERE message_id=? AND channel_type='wechat' AND status='dispatched'`, messageID).Scan(&failures); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if countFailure {
		failures++
	}
	if countFailure && failures >= wechatCodexRecoveryMaxFailures {
		result, updateErr := tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox
			SET status='uncertain', recovery_attempts=?, updated_at=? WHERE message_id=? AND channel_type='wechat' AND status='dispatched'`,
			failures, time.Now().UnixMilli(), messageID)
		if updateErr != nil {
			return updateErr
		}
		if updated, rowsErr := result.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if updated == 0 {
			return nil
		}
		if err = w.enqueueTx(ctx, tx, ChannelTypeWechat, "uncertain:"+messageID, taskUUID, notice); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox
			SET next_recovery_at=?, recovery_attempts=?, updated_at=? WHERE message_id=? AND channel_type='wechat' AND status='dispatched'`,
			time.Now().Add(2*time.Minute).UnixMilli(), failures, time.Now().UnixMilli(), messageID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func codexTurnFromRead(result map[string]json.RawMessage, turnID string) (status, final string, found bool) {
	var thread struct {
		Turns []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Items  []struct {
				Type  string `json:"type"`
				Phase string `json:"phase"`
				Text  string `json:"text"`
			} `json:"items"`
		} `json:"turns"`
	}
	if json.Unmarshal(result["thread"], &thread) != nil {
		return "", "", false
	}
	for _, turn := range thread.Turns {
		if turn.ID != turnID {
			continue
		}
		for _, event := range turn.Items {
			if event.Type == "agentMessage" && (event.Phase == "final_answer" || event.Phase == "") {
				final = strings.TrimSpace(event.Text)
			}
		}
		return turn.Status, final, true
	}
	return "", "", false
}
