package localserver

// tasks_core.go 是任务写路径的 gin-free 核心层。
//
// 为什么单独成文件：HTTP 入口（tasks.go）与微信/技能入口（skillsurface.go）曾各自
// 实现同一套写逻辑，桥接手段是伪造 gin.Context 并改写 c.Request.Body / 注入 c.Params；
// 伪造的 gin.Context 在 c.JSON/c.Header 会走到未覆写的 WriteHeaderNow → nil panic，
// 且该 panic 发生在微信 workLoop goroutine，gin.CustomRecovery 拦不到，会直接击穿进程。
// 根因是写方法签名绑定 *gin.Context：只要核心仍收 gin.Context，非 HTTP 入口就只能继续伪造。
//
// 因此本文件里的方法一律只接收 context.Context + 显式参数，返回结构化结果；HTTP 入口在
// tasks.go 里做薄 wrapper，自行把结果映射为响应（状态码语义与原实现逐条一致）。
//
// 约定：
//   - 本文件的方法不写 HTTP 响应、不读 HTTP 请求头（需要的请求上下文由调用方经 ctx 注入）。
//   - 需要保留原有 HTTP 状态码语义时，用 newCoreStatusError 显式标注；HTTP 入口用
//     CoreErrorStatus / CoreErrorKey 取值（见 tasks.go 的 writeCoreError）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/expertgroup"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"
)

// ============================================================================
// 结构化返回与错误标注
// ============================================================================

// StartTaskResult 是启动任务的 gin-free 结果。
//
//   - SessionUUID：本次启动创建的执行会话；CLI 直接执行分支为空。
//   - Status：任务被置成的状态。
//   - AlreadyActive：true 表示只把任务推进到 active、不创建执行会话
//     （CLI 直接执行分支，HTTP 入口据此返回 200 而非 202）。
//
// 它取代了旧哨兵 errTaskSkillResponseSent —— 那个哨兵在成功路径也被返回，
// 调用方必须同时用 errors.Is 判断成功与失败，语义含混。
type StartTaskResult struct {
	SessionUUID   string
	Status        string
	AlreadyActive bool
}

// ExecutionModeResult 是设置/切换执行方式的 gin-free 结果。
//
//   - Response：HTTP 入口应写入的响应体（与旧 assignExecutionMode / switchExecutionMode 一致）。
//   - Before / After：切换前后的执行态快照，供 SkillSurface 组装 before/after 字段；
//     核心层读取失败时按 best-effort 留 nil，不影响切换本身。
type ExecutionModeResult struct {
	Before   gin.H
	After    gin.H
	Response gin.H
}

// coreStatusError 让 gin-free 核心层在不写 HTTP 响应的前提下保留原有状态码语义。
// key 为空表示按 err 走 i18n.LocalServerError 的既有映射（i18n.Key）。
type coreStatusError struct {
	status int
	key    string
	code   string
	err    error
}

func (e *coreStatusError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.key
}

func (e *coreStatusError) Unwrap() error { return e.err }

func newCoreStatusError(status int, key, code string, err error) error {
	return &coreStatusError{status: status, key: key, code: code, err: err}
}

