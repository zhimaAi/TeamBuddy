package localserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/taskruntime"
)

const (
	maxTaskSkillBodyBytes = 512 * 1024
	maxActivityContent    = 64 * 1024
	maxActivityThreadID   = 256
)

type taskSkillHistoryItem struct {
	UUID      string `json:"uuid"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Tool      string `json:"tool"`
	ThreadID  string `json:"thread_id,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// RegisterSkillRoutes exposes the task-scoped surface used by the bundled
// teambuddy Skill. The route group applies the dedicated loopback guard.
func (h *TasksHandler) RegisterSkillRoutes(r *gin.RouterGroup) {
	r.GET("/tasks/:uuid", h.getTaskForSkill)
	r.PUT("/tasks/:uuid/status", h.updateTaskStatusFromSkill)
	r.PUT("/tasks/:uuid/vibe-session", h.updateVibeCodingSessionFromSkill)
	r.POST("/tasks/:uuid/activities", h.createSkillActivity)
}

type vibeCodingSession struct {
	Type        string `json:"type"`
	ToolName    string `json:"tool_name,omitempty"`
	ThreadID    string `json:"thread_id"`
	BindingKind string `json:"binding_kind,omitempty"`
	BindingKey  string `json:"binding_key,omitempty"`
}

func (h *TasksHandler) getCodexCapability(c *gin.Context) {
	reasonCode := h.codexSkillReason
	response := gin.H{"available": h.codexSkillReady, "reason_code": reasonCode}
	if !h.codexSkillReady {
		if reasonCode == "" {
			reasonCode = builtinskills.CapabilityReasonInstallationFail
			response["reason_code"] = reasonCode
		}
		response["message"] = i18n.T(c, codexCapabilityMessageKey(reasonCode))
	}
	tools := gin.H{}
	for _, tool := range taskruntime.VibeCodingTools() {
		_, ready, reason := h.vibeSkill(tool)
		definition, _ := taskruntime.VibeTool(tool)
		item := gin.H{"available": ready, "reason_code": reason, "launch": definition.Launch,
			"binding_kind": definition.BindingKind, "requires_report_id": definition.RequiresReportID,
			"supports_resume": definition.SupportsResume, "supports_rebind": definition.SupportsRebind}
		if !ready {
			if reason == "" {
				reason = builtinskills.CapabilityReasonInstallationFail
				item["reason_code"] = reason
			}
			item["message"] = i18n.T(c, vibeCapabilityMessageKey(tool, reason))
		}
		switch tool {
		case taskruntime.ExecutionToolCodex:
			// Codex 应用通过深链接打开，不探测可执行文件。
			item["installed"] = true
		default:
			if vibeLaunchKind(tool) == "terminal" {
				execPath, err := executor.ResolveVibeCodingExecutable(tool)
				item["installed"] = err == nil && execPath != ""
				if err == nil {
					item["exec_path"] = execPath
				}
			}
		}
		tools[tool] = item
	}
	response["tools"] = tools
	c.JSON(http.StatusOK, response)
}

func vibeCapabilityMessageKey(tool, reasonCode string) string {
	if tool == taskruntime.ExecutionToolGeneric {
		if reasonCode == builtinskills.CapabilityReasonNotManaged {
			return "localserver_generic_skill_not_managed"
		}
		return "localserver_generic_skill_install_failed"
	}
	return codexCapabilityMessageKey(reasonCode)
}

func codexCapabilityMessageKey(reasonCode string) string {
	if reasonCode == builtinskills.CapabilityReasonNotManaged {
		return "localserver_codex_skill_not_managed"
	}
	return "localserver_codex_skill_install_failed"
}

func (h *TasksHandler) getTaskForSkill(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	var sourceType, title, description, status, legacyWorkDir, taskDir, executionMode, executionTool string
	var createdAt, updatedAt int64
	var executionIndex int
	err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT source_type, title, content_snapshot, status, work_dir,
		COALESCE(task_dir,''), execution_mode, execution_tool, created_at, updated_at, execution_index FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(
		&sourceType, &title, &description, &status, &legacyWorkDir, &taskDir, &executionMode, &executionTool, &createdAt, &updatedAt, &executionIndex,
	)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if !isVibeCodingTask(executionMode, executionTool) {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
		return
	}
	workDirs, err := h.loadTaskWorkDirs(taskUUID, legacyWorkDir)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	history, err := h.loadTaskSkillHistory(c.Request.Context(), taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"task": gin.H{
			"uuid": taskUUID, "source_type": sourceType, "title": title, "description": description,
			"status": taskSkillPublicStatus(status), "execution_mode": executionMode, "execution_tool": executionTool,
			"work_dirs": workDirs, "task_dir": taskDir,
			"created_at": createdAt, "updated_at": updatedAt, "execution_index": executionIndex,
		},
		"history": history, "allowed_statuses": []string{"pending", "in_progress", "blocked", "done"},
	})
}

