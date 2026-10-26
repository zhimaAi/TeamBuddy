package localserver

// 远程通道「代为操作本地客户端」技能 共享业务层（计划 §四）
//
// 唯一来源：被 CLI Skill 接口（cli_skill.go 注册的路由）与微信命令快路径（wechat_dispatch.go）共同调用，
// 防止在两处重复实现相同业务与 SQL。
//
// 引用语义：计划 §六明确「一律使用友好 ID」，本文件持有全部规则：
//   - 任务：short_id（沿用 gt_remote_task_aliases），与现有 @ 命令一致；
//   - 项目 / 流水线 / 专家团 / CLI / 模型 / Vibe 工具：每次即时全量查询 + 确定性排序 + 取第 N 项；
//   - 索引越界、列表为空、引用不存在一律硬失败，不取最近项、不默认补全（计划 §5.5）。
//
// 当前文件仅承载 R1~R9 与 W1~W5 所需的最小实现；其余能力（详情补全、字段扩展）按需继续在该文件追加。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/executor"
	"goteams-client/internal/expertgroup"
	"goteams-client/internal/i18n"
	"goteams-client/internal/pipeline"
	"goteams-client/internal/project"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"
)

// wechatEnqueuer 保留旧 5 参数接口以兼容尚未改造的调用点；新代码直接使用 remoteEnqueuer。
//
// 为什么不用 *wechatRemote：wechatRemote 需要 SkillSurface 的格式化能力，SkillSurface 需要
// wechatRemote 的 enqueue —— 这是真实的双向依赖，直接注入具体类型会让「谁先构造」变成隐式契约
// （attachSkillSurface 的装配顺序风险）。依赖倒置后依赖方向单向：wechatRemote 实现本接口，
// SkillSurface 只认该接口。
type wechatEnqueuer interface {
	enqueue(ctx context.Context, sourceID, taskUUID, content string) error
}

// remoteEnqueuer 是通道无关的消息投递接口，支持 channelType 参数。
// SkillSurface 通过该接口把消息投递到任意通道；当前阶段 CLI skill 调用仍固定传 ChannelTypeWechat，
// 后续 chat-reply/messages 接入 channel_type 请求字段后可直接透传。
type remoteEnqueuer interface {
	Enqueue(ctx context.Context, channelType, sourceID, taskUUID, content string) error
}

// i18nGinContextLocaleKey 是 i18n 包内部 `contextLocaleKey` 常量的镜像。
//
// SkillSurface 不直接调 i18n.Middleware，而是合成 gin.Context 调 W2 复用业务实现
// （见 WechatCreateTeamTaskFromTemplate）。在合成 ctx 上设置 locale 时必须使用 i18n
// 内部约定的字符串 key，否则 i18n.T(c, key) 会回退到默认语言（zh-CN），
// W2 写出的 `error` 字段就会与用户语言不一致。
const i18nGinContextLocaleKey = "goteams.i18n.locale"

// SkillSurface 把 cli-skill 接口与微信快路径共用的业务实现集中到一处。
// 构造时显式注入依赖，避免与 TasksHandler 强耦合。
type SkillSurface struct {
	db              *sql.DB
	taskCloudClient func() *cloud.Client
	tasks           *TasksHandler
	// enqueuer：通道无关消息投递窄接口（由 wechatRemote/wecomChannel 等实现）。
	// CLI skill 当前所有调用都默认走 ChannelTypeWechat；后续 chat-reply/messages 接入 channel_type 请求
	// 字段后会切换到读请求中的 channel_type（见 Phase C-3）。
	enqueuer           remoteEnqueuer
	orchestratorGetter func() *workflow.Orchestrator
	vibeSkills         map[string]VibeSkillStatus
	skillsRoot         string
	codexSkillReady    bool
	codexSkillReason   string
	// accountSession：当前云登录态的账号会话 getter（由 server.go 以与 validateSession
	// 完全一致的回退口径注入）。取代原先无人写入的 wechatSessionKey —— 它导致
	// sessionFromCtx 恒 false、微信 /tt 恒提示未登录。
	accountSession func() *browserSession
	// channelStateStore: 通道状态存储（用于 channelType 支持）
	channelStateStore *channelStateStore
}

// NewSkillSurface 装配共享层。
//   - vibeSkills / skillsRoot / codexSkillReason 由 server.go 直接透传，
//     避免 handler 内部隐式读取全局配置。
//   - enqueuer 是窄接口而非 *wechatRemote：装配顺序不再是正确性前提。
//   - accountSession 由 server.go 以 currentSession() + sessionMatchesConfig() 注入。
//   - channelStateStore 用于通道状态读写（支持多渠道）。
func NewSkillSurface(
	db *sql.DB,
	taskCloudClient func() *cloud.Client,
	tasks *TasksHandler,
	enqueuer remoteEnqueuer,
	orchestratorGetter func() *workflow.Orchestrator,
	vibeSkillStatuses []VibeSkillStatus,
	skillsRoot string,
	codexSkillReady bool,
	codexSkillReason string,
	accountSession func() *browserSession,
	channelStateStore *channelStateStore,
) *SkillSurface {
	indexed := make(map[string]VibeSkillStatus, len(vibeSkillStatuses))
	for _, status := range vibeSkillStatuses {
		indexed[status.Tool] = status
	}
	return &SkillSurface{
		db: db, taskCloudClient: taskCloudClient, tasks: tasks,
		enqueuer: enqueuer, orchestratorGetter: orchestratorGetter,
		vibeSkills: indexed, skillsRoot: skillsRoot,
		codexSkillReady: codexSkillReady, codexSkillReason: codexSkillReason,
		accountSession:    accountSession,
		channelStateStore: channelStateStore,
	}
}

// vibeSkill 复用 task_skill.go 中的同名逻辑，避免重复实现。
func (s *SkillSurface) vibeSkill(tool string) (root string, ready bool, reason string) {
	lookup := strings.ToLower(strings.TrimSpace(tool))
	if lookup == taskruntime.ExecutionToolCodexCLI {
		lookup = taskruntime.ExecutionToolCodex
	}
	if status, ok := s.vibeSkills[lookup]; ok {
		return status.Root, status.Ready, status.Reason
	}
	if lookup == taskruntime.ExecutionToolCodex {
		return s.skillsRoot, s.codexSkillReady, s.codexSkillReason
	}
	return "", false, builtinskills.CapabilityReasonInstallationFail
}

// RegisterCLISkillRoutes 把 R1~R9 + W1~W5 全部挂在指定的路由组上。
// 路由组已配置 `/cli-skill` 前缀与 `requireTaskSkillCaller()` 中间件，
// 这里统一使用 `:uuid` 作为路径参数名（计划 §5.6 实测发现 gin 同位置通配符必须同名）。
// 参数承载的是友好 ID（short_id 或列表序号），由各 handler 内部解析。
func (s *SkillSurface) RegisterCLISkillRoutes(r *gin.RouterGroup) {
	r.POST("/chat", s.cliChat)
	r.POST("/chat-reply", s.cliChatReply)
	r.POST("/messages", s.sendMessage)

	// R 系列：查看类。
	r.GET("/lookup/team-work", s.lookupTeamWork)
	r.GET("/lookup/projects", s.lookupProjects)
	r.GET("/lookup/pipelines", s.lookupPipelines)
	r.GET("/lookup/expert-groups", s.lookupExpertGroups)
	r.GET("/lookup/clis", s.lookupCLIs)
	r.GET("/lookup/models", s.lookupModels)
	r.GET("/lookup/vibe-tools", s.lookupVibeTools)
	r.GET("/lookup/tasks", s.lookupTasks)
	r.GET("/lookup/tasks/:uuid", s.lookupTaskDetail)

	// W 系列：写操作（任务 ID 友好值 = short_id 或 task 列表序号）。
	r.POST("/tasks", s.createTaskFromSkill)
	r.POST("/tasks/from-template", s.createTaskFromTemplate)
	r.POST("/tasks/:uuid/start", s.startTaskFromSkill)
	r.POST("/tasks/:uuid/next", s.nextStepFromSkill)
	// 蓝图声明的第 4 个写路由：与 W4 同一语义（末步不改任务状态、不写库），
	// 复用同一 handler 以免两条同义路径各自漂移。
	r.POST("/tasks/:uuid/complete", s.nextStepFromSkill)
	r.PUT("/tasks/:uuid/execution-mode", s.switchExecutionModeFromSkill)
	// W7~W9：任务状态管理。微信侧 /c /r /d 的业务核心（gin-free）
	// 也复用这三个 handler，让 CLI Skill 兜底具备自然语言批量操作任务状态的能力。
	r.POST("/tasks/:uuid/cancel", s.cancelTaskFromSkill)
	r.POST("/tasks/:uuid/resume", s.resumeTaskFromSkill)
	r.POST("/tasks/:uuid/done", s.completeTaskFromSkill)

	// 兼容旧路径（与本次重写前一致），仅供老 Skill 客户端调用。
	r.POST("/tasks/:uuid/activities", s.reportActivity)
	r.GET("/tasks/:uuid", s.skillGetTask)
}

// errSkillBadRequest 统一用 400 抛出，便于用户面对参数缺失或越界时得到明确错误。
type errSkillBadRequest struct{ key string }

func (e *errSkillBadRequest) Error() string { return e.key }

func newSkillBadRequest(key string) error { return &errSkillBadRequest{key: key} }

// errSkillInvalidInput 标记「用户入参非法」类错误（ValidateExecutionTarget /
// ValidateCLITarget 等）。共享核心层只返回原始错误，调用方按入口形态决定响应：
// HTTP 入口按 400 + i18n.Key 映射（与原实现一致），微信入口取本地化文案。
// 不在这里写死 key —— 文案表仍由 i18n.Key 统一维护，避免两处各写一套映射。
type errSkillInvalidInput struct{ err error }

func (e *errSkillInvalidInput) Error() string { return e.err.Error() }

func (e *errSkillInvalidInput) Unwrap() error { return e.err }

func newSkillInvalidInput(err error) error {
	if err == nil {
		return nil
	}
	return &errSkillInvalidInput{err: err}
}

// resolveTaskRef 解析任务友好引用：先用 short_id 试，命中即返回；命中失败再尝试任务列表序号。
// 严格遵循计划 §六「不缓存、不持久化」原则，每次都即时解析。
func (s *SkillSurface) resolveTaskRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", newSkillBadRequest("localserver_skill_task_ref_required")
	}
	n, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || n < 1 {
		return "", newSkillBadRequest("localserver_skill_task_ref_invalid")
	}
	var uuid string
	scanErr := s.db.QueryRowContext(ctx, `SELECT task_uuid FROM gt_remote_task_aliases WHERE short_id=?`, n).Scan(&uuid)
	if scanErr == nil {
		return uuid, s.checkTaskScope(ctx, uuid)
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return "", scanErr
	}
	// 未命中 short_id 时按列表序号解析：与列表 R8 输出（ORDER BY short_id）保持一致。
	scopeSQL, args := taskScopeSQL(s.currentTaskCloudClient(), "t")
	scanErr = s.db.QueryRowContext(ctx, `SELECT t.uuid FROM gt_tasks t
		JOIN gt_remote_task_aliases a ON a.task_uuid = t.uuid
		WHERE `+scopeSQL+` ORDER BY a.short_id ASC LIMIT 1 OFFSET ?`, append(args, n-1)...).Scan(&uuid)
	if scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", newSkillBadRequest("localserver_skill_task_ref_invalid")
		}
		return "", scanErr
	}
	return uuid, nil
}

// resolveProjectIndex 取第 N 个本地项目（确定性排序：created_at, rowid）。
func (s *SkillSurface) resolveProjectIndex(ctx context.Context, index int) (string, error) {
	if index < 1 {
		return "", newSkillBadRequest("localserver_skill_project_index_invalid")
	}
	var uuid string
	err := s.db.QueryRowContext(ctx, `SELECT uuid FROM gt_projects ORDER BY created_at, rowid LIMIT 1 OFFSET ?`, index-1).Scan(&uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", newSkillBadRequest("localserver_skill_project_index_invalid")
	}
	return uuid, err
}

// resolvePipelineByIndex 是流水线索引的唯一解析来源（本地 + 云端缓存段，与
// pipeline.NewService(...).List 输出一致）。UUID 与 CloudPipelineID 必须同源：
// 若各写一套 OFFSET 查询，列表变动时两套索引会各自错位。
func (s *SkillSurface) resolvePipelineByIndex(ctx context.Context, index int) (*pipeline.Pipeline, error) {
	if index < 1 {
		return nil, newSkillBadRequest("localserver_skill_pipeline_index_invalid")
	}
	pipes, err := pipeline.NewService(s.db).List(ctx)
	if err != nil {
		return nil, err
	}
	if index > len(pipes) {
		return nil, newSkillBadRequest("localserver_skill_pipeline_index_invalid")
	}
	return &pipes[index-1], nil
}

