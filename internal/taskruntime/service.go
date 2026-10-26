package taskruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"goteams-client/internal/cloudsync"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

var (
	ErrNotFound                        = errors.New("task not found")
	ErrTaskTitleAndDescriptionRequired = errors.New("任务标题与任务描述不能同时为空")
	ErrTaskContextLocked               = errors.New("任务已开始，不能修改流水线、关联项目或任务目录")
	ErrPipelineSnapshotLocked          = errors.New("任务已经分配流水线，不能覆盖永久快照")
	// ErrCloudUserIDRequired 关联线上的任务缺少账号归属：这类任务进不了上报队列。
	ErrCloudUserIDRequired = errors.New("关联线上的任务必须包含云端账号归属")
	// ErrCloudSourceKeyRequired 关联线上的任务缺少云端地址标识。
	ErrCloudSourceKeyRequired = errors.New("关联线上的任务必须包含云端地址标识")
)

const generatedTaskTitleMaxRunes = 20

type SnapshotStep struct {
	SourceStepID string `json:"source_step_id"`
	LocalStepID  string `json:"local_step_id"`
	CloudStepID  string `json:"cloud_step_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Avatar       string `json:"avatar"`
	Prompt       string `json:"prompt"`
	CLIType      string `json:"cli_type"`
	ModelName    string `json:"model_name"`
	SortOrder    int    `json:"sort_order"`
}

type Snapshot struct {
	SourceType       string         `json:"source_type"`
	SourcePipelineID string         `json:"source_pipeline_id"`
	CloudSourceKey   string         `json:"cloud_source_key"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Avatar           string         `json:"avatar"`
	Steps            []SnapshotStep `json:"steps"`
}

type UpdateInput struct {
	Title                string
	Content              string
	Priority             string
	PlannedStartDate     string
	PlannedEndDate       string
	ProjectUUID          string
	SubprojectUUIDs      []string
	WorkDirs             []string
	SelectedPipelineUUID string
}

type CreateInput struct {
	SourceType                 string
	CreatedFromConversation    bool
	CloudSourceKey             string
	CloudUserID                string
	WorkItemType               string
	WorkItemID                 string
	WorkspaceID                int64
	ProjectUUID                string
	SubprojectUUIDs            []string
	Title                      string
	Content                    string
	Priority                   string
	PlannedStartDate           string
	PlannedEndDate             string
	WorkDirs                   []string
	Status                     string
	SelectedPipelineUUID       string
	SelectedExpertGroupUUID    string
	SelectedPipelineConfigJSON string
	ExecutionMode              string
	ExecutionTool              string
	ExecutionModel             string
	// SyncAgentOutput 为指针：nil 表示沿用默认值（自动上报产出文件），
	// 只有显式 false 才关闭该任务的产出文件自动上报。
	SyncAgentOutput *bool
	Snapshot        Snapshot
}

type Service struct {
	db       *sql.DB
	taskRoot string
}

type AdvanceResult struct {
	NextStepUUID string `json:"next_step_uuid"`
	TaskDone     bool   `json:"task_done"`
}

func NewService(db *sql.DB, taskRoot string) *Service {
	return &Service{db: db, taskRoot: taskRoot}
}

