package expertgroup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"goteams-client/internal/executor"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RoleLeader = "leader"
	RoleMember = "member"
)

var (
	ErrNotFound          = errors.New("专家团不存在")
	ErrMemberNotFound    = errors.New("专家团成员不存在")
	ErrNotReady          = errors.New("专家团配置不完整")
	ErrInvalidBatchInput = errors.New("专家团批量配置参数无效")
)

type Member struct {
	UUID            string `json:"uuid"`
	ExpertGroupUUID string `json:"expert_group_uuid"`
	MemberRole      string `json:"member_role"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Avatar          string `json:"avatar"`
	Prompt          string `json:"prompt"`
	CLIType         string `json:"cli_type"`
	ModelName       string `json:"model_name"`
	ThinkingLevel   string `json:"thinking_level"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type Group struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	Ready       bool   `json:"ready"`
	// SourceType marks read-only cloud 团队资源 ("cloud"); empty = local 私有资源.
	SourceType string   `json:"source_type,omitempty"`
	Leader     *Member  `json:"leader,omitempty"`
	Members    []Member `json:"members"`
}

type GroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
}

type MemberInput struct {
	MemberRole    string                      `json:"member_role"`
	Name          string                      `json:"name"`
	Description   string                      `json:"description"`
	Avatar        string                      `json:"avatar"`
	Prompt        string                      `json:"prompt"`
	CLIType       string                      `json:"cli_type"`
	ModelName     string                      `json:"model_name"`
	ThinkingLevel executor.ThinkingLevelInput `json:"thinking_level"`
}