// resolvePipelineIndex 返回第 N 条流水线的本地 UUID。
func (s *SkillSurface) resolvePipelineIndex(ctx context.Context, index int) (string, error) {
	item, err := s.resolvePipelineByIndex(ctx, index)
	if err != nil {
		return "", err
	}
	return item.UUID, nil
}

// resolvePipelineCloudIDIndex 返回第 N 条流水线的云端 CloudPipelineID。
//
// 为什么必须单独取它：团队导入接口的 cloud_pipeline_id 按 int64 解析，传本地 UUID 会恒
// pipeline_id_invalid。本地段或云端缓存缺失时属于数据缺失，**硬失败**：绝不回退传本地 UUID
// （那会让用户拿到一个看似成功、实际必然失败的请求）。
func (s *SkillSurface) resolvePipelineCloudIDIndex(ctx context.Context, index int) (string, error) {
	item, err := s.resolvePipelineByIndex(ctx, index)
	if err != nil {
		return "", err
	}
	cloudID := strings.TrimSpace(item.CloudPipelineID)
	if cloudID == "" {
		return "", newSkillBadRequest("localserver_cloud_pipeline_cache_failed")
	}
	return cloudID, nil
}

// resolveExpertGroupIndex 同时支持本地 + 云端缓存段。
func (s *SkillSurface) resolveExpertGroupIndex(ctx context.Context, index int) (string, error) {
	if index < 1 {
		return "", newSkillBadRequest("localserver_skill_expert_group_index_invalid")
	}
	groups, err := expertgroup.NewService(s.db).List(ctx)
	if err != nil {
		return "", err
	}
	if index > len(groups) {
		return "", newSkillBadRequest("localserver_skill_expert_group_index_invalid")
	}
	return groups[index-1].UUID, nil
}

// cliDefinition 是 R5/R6 列表返回时所依赖的精简视图。
type cliDefinition struct {
	Type       string
	Name       string
	Installed  bool
	ExecPath   string
	Version    string
	ModelNames []string
}