func (h *TasksHandler) getCodexContext(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	var legacyWorkDir, taskDir, executionMode, executionTool, sessionJSON, model string
	var executionIndex int
	err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT work_dir, COALESCE(task_dir,''), execution_mode, execution_tool,
		vibe_coding_session_json, execution_index, vibe_coding_model FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&legacyWorkDir, &taskDir, &executionMode, &executionTool, &sessionJSON, &executionIndex, &model)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if !isVibeCodingTask(executionMode, executionTool) {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
		return
	}
	skillRoot, skillReady, skillReason := h.vibeSkill(executionTool)
	if !skillReady {
		if skillReason == "" {
			skillReason = builtinskills.CapabilityReasonInstallationFail
		}
		i18n.Error(c, http.StatusConflict, vibeCapabilityMessageKey(executionTool, skillReason), skillReason)
		return
	}
	workDirs, err := h.loadTaskWorkDirs(taskUUID, legacyWorkDir)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if len(workDirs) == 0 || strings.TrimSpace(workDirs[0]) == "" {
		i18n.Error(c, http.StatusConflict, "localserver_task_work_dir_required", "task_work_dir_required")
		return
	}
	skillPath := filepath.Join(skillRoot, builtinskills.TaskSkillName, "SKILL.md")
	linkPath := encodeMarkdownLocalPath(skillPath)
	promptKey := "localserver_codex_task_prompt"
	if executionTool == taskruntime.ExecutionToolGeneric {
		promptKey = "localserver_generic_task_prompt"
	}
	prompt := i18n.Format(c, promptKey, i18n.Params{
		"SkillPath": linkPath, "TaskUUID": taskUUID, "Tool": executionTool,
		"ExecutionIndex": executionIndex, "OutputDirSection": vibeOutputDirSection(c, taskDir),
	})
	if executionTool == taskruntime.ExecutionToolQoderIDE {
		prompt = "/teambuddy\n" + prompt
		prompt += "\n\n" + i18n.Format(c, "localserver_qoder_ide_task_prompt", i18n.Params{"WorkDir": workDirs[0]})
		marker, _ := json.Marshal(gin.H{"task_uuid": taskUUID, "execution_index": executionIndex})
		prompt += "\nTeamBuddy-Qoder-IDE: " + string(marker)
	}
	response := gin.H{
		"task_uuid": taskUUID, "work_dir": workDirs[0], "prompt": prompt,
		"tool": executionTool, "launch": vibeLaunchKind(executionTool), "model_name": model,
	}
	if vibeLaunchKind(executionTool) == "terminal" {
		execPath, err := executor.ResolveVibeCodingExecutable(executionTool)
		if err != nil || execPath == "" {
			i18n.Error(c, http.StatusConflict, "localserver_vibe_cli_not_found", "vibe_cli_not_found")
			return
		}
		response["exec_path"] = execPath
	}
	if session, ok := validVibeCodingSession(sessionJSON, executionTool); ok {
		if executionTool == taskruntime.ExecutionToolGeneric {
			response["bound"] = true
			response["tool_name"] = session.ToolName
		} else {
			response["thread_id"] = session.ThreadID
		}
	}
	c.JSON(http.StatusOK, response)
}

