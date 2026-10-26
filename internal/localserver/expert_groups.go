package localserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/cloud"
	"goteams-client/internal/expertgroup"
	"goteams-client/internal/i18n"
	"goteams-client/internal/pipeline"
)

type ExpertGroupsHandler struct {
	db          func() *sql.DB
	iconStore   *IconStore
	cloudClient func() *cloud.Client
}

func NewExpertGroupsHandler(db func() *sql.DB, iconStore *IconStore, cloudClients ...func() *cloud.Client) *ExpertGroupsHandler {
	h := &ExpertGroupsHandler{db: db, iconStore: iconStore}
	if len(cloudClients) > 0 {
		h.cloudClient = cloudClients[0]
	}
	return h
}

func (h *ExpertGroupsHandler) RegisterCloudRoutes(r *gin.RouterGroup) {
	r.POST("/expert-groups/sync", h.syncCloud)
	r.POST("/expert-groups/sync-cloud", h.syncCloud)
}

// syncCloud 拉取云端可用专家团（scope 过滤后）并镜像到内存缓存。仅供云端登录态。
func (h *ExpertGroupsHandler) syncCloud(c *gin.Context) {
	if h.cloudClient == nil || h.cloudClient() == nil {
		i18n.LocalServerError(c, http.StatusServiceUnavailable, errors.New("云端客户端未初始化"))
		return
	}
	items, err := syncCloudExpertGroups(c.Request.Context(), h.cloudClient())
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "synced": len(items)})
}

// syncCloudExpertGroups fetches the cloud expert-group list and replaces the in-memory cache.
func syncCloudExpertGroups(ctx context.Context, client *cloud.Client) ([]expertgroup.CloudGroup, error) {
	groups, err := client.GetExpertGroups(ctx)
	if err != nil {
		return nil, err
	}
	inputs := make([]expertgroup.CloudGroupInput, 0, len(groups))
	for _, group := range groups {
		input := expertgroup.CloudGroupInput{
			CloudGroupID: strconv.FormatInt(group.ID, 10),
			Name:         group.Name,
			Description:  group.Description,
			Avatar:       group.Avatar,
		}
		for _, agent := range group.Agents {
			role := agent.Role
			if role == "" {
				role = expertgroup.RoleMember
			}
			input.Agents = append(input.Agents, expertgroup.CloudAgent{
				CloudAgentID: agent.Name, // 云端 agent 唯一性以 name 为准
				Role:         role,
				Name:         agent.Name,
				Avatar:       agent.Avatar,
				Prompt:       agent.Prompt,
				SortOrder:    agent.SortOrder,
			})
		}
		inputs = append(inputs, input)
	}
	return expertgroup.DefaultCloudStore().ReplaceAll(pipeline.CloudSourceKey(client.BaseURL()), inputs, client.SessionScope())
}

func (h *ExpertGroupsHandler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("", h.list)
	r.POST("", h.create)
	r.GET("/reusable-agents", h.listReusableAgents)
	r.GET("/:uuid", h.get)
	r.POST("/:uuid/copy", h.copy)
	r.PUT("/:uuid", h.update)
	r.DELETE("/:uuid", h.delete)
	r.POST("/:uuid/members", h.addMember)
	r.PUT("/:uuid/members/execution-config", h.batchUpdateMemberExecution)
	r.PUT("/:uuid/members/:member_uuid", h.updateMember)
	r.DELETE("/:uuid/members/:member_uuid", h.deleteMember)
}

func (h *ExpertGroupsHandler) service(c *gin.Context) (*expertgroup.Service, bool) {
	if h.db == nil || h.db() == nil {
		i18n.LocalServerError(c, http.StatusServiceUnavailable, errors.New("本地任务数据库尚未就绪"))
		return nil, false
	}
	return expertgroup.NewService(h.db()), true
}