func (s *SkillSurface) collectCLIs(ctx context.Context) []cliDefinition {
	items := executor.GetCLIDiscovery(ctx)
	out := make([]cliDefinition, 0, len(items))
	for _, def := range items {
		models := executor.GetCLIModels(ctx, def.Type)
		out = append(out, cliDefinition{
			Type: def.Type, Name: def.Name, Installed: def.Installed,
			ExecPath: def.ExecPath, Version: def.Version, ModelNames: models,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func (s *SkillSurface) resolveCLIIndex(ctx context.Context, index int) (*cliDefinition, error) {
	if index < 1 {
		return nil, newSkillBadRequest("localserver_skill_cli_index_invalid")
	}
	items := s.collectCLIs(ctx)
	if index > len(items) {
		return nil, newSkillBadRequest("localserver_skill_cli_index_invalid")
	}
	return &items[index-1], nil
}

func (s *SkillSurface) resolveModelIndex(ctx context.Context, cliIndex, modelIndex int) (cliType, modelName string, err error) {
	if cliIndex < 1 {
		return "", "", newSkillBadRequest("localserver_skill_cli_index_invalid")
	}
	if modelIndex < 1 {
		return "", "", newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	cliDef, err := s.resolveCLIIndex(ctx, cliIndex)
	if err != nil {
		return "", "", err
	}
	if modelIndex > len(cliDef.ModelNames) {
		return "", "", newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	return cliDef.Type, cliDef.ModelNames[modelIndex-1], nil
}

// resolveModelForCurrentCLI 供「独立 -mid」使用：按任务当前 execution_tool 直接解析模型序号，
// 不经 CLI 序号（用户只给了模型序号时，CLI 由任务当前执行态决定，不猜、不默认取第 1 个）。
func (s *SkillSurface) resolveModelForCurrentCLI(ctx context.Context, taskUUID string, modelIndex int) (cliType, modelName string, err error) {
	tool, toolErr := s.currentTaskExecution(ctx, taskUUID)
	if toolErr != nil {
		return "", "", newSkillBadRequest("localserver_skill_cli_required")
	}
	if tool == "" {
		return "", "", newSkillBadRequest("localserver_skill_cli_required")
	}
	models := executor.GetCLIModels(ctx, tool)
	if modelIndex < 1 {
		return "", "", newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	if modelIndex > len(models) {
		return "", "", newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	return tool, models[modelIndex-1], nil
}

func (s *SkillSurface) resolveVibeToolIndex(ctx context.Context, index int) (string, error) {
	if index < 1 {
		return "", newSkillBadRequest("localserver_skill_vibe_tool_index_invalid")
	}
	tools := taskruntime.VibeCodingTools()
	if index > len(tools) {
		return "", newSkillBadRequest("localserver_skill_vibe_tool_index_invalid")
	}
	return tools[index-1], nil
}

// ============================================================================
// 以下是 lookup（R 系列）接口实现。
// ============================================================================

func lookupItems(items []gin.H) gin.H {
	return gin.H{"items": items, "total": len(items)}
}

func indexItem(index int, payload gin.H) gin.H {
	out := gin.H{"index": index}
	for k, v := range payload {
		out[k] = v
	}
	return out
}

func (s *SkillSurface) lookupTeamWork(c *gin.Context) {
	session, ok := getSession(c)
	if !ok || s.taskCloudClient == nil || s.taskCloudClient() == nil {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}
	client := s.taskCloudClient()
	result, err := client.GetMyWork(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	assigned := make(map[string]localTaskRef)
	cloudSourceKey := pipeline.CloudSourceKey(client.BaseURL())
	rows, err := s.db.Query(`SELECT work_item_type, work_item_id, uuid, status FROM gt_tasks
		WHERE source_type='cloud' AND cloud_source_key=? AND cloud_user_id=?`, cloudSourceKey, session.UserID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ref localTaskRef
		var wt, wi string
		if err := rows.Scan(&wt, &wi, &ref.uuid, &ref.status); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		assigned[wt+":"+wi] = ref
	}
	annotateMyWorkItems(result.List, assigned)
	items := make([]gin.H, 0, len(result.List))
	for idx, raw := range result.List {
		typeVal, _ := raw["type"].(string)
		titleVal, _ := raw["title"].(string)
		wtID := workItemIDString(raw["id"])
		wsID, _ := raw["workspace_id"].(float64)
		wsName, _ := raw["workspace_name"].(string)
		payload := gin.H{
			"type":           typeVal,
			"work_item_id":   wtID,
			"workspace_id":   int64(wsID),
			"workspace_name": wsName,
			"title":          titleVal,
			"has_local_task": raw["local_task_uuid"] != nil,
		}
		if lu, ok := raw["local_task_uuid"].(string); ok {
			payload["local_task_uuid"] = lu
		}
		if ls, ok := raw["local_task_status"].(string); ok {
			payload["local_task_status"] = ls
		}
		items = append(items, indexItem(idx+1, payload))
	}
	c.JSON(http.StatusOK, lookupItems(items))
}

func (s *SkillSurface) lookupProjects(c *gin.Context) {
	items, err := project.NewService(s.db).List(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for idx, item := range items {
		out = append(out, indexItem(idx+1, gin.H{
			"uuid": item.UUID, "name": item.Name, "main_dir": item.MainDir, "local_dir": item.LocalDir,
		}))
	}
	c.JSON(http.StatusOK, lookupItems(out))
}

func (s *SkillSurface) lookupPipelines(c *gin.Context) {
	items, err := pipeline.NewService(s.db).List(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for idx, item := range items {
		out = append(out, indexItem(idx+1, gin.H{
			"uuid": item.UUID, "name": item.Name, "source_type": item.SourceType, "step_count": len(item.Steps),
		}))
	}
	c.JSON(http.StatusOK, lookupItems(out))
}

func (s *SkillSurface) lookupExpertGroups(c *gin.Context) {
	items, err := expertgroup.NewService(s.db).List(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for idx, item := range items {
		out = append(out, indexItem(idx+1, gin.H{
			"uuid": item.UUID, "name": item.Name, "ready": item.Ready, "source_type": item.SourceType,
		}))
	}
	c.JSON(http.StatusOK, lookupItems(out))
}

func (s *SkillSurface) lookupCLIs(c *gin.Context) {
	items := s.collectCLIs(c.Request.Context())
	out := make([]gin.H, 0, len(items))
	for idx, def := range items {
		out = append(out, indexItem(idx+1, gin.H{
			"type": def.Type, "name": def.Name, "installed": def.Installed,
			"version": def.Version, "models": def.ModelNames, "model_count": len(def.ModelNames),
		}))
	}
	c.JSON(http.StatusOK, lookupItems(out))
}

func (s *SkillSurface) lookupModels(c *gin.Context) {
	idx, err := strconv.Atoi(c.Query("cli"))
	if err != nil || idx < 1 {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_cli_index_invalid", "cli_index_invalid")
		return
	}
	def, err := s.resolveCLIIndex(c.Request.Context(), idx)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	items := make([]gin.H, 0, len(def.ModelNames))
	for i, name := range def.ModelNames {
		items = append(items, indexItem(i+1, gin.H{"model": name, "cli_type": def.Type}))
	}
	c.JSON(http.StatusOK, lookupItems(items))
}

func (s *SkillSurface) lookupVibeTools(c *gin.Context) {
	tools := taskruntime.VibeCodingTools()
	items := make([]gin.H, 0, len(tools))
	for idx, tool := range tools {
		_, ready, reason := s.vibeSkill(tool)
		payload := gin.H{
			"tool": tool, "ready": ready, "reason_code": reason, "installed": true,
		}
		if vibeLaunchKind(tool) == "terminal" {
			execPath, err := executor.ResolveVibeCodingExecutable(tool)
			payload["installed"] = err == nil && execPath != ""
		}
		if tool == taskruntime.ExecutionToolQoderIDE {
			delete(payload, "installed")
		}
		items = append(items, indexItem(idx+1, payload))
	}
	c.JSON(http.StatusOK, lookupItems(items))
}

func (s *SkillSurface) lookupTasks(c *gin.Context) {
	q := c.Request.URL.Query()
	sourceType := q.Get("source_type")
	status := q.Get("status")
	scopeSQL, args := taskScopeSQL(s.currentTaskCloudClient(), "t")
	conditions := []string{scopeSQL}
	if status != "" {
		conditions = append(conditions, "t.status=?")
		args = append(args, status)
	}
	if sourceType != "" {
		conditions = append(conditions, "COALESCE(t.source_type, 'local')=?")
		args = append(args, sourceType)
	}
	query := `SELECT a.short_id, t.uuid, t.title, t.status, t.execution_mode, t.execution_tool, t.created_at
		FROM gt_tasks t JOIN gt_remote_task_aliases a ON a.task_uuid=t.uuid`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY a.short_id`
	rows, err := s.db.QueryContext(c.Request.Context(), query, args...)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var shortID int64
		var uuidVal, title, statusVal, mode, tool string
		var createdAt int64
		if err := rows.Scan(&shortID, &uuidVal, &title, &statusVal, &mode, &tool, &createdAt); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		items = append(items, gin.H{
			"index":          len(items) + 1,
			"short_id":       shortID,
			"uuid":           uuidVal,
			"title":          title,
			"status":         statusVal,
			"execution_mode": mode,
			"execution_tool": tool,
			"created_at":     createdAt,
		})
	}
	c.JSON(http.StatusOK, lookupItems(items))
}

func (s *SkillSurface) lookupTaskDetail(c *gin.Context) {
	ref := strings.TrimSpace(c.Param("uuid"))
	taskUUID, err := s.resolveTaskRef(c.Request.Context(), ref)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	detail, err := s.tasks.loadTaskDetail(c.Request.Context(), taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	var shortID int64
	_ = s.db.QueryRowContext(c.Request.Context(), `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, taskUUID).Scan(&shortID)
	if projectUUID, ok := detail["project_uuid"].(string); ok && projectUUID != "" {
		detail["project_index"] = s.indexFromUUID(c.Request.Context(), "project", projectUUID)
	}
	if sel, ok := detail["selected_pipeline_uuid"].(string); ok && sel != "" {
		detail["pipeline_index"] = s.indexFromUUID(c.Request.Context(), "pipeline", sel)
	}
	if eg, ok := detail["selected_expert_group_uuid"].(string); ok && eg != "" {
		detail["expert_group_index"] = s.indexFromUUID(c.Request.Context(), "expert_group", eg)
	}
	detail["short_id"] = shortID
	c.JSON(http.StatusOK, detail)
}

// indexFromUUID 在 lookupTaskDetail 返回前给"关联对象"同时附上友好序号。
func (s *SkillSurface) indexFromUUID(ctx context.Context, kind, uuid string) int {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return 0
	}
	switch kind {
	case "project":
		items, err := project.NewService(s.db).List(ctx)
		if err != nil {
			return 0
		}
		for idx, item := range items {
			if item.UUID == uuid {
				return idx + 1
			}
		}
	case "pipeline":
		items, err := pipeline.NewService(s.db).List(ctx)
		if err != nil {
			return 0
		}
		for idx, item := range items {
			if item.UUID == uuid {
				return idx + 1
			}
		}
	case "expert_group":
		items, err := expertgroup.NewService(s.db).List(ctx)
		if err != nil {
			return 0
		}
		for idx, item := range items {
			if item.UUID == uuid {
				return idx + 1
			}
		}
	}
	return 0
}

// ============================================================================
// 以下是 write（W 系列）接口实现。
// ============================================================================

// createTaskBody 同时承载本地创建与团队导入两种场景，由 work_item_id 是否为空区分。
type createTaskBody struct {
	Title           string              `json:"title"`
	Description     string              `json:"description"`
	ProjectIndex    int                 `json:"project_index"`
	SubprojectIndex []int               `json:"subproject_indexes"`
	WorkDirs        []string            `json:"work_dirs"`
	PipelineIndex   int                 `json:"pipeline_index"`
	ExpertGroupIdx  int                 `json:"expert_group_index"`
	StepConfigs     []map[string]string `json:"step_configs"`
	ExecutionMode   string              `json:"execution_mode"`
	ExecutionTool   string              `json:"execution_tool"`
	ModelName       string              `json:"model_name"`
	Status          string              `json:"status"`
	// 团队导入：只有 work_item_type + work_item_id 都非空时启用。
	WorkItemType string `json:"work_item_type"`
	WorkItemID   string `json:"work_item_id"`
	WorkspaceID  int64  `json:"workspace_id"`
}

func (s *SkillSurface) createTaskFromSkill(c *gin.Context) {
	var body createTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_title_required", "task_title_required")
		return
	}
	if body.WorkItemType != "" || body.WorkItemID != "" {
		s.runImportTeamTask(c, body)
		return
	}
	s.createLocalTaskHandler(c, body)
}

// createLocalTaskHandler 是 HTTP 入口的薄 wrapper：业务全在 createLocalTaskCore，
// 与微信 /ct 走同一份实现（历史事故正是两套孪生实现各自漂移造成的）。
func (s *SkillSurface) createLocalTaskHandler(c *gin.Context, body createTaskBody) {
	taskUUID, err := s.createLocalTaskCore(c.Request.Context(), body)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	s.respondTaskCreated(c, taskUUID)
}

// createLocalTaskCore 是本地任务创建的 gin-free 实现，HTTP 入口（createLocalTaskHandler）
// 与微信快路径（WechatCreateTask）共用。
// 返回新建任务的 UUID；索引越界 / 参数非法等一律返回 errSkillBadRequest，由调用方决定文案形态。
func (s *SkillSurface) createLocalTaskCore(ctx context.Context, body createTaskBody) (string, error) {
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" {
		return "", newSkillBadRequest("localserver_title_required")
	}
	projectUUID, _ := s.maybeResolveProject(ctx, body.ProjectIndex)
	var pipelineUUID string
	if body.PipelineIndex > 0 {
		pu, err := s.resolvePipelineIndex(ctx, body.PipelineIndex)
		if err != nil {
			return "", err
		}
		pipelineUUID = pu
	}
	var expertGroupUUID string
	if body.ExpertGroupIdx > 0 {
		eg, err := s.resolveExpertGroupIndex(ctx, body.ExpertGroupIdx)
		if err != nil {
			return "", err
		}
		expertGroupUUID = eg
	}
	subprojectUUIDs := make([]string, 0, len(body.SubprojectIndex))
	for _, idx := range body.SubprojectIndex {
		projectUUIDValue, err := s.resolveProjectIndex(ctx, idx)
		if err != nil {
			return "", err
		}
		subprojectUUIDs = append(subprojectUUIDs, projectUUIDValue)
	}
	executionMode := taskruntime.NormalizeExecutionMode(body.ExecutionMode)
	executionTool := strings.ToLower(strings.TrimSpace(body.ExecutionTool))
	if executionMode == "" && pipelineUUID != "" {
		executionMode = taskruntime.ExecutionModePipeline
	}
	if err := taskruntime.ValidateExecutionTarget(executionMode, executionTool, pipelineUUID, expertGroupUUID); err != nil {
		return "", newSkillInvalidInput(err)
	}
	if executionMode == taskruntime.ExecutionModeCLI {
		if err := taskruntime.ValidateCLITarget(executionTool, body.ModelName); err != nil {
			return "", newSkillInvalidInput(err)
		}
	}
	configs, err := s.buildStepConfigs(ctx, pipelineUUID, body.StepConfigs)
	if err != nil {
		return "", err
	}
	input := taskruntime.CreateInput{
		SourceType:                 "local",
		Title:                      body.Title,
		Content:                    body.Description,
		ProjectUUID:                projectUUID,
		SubprojectUUIDs:            subprojectUUIDs,
		WorkDirs:                   body.WorkDirs,
		Status:                     defaultIfEmpty(body.Status, "active"),
		ExecutionMode:              executionMode,
		ExecutionTool:              executionTool,
		ExecutionModel:             strings.TrimSpace(body.ModelName),
		SelectedPipelineUUID:       pipelineUUID,
		SelectedExpertGroupUUID:    expertGroupUUID,
		SelectedPipelineConfigJSON: mustMarshal(configs),
	}
	return taskruntime.NewService(s.db, s.tasks.taskRoot).Create(ctx, input)
}

func (s *SkillSurface) buildStepConfigs(ctx context.Context, pipelineUUID string, raw []map[string]string) ([]stepExecutionConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if pipelineUUID == "" {
		return nil, newSkillBadRequest("localserver_skill_step_configs_require_pipeline")
	}
	pipes, err := pipeline.NewService(s.db).Get(ctx, pipelineUUID)
	if err != nil {
		return nil, err
	}
	if pipes == nil {
		return nil, newSkillBadRequest("localserver_skill_pipeline_index_invalid")
	}
	byUUID := make(map[string]int, len(pipes.Steps))
	byCloudStepID := make(map[string]int, len(pipes.Steps))
	for i, st := range pipes.Steps {
		byUUID[st.UUID] = i
		if st.CloudStepID != "" {
			byCloudStepID[st.CloudStepID] = i
		}
	}
	configs := make([]stepExecutionConfig, 0, len(raw))
	for _, item := range raw {
		cfg := stepExecutionConfig{
			CLIType: strings.TrimSpace(item["cli_type"]),
			Model:   strings.TrimSpace(firstNonEmpty(item["model"], item["model_name"])),
		}
		switch {
		case item["step_uuid"] != "":
			if _, ok := byUUID[item["step_uuid"]]; ok {
				cfg.StepUUID = item["step_uuid"]
			}
		case item["source_step_id"] != "":
			if i, ok := byCloudStepID[item["source_step_id"]]; ok {
				cfg.StepUUID = pipes.Steps[i].UUID
				cfg.SourceStepID = item["source_step_id"]
				cfg.CloudStepID = item["source_step_id"]
			}
		case item["cloud_step_id"] != "":
			if i, ok := byCloudStepID[item["cloud_step_id"]]; ok {
				cfg.StepUUID = pipes.Steps[i].UUID
				cfg.CloudStepID = item["cloud_step_id"]
			}
		}
		if cfg.StepUUID == "" {
			return nil, newSkillBadRequest("localserver_skill_step_unknown")
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func (s *SkillSurface) respondTaskCreated(c *gin.Context, taskUUID string) {
	var shortID int64
	_ = s.db.QueryRowContext(c.Request.Context(), `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, taskUUID).Scan(&shortID)
	title := ""
	_ = s.db.QueryRowContext(c.Request.Context(), `SELECT title FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&title)
	c.JSON(http.StatusCreated, gin.H{"task_uuid": taskUUID, "short_id": shortID, "title": title})
}

func (s *SkillSurface) maybeResolveProject(ctx context.Context, idx int) (string, error) {
	if idx == 0 {
		return "", nil
	}
	return s.resolveProjectIndex(ctx, idx)
}

// runImportTeamTask 走现有的 importTeamTask 路径，新建 source_type=cloud 的本地任务。
// 现有 importTeamTask 已经处理 workspace_id 补全、云端流水线缓存同步、cloud_source_key 等，
// 直接复用比 skill 重写更安全。
func (s *SkillSurface) runImportTeamTask(c *gin.Context, body createTaskBody) {
	session, ok := getSession(c)
	if !ok || s.taskCloudClient == nil || s.taskCloudClient() == nil {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}
	if body.WorkItemID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_work_item_id_invalid", "work_item_id_invalid")
		return
	}
	if body.PipelineIndex < 1 {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_pipeline_required_for_team_task", "pipeline_required_for_team_task")
		return
	}
	// 团队导入的 cloud_pipeline_id 必须是**云端流水线 ID**：importTeamTask 按 int64 解析它，
	// 传本地 UUID 会恒 pipeline_id_invalid（历史缺陷）。本地段或云端缓存缺失时硬失败，
	// 绝不回退传本地 UUID。两个解析器都委托 resolvePipelineByIndex，索引同源、不会各自错位。
	cloudPipelineID, err := s.resolvePipelineCloudIDIndex(c.Request.Context(), body.PipelineIndex)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	localPipelineUUID, err := s.resolvePipelineIndex(c.Request.Context(), body.PipelineIndex)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	subprojectUUIDs := make([]string, 0, len(body.SubprojectIndex))
	for _, idx := range body.SubprojectIndex {
		uuid, err := s.resolveProjectIndex(c.Request.Context(), idx)
		if err != nil {
			s.writeSkillError(c, err)
			return
		}
		subprojectUUIDs = append(subprojectUUIDs, uuid)
	}
	// step_configs 按本地流水线 UUID 解析步骤（buildStepConfigs 用 pipeline.Service.Get）。
	stepConfigs := make([]stepExecutionConfig, 0)
	if raw := body.StepConfigs; len(raw) > 0 {
		converted, err := s.buildStepConfigs(c.Request.Context(), localPipelineUUID, raw)
		if err != nil {
			s.writeSkillError(c, err)
			return
		}
		stepConfigs = converted
	}
	stepsConfigForImport := make([]map[string]string, 0, len(stepConfigs))
	for _, cfg := range stepConfigs {
		stepsConfigForImport = append(stepsConfigForImport, map[string]string{
			"cloud_step_id": cfg.CloudStepID,
			"step_uuid":     cfg.StepUUID,
			"cli_type":      cfg.CLIType,
			"model":         cfg.Model,
		})
	}
	fakeBody := importTeamTaskBody{
		WorkItemType:    body.WorkItemType,
		WorkItemID:      body.WorkItemID,
		CloudPipeline:   cloudPipelineID,
		StepConfigs:     stepsConfigForImport,
		WorkDirs:        body.WorkDirs,
		SubprojectUUIDs: subprojectUUIDs,
		Status:          defaultIfEmpty(body.Status, "active"),
	}
	if body.ProjectIndex > 0 {
		if pu, perr := s.resolveProjectIndex(c.Request.Context(), body.ProjectIndex); perr == nil {
			fakeBody.ProjectUUID = pu
		} else {
			s.writeSkillError(c, perr)
			return
		}
	}
	_ = session // 已通过 getSession 校验；为后续扩展保留。

	bodyJSON, _ := json.Marshal(fakeBody)
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyJSON))
	c.Request.ContentLength = int64(len(bodyJSON))
	s.tasks.importTeamTask(c)
}

// importTeamTaskBody 是 importTeamTask 现有 JSON 入参的镜像。
// 真实字段含义见 internal/localserver/tasks.go 中的 importTeamTask。
type importTeamTaskBody struct {
	WorkItemType    string              `json:"work_item_type"`
	WorkItemID      string              `json:"work_item_id"`
	CloudPipeline   string              `json:"cloud_pipeline_id"`
	StepConfigs     []map[string]string `json:"step_configs"`
	WorkDir         string              `json:"work_dir"`
	WorkDirs        []string            `json:"work_dirs"`
	ProjectUUID     string              `json:"project_uuid"`
	SubprojectUUIDs []string            `json:"subproject_uuids"`
	Status          string              `json:"status"`
}

// createTaskFromTemplate W2: 参考已有任务配置创建团队工作。
// 输入：{"template":"<参考任务友好 ID>","work_item":"<我的工作序号>","title":"可选"}
// 继承模板的 project_uuid、关联项目、关联目录、工作目录、流水线以及步骤的 CLI/模型。
func (s *SkillSurface) createTaskFromTemplate(c *gin.Context) {
	session, ok := getSession(c)
	if !ok {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}
	if s.taskCloudClient == nil || s.taskCloudClient() == nil {
		i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_session_expired", "cloud_session_expired")
		return
	}
	var body struct {
		Template   string `json:"template"`
		WorkItemID int    `json:"work_item"`
		Title      string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.WorkItemID < 1 || strings.TrimSpace(body.Template) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_task_ref_required", "skill_task_ref_required")
		return
	}
	templateUUID, err := s.resolveTaskRef(c.Request.Context(), body.Template)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	templateDetail, err := s.tasks.loadTaskDetail(c.Request.Context(), templateUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	pipelineUUID, _ := templateDetail["selected_pipeline_uuid"].(string)
	if pipelineUUID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_template_requires_pipeline", "skill_template_requires_pipeline")
		return
	}
	// 导入接口的 cloud_pipeline_id 必须是云端 ID：模板存的是本地 UUID，必须先换成 CloudPipelineID，
	// 缺失时硬失败（不得回退传本地 UUID，那会恒 pipeline_id_invalid）。
	templatePipe, pipeErr := pipeline.NewService(s.db).Get(c.Request.Context(), pipelineUUID)
	if pipeErr != nil {
		s.writeSkillError(c, pipeErr)
		return
	}
	cloudPipelineID := ""
	if templatePipe != nil {
		cloudPipelineID = strings.TrimSpace(templatePipe.CloudPipelineID)
	}
	// 注意：cloudPipelineID 留空不立即硬失败 —— 本地流水线没有云端 ID 是合法场景
	// （参考任务由 UI 通过 /api/local/tasks + work_item 绑定创建，详见
	// web/.../ConfigureTeamTaskModal.vue:580）。后续根据 cloudPipelineID 是否为空
	// 分支：空 → 走 s.tasks.createTask（UI 兼容路径），非空 → 走 s.tasks.importTeamTask。
	client := s.taskCloudClient()
	work, err := client.GetMyWork(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	if body.WorkItemID > len(work.List) {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_team_work_index_invalid", "skill_team_work_index_invalid")
		return
	}
	picked := work.List[body.WorkItemID-1]
	wType, _ := picked["type"].(string)
	wID := workItemIDString(picked["id"])
	if wType == "" || wID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_team_work_item_missing", "skill_team_work_item_missing")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title, _ = picked["title"].(string)
	}
	var templateSteps []map[string]interface{}
	if steps, ok := templateDetail["steps"].([]map[string]interface{}); ok {
		templateSteps = steps
	}
	subprojectUUIDs := detailSubprojectUUIDs(templateDetail)
	var workDirs []string
	if dirs, ok := templateDetail["work_dirs"].([]string); ok && len(dirs) > 0 {
		workDirs = dirs
	} else if dir, ok := templateDetail["work_dir"].(string); ok && dir != "" {
		workDirs = []string{dir}
	}
	projectUUID, _ := templateDetail["project_uuid"].(string)

	// 本地流水线分支：模板没有云端流水线 ID 时，复用 UI 团队工作创建
	//（POST /api/local/tasks → tasks.go:createTask）—— 本地 pipeline_uuid + 团队工作项
	// 绑定，source_type=cloud，行为与 ConfigureTeamTaskModal.vue 完全一致。
	// 这里的契约关键点：work_item_id 走 workItemIDString 已剥掉 JSON 引号，但 createTask
	// body 的 WorkItemID 是 json.RawMessage、ShouldBindJSON 后由内部 strings.Trim 重新
	// 去掉引号；map 里的字符串值被 json.Marshal 加回引号，刚好对得上；workspace_id 传 0
	// 让 createTask 内部从 GetWorkItems 自动补全（与 UI 提交 form 不传 workspace_id 时
	// 的兜底一致）。
	if cloudPipelineID == "" {
		desc, _ := picked["description"].(string)
		if desc == "" {
			desc, _ = picked["content"].(string)
		}
		localStepConfigs := make([]stepExecutionConfig, 0, len(templateSteps))
		for _, st := range templateSteps {
			cliType, _ := st["cli_type"].(string)
			modelName, _ := st["model_name"].(string)
			if modelName == "" {
				modelName, _ = st["model"].(string)
			}
			stepUUID, _ := st["uuid"].(string)
			if stepUUID == "" {
				continue
			}
			localStepConfigs = append(localStepConfigs, stepExecutionConfig{
				StepUUID:  stepUUID,
				CLIType:   cliType,
				Model:     modelName,
				ModelName: modelName,
			})
		}
		// child_project_uuids 是 subproject_uuids 的旧别名（前端 CreateTaskModal
		// 历史只认 child_project_uuids；createTask body 两个字段都接，保留两者以
		// 兼容配置过子项目的模板任务）。
		childProjects := subprojectUUIDs
		workDir := ""
		if len(workDirs) > 0 {
			workDir = workDirs[0]
		}
		localBody := map[string]interface{}{
			"title":               title,
			"description":         desc,
			"content":             desc,
			"pipeline_uuid":       pipelineUUID,
			"execution_mode":      "pipeline",
			"step_configs":        localStepConfigs,
			"project_uuid":        projectUUID,
			"subproject_uuids":    subprojectUUIDs,
			"child_project_uuids": childProjects,
			"work_dir":            workDir,
			"work_dirs":           workDirs,
			"status":              "pending",
			"work_item_type":      wType,
			"work_item_id":        wID,
			"workspace_id":        int64(0),
			"create_notification": true,
		}
		bodyJSON, _ := json.Marshal(localBody)
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyJSON))
		c.Request.ContentLength = int64(len(bodyJSON))
		s.tasks.createTask(c)
		return
	}

	// 云端流水线分支：保持原有 importTeamTask 路径，cloud_pipeline_id 必须是云端 ID。
	// 模板存的是本地 UUID，必须换成 CloudPipelineID；缺失时已经在上面分支到 createTask。
	stepConfigs := []map[string]string{}
	for _, st := range templateSteps {
		cliType, _ := st["cli_type"].(string)
		modelName, _ := st["model_name"].(string)
		if modelName == "" {
			modelName, _ = st["model"].(string)
		}
		stepKey, _ := st["step_key"].(string)
		stepUUID, _ := st["uuid"].(string)
		if stepKey == "" && stepUUID == "" {
			continue
		}
		stepConfigs = append(stepConfigs, map[string]string{
			"cloud_step_id":  stepKey,
			"step_uuid":      stepUUID,
			"source_step_id": stepKey,
			"cli_type":       cliType,
			"model":          modelName,
		})
	}
	fakeBody := importTeamTaskBody{
		WorkItemType:    wType,
		WorkItemID:      wID,
		CloudPipeline:   cloudPipelineID,
		StepConfigs:     stepConfigs,
		WorkDirs:        workDirs,
		SubprojectUUIDs: subprojectUUIDs,
		Status:          "active",
	}
	if projectUUID != "" {
		fakeBody.ProjectUUID = projectUUID
	}
	_ = title
	_ = session
	bodyJSON, _ := json.Marshal(fakeBody)
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyJSON))
	c.Request.ContentLength = int64(len(bodyJSON))
	s.tasks.importTeamTask(c)
}

// startTaskFromSkill: 任务 ID 由友好 ID 解析后，调用 tasks_core.go 的 gin-free 核心。
// 状态码语义与 HTTP /start 一致：NotFound=404、其他=409，成功=202（CLI 直执=200）。
func (s *SkillSurface) startTaskFromSkill(c *gin.Context) {
	taskUUID, err := s.resolveTaskRef(c.Request.Context(), strings.TrimSpace(c.Param("uuid")))
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	result, err := s.tasks.startTaskCore(c.Request.Context(), taskUUID, c.GetHeader("X-Request-ID"), pipelineAssignmentInput{})
	if err != nil {
		i18n.LocalServerError(c, CoreErrorStatus(err, http.StatusConflict), err)
		return
	}
	if result.AlreadyActive {
		c.JSON(http.StatusOK, gin.H{"status": "active", "session_uuid": ""})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": result.SessionUUID})
}

// nextStepFromSkill: 走 tasks_core.go 的 gin-free 核心（completeStepCore）。
//
// 末步守卫必须**在调用之前**：taskruntime.CompleteStep 在末步已把 gt_tasks.status 提交为
// done（service.go:973），事后只能改文案、状态已无法回滚。因此先用 taskHasPendingStepAfter
// 预探测，无后续 pending 步骤时直接返回，**不调用 CompleteStep、不写库**（计划 §一决策）。
func (s *SkillSurface) nextStepFromSkill(c *gin.Context) {
	ctx := c.Request.Context()
	ref := strings.TrimSpace(c.Param("uuid"))
	taskUUID, err := s.resolveTaskRef(ctx, ref)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	stepUUID, err := s.currentStepUUID(ctx, taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusNotFound, err)
		return
	}
	if stepUUID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_no_active_step", "no_active_step")
		return
	}
	hasNext, probeErr := s.tasks.taskHasPendingStepAfter(ctx, taskUUID, stepUUID)
	if probeErr != nil {
		i18n.LocalServerError(c, http.StatusConflict, probeErr)
		return
	}
	if !hasNext {
		// 末步：不调用 CompleteStep，任务与步骤状态原样保留。
		c.JSON(http.StatusOK, gin.H{
			"task_done":      true,
			"next_step_uuid": "",
			"message":        i18n.Format(c, "localserver_skill_task_last_step", nil),
			"auto_started":   false,
		})
		return
	}
	result, sessionUUID, err := s.tasks.completeStepCore(ctx, taskUUID, stepUUID, true)
	if err != nil {
		if startErr := CoreAutoStartError(err); startErr != nil {
			// 步骤已推进成功，只是下一步自动启动失败：与原实现一致，200 + start_error。
			c.JSON(http.StatusOK, gin.H{
				"task_done":      result.TaskDone,
				"next_step_uuid": result.NextStepUUID,
				"auto_started":   false,
				"start_error":    startErr.Error(),
			})
			return
		}
		i18n.LocalServerError(c, http.StatusConflict, err)
		return
	}
	response := gin.H{
		"task_done":      result.TaskDone,
		"next_step_uuid": result.NextStepUUID,
		"auto_started":   false,
	}
	if sessionUUID != "" {
		response["session_uuid"] = sessionUUID
		response["auto_started"] = true
	}
	c.JSON(http.StatusOK, response)
}

func (s *SkillSurface) currentStepUUID(ctx context.Context, taskUUID string) (string, error) {
	var stepUUID string
	err := s.db.QueryRowContext(ctx, `SELECT current_step_uuid FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&stepUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("任务不存在")
	}
	return stepUUID, err
}

// switchExecutionModeFromSkill: W5 单次绑定 —— 入参在这里绑定一次，随后交给共享的
// resolveSwitchInputCore 解析友好序号、再由 tasks_core.go 的 gin-free 核心执行。
//
// 历史缺陷（本次修复）：旧实现走 applyExecutionModeByUUID → assignExecutionMode，后者重新
// ShouldBindJSON 一次（body 已被读空 → EOF/400），并用 append 追加 c.Params 的 uuid，
// 而 gin 的 Params.ByName 取**第一个**匹配 → c.Param("uuid") 拿到的是友好 ID 而非 taskUUID。
// 现在核心层收显式 taskUUID，既不再读 body，也不再借道 c.Params。
func (s *SkillSurface) switchExecutionModeFromSkill(c *gin.Context) {
	ctx := c.Request.Context()
	ref := strings.TrimSpace(c.Param("uuid"))
	taskUUID, err := s.resolveTaskRef(ctx, ref)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	var body struct {
		Mode            string `json:"mode"`
		Tool            string `json:"tool"`
		ModelName       string `json:"model_name"`
		CLIIndex        int    `json:"cli_index"`
		ModelIndex      int    `json:"model_index"`
		PipelineIndex   int    `json:"pipeline_index"`
		ExpertGroupIdx  int    `json:"expert_group_index"`
		VibeToolIndex   int    `json:"vibe_tool_index"`
		ReplaceExisting bool   `json:"replace_existing"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	input, err := s.resolveSwitchInputCore(ctx, taskUUID, WechatSwitchInput{
		Ref: taskUUID, Mode: body.Mode, Tool: body.Tool, ModelName: body.ModelName,
		CLIIndex: body.CLIIndex, ModelIndex: body.ModelIndex,
		PipelineIdx: body.PipelineIndex, ExpertGroupIdx: body.ExpertGroupIdx,
		VibeIdx: body.VibeToolIndex, ReplaceExisting: body.ReplaceExisting,
	})
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	previous, previousErr := s.describeCurrentExecutionCore(ctx, taskUUID)
	if previousErr != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, previousErr)
		return
	}
	result, err := s.tasks.assignExecutionModeCore(contextWithRequestID(ctx, c.GetHeader("X-Request-ID")), taskUUID, input)
	if err != nil {
		s.writeCoreError(c, err)
		return
	}
	current, _ := s.describeCurrentExecutionCore(ctx, taskUUID)
	if current == nil && result.After != nil {
		current = result.After
	}
	c.JSON(http.StatusOK, gin.H{
		"assigned": true,
		"switched": body.ReplaceExisting,
		"before":   previous,
		"after":    current,
	})
}

// resolveSwitchInputCore 把「友好序号入参」解析为可直接交给 gin-free 核心的 executionModeInput。
// HTTP W5 与微信 /swm /swr /swcli 共用同一份规则（计划 §5.5：缺参硬失败、不默认补全）。
func (s *SkillSurface) resolveSwitchInputCore(ctx context.Context, taskUUID string, in WechatSwitchInput) (executionModeInput, error) {
	mode := taskruntime.NormalizeExecutionMode(in.Mode)
	if mode == "" {
		return executionModeInput{}, newSkillBadRequest("localserver_execution_mode_required")
	}
	tool := strings.ToLower(strings.TrimSpace(in.Tool))
	modelName := strings.TrimSpace(in.ModelName)
	var pipelineUUID string
	var expertGroupUUID string
	switch mode {
	case taskruntime.ExecutionModeCLI:
		if in.CLIIndex > 0 {
			// -cid=C -mid=M：CLI 序号决定 tool，模型序号在其模型列表内解析。
			cliType, resolvedModel, err := s.resolveModelIndex(ctx, in.CLIIndex, in.ModelIndex)
			if err != nil {
				return executionModeInput{}, err
			}
			tool = cliType
			modelName = resolvedModel
		} else if in.ModelIndex > 0 && tool == "" {
			// 独立 -mid=M：CLI 沿用任务当前执行方式，不默认取列表第 1 项。
			cliType, resolvedModel, err := s.resolveModelForCurrentCLI(ctx, taskUUID, in.ModelIndex)
			if err != nil {
				return executionModeInput{}, err
			}
			tool = cliType
			modelName = resolvedModel
		} else if tool == "" {
			currentTool, currentErr := s.currentTaskExecution(ctx, taskUUID)
			if currentErr != nil || currentTool == "" {
				return executionModeInput{}, newSkillBadRequest("localserver_skill_cli_required")
			}
			tool = currentTool
		}
	case taskruntime.ExecutionModePipeline:
		if in.PipelineIdx == 0 {
			return executionModeInput{}, newSkillBadRequest("localserver_skill_pipeline_index_required")
		}
		pu, err := s.resolvePipelineIndex(ctx, in.PipelineIdx)
		if err != nil {
			return executionModeInput{}, err
		}
		pipelineUUID = pu
		tool = ""
		modelName = ""
	case taskruntime.ExecutionModeExpertGroup:
		if in.ExpertGroupIdx == 0 {
			return executionModeInput{}, newSkillBadRequest("localserver_skill_expert_group_index_required")
		}
		eg, err := s.resolveExpertGroupIndex(ctx, in.ExpertGroupIdx)
		if err != nil {
			return executionModeInput{}, err
		}
		expertGroupUUID = eg
	case taskruntime.ExecutionModeVibeCoding:
		if in.VibeIdx > 0 {
			vt, err := s.resolveVibeToolIndex(ctx, in.VibeIdx)
			if err != nil {
				return executionModeInput{}, err
			}
			tool = vt
		}
		if tool == "" {
			return executionModeInput{}, newSkillBadRequest("localserver_skill_vibe_tool_required")
		}
	}
	if err := taskruntime.ValidateExecutionTarget(mode, tool, pipelineUUID, expertGroupUUID); err != nil {
		return executionModeInput{}, newSkillInvalidInput(err)
	}
	if mode == taskruntime.ExecutionModeCLI {
		if err := taskruntime.ValidateCLITarget(tool, modelName); err != nil {
			return executionModeInput{}, newSkillInvalidInput(err)
		}
	}
	return executionModeInput{
		Mode:            mode,
		Tool:            tool,
		ModelName:       modelName,
		PipelineUUID:    pipelineUUID,
		ExpertGroupUUID: expertGroupUUID,
		ReplaceExisting: in.ReplaceExisting,
	}, nil
}

// writeCoreError 把 gin-free 核心层返回的错误映射为 CLI Skill 的 HTTP 响应。
// 与 tasks.go 的同名映射保持同一口径：显式标注 key 的错误按 key 输出，其余交给
// i18n.LocalServerError 依错误文本映射（不重复实现一套文案表）。
func (s *SkillSurface) writeCoreError(c *gin.Context, err error) {
	status := CoreErrorStatus(err, http.StatusConflict)
	if key, code := CoreErrorKey(err); key != "" {
		i18n.Error(c, status, key, code)
		return
	}
	i18n.LocalServerError(c, status, err)
}

func (s *SkillSurface) currentTaskExecution(ctx context.Context, taskUUID string) (string, error) {
	var tool string
	err := s.db.QueryRowContext(ctx, `SELECT execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&tool)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("任务不存在")
	}
	return strings.ToLower(strings.TrimSpace(tool)), err
}

// describeCurrentExecutionCore 是 describeCurrentExecution 的 ctx-only 版本，供微信通道复用。
func (s *SkillSurface) describeCurrentExecutionCore(ctx context.Context, taskUUID string) (gin.H, error) {
	var mode, tool, status string
	var modelName sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT execution_mode, execution_tool, status FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&mode, &tool, &status)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(mode, taskruntime.ExecutionModeCLI) {
		_ = s.db.QueryRowContext(ctx,
			`SELECT model_name FROM gt_task_steps WHERE task_uuid=? AND step_key=? LIMIT 1`, taskUUID, taskruntime.CLIDirectStepKey).Scan(&modelName)
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
// 与旧版 cli_skill.go 平行的兼容性实现，仅保留最简功能用于老的 chat 兜底链路。
// ============================================================================

func (s *SkillSurface) cliChat(c *gin.Context) {
	var body struct {
		Message       string `json:"message"`
		BoundTaskUUID string `json:"bound_task_uuid"`
		CLIType       string `json:"cli_type"`
		CLIModel      string `json:"cli_model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.BoundTaskUUID != "" {
		if err := s.checkTaskScope(c.Request.Context(), body.BoundTaskUUID); err != nil {
			s.writeSkillError(c, err)
			return
		}
	}

	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_content_invalid", "skill_activity_content_invalid")
		return
	}
	context := gin.H{"message": body.Message}
	if body.BoundTaskUUID != "" {
		detail, err := s.tasks.loadTaskDetail(c.Request.Context(), body.BoundTaskUUID)
		if err == nil {
			context["bound_task"] = gin.H{
				"uuid":           body.BoundTaskUUID,
				"title":          detail["title"],
				"status":         detail["status"],
				"execution_mode": detail["execution_mode"],
				"execution_tool": detail["execution_tool"],
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"action":          "process",
		"message":         body.Message,
		"bound_task_uuid": body.BoundTaskUUID,
		"context":         context,
		"instruction":     "优先调用本地 /api/local/cli-skill/lookup 与 /tasks 接口处理客户端互动类请求；任务执行类请直接启动 CLI。",
	})
}

func (s *SkillSurface) cliChatReply(c *gin.Context) {
	var body struct {
		Content     string `json:"content"`
		TaskUUID    string `json:"task_uuid"`
		ReplyType   string `json:"reply_type"`
		SessionUUID string `json:"session_uuid"`
		ChannelType string `json:"channel_type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.TaskUUID != "" {
		if err := s.checkTaskScope(c.Request.Context(), body.TaskUUID); err != nil {
			s.writeSkillError(c, err)
			return
		}
	}

	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_content_invalid", "skill_activity_content_invalid")
		return
	}
	replyType := strings.TrimSpace(body.ReplyType)
	if replyType == "" {
		replyType = "direct_message"
	}
	channelType := NormalizeChannelType(body.ChannelType)
	var finalContent string
	switch replyType {
	case "task_result":
		var shortID int64
		if body.TaskUUID != "" {
			_ = s.db.QueryRowContext(c.Request.Context(),
				`SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, body.TaskUUID).Scan(&shortID)
		}
		finalContent = i18n.Format(c, "remote_channel_result",
			i18n.Params{"ID": shortID, "Task": "", "Result": body.Content})
	default:
		finalContent = body.Content
	}
	sourceID := "cli-chat:"
	if body.TaskUUID != "" {
		sourceID += body.TaskUUID
	} else {
		sourceID += "no-task"
	}
	if err := s.enqueuer.Enqueue(c.Request.Context(), channelType, sourceID, body.TaskUUID, finalContent); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "channel_type": channelType})
}

