package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Skill 是云端共享技能，不绑定客户端账号或工作空间。
type Skill struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Remark       string `json:"remark"`
	Icon         string `json:"icon"`
	Description  string `json:"description"`
	Sharer       string `json:"sharer"`
	SourceURL    string `json:"source_url"`
	Source       string `json:"source"`
	ReviewStatus string `json:"review_status"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type SkillPage struct {
	List  []Skill `json:"list"`
	Total int64   `json:"total"`
	Page  int     `json:"page"`
	Size  int     `json:"size"`
}

type CreateSkillInput struct {
	Name         string `json:"name"`
	Remark       string `json:"remark"`
	Icon         string `json:"icon"`
	Description  string `json:"description"`
	Sharer       string `json:"sharer"`
	SourceURL    string `json:"source_url"`
	Source       string `json:"source"`
	ReviewStatus string `json:"review_status"`
}

type CreateSkillResult struct {
	ID int64 `json:"id"`
}

func (c *Client) ListApprovedSkills(ctx context.Context, page, size int, name, locale string) (*SkillPage, error) {
	query := url.Values{"page": {strconv.Itoa(page)}, "size": {strconv.Itoa(size)}, "name": {name}, "review_status": {"approved"}}
	var result SkillPage
	if err := c.skillRequest(ctx, http.MethodGet, "/api/skills?"+query.Encode(), nil, &result, locale); err != nil {
		return nil, err
	}
	// 防御旧版服务忽略筛选参数；绝不向客户端暴露未审核记录。
	approved := make([]Skill, 0, len(result.List))
	for _, item := range result.List {
		if item.ReviewStatus == "approved" {
			approved = append(approved, item)
		}
	}
	result.List = approved
	return &result, nil
}

func (c *Client) CreateSkill(ctx context.Context, input CreateSkillInput, locale string) (*CreateSkillResult, error) {
	input.ReviewStatus = "pending"
	var result CreateSkillResult
	if err := c.skillRequest(ctx, http.MethodPost, "/api/skills", input, &result, locale); err != nil {
		return nil, err
	}
	return &result, nil
}

// 技能为公开接口，不能复用会注入账号、设备凭据的 do。
func (c *Client) skillRequest(ctx context.Context, method, path string, body interface{}, result interface{}, locale string) error {
	if !c.IsConfigured() {
		return fmt.Errorf("cloud API is not configured")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Close = true // 独立匿名客户端不保留每次请求的空闲连接。
	req.Header.Set("lang", locale)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}
	const maxResponse = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponse {
		return fmt.Errorf("skill response exceeds limit")
	}
	if err := decodeAPIResponse(resp.StatusCode, data, result); err != nil {
		return err
	}
	return nil
}
