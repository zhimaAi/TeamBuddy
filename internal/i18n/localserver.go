package i18n

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/expertgroup"
	"goteams-client/internal/pipeline"
	"goteams-client/internal/project"
	"goteams-client/internal/taskruntime"
)

// Error writes a localized localserver error without exposing internal error details.
func LocalServerError(c *gin.Context, status int, err error) {
	key, code := Key(err)
	if key == "localserver_internal_error" {
		key = fallbackKeyForStatus(status)
	}
	LocalServerKeyError(c, status, key, code)
}

// LocalServerKeyError writes a localserver error from a stable translation key.
func LocalServerKeyError(c *gin.Context, status int, key, code string) {
	c.Header(headerContentLanguage, FromContext(c))
	payload := gin.H{"error": T(c, key)}
	if code != "" {
		payload["code"] = code
	}
	c.JSON(status, payload)
}

// LocalServerErrorWith preserves response fields such as task UUID while
// replacing the user-visible error with a localized stable message.
func LocalServerErrorWith(c *gin.Context, status int, err error, fields gin.H) {
	c.Header(headerContentLanguage, FromContext(c))
	key, code := Key(err)
	if key == "localserver_internal_error" {
		key = fallbackKeyForStatus(status)
	}
	fields["error"] = T(c, key)
	if code != "" {
		fields["code"] = code
	}
	c.JSON(status, fields)
}