func (s *SkillSurface) sendMessage(c *gin.Context) {
	var body struct {
		Content     string `json:"content"`
		TaskUUID    string `json:"task_uuid"`
		ChannelType string `json:"channel_type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	if body.TaskUUID != "" {
		if err := s.checkTaskScope(c.Request.Context(), body.TaskUUID); err != nil {
			s.writeSkillError(c, err)
			return
		}
	}

	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_content_invalid", "skill_activity_content_invalid")
		return
	}
	taskUUID := strings.TrimSpace(body.TaskUUID)
	channelType := NormalizeChannelType(body.ChannelType)
	sourceID := "cli-skill:message"
	if taskUUID != "" {
		sourceID = "cli-skill:" + taskUUID
	}
	if err := s.enqueuer.Enqueue(c.Request.Context(), channelType, sourceID, taskUUID, body.Content); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "channel_type": channelType})
}

func (s *SkillSurface) reportActivity(c *gin.Context) {
	taskUUID := strings.TrimSpace(c.Param("uuid"))
	if taskUUID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_task_uuid_invalid", "task_uuid_invalid")
		return
	}

	if err := s.checkTaskScope(c.Request.Context(), taskUUID); err != nil {
		s.writeSkillError(c, err)
		return
	}
	var body struct {
		Type        string `json:"type"`
		Content     string `json:"content"`
		Tool        string `json:"tool"`
		ThreadID    string `json:"thread_id"`
		ChannelType string `json:"channel_type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	body.Type = strings.TrimSpace(body.Type)
	if body.Type != "question" && body.Type != "result" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_type_invalid", "skill_activity_type_invalid")
		return
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_skill_activity_content_invalid", "skill_activity_content_invalid")
		return
	}
	now := time.Now().UnixMilli()
	progressUUID := uuid.NewString()
	var executionMode, executionTool string
	if err := s.db.QueryRowContext(c.Request.Context(),
		`SELECT execution_mode, execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&executionMode, &executionTool); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
			return
		}
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	recordType, prompt, result := "initial_run", "", body.Content
	if body.Type == "question" {
		recordType, prompt, result = "user_question", body.Content, ""
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	cliType := body.Tool
	if cliType == "" {
		cliType = executionTool
	}
	if _, err := tx.ExecContext(c.Request.Context(),
		`INSERT INTO gt_task_progress (uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, session_uuid, record_type, user_prompt, cli_type, model_name, status, final_result, execution_mode, external_session_id, activity_actor, activity_kind, created_at, started_at, finished_at)
		 VALUES (?, ?, '', '', '', ?, ?, ?, ?, 'success', ?, ?, ?, 'tool', 'skill_activity', ?, ?, ?)`,
		progressUUID, taskUUID, recordType, prompt, cliType, result, executionMode, body.ThreadID, now, now, now); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(),
		`UPDATE gt_tasks SET updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if body.Type == "result" {
		var title string
		_ = s.db.QueryRowContext(c.Request.Context(),
			`SELECT title FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&title)
		var shortID int64
		_ = s.db.QueryRowContext(c.Request.Context(),
			`SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, taskUUID).Scan(&shortID)
		messageBody := i18n.Format(c, "remote_channel_result",
			i18n.Params{"ID": shortID, "Task": title, "Result": body.Content})
		channelType := NormalizeChannelType(body.ChannelType)
		_ = s.enqueuer.Enqueue(c.Request.Context(), channelType, "cli-skill:"+taskUUID, taskUUID, messageBody)
	}
	c.JSON(http.StatusCreated, gin.H{
		"uuid":       progressUUID,
		"type":       body.Type,
		"content":    body.Content,
		"tool":       cliType,
		"thread_id":  body.ThreadID,
		"created_at": now,
	})
}

func (s *SkillSurface) skillGetTask(c *gin.Context) {
	taskUUID := strings.TrimSpace(c.Param("uuid"))
	if taskUUID == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_task_uuid_invalid", "task_uuid_invalid")
		return
	}

	if err := s.checkTaskScope(c.Request.Context(), taskUUID); err != nil {
		s.writeSkillError(c, err)
		return
	}
	detail, err := s.tasks.loadTaskDetail(c.Request.Context(), taskUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
			return
		}
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": detail})
}

