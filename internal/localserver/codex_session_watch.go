package localserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

const (
	codexDesktopOriginator          = "Codex Desktop"
	codexDesktopSessionWatchTimeout = 90 * time.Second
	codexDesktopSessionWatchEvery   = 400 * time.Millisecond
	codexDesktopSessionWatchSkew    = 2 * time.Second
	codexDesktopSessionHeaderBytes  = 8192
	codexDesktopSessionScanBytes    = 8 << 20
)

var (
	codexSessionIDPattern     = regexp.MustCompile(`"session_id":"([0-9a-fA-F-]{36})"`)
	codexOriginatorPattern    = regexp.MustCompile(`"originator":"([^"]*)"`)
	codexSessionTimePattern   = regexp.MustCompile(`"timestamp":"([^"]+)"`)
	codexSessionMetaTypeCheck = `"type":"session_meta"`
)

// codexDesktopSessionWatch is one in-flight bind attempt for a task.
type codexDesktopSessionWatch struct {
	cancel context.CancelFunc
}

// watchCodexDesktopSession starts a short watch for the Codex app thread
// opened by the following deep link. It returns before the app creates the
// session. The watch never picks the newest file: the session must be created
// after this call, come from Codex Desktop, and contain this task UUID.
func (h *TasksHandler) watchCodexDesktopSession(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	var executionMode, executionTool, sessionJSON string
	var executionIndex int
	err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT execution_mode, execution_tool, vibe_coding_session_json, execution_index
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&executionMode, &executionTool, &sessionJSON, &executionIndex)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if executionMode != taskruntime.ExecutionModeVibeCoding || executionTool != taskruntime.ExecutionToolCodex {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
		return
	}
	if _, bound := validVibeCodingSession(sessionJSON, executionTool); bound {
		c.JSON(http.StatusOK, gin.H{"watching": false, "bound": true})
		return
	}
	h.startCodexDesktopSessionWatch(taskUUID, executionIndex)
	c.JSON(http.StatusAccepted, gin.H{"watching": true})
}

func (h *TasksHandler) startCodexDesktopSessionWatch(taskUUID string, executionIndex int) {
	h.codexWatchMu.Lock()
	defer h.codexWatchMu.Unlock()
	if h.codexWatches == nil {
		h.codexWatches = map[string]*codexDesktopSessionWatch{}
	}
	if current := h.codexWatches[taskUUID]; current != nil {
		current.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexDesktopSessionWatchTimeout)
	watch := &codexDesktopSessionWatch{cancel: cancel}
	h.codexWatches[taskUUID] = watch
	started := time.Now()
	go func() {
		defer func() {
			cancel()
			h.codexWatchMu.Lock()
			if h.codexWatches[taskUUID] == watch {
				delete(h.codexWatches, taskUUID)
			}
			h.codexWatchMu.Unlock()
		}()
		h.watchCodexDesktopSessionFiles(ctx, taskUUID, executionIndex, started)
	}()
}