func (h *TasksHandler) updateVibeCodingSessionFromSkill(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	var body struct {
		vibeCodingSession
		ExecutionIndex *int `json:"execution_index"`
	}
	if !decodeTaskSkillJSON(c, &body) {
		return
	}
	body.Type = strings.ToLower(strings.TrimSpace(body.Type))
	body.ThreadID = strings.TrimSpace(body.ThreadID)
	body.ToolName = strings.TrimSpace(body.ToolName)
	if !validGenericToolName(body.ToolName) {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.Type == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_vibe_session_type_invalid", "vibe_session_type_invalid")
		return
	}
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		defer lease.Close()
	}
	tx, err := h.taskDB().BeginTx(c.Request.Context(), nil)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	var executionMode, executionTool, boundJSON string
	var executionIndex int
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT execution_mode, execution_tool, vibe_coding_session_json, execution_index FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&executionMode, &executionTool, &boundJSON, &executionIndex); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if body.ExecutionIndex == nil || *body.ExecutionIndex != executionIndex {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
		return
	}
	if executionMode != taskruntime.ExecutionModeVibeCoding {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_vibe_coding", "task_not_vibe_coding")
		return
	}
	if body.Type != strings.ToLower(strings.TrimSpace(executionTool)) {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_type_mismatch", "vibe_session_type_mismatch")
		return
	}
	if !taskruntime.IsVibeCodingTool(body.Type) {
		i18n.Error(c, http.StatusBadRequest, "localserver_vibe_session_type_invalid", "vibe_session_type_invalid")
		return
	}
	if body.Type == taskruntime.ExecutionToolGeneric {
		// 通用工具没有统一原生会话标识。每个会话持有自己的请求键，
		// 服务端签发报告 ID；相同请求可恢复响应，其他会话不能接管。
		key, valid := normalizeCodexThreadID(body.BindingKey)
		if !valid || body.ThreadID != "" || body.BindingKind != "generic" {
			i18n.Error(c, http.StatusBadRequest, "localserver_vibe_session_thread_id_invalid", "vibe_session_thread_id_invalid")
			return
		}
		body.BindingKey = key
		if bound, ok := validVibeCodingSession(boundJSON, body.Type); ok {
			if bound.BindingKey != key {
				i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
				return
			}
			body.ThreadID = bound.ThreadID
			if bound.ToolName != "" {
				body.ToolName = bound.ToolName
			}
		} else if strings.TrimSpace(boundJSON) != "" && strings.TrimSpace(boundJSON) != "{}" {
			i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
			return
		} else {
			body.ThreadID = uuid.NewString()
		}
	} else if body.BindingKey != "" || body.BindingKind != "" || body.ToolName != "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_vibe_session_type_invalid", "vibe_session_type_invalid")
		return
	}
	normalizedThreadID, valid := normalizeVibeThreadID(body.Type, body.ThreadID)
	if !valid {
		i18n.Error(c, http.StatusBadRequest, "localserver_vibe_session_thread_id_invalid", "vibe_session_thread_id_invalid")
		return
	}
	body.ThreadID = normalizedThreadID
	if bound, ok := validVibeCodingSession(boundJSON, body.Type); ok && bound.ThreadID != body.ThreadID {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
		return
	}
	// Remote dispatch may resume this exact binding. Its handshake is
	// idempotent; only a new binding must wait for remote execution to settle.
	if _, bound := validVibeCodingSession(boundJSON, body.Type); !bound {
		status, err := remoteExecutionBlock(c.Request.Context(), tx, taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		if status != "" {
			i18n.Error(c, http.StatusConflict, "localserver_active_sessions", "task_has_active_sessions")
			return
		}
	}
	sessionJSON, err := json.Marshal(body.vibeCodingSession)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(c.Request.Context(), `UPDATE gt_tasks SET vibe_coding_session_json=?, updated_at=?
		WHERE uuid=? AND execution_mode='vibe_coding' AND execution_tool=?`, string(sessionJSON), now, taskUUID, body.Type)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, rowsErr)
		return
	} else if rows != 1 {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_type_mismatch", "vibe_session_type_mismatch")
		return
	}
	if err = tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	h.notifyTaskChanged(taskUUID)
	response := gin.H{
		"task_uuid": taskUUID, "type": body.Type, "thread_id": body.ThreadID, "updated_at": now,
	}
	if body.Type == taskruntime.ExecutionToolGeneric {
		response["binding_kind"] = body.BindingKind
		response["tool_name"] = body.ToolName
	}
	c.JSON(http.StatusOK, response)
}

