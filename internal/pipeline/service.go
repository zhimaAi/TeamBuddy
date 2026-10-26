package pipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"goteams-client/internal/cloudscope"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("流水线不存在")
	ErrStepNotFound = errors.New("Agent 编排不存在")
)

type Step struct {
	UUID         string `json:"uuid"`
	PipelineUUID string `json:"pipeline_uuid"`
	SortOrder    int    `json:"sort_order"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Avatar       string `json:"avatar"`
	Prompt       string `json:"prompt"`
	CLIType      string `json:"cli_type"`
	ModelName    string `json:"model_name"`
	CloudStepID  string `json:"cloud_step_id"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type Pipeline struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Avatar          string `json:"avatar"`
	SourceType      string `json:"source_type"`
	CloudPipelineID string `json:"cloud_pipeline_id"`
	CloudSourceKey  string `json:"cloud_source_key"`
	CloudSyncedAt   int64  `json:"cloud_synced_at"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	Steps           []Step `json:"steps"`
}

type PipelineInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
}

type StepInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
	Prompt      string `json:"prompt"`
	CLIType     string `json:"cli_type"`
	ModelName   string `json:"model_name"`
}

type BatchStepExecutionInput struct {
	StepUUIDs []string `json:"step_uuids"`
	CLIType   string   `json:"cli_type"`
	ModelName string   `json:"model_name"`
}

type CloudPipeline struct {
	CloudPipelineID string
	Name            string
	Description     string
	Avatar          string
	Steps           []CloudStep
}

type CloudStep struct {
	CloudStepID string
	SortOrder   int
	Name        string
	Description string
	Avatar      string
	Prompt      string
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) List(ctx context.Context) ([]Pipeline, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uuid, name, description, avatar, source_type, cloud_pipeline_id, cloud_source_key, cloud_synced_at, created_at, updated_at
		FROM gt_pipelines
		WHERE source_type = 'local'
		ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询流水线失败: %w", err)
	}

	items := make([]Pipeline, 0)
	for rows.Next() {
		var item Pipeline
		if err := rows.Scan(&item.UUID, &item.Name, &item.Description, &item.Avatar, &item.SourceType, &item.CloudPipelineID, &item.CloudSourceKey, &item.CloudSyncedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取流水线失败: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历流水线失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭流水线查询失败: %w", err)
	}
	// The SQLite manager intentionally uses one connection. Load child rows only
	// after the parent result set has been fully consumed and closed.
	for index := range items {
		steps, err := s.listSteps(ctx, items[index].UUID)
		if err != nil {
			return nil, err
		}
		items[index].Steps = steps
	}
	items = append(items, DefaultStore().List()...)
	return items, nil
}

func (s *Service) Get(ctx context.Context, pipelineUUID string) (*Pipeline, error) {
	if item, ok := DefaultStore().Get(pipelineUUID); ok {
		return item, nil
	}
	var item Pipeline
	err := s.db.QueryRowContext(ctx, `
		SELECT uuid, name, description, avatar, source_type, cloud_pipeline_id, cloud_source_key, cloud_synced_at, created_at, updated_at
		FROM gt_pipelines WHERE uuid = ? AND source_type = 'local'`, pipelineUUID).Scan(
		&item.UUID, &item.Name, &item.Description, &item.Avatar, &item.SourceType, &item.CloudPipelineID, &item.CloudSourceKey, &item.CloudSyncedAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询流水线失败: %w", err)
	}
	item.Steps, err = s.listSteps(ctx, pipelineUUID)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Service) Create(ctx context.Context, input PipelineInput) (*Pipeline, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, fmt.Errorf("流水线名称不能为空")
	}
	now := time.Now().UnixMilli()
	id := uuid.NewString()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gt_pipelines (uuid, name, description, avatar, source_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'local', ?, ?)`, id, input.Name, strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), now, now)
	if err != nil {
		return nil, fmt.Errorf("创建流水线失败: %w", err)
	}
	return s.Get(ctx, id)
}

