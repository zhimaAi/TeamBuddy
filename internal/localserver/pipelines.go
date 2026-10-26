package localserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/cloudscope"
	"goteams-client/internal/i18n"
	"goteams-client/internal/pipeline"
)

type PipelinesHandler struct {
	db          func() *sql.DB
	iconStore   *IconStore
	cloudClient func() *cloud.Client
}

func NewPipelinesHandler(db func() *sql.DB, iconStore *IconStore, cloudClients ...func() *cloud.Client) *PipelinesHandler {
	h := &PipelinesHandler{db: db, iconStore: iconStore}
	if len(cloudClients) > 0 {
		h.cloudClient = cloudClients[0]
	}
	return h
}

func (h *PipelinesHandler) RegisterCloudRoutes(r *gin.RouterGroup) {
	r.POST("/pipelines/sync", h.syncCloud)
	r.POST("/pipelines/sync-cloud", h.syncCloud)
}

func (h *PipelinesHandler) syncCloud(c *gin.Context) {
	if h.cloudClient == nil || h.cloudClient() == nil {
		i18n.LocalServerError(c, http.StatusServiceUnavailable, errors.New("云端客户端未初始化"))
		return
	}
	client := h.cloudClient()
	svc, ok := h.service(c)
	if !ok {
		return
	}
	result, err := syncCloudPipelines(c.Request.Context(), svc, client)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": result, "synced": len(result)})
}

func syncCloudPipelines(ctx context.Context, svc *pipeline.Service, client *cloud.Client) ([]pipeline.Pipeline, error) {
	pipelines, err := client.GetPipelines(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]pipeline.CloudPipeline, 0, len(pipelines))
	for _, pl := range pipelines {
		snapshot, err := client.GetPipelineSnapshot(ctx, pl.ID)
		if err != nil {
			return nil, fmt.Errorf("同步云端流水线 %d 失败: %w", pl.ID, err)
		}
		item := pipeline.CloudPipeline{CloudPipelineID: strconv.FormatInt(snapshot.ID, 10), Name: snapshot.Name, Avatar: snapshot.Icon}
		for _, step := range snapshot.Steps {
			item.Steps = append(item.Steps, pipeline.CloudStep{
				CloudStepID: step.StepKey,
				SortOrder:   step.SortOrder,
				Name:        step.Name,
				Avatar:      step.Avatar,
				Prompt:      step.Prompt,
			})
		}
		items = append(items, item)
	}
	return svc.SyncCloud(cloudscope.With(ctx, client.SessionScope()), pipeline.CloudSourceKey(client.BaseURL()), items)
}

func (s *Server) triggerCloudPipelineSync() {
	syncDB, syncClient := s.currentSessionDB(), s.currentCloudClient()
	if syncDB == nil || syncClient == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := syncCloudPipelines(ctx, pipeline.NewService(syncDB), syncClient); err != nil {
			applog.Warn("[LocalServer] 登录后同步云端流水线失败", "error", err)
		}
	}()
}

// triggerCloudExpertGroupSync refreshes the read-only cloud expert-group cache after login
// (需求 1896 优化点2). A failure must not fail login; it just leaves the cache stale.
func (s *Server) triggerCloudExpertGroupSync() {
	syncClient := s.currentCloudClient()
	if syncClient == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := syncCloudExpertGroups(ctx, syncClient); err != nil {
			applog.Warn("[LocalServer] 登录后同步云端专家团失败", "error", err)
		}
	}()
}

func (h *PipelinesHandler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("", h.list)
	r.POST("", h.create)
	r.GET("/:uuid", h.get)
	r.POST("/:uuid/copy", h.copy)
	r.PUT("/:uuid", h.update)
	r.DELETE("/:uuid", h.delete)
	r.POST("/:uuid/steps", h.addStep)
	r.PUT("/:uuid/steps/reorder", h.reorderSteps)
	r.PUT("/:uuid/steps/execution-config", h.batchUpdateStepExecution)
	r.PUT("/:uuid/steps/:step_uuid", h.updateStep)
	r.DELETE("/:uuid/steps/:step_uuid", h.deleteStep)
}

func (h *PipelinesHandler) service(c *gin.Context) (*pipeline.Service, bool) {
	if h.db == nil || h.db() == nil {
		i18n.LocalServerError(c, http.StatusServiceUnavailable, errors.New("本地任务数据库尚未就绪"))
		return nil, false
	}
	return pipeline.NewService(h.db()), true
}