func validVibeCodingSession(raw, expectedType string) (vibeCodingSession, bool) {
	var session vibeCodingSession
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &session) != nil {
		return vibeCodingSession{}, false
	}
	session.Type = strings.ToLower(strings.TrimSpace(session.Type))
	if session.Type == "" || session.Type != strings.ToLower(strings.TrimSpace(expectedType)) {
		return vibeCodingSession{}, false
	}
	if !taskruntime.IsVibeCodingTool(session.Type) {
		return vibeCodingSession{}, false
	}
	if session.Type == taskruntime.ExecutionToolGeneric {
		if _, valid := normalizeCodexThreadID(session.BindingKey); !valid || session.BindingKind != "generic" {
			return vibeCodingSession{}, false
		}
	}
	threadID, ok := normalizeVibeThreadID(session.Type, session.ThreadID)
	if !ok {
		return vibeCodingSession{}, false
	}
	session.ThreadID = threadID
	return session, true
}

// vibeOutputDirSection 生成「产出文档目录」段落。客户端只同步任务产出目录
// （task_dir）内的文件，因此 Vibe Coding 提示词必须显式给出该目录，否则外部工具
// 会把产出文档写进项目工作目录，产出无法同步到线上。任务没有产出目录时返回空串，
// 调用方仍需传入该参数（模板按 missingkey=error 渲染）。
func vibeOutputDirSection(c *gin.Context, taskDir string) string {
	taskDir = strings.TrimSpace(taskDir)
	if taskDir == "" {
		return ""
	}
	return i18n.Format(c, "localserver_vibe_output_dir", i18n.Params{"TaskDir": taskDir})
}

