package localserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/i18n"
)

func (s *Server) skillClient() *cloud.Client {
	// 保留当前服务选择，但公开接口始终不携带登录凭据。
	baseURL := ""
	if s.config.CloudConfig != nil {
		baseURL = s.config.CloudConfig.APIBaseURL
	}
	if client := s.currentCloudClient(); client != nil {
		baseURL = client.BaseURL()
	}
	return s.customCloudClient(baseURL)
}

func (s *Server) listSkills(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("size", "20"))
	name := strings.TrimSpace(c.Query("name"))
	if err != nil || sizeErr != nil || page < 1 || size < 1 || size > 100 || utf8.RuneCountInString(name) > 100 {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "invalid_skill_query")
		return
	}
	result, err := s.skillClient().ListApprovedSkills(c.Request.Context(), page, size, name, i18n.FromContext(c))
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) createSkill(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var input cloud.CreateSkillInput
	if err := c.ShouldBindJSON(&input); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "invalid_skill")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SourceURL = strings.TrimSpace(input.SourceURL)
	parsed, err := url.Parse(input.SourceURL)
	validURL := err == nil && parsed.Hostname() != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
	validLengths := input.Name != "" && utf8.RuneCountInString(input.Name) <= 100 && utf8.RuneCountInString(input.Remark) <= 1000 && utf8.RuneCountInString(input.Icon) <= 2048 && utf8.RuneCountInString(input.Description) <= 10000 && utf8.RuneCountInString(input.Sharer) <= 100 && utf8.RuneCountInString(input.SourceURL) <= 2048
	if !validURL || !validLengths {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "invalid_skill")
		return
	}
	// 保留用户手动选择的合法来源；旧客户端未传来源时仍按地址识别。
	switch input.Source {
	case "":
		input.Source = skillSourceFromHost(parsed.Hostname())
	case "github", "skillhub", "clawhub":
	default:
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "invalid_skill")
		return
	}
	// 检查、云端提交和成功计数串行执行，避免并发请求突破每日上限。
	s.skillQuotaMu.Lock()
	defer s.skillQuotaMu.Unlock()
	if err := s.loadSkillQuota(time.Now()); err != nil {
		applog.Error("读取技能每日提交次数失败", "error", err)
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "skill_quota_unavailable")
		return
	}
	if s.skillQuota.Count >= skillDailyLimit {
		i18n.Error(c, http.StatusTooManyRequests, "localserver_skill_daily_limit", "skill_daily_limit")
		return
	}
	// 提交前确认计数可持久化；上次落盘失败时也先补存，避免继续提交。
	if err := s.saveSkillQuota(); err != nil {
		applog.Error("保存技能每日提交次数失败", "error", err)
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "skill_quota_unavailable")
		return
	}
	result, err := s.skillClient().CreateSkill(c.Request.Context(), input, i18n.FromContext(c))
	if err != nil {
		writeSkillError(c, err)
		return
	}
	// 跨零点完成的请求计入成功时所在的日期。
	if date := time.Now().Format("2006-01-02"); s.skillQuota.Date != date {
		s.skillQuota = &skillDailyQuota{Date: date}
	}
	s.skillQuota.Count++
	if err := s.saveSkillQuota(); err != nil {
		// 云端已成功，不能将落盘错误误报为提交失败并诱导用户重复提交。
		applog.Error("技能提交成功但每日次数落盘失败", "error", err)
	}
	c.JSON(http.StatusOK, result)
}

func skillSourceFromHost(host string) string {
	switch strings.TrimSuffix(strings.ToLower(host), ".") {
	case "skillhub.cn", "www.skillhub.cn", "skillhub.tencent.com":
		return "skillhub"
	case "clawhub.ai", "www.clawhub.ai":
		return "clawhub"
	default:
		return "github"
	}
}

func writeSkillError(c *gin.Context, err error) {
	var apiErr *cloud.APIError
	if errors.As(err, &apiErr) && c.Request.Method == http.MethodPost && (apiErr.StatusCode == http.StatusConflict || apiErr.Code == "409") {
		i18n.Error(c, http.StatusConflict, "localserver_skill_source_exists", "skill_source_exists")
		return
	}
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusBadRequest || apiErr.Code == "400") {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "invalid_skill")
		return
	}
	i18n.Error(c, http.StatusBadGateway, "common_server_error", "skill_service_unavailable")
}