func (h *TasksHandler) watchCodexDesktopSessionFiles(ctx context.Context, taskUUID string, executionIndex int, started time.Time) {
	notBefore := started.Add(-codexDesktopSessionWatchSkew)
	ticker := time.NewTicker(codexDesktopSessionWatchEvery)
	defer ticker.Stop()
	seenSize := map[string]int64{}
	for {
		if ctx.Err() != nil {
			return
		}
		threadID, ok := findCodexDesktopSession(taskUUID, notBefore, seenSize)
		if ok {
			done := h.bindDiscoveredCodexSession(ctx, taskUUID, executionIndex, threadID, started)
			if done {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *TasksHandler) bindDiscoveredCodexSession(ctx context.Context, taskUUID string, executionIndex int, threadID string, started time.Time) bool {
	if ctx.Err() != nil {
		return true
	}
	var release func()
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			return false
		}
		release = lease.Close
	}
	if release != nil {
		defer release()
	}
	if ctx.Err() != nil {
		return true
	}
	tx, err := h.taskDB().BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var executionMode, executionTool, sessionJSON string
	var currentIndex int
	if err = tx.QueryRowContext(ctx, `SELECT execution_mode, execution_tool, vibe_coding_session_json, execution_index
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&executionMode, &executionTool, &sessionJSON, &currentIndex); err != nil {
		return false
	}
	if executionMode != taskruntime.ExecutionModeVibeCoding || executionTool != taskruntime.ExecutionToolCodex || currentIndex != executionIndex {
		return true
	}
	if _, bound := validVibeCodingSession(sessionJSON, executionTool); bound {
		return true
	}
	if status, err := remoteExecutionBlock(ctx, tx, taskUUID); err != nil || status != "" {
		return false
	}
	normalized, ok := normalizeCodexThreadID(threadID)
	if !ok {
		return true
	}
	payload, err := json.Marshal(vibeCodingSession{Type: taskruntime.ExecutionToolCodex, ThreadID: normalized})
	if err != nil {
		return false
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET vibe_coding_session_json=?, updated_at=?
		WHERE uuid=? AND execution_mode='vibe_coding' AND execution_tool=? AND execution_index=?
		AND (vibe_coding_session_json IS NULL OR vibe_coding_session_json IN ('', '{}'))`,
		string(payload), now, taskUUID, taskruntime.ExecutionToolCodex, executionIndex)
	if err != nil {
		return false
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false
	}
	if rows != 1 {
		return true
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_progress SET external_session_id=?
		WHERE task_uuid=? AND execution_mode='vibe_coding' AND cli_type=? AND external_session_id=''
		AND activity_kind IN (?, ?) AND created_at>=?`,
		normalized, taskUUID, taskruntime.ExecutionToolCodex,
		activityKindUserMessage, activityKindFinalResponse, started.UnixMilli()); err != nil {
		return false
	}
	if err = tx.Commit(); err != nil {
		return false
	}
	h.notifyTaskChanged(taskUUID)
	applog.Info("Codex 应用会话已绑定", "task_uuid", taskUUID, "thread_id", normalized)
	return true
}

func findCodexDesktopSession(taskUUID string, notBefore time.Time, seenSize map[string]int64) (string, bool) {
	for _, dir := range codexSessionDayDirs(time.Now()) {
		matches, err := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() || info.ModTime().Before(notBefore) {
				continue
			}
			if seenSize[path] == info.Size() {
				continue
			}
			threadID, matched := matchCodexDesktopSession(path, taskUUID, notBefore)
			if !matched {
				seenSize[path] = info.Size()
				continue
			}
			return threadID, true
		}
	}
	return "", false
}

func codexSessionRoots() []string {
	userHome, _ := os.UserHomeDir()
	return uniqueCodexSessionRoots(os.Getenv("CODEX_HOME"), userHome)
}

// uniqueCodexSessionRoots keeps the user-profile .codex/sessions directory even
// when CODEX_HOME is set. The Windows Codex app writes there and ignores
// CODEX_HOME; the CLI and macOS app use CODEX_HOME when it is set.
func uniqueCodexSessionRoots(codexHome, userHome string) []string {
	var roots []string
	add := func(root string) {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." {
			return
		}
		for _, existing := range roots {
			if strings.EqualFold(existing, root) {
				return
			}
		}
		roots = append(roots, root)
	}
	if home := strings.TrimSpace(userHome); home != "" {
		add(filepath.Join(home, ".codex", "sessions"))
	}
	if home := strings.TrimSpace(codexHome); home != "" {
		add(filepath.Join(home, "sessions"))
	}
	return roots
}

func codexSessionDayDirs(now time.Time) []string {
	seen := map[string]struct{}{}
	dirs := make([]string, 0, 8)
	for _, root := range codexSessionRoots() {
		for _, moment := range []time.Time{now, now.Add(-24 * time.Hour), now.UTC(), now.UTC().Add(-24 * time.Hour)} {
			dir := filepath.Join(root, moment.Format("2006"), moment.Format("01"), moment.Format("02"))
			key := strings.ToLower(dir)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func matchCodexDesktopSession(path, taskUUID string, notBefore time.Time) (string, bool) {
	header, ok := readCodexSessionHeader(path)
	if !ok || header.originator != codexDesktopOriginator || header.created.Before(notBefore) {
		return "", false
	}
	found, err := fileContainsString(path, taskUUID, codexDesktopSessionScanBytes)
	if err != nil || !found {
		return "", false
	}
	return header.sessionID, true
}

type codexSessionHeader struct {
	sessionID  string
	originator string
	created    time.Time
}

func readCodexSessionHeader(path string) (codexSessionHeader, bool) {
	file, err := os.Open(path)
	if err != nil {
		return codexSessionHeader{}, false
	}
	defer file.Close()
	buf := make([]byte, codexDesktopSessionHeaderBytes)
	n, err := file.Read(buf)
	if n == 0 || (err != nil && err != io.EOF) {
		return codexSessionHeader{}, false
	}
	text := string(buf[:n])
	if !strings.Contains(text, codexSessionMetaTypeCheck) {
		return codexSessionHeader{}, false
	}
	idMatch := codexSessionIDPattern.FindStringSubmatch(text)
	originMatch := codexOriginatorPattern.FindStringSubmatch(text)
	timeMatch := codexSessionTimePattern.FindStringSubmatch(text)
	if idMatch == nil || originMatch == nil || timeMatch == nil {
		return codexSessionHeader{}, false
	}
	created, err := time.Parse(time.RFC3339Nano, timeMatch[1])
	if err != nil {
		return codexSessionHeader{}, false
	}
	sessionID, ok := normalizeCodexThreadID(idMatch[1])
	if !ok {
		return codexSessionHeader{}, false
	}
	return codexSessionHeader{sessionID: sessionID, originator: originMatch[1], created: created}, true
}

func fileContainsString(path, needle string, limit int64) (bool, error) {
	if needle == "" {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	target := []byte(needle)
	overlap := len(target) - 1
	buf := make([]byte, 64*1024)
	var tail []byte
	var read int64
	for read < limit {
		n, err := file.Read(buf)
		if n > 0 {
			chunk := append(tail, buf[:n]...)
			if bytes.Contains(chunk, target) {
				return true, nil
			}
			if overlap > 0 && len(chunk) > overlap {
				tail = append([]byte(nil), chunk[len(chunk)-overlap:]...)
			} else {
				tail = append([]byte(nil), chunk...)
			}
			read += int64(n)
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}