// 工具名称仅作展示，不能参与工具路由、绑定校验或外部应用启动。
func validGenericToolName(name string) bool {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func vibeCodingBindingSummary(mode, tool, raw string) (bool, string) {
	if mode != taskruntime.ExecutionModeVibeCoding {
		return false, ""
	}
	session, ok := validVibeCodingSession(raw, tool)
	if !ok {
		return false, ""
	}
	if tool == taskruntime.ExecutionToolGeneric && validGenericToolName(session.ToolName) {
		return true, session.ToolName
	}
	return true, ""
}

// 复用执行切换链路封存旧执行，预期代次防止重复请求清空刚建立的新绑定。
func (h *TasksHandler) rebindGenericVibeTool(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	var body struct {
		ExecutionIndex *int `json:"execution_index"`
	}
	if !decodeTaskSkillJSON(c, &body) {
		return
	}
	if body.ExecutionIndex == nil || *body.ExecutionIndex < 0 {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	result, err := h.switchExecutionModeCore(withRequestID(c), taskUUID, executionModeInput{
		Mode: taskruntime.ExecutionModeVibeCoding, Tool: taskruntime.ExecutionToolGeneric,
		RebindExecutionIndex: body.ExecutionIndex,
	})
	if err != nil {
		h.writeCoreError(c, err, http.StatusConflict)
		return
	}
	c.JSON(http.StatusOK, result.Response)
}

func normalizeCodexThreadID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id.String() != strings.ToLower(value) {
		return "", false
	}
	return id.String(), true
}

func normalizeVibeThreadID(tool, value string) (string, bool) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == taskruntime.ExecutionToolCodex || tool == taskruntime.ExecutionToolCodexCLI || tool == taskruntime.ExecutionToolGeneric {
		return normalizeCodexThreadID(value)
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxActivityThreadID || strings.ContainsAny(value, "/\\ \t\r\n") {
		return "", false
	}
	for _, r := range value {
		if r > 127 || !(r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			return "", false
		}
	}
	first, last := value[0], value[len(value)-1]
	isAlphaNum := func(b byte) bool {
		return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
	}
	if !isAlphaNum(first) || !isAlphaNum(last) {
		return "", false
	}
	return value, true
}

func encodeMarkdownLocalPath(path string) string {
	normalized := filepath.ToSlash(path)
	segments := strings.Split(normalized, "/")
	for index, segment := range segments {
		if index == 0 && len(segment) == 2 && isASCIIAlpha(segment[0]) && segment[1] == ':' {
			continue
		}
		segments[index] = encodeMarkdownPathSegment(segment)
	}
	return strings.Join(segments, "/")
}

func encodeMarkdownPathSegment(segment string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for _, value := range []byte(segment) {
		if isURLUnreserved(value) {
			encoded.WriteByte(value)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[value>>4])
		encoded.WriteByte(hex[value&0x0f])
	}
	return encoded.String()
}

func isURLUnreserved(value byte) bool {
	return isASCIIAlpha(value) || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func (h *TasksHandler) updateTaskStatusFromSkill(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusConflict, err)
			return
		}
		defer lease.Close()
	}
	var body struct {
		Status         string `json:"status"`
		ExecutionIndex *int   `json:"execution_index"`
		Tool           string `json:"tool"`
		ThreadID       string `json:"thread_id"`
	}
	if !decodeTaskSkillJSON(c, &body) {
		return
	}
	publicStatus := strings.TrimSpace(body.Status)
	storedStatus := map[string]string{"pending": "pending", "in_progress": "active", "blocked": "blocked", "done": "done"}[publicStatus]
	if storedStatus == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_status_invalid", "skill_status_invalid")
		return
	}
	var executionMode, executionTool, boundJSON string
	var executionIndex int
	err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT execution_mode, execution_tool, vibe_coding_session_json, execution_index FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&executionMode, &executionTool, &boundJSON, &executionIndex)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if body.ExecutionIndex == nil || *body.ExecutionIndex != executionIndex {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
		return
	}
	if !isVibeCodingTask(executionMode, executionTool) {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
		return
	}
	executionTool = strings.ToLower(strings.TrimSpace(executionTool))
	body.ThreadID = resolveReportedThreadID(boundJSON, executionTool, body.ThreadID)
	if body.ThreadID == "" && !taskruntime.RequiresVibeReportID(executionTool) {
		tool := strings.ToLower(strings.TrimSpace(body.Tool))
		if tool == "" {
			tool = executionTool
		}
		if tool != executionTool {
			i18n.Error(c, http.StatusBadRequest, "localserver_skill_tool_invalid", "skill_tool_invalid")
			return
		}
	} else if !requireBoundSkillSession(c, boundJSON, executionTool, body.Tool, body.ThreadID) {
		return
	}
	now := time.Now().UnixMilli()
	result, err := h.taskDB().ExecContext(c.Request.Context(), `UPDATE gt_tasks SET status=?, updated_at=?
		WHERE uuid=? AND execution_mode=? AND execution_tool=? AND vibe_coding_session_json=?`, storedStatus, now, taskUUID, executionMode, executionTool, boundJSON)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if rows == 0 {
		h.writeSkillTaskAccessError(c, taskUUID)
		return
	}
	if storedStatus == "active" {
		if err := h.ensureVibeCodingConversation(c.Request.Context(), taskUUID, i18n.T(c, "localserver_vibe_coding_assigned_activity"), false); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
	}
	if err := h.syncVibeCodingConversationStatus(c.Request.Context(), taskUUID, storedStatus); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if storedStatus == "done" {
		_ = markTaskNotificationsRead(c.Request.Context(), h.taskDB(), taskUUID, true)
	}
	h.notifyTaskChanged(taskUUID)
	h.pushTaskSync(taskUUID)
	c.JSON(http.StatusOK, gin.H{"task_uuid": taskUUID, "status": publicStatus, "updated_at": now})
}

