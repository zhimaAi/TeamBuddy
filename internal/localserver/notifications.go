package localserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/cloud"
	"goteams-client/internal/i18n"
)

type NotificationsHandler struct {
	db          func() *sql.DB
	cloudClient func() *cloud.Client
}

func NewNotificationsHandler(db func() *sql.DB, clients ...func() *cloud.Client) *NotificationsHandler {
	h := &NotificationsHandler{db: db}
	if len(clients) > 0 {
		h.cloudClient = clients[0]
	}
	return h
}
func (h *NotificationsHandler) client() *cloud.Client {
	if h.cloudClient == nil {
		return nil
	}
	return h.cloudClient()
}

func (h *NotificationsHandler) RegisterRoutes(r *gin.RouterGroup) {
	r.Use(h.requireScope())
	r.GET("", h.list)
	r.PUT("/:uuid/read", h.read)
	r.PUT("/tasks/:task_uuid/read", h.readTask)
	r.PUT("/tasks/:task_uuid/archive", h.archiveTask)
}

func (h *NotificationsHandler) list(c *gin.Context) {
	includeArchived := c.Query("include_archived") == "1" || c.Query("include_archived") == "true" || c.Query("all") == "1"
	query := `SELECT n.uuid, n.task_uuid, t.title, t.status, n.task_step_uuid,
		COALESCE(s.name, ''), n.step_order, n.progress_uuid, n.terminal_status, n.summary, n.is_read, n.is_archived, n.created_at,
		t.execution_mode, t.execution_tool, COALESCE(t.work_dir, ''), COALESCE(t.project_uuid, '')
		FROM gt_task_notifications n JOIN gt_tasks t ON t.uuid = n.task_uuid
		LEFT JOIN gt_task_steps s ON s.uuid = n.task_step_uuid`
	scopeSQL, args := taskScopeSQL(h.client(), "t")
	query += " WHERE " + scopeSQL
	if !includeArchived {
		query += " AND n.is_archived = 0"
	}
	query += ` ORDER BY n.created_at DESC`

	rows, err := h.db().QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, taskID, taskTitle, taskStatus, stepID, stepName, progressID, status, summary, executionMode, executionTool string
		var workDir, projectUUID string
		var stepOrder int
		var isRead, isArchived bool
		var createdAt int64
		if err := rows.Scan(&id, &taskID, &taskTitle, &taskStatus, &stepID, &stepName, &stepOrder, &progressID, &status, &summary, &isRead, &isArchived, &createdAt,
			&executionMode, &executionTool, &workDir, &projectUUID); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		items = append(items, gin.H{"uuid": id, "task_uuid": taskID, "task_title": taskTitle, "task_status": taskStatus,
			"task_step_uuid": stepID, "step_name": stepName, "step_sort_order": stepOrder,
			"progress_uuid": progressID, "status": status, "summary": summary, "is_read": isRead, "is_archived": isArchived, "created_at": createdAt,
			"execution_mode": executionMode, "execution_tool": executionTool, "work_dir": workDir, "project_uuid": projectUUID})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *NotificationsHandler) read(c *gin.Context) {
	now := time.Now().UnixMilli()
	result, err := h.db().ExecContext(c.Request.Context(), `UPDATE gt_task_notifications SET is_read = 1, read_at = ? WHERE uuid = ?`, now, c.Param("uuid"))
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		i18n.LocalServerError(c, http.StatusNotFound, errors.New("通知不存在"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func markTaskNotificationsRead(ctx context.Context, db *sql.DB, taskUUID string, isRead bool) error {
	now := time.Now().UnixMilli()
	readAt := int64(0)
	if isRead {
		readAt = now
	}
	_, err := db.ExecContext(ctx, `UPDATE gt_task_notifications SET is_read = ?, read_at = ? WHERE task_uuid = ?`, isRead, readAt, taskUUID)
	return err
}

func (h *NotificationsHandler) readTask(c *gin.Context) {
	var body struct {
		IsRead bool `json:"is_read"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if err := markTaskNotificationsRead(c.Request.Context(), h.db(), c.Param("task_uuid"), body.IsRead); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "is_read": body.IsRead})
}

func (h *NotificationsHandler) archiveTask(c *gin.Context) {
	var body struct {
		IsArchived bool `json:"is_archived"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	result, err := h.db().ExecContext(c.Request.Context(), `UPDATE gt_task_notifications SET is_archived = ? WHERE task_uuid = ?`, body.IsArchived, c.Param("task_uuid"))
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		var exists int
		if err = h.db().QueryRowContext(c.Request.Context(), `SELECT COUNT(*) FROM gt_tasks WHERE uuid = ?`, c.Param("task_uuid")).Scan(&exists); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		if exists == 0 {
			i18n.LocalServerError(c, http.StatusNotFound, errors.New("任务不存在"))
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "is_archived": body.IsArchived})
}

func (h *NotificationsHandler) requireScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("task_uuid")
		query := "SELECT COUNT(*) FROM gt_tasks t WHERE t.uuid=? AND "
		if id == "" {
			id = c.Param("uuid")
			query = "SELECT COUNT(*) FROM gt_tasks t JOIN gt_task_notifications n ON n.task_uuid=t.uuid WHERE n.uuid=? AND "
		}
		if id == "" {
			c.Next()
			return
		}
		scopeSQL, args := taskScopeSQL(h.client(), "t")
		var count int
		err := h.db().QueryRowContext(c.Request.Context(), query+scopeSQL, append([]any{id}, args...)...).Scan(&count)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			c.Abort()
			return
		}
		if count == 0 {
			i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
			c.Abort()
			return
		}
		c.Next()
	}
}

// UnreadCount uses the same task scope as notification lists and mutations.
func (h *NotificationsHandler) UnreadCount(ctx context.Context) (int, error) {
	scopeSQL, args := taskScopeSQL(h.client(), "t")
	var count int
	err := h.db().QueryRowContext(ctx, "SELECT COUNT(*) FROM gt_task_notifications n JOIN gt_tasks t ON t.uuid=n.task_uuid WHERE n.is_archived=0 AND n.is_read=0 AND "+scopeSQL, args...).Scan(&count)
	return count, err
}