func (s *Service) Create(ctx context.Context, in CreateInput) (taskUUID string, err error) {
	if s.db == nil {
		return "", fmt.Errorf("任务数据库未初始化")
	}
	in.Title, err = resolveCreateTaskTitle(in.Title, in.Content)
	if err != nil {
		return "", err
	}
	if in.SourceType == "" {
		in.SourceType = "local"
	}
	if in.SourceType != "local" && in.SourceType != "cloud" {
		return "", fmt.Errorf("无效任务来源: %s", in.SourceType)
	}
	// 关联线上的任务必须带账号归属与云端地址标识。缺其中任何一个，这条任务都进不了
	// 上报队列（cloudsync 按 cloud_source_key + cloud_user_id 过滤），线上会永久丢失沟通记录。
	if in.SourceType == "cloud" {
		if strings.TrimSpace(in.CloudUserID) == "" {
			return "", ErrCloudUserIDRequired
		}
		if strings.TrimSpace(in.CloudSourceKey) == "" {
			return "", ErrCloudSourceKeyRequired
		}
	}
	in.ExecutionMode = NormalizeExecutionMode(in.ExecutionMode)
	in.ExecutionTool = strings.ToLower(strings.TrimSpace(in.ExecutionTool))
	in.ExecutionModel = strings.TrimSpace(in.ExecutionModel)
	if in.ExecutionMode == ExecutionModeVibeCoding {
		if err := ValidateVibeModel(in.ExecutionTool, in.ExecutionModel); err != nil {
			return "", err
		}
	}
	if in.ExecutionMode == "" && strings.TrimSpace(in.SelectedPipelineUUID) != "" {
		in.ExecutionMode = ExecutionModePipeline
	}
	if err := ValidateExecutionTarget(in.ExecutionMode, in.ExecutionTool, in.SelectedPipelineUUID, in.SelectedExpertGroupUUID); err != nil {
		return "", err
	}
	if in.ExecutionMode == ExecutionModeCLI {
		if err := ValidateCLITarget(in.ExecutionTool, in.ExecutionModel); err != nil {
			return "", err
		}
	}
	hasSnapshot := strings.TrimSpace(in.Snapshot.SourcePipelineID) != "" || len(in.Snapshot.Steps) > 0
	if hasSnapshot {
		if in.Snapshot.SourceType == "" {
			in.Snapshot.SourceType = in.SourceType
		}
		if err := ValidateSnapshot(&in.Snapshot); err != nil {
			return "", err
		}
	}
	workDirs, projectIDs, err := s.resolveWorkDirs(ctx, in.ProjectUUID, in.SubprojectUUIDs, in.WorkDirs)
	if err != nil {
		return "", err
	}

	root, err := filepath.Abs(strings.TrimSpace(s.taskRoot))
	if err != nil || strings.TrimSpace(s.taskRoot) == "" {
		return "", fmt.Errorf("任务数据根目录无效")
	}
	if err = os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建任务数据根目录失败: %w", err)
	}
	taskUUID = uuid.New().String()
	taskDirID := uuid.New().String()
	taskDir := filepath.Join(root, taskDirID)
	if err = os.Mkdir(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("创建任务专属目录失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(taskDir)
		}
	}()
	taskMDPath := filepath.Join(taskDir, "task.md")
	if err = os.WriteFile(taskMDPath, []byte(renderTaskMarkdown(in.Title, in.Content)), 0o644); err != nil {
		return "", fmt.Errorf("写入 task.md 失败: %w", err)
	}

	stepDirs := make([]string, len(in.Snapshot.Steps))
	for i := range in.Snapshot.Steps {
		stepDirs[i] = filepath.Join(taskDir, fmt.Sprintf("%02d-%s-%s", i+1, safeDirName(in.Snapshot.Steps[i].Name), uuid.New().String()[:8]))
		if err = os.Mkdir(stepDirs[i], 0o755); err != nil {
			return "", fmt.Errorf("创建 Agent 编排专属目录失败: %w", err)
		}
	}

	now := time.Now().UnixMilli()
	status := strings.TrimSpace(in.Status)
	if status == "" || status == "todo" {
		status = "pending"
	}
	if status == "in_progress" {
		status = "active"
	}
	if status == "developing" {
		status = "active"
	}
	if status == "developed" || status == "completed" {
		status = "done"
	}
	if status != "pending" && status != "active" && status != "done" && status != "blocked" {
		return "", fmt.Errorf("无效任务状态: %s", status)
	}
	if status == "active" && in.ExecutionMode == ExecutionModePipeline && !hasSnapshot {
		return "", fmt.Errorf("任务启动前必须选择流水线")
	}
	if status == "active" && in.ExecutionMode == "" {
		return "", fmt.Errorf("任务启动前必须选择执行方式")
	}
	snapshotUUID := ""
	hash := ""
	if hasSnapshot {
		snapshotUUID = uuid.New().String()
		hash, err = snapshotHash(in.Snapshot)
		if err != nil {
			return "", err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("开启任务事务失败: %w", err)
	}
	defer tx.Rollback()
	// 「同步 Agent 产出」默认开启；未显式传入（nil）时按 1 落库。
	syncAgentOutput := 1
	if in.SyncAgentOutput != nil && !*in.SyncAgentOutput {
		syncAgentOutput = 0
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_tasks
		(uuid, source_type, cloud_source_key, cloud_user_id, work_item_type, work_item_id,
		 cloud_work_item_type, cloud_work_item_id, workspace_id, project_uuid,
		 title, content_snapshot, priority, planned_start_date, planned_end_date, task_dir_id, task_dir, task_md_path,
		 selected_pipeline_uuid, selected_pipeline_config_json, pipeline_snapshot_uuid, selected_expert_group_uuid, execution_mode, execution_tool,
		 status, execution_status, current_step_uuid, current_step_completed, work_dir, created_at, updated_at, created_from_conversation,
		 sync_agent_output)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'idle', '', 0, ?, ?, ?, ?, ?)`,
		taskUUID, in.SourceType, in.CloudSourceKey, in.CloudUserID, in.WorkItemType, in.WorkItemID,
		in.WorkItemType, in.WorkItemID, in.WorkspaceID,
		strings.TrimSpace(in.ProjectUUID), in.Title, in.Content, strings.TrimSpace(in.Priority), strings.TrimSpace(in.PlannedStartDate), strings.TrimSpace(in.PlannedEndDate), taskDirID, taskDir, taskMDPath,
		strings.TrimSpace(in.SelectedPipelineUUID), strings.TrimSpace(in.SelectedPipelineConfigJSON), snapshotUUID,
		strings.TrimSpace(in.SelectedExpertGroupUUID), in.ExecutionMode, in.ExecutionTool, status, workDirs[0], now, now, in.CreatedFromConversation && in.SourceType == "local", syncAgentOutput)
	if err != nil {
		return "", fmt.Errorf("创建任务失败: %w", err)
	}
	if in.ExecutionMode == ExecutionModeVibeCoding {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET vibe_coding_model=? WHERE uuid=?`, in.ExecutionModel, taskUUID); err != nil {
			return "", err
		}
	}
	if hasSnapshot {
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_pipeline_snapshots
		(uuid, task_uuid, source_type, source_pipeline_id, cloud_source_key, name, description, avatar, snapshot_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotUUID, taskUUID, in.Snapshot.SourceType,
			in.Snapshot.SourcePipelineID, in.Snapshot.CloudSourceKey, in.Snapshot.Name, in.Snapshot.Description,
			in.Snapshot.Avatar, hash, now)
		if err != nil {
			return "", fmt.Errorf("保存临时流水线失败: %w", err)
		}
	}
	for i, projectID := range projectIDs {
		relation := "subproject"
		if i == 0 && projectID == strings.TrimSpace(in.ProjectUUID) {
			relation = "primary"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_project_links(task_uuid, project_uuid, relation_type, sort_order, created_at) VALUES(?, ?, ?, ?, ?)`, taskUUID, projectID, relation, i, now); err != nil {
			return "", fmt.Errorf("保存任务项目关联失败: %w", err)
		}
	}
	for i, dir := range workDirs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_work_dirs (task_uuid, path, sort_order, created_at) VALUES (?, ?, ?, ?)`, taskUUID, dir, i, now); err != nil {
			return "", fmt.Errorf("保存项目目录失败: %w", err)
		}
	}
	var currentStepUUID string
	for i, step := range in.Snapshot.Steps {
		stepUUID := uuid.New().String()
		stepStatus := "pending"
		if status == "active" && i == 0 {
			stepStatus = "active"
			currentStepUUID = stepUUID
		}
		stepKey := fmt.Sprintf("step-%02d", i+1)
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, source_step_id, local_step_uuid, cloud_step_id,
			 step_key, step_order, name, description, avatar, prompt_snapshot, cli_type, model_name, step_dir,
			 status, execution_status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'idle', ?, ?)`,
			stepUUID, taskUUID, snapshotUUID, in.Snapshot.SourceType, step.SourceStepID, step.LocalStepID, step.CloudStepID,
			stepKey, i, step.Name, step.Description, step.Avatar, step.Prompt, step.CLIType, step.ModelName, stepDirs[i], stepStatus, now, now)
		if err != nil {
			return "", fmt.Errorf("保存临时 Agent 编排失败: %w", err)
		}
	}
	// CLI 直接执行没有 Agent 编排，但依然需要一条隐式步骤承载用户选定的
	// CLI 与模型，使会话、动态、停止和断线恢复复用同一条执行链路。
	// 该步骤不参与流水线进度，状态恒为 active，作为接收第一条指令的门槛。
	if in.ExecutionMode == ExecutionModeCLI {
		stepUUID := uuid.New().String()
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, step_key, step_order, name, description, avatar,
			 prompt_snapshot, cli_type, model_name, step_dir, status, execution_status, execution_mode, created_at, updated_at)
			VALUES (?, ?, '', ?, ?, 0, ?, '', '', '', ?, ?, '', 'active', 'idle', ?, ?, ?)`,
			stepUUID, taskUUID, in.SourceType, CLIDirectStepKey, CLIDirectStepName, in.ExecutionTool,
			in.ExecutionModel, ExecutionModeCLI, now, now); err != nil {
			return "", fmt.Errorf("保存 CLI 直接执行步骤失败: %w", err)
		}
		currentStepUUID = stepUUID
	}
	if currentStepUUID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET current_step_uuid = ? WHERE uuid = ?`, currentStepUUID, taskUUID); err != nil {
			return "", fmt.Errorf("设置当前 Agent 编排失败: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return "", fmt.Errorf("提交任务事务失败: %w", err)
	}
	committed = true
	return taskUUID, nil
}

func resolveCreateTaskTitle(title, content string) (string, error) {
	title = strings.TrimSpace(title)
	if title != "" {
		return title, nil
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", ErrTaskTitleAndDescriptionRequired
	}
	runes := []rune(content)
	if len(runes) > generatedTaskTitleMaxRunes {
		runes = runes[:generatedTaskTitleMaxRunes]
	}
	return string(runes), nil
}

func (s *Service) Update(ctx context.Context, taskUUID string, in UpdateInput) error {
	if s.db == nil {
		return fmt.Errorf("任务数据库未初始化")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return fmt.Errorf("任务标题不能为空")
	}

	var status, taskMDPath, currentStep, snapshotUUID, selectedPipelineUUID, projectUUID, previousContent, executionMode, executionTool string
	err := s.db.QueryRowContext(ctx, `SELECT status, task_md_path, current_step_uuid, pipeline_snapshot_uuid,
		selected_pipeline_uuid, project_uuid, content_snapshot, execution_mode, execution_tool FROM gt_tasks WHERE uuid = ?`, taskUUID).
		Scan(&status, &taskMDPath, &currentStep, &snapshotUUID, &selectedPipelineUUID, &projectUUID, &previousContent, &executionMode, &executionTool)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	pending := status == "pending" && currentStep == ""
	nextProjectUUID := strings.TrimSpace(in.ProjectUUID)
	nextPipelineUUID := strings.TrimSpace(in.SelectedPipelineUUID)
	if nextPipelineUUID != "" && executionMode != "" && executionMode != ExecutionModePipeline {
		return ErrExecutionModeConflict
	}
	nextWorkDirs := in.WorkDirs
	nextSubprojects := in.SubprojectUUIDs
	if !pending {
		if nextProjectUUID != "" && nextProjectUUID != projectUUID {
			return ErrTaskContextLocked
		}
		if nextPipelineUUID != "" && nextPipelineUUID != selectedPipelineUUID {
			return ErrTaskContextLocked
		}
		nextProjectUUID = projectUUID
		nextPipelineUUID = selectedPipelineUUID
		nextWorkDirs = nil
		nextSubprojects = nil
	}
	if snapshotUUID != "" && nextPipelineUUID != selectedPipelineUUID {
		if nextPipelineUUID == "" {
			nextPipelineUUID = selectedPipelineUUID
		} else {
			return ErrPipelineSnapshotLocked
		}
	}

	var workDirs, projectIDs []string
	if pending {
		workDirs, projectIDs, err = s.resolveWorkDirs(ctx, nextProjectUUID, nextSubprojects, nextWorkDirs)
		if err != nil {
			return err
		}
	}

	// 任务详情编辑会提交用户可见的描述，其中不会暴露内部图片路径。若描述本身
	// 没变，只更新标题、优先级等字段时，保留 task.md 的原始正文，避免丢掉
	// 已按输入顺序插入的本地图片路径。
	preserveInlineAttachmentPaths := strings.TrimSpace(in.Content) == strings.TrimSpace(previousContent)
	if err = rewriteTaskMarkdown(taskMDPath, in.Title, in.Content, preserveInlineAttachmentPaths); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启任务事务失败: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	if pending {
		nextExecutionMode := executionMode
		nextExecutionTool := executionTool
		if nextPipelineUUID != "" {
			nextExecutionMode = ExecutionModePipeline
			nextExecutionTool = ""
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks
			SET title=?, content_snapshot=?, priority=?, planned_start_date=?, planned_end_date=?,
			    project_uuid=?, selected_pipeline_uuid=?, execution_mode=?, execution_tool=?, work_dir=?, updated_at=?
			WHERE uuid=?`,
			in.Title, in.Content, strings.TrimSpace(in.Priority), strings.TrimSpace(in.PlannedStartDate),
			strings.TrimSpace(in.PlannedEndDate), nextProjectUUID, nextPipelineUUID, nextExecutionMode, nextExecutionTool, workDirs[0], now, taskUUID); err != nil {
			return fmt.Errorf("更新任务失败: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM gt_task_project_links WHERE task_uuid=?`, taskUUID); err != nil {
			return fmt.Errorf("清理任务项目关联失败: %w", err)
		}
		for i, projectID := range projectIDs {
			relation := "subproject"
			if i == 0 && projectID == nextProjectUUID {
				relation = "primary"
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_project_links(task_uuid, project_uuid, relation_type, sort_order, created_at) VALUES(?, ?, ?, ?, ?)`,
				taskUUID, projectID, relation, i, now); err != nil {
				return fmt.Errorf("保存任务项目关联失败: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM gt_task_work_dirs WHERE task_uuid=?`, taskUUID); err != nil {
			return fmt.Errorf("清理任务目录失败: %w", err)
		}
		for i, dir := range workDirs {
			if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_work_dirs (task_uuid, path, sort_order, created_at) VALUES (?, ?, ?, ?)`,
				taskUUID, dir, i, now); err != nil {
				return fmt.Errorf("保存项目目录失败: %w", err)
			}
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks
			SET title=?, content_snapshot=?, priority=?, planned_start_date=?, planned_end_date=?, updated_at=?
			WHERE uuid=?`,
			in.Title, in.Content, strings.TrimSpace(in.Priority), strings.TrimSpace(in.PlannedStartDate),
			strings.TrimSpace(in.PlannedEndDate), now, taskUUID); err != nil {
			return fmt.Errorf("更新任务失败: %w", err)
		}
	}
	return tx.Commit()
}