func (s *SkillSurface) writeSkillError(c *gin.Context, err error) {
	var statusErr *coreStatusError
	if errors.As(err, &statusErr) {
		s.writeCoreError(c, err)
		return
	}
	var badRequest *errSkillBadRequest
	if errors.As(err, &badRequest) {
		i18n.Error(c, http.StatusBadRequest, badRequest.key, "skill_request_invalid")
		return
	}
	// 用户入参非法（ValidateExecutionTarget / ValidateCLITarget 等）：与原实现一致走
	// 400 + i18n.LocalServerError，让 i18n.Key 统一映射文案，不在共享核心层写死 key。
	var invalidInput *errSkillInvalidInput
	if errors.As(err, &invalidInput) {
		i18n.LocalServerError(c, http.StatusBadRequest, invalidInput.err)
		return
	}
	i18n.LocalServerError(c, http.StatusInternalServerError, err)
}

// ============================================================================
// 微信通道专用格式化方法（计划 §五 指令对照）。
// 调用方：wechat_dispatch.go 中的 handleMessage。
// 这些方法只在内部复用了与 lookupXxx 同一组的 service 查询（SQL 共享），
// 但输出层完全独立：i18n 拼装为微信自然语言，而不是 JSON。
// ============================================================================

func (s *SkillSurface) WechatFormatTeamWork(ctx context.Context, showAll bool) (string, error) {
	if s.taskCloudClient == nil || s.taskCloudClient() == nil {
		return s.trFromCtx(ctx, "localserver_skill_cloud_login_required", nil), nil
	}
	client := s.taskCloudClient()
	session, ok := s.sessionFromCtx(ctx)
	if !ok {
		return s.trFromCtx(ctx, "localserver_skill_cloud_login_required", nil), nil
	}
	result, err := client.GetMyWork(ctx)
	if err != nil {
		return "", err
	}
	assigned := make(map[string]localTaskRef)
	cloudSourceKey := pipeline.CloudSourceKey(client.BaseURL())
	rows, _ := s.db.QueryContext(ctx, `SELECT work_item_type, work_item_id, uuid, status FROM gt_tasks
		WHERE source_type='cloud' AND cloud_source_key=? AND cloud_user_id=?`, cloudSourceKey, session.UserID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ref localTaskRef
			var wt, wi string
			if scanErr := rows.Scan(&wt, &wi, &ref.uuid, &ref.status); scanErr == nil {
				assigned[wt+":"+wi] = ref
			}
		}
	}
	annotateMyWorkItems(result.List, assigned)

	// 按 is_done 过滤：showAll 为 false 时只保留 is_done=false 的项。
	// is_done 字段在云端响应中可能是 bool，也可能是字符串 "true"/"false"，
	// 这里统一按真值判断。
	if !showAll {
		filtered := make([]map[string]interface{}, 0)
		for _, item := range result.List {
			if isMyWorkItemDone(item) {
				continue
			}
			filtered = append(filtered, item)
		}
		result.List = filtered
	}

	lines := make([]string, 0, len(result.List))
	for _, raw := range result.List {
		title, _ := raw["title"].(string)
		wType, _ := raw["type"].(string)
		ttid := workItemIDString(raw["id"])
		marker := ""
		if raw["local_task_uuid"] != nil {
			marker = "*"
		}
		// 显示用 ttid（云端 work item 的对外名）作为唯一可读标识 —— 与 /ctt -ttid=
		// 完全同源，用户从 /tt 输出复制即可建任务，避免「列表序号会随过滤重排
		// 漂移」导致的引用错位。* 为已建本地任务的前置标记；空 marker 时第一
		// 段是空白（与「ttid：」对齐），两种状态视觉一致。
		lines = append(lines, fmt.Sprintf("%s ttid：<%s>  %s  (%s)", marker, ttid, title, wType))
	}
	if len(lines) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_team_work_empty", nil), nil
	}
	return s.trFromCtx(ctx, "localserver_skill_team_work_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatProjects(ctx context.Context) (string, error) {
	items, err := project.NewService(s.db).List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_projects_empty", nil), nil
	}
	lines := make([]string, 0, len(items))
	for i, item := range items {
		lines = append(lines, fmt.Sprintf("%d  %s", i+1, item.Name))
	}
	return s.trFromCtx(ctx, "localserver_skill_projects_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatPipelines(ctx context.Context) (string, error) {
	items, err := pipeline.NewService(s.db).List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_pipelines_empty", nil), nil
	}
	lines := make([]string, 0, len(items))
	for i, item := range items {
		lines = append(lines, fmt.Sprintf("%d  %s  (%s, %d 步)", i+1, item.Name, item.SourceType, len(item.Steps)))
	}
	return s.trFromCtx(ctx, "localserver_skill_pipelines_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatExpertGroups(ctx context.Context) (string, error) {
	items, err := expertgroup.NewService(s.db).List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_expert_groups_empty", nil), nil
	}
	lines := make([]string, 0, len(items))
	for i, item := range items {
		status := s.trFromCtx(ctx, "localserver_skill_expert_group_not_ready", nil)
		if item.Ready {
			status = s.trFromCtx(ctx, "localserver_skill_expert_group_ready", nil)
		}
		lines = append(lines, fmt.Sprintf("%d  %s  (%s, %s)", i+1, item.Name, item.SourceType, status))
	}
	return s.trFromCtx(ctx, "localserver_skill_expert_groups_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatCLIs(ctx context.Context) (string, error) {
	items := s.collectCLIs(ctx)
	if len(items) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_clis_empty", nil), nil
	}
	lines := make([]string, 0, len(items))
	for i, def := range items {
		status := s.trFromCtx(ctx, "localserver_skill_cli_installed", nil)
		if !def.Installed {
			status = s.trFromCtx(ctx, "localserver_skill_cli_missing", nil)
		}
		lines = append(lines, fmt.Sprintf("%d  %s  %s  (%d 模型)", i+1, def.Name, status, len(def.ModelNames)))
	}
	return s.trFromCtx(ctx, "localserver_skill_clis_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatModels(ctx context.Context, cliIndex int) (string, error) {
	def, err := s.resolveCLIIndex(ctx, cliIndex)
	if err != nil {
		key := "localserver_skill_cli_index_invalid"
		if errors.Is(err, &errSkillBadRequest{key: "localserver_skill_model_index_invalid"}) {
			key = "localserver_skill_model_index_invalid"
		}
		return s.trFromCtx(ctx, key, nil), err
	}
	if len(def.ModelNames) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_models_empty", i18n.Params{"CLI": def.Name}), nil
	}
	lines := make([]string, 0, len(def.ModelNames))
	for i, name := range def.ModelNames {
		lines = append(lines, fmt.Sprintf("%d  %s", i+1, name))
	}
	return s.trFromCtx(ctx, "localserver_skill_models_list", i18n.Params{
		"CLI":   def.Name,
		"Items": strings.Join(lines, "\n\n"),
	}), nil
}