type BatchMemberExecutionInput struct {
	MemberUUIDs   []string                    `json:"member_uuids"`
	CLIType       string                      `json:"cli_type"`
	ModelName     string                      `json:"model_name"`
	ThinkingLevel executor.ThinkingLevelInput `json:"thinking_level"`
}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) List(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid, name, description, avatar, created_at, updated_at
		FROM gt_expert_groups ORDER BY created_at, rowid`)
	if err != nil {
		return nil, fmt.Errorf("查询专家团失败: %w", err)
	}
	items := make([]Group, 0)
	for rows.Next() {
		var item Group
		if err := rows.Scan(&item.UUID, &item.Name, &item.Description, &item.Avatar, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取专家团失败: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		if err := s.loadMembers(ctx, &items[index]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, groupUUID string) (*Group, error) {
	groupUUID = strings.TrimSpace(groupUUID)
	// Cloud groups are listed from the in-memory cache. Switching a task must
	// resolve the same cache; they are never written to gt_expert_groups.
	if cloudGroup, ok := DefaultCloudStore().Get(groupUUID); ok {
		item := cloudGroup.ToGroup()
		return &item, nil
	}
	var item Group
	err := s.db.QueryRowContext(ctx, `SELECT uuid, name, description, avatar, created_at, updated_at
		FROM gt_expert_groups WHERE uuid=?`, groupUUID).Scan(
		&item.UUID, &item.Name, &item.Description, &item.Avatar, &item.CreatedAt, &item.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询专家团失败: %w", err)
	}
	if err := s.loadMembers(ctx, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Service) Create(ctx context.Context, input GroupInput) (*Group, error) {
	if err := validateGroupInput(input); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	id := uuid.NewString()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO gt_expert_groups
		(uuid, name, description, avatar, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), now, now); err != nil {
		return nil, fmt.Errorf("创建专家团失败: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, groupUUID string, input GroupInput) (*Group, error) {
	if err := validateGroupInput(input); err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gt_expert_groups SET name=?, description=?, avatar=?, updated_at=? WHERE uuid=?`,
		strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), time.Now().UnixMilli(), strings.TrimSpace(groupUUID))
	if err != nil {
		return nil, fmt.Errorf("更新专家团失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, groupUUID)
}

func (s *Service) Copy(ctx context.Context, groupUUID string) (*Group, error) {
	source, err := s.Get(ctx, groupUUID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	newUUID := uuid.NewString()
	name := nextCopyName(source.Name)
	if _, err = tx.ExecContext(ctx, `INSERT INTO gt_expert_groups
		(uuid, name, description, avatar, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		newUUID, name, source.Description, source.Avatar, now, now); err != nil {
		return nil, fmt.Errorf("复制专家团失败: %w", err)
	}
	all := make([]Member, 0, len(source.Members)+1)
	if source.Leader != nil {
		all = append(all, *source.Leader)
	}
	all = append(all, source.Members...)
	for _, member := range all {
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_expert_group_members
			(uuid, expert_group_uuid, member_role, name, description, avatar, prompt, cli_type, model_name, thinking_level, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), newUUID, member.MemberRole,
			member.Name, member.Description, member.Avatar, member.Prompt, member.CLIType, member.ModelName, member.ThinkingLevel, now, now); err != nil {
			return nil, fmt.Errorf("复制专家团成员失败: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, newUUID)
}

func (s *Service) Delete(ctx context.Context, groupUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM gt_expert_group_members WHERE expert_group_uuid=?`, groupUUID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM gt_expert_groups WHERE uuid=?`, groupUUID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Service) AddMember(ctx context.Context, groupUUID string, input MemberInput) (*Member, error) {
	if err := executor.ValidateThinkingLevel(ctx, input.CLIType, input.ModelName, "", input.ThinkingLevel.Value); err != nil {
		return nil, err
	}
	if err := validateMemberInput(input); err != nil {
		return nil, err
	}
	if _, err := s.Get(ctx, groupUUID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	role := normalizeRole(input.MemberRole)
	if role == RoleLeader {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_expert_group_members SET member_role='member', updated_at=?
			WHERE expert_group_uuid=? AND member_role='leader'`, time.Now().UnixMilli(), groupUUID); err != nil {
			return nil, err
		}
	}
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gt_expert_group_members
		(uuid, expert_group_uuid, member_role, name, description, avatar, prompt, cli_type, model_name, thinking_level, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, groupUUID, role, strings.TrimSpace(input.Name),
		strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), strings.TrimSpace(input.Prompt),
		strings.TrimSpace(input.CLIType), strings.TrimSpace(input.ModelName), input.ThinkingLevel.Value, now, now); err != nil {
		return nil, fmt.Errorf("新增专家团成员失败: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_expert_groups SET updated_at=? WHERE uuid=?`, now, groupUUID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetMember(ctx, groupUUID, id)
}

func (s *Service) UpdateMember(ctx context.Context, groupUUID, memberUUID string, input MemberInput) (*Member, error) {
	group, err := s.Get(ctx, groupUUID)
	if err != nil {
		return nil, err
	}
	var previous *Member
	if group.Leader != nil && group.Leader.UUID == memberUUID {
		previous = group.Leader
	}
	for i := range group.Members {
		if group.Members[i].UUID == memberUUID {
			previous = &group.Members[i]
			break
		}
	}
	if previous == nil {
		return nil, ErrMemberNotFound
	}
	level := executor.ResolveThinkingLevel(input.ThinkingLevel, input.CLIType, input.ModelName, previous.CLIType, previous.ModelName, previous.ThinkingLevel)
	if input.ThinkingLevel.Present || level != previous.ThinkingLevel || input.CLIType != previous.CLIType || input.ModelName != previous.ModelName {
		if err := executor.ValidateThinkingLevel(ctx, input.CLIType, input.ModelName, "", level); err != nil {
			return nil, err
		}
	}
	input.ThinkingLevel.Value = level
	// 云端团队专家团：仅允许本地配置执行方式（CLI/模型），编排字段由云端管理。
	if _, ok := DefaultCloudStore().Get(groupUUID); ok {
		return DefaultCloudStore().UpdateMemberExecution(groupUUID, memberUUID, input.CLIType, input.ModelName, input.ThinkingLevel)
	}
	if err := validateMemberInput(input); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	role := normalizeRole(input.MemberRole)
	if role == RoleLeader {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_expert_group_members SET member_role='member', updated_at=?
			WHERE expert_group_uuid=? AND member_role='leader' AND uuid<>?`, time.Now().UnixMilli(), groupUUID, memberUUID); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE gt_expert_group_members SET member_role=?, name=?, description=?, avatar=?, prompt=?, cli_type=?, model_name=?, thinking_level=?, updated_at=?
		WHERE uuid=? AND expert_group_uuid=?`, role, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description),
		strings.TrimSpace(input.Avatar), strings.TrimSpace(input.Prompt), strings.TrimSpace(input.CLIType), strings.TrimSpace(input.ModelName), level,
		time.Now().UnixMilli(), memberUUID, groupUUID)
	if err != nil {
		return nil, fmt.Errorf("更新专家团成员失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return nil, ErrMemberNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_expert_groups SET updated_at=? WHERE uuid=?`, time.Now().UnixMilli(), groupUUID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetMember(ctx, groupUUID, memberUUID)
}

// BatchUpdateMemberExecution applies the same CLI and model to the selected
// leader and members. It validates every target before changing any row.
func (s *Service) BatchUpdateMemberExecution(ctx context.Context, groupUUID string, input BatchMemberExecutionInput) (*Group, error) {
	group, err := s.Get(ctx, groupUUID)
	if err != nil {
		return nil, err
	}
	members := append([]Member{}, group.Members...)
	if group.Leader != nil {
		members = append(members, *group.Leader)
	}
	levels := make(map[string]string)
	for _, member := range members {
		level := executor.ResolveThinkingLevel(input.ThinkingLevel, input.CLIType, input.ModelName, member.CLIType, member.ModelName, member.ThinkingLevel)
		for _, id := range input.MemberUUIDs {
			if id != member.UUID {
				continue
			}
			if err := executor.ValidateThinkingLevel(ctx, input.CLIType, input.ModelName, "", level); err != nil {
				return nil, err
			}
			levels[id] = level
		}
	}
	groupUUID = strings.TrimSpace(groupUUID)
	input.CLIType = strings.TrimSpace(input.CLIType)
	input.ModelName = strings.TrimSpace(input.ModelName)
	if groupUUID == "" {
		return nil, ErrNotFound
	}
	if input.CLIType == "" || input.ModelName == "" || len(input.MemberUUIDs) == 0 {
		return nil, ErrInvalidBatchInput
	}

	memberUUIDs := make([]string, 0, len(input.MemberUUIDs))
	selected := make(map[string]struct{}, len(input.MemberUUIDs))
	for _, rawUUID := range input.MemberUUIDs {
		memberUUID := strings.TrimSpace(rawUUID)
		if memberUUID == "" {
			return nil, ErrInvalidBatchInput
		}
		if _, exists := selected[memberUUID]; exists {
			return nil, ErrInvalidBatchInput
		}
		selected[memberUUID] = struct{}{}
		memberUUIDs = append(memberUUIDs, memberUUID)
	}

	// 云端专家团只存在于内存缓存，不在 gt_expert_groups。单个成员配置已走这条路径，
	// 批量配置必须同样先命中缓存，否则会误报「专家团不存在」。
	if _, ok := DefaultCloudStore().Get(groupUUID); ok {
		return DefaultCloudStore().BatchUpdateMemberExecution(groupUUID, memberUUIDs, input.CLIType, input.ModelName, input.ThinkingLevel)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启专家团批量配置事务失败: %w", err)
	}
	defer tx.Rollback()

	var groupExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM gt_expert_groups WHERE uuid=?`, groupUUID).Scan(&groupExists); err != nil {
		return nil, fmt.Errorf("读取专家团失败: %w", err)
	}
	if groupExists == 0 {
		return nil, ErrNotFound
	}

	rows, err := tx.QueryContext(ctx, `SELECT uuid FROM gt_expert_group_members WHERE expert_group_uuid=?`, groupUUID)
	if err != nil {
		return nil, fmt.Errorf("读取专家团成员失败: %w", err)
	}
	available := make(map[string]struct{})
	for rows.Next() {
		var memberUUID string
		if err := rows.Scan(&memberUUID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取专家团成员失败: %w", err)
		}
		available[memberUUID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历专家团成员失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭专家团成员查询失败: %w", err)
	}
	for _, memberUUID := range memberUUIDs {
		if _, exists := available[memberUUID]; !exists {
			return nil, ErrMemberNotFound
		}
	}

	now := time.Now().UnixMilli()
	for _, memberUUID := range memberUUIDs {
		result, err := tx.ExecContext(ctx, `UPDATE gt_expert_group_members
			SET cli_type=?, model_name=?, thinking_level=?, updated_at=? WHERE uuid=? AND expert_group_uuid=?`,
			input.CLIType, input.ModelName, levels[memberUUID], now, memberUUID, groupUUID)
		if err != nil {
			return nil, fmt.Errorf("批量配置专家团成员失败: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return nil, fmt.Errorf("读取专家团批量配置结果失败: %w", err)
			}
			return nil, ErrMemberNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gt_expert_groups SET updated_at=? WHERE uuid=?`, now, groupUUID); err != nil {
		return nil, fmt.Errorf("更新专家团时间失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交专家团批量配置事务失败: %w", err)
	}
	return s.Get(ctx, groupUUID)
}

func (s *Service) DeleteMember(ctx context.Context, groupUUID, memberUUID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gt_expert_group_members WHERE uuid=? AND expert_group_uuid=?`, memberUUID, groupUUID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrMemberNotFound
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE gt_expert_groups SET updated_at=? WHERE uuid=?`, time.Now().UnixMilli(), groupUUID)
	return nil
}

func (s *Service) GetMember(ctx context.Context, groupUUID, memberUUID string) (*Member, error) {
	var item Member
	err := s.db.QueryRowContext(ctx, `SELECT uuid, expert_group_uuid, member_role, name, description, avatar, prompt, cli_type, model_name, thinking_level, created_at, updated_at
		FROM gt_expert_group_members WHERE uuid=? AND expert_group_uuid=?`, memberUUID, groupUUID).Scan(
		&item.UUID, &item.ExpertGroupUUID, &item.MemberRole, &item.Name, &item.Description, &item.Avatar,
		&item.Prompt, &item.CLIType, &item.ModelName, &item.ThinkingLevel, &item.CreatedAt, &item.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrMemberNotFound
	}
	return &item, err
}

func (s *Service) ValidateReady(ctx context.Context, groupUUID string) (*Group, error) {
	item, err := s.Get(ctx, groupUUID)
	if err != nil {
		return nil, err
	}
	if !item.Ready {
		return nil, ErrNotReady
	}
	return item, nil
}

func (s *Service) loadMembers(ctx context.Context, item *Group) error {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid, expert_group_uuid, member_role, name, description, avatar, prompt, cli_type, model_name, thinking_level, created_at, updated_at
		FROM gt_expert_group_members WHERE expert_group_uuid=?
		ORDER BY CASE member_role WHEN 'leader' THEN 0 ELSE 1 END, created_at, rowid`, item.UUID)
	if err != nil {
		return fmt.Errorf("查询专家团成员失败: %w", err)
	}
	defer rows.Close()
	item.Members = make([]Member, 0)
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.UUID, &member.ExpertGroupUUID, &member.MemberRole, &member.Name, &member.Description,
			&member.Avatar, &member.Prompt, &member.CLIType, &member.ModelName, &member.ThinkingLevel, &member.CreatedAt, &member.UpdatedAt); err != nil {
			return err
		}
		if member.MemberRole == RoleLeader {
			copy := member
			item.Leader = &copy
		} else {
			item.Members = append(item.Members, member)
		}
	}
	item.Ready = item.Leader != nil && len(item.Members) > 0 && memberReady(*item.Leader)
	for _, member := range item.Members {
		item.Ready = item.Ready && memberReady(member)
	}
	return rows.Err()
}

func validateGroupInput(input GroupInput) error {
	name := []rune(strings.TrimSpace(input.Name))
	if len(name) == 0 {
		return fmt.Errorf("专家团名称不能为空")
	}
	if len(name) > 20 {
		return fmt.Errorf("专家团名称不能超过20个字符")
	}
	if len([]rune(strings.TrimSpace(input.Description))) > 200 {
		return fmt.Errorf("专家团简介不能超过200个字符")
	}
	return nil
}

func validateMemberInput(input MemberInput) error {
	if role := normalizeRole(input.MemberRole); role != RoleLeader && role != RoleMember {
		return fmt.Errorf("无效专家团成员角色")
	}
	if len([]rune(strings.TrimSpace(input.Name))) == 0 {
		return fmt.Errorf("Agent 名称不能为空")
	}
	if len([]rune(strings.TrimSpace(input.Name))) > 20 {
		return fmt.Errorf("Agent 名称不能超过20个字符")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("Agent 提示词不能为空")
	}
	if strings.TrimSpace(input.CLIType) == "" || strings.TrimSpace(input.ModelName) == "" {
		return fmt.Errorf("Agent 必须配置 CLI 和模型")
	}
	return nil
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return RoleMember
	}
	return role
}

func memberReady(member Member) bool {
	return strings.TrimSpace(member.Prompt) != "" && strings.TrimSpace(member.CLIType) != "" && strings.TrimSpace(member.ModelName) != ""
}

func nextCopyName(name string) string {
	name = strings.TrimSpace(name)
	if len([]rune(name))+2 <= 20 {
		return name + " 2"
	}
	runes := []rune(name)
	return string(runes[:18]) + " 2"
}