// coreErrorAnnotated 返回显式标注了 HTTP 状态码的核心层错误。
func coreErrorAnnotated(err error) (*coreStatusError, bool) {
	var se *coreStatusError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// CoreErrorStatus 返回核心层错误对应的 HTTP 状态码；未显式标注时，
// taskruntime.ErrNotFound 映射为 404，其余回落到 fallback。
func CoreErrorStatus(err error, fallback int) int {
	if se, ok := coreErrorAnnotated(err); ok {
		return se.status
	}
	if errors.Is(err, taskruntime.ErrNotFound) {
		return http.StatusNotFound
	}
	return fallback
}

// CoreErrorKey 返回核心层错误显式约定的 i18n key 与 code；没有显式约定时返回空字符串，
// 调用方应改用 i18n.LocalServerError 让 i18n.Key 按错误文本映射。
func CoreErrorKey(err error) (key string, code string) {
	if se, ok := coreErrorAnnotated(err); ok {
		return se.key, se.code
	}
	return "", ""
}

// coreAutoStartError 表示步骤已推进、但下一步的自动启动失败。
// HTTP 入口保持 200 并把真实原因写入 start_error（与原 completeStep 一致），
// 非 HTTP 入口可直接取 err.Error() 作为文案。
type coreAutoStartError struct{ err error }

func (e *coreAutoStartError) Error() string { return e.err.Error() }
func (e *coreAutoStartError) Unwrap() error { return e.err }

// CoreAutoStartError 返回「步骤已推进但下一步自动启动失败」的真实原因；不是该情形时返回 nil。
func CoreAutoStartError(err error) error {
	var ae *coreAutoStartError
	if errors.As(err, &ae) {
		return ae.err
	}
	return nil
}

// annotateExecutionModeError 复刻旧 writeExecutionModeError 的状态码与文案映射，
// 让 gin-free 核心层在不写响应的前提下保留 404 / 501 / 409 与两个专用文案键。
func annotateExecutionModeError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, taskruntime.ErrVibeModelInvalid):
		return newCoreStatusError(http.StatusBadRequest, "localserver_vibe_model_invalid", "vibe_model_invalid", err)
	case errors.Is(err, taskruntime.ErrNotFound):
		return newCoreStatusError(http.StatusNotFound, "", "", err)
	case errors.Is(err, taskruntime.ErrExecutionModeUnsupported):
		return newCoreStatusError(http.StatusNotImplemented, "", "", err)
	case errors.Is(err, taskruntime.ErrExecutionModeRequired):
		return newCoreStatusError(http.StatusConflict, "localserver_execution_mode_required", "execution_mode_required", err)
	case errors.Is(err, taskruntime.ErrVibeBindingStale):
		return newCoreStatusError(http.StatusConflict, "localserver_vibe_session_stale", "vibe_session_stale", err)
	case errors.Is(err, taskruntime.ErrExecutionModeUnchanged):
		return newCoreStatusError(http.StatusConflict, "localserver_execution_mode_unchanged", "execution_mode_unchanged", err)
	default:
		return newCoreStatusError(http.StatusConflict, "", "", err)
	}
}

// requestIDCtxKey / contextWithRequestID / requestIDFromContext 让 HTTP 入口把
// X-Request-ID 透传给 gin-free 核心（核心不读请求头）。
type requestIDCtxKey struct{}

func contextWithRequestID(ctx context.Context, requestID string) context.Context {
	if strings.TrimSpace(requestID) == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDCtxKey{}, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(requestIDCtxKey{}).(string)
	return value
}

// ============================================================================
// 启动任务
// ============================================================================