func (h *ExpertGroupsHandler) list(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	items, err := svc.List(c.Request.Context())
	if err != nil {
		expertGroupError(c, err)
		return
	}
	// 合并内存中的云端专家团（团队只读，source_type=cloud），与云端流水线一致；本地列表在前。
	for _, cg := range expertgroup.DefaultCloudStore().List() {
		items = append(items, cg.ToGroup())
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *ExpertGroupsHandler) get(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.Get(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *ExpertGroupsHandler) create(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	input, uploaded, err := h.bindGroupInput(c)
	if err != nil {
		expertGroupError(c, err)
		return
	}
	item, err := svc.Create(c.Request.Context(), input)
	if err != nil {
		if uploaded != "" {
			_ = h.iconStore.Remove(uploaded)
		}
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *ExpertGroupsHandler) update(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	input, uploaded, err := h.bindGroupInput(c)
	if err != nil {
		expertGroupError(c, err)
		return
	}
	item, err := svc.Update(c.Request.Context(), c.Param("uuid"), input)
	if err != nil {
		if uploaded != "" {
			_ = h.iconStore.Remove(uploaded)
		}
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *ExpertGroupsHandler) copy(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.Copy(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *ExpertGroupsHandler) delete(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	if err := svc.Delete(c.Request.Context(), c.Param("uuid")); err != nil {
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (h *ExpertGroupsHandler) addMember(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	input, uploaded, err := h.bindMemberInput(c)
	if err != nil {
		expertGroupError(c, err)
		return
	}
	item, err := svc.AddMember(c.Request.Context(), c.Param("uuid"), input)
	if err != nil {
		if uploaded != "" {
			_ = h.iconStore.Remove(uploaded)
		}
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *ExpertGroupsHandler) updateMember(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	input, uploaded, err := h.bindMemberInput(c)
	if err != nil {
		expertGroupError(c, err)
		return
	}
	item, err := svc.UpdateMember(c.Request.Context(), c.Param("uuid"), c.Param("member_uuid"), input)
	if err != nil {
		if uploaded != "" {
			_ = h.iconStore.Remove(uploaded)
		}
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *ExpertGroupsHandler) batchUpdateMemberExecution(c *gin.Context) {
	var input expertgroup.BatchMemberExecutionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	svc, ok := h.service(c)
	if !ok {
		return
	}
	item, err := svc.BatchUpdateMemberExecution(c.Request.Context(), c.Param("uuid"), input)
	if err != nil {
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *ExpertGroupsHandler) deleteMember(c *gin.Context) {
	svc, ok := h.service(c)
	if !ok {
		return
	}
	if err := svc.DeleteMember(c.Request.Context(), c.Param("uuid"), c.Param("member_uuid")); err != nil {
		expertGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (h *ExpertGroupsHandler) listReusableAgents(c *gin.Context) {
	if h.db == nil || h.db() == nil {
		i18n.LocalServerError(c, http.StatusServiceUnavailable, errors.New("本地任务数据库尚未就绪"))
		return
	}
	type reusableAgent struct {
		UUID        string `json:"uuid"`
		Source      string `json:"source"`
		SourceName  string `json:"source_name"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Avatar      string `json:"avatar"`
		Prompt      string `json:"prompt"`
		CLIType     string `json:"cli_type"`
		ModelName   string `json:"model_name"`
	}
	items := make([]reusableAgent, 0)
	pipelines, err := pipeline.NewService(h.db()).List(c.Request.Context())
	if err != nil {
		expertGroupError(c, err)
		return
	}
	for _, item := range pipelines {
		for _, step := range item.Steps {
			items = append(items, reusableAgent{UUID: step.UUID, Source: "pipeline", SourceName: item.Name,
				Name: step.Name, Description: step.Description, Avatar: step.Avatar, Prompt: step.Prompt,
				CLIType: step.CLIType, ModelName: step.ModelName})
		}
	}
	groups, err := expertgroup.NewService(h.db()).List(c.Request.Context())
	if err != nil {
		expertGroupError(c, err)
		return
	}
	for _, group := range groups {
		members := append([]expertgroup.Member{}, group.Members...)
		if group.Leader != nil {
			members = append([]expertgroup.Member{*group.Leader}, members...)
		}
		for _, member := range members {
			items = append(items, reusableAgent{UUID: member.UUID, Source: "expert_group", SourceName: group.Name,
				Name: member.Name, Description: member.Description, Avatar: member.Avatar, Prompt: member.Prompt,
				CLIType: member.CLIType, ModelName: member.ModelName})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *ExpertGroupsHandler) bindGroupInput(c *gin.Context) (expertgroup.GroupInput, string, error) {
	var input expertgroup.GroupInput
	if !isMultipartRequest(c) {
		return input, "", c.ShouldBindJSON(&input)
	}
	input = expertgroup.GroupInput{Name: c.PostForm("name"), Description: c.PostForm("description"), Avatar: c.PostForm("avatar")}
	avatar, err := h.saveOptionalIcon(c)
	if err != nil {
		return input, "", err
	}
	if avatar != "" {
		input.Avatar = avatar
	}
	return input, avatar, nil
}

func (h *ExpertGroupsHandler) bindMemberInput(c *gin.Context) (expertgroup.MemberInput, string, error) {
	var input expertgroup.MemberInput
	if !isMultipartRequest(c) {
		return input, "", c.ShouldBindJSON(&input)
	}
	input = expertgroup.MemberInput{MemberRole: c.PostForm("member_role"), Name: c.PostForm("name"),
		Description: c.PostForm("description"), Avatar: c.PostForm("avatar"), Prompt: c.PostForm("prompt"),
		CLIType: c.PostForm("cli_type"), ModelName: c.PostForm("model_name")}
	avatar, err := h.saveOptionalIcon(c)
	if err != nil {
		return input, "", err
	}
	if avatar != "" {
		input.Avatar = avatar
	}
	return input, avatar, nil
}

func (h *ExpertGroupsHandler) saveOptionalIcon(c *gin.Context) (string, error) {
	if h.iconStore == nil {
		return "", fmt.Errorf("本地头像存储尚未初始化")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIconUploadRequestSize)
	header, err := c.FormFile("avatar_file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return "", nil
		}
		return "", err
	}
	return h.iconStore.Save(header)
}

func expertGroupError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, expertgroup.ErrNotFound) || errors.Is(err, expertgroup.ErrMemberNotFound) {
		status = http.StatusNotFound
	} else if strings.Contains(strings.ToLower(err.Error()), "unique") {
		status = http.StatusConflict
	}
	i18n.LocalServerError(c, status, err)
}