var pipelineNumberSuffix = regexp.MustCompile(`\s+[0-9]+$`)

// Copy creates a local, independently editable copy of either a local or a
// cloud pipeline. The generated suffix follows the product rule: strip an
// existing numeric suffix, count pipelines with the same base name, then use
// count + 1.
func (s *Service) Copy(ctx context.Context, pipelineUUID string) (*Pipeline, error) {
	source, err := s.Get(ctx, strings.TrimSpace(pipelineUUID))
	if err != nil {
		return nil, err
	}
	baseName := pipelineBaseName(source.Name)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启复制事务失败: %w", err)
	}
	defer tx.Rollback()

	matchingNames := 0
	rows, err := tx.QueryContext(ctx, `SELECT name FROM gt_pipelines WHERE source_type = 'local'`)
	if err != nil {
		return nil, fmt.Errorf("读取流水线名称失败: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取流水线名称失败: %w", err)
		}
		if pipelineBaseName(name) == baseName {
			matchingNames++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历流水线名称失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭流水线名称查询失败: %w", err)
	}
	for _, item := range DefaultStore().List() {
		if pipelineBaseName(item.Name) == baseName {
			matchingNames++
		}
	}

	now := time.Now().UnixMilli()
	copiedUUID := uuid.NewString()
	copiedName := fmt.Sprintf("%s %d", baseName, matchingNames+1)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gt_pipelines (uuid, name, description, avatar, source_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'local', ?, ?)`, copiedUUID, copiedName, source.Description, source.Avatar, now, now); err != nil {
		return nil, fmt.Errorf("复制流水线失败: %w", err)
	}
	for _, step := range source.Steps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gt_pipeline_steps
			(uuid, pipeline_uuid, sort_order, name, description, avatar, prompt, cli_type, model_name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), copiedUUID, step.SortOrder, step.Name, step.Description, step.Avatar,
			step.Prompt, step.CLIType, step.ModelName, now, now); err != nil {
			return nil, fmt.Errorf("复制 Agent 编排失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交复制事务失败: %w", err)
	}
	return s.Get(ctx, copiedUUID)
}

func pipelineBaseName(name string) string {
	trimmed := strings.TrimSpace(name)
	baseName := strings.TrimSpace(pipelineNumberSuffix.ReplaceAllString(trimmed, ""))
	if baseName == "" {
		return trimmed
	}
	return baseName
}

func (s *Service) Update(ctx context.Context, pipelineUUID string, input PipelineInput) (*Pipeline, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, fmt.Errorf("流水线名称不能为空")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE gt_pipelines SET name = ?, description = ?, avatar = ?, updated_at = ?
		WHERE uuid = ? AND source_type = 'local'`,
		input.Name, strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), time.Now().UnixMilli(), pipelineUUID)
	if err != nil {
		return nil, fmt.Errorf("更新流水线失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, pipelineUUID)
}

func (s *Service) Delete(ctx context.Context, pipelineUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启删除事务失败: %w", err)
	}
	defer tx.Rollback()
	var localPipeline int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_pipelines WHERE uuid = ? AND source_type = 'local'`, pipelineUUID).Scan(&localPipeline); err != nil {
		return fmt.Errorf("检查流水线来源失败: %w", err)
	}
	if localPipeline == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gt_pipeline_steps WHERE pipeline_uuid = ?`, pipelineUUID); err != nil {
		return fmt.Errorf("删除 Agent 编排失败: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM gt_pipelines WHERE uuid = ? AND source_type = 'local'`, pipelineUUID)
	if err != nil {
		return fmt.Errorf("删除流水线失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交删除事务失败: %w", err)
	}
	return nil
}

func (s *Service) AddStep(ctx context.Context, pipelineUUID string, input StepInput) (*Step, error) {
	if err := validateStepInput(input); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_pipelines WHERE uuid = ? AND source_type = 'local'`, pipelineUUID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	var order int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM gt_pipeline_steps WHERE pipeline_uuid = ?`, pipelineUUID).Scan(&order); err != nil {
		return nil, fmt.Errorf("读取 Agent 编排顺序失败: %w", err)
	}
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO gt_pipeline_steps
		(uuid, pipeline_uuid, sort_order, name, description, avatar, prompt, cli_type, model_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, pipelineUUID, order, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar),
		strings.TrimSpace(input.Prompt), strings.TrimSpace(input.CLIType), strings.TrimSpace(input.ModelName), now, now)
	if err != nil {
		return nil, fmt.Errorf("创建 Agent 编排失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}
	return s.getStep(ctx, pipelineUUID, id)
}

func (s *Service) UpdateStep(ctx context.Context, pipelineUUID, stepUUID string, input StepInput) (*Step, error) {
	if _, ok := DefaultStore().Get(pipelineUUID); ok {
		step, err := DefaultStore().UpdateStep(pipelineUUID, stepUUID, input.CLIType, input.ModelName)
		if err != nil {
			return nil, err
		}
		if err = s.saveCloudStepExecution(ctx, pipelineUUID, step.UUID, step.CLIType, step.ModelName); err != nil {
			return nil, err
		}
		return step, nil
	}
	var sourceType string
	err := s.db.QueryRowContext(ctx, `SELECT source_type FROM gt_pipelines WHERE uuid = ?`, pipelineUUID).Scan(&sourceType)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取流水线来源失败: %w", err)
	}
	if sourceType == "cloud" {
		input.CLIType = strings.TrimSpace(input.CLIType)
		input.ModelName = strings.TrimSpace(input.ModelName)
		if input.CLIType == "" || input.ModelName == "" {
			return nil, fmt.Errorf("云端 Agent 编排必须配置 CLI 和模型")
		}
		result, updateErr := s.db.ExecContext(ctx, `
			UPDATE gt_pipeline_steps SET cli_type = ?, model_name = ?, updated_at = ?
			WHERE uuid = ? AND pipeline_uuid = ?`, input.CLIType, input.ModelName, time.Now().UnixMilli(), stepUUID, pipelineUUID)
		if updateErr != nil {
			return nil, fmt.Errorf("更新云端 Agent 编排执行配置失败: %w", updateErr)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return nil, ErrStepNotFound
		}
		return s.getStep(ctx, pipelineUUID, stepUUID)
	}
	if err := validateStepInput(input); err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE gt_pipeline_steps
		SET name = ?, description = ?, avatar = ?, prompt = ?, cli_type = ?, model_name = ?, updated_at = ?
		WHERE uuid = ? AND pipeline_uuid = ?
		  AND EXISTS (SELECT 1 FROM gt_pipelines p WHERE p.uuid = gt_pipeline_steps.pipeline_uuid AND p.source_type = 'local')`,
		strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), strings.TrimSpace(input.Avatar), strings.TrimSpace(input.Prompt),
		strings.TrimSpace(input.CLIType), strings.TrimSpace(input.ModelName), time.Now().UnixMilli(), stepUUID, pipelineUUID)
	if err != nil {
		return nil, fmt.Errorf("更新 Agent 编排失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrStepNotFound
	}
	return s.getStep(ctx, pipelineUUID, stepUUID)
}

// BatchUpdateStepExecution validates every target before applying the same CLI
// and model to all selected steps. Local pipelines use one SQLite transaction;
// cloud pipelines use the MemStore's atomic write lock.
func (s *Service) BatchUpdateStepExecution(ctx context.Context, pipelineUUID string, input BatchStepExecutionInput) (*Pipeline, error) {
	pipelineUUID = strings.TrimSpace(pipelineUUID)
	input.CLIType = strings.TrimSpace(input.CLIType)
	input.ModelName = strings.TrimSpace(input.ModelName)
	if pipelineUUID == "" {
		return nil, ErrNotFound
	}
	if input.CLIType == "" {
		return nil, fmt.Errorf("CLI 不能为空")
	}
	if input.ModelName == "" {
		return nil, fmt.Errorf("模型不能为空")
	}
	if len(input.StepUUIDs) == 0 {
		return nil, fmt.Errorf("请至少选择一个 Agent")
	}

	stepUUIDs := make([]string, 0, len(input.StepUUIDs))
	selected := make(map[string]struct{}, len(input.StepUUIDs))
	for _, rawUUID := range input.StepUUIDs {
		stepUUID := strings.TrimSpace(rawUUID)
		if stepUUID == "" {
			return nil, fmt.Errorf("Agent UUID 不能为空")
		}
		if _, exists := selected[stepUUID]; exists {
			return nil, fmt.Errorf("Agent UUID 不能重复")
		}
		selected[stepUUID] = struct{}{}
		stepUUIDs = append(stepUUIDs, stepUUID)
	}

	if _, ok := DefaultStore().Get(pipelineUUID); ok {
		updated, err := DefaultStore().BatchUpdateStepExecution(pipelineUUID, stepUUIDs, input.CLIType, input.ModelName)
		if err != nil {
			return nil, err
		}
		selected := make(map[string]struct{}, len(stepUUIDs))
		for _, stepUUID := range stepUUIDs {
			selected[stepUUID] = struct{}{}
		}
		for _, step := range updated.Steps {
			if _, keep := selected[step.UUID]; !keep {
				continue
			}
			if err = s.saveCloudStepExecution(ctx, pipelineUUID, step.UUID, step.CLIType, step.ModelName); err != nil {
				return nil, err
			}
		}
		return updated, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启批量配置事务失败: %w", err)
	}
	defer tx.Rollback()

	var pipelineExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM gt_pipelines WHERE uuid = ? AND source_type = 'local'`, pipelineUUID).Scan(&pipelineExists); err != nil {
		return nil, fmt.Errorf("读取流水线失败: %w", err)
	}
	if pipelineExists == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT uuid FROM gt_pipeline_steps WHERE pipeline_uuid = ?`, pipelineUUID)
	if err != nil {
		return nil, fmt.Errorf("读取 Agent 编排失败: %w", err)
	}
	available := make(map[string]struct{})
	for rows.Next() {
		var stepUUID string
		if err := rows.Scan(&stepUUID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取 Agent 编排失败: %w", err)
		}
		available[stepUUID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历 Agent 编排失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("关闭 Agent 编排查询失败: %w", err)
	}
	for _, stepUUID := range stepUUIDs {
		if _, exists := available[stepUUID]; !exists {
			return nil, fmt.Errorf("Agent 不属于当前流水线: %s", stepUUID)
		}
	}

	now := time.Now().UnixMilli()
	for _, stepUUID := range stepUUIDs {
		result, err := tx.ExecContext(ctx, `
			UPDATE gt_pipeline_steps SET cli_type = ?, model_name = ?, updated_at = ?
			WHERE uuid = ? AND pipeline_uuid = ?`, input.CLIType, input.ModelName, now, stepUUID, pipelineUUID)
		if err != nil {
			return nil, fmt.Errorf("批量配置 Agent 失败: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return nil, fmt.Errorf("读取批量配置结果失败: %w", err)
			}
			return nil, fmt.Errorf("Agent 已不存在: %s", stepUUID)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gt_pipelines SET updated_at = ? WHERE uuid = ?`, now, pipelineUUID); err != nil {
		return nil, fmt.Errorf("更新流水线时间失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交批量配置事务失败: %w", err)
	}
	return s.Get(ctx, pipelineUUID)
}