func rewriteTaskMarkdown(path, title, content string, preserveInlineAttachmentPaths bool) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("任务文档路径无效")
	}
	existing, err := os.ReadFile(path)
	pastedSection := ""
	if err == nil {
		pastedSection = extractPastedImagesSection(string(existing))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取任务文档失败: %w", err)
	}
	updated := ""
	if preserveInlineAttachmentPaths && hasInlineTaskAttachmentPath(string(existing)) {
		updated = rewriteTaskMarkdownTitle(string(existing), title)
	}
	if updated == "" {
		updated = renderTaskMarkdown(title, content)
	}
	if pastedSection != "" && !hasInlineTaskAttachmentPath(updated) {
		updated = strings.TrimRight(updated, "\n") + "\n\n" + pastedSection
		if !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
	}
	if err = os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("写入 task.md 失败: %w", err)
	}
	return nil
}

func hasInlineTaskAttachmentPath(content string) bool {
	normalized := strings.ReplaceAll(content, "\\", "/")
	return strings.Contains(normalized, "/.goteams-attachments/")
}

func rewriteTaskMarkdownTitle(existing, title string) string {
	if !strings.HasPrefix(existing, "# ") {
		return ""
	}
	lineEnd := strings.IndexByte(existing, '\n')
	if lineEnd < 0 {
		return ""
	}
	body := strings.TrimPrefix(existing[lineEnd+1:], "\n")
	updated := fmt.Sprintf("# %s\n\n%s", title, body)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	return updated
}

