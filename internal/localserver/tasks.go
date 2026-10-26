package localserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goteams-client/internal/applog"
	"goteams-client/internal/artifactstore"
	"goteams-client/internal/cloud"
	"goteams-client/internal/cloudsync"
	"goteams-client/internal/executor"
	"goteams-client/internal/expertgroup"
	"goteams-client/internal/i18n"
	"goteams-client/internal/knowledge"
	"goteams-client/internal/pipeline"
	"goteams-client/internal/protocol"
	storagelog "goteams-client/internal/storage/log"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"
)

// TasksHandler task-related HTTP handler
//
// Task data is always located in the application-scoped goteams.db.
type TasksHandler struct {
	sessionDB        func() *sql.DB
	orchestrator     func() *workflow.Orchestrator
	cloudClient      func() *cloud.Client
	wsHub            *WSHub
	taskRoot         string
	skillsRoot       string
	codexSkillReady  bool
	codexSkillReason string
	vibeSkills       map[string]VibeSkillStatus
	assetRefreshMu   sync.Mutex
	assetRefreshNext map[string]time.Time
	codexWatchMu     sync.Mutex
	codexWatches     map[string]*codexDesktopSessionWatch
}

// VibeSkillStatus records whether the bundled teambuddy Skill was installed
// into one Vibe Coding tool's user skill directory.
type VibeSkillStatus struct {
	Tool   string
	Root   string
	Ready  bool
	Reason string
}

// VibeImBotSkillStatus records whether the bundled teambuddy-im-bot-cli Skill was installed
// into one Vibe Coding tool's user skill directory. 微信通道兜底流程通过 markdown 本地路径
// 引用此 Skill，路径由 wechatRemote 按 cliType 查询后交给 CLI 自行加载。
type VibeImBotSkillStatus struct {
	Tool   string
	Root   string
	Ready  bool
	Reason string
}

// NewTasksHandler creates task handler
func NewTasksHandler(sessionDB func() *sql.DB, orchestrator func() *workflow.Orchestrator, cloudClient func() *cloud.Client, wsHub *WSHub, taskRoot, skillsRoot string, codexSkillReady bool, codexSkillReason string, vibeSkills []VibeSkillStatus) *TasksHandler {
	indexed := make(map[string]VibeSkillStatus, len(vibeSkills))
	for _, status := range vibeSkills {
		indexed[status.Tool] = status
	}
	return &TasksHandler{
		sessionDB:        sessionDB,
		orchestrator:     orchestrator,
		cloudClient:      cloudClient,
		wsHub:            wsHub,
		taskRoot:         taskRoot,
		skillsRoot:       skillsRoot,
		codexSkillReady:  codexSkillReady,
		codexSkillReason: codexSkillReason,
		vibeSkills:       indexed,
		assetRefreshNext: make(map[string]time.Time),
		codexWatches:     map[string]*codexDesktopSessionWatch{},
	}
}

// taskDB returns the application-scoped task database.
func (h *TasksHandler) taskDB() *sql.DB {
	if h.sessionDB == nil {
		return nil
	}
	return h.sessionDB()
}

// taskCloudClient returns the cloud client used by the current session (login target: official or custom address).
// When logging in with a custom address, the session holds an independent client; if the session/client is missing, it falls back to the default client held by the processor.
// taskSyncPusher pushes local task execution results to the cloud incrementally via REST.
var taskSyncPusher = cloudsync.DefaultPusher()

// pushTaskSync aggregates and pushes the task's latest increment in the background.
func (h *TasksHandler) pushTaskSync(taskUUID string) {
	client, db := h.taskCloudClient(), h.taskDB()
	if client == nil || client.UserID() == "" || db == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := taskSyncPusher.PushTaskSync(ctx, db, client, taskUUID); err != nil {
			applog.Warn("[CloudSync] 推送任务结果失败", "task_uuid", taskUUID, "error", err.Error())
		}
	}()
	// 产出文档走独立通道：这里只触发一次去抖后的异步扫描 + 上报，
	// 不阻塞任务投影，也不因为某个文档失败连累任务上报。
	h.documentKick(taskUUID)
}

// notifyTaskChanged asks every open task view to reload the task snapshot and
// progress. The browser filters by task_uuid, so unrelated task pages do not reload.
func (h *TasksHandler) notifyTaskChanged(taskUUID string) {
	if h.wsHub == nil {
		return
	}
	h.wsHub.BroadcastTaskChanged(taskUUID)
}

// deleteTaskSync is intentionally a no-op for cloud reporting.
//
// 需求 1896 边界：客户端任务及其执行过程全程本地化，但每一次运行上报到云端的
// 执行记录/日志属于团队共享的审计数据，不能被任何一次本地删除带走。由此同一需求
// 才能保留同一用户与不同用户的多次运行历史。这里仅清掉本地增量同步游标，避免已删
// 任务的后续会话推送被误判为新任务；云端运行记录保持原样，直到云端侧另行归档。
func (h *TasksHandler) deleteTaskSync(taskUUID string) {
	taskSyncPusher.ForgetTask(taskUUID)
}
func (h *TasksHandler) taskCloudClient() *cloud.Client {
	if h.cloudClient == nil {
		return nil
	}
	return h.cloudClient()
}

// resolveCloudUserID 取当前云端账号 ID，用于写入任务的 cloud_user_id。
// 以云客户端为准（上报链路判断任务归属用的也是它）；浏览器会话里的 UserID 只作兜底，
// 因为会话可能是从磁盘恢复的，旧版本持久化的会话缺少该字段时是空值。
func (h *TasksHandler) resolveCloudUserID(session *browserSession) string {
	if client := h.taskCloudClient(); client != nil && client.UserID() != "" {
		return client.UserID()
	}
	if session != nil {
		return strings.TrimSpace(session.UserID)
	}
	return ""
}

// RegisterRoutes registers task routes
func (h *TasksHandler) RegisterRoutes(r *gin.RouterGroup) {
	r.Use(h.requireTaskScope())
	r.GET("", h.listTasks)
	r.GET("/conversations/search", h.searchConversations)
	r.GET("/workspaces", h.listSidebarWorkspaces)
	r.PUT("/workspaces", h.updateSidebarWorkspace)
	r.DELETE("/workspaces", h.deleteSidebarWorkspace)
	r.POST("/workspaces/delete-preview", h.previewWorkspaceDeletion)
	r.POST("/workspaces/delete", h.deleteWorkspaceTasks)
	r.GET("/codex-capability", h.getCodexCapability)
	r.GET("/:uuid", h.getTask)
	r.POST("", h.createTask)
	r.PUT("/:uuid", h.updateTask)
	r.PUT("/:uuid/title", h.renameTask)
	r.PUT("/:uuid/status", h.updateTaskStatus)
	r.GET("/:uuid/git/branches", h.listTaskGitBranches)
	r.POST("/:uuid/git/checkout", h.checkoutTaskGitBranch)
	r.PUT("/:uuid/execution-mode", h.assignExecutionMode)
	r.POST("/:uuid/vibe-rebind", h.rebindGenericVibeTool)
	r.GET("/:uuid/codex-context", h.getCodexContext)
	r.POST("/:uuid/vibe-manual-inject", h.injectVibeManualPrompt)
	r.POST("/:uuid/codex-session-watch", h.watchCodexDesktopSession)
	r.POST("/:uuid/start", h.startTask)
	r.POST("/:uuid/assign-pipeline", h.assignAndStartTask)
	r.PUT("/:uuid/pipeline", h.assignTaskPipeline)
	r.GET("/:uuid/records", h.conversationRecords)
	r.GET("/:uuid/progress", h.listTaskProgress)
	r.GET("/:uuid/execution-history", h.listTaskExecutionHistory)
	r.GET("/:uuid/execution-history/:history_uuid", h.getTaskExecutionHistory)
	r.GET("/:uuid/activities/:activity_id", h.getTaskActivity)
	r.POST("/:uuid/expert-messages", h.sendExpertMessage)
	r.POST("/:uuid/steps/:step_uuid/questions", h.askStepQuestion)
	r.POST("/:uuid/steps/:step_uuid/complete", h.completeStep)
	r.POST("/:uuid/steps/:step_uuid/runs", h.runStep)
	r.PUT("/:uuid/steps/:step_uuid/prompt", h.updateStepPrompt)
	r.GET("/:uuid/workspace/roots", h.listTaskWorkspaceRoots)
	r.GET("/:uuid/workspace/entries", h.listTaskWorkspaceEntries)
	r.GET("/:uuid/workspace/content", h.getTaskWorkspaceContent)
	r.GET("/:uuid/workspace/search", h.searchTaskWorkspace)
	r.GET("/:uuid/files", h.listTaskFiles)
	r.GET("/:uuid/files/content", h.getTaskFileContent)
	r.POST("/:uuid/files/locate", h.locateTaskOutputFile)
	r.GET("/:uuid/files/raw", h.getTaskAttachmentContent)
	r.POST("/:uuid/files/content", h.saveTaskFileContent)
	// 产出文档上报状态与手动重报（独立旁路通道）
	r.GET("/:uuid/files/upload-status", h.taskDocumentUploadStatus)
	r.POST("/:uuid/files/reupload", h.reuploadTaskDocuments)
	r.POST("/:uuid/files/report", h.reportTaskDocuments)
	// 执行记录（任务投影 + 沟通会话）上报状态与手动重报
	r.GET("/:uuid/sync-status", h.taskSyncStatus)
	r.POST("/:uuid/sync", h.retryTaskSync)
	// S-IN-08: 超长引用内容唯一命名落盘到任务产出目录
	r.POST("/:uuid/files/reference", h.spillReferenceFile)
	r.POST("/:uuid/files/attachments", h.uploadTaskAttachment)
	r.GET("/:uuid/sessions", h.listTaskSessions)
	r.GET("/sessions/:uuid/events", h.listSessionEvents)
	r.GET("/sessions/:uuid/events/:sequence/full", h.getSessionEventFull)
	r.POST("/sessions/:uuid/messages", h.continueSession)
	r.POST("/sessions/:uuid/stop", h.stopSession)
	r.DELETE("/:uuid", h.deleteTask)

	r.GET("/cli-count", h.getCliTaskCount)
	r.GET("/cli-discovery", h.cliDiscovery)
	r.GET("/cli-models", h.cliDiscoveryModels)

	// Status (kanban column) configuration
	r.GET("/task-lanes", h.listTaskLanes)
	r.POST("/task-lanes", h.createTaskLane)
	r.PUT("/task-lanes/reorder", h.reorderTaskLanes)
	r.PUT("/task-lanes/:id", h.updateTaskLane)
	r.DELETE("/task-lanes/:id", h.deleteTaskLane)
}

type stepExecutionConfig struct {
	StepUUID     string `json:"step_uuid"`
	SourceStepID string `json:"source_step_id"`
	CloudStepID  string `json:"cloud_step_id"`
	CLIType      string `json:"cli_type"`
	Model        string `json:"model"`
	ModelName    string `json:"model_name"`
}

type pipelineAssignmentInput struct {
	PipelineUUID string                `json:"pipeline_uuid"`
	StepConfigs  []stepExecutionConfig `json:"step_configs"`
}

func (h *TasksHandler) snapshotFromPipeline(ctx context.Context, input pipelineAssignmentInput) (taskruntime.Snapshot, error) {
	svc := pipeline.NewService(h.taskDB())
	pipe, err := svc.Get(ctx, strings.TrimSpace(input.PipelineUUID))
	if errors.Is(err, pipeline.ErrNotFound) && h.taskCloudClient() != nil {
		// 重启后内存缓存为空且登录异步同步尚未完成：对云端流水线做一次冷缓存兜底同步再重试。
		// 兜底同步独立限时，避免云端慢响应把任务创建/启动请求拖住。
		applog.Warn("[LocalServer] 流水线未在本地缓存中找到，触发云端重同步后重试", "pipeline_uuid", input.PipelineUUID)
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, syncErr := syncCloudPipelines(syncCtx, svc, h.taskCloudClient()); syncErr != nil {
			applog.Warn("[LocalServer] 流水线兜底重同步失败", "pipeline_uuid", input.PipelineUUID, "error", syncErr.Error())
		} else {
			pipe, err = svc.Get(ctx, strings.TrimSpace(input.PipelineUUID))
		}
	}
	if err != nil {
		return taskruntime.Snapshot{}, fmt.Errorf("读取流水线失败: %w", err)
	}
	configs := make(map[string]stepExecutionConfig, len(input.StepConfigs)*3)
	for _, config := range input.StepConfigs {
		for _, key := range []string{config.StepUUID, config.SourceStepID, config.CloudStepID} {
			if key = strings.TrimSpace(key); key != "" {
				configs[key] = config
			}
		}
	}
	sourceID := pipe.UUID
	if pipe.SourceType == "cloud" {
		sourceID = pipe.CloudPipelineID
	}
	snapshot := taskruntime.Snapshot{SourceType: pipe.SourceType, SourcePipelineID: sourceID,
		CloudSourceKey: pipe.CloudSourceKey, Name: pipe.Name, Description: pipe.Description, Avatar: pipe.Avatar}
	for _, step := range pipe.Steps {
		config, ok := configs[step.UUID]
		if !ok && step.CloudStepID != "" {
			config = configs[step.CloudStepID]
		}
		cliType := strings.TrimSpace(config.CLIType)
		if cliType == "" {
			cliType = strings.TrimSpace(step.CLIType)
		}
		model := strings.TrimSpace(config.ModelName)
		if model == "" {
			model = strings.TrimSpace(config.Model)
		}
		if model == "" {
			model = strings.TrimSpace(step.ModelName)
		}
		snapshotStep := taskruntime.SnapshotStep{SourceStepID: step.UUID, LocalStepID: step.UUID,
			Name: step.Name, Description: step.Description, Avatar: step.Avatar, Prompt: step.Prompt,
			CLIType: cliType, ModelName: model, SortOrder: step.SortOrder}
		if pipe.SourceType == "cloud" {
			snapshotStep.SourceStepID = step.CloudStepID
			snapshotStep.LocalStepID = ""
			snapshotStep.CloudStepID = step.CloudStepID
		}
		snapshot.Steps = append(snapshot.Steps, snapshotStep)
	}
	return snapshot, nil
}

// RegisterTeamRoutes registers the only task APIs that require a cloud login.
func (h *TasksHandler) RegisterTeamRoutes(r *gin.RouterGroup) {
	r.GET("/my-work", h.listMyWork)
	r.GET("/work-items/:workspaceId/:id", h.getTeamWorkItem)
	r.GET("/pipelines", h.listTeamPipelines)
	r.GET("/pipelines/:id/snapshot", h.getTeamPipelineSnapshot)
	r.POST("/imports", h.importTeamTask)
}

