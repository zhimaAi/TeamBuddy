package localserver

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloudsync"
	"goteams-client/internal/i18n"
)

// 产出文档上报（客户端 → 云端）的本地接口与状态读取。
//
// 这是一个旁路通道：上报状态落在 gt_task_document_uploads，文件树接口只做只读合并，
// 上报本身由 cloudsync.DocumentSyncService 独立协程完成，不影响任务主链路。

const (
	// documentReuploadMaxPaths 一次手动重报最多处理的文件数
	documentReuploadMaxPaths = 50
	// documentStatusQueryMaxPaths 一次状态查询最多处理的文件数
	documentStatusQueryMaxPaths = 50
)

// documentUploadItem 是单文件状态查询的返回项。
type documentUploadItem struct {
	Path string `json:"path"`
	cloudsync.DocumentUploadState
}

// documentKick 触发某个任务的产出文档扫描与上报（异步、去抖，失败不影响调用方）。
func (h *TasksHandler) documentKick(taskUUID string) {
	if taskUUID == "" || !documentUploadsEnabled() {
		return
	}
	cloudsync.DefaultDocumentSyncService().Kick(taskUUID)
}

// documentUploadsEnabled 返回产出文档上报是否启用；关闭时文件树不返回上报状态。
func documentUploadsEnabled() bool {
	return cloudsync.DocumentUploadConfig().Enabled
}

// taskDocumentUploadEnabled 判断某个来源的任务是否参与产出文档上报。
// 只有 cloud 来源任务在云端有 pipeline_tasks 行，本地任务上报必然 404，因此不参与。
func (h *TasksHandler) taskDocumentUploadEnabled(sourceType string) bool {
	return documentUploadsEnabled() && sourceType == "cloud"
}

// loadDocumentUploadStates 读取任务下所有文件的上报状态，键为规范化后的相对路径。
// 未启用或任务无记录时返回空表（调用方据此不填 upload 字段）。
func (h *TasksHandler) loadDocumentUploadStates(ctx context.Context, taskUUID string) (map[string]cloudsync.DocumentUploadState, error) {
	states := make(map[string]cloudsync.DocumentUploadState)
	if !documentUploadsEnabled() {
		return states, nil
	}
	db := h.taskDB()
	if db == nil {
		return states, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT rel_path, status, attempts, last_error, uploaded_at,
		doc_key, remote_revision FROM gt_task_document_uploads WHERE task_uuid=?`, taskUUID)
	if err != nil {
		return states, err
	}
	defer rows.Close()
	for rows.Next() {
		var relPath, status, lastError, docKey string
		var attempts int
		var uploadedAt, revision int64
		if err := rows.Scan(&relPath, &status, &attempts, &lastError, &uploadedAt, &docKey, &revision); err != nil {
			return states, err
		}
		states[cloudsync.DocumentRelKey(relPath)] = cloudsync.DocumentUploadState{
			Status:         status,
			Attempts:       attempts,
			LastError:      lastError,
			UploadedAt:     uploadedAt,
			DocKey:         docKey,
			RemoteRevision: revision,
			Reuploadable:   documentReuploadable(status),
		}
	}
	return states, rows.Err()
}

// documentReuploadable 判断该状态下文件是否可以手动重报。
func documentReuploadable(status string) bool {
	switch status {
	case "synced", "failed", "pending":
		return true
	default:
		return false
	}
}

// markDocumentPending 手动保存文件后强制该文件重新上报：
// 清零重试计数与变更水位，让下一轮扫描按最新内容重新判定。
// 任务关闭「同步 Agent 产出」时不做任何标记：编辑保存不应触发自动上报。
func (h *TasksHandler) markDocumentPending(taskUUID, relPath string) {
	if !documentUploadsEnabled() {
		return
	}
	db := h.taskDB()
	if db == nil {
		return
	}
	if !cloudsync.TaskAutoUploadEnabled(context.Background(), db, taskUUID) {
		return
	}
	relPath = cloudsync.DocumentRelKey(relPath)
	if relPath == "" {
		return
	}
	if _, err := db.Exec(`UPDATE gt_task_document_uploads
		SET status='pending', attempts=0, next_attempt_at=0, last_error='', modified_at=0, updated_at=?
		WHERE task_uuid=? AND rel_path=?`, time.Now().UnixMilli(), taskUUID, relPath); err != nil {
		applog.Warn("[DocSync] 标记产出文档待上报失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
	}
}

// reuploadTaskDocuments 手动重报：POST /api/local/tasks/:uuid/files/reupload
// body: {"path": "a.md"} 或 {"paths": ["a.md", "b.md"]}（上限 50）
func (h *TasksHandler) reuploadTaskDocuments(c *gin.Context) {
	taskUUID := c.Param("uuid")
	if !documentUploadsEnabled() {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}
	var body struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	paths := make([]string, 0, len(body.Paths)+1)
	if strings.TrimSpace(body.Path) != "" {
		paths = append(paths, body.Path)
	}
	paths = append(paths, body.Paths...)
	if len(paths) == 0 {
		i18n.Error(c, http.StatusBadRequest, "localserver_path_required", "path_required")
		return
	}
	if len(paths) > documentReuploadMaxPaths {
		i18n.Errorf(c, http.StatusBadRequest, "localserver_reupload_limit", i18n.Params{"Max": documentReuploadMaxPaths})
		return
	}

	var taskDir, sourceType string
	err := h.taskDB().QueryRow(`SELECT COALESCE(task_dir,''), COALESCE(source_type,'') FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&taskDir, &sourceType)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if !h.taskDocumentUploadEnabled(sourceType) {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}

	service := cloudsync.DefaultDocumentSyncService()
	if service == nil || h.taskDB() == nil {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}

	queued := make([]string, 0, len(paths))
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		// 复用 GET/POST files/content 的安全边界语义（越界、符号链接一律拒绝）
		if _, err := h.safeTaskFilePath(taskDir, raw); err != nil {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
		if err := service.Requeue(c.Request.Context(), taskUUID, raw); err != nil {
			// 文件还没被扫描进上报范围（新生成的文件）：本次扫描会建立记录并排入队列
			applog.Info("[DocSync] 手动重报的文件尚未登记，转由扫描入队",
				"task_uuid", taskUUID, "rel_path", raw, "error", err.Error())
		}
		queued = append(queued, cloudsync.DocumentRelKey(raw))
	}
	// 自动同步开启时触发一次扫描 + 上报；关闭时只上报本次显式选择的文件，
	// 不能走 Kick（扫描会把任务目录下全部产出重新入队）。
	if cloudsync.TaskAutoUploadEnabled(c.Request.Context(), h.taskDB(), taskUUID) {
		h.documentKick(taskUUID)
	} else {
		service.KickUploadOnly(taskUUID)
	}

	response := gin.H{"queued": len(queued) > 0, "paths": queued}
	if len(queued) == 1 {
		response["doc_key"] = cloudsync.DocumentDocKey(queued[0])
	}
	c.JSON(http.StatusOK, response)
}