func (s *Service) DeleteStep(ctx context.Context, pipelineUUID, stepUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM gt_pipeline_steps WHERE uuid = ? AND pipeline_uuid = ?
		AND EXISTS (SELECT 1 FROM gt_pipelines p WHERE p.uuid = gt_pipeline_steps.pipeline_uuid AND p.source_type = 'local')`, stepUUID, pipelineUUID)
	if err != nil {
		return fmt.Errorf("删除 Agent 编排失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrStepNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		WITH ordered AS (
			SELECT uuid, ROW_NUMBER() OVER (ORDER BY sort_order, created_at, rowid) AS new_order
			FROM gt_pipeline_steps WHERE pipeline_uuid = ?
		)
		UPDATE gt_pipeline_steps SET sort_order = (SELECT new_order FROM ordered WHERE ordered.uuid = gt_pipeline_steps.uuid)
		WHERE pipeline_uuid = ?`, pipelineUUID, pipelineUUID); err != nil {
		return fmt.Errorf("重排 Agent 编排失败: %w", err)
	}
	return tx.Commit()
}

func (s *Service) ReorderSteps(ctx context.Context, pipelineUUID string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("Agent 编排顺序不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_pipeline_steps s JOIN gt_pipelines p ON p.uuid=s.pipeline_uuid
		WHERE s.pipeline_uuid = ? AND p.source_type='local'`, pipelineUUID).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("必须提交流水线的全部 Agent 编排")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("Agent 编排 ID 重复: %s", id)
		}
		seen[id] = struct{}{}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gt_pipeline_steps SET sort_order = -sort_order WHERE pipeline_uuid = ?`, pipelineUUID); err != nil {
		return fmt.Errorf("更新 Agent 编排顺序失败: %w", err)
	}
	for index, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE gt_pipeline_steps SET sort_order = ?, updated_at = ? WHERE uuid = ? AND pipeline_uuid = ?`, index+1, time.Now().UnixMilli(), id, pipelineUUID)
		if err != nil {
			return fmt.Errorf("更新 Agent 编排顺序失败: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return ErrStepNotFound
		}
	}
	return tx.Commit()
}

func (s *Service) listSteps(ctx context.Context, pipelineUUID string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uuid, pipeline_uuid, sort_order, name, description, avatar, prompt, cli_type, model_name, cloud_step_id, created_at, updated_at
		FROM gt_pipeline_steps WHERE pipeline_uuid = ? ORDER BY sort_order, created_at, rowid`, pipelineUUID)
	if err != nil {
		return nil, fmt.Errorf("查询 Agent 编排失败: %w", err)
	}
	defer rows.Close()
	items := make([]Step, 0)
	for rows.Next() {
		var item Step
		if err := rows.Scan(&item.UUID, &item.PipelineUUID, &item.SortOrder, &item.Name, &item.Description, &item.Avatar, &item.Prompt, &item.CLIType, &item.ModelName, &item.CloudStepID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取 Agent 编排失败: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) getStep(ctx context.Context, pipelineUUID, stepUUID string) (*Step, error) {
	var item Step
	err := s.db.QueryRowContext(ctx, `
		SELECT uuid, pipeline_uuid, sort_order, name, description, avatar, prompt, cli_type, model_name, cloud_step_id, created_at, updated_at
		FROM gt_pipeline_steps WHERE uuid = ? AND pipeline_uuid = ?`, stepUUID, pipelineUUID).Scan(
		&item.UUID, &item.PipelineUUID, &item.SortOrder, &item.Name, &item.Description, &item.Avatar, &item.Prompt,
		&item.CLIType, &item.ModelName, &item.CloudStepID, &item.CreatedAt, &item.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrStepNotFound
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// CloudSourceKey returns a stable, non-sensitive identity for a cloud endpoint.
func CloudSourceKey(baseURL string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))))
	return fmt.Sprintf("%x", sum[:16])
}