// startTaskCore 是启动任务的 gin-free 实现，取代旧 startTaskByUUID。
// HTTP 状态码语义（CLI=200、其他=202、NotFound=404、其他=409）由调用方按标注映射。
func (h *TasksHandler) startTaskCore(ctx context.Context, taskUUID, requestID string, input pipelineAssignmentInput) (StartTaskResult, error) {
	if strings.TrimSpace(taskUUID) == "" {
		return StartTaskResult{}, newCoreStatusError(http.StatusBadRequest, "", "", fmt.Errorf("任务 UUID 不能为空"))
	}
	var mode string
	if err := h.taskDB().QueryRowContext(ctx, `SELECT execution_mode FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&mode); err != nil {
		// 与原实现一致：这里读不到任务一律按 404 处理。
		return StartTaskResult{}, newCoreStatusError(http.StatusNotFound, "", "", err)
	}
	if mode == taskruntime.ExecutionModeCLI {
		// CLI 直接执行由用户在对话框发出第一条指令时才会创建会话，
		// 此处只把任务推进到进行中，不预先生成执行步骤。
		if _, err := h.taskDB().ExecContext(ctx,
			`UPDATE gt_tasks SET status='active', updated_at=? WHERE uuid=? AND status<>'done'`,
			time.Now().UnixMilli(), taskUUID); err != nil {
			return StartTaskResult{}, newCoreStatusError(http.StatusInternalServerError, "", "", err)
		}
		h.notifyTaskChanged(taskUUID)
		h.pushTaskSync(taskUUID)
		return StartTaskResult{SessionUUID: "", Status: "active", AlreadyActive: true}, nil
	}
	if mode != taskruntime.ExecutionModeExpertGroup {
		if err := h.ensureTaskPipelineAssigned(ctx, taskUUID, input); err != nil {
			return StartTaskResult{}, newCoreStatusError(http.StatusConflict, "", "", err)
		}
		if refreshErr := h.refreshPendingPipelineExecution(ctx, taskUUID); refreshErr != nil {
			applog.Warn("[LocalServer] 启动前补齐流水线执行配置失败", "task_uuid", taskUUID, "error", refreshErr.Error())
		}
	}
	sessionUUID, err := h.startTaskExecution(ctx, taskUUID, requestID)
	if err != nil {
		return StartTaskResult{}, err
	}
	h.pushTaskSync(taskUUID)
	return StartTaskResult{SessionUUID: sessionUUID, Status: "active"}, nil
}

// ============================================================================
// 设置 / 切换执行方式
// ============================================================================

// assignExecutionModeCore 是 assignExecutionMode 的 gin-free 实现。
// 入参归一化与校验与原 handler 一致；ReplaceExisting=true 时委托 switchExecutionModeCore。
func (h *TasksHandler) assignExecutionModeCore(ctx context.Context, taskUUID string, input executionModeInput) (ExecutionModeResult, error) {
	input.Mode = taskruntime.NormalizeExecutionMode(input.Mode)
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.PipelineUUID = strings.TrimSpace(input.PipelineUUID)
	input.ExpertGroupUUID = strings.TrimSpace(input.ExpertGroupUUID)
	if err := taskruntime.ValidateExecutionTarget(input.Mode, input.Tool, input.PipelineUUID, input.ExpertGroupUUID); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, taskruntime.ErrExecutionModeUnsupported) {
			status = http.StatusNotImplemented
		}
		return ExecutionModeResult{}, newCoreStatusError(status, "", "", err)
	}
	if input.Mode == taskruntime.ExecutionModeCLI {
		if err := taskruntime.ValidateCLITarget(input.Tool, input.ModelName); err != nil {
			return ExecutionModeResult{}, newCoreStatusError(http.StatusBadRequest, "", "", err)
		}
	}
	// 空 taskUUID 不做本地拦截：交给下游校验，保持与旧 assignExecutionMode 完全一致的状态码
	// （旧实现直接取 c.Param("uuid")，空值最终由 ensureTaskPipelineAssigned 报 404）。
	taskUUID = strings.TrimSpace(taskUUID)
	if input.ReplaceExisting {
		return h.switchExecutionModeCore(ctx, taskUUID, input)
	}
	requestID := requestIDFromContext(ctx)
	switch input.Mode {
	case taskruntime.ExecutionModePipeline:
		if input.PipelineUUID == "" {
			return ExecutionModeResult{}, newCoreStatusError(http.StatusBadRequest, "localserver_pipeline_uuid_required", "pipeline_required", nil)
		}
		if err := h.ensureTaskPipelineAssigned(ctx, taskUUID, pipelineAssignmentInput{
			PipelineUUID: input.PipelineUUID,
			StepConfigs:  input.StepConfigs,
		}); err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		response := gin.H{"assigned": true, "execution_mode": input.Mode, "execution_tool": ""}
		if input.Start {
			sessionUUID, err := h.startTaskExecution(ctx, taskUUID, requestID)
			if err != nil {
				return ExecutionModeResult{}, annotateExecutionModeError(err)
			}
			response["status"] = "active"
			response["session_uuid"] = sessionUUID
		}
		h.notifyTaskChanged(taskUUID)
		h.pushTaskSync(taskUUID)
		return ExecutionModeResult{Response: response}, nil
	case taskruntime.ExecutionModeVibeCoding:
		if err := taskruntime.NewService(h.taskDB(), h.taskRoot).AssignVibeCoding(ctx, taskUUID, input.Tool, input.ModelName, input.Start); err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		if input.Start {
			if err := h.ensureVibeCodingConversation(ctx, taskUUID, i18n.WithLocale(i18n.LocaleFromContext(ctx), "localserver_vibe_coding_assigned_activity"), false); err != nil {
				return ExecutionModeResult{}, newCoreStatusError(http.StatusInternalServerError, "", "", err)
			}
		}
		h.notifyTaskChanged(taskUUID)
		h.pushTaskSync(taskUUID)
		return ExecutionModeResult{Response: gin.H{"assigned": true, "execution_mode": input.Mode, "execution_tool": input.Tool}}, nil
	case taskruntime.ExecutionModeCLI:
		if err := taskruntime.NewService(h.taskDB(), h.taskRoot).AssignCLI(
			ctx, taskUUID, input.Tool, input.ModelName, input.Start,
		); err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		h.notifyTaskChanged(taskUUID)
		h.pushTaskSync(taskUUID)
		return ExecutionModeResult{Response: gin.H{
			"assigned":       true,
			"execution_mode": input.Mode,
			"execution_tool": input.Tool,
			"model_name":     input.ModelName,
		}}, nil
	case taskruntime.ExecutionModeExpertGroup:
		group, err := expertgroup.NewService(h.taskDB()).ValidateReady(ctx, input.ExpertGroupUUID)
		if err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		if err = taskruntime.NewService(h.taskDB(), h.taskRoot).AssignExpertGroup(ctx, taskUUID, group); err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		response := gin.H{"assigned": true, "execution_mode": input.Mode, "expert_group_uuid": input.ExpertGroupUUID}
		if input.Start {
			sessionUUID, startErr := h.startTaskExecution(ctx, taskUUID, requestID)
			if startErr != nil {
				return ExecutionModeResult{}, annotateExecutionModeError(startErr)
			}
			response["status"] = "active"
			response["session_uuid"] = sessionUUID
		}
		h.notifyTaskChanged(taskUUID)
		h.pushTaskSync(taskUUID)
		return ExecutionModeResult{Response: response}, nil
	default:
		return ExecutionModeResult{}, newCoreStatusError(http.StatusBadRequest, "localserver_execution_mode_invalid", "execution_mode_invalid", nil)
	}
}

// switchExecutionModeCore 是 switchExecutionMode 的 gin-free 实现。
//
// 入参归一化与校验由调用方（assignExecutionModeCore / SkillSurface）完成，与原
// switchExecutionMode 的契约保持一致 —— 它自己从不解析 body、也不读 c.Params。
func (h *TasksHandler) switchExecutionModeCore(ctx context.Context, taskUUID string, input executionModeInput) (ExecutionModeResult, error) {
	taskUUID = strings.TrimSpace(taskUUID)
	before, _ := h.executionModeState(ctx, taskUUID)
	replacement := taskruntime.SwitchExecutionInput{
		Mode: input.Mode, Tool: input.Tool, ModelName: input.ModelName,
		RebindExecutionIndex: input.RebindExecutionIndex,
		SelectedPipelineUUID: input.PipelineUUID,
	}
	switch input.Mode {
	case taskruntime.ExecutionModePipeline:
		configRaw, err := json.Marshal(input.StepConfigs)
		if err != nil {
			return ExecutionModeResult{}, newCoreStatusError(http.StatusBadRequest, "", "", err)
		}
		snapshot, err := h.snapshotFromPipeline(ctx, pipelineAssignmentInput{
			PipelineUUID: input.PipelineUUID,
			StepConfigs:  input.StepConfigs,
		})
		if err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		replacement.SelectedPipelineConfigJSON = string(configRaw)
		replacement.Pipeline = &snapshot
	case taskruntime.ExecutionModeExpertGroup:
		group, err := expertgroup.NewService(h.taskDB()).ValidateReady(ctx, input.ExpertGroupUUID)
		if err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
		replacement.ExpertGroup = group
	}

	// 客户端断开不能让进程停掉而数据库只换了一半：校验用请求 ctx，
	// 破坏性部分（context.WithoutCancel）刻意不随请求取消。
	ctx = context.WithoutCancel(ctx)
	switchService := taskruntime.NewService(h.taskDB(), h.taskRoot)
	if err := switchService.ValidateExecutionSwitch(ctx, taskUUID, replacement); err != nil {
		return ExecutionModeResult{}, annotateExecutionModeError(err)
	}
	orchestrator := h.orchestrator()
	if orchestrator == nil {
		return ExecutionModeResult{}, newCoreStatusError(http.StatusServiceUnavailable,
			"localserver_execution_switch_unavailable", "execution_switch_unavailable", nil)
	}
	switchLease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
	if err != nil {
		return ExecutionModeResult{}, annotateExecutionModeError(err)
	}
	defer switchLease.Close()
	// 等待锁期间其他请求可能已换代；重新绑定必须在停止会话前复核，
	// 避免旧页面请求影响刚启动的新执行。
	if input.RebindExecutionIndex != nil {
		if err := switchService.ValidateExecutionSwitch(ctx, taskUUID, replacement); err != nil {
			return ExecutionModeResult{}, annotateExecutionModeError(err)
		}
	}
	// 等价的 gin-free 守卫（取代 guardRemoteExecutionMutation 的响应写入）。
	if block, blockErr := remoteExecutionBlock(ctx, h.taskDB(), taskUUID); blockErr != nil {
		return ExecutionModeResult{}, newCoreStatusError(http.StatusInternalServerError, "", "", blockErr)
	} else if block == "uncertain" {
		return ExecutionModeResult{}, newCoreStatusError(http.StatusConflict,
			"remote_channel_uncertain_action_required", "wechat_uncertain_action_required", nil)
	} else if block != "" {
		return ExecutionModeResult{}, newCoreStatusError(http.StatusConflict,
			"localserver_active_sessions", "task_has_active_sessions", nil)
	}
	if err = switchLease.StopTaskSessions(ctx); err != nil {
		applog.Warn("[execution-switch] 终止当前执行失败", "task_uuid", taskUUID, "error", err)
		return ExecutionModeResult{}, newCoreStatusError(http.StatusConflict,
			"localserver_execution_switch_stop_failed", "execution_switch_stop_failed", nil)
	}
	// 本地切换会删掉当前步骤和会话。先把这一次执行的 Token 与对话封存到云端，
	// 切换事务再把 execution_index +1，后续推送写入新的云端运行记录。
	h.flushTaskExecution(ctx, taskUUID)

	if err = switchService.SwitchExecution(ctx, taskUUID, replacement); err != nil {
		return ExecutionModeResult{}, annotateExecutionModeError(err)
	}
	if input.Mode == taskruntime.ExecutionModeVibeCoding {
		if err = h.ensureVibeCodingConversation(ctx, taskUUID, i18n.WithLocale(i18n.LocaleFromContext(ctx), "localserver_vibe_coding_assigned_activity"), false); err != nil {
			h.pushTaskSync(taskUUID)
			return ExecutionModeResult{}, newCoreStatusError(http.StatusInternalServerError, "", "", err)
		}
	}
	response := gin.H{
		"assigned": true, "switched": true, "execution_mode": input.Mode,
		"execution_tool": input.Tool, "status": "active", "start_status": "started",
	}
	// 在切换提交之后读取新配置。
	build := func() ExecutionModeResult {
		after, _ := h.executionModeState(ctx, taskUUID)
		return ExecutionModeResult{Before: before, After: after, Response: response}
	}
	// 切换只准备新的执行配置；首条用户消息负责创建 Session。Vibe Coding 保持原行为。
	if input.Mode != taskruntime.ExecutionModeVibeCoding {
		response["start_status"] = "awaiting_message"
		response["status"] = "pending"
	}
	if input.Mode == taskruntime.ExecutionModeCLI {
		response["model_name"] = input.ModelName
	}
	if input.Mode == taskruntime.ExecutionModeExpertGroup {
		response["expert_group_uuid"] = input.ExpertGroupUUID
	}
	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	return build(), nil
}

// executionModeState 读取任务当前执行态，形状与 SkillSurface.describeCurrentExecutionCore 一致，
// 供 ExecutionModeResult.Before / After best-effort 填充。
func (h *TasksHandler) executionModeState(ctx context.Context, taskUUID string) (gin.H, error) {
	var mode, tool, status string
	var modelName sql.NullString
	if err := h.taskDB().QueryRowContext(ctx,
		`SELECT execution_mode, execution_tool, status FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&mode, &tool, &status); err != nil {
		return nil, err
	}
	if strings.EqualFold(mode, taskruntime.ExecutionModeCLI) {
		_ = h.taskDB().QueryRowContext(ctx,
			`SELECT model_name FROM gt_task_steps WHERE task_uuid=? AND step_key=? LIMIT 1`,
			taskUUID, taskruntime.CLIDirectStepKey).Scan(&modelName)
	}
	return gin.H{
		"task_uuid":      taskUUID,
		"execution_mode": mode,
		"execution_tool": tool,
		"model_name":     modelName.String,
		"status":         status,
	}, nil
}

