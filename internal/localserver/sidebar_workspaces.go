package localserver

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

type sidebarWorkspace struct {
	DirectoryKey string `json:"directory_key"`
	WorkDir      string `json:"work_dir"`
	DisplayName  string `json:"display_name"`
	PinnedAt     int64  `json:"pinned_at"`
}

// 不要求目录仍存在；目录丢失后也应允许管理和删除本地任务。空目录不得批量删除。
func workspaceDirectoryKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(strings.ToLower(path), `\\?\unc\`) {
			path = `\\` + path[8:]
		} else {
			path = strings.TrimPrefix(path, `\\?\`)
		}
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (h *TasksHandler) listSidebarWorkspaces(c *gin.Context) {
	rows, err := h.taskDB().QueryContext(c.Request.Context(), `SELECT directory_key,work_dir,display_name,pinned_at FROM gt_sidebar_workspaces`)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	defer rows.Close()
	items := []sidebarWorkspace{}
	for rows.Next() {
		var item sidebarWorkspace
		if err := rows.Scan(&item.DirectoryKey, &item.WorkDir, &item.DisplayName, &item.PinnedAt); err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

func (h *TasksHandler) updateSidebarWorkspace(c *gin.Context) {
	var input struct {
		WorkDir     string  `json:"work_dir"`
		CreateOnly  bool    `json:"create_only"`
		DisplayName *string `json:"display_name"`
		Pinned      *bool   `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.Error(c, 400, "common_request_invalid", "")
		return
	}
	key := workspaceDirectoryKey(input.WorkDir)
	if key == "" || (input.DisplayName == nil && input.Pinned == nil) {
		i18n.Error(c, 400, "common_request_invalid", "")
		return
	}
	if input.DisplayName != nil {
		name := strings.TrimSpace(*input.DisplayName)
		if strings.ContainsAny(name, "\r\n") || utf8.RuneCountInString(name) > 200 {
			i18n.Error(c, 400, "localserver_workspace_name_invalid", "")
			return
		}
		input.DisplayName = &name
	}
	if input.CreateOnly {
		directory, err := normalizeExistingDirectory(input.WorkDir)
		if err != nil {
			i18n.LocalServerError(c, 400, err)
			return
		}
		input.WorkDir = directory
	}
	var name any
	var pinned any
	if input.DisplayName != nil {
		name = *input.DisplayName
	}
	if input.Pinned != nil {
		pinned = int64(0)
		if *input.Pinned {
			pinned = time.Now().UnixMilli()
		}
	}
	_, err := h.taskDB().ExecContext(c.Request.Context(), `INSERT INTO gt_sidebar_workspaces(directory_key,work_dir,display_name,pinned_at)
 VALUES(?,?,COALESCE(?,''),COALESCE(?,0)) ON CONFLICT(directory_key) DO UPDATE SET
 work_dir=excluded.work_dir,display_name=COALESCE(?,display_name),pinned_at=COALESCE(?,pinned_at) WHERE ?=0`, key, strings.TrimSpace(input.WorkDir), name, pinned, name, pinned, input.CreateOnly)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	var item sidebarWorkspace
	err = h.taskDB().QueryRowContext(c.Request.Context(), `SELECT directory_key,work_dir,display_name,pinned_at FROM gt_sidebar_workspaces WHERE directory_key=?`, key).Scan(&item.DirectoryKey, &item.WorkDir, &item.DisplayName, &item.PinnedAt)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	c.JSON(200, item)
}

type workspaceDeletionTask struct {
	UUID     string `json:"uuid"`
	Title    string `json:"title"`
	Archived bool   `json:"is_archived"`
	TaskDir  string `json:"-"`
}

// 仅删除明确目录的侧栏设置；空任务集合仍不能进入批量任务删除接口。
func (h *TasksHandler) deleteSidebarWorkspace(c *gin.Context) {
	input, key, ok := readWorkspaceDeletion(c)
	if !ok {
		return
	}
	if len(input.TaskUUIDs) > 0 {
		i18n.Error(c, 400, "common_request_invalid", "")
		return
	}
	if _, err := h.taskDB().ExecContext(c.Request.Context(), `DELETE FROM gt_sidebar_workspaces WHERE directory_key=?`, key); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"deleted_task_uuids": []string{}, "cleanup_failed_task_uuids": []string{}})
}

type workspaceDeletionInput struct {
	WorkDir   string   `json:"work_dir"`
	TaskUUIDs []string `json:"task_uuids"`
}

func readWorkspaceDeletion(c *gin.Context) (workspaceDeletionInput, string, bool) {
	var input workspaceDeletionInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024*1024)
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.Error(c, 400, "common_request_invalid", "")
		return input, "", false
	}
	key := workspaceDirectoryKey(input.WorkDir)
	if key == "" {
		i18n.Error(c, 400, "common_request_invalid", "")
		return input, "", false
	}
	return input, key, true
}