func extractPastedImagesSection(content string) string {
	const marker = "## 已粘贴图片"
	index := strings.Index(content, marker)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(content[index:])
}

// AssignPipeline creates the task's immutable, permanent execution snapshot.
// A task can be assigned exactly once and only before it has started.
func (s *Service) AssignPipeline(ctx context.Context, taskUUID string, snapshot Snapshot) (err error) {
	return s.AssignPipelineSelection(ctx, taskUUID, "", "", snapshot)
}

// AssignPipelineSelection atomically persists the selected pipeline, its
// execution configuration and the immutable task snapshot. Empty selection
// fields preserve values already stored during task creation.
func (s *Service) AssignPipelineSelection(ctx context.Context, taskUUID, selectedPipelineUUID, selectedConfigJSON string, snapshot Snapshot) (err error) {
	if err := ValidateSnapshot(&snapshot); err != nil {
		return err
	}
	selectedPipelineUUID = strings.TrimSpace(selectedPipelineUUID)
	selectedConfigJSON = strings.TrimSpace(selectedConfigJSON)
	var taskDir, existingSnapshot, currentStep, executionMode string
	err = s.db.QueryRowContext(ctx, `SELECT task_dir, pipeline_snapshot_uuid, current_step_uuid, execution_mode FROM gt_tasks WHERE uuid = ?`, taskUUID).
		Scan(&taskDir, &existingSnapshot, &currentStep, &executionMode)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if executionMode != "" && executionMode != ExecutionModePipeline {
		return ErrExecutionModeConflict
	}
	if currentStep != "" {
		return fmt.Errorf("任务已经开始，不能重新分配流水线")
	}
	if existingSnapshot != "" {
		return fmt.Errorf("任务已经分配流水线，不能覆盖永久快照")
	}

	snapshotUUID := uuid.NewString()
	hash, err := snapshotHash(snapshot)
	if err != nil {
		return err
	}
	stepDirs := make([]string, len(snapshot.Steps))
	createdDirs := make([]string, 0, len(snapshot.Steps))
	defer func() {
		if err != nil {
			for _, dir := range createdDirs {
				_ = os.RemoveAll(dir)
			}
		}
	}()
	for i, step := range snapshot.Steps {
		stepDirs[i] = filepath.Join(taskDir, fmt.Sprintf("%02d-%s-%s", i+1, safeDirName(step.Name), uuid.NewString()[:8]))
		if err = os.Mkdir(stepDirs[i], 0o755); err != nil {
			return fmt.Errorf("创建 Agent 编排专属目录失败: %w", err)
		}
		createdDirs = append(createdDirs, stepDirs[i])
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var unchanged int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_tasks WHERE uuid=? AND pipeline_snapshot_uuid='' AND current_step_uuid='' AND execution_mode IN ('', 'pipeline')`, taskUUID).Scan(&unchanged); err != nil {
		return err
	}
	if unchanged != 1 {
		return fmt.Errorf("任务状态已变化，不能分配流水线")
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_pipeline_snapshots
		(uuid, task_uuid, source_type, source_pipeline_id, cloud_source_key, name, description, avatar, snapshot_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshotUUID, taskUUID, snapshot.SourceType, snapshot.SourcePipelineID,
		snapshot.CloudSourceKey, snapshot.Name, snapshot.Description, snapshot.Avatar, hash, now); err != nil {
		return err
	}
	for i, step := range snapshot.Steps {
		stepUUID := uuid.NewString()
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, source_step_id, local_step_uuid, cloud_step_id,
			 step_key, step_order, name, description, avatar, prompt_snapshot, cli_type, model_name, step_dir,
			 status, execution_status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'idle', ?, ?)`, stepUUID, taskUUID,
			snapshotUUID, snapshot.SourceType, step.SourceStepID, step.LocalStepID, step.CloudStepID,
			fmt.Sprintf("step-%02d", i+1), i, step.Name, step.Description, step.Avatar, step.Prompt,
			step.CLIType, step.ModelName, stepDirs[i], now, now); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET
		pipeline_snapshot_uuid=?,
		selected_pipeline_uuid=CASE WHEN ?<>'' THEN ? ELSE selected_pipeline_uuid END,
		selected_pipeline_config_json=CASE WHEN ?<>'' THEN ? ELSE selected_pipeline_config_json END,
		execution_mode='pipeline', execution_tool='', updated_at=?
		WHERE uuid=? AND pipeline_snapshot_uuid='' AND current_step_uuid='' AND execution_mode IN ('', 'pipeline')`,
		snapshotUUID, selectedPipelineUUID, selectedPipelineUUID, selectedConfigJSON, selectedConfigJSON, now, taskUUID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("任务状态已变化，不能分配流水线")
	}
	return tx.Commit()
}

// AssignVibeCoding assigns the task to an external editor without starting the
// internal workflow. start only moves the board status to active.
func (s *Service) AssignVibeCoding(ctx context.Context, taskUUID, tool, model string, start bool) error {
	model = strings.TrimSpace(model)
	tool = strings.ToLower(strings.TrimSpace(tool))
	if err := ValidateExecutionAssignment(ExecutionModeVibeCoding, tool, ""); err != nil {
		return err
	}
	if err := ValidateVibeModel(tool, model); err != nil {
		return err
	}
	var mode, existingTool, status, pipelineUUID, snapshotUUID, currentStep string
	err := s.db.QueryRowContext(ctx, `SELECT execution_mode, execution_tool, status, selected_pipeline_uuid, pipeline_snapshot_uuid, current_step_uuid
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&mode, &existingTool, &status, &pipelineUUID, &snapshotUUID, &currentStep)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if mode != "" && (mode != ExecutionModeVibeCoding || existingTool != tool) {
		return ErrExecutionModeConflict
	}
	if pipelineUUID != "" || snapshotUUID != "" || currentStep != "" {
		return ErrExecutionModeConflict
	}
	if start && status == "done" {
		return fmt.Errorf("已完成任务不能重新启动")
	}
	nextStatus := status
	if start {
		nextStatus = "active"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gt_tasks SET execution_mode=?, execution_tool=?, vibe_coding_model=CASE WHEN execution_mode='' THEN ? ELSE vibe_coding_model END, status=?, updated_at=?
		WHERE uuid=? AND execution_mode IN ('', 'vibe_coding') AND selected_pipeline_uuid='' AND pipeline_snapshot_uuid='' AND current_step_uuid=''`,
		ExecutionModeVibeCoding, tool, model, nextStatus, time.Now().UnixMilli(), taskUUID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrExecutionModeConflict
	}
	return nil
}

// AssignCLI assigns the task to a direct CLI and model execution. The mode skips
// both the pipeline and the Agent orchestration: it records the chosen runtime on
// the implicit step, moves the board status to active, and waits for the user's
// first instruction instead of starting a session.
func (s *Service) AssignCLI(ctx context.Context, taskUUID, cliType, modelName string, start bool) error {
	cliType = strings.ToLower(strings.TrimSpace(cliType))
	modelName = strings.TrimSpace(modelName)
	if err := ValidateExecutionTarget(ExecutionModeCLI, cliType, "", ""); err != nil {
		return err
	}
	if err := ValidateCLITarget(cliType, modelName); err != nil {
		return err
	}
	var mode, status, pipelineUUID, snapshotUUID, currentStep, expertSnapshotUUID string
	err := s.db.QueryRowContext(ctx, `SELECT execution_mode, status, selected_pipeline_uuid,
		pipeline_snapshot_uuid, current_step_uuid, expert_group_snapshot_uuid FROM gt_tasks WHERE uuid=?`,
		taskUUID).Scan(&mode, &status, &pipelineUUID, &snapshotUUID, &currentStep, &expertSnapshotUUID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if mode != "" && mode != ExecutionModeCLI {
		return ErrExecutionModeConflict
	}
	if pipelineUUID != "" || snapshotUUID != "" || expertSnapshotUUID != "" {
		return ErrExecutionModeConflict
	}
	if start && status == "done" {
		return fmt.Errorf("已完成任务不能重新启动")
	}
	nextStatus := status
	if start {
		nextStatus = "active"
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启 CLI 指派事务失败: %w", err)
	}
	defer tx.Rollback()

	stepUUID := currentStep
	if stepUUID != "" {
		// 已指派过 CLI 的任务允许仅更新该隐式步骤的 CLI 与模型，其他来源的
		// 当前步骤说明任务已被别的执行方式占用。
		var stepKey string
		if err = tx.QueryRowContext(ctx, `SELECT step_key FROM gt_task_steps WHERE task_uuid=? AND uuid=?`,
			taskUUID, stepUUID).Scan(&stepKey); err != nil {
			return ErrExecutionModeConflict
		}
		if stepKey != CLIDirectStepKey {
			return ErrExecutionModeConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET cli_type=?, model_name=?, execution_mode=?, status='active', updated_at=?
			WHERE task_uuid=? AND uuid=?`, cliType, modelName, ExecutionModeCLI, now, taskUUID, stepUUID); err != nil {
			return fmt.Errorf("更新 CLI 直接执行步骤失败: %w", err)
		}
	} else {
		stepUUID = uuid.New().String()
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, step_key, step_order, name, description, avatar,
			 prompt_snapshot, cli_type, model_name, step_dir, status, execution_status, execution_mode, created_at, updated_at)
			VALUES (?, ?, '', 'local', ?, 0, ?, '', '', '', ?, ?, '', 'active', 'idle', ?, ?, ?)`,
			stepUUID, taskUUID, CLIDirectStepKey, CLIDirectStepName, cliType, modelName,
			ExecutionModeCLI, now, now); err != nil {
			return fmt.Errorf("保存 CLI 直接执行步骤失败: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET execution_mode=?, execution_tool=?, status=?, current_step_uuid=?, updated_at=?
		WHERE uuid=? AND execution_mode IN ('', 'cli') AND selected_pipeline_uuid='' AND pipeline_snapshot_uuid='' AND expert_group_snapshot_uuid=''`,
		ExecutionModeCLI, cliType, nextStatus, stepUUID, now, taskUUID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrExecutionModeConflict
	}
	return tx.Commit()
}