func (h *TasksHandler) listTeamPipelines(c *gin.Context) {
	pipelines, err := h.taskCloudClient().GetPipelines(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	items := make([]gin.H, 0, len(pipelines))
	for _, item := range pipelines {
		items = append(items, gin.H{"id": item.ID, "name": item.Name, "description": "", "step_count": 0})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *TasksHandler) getTeamPipelineSnapshot(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_id_invalid", "pipeline_id_invalid")
		return
	}
	snapshot, err := h.taskCloudClient().GetPipelineSnapshot(c.Request.Context(), id)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	steps := make([]gin.H, 0, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		steps = append(steps, gin.H{"id": step.StepKey, "uuid": step.StepKey, "name": step.Name, "sort_order": step.SortOrder})
	}
	c.JSON(http.StatusOK, gin.H{"id": snapshot.ID, "name": snapshot.Name, "description": "", "steps": steps})
}

func (h *TasksHandler) importTeamTask(c *gin.Context) {
	var body struct {
		WorkItemType  string          `json:"work_item_type"`
		WorkItemID    json.RawMessage `json:"work_item_id"`
		CloudPipeline json.RawMessage `json:"cloud_pipeline_id"`
		StepConfigs   []struct {
			CloudStepID string `json:"cloud_step_id"`
			CLIType     string `json:"cli_type"`
			Model       string `json:"model"`
		} `json:"step_configs"`
		WorkDir         string   `json:"work_dir"`
		WorkDirs        []string `json:"work_dirs"`
		ProjectUUID     string   `json:"project_uuid"`
		SubprojectUUIDs []string `json:"subproject_uuids"`
		Status          string   `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	parseID := func(raw json.RawMessage) (int64, error) {
		value := strings.Trim(string(raw), "\"")
		return strconv.ParseInt(value, 10, 64)
	}
	workItemID, err := parseID(body.WorkItemID)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_work_item_id_invalid", "work_item_id_invalid")
		return
	}
	pipelineID, err := parseID(body.CloudPipeline)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_id_invalid", "pipeline_id_invalid")
		return
	}
	items, err := h.taskCloudClient().GetWorkItems(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	var selected *cloud.WorkItem
	for i := range items {
		if items[i].Type == body.WorkItemType && items[i].ID == workItemID {
			selected = &items[i]
			break
		}
	}
	if selected == nil {
		i18n.Error(c, http.StatusNotFound, "localserver_cloud_work_item_not_found", "cloud_work_item_not_found")
		return
	}
	applog.Info("[CloudSync] 导入团队任务：已从云端工作项列表匹配到 selected",
		"work_item_id", selected.ID, "work_item_type", selected.Type, "workspace_id", selected.WorkspaceID)
	remote, err := h.taskCloudClient().GetPipelineSnapshot(c.Request.Context(), pipelineID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	workDirs := body.WorkDirs
	if len(workDirs) == 0 && strings.TrimSpace(body.WorkDir) != "" {
		workDirs = []string{body.WorkDir}
	}
	session := h.currentSessionForRequest(c)
	if session == nil {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}
	cloudSourceKey := pipeline.CloudSourceKey(h.taskCloudClient().BaseURL())
	pipeSvc := pipeline.NewService(h.taskDB())
	existing, err := pipeSvc.List(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	cloudPipelines := make([]pipeline.CloudPipeline, 0)
	remoteID := strconv.FormatInt(pipelineID, 10)
	for _, item := range existing {
		if item.SourceType != "cloud" || item.CloudSourceKey != cloudSourceKey || item.CloudPipelineID == remoteID {
			continue
		}
		cached := pipeline.CloudPipeline{CloudPipelineID: item.CloudPipelineID, Name: item.Name, Description: item.Description, Avatar: item.Avatar}
		for _, step := range item.Steps {
			cached.Steps = append(cached.Steps, pipeline.CloudStep{CloudStepID: step.CloudStepID, SortOrder: step.SortOrder, Name: step.Name, Description: step.Description, Avatar: step.Avatar, Prompt: step.Prompt})
		}
		cloudPipelines = append(cloudPipelines, cached)
	}
	selectedCloud := pipeline.CloudPipeline{CloudPipelineID: remoteID, Name: remote.Name, Avatar: remote.Icon}
	for _, step := range remote.Steps {
		selectedCloud.Steps = append(selectedCloud.Steps, pipeline.CloudStep{CloudStepID: step.StepKey, SortOrder: step.SortOrder, Name: step.Name, Prompt: step.Prompt})
	}
	cloudPipelines = append(cloudPipelines, selectedCloud)
	synced, err := pipeSvc.SyncCloud(c.Request.Context(), cloudSourceKey, cloudPipelines)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	selectedPipelineUUID := ""
	for _, item := range synced {
		if item.CloudPipelineID == remoteID {
			selectedPipelineUUID = item.UUID
			break
		}
	}
	if selectedPipelineUUID == "" {
		i18n.Error(c, http.StatusInternalServerError, "localserver_cloud_pipeline_cache_failed", "cloud_pipeline_cache_failed")
		return
	}
	configs := make([]stepExecutionConfig, 0, len(body.StepConfigs))
	for _, item := range body.StepConfigs {
		configs = append(configs, stepExecutionConfig{CloudStepID: item.CloudStepID, CLIType: item.CLIType, Model: item.Model})
	}
	// 待开始任务：先验证流水线快照结构，再创建任务。
	// 直接执行的任务需校验 CLI 和模型；待开始任务只校验结构，CLI 和模型在启动时校验。
	if selectedPipelineUUID != "" && len(configs) > 0 {
		if preSnapshot, preErr := h.snapshotFromPipeline(c.Request.Context(), pipelineAssignmentInput{
			PipelineUUID: selectedPipelineUUID,
			StepConfigs:  configs,
		}); preErr == nil {
			var preErr error
			if body.Status == protocol.TaskStatusInProgress || body.Status == protocol.TaskStatusActive {
				preErr = taskruntime.ValidateSnapshotFull(&preSnapshot)
			} else {
				preErr = taskruntime.ValidateSnapshot(&preSnapshot)
			}
			if preErr != nil {
				i18n.LocalServerError(c, http.StatusBadRequest, preErr)
				return
			}
		}
	}
	selectedConfig, _ := json.Marshal(body.StepConfigs)
	// 关联线上的任务必须带账号归属，否则上报轮询永远选不到它（线上会丢沟通记录）。
	cloudUserID := h.resolveCloudUserID(session)
	if cloudUserID == "" {
		i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_user_missing", "cloud_user_missing")
		return
	}
	taskUUID, err := taskruntime.NewService(h.taskDB(), h.taskRoot).Create(c.Request.Context(), taskruntime.CreateInput{
		SourceType: "cloud", CloudSourceKey: cloudSourceKey, CloudUserID: cloudUserID,
		WorkItemType: selected.Type, WorkItemID: strconv.FormatInt(selected.ID, 10), WorkspaceID: selected.WorkspaceID,
		Title: selected.Title, Content: selected.Description, WorkDirs: workDirs, ProjectUUID: body.ProjectUUID,
		SubprojectUUIDs: body.SubprojectUUIDs, Status: "pending", SelectedPipelineUUID: selectedPipelineUUID,
		SelectedPipelineConfigJSON: string(selectedConfig),
	})
	if err != nil {
		respondTaskCreateError(c, http.StatusConflict, err)
		return
	}
	if body.Status == protocol.TaskStatusInProgress || body.Status == protocol.TaskStatusActive {
		if err = h.ensureTaskPipelineAssigned(c.Request.Context(), taskUUID, pipelineAssignmentInput{PipelineUUID: selectedPipelineUUID, StepConfigs: configs}); err != nil {
			i18n.LocalServerErrorWith(c, http.StatusConflict, err, gin.H{"uuid": taskUUID})
			return
		}
		if _, err = h.startTaskExecution(c.Request.Context(), taskUUID, ""); err != nil {
			i18n.LocalServerErrorWith(c, http.StatusInternalServerError, err, gin.H{"uuid": taskUUID})
			return
		}
	} else if len(configs) > 0 {
		// 待开始任务：分配流水线快照（CLI 和模型在 Start 时校验）
		if err = h.ensureTaskPipelineAssigned(c.Request.Context(), taskUUID, pipelineAssignmentInput{PipelineUUID: selectedPipelineUUID, StepConfigs: configs}); err != nil {
			i18n.LocalServerErrorWith(c, http.StatusConflict, err, gin.H{"uuid": taskUUID})
			return
		}
	}
	// 导入完成后立即同步一次任务投影到云端，避免后续会话推送被云端以 404「任务不存在」拒绝。
	// 同步调用以确保返回 201 时云端已有任务记录；推送失败不阻塞导入响应，仅返回 warning。
	// 分支 C（无 configs）由于 pipeline_snapshot_uuid 为空，BuildTaskSync 会静默跳过，无副作用。
	warning := ""
	var pushSnapshotUUID string
	if h.taskDB() != nil {
		_ = h.taskDB().QueryRowContext(c.Request.Context(), `SELECT pipeline_snapshot_uuid FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&pushSnapshotUUID)
	}
	applog.Info("[CloudSync] 导入后准备同步任务投影", "task_uuid", taskUUID,
		"pipeline_snapshot_uuid", pushSnapshotUUID, "cloud_client_nil", h.taskCloudClient() == nil)
	if pushSnapshotUUID == "" {
		applog.Warn("[CloudSync] 导入任务未分配流水线快照，本次不会推送云端；进入详情页分配流水线/步骤后，首次执行或状态变更时才会同步",
			"task_uuid", taskUUID)
	}
	if h.taskCloudClient() == nil || h.taskDB() == nil {
		applog.Warn("[CloudSync] 导入后未推送任务投影：云端客户端或任务数据库未初始化",
			"cloud_client_nil", h.taskCloudClient() == nil, "db_nil", h.taskDB() == nil, "task_uuid", taskUUID)
	} else {
		syncCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		if err := taskSyncPusher.PushTaskSync(syncCtx, h.taskDB(), h.taskCloudClient(), taskUUID); err != nil {
			applog.Warn("[CloudSync] 导入后推送任务投影失败", "task_uuid", taskUUID, "error", err.Error())
			warning = i18n.T(c, "localserver_import_sync_warning")
		} else {
			applog.Info("[CloudSync] 导入后任务投影已推送云端", "task_uuid", taskUUID)
		}
		cancel()
	}
	resp := gin.H{"uuid": taskUUID}
	if warning != "" {
		resp["warning"] = warning
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *TasksHandler) currentSessionForRequest(c *gin.Context) *browserSession {
	session, ok := getSession(c)
	if !ok {
		return nil
	}
	return session
}

// listMyWork returns the "My Work" list in the cloud and marks the work items for which local tasks have been created.
func (h *TasksHandler) listMyWork(c *gin.Context) {
	session, ok := getSession(c)
	if !ok {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}

	result, err := h.taskCloudClient().GetMyWork(c.Request.Context())
	if err != nil {
		var apiErr *cloud.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_session_expired", "cloud_session_expired")
			return
		}
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}

	// The team board groups the work items by lane, so the bound local task's
	// status is returned together with its uuid: without it the board would need
	// a second (paginated) /tasks request and could misplace items that fall
	// outside the first page.
	primaryKey, alternateKey := pipeline.CloudSourceKeys(h.taskCloudClient().BaseURL())
	assigned, err := loadBoundCloudTasks(h.taskDB(), primaryKey, alternateKey, session.UserID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	annotateMyWorkItems(result.List, assigned)
	c.JSON(http.StatusOK, gin.H{"items": result.List})
}

func (h *TasksHandler) getTeamWorkItem(c *gin.Context) {
	if h.taskCloudClient() == nil {
		i18n.Error(c, http.StatusUnauthorized, "localserver_not_authenticated", "not_authenticated")
		return
	}
	workspaceID, err := strconv.ParseInt(c.Param("workspaceId"), 10, 64)
	if err != nil || workspaceID <= 0 {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	workItemID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workItemID <= 0 {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
		return
	}
	item, err := h.taskCloudClient().GetWorkItem(c.Request.Context(), workspaceID, workItemID)
	if err != nil {
		var apiErr *cloud.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_session_expired", "cloud_session_expired")
			return
		}
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	comments, err := h.taskCloudClient().ListWorkItemComments(c.Request.Context(), workspaceID, workItemID)
	if err != nil {
		applog.Warn("[TeamWork] 加载原始需求评论失败", "workspace_id", workspaceID, "work_item_id", workItemID, "error", err.Error())
		comments = []map[string]interface{}{}
	}
	attachments, err := h.taskCloudClient().ListWorkItemAttachments(c.Request.Context(), workspaceID, workItemID)
	if err != nil {
		applog.Warn("[TeamWork] 加载原始需求附件失败", "workspace_id", workspaceID, "work_item_id", workItemID, "error", err.Error())
		attachments = []map[string]interface{}{}
	}
	// 自定义字段只存字段值，选项型字段存的是选项 key，所以定义也要一并下发。
	typeKey, _ := item["type"].(string)
	customFields, err := h.taskCloudClient().ListWorkItemCustomFields(c.Request.Context(), workspaceID, typeKey)
	if err != nil {
		applog.Warn("[TeamWork] 加载原始需求自定义字段失败", "workspace_id", workspaceID, "work_item_id", workItemID, "error", err.Error())
		customFields = []cloud.CustomFieldDefinition{}
	}
	c.JSON(http.StatusOK, gin.H{
		"item":          item,
		"comments":      comments,
		"attachments":   attachments,
		"custom_fields": customFields,
	})
}

// localTaskRef identifies the local task bound to a cloud work item.
type localTaskRef struct {
	uuid   string
	status string
}

// annotateMyWorkItems marks the work items that already have a bound local task.
// local_task_uuid lets the client open the task; local_task_status lets the team
// board place the work item into the matching lane.
func annotateMyWorkItems(items []map[string]interface{}, assigned map[string]localTaskRef) {
	for _, item := range items {
		workItemType, _ := item["type"].(string)
		workItemID := workItemIDString(item["id"])
		ref, ok := assigned[workItemType+":"+workItemID]
		if !ok || ref.uuid == "" {
			continue
		}
		item["local_task_uuid"] = ref.uuid
		item["local_task_status"] = ref.status
	}
}

// isMyWorkItemDone 兼容 bool、字符串 "true"/"false"、数字等多种形式判断 is_done 真值。
// 字段缺失或未知类型视为未完成。
func isMyWorkItemDone(item map[string]interface{}) bool {
	if item == nil {
		return false
	}
	raw, ok := item["is_done"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "y":
			return true
		case "false", "0", "no", "n", "":
			return false
		default:
			return false
		}
	case float64:
		return v != 0
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return false
		}
		return n != 0
	default:
		return false
	}
}

func workItemIDString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