func (h *TasksHandler) previewWorkspaceDeletion(c *gin.Context) {
	input, key, ok := readWorkspaceDeletion(c)
	if !ok {
		return
	}
	// 从任务表查完整范围，不受通知、搜索、归档或附加目录影响。
	scopeSQL, scopeArgs := taskScopeSQL(h.taskCloudClient(), "t")
	rows, err := h.taskDB().QueryContext(c.Request.Context(), `SELECT uuid,title,work_dir,
 EXISTS(SELECT 1 FROM gt_task_notifications n WHERE n.task_uuid=t.uuid AND n.is_archived=1) FROM gt_tasks t WHERE `+scopeSQL+` ORDER BY uuid`, scopeArgs...)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	defer rows.Close()
	items := []workspaceDeletionTask{}
	for rows.Next() {
		var item workspaceDeletionTask
		var workDir string
		if err := rows.Scan(&item.UUID, &item.Title, &workDir, &item.Archived); err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		if workspaceDirectoryKey(workDir) == key {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"work_dir": input.WorkDir, "items": items})
}

func (h *TasksHandler) deleteWorkspaceTasks(c *gin.Context) {
	input, key, ok := readWorkspaceDeletion(c)
	if !ok {
		return
	}
	if len(input.TaskUUIDs) == 0 {
		i18n.Error(c, 400, "common_request_invalid", "")
		return
	}
	seen := map[string]bool{}
	for _, id := range input.TaskUUIDs {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			i18n.Error(c, 400, "common_request_invalid", "")
			return
		}
		seen[id] = true
	}
	// 固定顺序获取执行门闩，阻止检查后启动，也避免两次批量操作逆序持锁。
	sort.Strings(input.TaskUUIDs)
	if orchestrator := h.orchestrator(); orchestrator != nil {
		for _, id := range input.TaskUUIDs {
			lease, err := orchestrator.BeginTaskExecutionSwitch(id)
			if err != nil {
				i18n.LocalServerError(c, 500, err)
				return
			}
			defer lease.Close()
		}
	}
	for _, id := range input.TaskUUIDs {
		if !guardRemoteExecutionMutation(c, h.taskDB(), id) {
			return
		}
	}
	tx, err := h.taskDB().BeginTx(c.Request.Context(), nil)
	if err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	defer tx.Rollback()
	tasks := []workspaceDeletionTask{}
	scopeSQL, scopeArgs := taskScopeSQL(h.taskCloudClient(), "t")
	for _, id := range input.TaskUUIDs {
		var item workspaceDeletionTask
		var workDir string
		var active int
		item.UUID = id
		err := tx.QueryRowContext(c.Request.Context(), `SELECT work_dir,task_dir,
 (SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=t.uuid AND status IN ('created','running','waiting_input','stop_requested')) FROM gt_tasks t WHERE uuid=? AND `+scopeSQL, append([]any{id}, scopeArgs...)...).Scan(&workDir, &item.TaskDir, &active)
		if err == sql.ErrNoRows || (err == nil && workspaceDirectoryKey(workDir) != key) {
			i18n.Error(c, 409, "localserver_workspace_tasks_changed", "workspace_tasks_changed")
			return
		}
		if err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		if active > 0 {
			i18n.Error(c, 409, "localserver_active_sessions", "task_has_active_sessions")
			return
		}
		tasks = append(tasks, item)
	}
	var sessionUUIDs []string
	for _, item := range tasks {
		ids, err := deleteTaskRows(c.Request.Context(), tx, item.UUID)
		if err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
		sessionUUIDs = append(sessionUUIDs, ids...)
		if _, err := tx.ExecContext(c.Request.Context(), `DELETE FROM gt_tasks WHERE uuid=?`, item.UUID); err != nil {
			i18n.LocalServerError(c, 500, err)
			return
		}
	}
	if _, err := tx.ExecContext(c.Request.Context(), `DELETE FROM gt_sidebar_workspaces WHERE directory_key=?`, key); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	if err := tx.Commit(); err != nil {
		i18n.LocalServerError(c, 500, err)
		return
	}
	taskruntime.RemoveSessionArtifacts(sessionUUIDs)
	cleanupFailed := []string{}
	for _, item := range tasks {
		if !h.cleanupTaskDirectory(item.TaskDir) {
			cleanupFailed = append(cleanupFailed, item.UUID)
		}
		h.deleteTaskSync(item.UUID)
		if h.wsHub != nil {
			h.wsHub.BroadcastToAll("task.changed", gin.H{"task_uuid": item.UUID, "deleted": true})
		}
	}
	response := gin.H{"deleted_task_uuids": input.TaskUUIDs, "cleanup_failed_task_uuids": cleanupFailed}
	if len(cleanupFailed) > 0 {
		response["warning"] = i18n.T(c, "localserver_task_cleanup_failed")
	}
	c.JSON(200, response)
}