func (s *Service) Delete(ctx context.Context, taskUUID string) error {
	var taskDir string
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT task_dir, (SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid = gt_tasks.uuid AND status IN ('created','running','waiting_input','stop_requested')) FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&taskDir, &active)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("任务仍有正在执行的 CLI 会话，请先终止后再删除")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := cloudsync.CaptureTaskExecutionTx(ctx, tx, taskUUID); err != nil {
		return err
	}
	sessionUUIDs, err := DeleteTaskSessionRowsTx(ctx, tx, taskUUID)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM gt_task_notifications WHERE task_uuid = ?`,
		`DELETE FROM gt_task_execution_history WHERE task_uuid = ?`,
		`DELETE FROM gt_cli_sessions WHERE task_uuid = ?`,
		`DELETE FROM gt_task_progress WHERE task_uuid = ?`,
		`DELETE FROM gt_task_steps WHERE task_uuid = ?`,
		`DELETE FROM gt_task_work_dirs WHERE task_uuid = ?`,
		`DELETE FROM gt_task_pipeline_snapshots WHERE task_uuid = ?`,
		`DELETE FROM gt_task_expert_group_snapshots WHERE task_uuid = ?`,
		`DELETE FROM gt_tasks WHERE uuid = ?`,
	} {
		if _, err = tx.ExecContext(ctx, statement, taskUUID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	RemoveSessionArtifacts(sessionUUIDs)
	root, _ := filepath.Abs(s.taskRoot)
	target, _ := filepath.Abs(taskDir)
	rel, relErr := filepath.Rel(root, target)
	if relErr == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		if err = os.RemoveAll(target); err != nil {
			return fmt.Errorf("任务数据已从数据库删除，但专属目录删除失败: %w", err)
		}
	}
	return nil
}

// Start activates the first immutable step. The caller starts its initial CLI
// conversation only when the returned step UUID is non-empty.
func (s *Service) Start(ctx context.Context, taskUUID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var currentStepUUID, pipelineSnapshotUUID, expertSnapshotUUID, executionMode string
	if err = tx.QueryRowContext(ctx, `SELECT current_step_uuid, pipeline_snapshot_uuid, expert_group_snapshot_uuid, execution_mode FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&currentStepUUID, &pipelineSnapshotUUID, &expertSnapshotUUID, &executionMode); err == sql.ErrNoRows {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if currentStepUUID != "" {
		return "", fmt.Errorf("任务已经开始")
	}
	if executionMode == ExecutionModePipeline && pipelineSnapshotUUID == "" {
		return "", fmt.Errorf("任务启动前必须选择流水线")
	}
	if executionMode == ExecutionModeExpertGroup && expertSnapshotUUID == "" {
		return "", fmt.Errorf("任务启动前必须选择专家团")
	}
	rows, err := tx.QueryContext(ctx, `SELECT uuid, COALESCE(cli_type,''), COALESCE(model_name,''), COALESCE(member_role,'')
		FROM gt_task_steps WHERE task_uuid = ? ORDER BY step_order, rowid`, taskUUID)
	if err != nil {
		return "", err
	}
	stepIndex := 0
	missingSteps := make([]int, 0)
	for rows.Next() {
		stepIndex++
		var stepUUID string
		var cliType, modelName, memberRole string
		if scanErr := rows.Scan(&stepUUID, &cliType, &modelName, &memberRole); scanErr != nil {
			rows.Close()
			return "", scanErr
		}
		if stepIndex == 1 || memberRole == "leader" {
			currentStepUUID = stepUUID
		}
		if strings.TrimSpace(cliType) == "" || strings.TrimSpace(modelName) == "" {
			missingSteps = append(missingSteps, stepIndex)
		}
	}
	rows.Close()
	if stepIndex == 0 {
		return "", fmt.Errorf("任务没有 Agent 编排")
	}
	if len(missingSteps) > 0 {
		return "", fmt.Errorf("第 %d 个 Agent 编排缺少 CLI 和模型配置", missingSteps[0])
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status = 'active', updated_at = ? WHERE uuid = ? AND status = 'pending'`, now, currentStepUUID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status = CASE WHEN status = 'done' THEN status ELSE 'active' END, current_step_uuid = ?, current_step_completed = 0,
		started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END, updated_at = ? WHERE uuid = ?`, currentStepUUID, now, now, taskUUID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return currentStepUUID, nil
}

func ValidateSnapshot(snapshot *Snapshot) error {
	if snapshot.SourceType == "" {
		snapshot.SourceType = "local"
	}
	if snapshot.SourceType != "local" && snapshot.SourceType != "cloud" {
		return fmt.Errorf("无效流水线来源: %s", snapshot.SourceType)
	}
	if strings.TrimSpace(snapshot.SourcePipelineID) == "" {
		return fmt.Errorf("流水线来源 ID 不能为空")
	}
	if len(snapshot.Steps) == 0 {
		return fmt.Errorf("流水线至少需要一个 Agent 编排步骤")
	}
	for i := range snapshot.Steps {
		step := &snapshot.Steps[i]
		step.Name = strings.TrimSpace(step.Name)
		step.Prompt = strings.TrimSpace(step.Prompt)
		step.CLIType = strings.TrimSpace(step.CLIType)
		step.ModelName = strings.TrimSpace(step.ModelName)
		if step.Name == "" || step.Prompt == "" {
			return fmt.Errorf("第 %d 个 Agent 编排必须配置名称和提示词", i+1)
		}
	}
	sort.SliceStable(snapshot.Steps, func(i, j int) bool { return snapshot.Steps[i].SortOrder < snapshot.Steps[j].SortOrder })
	return nil
}

// ValidateSnapshotFull validates the snapshot structure and also requires
// every step to have CLI and Model configured. Used when the task will start
// executing immediately after pipeline assignment.
func ValidateSnapshotFull(snapshot *Snapshot) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	for i, step := range snapshot.Steps {
		if step.CLIType == "" || step.ModelName == "" {
			return fmt.Errorf("第 %d 个 Agent 编排必须配置 CLI 和模型", i+1)
		}
	}
	return nil
}