func (h *TasksHandler) createSkillActivity(c *gin.Context) {
	taskUUID, ok := validateTaskSkillUUID(c)
	if !ok {
		return
	}
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusConflict, err)
			return
		}
		defer lease.Close()
	}
	var body struct {
		ExecutionIndex *int   `json:"execution_index"`
		EventID        string `json:"event_id"`
		Type           string `json:"type"`
		Content        string `json:"content"`
		Tool           string `json:"tool"`
		ThreadID       string `json:"thread_id"`
	}
	if !decodeTaskSkillJSON(c, &body) {
		return
	}
	body.Type = strings.TrimSpace(body.Type)
	if body.Type != "question" && body.Type != "result" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_type_invalid", "skill_activity_type_invalid")
		return
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" || utf8.RuneCountInString(body.Content) > maxActivityContent {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_content_invalid", "skill_activity_content_invalid")
		return
	}
	body.Tool = strings.ToLower(strings.TrimSpace(body.Tool))
	body.ThreadID = strings.TrimSpace(body.ThreadID)
	if utf8.RuneCountInString(body.ThreadID) > maxActivityThreadID {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_thread_id_invalid", "skill_thread_id_invalid")
		return
	}
	now := time.Now().UnixMilli()
	progressUUID := uuid.NewString()
	if body.EventID != "" {
		id, err := uuid.Parse(body.EventID)
		if err != nil {
			i18n.Error(c, http.StatusBadRequest, "localserver_skill_event_id_invalid", "skill_event_id_invalid")
			return
		}
		progressUUID = id.String()
	}
	recordType, prompt, result := "initial_run", "", body.Content
	activityActor, activityKind := activityActorTool, activityKindFinalResponse
	if body.Type == "question" {
		recordType, prompt, result = "user_question", body.Content, ""
		activityActor, activityKind = activityActorUser, activityKindUserMessage
	}
	tx, err := h.taskDB().BeginTx(c.Request.Context(), nil)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	var executionMode, executionTool, boundJSON string
	var executionIndex int
	if err := tx.QueryRowContext(c.Request.Context(), `SELECT execution_mode, execution_tool, vibe_coding_session_json, execution_index FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&executionMode, &executionTool, &boundJSON, &executionIndex); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if body.ExecutionIndex == nil || *body.ExecutionIndex != executionIndex {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
		return
	}
	if !isVibeCodingTask(executionMode, executionTool) {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
		return
	}
	executionTool = strings.ToLower(strings.TrimSpace(executionTool))
	if body.Tool == "" {
		body.Tool = executionTool
	}
	if body.Tool != executionTool {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_tool_invalid", "skill_tool_invalid")
		return
	}
	if body.ThreadID != "" {
		if normalized, valid := normalizeVibeThreadID(body.Tool, body.ThreadID); valid {
			body.ThreadID = normalized
		} else {
			i18n.Error(c, http.StatusBadRequest, "localserver_skill_thread_id_invalid", "skill_thread_id_invalid")
			return
		}
	}
	body.ThreadID = resolveReportedThreadID(boundJSON, executionTool, body.ThreadID)
	if (taskruntime.RequiresVibeReportID(executionTool) || body.ThreadID != "") && !requireBoundSkillSession(c, boundJSON, executionTool, body.Tool, body.ThreadID) {
		return
	}
	var existingTask, existingTool, existingThread, existingPrompt, existingResult string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT task_uuid, cli_type, external_session_id, user_prompt, final_result
		FROM gt_task_progress WHERE uuid=?`, progressUUID).Scan(&existingTask, &existingTool, &existingThread, &existingPrompt, &existingResult)
	if err == nil {
		if existingTask != taskUUID || existingTool != body.Tool || existingThread != body.ThreadID || existingPrompt != prompt || existingResult != result {
			i18n.Error(c, http.StatusConflict, "localserver_skill_event_conflict", "skill_event_conflict")
			return
		}
		c.JSON(http.StatusOK, gin.H{"uuid": progressUUID, "duplicate": true})
		return
	}
	if err != sql.ErrNoRows {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(), `INSERT INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, session_uuid, record_type, user_prompt,
		 cli_type, model_name, status, final_result, execution_mode, external_session_id,
		 activity_actor, activity_kind, created_at, started_at, finished_at)
		VALUES (?, ?, '', '', '', ?, ?, ?, '', 'success', ?, 'vibe_coding', ?, ?, ?, ?, ?, ?)`,
		progressUUID, taskUUID, recordType, prompt, body.Tool, result, body.ThreadID,
		activityActor, activityKind, now, now, now); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE gt_tasks SET updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE gt_task_notifications SET progress_uuid=?,
		is_read=0, read_at=0, is_archived=0, created_at=? WHERE task_uuid=?`, progressUUID, now, taskUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	h.notifyTaskChanged(taskUUID)
	h.pushTaskSync(taskUUID)
	c.JSON(http.StatusCreated, gin.H{
		"uuid": progressUUID, "type": body.Type, "content": body.Content, "tool": body.Tool,
		"thread_id": body.ThreadID, "created_at": now,
	})
}

func (h *TasksHandler) loadTaskSkillHistory(ctx context.Context, taskUUID string) ([]taskSkillHistoryItem, error) {
	rows, err := h.taskDB().QueryContext(ctx, `SELECT uuid, record_type, user_prompt, final_result, cli_type,
		external_session_id, status, created_at, finished_at FROM gt_task_progress WHERE task_uuid=? ORDER BY created_at, rowid`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]taskSkillHistoryItem, 0)
	for rows.Next() {
		var id, recordType, prompt, result, tool, threadID, status string
		var createdAt, finishedAt int64
		if err := rows.Scan(&id, &recordType, &prompt, &result, &tool, &threadID, &status, &createdAt, &finishedAt); err != nil {
			return nil, err
		}
		if recordType == "user_question" && strings.TrimSpace(prompt) != "" {
			items = append(items, taskSkillHistoryItem{UUID: id, Type: "question", Content: prompt, Tool: tool, ThreadID: threadID, Status: status, CreatedAt: createdAt})
		}
		if recordType != "user_question" && strings.TrimSpace(result) != "" {
			if finishedAt == 0 {
				finishedAt = createdAt
			}
			items = append(items, taskSkillHistoryItem{UUID: id, Type: "result", Content: result, Tool: tool, ThreadID: threadID, Status: status, CreatedAt: finishedAt})
		}
	}
	return items, rows.Err()
}

func validateTaskSkillUUID(c *gin.Context) (string, bool) {
	taskUUID := strings.TrimSpace(c.Param("uuid"))
	if _, err := uuid.Parse(taskUUID); err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_task_uuid_invalid", "task_uuid_invalid")
		return "", false
	}
	return taskUUID, true
}

func isVibeCodingTask(mode, tool string) bool {
	return mode == taskruntime.ExecutionModeVibeCoding && taskruntime.IsVibeCodingTool(tool)
}

// vibeLaunchKind 决定前端如何交付本次执行：app 用深链接打开桌面应用，terminal 由
// 客户端拉起终端，clipboard 只复制提示词。取值来自工具定义表，未登记的工具返回空串。
func vibeLaunchKind(tool string) string {
	definition, ok := taskruntime.VibeTool(tool)
	if !ok {
		return ""
	}
	return definition.Launch
}

func (h *TasksHandler) vibeSkill(tool string) (root string, ready bool, reason string) {
	lookup := strings.ToLower(strings.TrimSpace(tool))
	if lookup == taskruntime.ExecutionToolCodexCLI {
		lookup = taskruntime.ExecutionToolCodex
	}
	if status, ok := h.vibeSkills[lookup]; ok {
		return status.Root, status.Ready, status.Reason
	}
	if lookup == taskruntime.ExecutionToolCodex {
		return h.skillsRoot, h.codexSkillReady, h.codexSkillReason
	}
	return "", false, builtinskills.CapabilityReasonInstallationFail
}

func (h *TasksHandler) writeSkillTaskAccessError(c *gin.Context, taskUUID string) {
	var mode, tool string
	if err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT execution_mode, execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&mode, &tool); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
	} else {
		i18n.Error(c, http.StatusConflict, "localserver_task_not_codex", "task_not_codex")
	}
}

func decodeTaskSkillJSON(c *gin.Context, target interface{}) bool {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		i18n.Error(c, http.StatusUnsupportedMediaType, "localserver_json_content_type_required", "json_content_type_required")
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTaskSkillBodyBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil || !utf8.Valid(raw) {
		i18n.Error(c, http.StatusBadRequest, "localserver_json_utf8_required", "json_utf8_required")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_json_invalid", "json_invalid")
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		i18n.Error(c, http.StatusBadRequest, "localserver_json_single_object_required", "json_single_object_required")
		return false
	}
	return true
}

func resolveReportedThreadID(raw, tool, threadID string) string {
	threadID = strings.TrimSpace(threadID)
	// 通用工具必须显式提交本会话领取的报告 ID，不能借用其他会话的绑定。
	if taskruntime.RequiresVibeReportID(tool) {
		return threadID
	}
	if threadID != "" {
		if normalized, ok := normalizeVibeThreadID(tool, threadID); ok {
			return normalized
		}
		return threadID
	}
	if bound, ok := validVibeCodingSession(raw, tool); ok {
		return bound.ThreadID
	}
	return ""
}

func requireBoundSkillSession(c *gin.Context, raw, expectedTool, tool, threadID string) bool {
	bound, ok := validVibeCodingSession(raw, expectedTool)
	normalized, valid := normalizeVibeThreadID(expectedTool, threadID)
	if !ok || !valid || strings.ToLower(strings.TrimSpace(tool)) != expectedTool || normalized != bound.ThreadID {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale")
		return false
	}
	return true
}

func taskSkillPublicStatus(status string) string {
	if status == "active" {
		return "in_progress"
	}
	return status
}

// ensureVibeCodingConversation makes an active Vibe Coding task discoverable
// from the conversation page. The assignment activity is only created when the
// task has no existing activity; the notification is an idempotent list anchor
// and is not rendered as an extra message. Conversation-page creation marks
// the anchor read until the first Skill activity arrives; other entry points
// create an unread anchor immediately.
func (h *TasksHandler) ensureVibeCodingConversation(ctx context.Context, taskUUID, activityContent string, initiallyRead bool) error {
	tx, err := h.taskDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var title, status, executionMode, executionTool string
	if err = tx.QueryRowContext(ctx, `SELECT title, status, execution_mode, execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&title, &status, &executionMode, &executionTool); err != nil {
		return err
	}
	if status != "active" || executionMode != taskruntime.ExecutionModeVibeCoding {
		return tx.Commit()
	}

	now := time.Now().UnixMilli()
	assignmentProgressUUID := vibeCodingAssignmentProgressUUID(taskUUID)
	progressUUID := assignmentProgressUUID
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, session_uuid, record_type, user_prompt,
		 cli_type, model_name, status, final_result, execution_mode, external_session_id,
		 activity_actor, activity_kind, created_at, started_at, finished_at)
		SELECT ?, ?, '', '', '', 'initial_run', '', ?, '', 'success', ?, 'vibe_coding', '',
		       ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM gt_task_progress WHERE task_uuid=?)`,
		progressUUID, taskUUID, executionTool, activityContent,
		activityActorSystem, activityKindAssignment, now, now, now, taskUUID)
	if err != nil {
		return err
	}
	if inserted, rowsErr := result.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if inserted == 0 {
		if err = tx.QueryRowContext(ctx, `SELECT uuid FROM gt_task_progress WHERE task_uuid=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, taskUUID).
			Scan(&progressUUID); err != nil {
			return err
		}
	}
	if progressUUID == assignmentProgressUUID {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_task_progress SET activity_actor=?, activity_kind=? WHERE uuid=?`,
			activityActorSystem, activityKindAssignment, progressUUID); err != nil {
			return err
		}
	}

	var notificationUUID string
	err = tx.QueryRowContext(ctx, `SELECT uuid FROM gt_task_notifications WHERE task_uuid=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, taskUUID).
		Scan(&notificationUUID)
	if err == sql.ErrNoRows {
		notificationUUID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("vibe-coding-notification:"+taskUUID)).String()
		notificationSessionUUID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("vibe-coding-notification-session:"+taskUUID)).String()
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO gt_task_notifications
			(uuid, task_uuid, task_step_uuid, step_order, progress_uuid, session_uuid,
			 terminal_status, title, summary, is_read, is_archived, created_at)
			VALUES (?, ?, '', 0, ?, ?, 'running', ?, ?, ?, 0, ?)`,
			notificationUUID, taskUUID, progressUUID, notificationSessionUUID, title, activityContent, initiallyRead, now); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if _, err = tx.ExecContext(ctx, `UPDATE gt_task_notifications SET progress_uuid=?, terminal_status='running',
		title=?, summary=?, is_archived=0 WHERE uuid=?`, progressUUID, title, activityContent, notificationUUID); err != nil {
		return err
	}

	return tx.Commit()
}

func (h *TasksHandler) syncVibeCodingConversationStatus(ctx context.Context, taskUUID, taskStatus string) error {
	notificationStatus := map[string]string{
		"pending": "created",
		"active":  "running",
		"blocked": "failed",
		"done":    "success",
	}[taskStatus]
	if notificationStatus == "" {
		return nil
	}
	_, err := h.taskDB().ExecContext(ctx, `UPDATE gt_task_notifications SET terminal_status=?, created_at=?
		WHERE task_uuid=? AND EXISTS (SELECT 1 FROM gt_tasks t WHERE t.uuid=? AND t.execution_mode='vibe_coding')`,
		notificationStatus, time.Now().UnixMilli(), taskUUID, taskUUID)
	return err
}