// ============================================================================
// 推进步骤
// ============================================================================

// completeStepCore 是 completeStep 的 gin-free 实现，HTTP / CLI Skill / 微信三入口共用。
//
// 收尾三动作（markTaskNotificationsRead + pushTaskSync + notifyTaskChanged）放在核心层，
// 保证技能与微信入口不再漏掉云端投影与通知置读（旧实现三者皆无）。
// 第三个返回值为自动启动的 sessionUUID；自动启动失败时返回 coreAutoStartError（步骤已推进）。
func (h *TasksHandler) completeStepCore(ctx context.Context, taskUUID, stepUUID string, autoStart bool) (taskruntime.AdvanceResult, string, error) {
	orchestrator := h.orchestrator()
	if orchestrator == nil {
		return taskruntime.AdvanceResult{}, "", newCoreStatusError(http.StatusServiceUnavailable,
			"localserver_execution_switch_unavailable", "execution_unavailable", nil)
	}
	executionLease, err := orchestrator.BeginTaskExecution(taskUUID)
	if err != nil {
		return taskruntime.AdvanceResult{}, "", newCoreStatusError(http.StatusConflict, "", "", err)
	}
	defer executionLease.Close()

	result, err := taskruntime.NewService(h.taskDB(), h.taskRoot).CompleteStep(ctx, taskUUID, stepUUID)
	if err != nil {
		// 409；taskruntime.ErrNotFound 由 CoreErrorStatus 映射为 404（与原 handler 相同）。
		return taskruntime.AdvanceResult{}, "", err
	}
	_ = markTaskNotificationsRead(ctx, h.taskDB(), taskUUID, true)
	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	if result.NextStepUUID == "" || !autoStart {
		return *result, "", nil
	}
	sessionUUID, runErr := executionLease.RunStep(ctx, workflow.RunStepOptions{
		TaskUUID: taskUUID, StepUUID: result.NextStepUUID, RecordType: "initial_run",
	})
	if runErr != nil {
		return *result, "", &coreAutoStartError{err: runErr}
	}
	return *result, sessionUUID, nil
}

// taskHasPendingStepAfter 预探测「当前步骤之后是否还有 pending 步骤」。
//
// 为什么需要：taskruntime.CompleteStep 在末步已提交 status='done'（service.go:973），
// 只返回文案无法回滚状态。末步守卫必须前移到调用之前，否则「末步不自动完成任务」无法达成。
func (h *TasksHandler) taskHasPendingStepAfter(ctx context.Context, taskUUID, stepUUID string) (bool, error) {
	db := h.taskDB()
	if db == nil {
		return false, fmt.Errorf("本地任务数据库尚未就绪")
	}
	var stepOrder int
	if err := db.QueryRowContext(ctx,
		`SELECT step_order FROM gt_task_steps WHERE task_uuid=? AND uuid=?`, taskUUID, stepUUID).Scan(&stepOrder); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, taskruntime.ErrNotFound
		}
		return false, err
	}
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM gt_task_steps WHERE task_uuid=? AND step_order>? AND status='pending'`,
		taskUUID, stepOrder).Scan(&pending); err != nil {
		return false, err
	}
	return pending > 0, nil
}