// loadBoundCloudTasks 找出当前云端已经配过的本地任务。
// work_item_* 与 cloud_work_item_* 是同一身份的两套列，历史数据往往只写了一侧；
// http/https 只是同一主机的两种写法；绑定只认当前用户，与上传归属保持一致。
func loadBoundCloudTasks(db *sql.DB, primaryKey, alternateKey, currentUser string) (map[string]localTaskRef, error) {
	rows, err := db.Query(`
		SELECT COALESCE(NULLIF(work_item_type, ''), cloud_work_item_type, ''),
		       COALESCE(NULLIF(work_item_id, ''), cloud_work_item_id, ''),
		       uuid, status
		FROM gt_tasks
		WHERE source_type = 'cloud'
		  AND cloud_source_key IN (?, ?)
		  AND cloud_user_id = ?
		  AND COALESCE(NULLIF(work_item_id, ''), cloud_work_item_id, '') <> ''`,
		primaryKey, alternateKey, currentUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assigned := make(map[string]localTaskRef)
	for rows.Next() {
		var kind, id string
		var ref localTaskRef
		if err := rows.Scan(&kind, &id, &ref.uuid, &ref.status); err != nil {
			return nil, err
		}
		if currentUser != "" && kind != "" && id != "" && ref.uuid != "" {
			assigned[kind+":"+id] = ref
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assigned, nil
}

// getCliTaskCount returns the number of CLI tasks currently running globally.
func (h *TasksHandler) getCliTaskCount(c *gin.Context) {
	count := 0
	if orch := h.orchestrator(); orch != nil {
		if n, err := orch.GlobalRunningSessionCount(); err == nil {
			count = n
		}
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// cliDiscovery 默认读取快照；显式重新检测等待实际探测完成。
func (h *TasksHandler) cliDiscovery(c *gin.Context) {
	if c.Query("refresh") == "true" {
		if err := executor.RefreshCLIDiscovery(c.Request.Context()); err != nil {
			if c.Request.Context().Err() == nil {
				i18n.LocalServerKeyError(c, http.StatusServiceUnavailable, "localserver_internal_error", "cli_discovery_failed")
			}
			return
		}
	}
	items := executor.GetCLIDiscovery(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// cliDiscoveryModels 默认读取可用快照；空列表重试，refresh=true 强制探测。
func (h *TasksHandler) cliDiscoveryModels(c *gin.Context) {
	cliType := c.Query("cli_type")
	if cliType == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_cli_type_required", "cli_type_required")
		return
	}
	var models []string
	if c.Query("refresh") == "true" {
		models = executor.RefreshCLIModels(c.Request.Context(), cliType)
	} else {
		models = executor.GetCLIModels(c.Request.Context(), cliType)
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// listTasks task list
func (h *TasksHandler) listTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	status := c.Query("status")
	if status != "" {
		status = normalizeTaskStatus(status)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := `SELECT t.uuid, t.title, t.status, t.execution_status, t.cloud_agent_id, t.work_item_type, t.work_item_id,
		t.created_at, t.updated_at, t.content_snapshot,
		t.cloud_agent_name_snapshot, t.project_uuid, COALESCE(pr.name,''), COALESCE(pr.icon,''), t.selected_pipeline_uuid,
		t.pipeline_snapshot_uuid, t.execution_mode, t.execution_tool, t.vibe_coding_session_json, t.execution_index, t.vibe_coding_model,
		COALESCE(NULLIF(ps.name,''), sel.name, ''), COALESCE(NULLIF(ps.avatar,''), sel.avatar, ''),
		t.selected_expert_group_uuid, t.expert_group_snapshot_uuid, COALESCE(egs.name, eg.name, ''), COALESCE(egs.avatar, eg.avatar, ''),
		t.priority, t.planned_start_date, t.planned_end_date
		FROM gt_tasks t
		LEFT JOIN gt_projects pr ON pr.uuid=t.project_uuid
		LEFT JOIN gt_task_pipeline_snapshots ps ON ps.uuid=t.pipeline_snapshot_uuid
		LEFT JOIN gt_pipelines sel ON sel.uuid=t.selected_pipeline_uuid
		LEFT JOIN gt_task_expert_group_snapshots egs ON egs.uuid=t.expert_group_snapshot_uuid
		LEFT JOIN gt_expert_groups eg ON eg.uuid=t.selected_expert_group_uuid`
	// 本地任务页签只看本地新建的任务。团队工作配置后在本地生成的任务 source_type=cloud，
	// 只出现在团队工作里；不传 source_type 时仍返回全部，供团队看板按 uuid 取卡片。
	localOnly := c.Query("source_type") == "local"
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 3)
	scopeSQL, scopeArgs := taskScopeSQL(h.taskCloudClient(), "t")
	conditions = append(conditions, scopeSQL)
	args = append(args, scopeArgs...)
	if localOnly {
		conditions = append(conditions, `COALESCE(t.source_type, 'local') <> 'cloud'`)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		// 转义 LIKE 通配符，保持与前端 includes 相同的字面子串匹配。
		keyword = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
		conditions = append(conditions, `t.title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+keyword+"%")
	}
	if rawUUIDs := c.Query("uuids"); c.Request.URL.Query().Has("uuids") {
		values := strings.Split(rawUUIDs, ",")
		if len(values) > 100 {
			i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
			return
		}
		marks := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if _, err := uuid.Parse(value); err != nil {
				i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "request_invalid")
				return
			}
			marks = append(marks, "?")
			args = append(args, value)
		}
		conditions = append(conditions, `t.uuid IN (`+strings.Join(marks, ",")+`)`)
	}
	// 列表按可见泳道计数和分页，沿用 Renderer 的状态归类，不影响旧 status 查询。
	laneStatus := `CASE WHEN t.status='todo' THEN 'pending'
		WHEN t.status IN ('in_progress','running','developing','developed') THEN 'active'
		WHEN t.status='completed' THEN 'done' ELSE t.status END`
	listView := localOnly && c.Query("list_view") == "true"
	statusCounts := make(map[string]int64)
	if listView {
		conditions = append(conditions, `trim(COALESCE(t.work_item_id,''))=''`,
			laneStatus+` IN (SELECT lane_key FROM gt_task_lanes WHERE is_hidden=0)`)
		countSQL := `SELECT ` + laneStatus + `, COUNT(*) FROM gt_tasks t WHERE ` + strings.Join(conditions, " AND ") + ` GROUP BY 1`
		countRows, err := h.taskDB().QueryContext(c.Request.Context(), countSQL, args...)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		for countRows.Next() {
			var key string
			var count int64
			if err := countRows.Scan(&key, &count); err != nil {
				countRows.Close()
				i18n.LocalServerError(c, http.StatusInternalServerError, err)
				return
			}
			statusCounts[key] = count
		}
		err = countRows.Err()
		countRows.Close()
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
	}
	if status != "" {
		statusColumn := "t.status"
		if listView {
			statusColumn = laneStatus
		}
		conditions = append(conditions, statusColumn+` = ?`)
		args = append(args, status)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY t.created_at DESC, t.uuid DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := h.taskDB().QueryContext(c.Request.Context(), query, listArgs...)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	type TaskItem struct {
		UUID                      string              `json:"uuid"`
		Title                     string              `json:"title"`
		Status                    string              `json:"status"`
		ExecutionStatus           string              `json:"execution_status"`
		AgentID                   string              `json:"agent_id"`
		WorkItemType              string              `json:"work_item_type"`
		WorkItemID                string              `json:"work_item_id"`
		CreatedAt                 int64               `json:"created_at"`
		UpdatedAt                 int64               `json:"updated_at"`
		ContentSnapshot           string              `json:"content_snapshot"`
		AgentNameSnapshot         string              `json:"agent_name_snapshot"`
		ProjectUUID               string              `json:"project_uuid"`
		ProjectName               string              `json:"project_name"`
		ProjectIcon               string              `json:"project_icon"`
		SelectedPipelineUUID      string              `json:"selected_pipeline_uuid"`
		PipelineSnapshotUUID      string              `json:"pipeline_snapshot_uuid"`
		PipelineNameSnapshot      string              `json:"pipeline_name_snapshot"`
		PipelineAvatarSnapshot    string              `json:"pipeline_avatar_snapshot"`
		ExecutionMode             string              `json:"execution_mode"`
		ExecutionTool             string              `json:"execution_tool"`
		ExecutionModel            string              `json:"execution_model"`
		ExecutionToolName         string              `json:"execution_tool_name,omitempty"`
		ExecutionBound            bool                `json:"execution_bound"`
		ExecutionIndex            int                 `json:"execution_index"`
		SelectedExpertGroupUUID   string              `json:"selected_expert_group_uuid"`
		ExpertGroupSnapshotUUID   string              `json:"expert_group_snapshot_uuid"`
		ExpertGroupNameSnapshot   string              `json:"expert_group_name_snapshot"`
		ExpertGroupAvatarSnapshot string              `json:"expert_group_avatar_snapshot"`
		Priority                  string              `json:"priority"`
		PlannedStartDate          string              `json:"planned_start_date"`
		PlannedEndDate            string              `json:"planned_end_date"`
		LatestActivity            *taskLatestActivity `json:"latest_activity,omitempty"`
	}

	items := make([]TaskItem, 0)
	for rows.Next() {
		var t TaskItem
		var sessionJSON string
		// Scan must be aborted if it fails: ignoring the error will append the zero-valued TaskItem to the result set.
		//The front end gets the "ghost task" with an empty UUID.
		if err := rows.Scan(&t.UUID, &t.Title, &t.Status, &t.ExecutionStatus, &t.AgentID, &t.WorkItemType, &t.WorkItemID,
			&t.CreatedAt, &t.UpdatedAt, &t.ContentSnapshot, &t.AgentNameSnapshot, &t.ProjectUUID, &t.ProjectName, &t.ProjectIcon,
			&t.SelectedPipelineUUID, &t.PipelineSnapshotUUID, &t.ExecutionMode, &t.ExecutionTool, &sessionJSON, &t.ExecutionIndex, &t.ExecutionModel,
			&t.PipelineNameSnapshot, &t.PipelineAvatarSnapshot, &t.SelectedExpertGroupUUID, &t.ExpertGroupSnapshotUUID,
			&t.ExpertGroupNameSnapshot, &t.ExpertGroupAvatarSnapshot, &t.Priority, &t.PlannedStartDate, &t.PlannedEndDate); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		t.ExecutionBound, t.ExecutionToolName = vibeCodingBindingSummary(t.ExecutionMode, t.ExecutionTool, sessionJSON)
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	// 云端流水线仅存内存：快照未冻结时，用内存缓存补齐所选流水线的名称/头像展示。
	for index := range items {
		if items[index].PipelineNameSnapshot != "" || items[index].SelectedPipelineUUID == "" {
			continue
		}
		if pipe, ok := pipeline.DefaultStore().Get(items[index].SelectedPipelineUUID); ok {
			items[index].PipelineNameSnapshot = pipe.Name
			items[index].PipelineAvatarSnapshot = pipe.Avatar
		}
	}

	activityTargets := make([]taskActivityTarget, 0, len(items))
	for index := range items {
		activityTargets = append(activityTargets, taskActivityTarget{
			UUID: items[index].UUID, Status: items[index].Status, ExecutionMode: items[index].ExecutionMode,
		})
	}
	latestActivities, err := loadLatestTaskActivities(c.Request.Context(), h.taskDB(), activityTargets)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	for index := range items {
		items[index].LatestActivity = latestActivities[items[index].UUID]
	}

	var total int64
	countQuery := `SELECT COUNT(*) FROM gt_tasks t`
	if len(conditions) > 0 {
		countQuery += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	err = h.taskDB().QueryRowContext(c.Request.Context(), countQuery, args...).Scan(&total)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":         items,
		"total":         total,
		"page":          page,
		"page_size":     pageSize,
		"status_counts": statusCounts,
	})
}

func (h *TasksHandler) getTaskActivity(c *gin.Context) {
	taskUUID := strings.TrimSpace(c.Param("uuid"))
	activityID := strings.TrimSpace(c.Param("activity_id"))
	if taskUUID == "" || activityID == "" {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	activity, err := loadTaskActivityDetail(c.Request.Context(), h.taskDB(), taskUUID, activityID)
	if errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusNotFound, "localserver_activity_not_found", "activity_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, activity)
}

// getTask task details
func (h *TasksHandler) getTask(c *gin.Context) {
	if err := h.refreshPendingPipelineExecution(c.Request.Context(), c.Param("uuid")); err != nil {
		applog.Warn("[LocalServer] 补齐待开始流水线执行配置失败", "task_uuid", c.Param("uuid"), "error", err.Error())
	}
	task, err := h.loadTaskDetail(c.Request.Context(), c.Param("uuid"))
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, task)
}

func (h *TasksHandler) loadTaskDetail(ctx context.Context, taskUUID string) (map[string]interface{}, error) {
	var task map[string]interface{}
	var uuid, title, description, status, execStatus, sourceType, cloudSourceKey, agentID, cliType, workItemType, workItemID, workDir string
	var taskDir, currentStepUUID, selectedPipelineUUID, pipelineSnapshotUUID, pipelineName, pipelineAvatar, projectUUID, projectName, projectIcon string
	var selectedExpertGroupUUID, expertGroupSnapshotUUID, expertGroupName, expertGroupAvatar string
	var executionMode, executionTool, sessionJSON, executionModel string
	var executionIndex int
	var priority, plannedStartDate, plannedEndDate string
	var currentStepCompleted bool
	var createdFromConversation bool
	var awaitingMessage bool
	var createdAt, updatedAt, workspaceID int64
	err := h.taskDB().QueryRowContext(ctx,
		`SELECT t.uuid, t.title, t.content_snapshot, t.status, t.execution_status, t.source_type, t.cloud_source_key, t.cloud_agent_id, t.cli_type,
		        t.work_item_type, t.work_item_id, COALESCE(t.workspace_id,0), t.work_dir, t.task_dir, t.current_step_uuid, t.current_step_completed,
		        t.selected_pipeline_uuid, t.pipeline_snapshot_uuid, t.execution_mode, t.execution_tool, t.vibe_coding_session_json, t.execution_index, t.vibe_coding_model, COALESCE(NULLIF(p.name,''), sel.name, ''),
		        COALESCE(NULLIF(p.avatar,''), sel.avatar, ''), t.selected_expert_group_uuid, t.expert_group_snapshot_uuid,
		        COALESCE(egs.name, eg.name, ''), COALESCE(egs.avatar, eg.avatar, ''), t.project_uuid, COALESCE(pr.name,''), COALESCE(pr.icon,''),
		        t.priority, t.planned_start_date, t.planned_end_date, t.created_at, t.updated_at, t.created_from_conversation,
            (t.execution_index>0 AND t.started_at=0 AND t.status='pending' AND t.execution_mode<>'vibe_coding')
		 FROM gt_tasks t LEFT JOIN gt_task_pipeline_snapshots p ON p.uuid = t.pipeline_snapshot_uuid
		 LEFT JOIN gt_pipelines sel ON sel.uuid=t.selected_pipeline_uuid
		 LEFT JOIN gt_task_expert_group_snapshots egs ON egs.uuid=t.expert_group_snapshot_uuid
		 LEFT JOIN gt_expert_groups eg ON eg.uuid=t.selected_expert_group_uuid
		 LEFT JOIN gt_projects pr ON pr.uuid=t.project_uuid WHERE t.uuid = ?`,
		taskUUID).Scan(
		&uuid, &title, &description, &status, &execStatus, &sourceType, &cloudSourceKey, &agentID, &cliType, &workItemType, &workItemID, &workspaceID,
		&workDir, &taskDir, &currentStepUUID, &currentStepCompleted, &selectedPipelineUUID, &pipelineSnapshotUUID, &executionMode, &executionTool, &sessionJSON, &executionIndex, &executionModel, &pipelineName,
		&pipelineAvatar, &selectedExpertGroupUUID, &expertGroupSnapshotUUID, &expertGroupName, &expertGroupAvatar,
		&projectUUID, &projectName, &projectIcon, &priority, &plannedStartDate, &plannedEndDate, &createdAt, &updatedAt, &createdFromConversation, &awaitingMessage,
	)
	if err != nil {
		return nil, err
	}

	storedDescription := description
	// 云端任务正文是创建时保存的快照。旧版本可能把 /uploads/... 相对地址直接
	// 写入本地库，浏览器会把它解析成本地 sidecar 地址并请求失败。读取详情时再次
	// 做幂等归一化即可兼容旧数据，无需覆盖用户的本地快照。
	if client := h.taskCloudClient(); sourceType == "cloud" && client != nil &&
		(cloudSourceKey == "" || cloudSourceKey == pipeline.CloudSourceKey(client.BaseURL())) {
		description = client.NormalizeWorkItemContent(description)
		description = h.refreshTaskCloudAssetURLs(
			ctx, taskUUID, workspaceID, workItemID, storedDescription, description, client,
		)
	}

	// 云端流水线仅存内存：快照未冻结时，用内存缓存补齐所选流水线的名称/头像展示。
	if pipelineName == "" && selectedPipelineUUID != "" {
		if pipe, ok := pipeline.DefaultStore().Get(selectedPipelineUUID); ok {
			pipelineName = pipe.Name
			pipelineAvatar = pipe.Avatar
		}
	}

	executionBound, toolName := vibeCodingBindingSummary(executionMode, executionTool, sessionJSON)
	task = map[string]interface{}{
		"uuid":                         uuid,
		"title":                        title,
		"description":                  description,
		"status":                       status,
		"execution_status":             execStatus,
		"source_type":                  sourceType,
		"awaiting_message":             awaitingMessage,
		"created_from_conversation":    createdFromConversation,
		"agent_id":                     agentID,
		"cli_type":                     cliType,
		"work_item_type":               workItemType,
		"work_item_id":                 workItemID,
		"workspace_id":                 workspaceID,
		"work_dir":                     workDir,
		"task_dir":                     taskDir,
		"current_step_uuid":            currentStepUUID,
		"current_step_completed":       currentStepCompleted,
		"pipeline_snapshot_uuid":       pipelineSnapshotUUID,
		"selected_pipeline_uuid":       selectedPipelineUUID,
		"execution_mode":               executionMode,
		"execution_tool":               executionTool,
		"execution_model":              executionModel,
		"execution_tool_name":          toolName,
		"execution_bound":              executionBound,
		"execution_index":              executionIndex,
		"pipeline_name_snapshot":       pipelineName,
		"pipeline_avatar_snapshot":     pipelineAvatar,
		"selected_expert_group_uuid":   selectedExpertGroupUUID,
		"expert_group_snapshot_uuid":   expertGroupSnapshotUUID,
		"expert_group_name_snapshot":   expertGroupName,
		"expert_group_avatar_snapshot": expertGroupAvatar,
		"priority":                     priority,
		"planned_start_date":           plannedStartDate,
		"planned_end_date":             plannedEndDate,
		"project_uuid":                 projectUUID, "project_name": projectName, "project_icon": projectIcon,
		"created_at": createdAt,
		"updated_at": updatedAt,
	}

	workDirs, err := h.loadTaskWorkDirs(taskUUID, workDir)
	if err != nil {
		return nil, err
	}
	task["work_dirs"] = workDirs
	// CLI 直接执行的模型记录在隐式步骤上，详情页需要它来固定展示「直接执行：CLI · 模型」。
	if executionMode == taskruntime.ExecutionModeCLI {
		var cliStepModel string
		if scanErr := h.taskDB().QueryRowContext(ctx,
			`SELECT model_name FROM gt_task_steps WHERE task_uuid=? AND step_key=? LIMIT 1`,
			taskUUID, taskruntime.CLIDirectStepKey).Scan(&cliStepModel); scanErr != nil && scanErr != sql.ErrNoRows {
			return nil, fmt.Errorf("读取 CLI 直接执行模型失败: %w", scanErr)
		}
		task["execution_model"] = cliStepModel
	}
	projectRows, err := h.taskDB().QueryContext(ctx, `SELECT l.project_uuid, p.name, p.icon, p.main_dir, l.relation_type, l.sort_order
		FROM gt_task_project_links l JOIN gt_projects p ON p.uuid=l.project_uuid WHERE l.task_uuid=? ORDER BY l.sort_order, l.id`, taskUUID)
	if err != nil {
		return nil, err
	}
	// projects 用 []map[string]interface{}（JSON 输出与 gin.H 完全一致），
	// 避免消费方按 []gin.H / []map[string]interface{} 断言时产生类型漂移。
	projects := make([]map[string]interface{}, 0)
	for projectRows.Next() {
		var id, name, icon, mainDir, relation string
		var order int
		if err := projectRows.Scan(&id, &name, &icon, &mainDir, &relation, &order); err != nil {
			projectRows.Close()
			return nil, err
		}
		projects = append(projects, map[string]interface{}{"uuid": id, "name": name, "icon": icon, "icon_type": icon,
			"main_dir": mainDir, "local_dir": mainDir, "relation_type": relation, "sort_order": order})
	}
	projectRows.Close()
	if err := projectRows.Err(); err != nil {
		return nil, err
	}
	task["projects"] = projects

	// Query steps
	rows, err := h.taskDB().QueryContext(ctx,
		`SELECT uuid, step_key, name, description, avatar, step_order, cli_type, model_name, status, execution_status, prompt_snapshot, step_dir, member_role
		 FROM gt_task_steps WHERE task_uuid = ? ORDER BY step_order`,
		taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []map[string]interface{}
	for rows.Next() {
		var sUUID, stepKey, name, description, avatar, cliType, modelName, stepStatus, stepExecStatus, prompt, stepDir, memberRole string
		var sortOrder int
		if err := rows.Scan(&sUUID, &stepKey, &name, &description, &avatar, &sortOrder, &cliType, &modelName, &stepStatus, &stepExecStatus, &prompt, &stepDir, &memberRole); err != nil {
			return nil, fmt.Errorf("读取任务步骤失败: %w", err)
		}
		steps = append(steps, map[string]interface{}{
			"uuid":             sUUID,
			"step_key":         stepKey,
			"name":             name,
			"description":      description,
			"avatar":           avatar,
			"sort_order":       sortOrder,
			"cli_type":         cliType,
			"model":            modelName,
			"model_name":       modelName,
			"status":           stepStatus,
			"execution_status": stepExecStatus,
			"prompt_snapshot":  prompt,
			"step_dir":         stepDir,
			"member_role":      memberRole,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if steps == nil {
		steps = []map[string]interface{}{}
	}

	task["steps"] = steps
	return task, nil
}

const taskAssetRefreshRetryDelay = time.Minute

func (h *TasksHandler) refreshTaskCloudAssetURLs(
	ctx context.Context,
	taskUUID string,
	workspaceID int64,
	workItemID string,
	storedDescription string,
	description string,
	client *cloud.Client,
) string {
	now := time.Now()
	if !cloud.WorkItemContentNeedsAssetRefresh(description, now) || workspaceID <= 0 {
		return description
	}
	parsedWorkItemID, err := strconv.ParseInt(strings.TrimSpace(workItemID), 10, 64)
	if err != nil || parsedWorkItemID <= 0 {
		return description
	}

	// 任务执行期间详情页可能以 2 秒间隔轮询。失败后退避一分钟，并在请求开始前
	// 占用刷新窗口，避免云端不可用时产生并发重试和长时间阻塞。
	h.assetRefreshMu.Lock()
	if h.assetRefreshNext == nil {
		h.assetRefreshNext = make(map[string]time.Time)
	}
	for cachedTaskUUID, next := range h.assetRefreshNext {
		if !now.Before(next) {
			delete(h.assetRefreshNext, cachedTaskUUID)
		}
	}
	if next := h.assetRefreshNext[taskUUID]; now.Before(next) {
		h.assetRefreshMu.Unlock()
		return description
	}
	h.assetRefreshNext[taskUUID] = now.Add(taskAssetRefreshRetryDelay)
	h.assetRefreshMu.Unlock()

	refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	item, err := client.GetWorkItem(refreshCtx, workspaceID, parsedWorkItemID)
	if err != nil {
		applog.Warn("[LocalServer] 刷新云端任务图片签名失败", "task_uuid", taskUUID, "error", err.Error())
		return description
	}
	freshDescription := workItemDescription(item)
	refreshed := cloud.RefreshWorkItemContentAssetURLs(description, freshDescription)
	if refreshed == description {
		return description
	}

	// 只更新签名 URL，并用旧快照做 CAS 条件，避免覆盖同时发生的任务编辑。
	if _, err := h.taskDB().ExecContext(ctx,
		`UPDATE gt_tasks SET content_snapshot=? WHERE uuid=? AND content_snapshot=?`,
		refreshed, taskUUID, storedDescription,
	); err != nil {
		applog.Warn("[LocalServer] 保存刷新的云端任务图片签名失败", "task_uuid", taskUUID, "error", err.Error())
	} else {
		h.assetRefreshMu.Lock()
		delete(h.assetRefreshNext, taskUUID)
		h.assetRefreshMu.Unlock()
	}
	return refreshed
}

func workItemDescription(item map[string]interface{}) string {
	for _, key := range []string{"description", "content"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *TasksHandler) loadTaskWorkDirs(taskUUID, legacyWorkDir string) ([]string, error) {
	rows, err := h.taskDB().Query(
		`SELECT path FROM gt_task_work_dirs WHERE task_uuid = ? ORDER BY sort_order, id`,
		taskUUID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询任务工作目录失败: %w", err)
	}
	defer rows.Close()

	workDirs := make([]string, 0)
	for rows.Next() {
		var workDir string
		if err := rows.Scan(&workDir); err != nil {
			return nil, fmt.Errorf("读取任务工作目录失败: %w", err)
		}
		workDirs = append(workDirs, workDir)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历任务工作目录失败: %w", err)
	}
	if len(workDirs) == 0 && strings.TrimSpace(legacyWorkDir) != "" {
		workDirs = append(workDirs, legacyWorkDir)
	}
	return workDirs, nil
}

// createTask creates a task
func (h *TasksHandler) createTask(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTaskCreateRequestSize)
	var body struct {
		Title                   string                `json:"title"`
		Description             string                `json:"description"`
		PipelineUUID            string                `json:"pipeline_uuid"`
		ExecutionMode           string                `json:"execution_mode"`
		ExecutionTool           string                `json:"execution_tool"`
		ModelName               string                `json:"model_name"`
		ExpertGroupUUID         string                `json:"expert_group_uuid"`
		StepConfigs             []stepExecutionConfig `json:"step_configs"`
		ProjectUUID             string                `json:"project_uuid"`
		SubprojectUUIDs         []string              `json:"subproject_uuids"`
		ChildProjectUUIDs       []string              `json:"child_project_uuids"`
		WorkDir                 string                `json:"work_dir"`
		WorkDirs                []string              `json:"work_dirs"`
		Priority                string                `json:"priority"`
		PlannedStartDate        string                `json:"planned_start_date"`
		PlannedEndDate          string                `json:"planned_end_date"`
		Status                  string                `json:"status"`
		WorkItemType            string                `json:"work_item_type"`
		WorkItemID              json.RawMessage       `json:"work_item_id"`
		WorkspaceID             int64                 `json:"workspace_id"`
		CreateNotification      bool                  `json:"create_notification"`
		CreatedFromConversation bool                  `json:"created_from_conversation"`
		Attachments             []taskAttachmentInput `json:"attachments"`
		// SyncAgentOutput 任务级「同步 Agent 产出」开关；缺省（nil）按开启处理，
		// 兼容对话页等未携带该字段的历史入口。
		SyncAgentOutput *bool `json:"sync_agent_output"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			i18n.Error(c, http.StatusRequestEntityTooLarge, "localserver_task_content_too_large", "task_content_too_large")
			return
		}
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	sourceType := "local"
	var cloudSourceKey, cloudUserID string
	workItemID := strings.Trim(string(body.WorkItemID), "\"")
	if body.WorkItemType != "" && workItemID != "" {
		// 团队工作导入：复用新建任务表单，但保留云端工作项关联，
		// 使「我的工作」列表能标记该需求/缺陷为已导入。
		session := h.currentSessionForRequest(c)
		if session == nil || h.taskCloudClient() == nil {
			i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_import_login_required", "cloud_import_login_required")
			return
		}
		sourceType = "cloud"
		cloudSourceKey = pipeline.CloudSourceKey(h.taskCloudClient().BaseURL())
		cloudUserID = h.resolveCloudUserID(session)
		// 关联线上的任务必须带账号归属：缺了它，这条任务永远进不了上报队列
		// （轮询按 cloud_user_id 过滤），线上也就收不到任何沟通记录。
		if cloudUserID == "" {
			i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_user_missing", "cloud_user_missing")
			return
		}
		// 团队导入需携带工作项所属工作区 ID，否则云端校验 workspace_id>0 会拒绝创建任务投影，
		// 进而导致会话记录推送被 404「任务不存在」拒绝。前端创建表单未必传 workspace_id。
		// 补全来源是「我的工作」：它带真实类型和工作区，且不截断为最新 100 条。
		// /api/client/work-items 会把缺陷也标成需求，并只返回最新一页，不能用来补全。
		typeValid := body.WorkItemType == "requirement" || body.WorkItemType == "defect"
		if body.WorkspaceID == 0 || !typeValid {
			if result, werr := h.taskCloudClient().GetMyWork(c.Request.Context()); werr == nil && result != nil {
				if workspaceID, itemType, found := cloud.LookupMyWorkItem(result.List, workItemID); found {
					if body.WorkspaceID == 0 {
						body.WorkspaceID = workspaceID
					}
					if !typeValid && (itemType == "requirement" || itemType == "defect") {
						body.WorkItemType = itemType
					}
				}
			} else if werr != nil {
				applog.Warn("[CloudSync] 团队导入补全 workspace_id 失败：拉取我的工作出错", "work_item_id", workItemID, "error", werr.Error())
			}
			if body.WorkspaceID == 0 {
				applog.Warn("[CloudSync] 团队导入未获取到 workspace_id，云端将拒绝创建任务投影（会话推送会 404）", "work_item_id", workItemID, "work_item_type", body.WorkItemType)
			}
		}
	}

	rawWorkDirs := body.WorkDirs
	if len(rawWorkDirs) == 0 && strings.TrimSpace(body.WorkDir) != "" {
		rawWorkDirs = []string{body.WorkDir}
	}
	if len(body.SubprojectUUIDs) == 0 {
		body.SubprojectUUIDs = body.ChildProjectUUIDs
	}
	executionMode := taskruntime.NormalizeExecutionMode(body.ExecutionMode)
	executionTool := strings.ToLower(strings.TrimSpace(body.ExecutionTool))
	executionModel := strings.TrimSpace(body.ModelName)
	if executionMode == "" && strings.TrimSpace(body.PipelineUUID) != "" {
		executionMode = taskruntime.ExecutionModePipeline
	}
	// create_notification 不再强制 pipeline 模式：无流水线的配置任务（如团队需求
	// 配置后仅创建不启动）保持无模式，由通用通知块只写创建通知。
	if err := taskruntime.ValidateExecutionTarget(executionMode, executionTool, body.PipelineUUID, body.ExpertGroupUUID); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if executionMode == taskruntime.ExecutionModeCLI {
		if err := taskruntime.ValidateCLITarget(executionTool, executionModel); err != nil {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
	}
	if executionMode == taskruntime.ExecutionModePipeline && strings.TrimSpace(body.PipelineUUID) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_uuid_required", "pipeline_required")
		return
	}
	var err error
	// 通知创建路径会在创建任务前构建并校验完整快照，无需先重复读取流水线。
	// 其他路径保留原有的提前存在性检查，避免改变历史接口行为。
	if strings.TrimSpace(body.PipelineUUID) != "" && !body.CreateNotification {
		if _, err = pipeline.NewService(h.taskDB()).Get(c.Request.Context(), strings.TrimSpace(body.PipelineUUID)); err != nil {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
	}
	selectedConfig, err := json.Marshal(body.StepConfigs)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	requestedStatus := normalizeTaskStatus(body.Status)
	var preparedSnapshot taskruntime.Snapshot
	preparedSnapshotReady := false
	var preparedExpertGroup *expertgroup.Group
	if executionMode == taskruntime.ExecutionModeExpertGroup {
		preparedExpertGroup, err = expertgroup.NewService(h.taskDB()).ValidateReady(c.Request.Context(), body.ExpertGroupUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
	}
	if body.CreateNotification && executionMode == taskruntime.ExecutionModePipeline {
		if strings.TrimSpace(body.PipelineUUID) == "" {
			i18n.Error(c, http.StatusBadRequest, "localserver_notification_pipeline_required", "pipeline_required")
			return
		}
		var buildErr error
		preparedSnapshot, buildErr = h.snapshotFromPipeline(c.Request.Context(), pipelineAssignmentInput{PipelineUUID: body.PipelineUUID, StepConfigs: body.StepConfigs})
		if buildErr != nil {
			status := http.StatusInternalServerError
			code := "pipeline_load_failed"
			if errors.Is(buildErr, pipeline.ErrNotFound) {
				status = http.StatusBadRequest
				code = "pipeline_not_found"
			}
			i18n.LocalServerErrorWith(c, status, buildErr, gin.H{"code": code})
			return
		}
		// 显式 pending 是团队需求配置：先记下流水线，CLI/模型可以稍后在详情里补。
		// 只有对话页未传状态、创建后立刻启动时，才要求每个 Agent 都已配好 CLI 和模型。
		startImmediately := strings.TrimSpace(body.Status) == ""
		validateSnapshot := taskruntime.ValidateSnapshot
		if startImmediately {
			validateSnapshot = taskruntime.ValidateSnapshotFull
		}
		if validateErr := validateSnapshot(&preparedSnapshot); validateErr != nil {
			i18n.LocalServerErrorWith(c, http.StatusBadRequest, validateErr, gin.H{"code": "pipeline_incomplete"})
			return
		}
		preparedSnapshotReady = true
		if startImmediately {
			requestedStatus = "active"
		}
	}
	initialStatus := requestedStatus
	if requestedStatus == "active" && executionMode == "" {
		i18n.Error(c, http.StatusConflict, "localserver_execution_mode_required", "execution_mode_required")
		return
	}
	if initialStatus == "" || (initialStatus == "active" && executionMode == taskruntime.ExecutionModePipeline) {
		initialStatus = "pending"
	}
	taskService := taskruntime.NewService(h.taskDB(), h.taskRoot)
	// 「同步 Agent 产出」缺省开启：只有明确传 false 才关闭该任务的自动文件上报。
	syncAgentOutput := true
	if body.SyncAgentOutput != nil {
		syncAgentOutput = *body.SyncAgentOutput
	}
	taskUUID, err := taskService.Create(c.Request.Context(), taskruntime.CreateInput{
		SourceType: sourceType, CloudSourceKey: cloudSourceKey, CloudUserID: cloudUserID,
		WorkItemType: body.WorkItemType, WorkItemID: workItemID, WorkspaceID: body.WorkspaceID,
		Title: body.Title, Content: body.Description, WorkDirs: rawWorkDirs,
		CreatedFromConversation: body.CreatedFromConversation,
		ProjectUUID:             body.ProjectUUID, SubprojectUUIDs: body.SubprojectUUIDs, Status: initialStatus,
		Priority: body.Priority, PlannedStartDate: body.PlannedStartDate, PlannedEndDate: body.PlannedEndDate,
		SelectedPipelineUUID: strings.TrimSpace(body.PipelineUUID), SelectedPipelineConfigJSON: string(selectedConfig),
		SelectedExpertGroupUUID: strings.TrimSpace(body.ExpertGroupUUID),
		ExecutionMode:           executionMode, ExecutionTool: executionTool, ExecutionModel: executionModel,
		SyncAgentOutput: &syncAgentOutput,
	})
	if err != nil {
		respondTaskCreateError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.appendDraftTaskAttachments(c.Request.Context(), taskUUID, body.Description, body.Attachments); err != nil {
		_ = taskService.Delete(c.Request.Context(), taskUUID)
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	sessionUUID := ""
	if requestedStatus == "active" && executionMode == taskruntime.ExecutionModePipeline {
		snapshot := preparedSnapshot
		if !preparedSnapshotReady {
			var buildErr error
			snapshot, buildErr = h.snapshotFromPipeline(c.Request.Context(), pipelineAssignmentInput{PipelineUUID: body.PipelineUUID, StepConfigs: body.StepConfigs})
			if buildErr != nil {
				i18n.LocalServerErrorWith(c, http.StatusBadRequest, buildErr, gin.H{"uuid": taskUUID})
				return
			}
		}
		if assignErr := taskruntime.NewService(h.taskDB(), h.taskRoot).AssignPipeline(c.Request.Context(), taskUUID, snapshot); assignErr != nil {
			i18n.LocalServerErrorWith(c, http.StatusConflict, assignErr, gin.H{"uuid": taskUUID})
			return
		}
		sessionUUID, err = h.startTaskExecution(c.Request.Context(), taskUUID, "")
		if err != nil {
			i18n.LocalServerErrorWith(c, http.StatusInternalServerError, err, gin.H{"uuid": taskUUID})
			return
		}
	} else if executionMode == taskruntime.ExecutionModeExpertGroup {
		if assignErr := taskruntime.NewService(h.taskDB(), h.taskRoot).AssignExpertGroup(c.Request.Context(), taskUUID, preparedExpertGroup); assignErr != nil {
			i18n.LocalServerErrorWith(c, http.StatusConflict, assignErr, gin.H{"uuid": taskUUID})
			return
		}
		// 显式 pending 仅创建通知；未指定状态的对话创建流程仍沿用立即执行行为。
		if requestedStatus == "active" || (body.CreateNotification && strings.TrimSpace(body.Status) == "") {
			sessionUUID, err = h.startTaskExecution(c.Request.Context(), taskUUID, "")
			if err != nil {
				i18n.LocalServerErrorWith(c, http.StatusInternalServerError, err, gin.H{"uuid": taskUUID})
				return
			}
		}
	} else if strings.TrimSpace(body.PipelineUUID) != "" {
		// 新建任务选择了流水线但未立即启动：步骤 CLI/模型齐全时直接初始化执行副本（task_steps），
		// 步骤缺少本机配置的流水线不在此处生成快照，进入详情页后通过指定流水线引导弹窗补全。
		if snapshot, buildErr := h.snapshotFromPipeline(c.Request.Context(), pipelineAssignmentInput{PipelineUUID: body.PipelineUUID, StepConfigs: body.StepConfigs}); buildErr == nil {
			if assignErr := taskruntime.NewService(h.taskDB(), h.taskRoot).AssignPipeline(c.Request.Context(), taskUUID, snapshot); assignErr != nil {
				i18n.LocalServerErrorWith(c, http.StatusConflict, assignErr, gin.H{"uuid": taskUUID})
				return
			}
		}
	}
	if requestedStatus == "active" && executionMode == taskruntime.ExecutionModeVibeCoding {
		if err := h.ensureVibeCodingConversation(c.Request.Context(), taskUUID, i18n.T(c, "localserver_vibe_coding_assigned_activity"), body.CreateNotification); err != nil {
			if cleanupErr := taskService.Delete(c.Request.Context(), taskUUID); cleanupErr != nil {
				applog.Error("[createTask] Vibe Coding 会话初始化失败后清理任务失败", "task_uuid", taskUUID, "error", cleanupErr.Error())
				i18n.LocalServerErrorWith(c, http.StatusInternalServerError, err, gin.H{"uuid": taskUUID})
				return
			}
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
	}
	if body.CreateNotification {
		var stepUUID, progressUUID string
		var stepOrder int
		if sessionUUID != "" {
			_ = h.taskDB().QueryRowContext(c.Request.Context(),
				`SELECT s.uuid, s.sort_order, cs.task_progress_uuid
				 FROM gt_cli_sessions cs
				 JOIN gt_task_steps s ON s.uuid = cs.step_uuid
				 WHERE cs.uuid = ?`, sessionUUID).Scan(&stepUUID, &stepOrder, &progressUUID)
		}
		if stepUUID == "" {
			_ = h.taskDB().QueryRowContext(c.Request.Context(),
				`SELECT uuid, sort_order FROM gt_task_steps
				 WHERE task_uuid = ? ORDER BY sort_order LIMIT 1`, taskUUID).Scan(&stepUUID, &stepOrder)
		}

		taskDetail, detailErr := h.loadTaskDetail(c.Request.Context(), taskUUID)
		if detailErr != nil {
			// 详情加载失败也不能丢创建通知：对话列表按通知分组，缺了这条记录
			// 新建任务会在列表里不可见（直到首次执行），体验上就是「刚建的任务不见了」。
			// 兜底直接从任务主表取标题写入基础通知。
			var fallbackTitle string
			_ = h.taskDB().QueryRowContext(c.Request.Context(), `SELECT title FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&fallbackTitle)
			if fallbackTitle != "" {
				if _, insertErr := h.taskDB().ExecContext(c.Request.Context(),
					`INSERT INTO gt_task_notifications
					 (uuid, task_uuid, task_step_uuid, step_order, progress_uuid, session_uuid,
					  terminal_status, title, summary, is_read, is_archived, created_at)
					 VALUES (?, ?, '', 0, '', ?, 'running', ?, '任务已创建并开始执行', 1, 0, ?)`,
					uuid.New().String(), taskUUID, uuid.New().String(), fallbackTitle, time.Now().UnixMilli()); insertErr != nil {
					applog.Warn("[createTask] 兑底写入创建通知失败", "task_uuid", taskUUID, "error", insertErr.Error())
				}
			}
			h.notifyTaskChanged(taskUUID)
			c.JSON(http.StatusCreated, gin.H{"uuid": taskUUID, "warning": i18n.T(c, "localserver_task_created_warning")})
			return
		}
		taskTitle, _ := taskDetail["title"].(string)
		firstStepName := ""
		if len(preparedSnapshot.Steps) > 0 {
			firstStepName = preparedSnapshot.Steps[0].Name
		}
		if firstStepName == "" && executionMode == taskruntime.ExecutionModeExpertGroup {
			_ = h.taskDB().QueryRowContext(c.Request.Context(), `SELECT name FROM gt_task_steps WHERE task_uuid=? AND member_role='leader' LIMIT 1`, taskUUID).Scan(&firstStepName)
		}

		now := time.Now().UnixMilli()
		notifUUID := uuid.New().String()
		notifSessionUUID := uuid.New().String()
		notifTitle := taskTitle
		if firstStepName != "" {
			notifTitle = taskTitle + " · " + firstStepName
		}
		if executionMode != taskruntime.ExecutionModeVibeCoding {
			_, insertErr := h.taskDB().ExecContext(c.Request.Context(),
				`INSERT INTO gt_task_notifications
				 (uuid, task_uuid, task_step_uuid, step_order, progress_uuid, session_uuid,
				  terminal_status, title, summary, is_read, is_archived, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)`,
				notifUUID, taskUUID, stepUUID, stepOrder, progressUUID, notifSessionUUID,
				"running", notifTitle, "任务已创建并开始执行", now)
			if insertErr != nil {
				applog.Warn("[createTask] 写入创建通知失败", "task_uuid", taskUUID, "error", insertErr.Error())
			}
		}

		taskDetail["notification"] = buildTaskCreatedNotification(taskUUID, taskTitle, firstStepName, sessionUUID)
		h.notifyTaskChanged(taskUUID)
		c.JSON(http.StatusCreated, taskDetail)
		return
	}
	// 任务已落库，需要通知已打开的看板/对话列表重新拉取；缺这一步时新建任务
	// 只能靠重新挂载视图（切页/刷新）才出现，看起来就像「刚建的任务不见了」。
	h.notifyTaskChanged(taskUUID)
	c.JSON(http.StatusCreated, gin.H{"uuid": taskUUID})
}

func (h *TasksHandler) updateTask(c *gin.Context) {
	taskUUID := c.Param("uuid")
	// 与重命名和删除共用门闩，覆盖 task.md 读改写及数据库更新的完整过程。
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		defer lease.Close()
	}
	var body struct {
		Title             string   `json:"title"`
		Description       string   `json:"description"`
		Content           string   `json:"content"`
		PipelineUUID      string   `json:"pipeline_uuid"`
		ProjectUUID       string   `json:"project_uuid"`
		SubprojectUUIDs   []string `json:"subproject_uuids"`
		ChildProjectUUIDs []string `json:"child_project_uuids"`
		WorkDir           string   `json:"work_dir"`
		WorkDirs          []string `json:"work_dirs"`
		Priority          string   `json:"priority"`
		PlannedStartDate  string   `json:"planned_start_date"`
		PlannedEndDate    string   `json:"planned_end_date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	content := body.Content
	if strings.TrimSpace(content) == "" {
		content = body.Description
	}
	workDirs := body.WorkDirs
	if len(workDirs) == 0 && strings.TrimSpace(body.WorkDir) != "" {
		workDirs = []string{body.WorkDir}
	}
	subprojects := body.SubprojectUUIDs
	if len(subprojects) == 0 {
		subprojects = body.ChildProjectUUIDs
	}
	if strings.TrimSpace(body.PipelineUUID) != "" {
		if _, err := pipeline.NewService(h.taskDB()).Get(c.Request.Context(), strings.TrimSpace(body.PipelineUUID)); err != nil {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
	}
	err := taskruntime.NewService(h.taskDB(), h.taskRoot).Update(c.Request.Context(), taskUUID, taskruntime.UpdateInput{
		Title:                body.Title,
		Content:              content,
		Priority:             body.Priority,
		PlannedStartDate:     body.PlannedStartDate,
		PlannedEndDate:       body.PlannedEndDate,
		ProjectUUID:          body.ProjectUUID,
		SubprojectUUIDs:      subprojects,
		WorkDirs:             workDirs,
		SelectedPipelineUUID: strings.TrimSpace(body.PipelineUUID),
	})
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, taskruntime.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, taskruntime.ErrTaskContextLocked), errors.Is(err, taskruntime.ErrPipelineSnapshotLocked), errors.Is(err, taskruntime.ErrExecutionModeConflict):
			status = http.StatusConflict
		}
		i18n.LocalServerError(c, status, err)
		return
	}
	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	task, loadErr := h.loadTaskDetail(c.Request.Context(), taskUUID)
	if loadErr != nil {
		c.JSON(http.StatusOK, gin.H{"uuid": taskUUID})
		return
	}
	c.JSON(http.StatusOK, task)
}

func respondTaskCreateError(c *gin.Context, defaultStatus int, err error) {
	if errors.Is(err, taskruntime.ErrTaskTitleAndDescriptionRequired) {
		i18n.Error(c, http.StatusBadRequest, "localserver_task_title_or_description_required", "task_title_or_description_required")
		return
	}
	if errors.Is(err, taskruntime.ErrCloudUserIDRequired) {
		i18n.Error(c, http.StatusUnauthorized, "localserver_cloud_user_missing", "cloud_user_missing")
		return
	}
	if errors.Is(err, taskruntime.ErrCloudSourceKeyRequired) {
		i18n.Error(c, http.StatusBadRequest, "localserver_cloud_source_missing", "cloud_source_missing")
		return
	}
	i18n.LocalServerError(c, defaultStatus, err)
}

// buildTaskCreatedNotification builds the "task created and started" notification
// carried in the create-task response. It is returned in memory only and not persisted.
func buildTaskCreatedNotification(taskUUID, taskTitle, firstStepName, sessionUUID string) gin.H {
	return gin.H{
		"task_uuid":    taskUUID,
		"task_title":   taskTitle,
		"step_name":    firstStepName,
		"session_uuid": sessionUUID,
		"status":       "created",
		"summary":      "任务已创建并开始执行",
		"created_at":   time.Now().UnixMilli(),
	}
}

// updateTaskStatus updates task status
func (h *TasksHandler) updateTaskStatus(c *gin.Context) {
	taskUUID := c.Param("uuid")
	var body struct {
		Status       string                `json:"status"`
		PipelineUUID string                `json:"pipeline_uuid"`
		StepConfigs  []stepExecutionConfig `json:"step_configs"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	body.Status = normalizeTaskStatus(body.Status)
	var previousStatus, currentStepUUID, executionMode string
	if err := h.taskDB().QueryRow(`SELECT status, current_step_uuid, execution_mode FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&previousStatus, &currentStepUUID, &executionMode); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if executionMode == taskruntime.ExecutionModeVibeCoding {
		i18n.Error(c, http.StatusConflict, "localserver_vibe_status_readonly", "vibe_status_readonly")
		return
	}
	if body.Status == "active" && previousStatus == "pending" && currentStepUUID == "" {
		// CLI 直接执行等待用户在对话里发出第一条指令。
		if executionMode == taskruntime.ExecutionModeCLI {
			if _, err := h.taskDB().Exec(`UPDATE gt_tasks SET status='active', updated_at=? WHERE uuid=?`, time.Now().UnixMilli(), taskUUID); err != nil {
				i18n.LocalServerError(c, http.StatusInternalServerError, err)
				return
			}
			h.notifyTaskChanged(taskUUID)
			h.pushTaskSync(taskUUID)
			c.JSON(http.StatusOK, gin.H{"status": "in_progress"})
			return
		}
		if executionMode == taskruntime.ExecutionModeExpertGroup {
			sessionUUID, startErr := h.startTaskExecution(c.Request.Context(), taskUUID, "")
			if startErr != nil {
				i18n.LocalServerError(c, http.StatusConflict, startErr)
				return
			}
			h.notifyTaskChanged(taskUUID)
			h.pushTaskSync(taskUUID)
			c.JSON(http.StatusOK, gin.H{"status": "in_progress", "session_uuid": sessionUUID})
			return
		}
		if assignErr := h.ensureTaskPipelineAssigned(c.Request.Context(), taskUUID, pipelineAssignmentInput{PipelineUUID: body.PipelineUUID, StepConfigs: body.StepConfigs}); assignErr != nil {
			i18n.LocalServerError(c, http.StatusConflict, assignErr)
			return
		}
		if refreshErr := h.refreshPendingPipelineExecution(c.Request.Context(), taskUUID); refreshErr != nil {
			applog.Warn("[LocalServer] 启动前补齐流水线执行配置失败", "task_uuid", taskUUID, "error", refreshErr.Error())
		}
		sessionUUID, err := h.startTaskExecution(c.Request.Context(), taskUUID, "")
		if err != nil {
			i18n.LocalServerError(c, http.StatusConflict, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "in_progress", "session_uuid": sessionUUID})
		return
	}
	// Verify status value: first check the gt_task_lanes table, while being compatible with hard-coded status
	var laneCount int
	h.taskDB().QueryRow(`SELECT COUNT(*) FROM gt_task_lanes WHERE lane_key = ?`, body.Status).Scan(&laneCount)
	if laneCount == 0 && !workflow.ValidateTaskStatus(body.Status) {
		i18n.Error(c, http.StatusBadRequest, "localserver_invalid_task_status", "invalid_task_status")
		return
	}

	now := time.Now().UnixMilli()
	result, err := h.taskDB().Exec(`UPDATE gt_tasks SET status = ?, updated_at = ? WHERE uuid = ?`, body.Status, now, taskUUID)
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
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if body.Status == "done" {
		_ = markTaskNotificationsRead(c.Request.Context(), h.taskDB(), taskUUID, true)
	}
	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func normalizeTaskStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "", "pending", "todo":
		return "pending"
	case "active", "in_progress", "developing":
		return "active"
	case "done", "completed", "developed":
		return "done"
	case "blocked":
		return "blocked"
	default:
		return strings.TrimSpace(status)
	}
}

// startTask 是薄 wrapper：解析请求后委托 tasks_core.go 的 gin-free 核心，
// 并保持既有 HTTP 状态码语义（CLI=200、其他=202、NotFound=404、其他=409）。
func (h *TasksHandler) startTask(c *gin.Context) {
	var input pipelineAssignmentInput
	if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	result, err := h.startTaskCore(c.Request.Context(), c.Param("uuid"), c.GetHeader("X-Request-ID"), input)
	if err != nil {
		h.writeCoreError(c, err, http.StatusConflict)
		return
	}
	if result.AlreadyActive {
		// CLI 直接执行：只把任务推进到进行中，不预先生成执行会话。
		c.JSON(http.StatusOK, gin.H{"status": "active", "session_uuid": ""})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": result.SessionUUID})
}

// writeCoreError 把 gin-free 核心层返回的错误映射为 HTTP 响应。
// 显式标注过 key 的错误按 key 输出（i18n.Error），其余交给 i18n.LocalServerError 按错误文本映射，
// 这样既保住了核心层的状态码语义，也不重复实现一套错误文案表。
func (h *TasksHandler) writeCoreError(c *gin.Context, err error, fallback int) {
	status := CoreErrorStatus(err, fallback)
	if key, code := CoreErrorKey(err); key != "" {
		i18n.Error(c, status, key, code)
		return
	}
	i18n.LocalServerError(c, status, err)
}

// refreshPendingPipelineExecution copies CLI and model from the live pipeline
// onto an unstarted task snapshot. Configuring the pipeline after the task was
// created must show up in the task detail and be usable when the task starts.
// Steps that already have an execution config are left unchanged, and a task
// that has already started keeps its frozen snapshot.
func (h *TasksHandler) refreshPendingPipelineExecution(ctx context.Context, taskUUID string) error {
	if h.taskDB() == nil {
		return nil
	}
	var status, currentStep, selectedPipeline, snapshotUUID, executionMode string
	err := h.taskDB().QueryRowContext(ctx, `SELECT status, current_step_uuid, selected_pipeline_uuid, pipeline_snapshot_uuid, execution_mode
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&status, &currentStep, &selectedPipeline, &snapshotUUID, &executionMode)
	if err == sql.ErrNoRows || status == "done" || currentStep != "" || snapshotUUID == "" || strings.TrimSpace(selectedPipeline) == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if executionMode != "" && executionMode != taskruntime.ExecutionModePipeline {
		return nil
	}
	var missing int
	if err = h.taskDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_task_steps
		WHERE task_uuid=? AND (TRIM(COALESCE(cli_type,''))='' OR TRIM(COALESCE(model_name,''))='')`, taskUUID).Scan(&missing); err != nil {
		return err
	}
	if missing == 0 {
		return nil
	}
	pipe, err := pipeline.NewService(h.taskDB()).Get(ctx, selectedPipeline)
	if err != nil {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, step := range pipe.Steps {
		cliType := strings.TrimSpace(step.CLIType)
		modelName := strings.TrimSpace(step.ModelName)
		if cliType == "" && modelName == "" {
			continue
		}
		ids := make([]string, 0, 2)
		if id := strings.TrimSpace(step.UUID); id != "" {
			ids = append(ids, id)
		}
		if id := strings.TrimSpace(step.CloudStepID); id != "" && id != strings.TrimSpace(step.UUID) {
			ids = append(ids, id)
		}
		for _, id := range ids {
			if _, err = h.taskDB().ExecContext(ctx, `UPDATE gt_task_steps
				SET cli_type=CASE WHEN TRIM(COALESCE(cli_type,''))='' AND ?<>'' THEN ? ELSE cli_type END,
				    model_name=CASE WHEN TRIM(COALESCE(model_name,''))='' AND ?<>'' THEN ? ELSE model_name END,
				    updated_at=?
				WHERE task_uuid=? AND (TRIM(COALESCE(cli_type,''))='' OR TRIM(COALESCE(model_name,''))='')
				  AND (source_step_id=? OR cloud_step_id=? OR local_step_uuid=?)`,
				cliType, cliType, modelName, modelName, now, taskUUID, id, id, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *TasksHandler) ensureTaskPipelineAssigned(ctx context.Context, taskUUID string, input pipelineAssignmentInput) error {
	var snapshotUUID, selectedPipelineUUID, configJSON, executionMode string
	if err := h.taskDB().QueryRowContext(ctx, `SELECT pipeline_snapshot_uuid, selected_pipeline_uuid, selected_pipeline_config_json, execution_mode
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&snapshotUUID, &selectedPipelineUUID, &configJSON, &executionMode); err == sql.ErrNoRows {
		return taskruntime.ErrNotFound
	} else if err != nil {
		return err
	}
	if executionMode != "" && executionMode != taskruntime.ExecutionModePipeline {
		return taskruntime.ErrExecutionModeConflict
	}
	if snapshotUUID != "" {
		if executionMode == "" {
			_, _ = h.taskDB().ExecContext(ctx, `UPDATE gt_tasks SET execution_mode='pipeline', execution_tool='' WHERE uuid=? AND execution_mode=''`, taskUUID)
		}
		if len(input.StepConfigs) > 0 {
			for _, cfg := range input.StepConfigs {
				cliType := strings.TrimSpace(cfg.CLIType)
				model := strings.TrimSpace(cfg.Model)
				if model == "" {
					model = strings.TrimSpace(cfg.ModelName)
				}
				if cliType == "" && model == "" {
					continue
				}
				ids := []string{cfg.SourceStepID}
				if cfg.StepUUID != "" {
					ids = append(ids, cfg.StepUUID)
				}
				if cfg.CloudStepID != "" {
					ids = append(ids, cfg.CloudStepID)
				}
				for _, id := range ids {
					if id == "" {
						continue
					}
					h.taskDB().ExecContext(ctx,
						`UPDATE gt_task_steps SET cli_type=?, model_name=?, updated_at=? WHERE task_uuid=? AND (source_step_id=? OR cloud_step_id=?)`,
						cliType, model, time.Now().UnixMilli(), taskUUID, id, id)
				}
			}
		}
		return nil
	}
	if strings.TrimSpace(input.PipelineUUID) == "" {
		input.PipelineUUID = selectedPipelineUUID
	}
	if len(input.StepConfigs) == 0 && strings.TrimSpace(configJSON) != "" {
		if err := json.Unmarshal([]byte(configJSON), &input.StepConfigs); err != nil {
			return fmt.Errorf("读取预选流水线运行配置失败: %w", err)
		}
	}
	if strings.TrimSpace(input.PipelineUUID) == "" {
		return fmt.Errorf("任务启动前必须选择流水线")
	}
	configRaw, err := json.Marshal(input.StepConfigs)
	if err != nil {
		return err
	}
	snapshot, err := h.snapshotFromPipeline(ctx, input)
	if err != nil {
		return err
	}
	return taskruntime.NewService(h.taskDB(), h.taskRoot).AssignPipelineSelection(
		ctx, taskUUID, input.PipelineUUID, string(configRaw), snapshot,
	)
}

type executionModeInput struct {
	Mode                 string                `json:"mode"`
	Tool                 string                `json:"tool"`
	ModelName            string                `json:"model_name"`
	PipelineUUID         string                `json:"pipeline_uuid"`
	ExpertGroupUUID      string                `json:"expert_group_uuid"`
	StepConfigs          []stepExecutionConfig `json:"step_configs"`
	Start                bool                  `json:"start"`
	ReplaceExisting      bool                  `json:"replace_existing"`
	RebindExecutionIndex *int                  `json:"-"`
}

// assignExecutionMode 是薄 wrapper：解析入参后委托 tasks_core.go 的 gin-free 核心，
// 并保持既有状态码语义（400/404/409/501 不变）。
func (h *TasksHandler) assignExecutionMode(c *gin.Context) {
	var input executionModeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	ctx := withRequestID(c)
	result, err := h.assignExecutionModeCore(ctx, c.Param("uuid"), input)
	if err != nil {
		h.writeCoreError(c, err, http.StatusConflict)
		return
	}
	c.JSON(http.StatusOK, result.Response)
}

// withRequestID 把 X-Request-ID 注入 ctx，供 gin-free 核心层透传给执行编排
// （核心层不读 HTTP 请求头）。
func withRequestID(c *gin.Context) context.Context {
	return contextWithRequestID(c.Request.Context(), c.GetHeader("X-Request-ID"))
}

// switchExecutionMode 是薄 wrapper：入参已由调用方归一化并校验，
// 这里只负责委托核心层并把结果写回响应。
func (h *TasksHandler) switchExecutionMode(c *gin.Context, taskUUID string, input executionModeInput) {
	result, err := h.switchExecutionModeCore(withRequestID(c), taskUUID, input)
	if err != nil {
		h.writeCoreError(c, err, http.StatusConflict)
		return
	}
	c.JSON(http.StatusOK, result.Response)
}

func (h *TasksHandler) flushTaskExecution(ctx context.Context, taskUUID string) {
	client := h.taskCloudClient()
	db := h.taskDB()
	if client == nil || db == nil {
		return
	}
	flushCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := cloudsync.FlushTaskExecution(flushCtx, db, client, taskUUID); err != nil {
		applog.Warn("[execution-switch] 封存当前执行到云端失败，切换后仍会保留已成功上报的记录", "task_uuid", taskUUID, "error", err.Error())
	}
}

func (h *TasksHandler) startTaskExecutionForSwitch(ctx context.Context, taskUUID, requestID string, switchLease *workflow.TaskExecutionSwitch) (string, error) {
	stepUUID, err := taskruntime.NewService(h.taskDB(), h.taskRoot).Start(ctx, taskUUID)
	if err != nil {
		return "", err
	}
	return switchLease.RunStep(ctx, workflow.RunStepOptions{TaskUUID: taskUUID, StepUUID: stepUUID,
		RequestID: requestID, RecordType: "initial_run"})
}

func (h *TasksHandler) assignTaskPipeline(c *gin.Context) {
	var input pipelineAssignmentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.PipelineUUID) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_uuid_required", "pipeline_required")
		return
	}
	if err := h.ensureTaskPipelineAssigned(c.Request.Context(), c.Param("uuid"), input); err != nil {
		status := http.StatusConflict
		if errors.Is(err, taskruntime.ErrNotFound) {
			status = http.StatusNotFound
		}
		i18n.LocalServerError(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"assigned": true})
}

// assignAndStartTask freezes the selected pipeline into the task and starts its
// first step in one API operation. The snapshot remains permanent even if the
// source pipeline is later edited or deleted.
func (h *TasksHandler) assignAndStartTask(c *gin.Context) {
	var input pipelineAssignmentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.PipelineUUID) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_uuid_required", "pipeline_required")
		return
	}
	if err := h.ensureTaskPipelineAssigned(c.Request.Context(), c.Param("uuid"), input); err != nil {
		status := http.StatusConflict
		if errors.Is(err, taskruntime.ErrNotFound) {
			status = http.StatusNotFound
		}
		i18n.LocalServerError(c, status, err)
		return
	}
	sessionUUID, err := h.startTaskExecution(c.Request.Context(), c.Param("uuid"), c.GetHeader("X-Request-ID"))
	if err != nil {
		i18n.LocalServerError(c, http.StatusConflict, err)
		return
	}
	h.pushTaskSync(c.Param("uuid"))
	c.JSON(http.StatusAccepted, gin.H{"assigned": true, "session_uuid": sessionUUID})
}

func (h *TasksHandler) startTaskExecution(ctx context.Context, taskUUID, requestID string) (string, error) {
	orchestrator := h.orchestrator()
	if orchestrator == nil {
		return "", fmt.Errorf("任务执行服务尚未就绪")
	}
	executionLease, err := orchestrator.BeginTaskExecution(taskUUID)
	if err != nil {
		return "", err
	}
	defer executionLease.Close()
	stepUUID, err := taskruntime.NewService(h.taskDB(), h.taskRoot).Start(ctx, taskUUID)
	if err != nil {
		return "", err
	}
	return executionLease.RunStep(ctx, workflow.RunStepOptions{TaskUUID: taskUUID, StepUUID: stepUUID,
		RequestID: requestID, RecordType: "initial_run"})
}

func (h *TasksHandler) askStepQuestion(c *gin.Context) {
	var body struct {
		Question        string `json:"question"`
		DisplayQuestion string `json:"display_question"`
		RequestID       string `json:"request_id"`
		CLIType         string `json:"cli_type"`
		Model           string `json:"model"`
		ModelName       string `json:"model_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_question_required", "question_required")
		return
	}
	displayQuestion := body.DisplayQuestion
	if strings.TrimSpace(displayQuestion) == "" {
		displayQuestion = body.Question
	}
	prompt, err := h.resolveTaskAttachmentReferences(c.Request.Context(), c.Param("uuid"), body.Question)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	var parentSessionUUID string
	err = h.taskDB().QueryRowContext(c.Request.Context(), `SELECT uuid FROM gt_cli_sessions
		WHERE task_uuid = ? AND step_uuid = ? AND status IN ('success','failed','stopped','interrupted')
		ORDER BY run_no DESC LIMIT 1`, c.Param("uuid"), c.Param("step_uuid")).Scan(&parentSessionUUID)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusConflict, "localserver_current_session_unavailable", "session_unavailable")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	modelName := strings.TrimSpace(body.ModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(body.Model)
	}
	sessionUUID, err := h.orchestrator().ContinueConversation(c.Request.Context(), workflow.ContinueConversationOptions{
		ParentSessionUUID: parentSessionUUID,
		Prompt:            prompt,
		DisplayPrompt:     displayQuestion,
		RequestID:         body.RequestID,
		CLIType:           body.CLIType,
		Model:             modelName,
	})
	if err != nil {
		h.writeWorkflowError(c, err, http.StatusConflict)
		return
	}
	_ = markTaskNotificationsRead(c.Request.Context(), h.taskDB(), c.Param("uuid"), true)
	h.pushTaskSync(c.Param("uuid"))
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": sessionUUID})
}

// completeStep 是薄 wrapper：委托 gin-free 核心完整步收尾（含 markTaskNotificationsRead /
// pushTaskSync / notifyTaskChanged 三动作），并保持既有响应形状与状态码。
func (h *TasksHandler) completeStep(c *gin.Context) {
	var body struct {
		AutoStart *bool `json:"auto_start"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	autoStart := body.AutoStart != nil && *body.AutoStart
	taskUUID := c.Param("uuid")
	result, sessionUUID, err := h.completeStepCore(c.Request.Context(), taskUUID, c.Param("step_uuid"), autoStart)
	if err != nil {
		if startErr := CoreAutoStartError(err); startErr != nil {
			// 步骤已推进成功，只是下一步自动启动失败：与旧实现一致，仍返回 200 + start_error。
			c.JSON(http.StatusOK, gin.H{
				"task_done": result.TaskDone, "next_step_uuid": result.NextStepUUID,
				"auto_started": false, "start_error": startErr.Error(),
			})
			return
		}
		h.writeCoreError(c, err, http.StatusConflict)
		return
	}
	response := gin.H{"task_done": result.TaskDone, "next_step_uuid": result.NextStepUUID, "auto_started": false}
	if sessionUUID != "" {
		response["session_uuid"] = sessionUUID
		response["auto_started"] = true
	}
	c.JSON(http.StatusOK, response)
}

func (h *TasksHandler) listTaskProgress(c *gin.Context) {
	// 只有云端来源的任务参与上报，本地任务不返回上报状态，避免界面误显示「待上报」。
	var sourceType string
	if err := h.taskDB().QueryRowContext(c.Request.Context(),
		`SELECT COALESCE(source_type,'') FROM gt_tasks WHERE uuid=?`, c.Param("uuid")).Scan(&sourceType); err != nil && err != sql.ErrNoRows {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	reportsToCloud := sourceType == "cloud"
	rows, err := h.taskDB().QueryContext(c.Request.Context(), `SELECT p.uuid, p.task_uuid, p.task_step_uuid, p.session_uuid,
		p.record_type, p.user_prompt, p.cli_type, p.model_name, p.status, p.final_result,
		p.execution_mode, p.external_session_id, p.dispatch_status, p.dispatch_message, p.created_at, p.started_at, p.finished_at,
		COALESCE(s.latest_event_type, ''), COALESCE(s.latest_event_content, ''), COALESCE(s.latest_event_at, 0),
		COALESCE(s.input_tokens, 0), COALESCE(s.output_tokens, 0), COALESCE(s.total_tokens, 0), COALESCE(s.duration_ms, 0),
		COALESCE(s.cloud_sync_status, ''), COALESCE(s.cloud_sync_error, ''), COALESCE(s.cloud_sync_at, 0),
		COALESCE(s.cloud_synced_at, 0), COALESCE(s.updated_at, 0),
		COALESCE(p.cloud_sync_status, ''), COALESCE(p.cloud_sync_error, ''), COALESCE(p.cloud_synced_at, 0),
		COALESCE(p.activity_kind, '')
		FROM gt_task_progress p LEFT JOIN gt_cli_sessions s ON s.uuid = p.session_uuid
		WHERE p.task_uuid = ? ORDER BY p.created_at, p.rowid`, c.Param("uuid"))
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, taskID, stepID, sessionID, recordType, prompt, cliType, model, status, result string
		var executionMode, externalSessionID, dispatchStatus, dispatchMessage string
		var createdAt, startedAt, finishedAt int64
		var latestEventType, latestEventContent string
		var latestEventAt int64
		var inputTokens, outputTokens, totalTokens, durationMs int64
		var syncStatus, syncError string
		var syncAt, syncWatermark, sessionUpdatedAt int64
		var progressSyncStatus, progressSyncError, activityKind string
		var progressSyncedAt int64
		if err := rows.Scan(&id, &taskID, &stepID, &sessionID, &recordType, &prompt, &cliType, &model, &status, &result,
			&executionMode, &externalSessionID, &dispatchStatus, &dispatchMessage, &createdAt, &startedAt, &finishedAt, &latestEventType, &latestEventContent, &latestEventAt,
			&inputTokens, &outputTokens, &totalTokens, &durationMs,
			&syncStatus, &syncError, &syncAt, &syncWatermark, &sessionUpdatedAt,
			&progressSyncStatus, &progressSyncError, &progressSyncedAt, &activityKind); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		// 上报状态分两种载体：
		//   - 有受管 CLI 会话：状态挂在会话上，成功要求水位追平会话 updated_at；
		//   - Vibe 手动模式没有受管会话：状态直接记在沟通记录行上，一条一个状态。
		syncState, itemSyncError, itemSyncedAt := "", "", int64(0)
		switch {
		case !reportsToCloud:
			// 本地任务不参与上报，不返回状态，界面不展示任何上报提示
		case sessionID != "":
			syncState = cloudsync.SyncStatusPending
			if syncStatus == cloudsync.SyncStatusFailed {
				syncState = cloudsync.SyncStatusFailed
			} else if syncStatus == cloudsync.SyncStatusSuccess && syncWatermark >= sessionUpdatedAt {
				syncState = cloudsync.SyncStatusSuccess
			}
			itemSyncError, itemSyncedAt = syncError, syncAt
		case vibeRecordReportsToCloud(executionMode, externalSessionID, activityKind):
			syncState = progressSyncStatus
			if syncState != cloudsync.SyncStatusSuccess && syncState != cloudsync.SyncStatusFailed {
				syncState = cloudsync.SyncStatusPending
			}
			itemSyncError, itemSyncedAt = progressSyncError, progressSyncedAt
		}
		items = append(items, gin.H{"uuid": id, "task_uuid": taskID, "task_step_uuid": stepID,
			"session_uuid": sessionID, "record_type": recordType, "user_prompt": prompt, "question": prompt,
			"cli_type": cliType, "model": model, "model_name": model, "status": status,
			"execution_mode": executionMode, "external_session_id": externalSessionID,
			"dispatch_status": dispatchStatus, "dispatch_message": dispatchMessage,
			"final_result": result, "result": result, "created_at": createdAt, "started_at": startedAt, "finished_at": finishedAt,
			"latest_event_type": latestEventType, "latest_event_content": latestEventContent, "latest_event_at": latestEventAt,
			"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": totalTokens, "duration_ms": durationMs,
			"cloud_sync_status": syncState, "cloud_sync_error": itemSyncError, "cloud_synced_at": itemSyncedAt})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// vibeRecordReportsToCloud 判断一条 Vibe 沟通记录是否参与上报。
// 取数条件必须与 cloudsync.buildVibeSession 一致：没有 thread_id 或不是问答记录的
// 行根本不会进快照，给它们挂「待上报」只会误导用户。
func vibeRecordReportsToCloud(executionMode, externalSessionID, activityKind string) bool {
	return executionMode == taskruntime.ExecutionModeVibeCoding && externalSessionID != "" &&
		(activityKind == activityKindUserMessage || activityKind == activityKindFinalResponse)
}

func (h *TasksHandler) listTaskExecutionHistory(c *gin.Context) {
	items, err := taskruntime.NewService(h.taskDB(), h.taskRoot).ListExecutionHistory(
		c.Request.Context(), strings.TrimSpace(c.Param("uuid")),
	)
	if errors.Is(err, taskruntime.ErrNotFound) {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *TasksHandler) getTaskExecutionHistory(c *gin.Context) {
	detail, err := taskruntime.NewService(h.taskDB(), h.taskRoot).GetExecutionHistory(
		c.Request.Context(), strings.TrimSpace(c.Param("uuid")), strings.TrimSpace(c.Param("history_uuid")),
	)
	if errors.Is(err, taskruntime.ErrExecutionHistoryNotFound) {
		i18n.Error(c, http.StatusNotFound, "localserver_execution_history_not_found", "execution_history_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *TasksHandler) sendExpertMessage(c *gin.Context) {
	var body struct {
		Content        string `json:"content"`
		DisplayContent string `json:"display_content"`
		RequestID      string `json:"request_id"`
		MemberUUID     string `json:"member_uuid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_question_required", "question_required")
		return
	}
	var mode string
	if err := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT execution_mode FROM gt_tasks WHERE uuid=?`, c.Param("uuid")).Scan(&mode); err != nil {
		i18n.LocalServerError(c, http.StatusNotFound, err)
		return
	}
	if mode != taskruntime.ExecutionModeExpertGroup {
		i18n.LocalServerError(c, http.StatusConflict, fmt.Errorf("任务未指派给专家团"))
		return
	}
	displayContent := strings.TrimSpace(body.DisplayContent)
	if displayContent == "" {
		displayContent = body.Content
	}
	prompt, err := h.resolveTaskAttachmentReferences(c.Request.Context(), c.Param("uuid"), body.Content)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	sessionUUID, err := h.orchestrator().StartExpertMessage(c.Request.Context(), c.Param("uuid"), body.MemberUUID,
		prompt, displayContent, body.RequestID)
	if err != nil {
		if errors.Is(err, taskruntime.ErrExpertRoutingPending) {
			i18n.Error(c, http.StatusConflict, "localserver_expert_routing_pending", "expert_routing_pending")
			return
		}
		h.writeWorkflowError(c, err, http.StatusConflict)
		return
	}
	_ = markTaskNotificationsRead(c.Request.Context(), h.taskDB(), c.Param("uuid"), true)
	h.notifyTaskChanged(c.Param("uuid"))
	h.pushTaskSync(c.Param("uuid"))
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": sessionUUID})
}

// deleteTask delete task (cascade deletion of sub-table data)
func (h *TasksHandler) deleteTask(c *gin.Context) {
	taskUUID := c.Param("uuid")
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		defer lease.Close()
	}
	if !guardRemoteExecutionMutation(c, h.taskDB(), taskUUID) {
		return
	}

	//Refuse to delete tasks with active Sessions to prevent the CLI process from becoming an orphan that cannot be terminated.
	// If the query fails, an error must be reported directly: swallowing the error will make activeCount always 0, and the protection will be in vain.
	// Instead, cascading deletions of running session records are performed - exactly what this check is intended to prevent.
	var activeCount int
	var taskDir string
	if err := h.taskDB().QueryRow(
		`SELECT task_dir, (SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid = gt_tasks.uuid AND status IN ('created', 'running', 'waiting_input', 'stop_requested')) FROM gt_tasks WHERE uuid = ?`,
		taskUUID).Scan(&taskDir, &activeCount); err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	} else if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if activeCount > 0 {
		i18n.Error(c, http.StatusConflict, "localserver_active_sessions", "task_has_active_sessions")
		return
	}

	tx, err := h.taskDB().Begin()
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	sessionUUIDs, err := deleteTaskRows(c.Request.Context(), tx, taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	result, err := tx.Exec(`DELETE FROM gt_tasks WHERE uuid = ?`, taskUUID)
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
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}

	if err := tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	taskruntime.RemoveSessionArtifacts(sessionUUIDs)
	// Database deletion is final even when file cleanup fails.
	cleanupFailed := !h.cleanupTaskDirectory(taskDir)
	h.deleteTaskSync(taskUUID)
	if h.wsHub != nil {
		h.wsHub.BroadcastToAll("task.changed", gin.H{"task_uuid": taskUUID, "deleted": true})
	}
	response := gin.H{"uuid": taskUUID, "deleted": true}
	if cleanupFailed {
		applog.Warn("任务已删除，专属目录未能清理", "task_uuid", taskUUID)
		c.Header("Content-Language", i18n.FromContext(c))
		response["warning"] = i18n.T(c, "localserver_task_cleanup_failed")
	}
	c.JSON(http.StatusOK, response)
}

// runStep execution step
func (h *TasksHandler) runStep(c *gin.Context) {
	taskUUID := c.Param("uuid")
	stepUUID := c.Param("step_uuid")

	var body struct {
		Question        string `json:"question"`
		DisplayQuestion string `json:"display_question"`
		RequestID       string `json:"request_id"`
		CLIType         string `json:"cli_type"`
		Model           string `json:"model"`
		ModelName       string `json:"model_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	var storedWorkDir string
	err := h.taskDB().QueryRow(`SELECT work_dir FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&storedWorkDir)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	workDir, err := normalizeExistingDirectory(storedWorkDir)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	prompt := strings.TrimSpace(body.Question)
	displayQuestion := strings.TrimSpace(body.DisplayQuestion)
	recordType := "initial_run"
	if prompt != "" {
		resolvedPrompt, resolveErr := h.resolveTaskAttachmentReferences(c.Request.Context(), taskUUID, body.Question)
		if resolveErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": resolveErr.Error()})
			return
		}
		prompt = resolvedPrompt
		if displayQuestion == "" {
			displayQuestion = body.Question
		}
		recordType = "user_question"
	}
	modelName := strings.TrimSpace(body.ModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(body.Model)
	}

	sessionUUID, err := h.orchestrator().RunStep(c.Request.Context(), workflow.RunStepOptions{
		TaskUUID:      taskUUID,
		StepUUID:      stepUUID,
		WorkDir:       workDir,
		RequestID:     body.RequestID,
		CLIType:       body.CLIType,
		Model:         modelName,
		UserPrompt:    prompt,
		DisplayPrompt: displayQuestion,
		RecordType:    recordType,
	})
	if err != nil {
		h.writeWorkflowError(c, err, http.StatusInternalServerError)
		return
	}

	h.pushTaskSync(taskUUID)
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": sessionUUID})
}

// updateStepPrompt saves the prompt snapshot. Editing the current executing
// step still invalidates that step's progress and restarts a fresh CLI round.
// Editing any other step only writes prompt_snapshot.
func (h *TasksHandler) updateStepPrompt(c *gin.Context) {
	taskUUID := c.Param("uuid")
	stepUUID := c.Param("step_uuid")

	var body struct {
		PromptSnapshot string `json:"prompt_snapshot"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.PromptSnapshot) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_prompt_required", "prompt_required")
		return
	}
	reqCtx := context.WithoutCancel(c.Request.Context())
	orchestrator := h.orchestrator()
	if orchestrator == nil {
		i18n.Error(c, http.StatusServiceUnavailable, "localserver_execution_switch_unavailable", "execution_unavailable")
		return
	}
	executionLease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusConflict, err)
		return
	}
	defer executionLease.Close()

	// 1. Verify task and step exist, and capture the state before resetting it.
	var stepStatus, stepExecutionStatus, currentStepUUID string
	var taskExists bool
	err = h.taskDB().QueryRow(`SELECT COUNT(*)>0 FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskExists)
	if err != nil || !taskExists {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	err = h.taskDB().QueryRow(
		`SELECT s.status, s.execution_status
		 FROM gt_task_steps s WHERE s.uuid=? AND s.task_uuid=?`,
		stepUUID, taskUUID,
	).Scan(&stepStatus, &stepExecutionStatus)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_step_not_found", "step_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if err = h.taskDB().QueryRow(`SELECT COALESCE(current_step_uuid, '') FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&currentStepUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	now := time.Now().UnixMilli()

	// 客户端断开（curl 超时、页面关闭）会取消请求 context，导致进程终止、
	// 状态重置等关键操作中途失败，留下「步骤已回退但无会话执行」的中间态；
	// 因此本接口内部一律使用不随客户端断开取消的 context。
	applog.Info("[updateStepPrompt] 开始修改步骤提示词", "task_uuid", taskUUID, "step_uuid", stepUUID)

	if stepUUID != currentStepUUID {
		if _, err = h.taskDB().Exec(`UPDATE gt_task_steps SET prompt_snapshot=?, updated_at=? WHERE uuid=? AND task_uuid=?`,
			body.PromptSnapshot, now, stepUUID, taskUUID); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		h.pushTaskSync(taskUUID)
		h.notifyTaskChanged(taskUUID)
		c.JSON(http.StatusOK, gin.H{"prompt_updated": true, "prompt_only": true})
		return
	}

	// 提示词重启不能中断另一步骤的越序对话。独占门闩避免检查后插入新执行。
	var otherActive int
	if err := h.taskDB().QueryRowContext(reqCtx, `SELECT COUNT(*) FROM gt_cli_sessions
		WHERE task_uuid=? AND step_uuid<>? AND status IN ('created','running','waiting_input','stop_requested')`, taskUUID, stepUUID).Scan(&otherActive); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if otherActive > 0 {
		h.writeWorkflowError(c, workflow.ErrTaskBusy, http.StatusConflict)
		return
	}

	// active/completed are the business states for a step that has entered the
	// execution chain. execution_status also covers historical failed/stopped
	// attempts that may have already been rolled back to pending.
	shouldAutoStart := stepStatus == "active" || stepStatus == "completed" || stepExecutionStatus != "idle"

	// 2. Stop the active CLI before deleting the current step's progress.
	// 停止失败时不能继续：残留的 stop_requested 会话会阻塞后续会话启动，
	// 若继续修改步骤状态会出现「步骤已回退但无会话执行」的卡死状态，故直接返回让用户重试。
	stopStart := time.Now()
	if stopErr := executionLease.StopTaskSessions(reqCtx); stopErr != nil {
		applog.Warn("[updateStepPrompt] 终止任务会话失败", "task_uuid", taskUUID, "error", stopErr, "cost_ms", time.Since(stopStart).Milliseconds())
		i18n.LocalServerError(c, http.StatusConflict, stopErr)
		return
	}
	applog.Info("[updateStepPrompt] 终止任务会话完成", "task_uuid", taskUUID, "cost_ms", time.Since(stopStart).Milliseconds())
	// Wait briefly for sessions to fully terminate
	time.Sleep(500 * time.Millisecond)

	tx, err := h.taskDB().Begin()
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	// 越序对话彼此独立：只重置当前步骤，保留其他步骤已有的讨论和执行结果。
	if _, err = tx.Exec(`DELETE FROM gt_task_progress WHERE task_uuid=? AND task_step_uuid=?`,
		taskUUID, stepUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	// 只重置被修改步骤的可见状态，旧会话仍保留用于审计。
	if _, err = tx.Exec(`UPDATE gt_task_steps SET
		status='pending', execution_status='idle',
		output_summary='', error_summary='',
		input_tokens=0, output_tokens=0, total_tokens=0, conversation_rounds=0,
		started_at=0, completed_at=0, finished_at=0, duration_ms=0,
		updated_at=?
		WHERE task_uuid=? AND uuid=?`, now, taskUUID, stepUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if _, err = tx.Exec(`UPDATE gt_task_steps SET prompt_snapshot=?, updated_at=? WHERE uuid=?`,
		body.PromptSnapshot, now, stepUUID); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	// 5. A previously active/completed/executed step is immediately made current
	// and started after the transaction. A future idle step remains pending.
	if shouldAutoStart {
		if _, err = tx.Exec(`UPDATE gt_task_steps SET status='active', updated_at=? WHERE uuid=?`,
			now, stepUUID); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		if _, err = tx.Exec(`UPDATE gt_tasks SET current_step_uuid=?, current_step_completed=0,
			execution_status='idle', status='active', finished_at=0, updated_at=? WHERE uuid=?`,
			stepUUID, now, taskUUID); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
	}

	if err = tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	if !shouldAutoStart {
		c.JSON(http.StatusOK, gin.H{"prompt_updated": true, "execution_deferred": true})
		return
	}

	// 6. Start a fresh round so the new prompt is used as the complete initial
	// prompt. Do not resume the old native conversation.
	sessionUUID, runErr := executionLease.RunStep(reqCtx, workflow.RunStepOptions{
		TaskUUID: taskUUID,
		StepUUID: stepUUID,
	})
	if runErr != nil {
		key, _ := i18n.Key(runErr)
		if errors.Is(runErr, workflow.ErrStepRuntimeRequired) {
			key = "localserver_pipeline_step_execution_required"
		}
		c.Header("Content-Language", i18n.FromContext(c))
		c.JSON(http.StatusOK, gin.H{
			"prompt_updated": true,
			"session_error":  i18n.T(c, key),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"prompt_updated": true,
		"session_uuid":   sessionUUID,
	})
}

// taskFileEntry represents a file or directory entry in the task file tree.
type taskFileEntry struct {
	Name          string          `json:"name"`
	Path          string          `json:"path"`
	AbsolutePath  string          `json:"absolute_path"`
	IsDir         bool            `json:"is_dir"`
	FileType      string          `json:"file_type,omitempty"`
	Size          int64           `json:"size,omitempty"`
	ModifiedAt    int64           `json:"modified_at,omitempty"`
	OwnerStepUUID string          `json:"owner_step_uuid,omitempty"`
	OwnerStepName string          `json:"owner_step_name,omitempty"`
	Children      []taskFileEntry `json:"children,omitempty"`
	// Upload 是该文件的产出文档上报状态；上报开关关闭时为 null
	Upload *cloudsync.DocumentUploadState `json:"upload"`
	// UploadSummary 是目录节点下所有文件的上报状态聚合，供折叠状态展示
	UploadSummary *cloudsync.DocumentUploadSummary `json:"upload_summary,omitempty"`
}

type taskFileOwner struct {
	StepUUID string
	StepName string
	StepDir  string
}

// listTaskFiles returns the tree structure of files and folders under the task directory.
func (h *TasksHandler) listTaskFiles(c *gin.Context) {
	taskUUID := c.Param("uuid")

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
	if taskDir == "" {
		i18n.Error(c, http.StatusNotFound, "localserver_file_path_empty", "task_directory_empty")
		return
	}

	root, err := filepath.Abs(taskDir)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	owners, err := h.loadTaskFileOwners(c.Request.Context(), taskUUID, root)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	// 上报状态是只读旁路信息：读取失败不影响文件树本身（退化为一棵树没有状态）
	uploads, uploadErr := h.loadDocumentUploadStates(c.Request.Context(), taskUUID)
	if uploadErr != nil {
		applog.Warn("[DocSync] 读取产出文档上报状态失败", "task_uuid", taskUUID, "error", uploadErr.Error())
	}
	// 只有 cloud 来源任务在云端有 pipeline_tasks 行、才会真正上报；本地任务不参与，
	// 这里按任务返回开关，避免本地任务抽屉显示「已上报 0 / 共 N」的假状态。
	uploadEnabled := h.taskDocumentUploadEnabled(sourceType)
	if !uploadEnabled {
		uploads = nil
	}
	// 任务级「同步 Agent 产出」开关：关闭时抽屉显示「任务默认不上报文件」并提供手动上报入口。
	// 读取失败按开启处理，避免仅因一次查询失败把提示显示成关闭。
	autoUploadEnabled := uploadEnabled
	if uploadEnabled {
		var syncAgentOutput bool
		if flagErr := h.taskDB().QueryRow(`SELECT COALESCE(sync_agent_output,1) FROM gt_tasks WHERE uuid=?`, taskUUID).
			Scan(&syncAgentOutput); flagErr != nil {
			applog.Warn("[DocSync] 读取任务产出同步开关失败", "task_uuid", taskUUID, "error", flagErr.Error())
		} else {
			autoUploadEnabled = syncAgentOutput
		}
	}
	tree, err := buildFileTree(root, root, "", owners, uploads)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_dir":            root,
		"tree":                tree,
		"upload_enabled":      uploadEnabled,
		"auto_upload_enabled": autoUploadEnabled,
	})
}

func (h *TasksHandler) loadTaskFileOwners(ctx context.Context, taskUUID, root string) ([]taskFileOwner, error) {
	rows, err := h.taskDB().QueryContext(ctx, `SELECT uuid, name, step_dir FROM gt_task_steps WHERE task_uuid = ?`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := make([]taskFileOwner, 0)
	for rows.Next() {
		var owner taskFileOwner
		if err := rows.Scan(&owner.StepUUID, &owner.StepName, &owner.StepDir); err != nil {
			return nil, err
		}
		if strings.TrimSpace(owner.StepDir) == "" {
			continue
		}
		owner.StepDir, err = filepath.Abs(strings.TrimSpace(owner.StepDir))
		if err != nil || !pathWithinDirectory(root, owner.StepDir) {
			continue
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

// buildFileTree recursively builds a file tree entry. uploads 以规范化相对路径为键，
// 为 nil 时不填充上报状态（上报开关关闭）。
func buildFileTree(root, currentDir, relativePath string, owners []taskFileOwner, uploads map[string]cloudsync.DocumentUploadState) ([]taskFileEntry, error) {
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return nil, err
	}

	result := make([]taskFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == legacyTaskAttachmentDirectory {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		relPath := filepath.Join(relativePath, entry.Name())
		absolutePath := filepath.Join(currentDir, entry.Name())
		if !pathWithinDirectory(root, absolutePath) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		node := taskFileEntry{
			Name:         entry.Name(),
			Path:         relPath,
			AbsolutePath: absolutePath,
			IsDir:        entry.IsDir(),
		}
		for _, owner := range owners {
			if pathWithinDirectory(owner.StepDir, absolutePath) {
				node.OwnerStepUUID = owner.StepUUID
				node.OwnerStepName = owner.StepName
				break
			}
		}
		if !entry.IsDir() {
			ext := strings.TrimPrefix(filepath.Ext(entry.Name()), ".")
			node.FileType = ext
			node.Size = info.Size()
			node.ModifiedAt = info.ModTime().UnixMilli()
			if uploads != nil {
				if state, ok := uploads[cloudsync.DocumentRelKey(relPath)]; ok {
					node.Upload = &state
				}
			}
		} else {
			children, err := buildFileTree(root, absolutePath, relPath, owners, uploads)
			if err == nil && len(children) > 0 {
				node.Children = children
			} else {
				// Always include empty directories with empty children array
				node.Children = []taskFileEntry{}
			}
			if uploads != nil {
				node.UploadSummary = summarizeUploads(node.Children)
			}
		}
		result = append(result, node)
	}
	return result, nil
}

// summarizeUploads 递归汇总目录下所有文件的上报状态。
func summarizeUploads(children []taskFileEntry) *cloudsync.DocumentUploadSummary {
	if len(children) == 0 {
		return nil
	}
	summary := &cloudsync.DocumentUploadSummary{}
	for _, child := range children {
		if child.IsDir {
			if nested := child.UploadSummary; nested != nil {
				summary.Total += nested.Total
				summary.Synced += nested.Synced
				summary.Failed += nested.Failed
				summary.Pending += nested.Pending
				summary.Skipped += nested.Skipped
			}
			continue
		}
		// 不参与上报的文件（白名单外/已删除）不计入 Total，
		// 否则目录上的「已上报/总数」永远追不平
		status := ""
		if child.Upload != nil {
			status = child.Upload.Status
		}
		switch status {
		case "skipped":
			summary.Skipped++
			continue
		case "deleted":
			continue
		}
		summary.Total++
		switch status {
		case "synced":
			summary.Synced++
		case "failed":
			summary.Failed++
		default:
			// pending / uploading 归入待处理，前端显示为等待上报
			summary.Pending++
		}
	}
	if summary.Total == 0 {
		return nil
	}
	return summary
}

// getTaskFileContent reads a file's content relative to the task directory.
func (h *TasksHandler) getTaskFileContent(c *gin.Context) {
	taskUUID := c.Param("uuid")
	filePath := c.Query("path")
	if strings.TrimSpace(filePath) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_path_required", "path_required")
		return
	}

	var taskDir string
	err := h.taskDB().QueryRow(`SELECT task_dir FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskDir)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	absPath, err := h.safeTaskFilePath(taskDir, filePath)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	// Only allow text and image file types
	ext := strings.TrimPrefix(filepath.Ext(absPath), ".")
	if !isReadableFileType(ext) {
		i18n.Errorf(c, http.StatusBadRequest, "localserver_readable_file_type", i18n.Params{"FileType": ext})
		return
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			i18n.Error(c, http.StatusNotFound, "localserver_file_not_found", "file_not_found")
			return
		}
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	response := gin.H{
		"path":      filePath,
		"file_type": ext,
		"size":      len(content),
	}
	if isImageFileType(ext) {
		response["encoding"] = "base64"
		response["content"] = base64.StdEncoding.EncodeToString(content)
	} else {
		response["content"] = string(content)
	}
	c.JSON(http.StatusOK, response)
}

// saveTaskFileContent writes content to a file relative to the task directory.
func (h *TasksHandler) saveTaskFileContent(c *gin.Context) {
	taskUUID := c.Param("uuid")
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		defer lease.Close()
	}

	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_path_required", "path_required")
		return
	}

	var taskDir string
	err := h.taskDB().QueryRow(`SELECT task_dir FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskDir)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	absPath, err := h.safeTaskFilePath(taskDir, body.Path)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if err := os.WriteFile(absPath, []byte(body.Content), 0o644); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	// 保存即同步：抽屉里手动修改后立刻把该文件重新排入产出文档上报队列
	h.markDocumentPending(taskUUID, body.Path)
	h.documentKick(taskUUID)

	c.JSON(http.StatusOK, gin.H{"path": body.Path, "saved": true})
}

// spillReferenceFile 把超长引用内容唯一命名落盘到任务产出目录（S-IN-08 / S-DA-12 / S-UI-23）。
// POST /api/local/tasks/:uuid/files/reference  body {file_name, content}
//
// 落盘位置为任务根目录（与 GET /tasks/:uuid/files 的根一致），文件名由源文件名去扩展名后
// 消毒并追加「-引用」后缀；同名时依次尝试 -2 / -3 … 最多 999 次，任何情况下都不覆盖既有产出文件。
func (h *TasksHandler) spillReferenceFile(c *gin.Context) {
	taskUUID := c.Param("uuid")

	var body struct {
		FileName string `json:"file_name"`
		Content  string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.FileName) == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_reference_name_required", "localserver_reference_name_required")
		return
	}
	if body.Content == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_reference_content_required", "localserver_reference_content_required")
		return
	}

	var taskDir string
	err := h.taskDB().QueryRow(`SELECT task_dir FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskDir)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if strings.TrimSpace(taskDir) == "" {
		i18n.Error(c, http.StatusNotFound, "localserver_file_path_empty", "task_directory_empty")
		return
	}

	// 复用 safeTaskFilePath 的安全边界语义校验任务根目录（符号链接 / 越界）
	root, err := h.safeTaskFilePath(taskDir, ".")
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	// S-DA-12 / Minor-3: 唯一命名 + O_CREATE|O_EXCL 原子创建，绝不覆盖既有产出文件
	relativePath, err := createReferenceFile(root, body.FileName, body.Content)
	if err != nil {
		if errors.Is(err, errReferenceNameInvalid) {
			i18n.LocalServerError(c, http.StatusBadRequest, err)
			return
		}
		// 边界 E13 / S-UI-23：命名耗尽或写盘失败必须返回明确错误，前端据此提示，不静默丢失引用内容
		applog.Warn("引用内容落盘失败", "task_uuid", taskUUID, "file_name", body.FileName, "error", err)
		i18n.Error(c, http.StatusInternalServerError, "localserver_reference_write_failed", "localserver_reference_write_failed")
		return
	}
	absPath := filepath.Join(root, relativePath)

	// 新增产出文件：立即触发一次产出文档上报（去抖后异步执行）
	h.documentKick(taskUUID)

	c.JSON(http.StatusOK, gin.H{
		"path":          relativePath,
		"absolute_path": absPath,
		"size":          len(body.Content),
	})
}

// referenceSpillSuffix 引用落盘文件名的固定后缀（S-DA-12）。
const referenceSpillSuffix = "-引用"

// referenceSpillMaxAttempts 唯一命名的最大尝试次数（-2 ~ -999）。
const referenceSpillMaxAttempts = 999

// errReferenceNameExhausted 表示在任务目录内无法为引用内容生成唯一文件名。
var errReferenceNameExhausted = errors.New("引用落盘文件名尝试次数已耗尽")

// errReferenceNameInvalid 表示引用文件名不合法（空名或消毒后仍含路径分隔符）。
// 该错误属于入参错误（400），与写盘失败（500）区分，避免客户端误判为可重试的服务端故障。
var errReferenceNameInvalid = errors.New("引用文件名无效")

// referenceRelName 生成第 attempt 次尝试的落盘文件名（attempt=1 即无后缀序号）。
// 扩展名保留源文件的 md/txt；其它类型统一落为 .md。文件名经 knowledge.SanitizeFileName 消毒，
// 因此不会包含路径分隔符。
func referenceRelName(fileName string, attempt int) (string, error) {
	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 引用文件名不能为空", errReferenceNameInvalid)
	}
	sourceExt := filepath.Ext(trimmed)
	ext := strings.ToLower(sourceExt)
	if ext != ".md" && ext != ".txt" {
		ext = ".md"
	}
	base := knowledge.SanitizeFileName(strings.TrimSuffix(trimmed, sourceExt))

	name := base + referenceSpillSuffix + ext
	if attempt > 1 {
		name = fmt.Sprintf("%s%s-%d%s", base, referenceSpillSuffix, attempt, ext)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%w: 文件路径无效: %s", errReferenceNameInvalid, name)
	}
	return name, nil
}

// createReferenceFile 在任务产出目录内以 O_CREATE|O_EXCL 原子创建引用落盘文件（S-DA-12 / Minor-3）。
//
// 「判断名字是否可用」与「创建文件」在同一系统调用内完成，因此并发请求不会互相覆盖或互相顶掉：
// 撞名（ErrExist）时依次尝试 -2 / -3 … 最多 referenceSpillMaxAttempts 次；其它错误立即返回，
// 不做重试掩盖。写入失败时只删除本次创建出来的空文件，绝不触碰既有产出。返回的是相对 root 的文件名。
func createReferenceFile(root, fileName, content string) (string, error) {
	if _, err := referenceRelName(fileName, 1); err != nil {
		return "", err
	}

	for attempt := 1; attempt <= referenceSpillMaxAttempts; attempt++ {
		name, err := referenceRelName(fileName, attempt)
		if err != nil {
			return "", err
		}
		absPath := filepath.Join(root, name)
		// S-DA-12: 不覆盖既有产出；越界（理论上消毒后不可能）时视为入参错误
		if !pathWithinDirectory(root, absPath) {
			return "", fmt.Errorf("%w: 文件路径不在任务目录范围内", errReferenceNameInvalid)
		}

		// 探测与写入合并为一次原子创建（Minor-3）：O_EXCL 保证本进程之外的创建者也不会被覆盖。
		f, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue // 名字已被占用（可能是普通文件或目录），换下一个候选名
			}
			// Windows 上 O_EXCL 撞目录返回的是「is a directory」而非 ErrExist，
			// 因此这里额外用 Lstat 判定「名字确实被占用」→ 继续换名；确属写盘失败才返回。
			if _, statErr := os.Lstat(absPath); statErr == nil {
				continue
			}
			return "", err
		}

		if _, err := f.WriteString(content); err != nil {
			_ = f.Close()
			// 只清理本次创建物；失败也不影响既有文件
			if rmErr := os.Remove(absPath); rmErr != nil {
				applog.Warn("引用落盘失败后清理临时文件失败", "path", name, "error", rmErr)
			}
			return "", err
		}
		if err := f.Close(); err != nil {
			if rmErr := os.Remove(absPath); rmErr != nil {
				applog.Warn("引用落盘失败后清理临时文件失败", "path", name, "error", rmErr)
			}
			return "", err
		}
		return name, nil
	}
	return "", errReferenceNameExhausted
}

// isReadableFileType checks if the file extension is a supported readable type (text or image).
func isReadableFileType(ext string) bool {
	return isTextFileType(ext) || isImageFileType(ext)
}

// isTextFileType checks if the file extension is a supported text-based type.
func isTextFileType(ext string) bool {
	switch strings.ToLower(ext) {
	case
		"md", "markdown",
		"txt", "text", "log",
		"json", "xml", "yaml", "yml", "toml", "ini", "cfg", "conf", "properties",
		"html", "htm", "css", "scss", "less", "js", "jsx", "ts", "tsx", "vue", "svelte",
		"go", "py", "java", "c", "cpp", "cc", "cxx", "h", "hpp", "hxx", "rs", "swift", "kt", "scala",
		"sh", "bash", "zsh", "fish", "ps1", "bat", "cmd",
		"sql", "graphql", "gql",
		"csv", "tsv",
		"dockerfile", "makefile", "gradle", "cmake",
		"gitignore", "gitkeep", "env", "editorconfig",
		"proto", "lock",
		"rst", "asciidoc", "adoc",
		"pl", "rb", "php", "lua", "r", "m", "mm",
		"dart", "ex", "exs",
		"zig", "nim", "crystal":
		return true
	default:
		return false
	}
}

// isImageFileType checks if the file extension is a supported image type.
func isImageFileType(ext string) bool {
	switch strings.ToLower(ext) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg", "ico":
		return true
	default:
		return false
	}
}

// safeTaskFilePath validates and resolves a relative path within the task directory,
// preventing directory traversal attacks.
func (h *TasksHandler) safeTaskFilePath(taskDir, relPath string) (string, error) {
	if strings.TrimSpace(taskDir) == "" {
		return "", fmt.Errorf("任务目录为空")
	}
	root, err := filepath.Abs(strings.TrimSpace(taskDir))
	if err != nil {
		return "", fmt.Errorf("任务目录无效: %w", err)
	}
	cleanPath := filepath.Clean(strings.TrimSpace(relPath))
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("文件路径无效: %s", relPath)
	}
	absPath := filepath.Join(root, cleanPath)
	if !pathWithinDirectory(root, absPath) {
		return "", fmt.Errorf("文件路径不在任务目录范围内")
	}
	containsSymlink, err := pathContainsSymlink(root, absPath)
	if err != nil {
		return "", fmt.Errorf("文件路径无效: %w", err)
	}
	if containsSymlink {
		return "", fmt.Errorf("文件路径不允许包含符号链接")
	}
	return absPath, nil
}

func pathWithinDirectory(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathContainsSymlink(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, fmt.Errorf("文件路径不在任务目录范围内")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

// listTaskSessions returns all local CLI running records under the task, and the complete events can be played back on demand through WebSocket.
func (h *TasksHandler) listTaskSessions(c *gin.Context) {
	taskUUID := c.Param("uuid")
	rows, err := h.taskDB().Query(
		`SELECT s.uuid, s.task_uuid, s.step_uuid, ts.step_key, ts.name,
		        s.conversation_uuid, s.parent_session_uuid, s.run_no, s.status,
		        s.cli_type, s.external_session_id, s.work_dir, s.prompt_snapshot,
		        COALESCE((SELECT p.user_prompt FROM gt_task_progress p
		                  WHERE p.session_uuid = s.uuid AND p.record_type = 'user_question'
		                  ORDER BY p.created_at ASC LIMIT 1), '') AS display_prompt,
		        s.input_tokens, s.output_tokens, s.total_tokens,
		        s.started_at, s.finished_at, s.duration_ms, s.created_at, s.updated_at,
		        s.error_message
		 FROM gt_cli_sessions s
		 JOIN gt_task_steps ts ON ts.uuid = s.step_uuid
		 WHERE s.task_uuid = ?
		 ORDER BY ts.step_order ASC, s.created_at ASC`,
		taskUUID,
	)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	type SessionItem struct {
		UUID              string `json:"uuid"`
		TaskUUID          string `json:"task_uuid"`
		StepUUID          string `json:"step_uuid"`
		StepKey           string `json:"step_key"`
		StepName          string `json:"step_name"`
		ConversationUUID  string `json:"conversation_uuid"`
		ParentSessionUUID string `json:"parent_session_uuid"`
		RunNo             int    `json:"run_no"`
		Status            string `json:"status"`
		CLIType           string `json:"cli_type"`
		ExternalSessionID string `json:"external_session_id"`
		WorkDir           string `json:"work_dir"`
		PromptSnapshot    string `json:"prompt_snapshot"`
		DisplayPrompt     string `json:"display_prompt"`
		InputTokens       int    `json:"input_tokens"`
		OutputTokens      int    `json:"output_tokens"`
		TotalTokens       int    `json:"total_tokens"`
		StartedAt         int64  `json:"started_at"`
		FinishedAt        int64  `json:"finished_at"`
		DurationMs        int64  `json:"duration_ms"`
		CreatedAt         int64  `json:"created_at"`
		UpdatedAt         int64  `json:"updated_at"`
		ErrorMessage      string `json:"error_message"`
	}

	items := make([]SessionItem, 0)
	for rows.Next() {
		var item SessionItem
		if err := rows.Scan(
			&item.UUID, &item.TaskUUID, &item.StepUUID, &item.StepKey, &item.StepName,
			&item.ConversationUUID, &item.ParentSessionUUID, &item.RunNo, &item.Status,
			&item.CLIType, &item.ExternalSessionID, &item.WorkDir, &item.PromptSnapshot, &item.DisplayPrompt,
			&item.InputTokens, &item.OutputTokens, &item.TotalTokens,
			&item.StartedAt, &item.FinishedAt, &item.DurationMs, &item.CreatedAt, &item.UpdatedAt,
			&item.ErrorMessage,
		); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// listSessionEvents 按序返回一次 CLI 会话留存的执行过程事件（思考/工具调用/工具结果）。
// 动态区展开“执行过程”时按 after_sequence 增量拉取，事件结构与 WS executor.event 推送一致。
func (h *TasksHandler) listSessionEvents(c *gin.Context) {
	sessionUUID := strings.TrimSpace(c.Param("uuid"))
	if sessionUUID == "" {
		i18n.LocalServerError(c, http.StatusBadRequest, fmt.Errorf("缺少会话 UUID"))
		return
	}
	afterSeq := 0
	if v, err := strconv.Atoi(c.Query("after_sequence")); err == nil && v > 0 {
		afterSeq = v
	}
	entries, err := storagelog.NewEventStore(h.taskDB()).QueryAfterSequence(sessionUUID, afterSeq)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	type SessionEventItem struct {
		SessionUUID string                  `json:"session_uuid"`
		Sequence    int                     `json:"sequence"`
		EventType   string                  `json:"event_type"`
		Event       *executor.ExecutorEvent `json:"event,omitempty"`
		CreatedAt   int64                   `json:"created_at"`
	}
	items := make([]SessionEventItem, 0, len(entries))
	for _, entry := range entries {
		item := SessionEventItem{
			SessionUUID: entry.SessionUUID,
			Sequence:    entry.Sequence,
			EventType:   entry.EventType,
			CreatedAt:   entry.CreatedAt,
		}
		var event executor.ExecutorEvent
		if json.Unmarshal([]byte(entry.Payload), &event) == nil {
			item.Event = &event
		}
		items = append(items, item)
	}
	body := gin.H{"items": items}
	if activity, ok := h.orchestrator().LiveActivity(sessionUUID); ok && activity.Kind != "" {
		body["activity"] = activity
	}
	c.JSON(http.StatusOK, body)
}

func (h *TasksHandler) getSessionEventFull(c *gin.Context) {
	sessionUUID := strings.TrimSpace(c.Param("uuid"))
	sequence, err := strconv.ParseInt(c.Param("sequence"), 10, 64)
	if err != nil || sequence <= 0 || sessionUUID == "" {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "bad_request")
		return
	}
	var payload string
	err = h.taskDB().QueryRow(
		`SELECT payload_json FROM gt_session_events WHERE session_uuid = ? AND sequence = ?`,
		sessionUUID, sequence,
	).Scan(&payload)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "common_record_not_found", "not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	store := artifactstore.Default()
	if store == nil {
		c.JSON(http.StatusNotFound, gin.H{"code": "artifact_missing"})
		return
	}
	file, openErr := store.Open(sessionUUID, sequence)
	if openErr != nil {
		applog.Warn("[Executor] artifact_missing", "session", sessionUUID, "sequence", sequence)
		c.JSON(http.StatusNotFound, gin.H{"code": "artifact_missing"})
		return
	}
	defer file.Close()
	c.Header("Content-Type", "text/plain; charset=utf-8")
	if c.Query("download") == "1" {
		c.Header("Content-Disposition", "attachment")
	}
	_, _ = io.Copy(c.Writer, file)
	_ = payload
}

// continueSession reuses the CLI native session ID to initiate the next round of questions.
func (h *TasksHandler) continueSession(c *gin.Context) {
	parentSessionUUID := c.Param("uuid")
	var body struct {
		Content        string `json:"content"`
		DisplayContent string `json:"display_content"`
		RequestID      string `json:"request_id"`
		CLIType        string `json:"cli_type"`
		Model          string `json:"model"`
		ModelName      string `json:"model_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	displayContent := body.DisplayContent
	if strings.TrimSpace(displayContent) == "" {
		displayContent = body.Content
	}
	prompt, err := h.resolveSessionTaskAttachmentReferences(c.Request.Context(), parentSessionUUID, body.Content)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	modelName := strings.TrimSpace(body.ModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(body.Model)
	}
	sessionUUID, err := h.orchestrator().ContinueConversation(
		c.Request.Context(),
		workflow.ContinueConversationOptions{
			ParentSessionUUID: parentSessionUUID,
			Prompt:            prompt,
			DisplayPrompt:     displayContent,
			RequestID:         body.RequestID,
			CLIType:           body.CLIType,
			Model:             modelName,
		},
	)
	if err != nil {
		h.writeWorkflowError(c, err, http.StatusConflict)
		return
	}
	var taskUUID string
	if queryErr := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT task_uuid FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&taskUUID); queryErr == nil {
		_ = markTaskNotificationsRead(c.Request.Context(), h.taskDB(), taskUUID, true)
		h.pushTaskSync(taskUUID)
	}
	c.JSON(http.StatusAccepted, gin.H{"session_uuid": sessionUUID})
}

// stopSession stops the session
func (h *TasksHandler) stopSession(c *gin.Context) {
	sessionUUID := c.Param("uuid")
	err := h.orchestrator().StopSession(c.Request.Context(), sessionUUID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	var taskUUID string
	if queryErr := h.taskDB().QueryRowContext(c.Request.Context(), `SELECT task_uuid FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&taskUUID); queryErr == nil {
		h.pushTaskSync(taskUUID)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// listWorkItems List of work items that can be imported
func (h *TasksHandler) listWorkItems(c *gin.Context) {
	items, err := h.taskCloudClient().GetWorkItems(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []cloud.WorkItem{}
	}

	// 同一需求或缺陷只能属于一条本地任务。http/https 和两套身份列都要算进去，
	// 不能因为登录用户 ID 变了就再配出第二条。
	if sess, ok := getSession(c); ok {
		primaryKey, alternateKey := pipeline.CloudSourceKeys(h.taskCloudClient().BaseURL())
		assigned, qErr := loadBoundCloudTasks(h.taskDB(), primaryKey, alternateKey, sess.UserID)
		if qErr == nil {
			filtered := make([]cloud.WorkItem, 0, len(items))
			for _, it := range items {
				if _, taken := assigned[it.Type+":"+strconv.FormatInt(it.ID, 10)]; !taken {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}
	}

	c.JSON(http.StatusOK, gin.H{"items": items})
}

// listPipelines available Pipeline list
func (h *TasksHandler) listPipelines(c *gin.Context) {
	pipelines, err := h.taskCloudClient().GetPipelines(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	if pipelines == nil {
		pipelines = []cloud.PipelineSummary{}
	}
	c.JSON(http.StatusOK, gin.H{"items": pipelines})
}

// getPipelineSnapshot Pipeline snapshot
func (h *TasksHandler) getPipelineSnapshot(c *gin.Context) {
	pipelineIDStr := c.Param("id")
	pipelineID, err := strconv.ParseInt(pipelineIDStr, 10, 64)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_id_invalid", "pipeline_id_invalid")
		return
	}

	snapshot, err := h.taskCloudClient().GetPipelineSnapshot(c.Request.Context(), pipelineID)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func normalizeExistingDirectory(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("工作目录不能为空")
	}

	absolutePath, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("工作目录地址无效: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)

	info, err := os.Stat(absolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("工作目录不存在: %s", absolutePath)
		}
		return "", fmt.Errorf("无法访问工作目录 %s: %w", absolutePath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("工作目录地址不是文件夹: %s", absolutePath)
	}

	return absolutePath, nil
}

func normalizeExistingDirectories(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("至少需要一个工作目录")
	}

	workDirs := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for index, value := range raw {
		workDir, err := normalizeExistingDirectory(value)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个工作目录无效: %w", index+1, err)
		}
		key := workDir
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("工作目录不能重复: %s", workDir)
		}
		seen[key] = struct{}{}
		workDirs = append(workDirs, workDir)
	}
	return workDirs, nil
}

// ====== Task status (kanban column) configuration ======

type taskLaneRow struct {
	ID        int64  `json:"id"`
	LaneKey   string `json:"lane_key"`
	Title     string `json:"title"`
	Color     string `json:"color"`
	SortOrder int    `json:"sort_order"`
	IsHidden  int    `json:"is_hidden"`
	TaskCount int    `json:"task_count"`
}

func (h *TasksHandler) listTaskLanes(c *gin.Context) {
	rows, err := h.taskDB().Query(`
		SELECT l.id, l.lane_key, l.title, l.color, l.sort_order, l.is_hidden,
		       (SELECT COUNT(*) FROM gt_tasks t WHERE t.status = l.lane_key AND COALESCE(t.source_type, 'local') <> 'cloud') AS task_count
		FROM gt_task_lanes l
		ORDER BY l.sort_order`)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	var lanes []taskLaneRow
	for rows.Next() {
		var l taskLaneRow
		rows.Scan(&l.ID, &l.LaneKey, &l.Title, &l.Color, &l.SortOrder, &l.IsHidden, &l.TaskCount)
		lanes = append(lanes, l)
	}
	if lanes == nil {
		lanes = []taskLaneRow{}
	}
	c.JSON(http.StatusOK, gin.H{"items": lanes})
}

func (h *TasksHandler) createTaskLane(c *gin.Context) {
	var req struct {
		Title string `json:"title" binding:"required"`
		Color string `json:"color"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_title_required", "title_required")
		return
	}
	if req.Color == "" {
		req.Color = "#8c8c8c"
	}

	db := h.taskDB()
	now := time.Now().UnixMilli()

	// Generate lane_key: based on title pinyin or random
	laneKey := fmt.Sprintf("lane_%d", now)

	// Get the current maximum sort_order
	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM gt_task_lanes`).Scan(&maxOrder)

	result, err := db.Exec(
		`INSERT INTO gt_task_lanes (lane_key, title, color, sort_order, is_hidden, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		laneKey, strings.TrimSpace(req.Title), req.Color, maxOrder+1, now)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "lane_key": laneKey})
}

func (h *TasksHandler) updateTaskLane(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_id_invalid", "invalid_id")
		return
	}

	var req struct {
		Title    *string `json:"title"`
		Color    *string `json:"color"`
		IsHidden *int    `json:"is_hidden"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}

	sets := []string{}
	args := []interface{}{}
	if req.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, strings.TrimSpace(*req.Title))
	}
	if req.Color != nil {
		sets = append(sets, "color = ?")
		args = append(args, *req.Color)
	}
	if req.IsHidden != nil {
		sets = append(sets, "is_hidden = ?")
		args = append(args, *req.IsHidden)
	}
	if len(sets) == 0 {
		i18n.Error(c, http.StatusBadRequest, "localserver_no_update_fields", "no_update_fields")
		return
	}

	args = append(args, id)
	_, err = h.taskDB().Exec(`UPDATE gt_task_lanes SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *TasksHandler) deleteTaskLane(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_id_invalid", "invalid_id")
		return
	}

	db := h.taskDB()

	// Check if there are any tasks using this status
	var laneKey string
	err = db.QueryRow(`SELECT lane_key FROM gt_task_lanes WHERE id = ?`, id).Scan(&laneKey)
	if err == sql.ErrNoRows {
		i18n.Error(c, http.StatusNotFound, "localserver_status_not_found", "status_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM gt_tasks WHERE status = ?`, laneKey).Scan(&count)
	if count > 0 {
		i18n.Errorf(c, http.StatusConflict, "localserver_lane_tasks_exist", i18n.Params{"Count": count})
		return
	}

	_, err = db.Exec(`DELETE FROM gt_task_lanes WHERE id = ?`, id)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *TasksHandler) reorderTaskLanes(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_ids_required", "ids_required")
		return
	}

	db := h.taskDB()
	tx, err := db.Begin()
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	for i, id := range req.IDs {
		if _, err := tx.Exec(`UPDATE gt_task_lanes SET sort_order = ? WHERE id = ?`, i, id); err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UUID routes must enforce the same scope as the team board and upload queue.
// Task-skill reporting is separately authenticated and may finish an old offline execution.
func (h *TasksHandler) requireTaskScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("uuid")
		if id == "" {
			c.Next()
			return
		}
		var source, sourceKey, owner string
		query := "SELECT source_type, cloud_source_key, cloud_user_id FROM gt_tasks WHERE uuid=?"
		if strings.Contains(c.FullPath(), "/tasks/sessions/") {
			query = "SELECT t.source_type,t.cloud_source_key,t.cloud_user_id FROM gt_tasks t JOIN gt_cli_sessions s ON s.task_uuid=t.uuid WHERE s.uuid=?"
		}
		err := h.taskDB().QueryRowContext(c.Request.Context(), query, id).Scan(&source, &sourceKey, &owner)
		if err == sql.ErrNoRows {
			c.Next()
			return
		}
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			c.Abort()
			return
		}
		if source == "cloud" {
			client := h.taskCloudClient()
			allowed := false
			if client != nil && client.UserID() != "" {
				primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
				allowed = owner == client.UserID() && (sourceKey == primary || sourceKey == alternate)
			}
			if !allowed {
				i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// 执行错误在响应边界选择稳定 key，避免将领域错误原文直接返回用户。
func (h *TasksHandler) writeWorkflowError(c *gin.Context, err error, fallback int) {
	switch {
	case errors.Is(err, workflow.ErrTaskBusy):
		i18n.Error(c, http.StatusConflict, "localserver_task_session_busy", "task_has_active_sessions")
	case errors.Is(err, workflow.ErrStepRuntimeRequired):
		i18n.Error(c, http.StatusConflict, "localserver_pipeline_step_execution_required", "pipeline_incomplete")
	case errors.Is(err, taskruntime.ErrExecutionModeConflict):
		i18n.Error(c, http.StatusConflict, "localserver_execution_mode_conflict", "execution_mode_conflict")
	case errors.Is(err, sql.ErrNoRows):
		i18n.Error(c, http.StatusNotFound, "localserver_step_not_found", "step_not_found")
	case errors.Is(err, workflow.ErrPipelineTaskDone):
		i18n.Error(c, http.StatusConflict, "localserver_pipeline_task_reopen", "pipeline_task_reopen_required")
	case errors.Is(err, workflow.ErrRequestConflict):
		i18n.Error(c, http.StatusConflict, "localserver_conversation_request_conflict", "conversation_request_conflict")
	default:
		i18n.LocalServerError(c, fallback, err)
	}
}