// CloudSourceKeys returns the key of baseURL and, when the URL is http or https,
// the key of the same host and path under the other scheme. A Windows package that
// only flips http and https must still find tasks bound that morning. Different
// hosts, including goteams.cn and goteams.ai, stay distinct.
func CloudSourceKeys(baseURL string) (primary, alternate string) {
	primary = CloudSourceKey(baseURL)
	variant := schemeVariantURL(baseURL)
	if variant == "" {
		return primary, primary
	}
	alternate = CloudSourceKey(variant)
	if alternate == "" {
		return primary, primary
	}
	return primary, alternate
}

func schemeVariantURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	lower := strings.ToLower(raw)
	const httpsScheme = "https://"
	const httpScheme = "http://"
	switch {
	case strings.HasPrefix(lower, httpsScheme):
		return httpScheme + raw[len(httpsScheme):]
	case strings.HasPrefix(lower, httpScheme):
		return httpsScheme + raw[len(httpScheme):]
	default:
		return ""
	}
}

// SyncCloud refreshes the in-memory cloud pipeline cache for the supplied cloud
// endpoint. The cloud payload does not include the CLI and model chosen on this
// client, so those values are restored from gt_cloud_pipeline_step_execution.
func (s *Service) SyncCloud(ctx context.Context, sourceKey string, items []CloudPipeline) ([]Pipeline, error) {
	pipes, err := DefaultStore().ReplaceAll(sourceKey, items, cacheWriteScope(ctx)...)
	if err != nil {
		return nil, err
	}
	for i := range pipes {
		if err = s.overlayCloudStepExecution(ctx, &pipes[i]); err != nil {
			return nil, err
		}
		DefaultStore().ApplyStepExecution(pipes[i].UUID, pipes[i].Steps)
	}
	return pipes, nil
}