// reportTaskDocuments 任务级手动上报：POST /api/local/tasks/:uuid/files/report
// 忽略任务级「同步 Agent 产出」开关，把任务产出目录下命中规则的文件全部排入上报队列。
func (h *TasksHandler) reportTaskDocuments(c *gin.Context) {
	taskUUID := c.Param("uuid")
	if !documentUploadsEnabled() {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}
	var sourceType string
	if err := h.taskDB().QueryRow(`SELECT COALESCE(source_type,'') FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&sourceType); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if !h.taskDocumentUploadEnabled(sourceType) {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}
	service := cloudsync.DefaultDocumentSyncService()
	if service == nil {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_document_upload_disabled", "document_upload_disabled")
		return
	}
	queued, err := service.ReportNow(c.Request.Context(), taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	applog.Info("[DocSync] 任务产出手动上报已入队", "task_uuid", taskUUID, "queued", queued)
	c.JSON(http.StatusOK, gin.H{"queued": queued > 0, "count": queued})
}

// taskDocumentUploadStatus 单/多文件上报状态查询：
// GET /api/local/tasks/:uuid/files/upload-status?paths=a.md,b.md
func (h *TasksHandler) taskDocumentUploadStatus(c *gin.Context) {
	taskUUID := c.Param("uuid")
	if !documentUploadsEnabled() {
		c.JSON(http.StatusOK, gin.H{"items": []documentUploadItem{}, "enabled": false})
		return
	}
	var sourceType string
	if err := h.taskDB().QueryRow(`SELECT COALESCE(source_type,'') FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&sourceType); err != nil && err != sql.ErrNoRows {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if !h.taskDocumentUploadEnabled(sourceType) {
		// 本地任务不参与上报：返回空集并置 enabled=false，前端据此停止轮询
		c.JSON(http.StatusOK, gin.H{"items": []documentUploadItem{}, "enabled": false})
		return
	}
	rawPaths := strings.Split(c.Query("paths"), ",")
	items := make([]documentUploadItem, 0, len(rawPaths))
	states, err := h.loadDocumentUploadStates(c.Request.Context(), taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	for _, raw := range rawPaths {
		relPath := cloudsync.DocumentRelKey(raw)
		if relPath == "" || len(items) >= documentStatusQueryMaxPaths {
			continue
		}
		item := documentUploadItem{Path: relPath}
		if state, ok := states[relPath]; ok {
			item.DocumentUploadState = state
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "enabled": true})
}