func (s *Service) resolveWorkDirs(ctx context.Context, primary string, subprojects, associated []string) ([]string, []string, error) {
	projectIDs := make([]string, 0, len(subprojects)+1)
	dirs := make([]string, 0, len(subprojects)+len(associated)+1)
	seenProjects := map[string]struct{}{}
	appendProject := func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		if _, exists := seenProjects[id]; exists {
			return fmt.Errorf("任务项目不能重复: %s", id)
		}
		var dir string
		if err := s.db.QueryRowContext(ctx, `SELECT main_dir FROM gt_projects WHERE uuid=?`, id).Scan(&dir); err == sql.ErrNoRows {
			return fmt.Errorf("项目不存在: %s", id)
		} else if err != nil {
			return err
		}
		seenProjects[id] = struct{}{}
		projectIDs = append(projectIDs, id)
		dirs = append(dirs, dir)
		return nil
	}
	if err := appendProject(primary); err != nil {
		return nil, nil, err
	}
	for _, id := range subprojects {
		if err := appendProject(id); err != nil {
			return nil, nil, err
		}
	}
	dirs = append(dirs, associated...)
	normalized, err := normalizeWorkDirs(dirs)
	return normalized, projectIDs, err
}

// CompleteStep permanently closes the current step and atomically activates
// the next one. Completed steps can never be executed again.
func (s *Service) CompleteStep(ctx context.Context, taskUUID, stepUUID string) (*AdvanceResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentStepUUID, stepStatus string
	var stepOrder int
	err = tx.QueryRowContext(ctx, `SELECT t.current_step_uuid, s.status, s.step_order
		FROM gt_tasks t JOIN gt_task_steps s ON s.task_uuid = t.uuid
		WHERE t.uuid = ? AND s.uuid = ?`, taskUUID, stepUUID).Scan(&currentStepUUID, &stepStatus, &stepOrder)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if currentStepUUID != stepUUID || stepStatus != "active" {
		if stepStatus == "completed" {
			return nil, fmt.Errorf("该 Agent 编排已经完成")
		}
		return nil, fmt.Errorf("只能完成当前 Agent 编排")
	}
	var activeSessions, terminalProgress int
	if err = tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid = ? AND status IN ('created','running','waiting_input','stop_requested')),
		(SELECT COUNT(*) FROM gt_task_progress WHERE task_uuid = ? AND task_step_uuid = ? AND status IN ('success','failed','stopped','interrupted'))`,
		taskUUID, taskUUID, stepUUID).Scan(&activeSessions, &terminalProgress); err != nil {
		return nil, err
	}
	if activeSessions > 0 {
		return nil, fmt.Errorf("当前 CLI 仍在执行")
	}
	if terminalProgress == 0 {
		return nil, fmt.Errorf("当前 Agent 编排尚未产生执行结果")
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status = 'completed', completed_at = ?, updated_at = ? WHERE uuid = ?`, now, now, stepUUID); err != nil {
		return nil, err
	}
	var nextStepUUID string
	err = tx.QueryRowContext(ctx, `SELECT uuid FROM gt_task_steps WHERE task_uuid = ? AND step_order > ? AND status = 'pending' ORDER BY step_order LIMIT 1`, taskUUID, stepOrder).Scan(&nextStepUUID)
	result := &AdvanceResult{}
	if err == sql.ErrNoRows {
		result.TaskDone = true
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status = 'done', current_step_uuid = ?, current_step_completed = 1, execution_status = 'success', finished_at = ?, updated_at = ? WHERE uuid = ?`, stepUUID, now, now, taskUUID); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		result.NextStepUUID = nextStepUUID
		if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status = 'active', updated_at = ? WHERE uuid = ?`, now, nextStepUUID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status = 'active', current_step_uuid = ?, current_step_completed = 0, execution_status = 'idle', updated_at = ? WHERE uuid = ?`, nextStepUUID, now, taskUUID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeWorkDirs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("至少需要一个项目目录")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		abs, err := filepath.Abs(strings.TrimSpace(value))
		if err != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("项目目录无效: %s", value)
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("项目目录不存在或不是目录: %s", abs)
		}
		key := filepath.Clean(abs)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, filepath.Clean(abs))
	}
	return result, nil
}

func safeDirName(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "step"
	}
	if len([]rune(result)) > 32 {
		return string([]rune(result)[:32])
	}
	return result
}

func renderTaskMarkdown(title, content string) string {
	return fmt.Sprintf("# %s\n\n%s\n", title, strings.TrimSpace(content))
}

func snapshotHash(snapshot Snapshot) (string, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("序列化流水线快照失败: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
