package localserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloudsync"
	"goteams-client/internal/i18n"
)

// 执行记录上报（任务投影 + 步骤会话）是本地的 outbox 旁路通道。
// 这里只做两件事：把 outbox / 会话表里的上报状态暴露给界面，
// 以及接受一次手动重报。真正的推送仍由 SessionSyncService 与 Pusher 负责。

// taskSyncRetryTimeout 手动重报一次的最大等待时间，超时按失败返回当前状态。
const taskSyncRetryTimeout = 30 * time.Second

// taskSyncErrorMaxRunes 回传给界面的失败原因上限，避免超长堆栈撑爆提示。
const taskSyncErrorMaxRunes = 300

// 上报不可用的原因，供界面直接说明，避免用户点了重报才发现做不了。
const (
	taskSyncReasonNotCloud  = "not_cloud"
	taskSyncReasonLoggedOut = "logged_out"
	taskSyncReasonNotOwned  = "not_owned"
)

// taskSyncResponse 是对外的上报状态：任务级聚合状态 + 是否可用。
type taskSyncResponse struct {
	Enabled  bool `json:"enabled"`
	LoggedIn bool `json:"logged_in"`
	// Owned 表示该任务归属当前登录账号；false 时后端拒绝推送。
	Owned bool `json:"owned"`
	// Reason 为不可上报的原因（not_cloud / logged_out / not_owned），可用时为空。
	Reason string `json:"reason,omitempty"`
	cloudsync.TaskSyncStatus
	// RetryError 是本地化提示；RetryErrorDetail 保留后端真实失败原因，便于定位。
	RetryError       string `json:"retry_error,omitempty"`
	RetryErrorDetail string `json:"retry_error_detail,omitempty"`
}

func (h *TasksHandler) buildTaskSyncResponse(c *gin.Context, taskUUID string) (taskSyncResponse, error) {
	response := taskSyncResponse{TaskSyncStatus: cloudsync.TaskSyncStatus{Status: cloudsync.SyncStatusPending}}
	db := h.taskDB()
	if db == nil {
		return response, sql.ErrConnDone
	}
	status, err := cloudsync.LoadTaskSyncStatus(c.Request.Context(), db, taskUUID)
	if err != nil {
		return response, err
	}
	client := h.taskCloudClient()
	response.TaskSyncStatus = status
	response.LoggedIn = client != nil && client.UserID() != ""
	if response.LoggedIn && status.SourceType == "cloud" {
		owned, ownErr := cloudsync.TaskOwnedByClient(c.Request.Context(), db, client, taskUUID)
		if ownErr != nil {
			return response, ownErr
		}
		response.Owned = owned
	}
	switch {
	case status.SourceType != "cloud":
		response.Reason = taskSyncReasonNotCloud
	case !response.LoggedIn:
		response.Reason = taskSyncReasonLoggedOut
	case !response.Owned:
		response.Reason = taskSyncReasonNotOwned
	}
	response.Enabled = status.SourceType == "cloud"
	return response, nil
}

// taskSyncStatus GET /api/local/tasks/:uuid/sync-status
// 只读接口，供界面轮询展示「整个绑定是否已上报线上」。
func (h *TasksHandler) taskSyncStatus(c *gin.Context) {
	response, err := h.buildTaskSyncResponse(c, strings.TrimSpace(c.Param("uuid")))
	if errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// retryTaskSync POST /api/local/tasks/:uuid/sync
// 手动重新上报：清退避 + 强制新 revision 推一次，然后回传最新状态。
// 推送失败仍返回 200，让界面统一按 status=failed 展示并提供再次重报。
func (h *TasksHandler) retryTaskSync(c *gin.Context) {
	taskUUID := strings.TrimSpace(c.Param("uuid"))
	client, db := h.taskCloudClient(), h.taskDB()
	if client == nil || client.UserID() == "" {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_cloud_client_not_ready", "cloud_client_not_ready")
		return
	}
	if db == nil {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_database_not_ready", "database_not_ready")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), taskSyncRetryTimeout)
	defer cancel()
	if err := taskSyncPusher.RetryTaskSync(ctx, db, client, taskUUID); err != nil {
		// 界面已经按 status=failed 展示并提供再次重报；这里把真实原因一并带回去，
		// 不再只给一句通用文案——否则用户无法判断是账号不匹配、任务未关联还是网络问题。
		applog.Warn("[CloudSync] 手动重报执行记录失败", "task_uuid", taskUUID, "error", err.Error())
		response, buildErr := h.buildTaskSyncResponse(c, taskUUID)
		if buildErr != nil {
			i18n.Error(c, http.StatusBadRequest, "localserver_task_sync_unavailable", "task_sync_unavailable")
			return
		}
		response.RetryError = i18n.T(c, "localserver_task_sync_push_failed")
		response.RetryErrorDetail = truncateSyncError(err.Error())
		c.JSON(http.StatusOK, response)
		return
	}
	response, err := h.buildTaskSyncResponse(c, taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// truncateSyncError 按字符数截断，避免把超长错误原文整段塞进响应。
func truncateSyncError(value string) string {
	runes := []rune(value)
	if len(runes) <= taskSyncErrorMaxRunes {
		return value
	}
	return string(runes[:taskSyncErrorMaxRunes]) + "…"
}

// triggerTaskOwnerBackfill 登录（含重启后恢复登录态）时把本环境中归属缺失的历史任务
// 回填成当前账号。cloud_user_id 为空的任务既不会被轮询上报、也点不动重报，
// 必须在会话建立后立刻补上，否则线上会持续丢沟通记录。
func (s *Server) triggerTaskOwnerBackfill() {
	db, client := s.currentSessionDB(), s.currentCloudClient()
	if db == nil || client == nil || client.UserID() == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		updated, err := cloudsync.BackfillTaskOwner(ctx, db, client)
		if err != nil {
			applog.Warn("[CloudSync] 登录后回填任务归属失败", "error", err.Error())
			return
		}
		if updated > 0 {
			applog.Info("[CloudSync] 登录后已回填历史任务归属",
				"count", updated, "user_id", client.UserID())
		}
	}()
}