func (s *SkillSurface) WechatFormatVibeTools(ctx context.Context) (string, error) {
	tools := taskruntime.VibeCodingTools()
	if len(tools) == 0 {
		return s.trFromCtx(ctx, "localserver_skill_vibe_tools_empty", nil), nil
	}
	lines := make([]string, 0, len(tools))
	for i, tool := range tools {
		_, ready, _ := s.vibeSkill(tool)
		status := s.trFromCtx(ctx, "localserver_skill_vibe_tool_not_ready", nil)
		if ready {
			status = s.trFromCtx(ctx, "localserver_skill_vibe_tool_ready", nil)
		}
		lines = append(lines, fmt.Sprintf("%d  %s  %s", i+1, tool, status))
	}
	return s.trFromCtx(ctx, "localserver_skill_vibe_tools_list", i18n.Params{"Items": strings.Join(lines, "\n\n")}), nil
}

func (s *SkillSurface) WechatFormatTaskDetail(ctx context.Context, ref string) (string, error) {
	taskUUID, err := s.resolveTaskRef(ctx, ref)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	detail, err := s.tasks.loadTaskDetail(ctx, taskUUID)
	if err != nil {
		return "", err
	}
	title := pickStringDetail(detail, "title")
	status := pickStringDetail(detail, "status")
	mode := pickStringDetail(detail, "execution_mode")
	tool := pickStringDetail(detail, "execution_tool")
	workDir := pickStringDetail(detail, "work_dir")
	projectUUID := pickStringDetail(detail, "project_uuid")
	pipelineUUID := pickStringDetail(detail, "selected_pipeline_uuid")
	expertGroupUUID := pickStringDetail(detail, "selected_expert_group_uuid")
	var pipelineName, expertGroupName, projectName string
	if name, err := s.indexFromUUIDOrName(ctx, "pipeline", pipelineUUID); err == nil {
		pipelineName = name
	}
	if name, err := s.indexFromUUIDOrName(ctx, "expert_group", expertGroupUUID); err == nil {
		expertGroupName = name
	}
	if name, err := s.indexFromUUIDOrName(ctx, "project", projectUUID); err == nil {
		projectName = name
	}
	var shortID int64
	_ = s.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, taskUUID).Scan(&shortID)
	lines := []string{
		fmt.Sprintf("@%d  %s  %s", shortID, title, status),
		fmt.Sprintf("mode=%s tool=%s", mode, tool),
	}
	if projectName != "" {
		lines = append(lines, "项目 "+projectName)
	}
	if pipelineName != "" {
		lines = append(lines, "流水线 "+pipelineName)
	}
	if expertGroupName != "" {
		lines = append(lines, "专家团 "+expertGroupName)
	}
	if workDir != "" {
		lines = append(lines, "工作目录 "+workDir)
	}
	return strings.Join(lines, "\n\n"), nil
}