func (h *PipelinesHandler) list(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	items, err := svc.List(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *PipelinesHandler) get(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.Get(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		pipelineError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *PipelinesHandler) create(c *gin.Context) {
	input, newAvatarURL, err := h.bindPipelineInput(c)
	if err != nil {
		pipelineAvatarError(c, err)
		return
	}
	svc, ok := h.service(c)
	if !ok {
		if newAvatarURL != "" && h.iconStore != nil {
			_ = h.iconStore.Remove(newAvatarURL)
		}
		return
	}
	item, err := svc.Create(c.Request.Context(), input)
	if err != nil {
		if newAvatarURL != "" && h.iconStore != nil {
			_ = h.iconStore.Remove(newAvatarURL)
		}
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *PipelinesHandler) copy(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.Copy(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		pipelineError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *PipelinesHandler) update(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	pipelineUUID := c.Param("uuid")
	previousPipeline, previousErr := svc.Get(c.Request.Context(), pipelineUUID)
	if isMultipartRequest(c) && (previousErr != nil || previousPipeline.SourceType != "local") {
		i18n.Error(c, http.StatusBadRequest, "localserver_pipeline_upload_local_only", "local_pipeline_only")
		return
	}
	input, newAvatarURL, err := h.bindPipelineInput(c)
	if err != nil {
		pipelineAvatarError(c, err)
		return
	}
	item, err := svc.Update(c.Request.Context(), pipelineUUID, input)
	if err != nil {
		if newAvatarURL != "" && h.iconStore != nil {
			_ = h.iconStore.Remove(newAvatarURL)
		}
		pipelineError(c, err)
		return
	}
	if previousPipeline != nil && previousPipeline.Avatar != item.Avatar {
		h.removeIconIfUnused(c.Request.Context(), previousPipeline.Avatar)
	}
	c.JSON(http.StatusOK, item)
}

func (h *PipelinesHandler) delete(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.Get(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		pipelineError(c, err)
		return
	}
	if err := svc.Delete(c.Request.Context(), c.Param("uuid")); err != nil {
		pipelineError(c, err)
		return
	}
	h.removeIconIfUnused(c.Request.Context(), item.Avatar)
	for _, step := range item.Steps {
		h.removeIconIfUnused(c.Request.Context(), step.Avatar)
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (h *PipelinesHandler) addStep(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	input, newAvatarURL, err := h.bindStepInput(c)
	if err != nil {
		pipelineAvatarError(c, err)
		return
	}
	item, err := svc.AddStep(c.Request.Context(), c.Param("uuid"), input)
	if err != nil {
		if newAvatarURL != "" && h.iconStore != nil {
			_ = h.iconStore.Remove(newAvatarURL)
		}
		pipelineError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *PipelinesHandler) batchUpdateStepExecution(c *gin.Context) {
	var input pipeline.BatchStepExecutionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.BatchUpdateStepExecution(c.Request.Context(), c.Param("uuid"), input)
	if err != nil {
		pipelineError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *PipelinesHandler) updateStep(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	pipelineUUID := c.Param("uuid")
	stepUUID := c.Param("step_uuid")
	previousPipeline, previousErr := svc.Get(c.Request.Context(), pipelineUUID)
	if isMultipartRequest(c) && (previousErr != nil || previousPipeline.SourceType != "local") {
		i18n.Error(c, http.StatusBadRequest, "localserver_agent_upload_local_only", "local_agent_only")
		return
	}
	previousStep := findPipelineStep(previousPipeline, stepUUID)
	if isMultipartRequest(c) && previousStep == nil {
		pipelineError(c, pipeline.ErrStepNotFound)
		return
	}

	input, newAvatarURL, err := h.bindStepInput(c)
	if err != nil {
		pipelineAvatarError(c, err)
		return
	}
	item, err := svc.UpdateStep(c.Request.Context(), pipelineUUID, stepUUID, input)
	if err != nil {
		if newAvatarURL != "" && h.iconStore != nil {
			_ = h.iconStore.Remove(newAvatarURL)
		}
		pipelineError(c, err)
		return
	}
	if previousStep != nil && previousStep.Avatar != item.Avatar {
		h.removeIconIfUnused(c.Request.Context(), previousStep.Avatar)
	}
	c.JSON(http.StatusOK, item)
}

func (h *PipelinesHandler) deleteStep(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	pipelineUUID := c.Param("uuid")
	stepUUID := c.Param("step_uuid")
	item, err := svc.Get(c.Request.Context(), pipelineUUID)
	if err != nil {
		pipelineError(c, err)
		return
	}
	previousStep := findPipelineStep(item, stepUUID)
	if previousStep == nil {
		pipelineError(c, pipeline.ErrStepNotFound)
		return
	}
	if err := svc.DeleteStep(c.Request.Context(), pipelineUUID, stepUUID); err != nil {
		pipelineError(c, err)
		return
	}
	h.removeIconIfUnused(c.Request.Context(), previousStep.Avatar)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// 流水线复制会复用头像 URL；只有最后一个数据库引用消失时才删除受管文件，
// 避免删除或换头像时破坏副本仍在使用的头像。
func (h *PipelinesHandler) removeIconIfUnused(ctx context.Context, rawURL string) {
	if h.iconStore == nil {
		return
	}
	if _, managed := managedIconFilename(rawURL); !managed {
		return
	}
	if h.db == nil {
		return
	}
	db := h.db()
	if db == nil {
		return
	}
	var references int
	err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM gt_pipelines WHERE avatar = ?) +
			(SELECT COUNT(*) FROM gt_pipeline_steps WHERE avatar = ?)`,
		rawURL, rawURL).Scan(&references)
	if err != nil {
		applog.Warn("[LocalServer] 检查受管头像引用失败", "error", err)
		return
	}
	if references > 0 {
		return
	}
	if err := h.iconStore.Remove(rawURL); err != nil {
		applog.Warn("[LocalServer] 删除未引用受管头像失败", "error", err)
	}
}

func (h *PipelinesHandler) bindPipelineInput(c *gin.Context) (pipeline.PipelineInput, string, error) {
	var input pipeline.PipelineInput
	if !isMultipartRequest(c) {
		if err := c.ShouldBindJSON(&input); err != nil {
			return input, "", err
		}
		return input, "", nil
	}
	if h.iconStore == nil {
		return input, "", fmt.Errorf("本地头像存储尚未初始化")
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIconUploadRequestSize)
	input = pipeline.PipelineInput{
		Name:        c.PostForm("name"),
		Description: c.PostForm("description"),
		Avatar:      c.PostForm("avatar"),
	}
	header, err := c.FormFile("avatar_file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return input, "", nil
		}
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			return input, "", ErrIconTooLarge
		}
		return input, "", fmt.Errorf("读取上传图标失败: %w", err)
	}
	avatarURL, err := h.iconStore.Save(header)
	if err != nil {
		return input, "", err
	}
	input.Avatar = avatarURL
	return input, avatarURL, nil
}

func (h *PipelinesHandler) bindStepInput(c *gin.Context) (pipeline.StepInput, string, error) {
	var input pipeline.StepInput
	if !isMultipartRequest(c) {
		if err := c.ShouldBindJSON(&input); err != nil {
			return input, "", err
		}
		return input, "", nil
	}
	if h.iconStore == nil {
		return input, "", fmt.Errorf("本地头像存储尚未初始化")
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIconUploadRequestSize)
	input = pipeline.StepInput{
		Name:        c.PostForm("name"),
		Description: c.PostForm("description"),
		Avatar:      c.PostForm("avatar"),
		Prompt:      c.PostForm("prompt"),
		CLIType:     c.PostForm("cli_type"),
		ModelName:   c.PostForm("model_name"),
	}
	header, err := c.FormFile("avatar_file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return input, "", nil
		}
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			return input, "", ErrIconTooLarge
		}
		return input, "", fmt.Errorf("读取上传头像失败: %w", err)
	}
	avatarURL, err := h.iconStore.Save(header)
	if err != nil {
		return input, "", err
	}
	input.Avatar = avatarURL
	return input, avatarURL, nil
}

func isMultipartRequest(c *gin.Context) bool {
	return strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data")
}

func findPipelineStep(item *pipeline.Pipeline, stepUUID string) *pipeline.Step {
	if item == nil {
		return nil
	}
	for index := range item.Steps {
		if item.Steps[index].UUID == stepUUID {
			return &item.Steps[index]
		}
	}
	return nil
}

func pipelineAvatarError(c *gin.Context, err error) {
	if errors.Is(err, ErrIconTooLarge) {
		i18n.LocalServerError(c, http.StatusRequestEntityTooLarge, err)
		return
	}
	i18n.LocalServerError(c, http.StatusBadRequest, err)
}

func (h *PipelinesHandler) reorderSteps(c *gin.Context) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	svc, ok := h.service(c)
	if !ok {
		return
	}
	if err := svc.ReorderSteps(c.Request.Context(), c.Param("uuid"), body.IDs); err != nil {
		pipelineError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func pipelineError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, pipeline.ErrNotFound), errors.Is(err, pipeline.ErrStepNotFound):
		i18n.LocalServerError(c, http.StatusNotFound, err)
	default:
		i18n.LocalServerError(c, http.StatusBadRequest, err)
	}
}