// Key maps known domain errors and stable validation text to translation keys.
// Unknown errors intentionally use a generic message.
func Key(err error) (string, string) {
	if err == nil {
		return "localserver_internal_error", ""
	}
	switch {
	case errors.Is(err, project.ErrNotFound):
		return "localserver_project_not_found", "project_not_found"
	case errors.Is(err, project.ErrInUse):
		return "localserver_project_in_use", "project_in_use"
	case errors.Is(err, pipeline.ErrNotFound):
		return "localserver_pipeline_not_found", "pipeline_not_found"
	case errors.Is(err, pipeline.ErrStepNotFound):
		return "localserver_step_not_found", "step_not_found"
	case errors.Is(err, expertgroup.ErrNotFound):
		return "localserver_expert_group_not_found", "expert_group_not_found"
	case errors.Is(err, expertgroup.ErrMemberNotFound):
		return "localserver_expert_member_not_found", "expert_member_not_found"
	case errors.Is(err, expertgroup.ErrNotReady):
		return "localserver_expert_group_not_ready", "expert_group_not_ready"
	case errors.Is(err, expertgroup.ErrInvalidBatchInput):
		return "localserver_expert_group_invalid", "expert_group_invalid"
	case errors.Is(err, taskruntime.ErrNotFound):
		return "localserver_task_not_found", "task_not_found"
	case errors.Is(err, taskruntime.ErrVibeModelInvalid):
		return "localserver_vibe_model_invalid", "vibe_model_invalid"
	case errors.Is(err, taskruntime.ErrExecutionModeConflict):
		return "localserver_execution_mode_conflict", "execution_mode_conflict"
	case errors.Is(err, taskruntime.ErrExecutionModeUnsupported):
		return "localserver_execution_mode_unsupported", "execution_mode_unsupported"
	case strings.Contains(err.Error(), "图片大小不能超过"):
		return "localserver_icon_too_large", "icon_too_large"
	case strings.Contains(err.Error(), "仅支持 PNG"):
		return "localserver_icon_type_invalid", "icon_type_invalid"
	case strings.Contains(err.Error(), "本地任务数据库尚未就绪"):
		return "localserver_database_not_ready", "database_not_ready"
	case strings.Contains(err.Error(), "云端客户端未初始化"):
		return "localserver_cloud_client_not_ready", "cloud_client_not_ready"
	case strings.Contains(err.Error(), "本地图标存储尚未初始化"):
		return "localserver_icon_store_not_ready", "icon_store_not_ready"
	case strings.Contains(err.Error(), "本地头像存储尚未初始化"):
		return "localserver_avatar_store_not_ready", "avatar_store_not_ready"
	case strings.Contains(err.Error(), "通知不存在"):
		return "localserver_notification_not_found", "notification_not_found"
	case strings.Contains(err.Error(), "附件不存在"), strings.Contains(err.Error(), "引用的附件不存在"):
		return "localserver_attachment_not_found", "attachment_not_found"
	case strings.Contains(err.Error(), "文件不存在"):
		return "localserver_file_not_found", "file_not_found"
	case strings.Contains(err.Error(), "状态不存在"):
		return "localserver_status_not_found", "status_not_found"
	case strings.Contains(err.Error(), "任务不存在"):
		return "localserver_task_not_found", "task_not_found"
	case strings.Contains(err.Error(), "任务启动前必须选择流水线"):
		return "localserver_pipeline_required", "pipeline_required"
	case strings.Contains(err.Error(), "必须配置 CLI 和模型"), strings.Contains(err.Error(), "缺少 CLI 和模型"):
		return "localserver_pipeline_step_execution_required", "pipeline_incomplete"
	case strings.Contains(err.Error(), "必须配置名称和提示词"):
		return "localserver_pipeline_step_content_required", "pipeline_incomplete"
	case strings.Contains(err.Error(), "任务启动前必须选择专家团"), strings.Contains(err.Error(), "专家团不能为空"):
		return "localserver_expert_group_required", "expert_group_required"
	case strings.Contains(err.Error(), "任务未指派给专家团"):
		return "localserver_expert_group_mode_required", "expert_group_mode_required"
	case strings.Contains(err.Error(), "专家团成员不存在"), strings.Contains(err.Error(), "所选 Agent 不在当前专家团"):
		return "localserver_expert_member_not_found", "expert_member_not_found"
	case strings.Contains(err.Error(), "专家团名称"), strings.Contains(err.Error(), "Agent 名称"), strings.Contains(err.Error(), "Agent 提示词"), strings.Contains(err.Error(), "Agent 必须配置"):
		return "localserver_expert_group_invalid", "expert_group_invalid"
	case strings.Contains(err.Error(), "任务启动前必须选择执行方式"), strings.Contains(err.Error(), "执行方式未选择"):
		return "localserver_execution_mode_required", "execution_mode_required"
	case strings.Contains(err.Error(), "Vibe Coding 当前仅支持 Codex"), strings.Contains(err.Error(), "Vibe Coding 工具无效"), strings.Contains(err.Error(), "Vibe Coding 执行不能指定流水线"), strings.Contains(err.Error(), "流水线执行不能指定编程工具"), strings.Contains(err.Error(), "无效任务执行方式"), strings.Contains(err.Error(), "CLI 执行不能指定流水线或专家团"):
		return "localserver_execution_mode_invalid", "execution_mode_invalid"
	case strings.Contains(err.Error(), "CLI 不能为空"), strings.Contains(err.Error(), "模型不能为空"):
		return "localserver_execution_mode_target_invalid", "execution_mode_target_invalid"
	case strings.Contains(err.Error(), "任务目录为空"):
		return "localserver_file_path_empty", "task_directory_empty"
	case strings.Contains(err.Error(), "工作目录不能为空"):
		return "localserver_workspace_required", "workspace_required"
	case strings.Contains(err.Error(), "工作目录地址无效"):
		return "localserver_workspace_invalid", "workspace_invalid"
	case strings.Contains(err.Error(), "工作目录不存在"), strings.Contains(err.Error(), "无法访问工作目录"), strings.Contains(err.Error(), "工作目录地址不是文件夹"):
		return "localserver_workspace_unavailable", "workspace_unavailable"
	case strings.Contains(err.Error(), "文件路径不在任务目录范围内"):
		return "localserver_file_path_outside", "file_path_outside"
	case strings.Contains(err.Error(), "文件路径不允许包含符号链接"):
		return "localserver_file_path_symlink", "file_path_symlink"
	case strings.Contains(err.Error(), "文件路径无效"):
		return "localserver_file_path_invalid", "file_path_invalid"
	case strings.Contains(err.Error(), "任务标题不能为空"):
		return "localserver_title_required", "title_required"
	case strings.Contains(err.Error(), "问题内容不能为空"):
		return "localserver_question_required", "question_required"
	case strings.Contains(err.Error(), "prompt_snapshot 不能为空"):
		return "localserver_prompt_required", "prompt_required"
	case strings.Contains(err.Error(), "pipeline_uuid 不能为空"):
		return "localserver_pipeline_uuid_required", "pipeline_required"
	case strings.Contains(err.Error(), "无效的任务状态"):
		return "localserver_invalid_task_status", "invalid_task_status"
	case strings.Contains(err.Error(), "任务仍有正在执行"):
		return "localserver_active_sessions", "task_has_active_sessions"
	default:
		return "localserver_internal_error", ""
	}
}

func fallbackKeyForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return "localserver_bad_request"
	case http.StatusUnauthorized:
		return "localserver_not_authenticated"
	case http.StatusNotFound:
		return "localserver_not_found"
	default:
		return "localserver_internal_error"
	}
}

// StatusForError preserves the existing HTTP status mapping for domain errors.
func StatusForError(err error, fallback int) int {
	switch {
	case errors.Is(err, project.ErrNotFound), errors.Is(err, pipeline.ErrNotFound),
		errors.Is(err, pipeline.ErrStepNotFound), errors.Is(err, taskruntime.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, project.ErrInUse), errors.Is(err, taskruntime.ErrTaskContextLocked),
		errors.Is(err, taskruntime.ErrPipelineSnapshotLocked):
		return http.StatusConflict
	default:
		return fallback
	}
}