// WechatNextStepByRef 进入下一步；末步时**不调用 CompleteStep、不写库**，仅返回说明文本
// （计划 §一决策）。
//
// 守卫必须前移：taskruntime.CompleteStep 在末步已提交 status='done'（service.go:973），
// 事后只能改文案、状态已无法回滚。因此先用 taskHasPendingStepAfter 预探测。
// 如果任务还没有进行中的步骤（current_step_uuid 为空），自动激活第一步并启动 CLI 会话。
// 如果任务有隐式步骤（如 CLI 直接模式），自动启动 CLI 会话。
func (s *SkillSurface) WechatNextStepByRef(ctx context.Context, ref string) (string, error) {
	taskUUID, err := s.resolveTaskRef(ctx, ref)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	// 获取当前步骤 UUID 和执行模式
	var stepUUID, executionMode string
	err = s.db.QueryRowContext(ctx, `SELECT current_step_uuid, execution_mode FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&stepUUID, &executionMode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("任务不存在")
	}
	if err != nil {
		return "", err
	}
	if stepUUID == "" {
		// 任务还没有进行中的步骤，尝试激活第一步并启动 CLI
		orchestrator := s.orchestratorGetter()
		if orchestrator == nil {
			return "", fmt.Errorf("任务执行服务尚未就绪")
		}
		executionLease, err := orchestrator.BeginTaskExecution(taskUUID)
		if err != nil {
			return "", err
		}
		defer executionLease.Close()
		startedStepUUID, startErr := taskruntime.NewService(s.db, s.tasks.taskRoot).Start(ctx, taskUUID)
		if startErr != nil {
			return "", startErr
		}
		if startedStepUUID == "" {
			// 没有可用的步骤
			return s.trFromCtx(ctx, "localserver_no_active_step", nil), nil
		}
		// 激活成功后立即启动 CLI 会话
		if _, runErr := executionLease.RunStep(ctx, workflow.RunStepOptions{
			TaskUUID:   taskUUID,
			StepUUID:   startedStepUUID,
			RequestID:  "", // 微信入口没有 requestID
			RecordType: "initial_run",
		}); runErr != nil {
			return "", runErr
		}
		// 成功激活第一步并启动 CLI
		return s.trFromCtx(ctx, "localserver_skill_task_first_step_started", nil), nil
	}
	// stepUUID 非空：可能是 CLI 直接模式的隐式步骤，也可能是 Pipeline/ExpertGroup 的显式步骤
	if executionMode == taskruntime.ExecutionModeCLI {
		// CLI 直接模式：隐式步骤已存在，直接启动 CLI 会话
		orchestrator := s.orchestratorGetter()
		if orchestrator == nil {
			return "", fmt.Errorf("任务执行服务尚未就绪")
		}
		executionLease, err := orchestrator.BeginTaskExecution(taskUUID)
		if err != nil {
			return "", err
		}
		defer executionLease.Close()
		if _, runErr := executionLease.RunStep(ctx, workflow.RunStepOptions{
			TaskUUID:   taskUUID,
			StepUUID:   stepUUID,
			RequestID:  "",
			RecordType: "initial_run",
		}); runErr != nil {
			return "", runErr
		}
		return s.trFromCtx(ctx, "localserver_skill_task_first_step_started", nil), nil
	}
	// Pipeline / ExpertGroup 模式：检查是否有下一步
	hasNext, probeErr := s.tasks.taskHasPendingStepAfter(ctx, taskUUID, stepUUID)
	if probeErr != nil {
		return "", probeErr
	}
	if !hasNext {
		return s.trFromCtx(ctx, "localserver_skill_task_last_step", nil), nil
	}
	result, _, err := s.tasks.completeStepCore(ctx, taskUUID, stepUUID, true)
	if err != nil {
		if startErr := CoreAutoStartError(err); startErr != nil {
			return "", startErr
		}
		return "", err
	}
	_ = result
	return s.trFromCtx(ctx, "localserver_skill_task_next_step_started", nil), nil
}

// WechatCreateTask 新建本地任务（友好 ID 入参），命中 V0 / W1 验证项。
// 与 HTTP W1 共用 createLocalTaskCore —— 两边不再各写一套「项目/流水线/专家团/子项目解析」。
//
// Pipeline 模式：任务创建后需要 /n 启动，追加 "本任务是流水线执行，您可以使用 /n -id=XX 进行开始任务或下一步流水线。"
// 其他模式（CLI 直接、Expert Group）：任务创建后自动启动 CLI，追加 "任务已开始执行。"
func (s *SkillSurface) WechatCreateTask(ctx context.Context, body createTaskBody) (string, error) {
	taskUUID, err := s.createLocalTaskCore(ctx, body)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	var shortID int64
	_ = s.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, taskUUID).Scan(&shortID)
	msg := s.trFromCtx(ctx, "localserver_skill_task_created", i18n.Params{"ID": shortID, "Title": strings.TrimSpace(body.Title)})
	// 判断执行模式：Pipeline 模式需要 /n 启动，其他模式自动启动
	executionMode := taskruntime.NormalizeExecutionMode(body.ExecutionMode)
	if executionMode == "" && body.PipelineIndex > 0 {
		executionMode = taskruntime.ExecutionModePipeline
	}
	if executionMode == taskruntime.ExecutionModePipeline {
		// Pipeline 模式：追加提示，等待用户 /n 启动
		msg += "\n\n" + s.trFromCtx(ctx, "localserver_skill_task_created_pipeline_hint", i18n.Params{"ID": shortID})
	} else {
		// CLI 直接 / Expert Group 模式：自动启动任务
		_, startErr := s.tasks.startTaskCore(ctx, taskUUID, "", pipelineAssignmentInput{})
		if startErr != nil {
			return "", startErr
		}
		msg += "\n\n" + s.trFromCtx(ctx, "localserver_skill_task_created_started", nil)
	}
	return msg, nil
}

// WechatCreateTeamTaskFromTemplate 微信侧 `/ctt` 的实现：参考已有本地任务创建团队工作。
//
// 参数语义：
//   - template：参考任务的友好 ID（short_id 或列表序号），未命中时按任务列表序号反查；
//   - workItemID：云端「我的工作」条目的 ttid（与 /tt 输出里 `<...>` 段严格一致），
//     解析为 R1 列表中的 1-based 序号后再交 W2；ttid 是云端 work item 的对外
//     稳定标识，不会随列表过滤重排而漂移；
//   - title：可选覆盖标题；W2 当前不消费该值，团队任务的实际标题由云端 work item 决定。
//
// 业务实现复用 W2 (`createTaskFromTemplate`)：函数本身是 gin-coupled 的，但只依赖
// `c.Request.Body` / `getSession(c)` / `c.JSON` 三个 gin 字段。生产代码里再造一遍
// 80 行 ref + template 解析 + importTeamTask 调用既易漂移又有与 W2 不同步的风险，
// 因此用 `gin.CreateTestContext` + `httptest.NewRecorder()` 在 SkillSurface 内做一
// 次「合成 ctx → 调 W2 → 读回响应」的合成：等效于 W2 被本进程内另一条 HTTP 路径触发，
// 业务逻辑零复制。
func (s *SkillSurface) WechatCreateTeamTaskFromTemplate(ctx context.Context, template string, workItemID string, title string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		err := newSkillBadRequest("localserver_skill_task_ref_invalid")
		return s.wechatErrorFromErr(ctx, err), err
	}
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		err := newSkillBadRequest("localserver_skill_team_work_id_not_found")
		return s.wechatErrorFromErr(ctx, err), err
	}
	session, ok := s.sessionFromCtx(ctx)
	if !ok {
		err := newSkillBadRequest("localserver_not_authenticated")
		return s.wechatErrorFromErr(ctx, err), err
	}
	if s.taskCloudClient == nil || s.taskCloudClient() == nil {
		err := newSkillBadRequest("localserver_cloud_session_expired")
		return s.wechatErrorFromErr(ctx, err), err
	}

	// ttid → 1-based 列表序号：W2 的 `work_item` 字段沿用 R1「我的工作」的 1-based
	// 序号（HTTP API 契约未变），所以微信侧把 -ttid=<ttid> 翻译成序号再交给 W2。
	// ttid 是云端 work item 的稳定标识，不依赖 /tt 当前的过滤/排序，能避免「列表
	// 序号随过滤重排漂移」的引用错位。
	client := s.taskCloudClient()
	work, err := client.GetMyWork(ctx)
	if err != nil {
		err := newSkillBadRequest("localserver_internal_error")
		return s.wechatErrorFromErr(ctx, err), err
	}
	workItemIdx := 0
	for i, item := range work.List {
		if workItemIDString(item["id"]) == workItemID {
			workItemIdx = i + 1
			break
		}
	}
	if workItemIdx == 0 {
		err := newSkillBadRequest("localserver_skill_team_work_id_not_found")
		return s.wechatErrorFromErr(ctx, err), err
	}

	body := map[string]interface{}{
		"template":  template,
		"work_item": workItemIdx,
		"title":     strings.TrimSpace(title),
	}
	bodyJSON, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	testCtx, _ := gin.CreateTestContext(rec)
	testCtx.Request = httptest.NewRequest(http.MethodPost, "/api/local/cli-skill/tasks/from-template", bytes.NewReader(bodyJSON))
	testCtx.Request.Header.Set("Content-Type", "application/json")
	testCtx.Request.ContentLength = int64(len(bodyJSON))
	testCtx.Set("session", session)
	// 把 ctx 的 locale 透传到 gin context：让 W2 失败时 i18n.Error 写入的 `error`
	// 字段已按用户语言翻译过，微信侧可直接原文返回，不必再走二次翻译。
	if locale := i18n.LocaleFromContext(ctx); locale != "" {
		testCtx.Set(i18nGinContextLocaleKey, locale)
	}

	s.createTaskFromTemplate(testCtx)

	var resp struct {
		UUID    string `json:"uuid"`
		Warning string `json:"warning"`
		Error   string `json:"error"`
		Code    string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// 4xx/5xx：W2 已按上面注入的 locale 写好 `error`；把它原样回给微信用户。
	if rec.Code >= 400 {
		localized := strings.TrimSpace(resp.Error)
		if localized == "" {
			localized = s.trFromCtx(ctx, "localserver_internal_error", nil)
		}
		// 错误码取自 i18n 内部约定，但 error 字段已是本地化文案。
		// 此处 err 仅为「让 wechatSkillFailure 知道这是失败」的状态位，不参与文案。
		failErr := newSkillBadRequest("localserver_internal_error")
		return localized, failErr
	}
	if rec.Code != http.StatusCreated || resp.UUID == "" {
		err := newSkillBadRequest("localserver_internal_error")
		return s.wechatErrorFromErr(ctx, err), err
	}
	// 成功：先确保别名行已登记（createTaskFromTemplate 走 W2 内部 createTask /
	// importTeamTask，两者均不主动写 gt_remote_task_aliases），再取出新建任务的
	// short_id，按 ctx locale 渲染给微信用户。
	_ = s.ensureRemoteAliases(ctx)
	var shortID int64
	_ = s.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, resp.UUID).Scan(&shortID)
	var titleText string
	_ = s.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, resp.UUID).Scan(&titleText)
	msg := s.trFromCtx(ctx, "localserver_skill_team_task_created", i18n.Params{
		"ID":    shortID,
		"Title": strings.TrimSpace(titleText),
	})
	// 检测是否为流水线任务，如是则追加提示；其他模式自动启动任务
	var executionMode string
	_ = s.db.QueryRowContext(ctx, `SELECT execution_mode FROM gt_tasks WHERE uuid=?`, resp.UUID).Scan(&executionMode)
	if executionMode == taskruntime.ExecutionModePipeline {
		msg += "\n" + s.trFromCtx(ctx, "localserver_skill_team_task_created_pipeline_hint", i18n.Params{"ID": shortID})
	} else {
		// CLI 直接 / Expert Group 模式：自动启动任务
		_, startErr := s.tasks.startTaskCore(ctx, resp.UUID, "", pipelineAssignmentInput{})
		if startErr != nil {
			return "", startErr
		}
		msg += "\n" + s.trFromCtx(ctx, "localserver_skill_task_created_started", nil)
	}
	// 响应里的 warning 是 W2 用 i18n.T(c, ...) 写出来的，可能与用户当前 locale 不一致。
	// 统一按 ctx 的 locale 再翻译一次（localserver_import_sync_warning 在所有 locale 中都已注册）。
	if resp.Warning != "" {
		msg += "\n" + s.trFromCtx(ctx, "localserver_import_sync_warning", nil)
	}
	return msg, nil
}

// WechatSwitchExecution 切换任务的执行方式（CLI / Vibe Coding / Pipeline / Expert Group）。
// 友好 ID 参数：ref 必填；CLI 支持 -cid + -mid，也支持独立的 -mid（沿用任务当前 CLI）。
//
// 全部走 tasks_core.go 的 assignExecutionModeCore / switchExecutionModeCore：不再伪造
// gin.Context、不再二次绑定 body、不再借道 c.Params 取 uuid（历史 W5 阻断级缺陷）。
func (s *SkillSurface) WechatSwitchExecution(ctx context.Context, in WechatSwitchInput) (string, error) {
	taskUUID, err := s.resolveTaskRef(ctx, in.Ref)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	input, err := s.resolveSwitchInputCore(ctx, taskUUID, in)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	before, _ := s.describeCurrentExecutionCore(ctx, taskUUID)
	result, err := s.tasks.assignExecutionModeCore(ctx, taskUUID, input)
	if err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	after := result.After
	if after == nil {
		after, _ = s.describeCurrentExecutionCore(ctx, taskUUID)
	}
	return formatExecutionComparison(s.trFromCtx(ctx, "localserver_skill_execution_switched", nil), before, after), nil
}

// ============================================================================
// 任务状态管理（/task /t /T 列表；/exit 退出；/c 取消；/r 恢复；/d 完成）。
// 微信命令、CLI Skill HTTP、自然语言兜底三个入口共用同一组 gin-free 业务核心，
// 防止在多入口之间漂移（这正是历史上 SKILL.md 写满虚构接口的成因）。
// ============================================================================

// wechatListTasksText 内部实现：按状态过滤列出任务（与现状 /task 输出格式完全一致）。
// status 取值：active / done / blocked / ""（"" 表示全部）。
func (s *SkillSurface) wechatListTasksText(ctx context.Context, status string) (string, error) {
	if err := s.ensureRemoteAliases(ctx); err != nil {
		return "", err
	}
	query := `SELECT a.short_id, t.title FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid`
	scopeSQL, args := taskScopeSQL(s.currentTaskCloudClient(), "t")
	query += ` WHERE ` + scopeSQL
	if status == "not_done" {
		query += ` AND t.status != 'done'`
	} else if status != "" {
		query += ` AND t.status=?`
		args = append(args, status)
	}
	query += ` ORDER BY a.short_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int64
		var title string
		if err = rows.Scan(&id, &title); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("@%d %s", id, title))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	labelKey := map[string]string{
		"active":   "remote_channel_status_running",
		"done":     "remote_channel_status_done",
		"blocked":  "remote_channel_status_blocked",
		"not_done": "remote_channel_status_not_done",
		"":         "remote_channel_status_all",
	}[status]
	params := i18n.Params{
		"Count":  len(lines),
		"Status": s.trFromCtx(ctx, labelKey, nil),
		"Tasks":  strings.Join(lines, "\n\n"),
		"Help":   s.trFromCtx(ctx, "remote_channel_help", nil),
	}
	if len(lines) == 0 {
		return s.trFromCtx(ctx, "remote_channel_empty_tasks", params), nil
	}
	return s.trFromCtx(ctx, "remote_channel_list", params), nil
}

// WechatListTasks 微信快路径 /task /t /T 的入口。
// status 取值：active / done / blocked / ""；非法值在调用方按 i18n 提示处理。
func (s *SkillSurface) WechatListTasks(ctx context.Context, status string) (string, error) {
	return s.wechatListTasksText(ctx, status)
}