func (s *Service) saveCloudStepExecution(ctx context.Context, pipelineUUID, stepUUID, cliType, modelName string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO gt_cloud_pipeline_step_execution
		(pipeline_uuid, step_uuid, cli_type, model_name, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(pipeline_uuid, step_uuid) DO UPDATE SET
			cli_type=excluded.cli_type, model_name=excluded.model_name, updated_at=excluded.updated_at`,
		strings.TrimSpace(pipelineUUID), strings.TrimSpace(stepUUID), strings.TrimSpace(cliType), strings.TrimSpace(modelName), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("保存云端流水线执行配置失败: %w", err)
	}
	return nil
}

func (s *Service) overlayCloudStepExecution(ctx context.Context, pipe *Pipeline) error {
	if s.db == nil || pipe == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT step_uuid, cli_type, model_name FROM gt_cloud_pipeline_step_execution WHERE pipeline_uuid=?`, pipe.UUID)
	if err != nil {
		return fmt.Errorf("读取云端流水线执行配置失败: %w", err)
	}
	defer rows.Close()
	saved := make(map[string]Step)
	for rows.Next() {
		var step Step
		if err = rows.Scan(&step.UUID, &step.CLIType, &step.ModelName); err != nil {
			return err
		}
		saved[step.UUID] = step
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range pipe.Steps {
		step, ok := saved[pipe.Steps[i].UUID]
		if !ok {
			continue
		}
		if strings.TrimSpace(step.CLIType) != "" {
			pipe.Steps[i].CLIType = step.CLIType
		}
		if strings.TrimSpace(step.ModelName) != "" {
			pipe.Steps[i].ModelName = step.ModelName
		}
	}
	return nil
}

// CleanupLegacyCloudPipelines removes cloud-sourced pipeline rows left by older
// versions that cached them in the local database. It runs at startup; the
// memory cache and the local-only list filter make a failure non-fatal.
func CleanupLegacyCloudPipelines(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启云端流水线清理事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM gt_pipeline_steps
		WHERE pipeline_uuid IN (SELECT uuid FROM gt_pipelines WHERE source_type = 'cloud')`); err != nil {
		return fmt.Errorf("清理云端流水线步骤失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gt_pipelines WHERE source_type = 'cloud'`); err != nil {
		return fmt.Errorf("清理云端流水线失败: %w", err)
	}
	return tx.Commit()
}

func validateStepInput(input StepInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return fmt.Errorf("Agent 编排名称不能为空")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return fmt.Errorf("Agent 编排提示词不能为空")
	}
	if strings.TrimSpace(input.CLIType) == "" {
		return fmt.Errorf("Agent 编排 CLI 不能为空")
	}
	if strings.TrimSpace(input.ModelName) == "" {
		return fmt.Errorf("Agent 编排模型不能为空")
	}
	return nil
}

func cacheWriteScope(ctx context.Context) []string {
	if scope, ok := cloudscope.From(ctx); ok {
		return []string{scope}
	}
	return nil
}