// WechatCancelTask 取消执行指定任务（gin-free）。
// 适用于 /c -f、自然语言「强制 / 直接 / -f」、CLI Skill W7 三处入口。
// 调用方负责处理 state 二次确认（仅微信侧需要；CLI Skill 通过 body.force 显式传入）。
//
// 失败时返回 (text, err)：text 已经是本地化文案，可直接展示给微信用户。
func (s *SkillSurface) WechatCancelTask(ctx context.Context, taskUUID string) (string, error) {
	if err := s.checkTaskScope(ctx, taskUUID); err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	var title string
	_ = s.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&title)
	if s.orchestratorGetter != nil {
		if orch := s.orchestratorGetter(); orch != nil {
			if stopErr := orch.StopTaskSessions(ctx, taskUUID); stopErr != nil {
				// 停止会话是最佳努力：失败仅记录，不阻断主流程（与现状 handleExecuteCancelTask 一致）。
				applog.Warn("SkillSurface 取消任务停止会话失败", "task_uuid", taskUUID, "error", stopErr.Error())
			}
		}
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_tasks SET status='blocked', updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
		return "", err
	}
	listText, listErr := s.wechatListTasksText(ctx, "active")
	if listErr != nil {
		return "", listErr
	}
	return s.trFromCtx(ctx, "remote_channel_cancel_done", i18n.Params{"Task": title, "Tasks": listText}), nil
}

// WechatResumeTask 恢复执行指定任务（gin-free）。
// blocked / done 状态下改回 active；其他状态保持不变（与现状一致）。
func (s *SkillSurface) WechatResumeTask(ctx context.Context, taskUUID string) (string, error) {
	if err := s.checkTaskScope(ctx, taskUUID); err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	var title, currentStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT title, status FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&title, &currentStatus); err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if currentStatus == "blocked" || currentStatus == "done" {
		if _, err := s.db.ExecContext(ctx, `UPDATE gt_tasks SET status='active', updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
			return "", err
		}
	}
	return s.trFromCtx(ctx, "remote_channel_task_resumed", i18n.Params{"Task": title}), nil
}

// WechatCompleteTask 标记任务完成（gin-free）。
func (s *SkillSurface) WechatCompleteTask(ctx context.Context, taskUUID string) (string, error) {
	if err := s.checkTaskScope(ctx, taskUUID); err != nil {
		return s.wechatErrorFromErr(ctx, err), err
	}
	var title string
	_ = s.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&title)
	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_tasks SET status='done', updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
		return "", err
	}
	listText, listErr := s.wechatListTasksText(ctx, "active")
	if listErr != nil {
		return "", listErr
	}
	return s.trFromCtx(ctx, "remote_channel_task_completed", i18n.Params{"Task": title, "Tasks": listText}), nil
}

// WechatExitTaskText 退出当前任务时给用户的回执文案（gin-free）。
// 仅返回文案；清绑定由微信侧处理（state 不属于 SkillSurface，避免双向依赖）。
func (s *SkillSurface) WechatExitTaskText(ctx context.Context, boundTaskTitle string) (string, error) {
	listText, err := s.wechatListTasksText(ctx, "active")
	if err != nil {
		return "", err
	}
	return s.trFromCtx(ctx, "remote_channel_task_exited", i18n.Params{"Task": boundTaskTitle, "Tasks": listText}), nil
}

// ensureRemoteAliases 等价于 wechatRemote.ensureAliases：
// 给尚未登记 short_id 的任务补齐别名行（listTasks 才能正确显示 @N）。
func (s *SkillSurface) ensureRemoteAliases(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO gt_remote_task_aliases(task_uuid, created_at)
		SELECT t.uuid, ? FROM gt_tasks t
		WHERE NOT EXISTS (SELECT 1 FROM gt_remote_task_aliases a WHERE a.task_uuid=t.uuid)
		ORDER BY t.created_at, t.rowid`, time.Now().UnixMilli())
	return err
}

// ============================================================================
// W7~W9：任务状态管理 HTTP 写接口。
// 任务 ID 使用友好 ID（short_id 或列表序号），与 W3/W4/W5 保持同一语义。
// ============================================================================

// skillTaskStatusBody 是 W7~W9 的统一请求体（当前只有 force 字段）。
// 后续若扩展（如 reason / dry_run），只需在本结构体加字段，所有 handler 同步可用。
type skillTaskStatusBody struct {
	Force bool `json:"force"`
}

// runTaskStatusAction W7~W9 共用：解析 :uuid → 友好 ID，读取可选 body，调核心，回写 HTTP。
//
// 返回结构固定为 {task_uuid, force, text}：task_uuid 是解析后的真实 UUID（便于客户端二次引用），
// force 透传请求体中的值（便于客户端验证自己的请求是否被尊重），text 是本地化文案。
func (s *SkillSurface) runTaskStatusAction(
	c *gin.Context,
	core func(context.Context, string) (string, error),
) {
	taskUUID, err := s.resolveTaskRef(c.Request.Context(), strings.TrimSpace(c.Param("uuid")))
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	var body skillTaskStatusBody
	// body 可选：缺省 force=false，由调用方决定二次确认语义。
	if c.Request.ContentLength > 0 {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil && !errors.Is(bindErr, io.EOF) {
			i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
			return
		}
	}
	text, err := core(c.Request.Context(), taskUUID)
	if err != nil {
		s.writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"task_uuid": taskUUID,
		"force":     body.Force,
		"text":      text,
	})
}

// W7: POST /cli-skill/tasks/:uuid/cancel —— 取消执行（stop sessions + status=blocked）。
func (s *SkillSurface) cancelTaskFromSkill(c *gin.Context) {
	s.runTaskStatusAction(c, s.WechatCancelTask)
}

// W8: POST /cli-skill/tasks/:uuid/resume —— 恢复执行（status 改回 active）。
func (s *SkillSurface) resumeTaskFromSkill(c *gin.Context) {
	s.runTaskStatusAction(c, s.WechatResumeTask)
}

// W9: POST /cli-skill/tasks/:uuid/done —— 标记完成（status=done）。
// 路径用 /done 而不是 /complete：避免与 W6（nextStepFromSkill 同义别名）混淆，
// W6 是「推进下一步」的语义，W9 是「直接完成任务」的状态管理语义。
func (s *SkillSurface) completeTaskFromSkill(c *gin.Context) {
	s.runTaskStatusAction(c, s.WechatCompleteTask)
}

// ============================================================================
// 小工具函数。
// ============================================================================

// WechatSwitchInput 是 WechatSwitchExecution 的友好 ID 入参。
//
// ModelIndex 承载「模型序号」两种用法：CLIIndex>0 时它是在该 CLI 模型列表内的序号；
// CLIIndex==0 且 ModelIndex>0 时它是独立的 -mid（CLI 沿用任务当前执行方式）。
type WechatSwitchInput struct {
	Ref             string
	Mode            string
	Tool            string
	ModelName       string
	CLIIndex        int
	ModelIndex      int
	PipelineIdx     int
	ExpertGroupIdx  int
	VibeIdx         int
	ReplaceExisting bool
}

func mustMarshal(v interface{}) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func defaultIfEmpty(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// trFromCtx 按 ctx 携带的 locale 读取翻译。
//
// 统一到 i18n.ContextWithLocale / i18n.LocaleFromContext：此前本文件另有一套私有
// localeHolderKey（"goteams.wechat.locale"），与 i18n.Middleware 写入的 localeKey{} 类型键
// 互不可见 —— HTTP 入口经此读取时**恒回落 zh-CN**，英文用户静默看到中文。双轨已删除。
func (s *SkillSurface) trFromCtx(ctx context.Context, key string, params i18n.Params) string {
	return i18n.FormatWithLocale(i18n.LocaleFromContext(ctx), key, params)
}

// wechatErrorFromErr 把 errSkillBadRequest / errSkillInvalidInput / 核心层分类错误
// 翻译成给微信用户看的简短文案。
//
// 关键约定：**绝不把裸 i18n key 当作文案返回**。errSkillBadRequest.Error() 与
// coreStatusError.Error()（key 非空时）返回的都是 key 本身，若落到最后的 err.Error()
// 分支就会把键名展示给用户。
func (s *SkillSurface) wechatErrorFromErr(ctx context.Context, err error) string {
	var badRequest *errSkillBadRequest
	if errors.As(err, &badRequest) {
		return s.trFromCtx(ctx, badRequest.key, nil)
	}
	// 入参非法类错误（ValidateExecutionTarget / ValidateCLITarget）：走与 HTTP 入口同一份
	// i18n.Key 映射，避免微信侧再维护一套「哪个错误配哪句话」的表。
	var invalidInput *errSkillInvalidInput
	if errors.As(err, &invalidInput) {
		if key, _ := i18n.Key(invalidInput.err); key != "" && key != "localserver_internal_error" {
			return s.trFromCtx(ctx, key, nil)
		}
	}
	// gin-free 核心层的分类错误：显式 key 优先；否则按 i18n.Key 映射被包裹的真实错误；
	// 仍无映射时用通用文案，不回落 err.Error()（那可能是 key 名）。
	var statusErr *coreStatusError
	if errors.As(err, &statusErr) {
		if statusErr.key != "" {
			return s.trFromCtx(ctx, statusErr.key, nil)
		}
		if statusErr.err != nil {
			if key, _ := i18n.Key(statusErr.err); key != "" && key != "localserver_internal_error" {
				return s.trFromCtx(ctx, key, nil)
			}
		}
		return s.trFromCtx(ctx, "localserver_internal_error", nil)
	}
	// 其它领域错误按 i18n.Key 映射，与 HTTP 入口同口径。
	if key, _ := i18n.Key(err); key != "" && key != "localserver_internal_error" {
		return s.trFromCtx(ctx, key, nil)
	}
	return err.Error()
}

// sessionFromCtx 取当前账号会话（云登录态）。
//
// 此前读的是 wechatSessionKey（string 常量），而全仓没有任何写入点 → 恒 false，
// 导致微信 /tt 恒提示「未登录云端」。现在统一从 server.go 注入的 accountSession getter 取，
// 与 validateSession 的回退口径完全一致（同一份 currentSession() + sessionMatchesConfig()）。
func (s *SkillSurface) sessionFromCtx(ctx context.Context) (*browserSession, bool) {
	if s.accountSession == nil {
		return nil, false
	}
	sess := s.accountSession()
	if sess == nil {
		return nil, false
	}
	return sess, true
}

// indexFromUUIDOrName 把 uuid 解析回 "序号  名称"，便于详情展示。
func (s *SkillSurface) indexFromUUIDOrName(ctx context.Context, kind, uuid string) (string, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return "", nil
	}
	idx := s.indexFromUUID(ctx, kind, uuid)
	switch kind {
	case "project":
		items, err := project.NewService(s.db).List(ctx)
		if err != nil {
			return "", err
		}
		if idx > 0 && idx <= len(items) {
			return fmt.Sprintf("%d  %s", idx, items[idx-1].Name), nil
		}
	case "pipeline":
		items, err := pipeline.NewService(s.db).List(ctx)
		if err != nil {
			return "", err
		}
		if idx > 0 && idx <= len(items) {
			return fmt.Sprintf("%d  %s", idx, items[idx-1].Name), nil
		}
	case "expert_group":
		items, err := expertgroup.NewService(s.db).List(ctx)
		if err != nil {
			return "", err
		}
		if idx > 0 && idx <= len(items) {
			return fmt.Sprintf("%d  %s", idx, items[idx-1].Name), nil
		}
	}
	return "", nil
}

// detailSubprojectUUIDs 从 loadTaskDetail 的 "projects" 取出关联项目 UUID。
//
// 双分支是刻意的：loadTaskDetail 现以 []map[string]interface{} 生产该键（tasks.go），但
// 契约上曾出现 []gin.H，消费方若只认一种就会静默取空（W2 关联项目恒空的历史缺陷）。
// 取值只做类型兼容，不改变上游数据结构，也不碰 steps / work_dirs（二者断言实测正确）。
func detailSubprojectUUIDs(detail map[string]interface{}) []string {
	raw, ok := detail["projects"]
	if !ok || raw == nil {
		return nil
	}
	out := make([]string, 0)
	appendItem := func(item map[string]interface{}) {
		if uuidVal, ok := item["uuid"].(string); ok && uuidVal != "" {
			out = append(out, uuidVal)
		}
	}
	switch projects := raw.(type) {
	case []map[string]interface{}:
		for _, item := range projects {
			appendItem(item)
		}
	case []gin.H:
		for _, item := range projects {
			appendItem(item)
		}
	case []interface{}:
		for _, item := range projects {
			if mapped, ok := item.(map[string]interface{}); ok {
				appendItem(mapped)
			}
		}
	}
	return out
}

func pickStringDetail(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func formatExecutionComparison(header string, before, after gin.H) string {
	if before == nil && after == nil {
		return header
	}
	lines := []string{header}
	if before != nil {
		lines = append(lines, fmt.Sprintf("before: mode=%v tool=%v model=%v status=%v",
			before["execution_mode"], before["execution_tool"], before["model_name"], before["status"]))
	}
	if after != nil {
		lines = append(lines, fmt.Sprintf("after:  mode=%v tool=%v model=%v status=%v",
			after["execution_mode"], after["execution_tool"], after["model_name"], after["status"]))
	}
	return strings.Join(lines, "\n\n")
}
