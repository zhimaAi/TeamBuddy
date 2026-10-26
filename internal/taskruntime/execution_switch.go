package taskruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goteams-client/internal/cloudsync"
	"goteams-client/internal/expertgroup"
)

var (
	ErrExecutionModeRequired  = fmt.Errorf("任务尚未指定执行方式")
	ErrExecutionModeUnchanged = fmt.Errorf("请选择与当前配置不同的执行方式")
	ErrVibeBindingStale       = fmt.Errorf("通用工具绑定或执行代次已变化")
)

// SwitchExecutionInput contains a fully validated replacement target. Pipeline
// and expert-group definitions are resolved before the current execution is
// stopped, so the destructive transaction only persists prepared data.
type SwitchExecutionInput struct {
	Mode                       string
	RebindExecutionIndex       *int
	Tool                       string
	ModelName                  string
	SelectedPipelineUUID       string
	SelectedPipelineConfigJSON string
	Pipeline                   *Snapshot
	ExpertGroup                *expertgroup.Group
}

type currentExecution struct {
	mode                 string
	tool                 string
	selectedPipelineUUID string
	pipelineSnapshotUUID string
	selectedExpertUUID   string
	expertSnapshotUUID   string
	currentStepUUID      string
	cliModel             string
	taskDir              string
	executionIndex       int
}

// ValidateExecutionSwitch verifies the replacement target without mutating the
// task. Callers use it before stopping active sessions.
func (s *Service) ValidateExecutionSwitch(ctx context.Context, taskUUID string, input SwitchExecutionInput) error {
	input.Mode = NormalizeExecutionMode(input.Mode)
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.SelectedPipelineUUID = strings.TrimSpace(input.SelectedPipelineUUID)
	if err := ValidateExecutionTarget(input.Mode, input.Tool, input.SelectedPipelineUUID, expertGroupUUID(input.ExpertGroup)); err != nil {
		return err
	}
	if input.Mode == ExecutionModeVibeCoding {
		if err := ValidateVibeModel(input.Tool, input.ModelName); err != nil {
			return err
		}
	}
	if input.Mode == ExecutionModeCLI {
		if err := ValidateCLITarget(input.Tool, input.ModelName); err != nil {
			return err
		}
	}
	if input.Mode == ExecutionModePipeline {
		if input.Pipeline == nil || input.SelectedPipelineUUID == "" {
			return fmt.Errorf("流水线不能为空")
		}
		if err := ValidateSnapshotFull(input.Pipeline); err != nil {
			return err
		}
	}
	if input.Mode == ExecutionModeExpertGroup && (input.ExpertGroup == nil || !input.ExpertGroup.Ready || input.ExpertGroup.Leader == nil) {
		return expertgroup.ErrNotReady
	}
	current, err := s.loadCurrentExecution(ctx, taskUUID)
	if err != nil {
		return err
	}
	if current.mode == "" {
		return ErrExecutionModeRequired
	}
	return validateReplacementTarget(current, input)
}

func validateReplacementTarget(current currentExecution, input SwitchExecutionInput) error {
	if input.RebindExecutionIndex != nil {
		definition, ok := VibeTool(input.Tool)
		if !ok || !definition.SupportsRebind || input.Mode != ExecutionModeVibeCoding ||
			!sameExecutionTarget(current, input) || current.executionIndex != *input.RebindExecutionIndex {
			return ErrVibeBindingStale
		}
		return nil
	}
	if sameExecutionTarget(current, input) {
		return ErrExecutionModeUnchanged
	}
	return nil
}

// SwitchExecution atomically archives and removes the old execution projection,
// then writes a fresh target. Task content, attachments, project links, work
// directories and files produced by the old execution are intentionally preserved.
func (s *Service) SwitchExecution(ctx context.Context, taskUUID string, input SwitchExecutionInput) (err error) {
	input.Mode = NormalizeExecutionMode(input.Mode)
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.SelectedPipelineUUID = strings.TrimSpace(input.SelectedPipelineUUID)
	input.SelectedPipelineConfigJSON = strings.TrimSpace(input.SelectedPipelineConfigJSON)
	if input.SelectedPipelineConfigJSON == "" {
		input.SelectedPipelineConfigJSON = "[]"
	}
	if err = s.ValidateExecutionSwitch(ctx, taskUUID, input); err != nil {
		return err
	}
	current, err := s.loadCurrentExecution(ctx, taskUUID)
	if err != nil {
		return err
	}

	prepared, err := prepareSwitchTarget(current.taskDir, input)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, dir := range prepared.createdDirs {
			_ = os.RemoveAll(dir)
		}
	}()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	latest, err := loadCurrentExecutionTx(ctx, tx, taskUUID)
	if err != nil {
		return err
	}
	if err = validateReplacementTarget(latest, input); err != nil {
		return err
	}
	if !sameCurrentExecution(current, latest) {
		return fmt.Errorf("任务执行配置已变化，请刷新后重试")
	}
	// Capture inside the same transaction as deletion. No network dependency:
	// an offline switch must retain the complete previous run for later upload.
	if err = cloudsync.CaptureTaskExecutionTx(ctx, tx, taskUUID); err != nil {
		return fmt.Errorf("保存执行同步快照失败: %w", err)
	}
	if err = archiveCurrentExecutionTx(ctx, tx, taskUUID); err != nil {
		return fmt.Errorf("归档当前执行失败: %w", err)
	}

	sessionUUIDs, err := DeleteTaskSessionRowsTx(ctx, tx, taskUUID)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM gt_task_notifications WHERE task_uuid=?`,
		`DELETE FROM gt_task_progress WHERE task_uuid=?`,
		`DELETE FROM gt_cli_sessions WHERE task_uuid=?`,
		`DELETE FROM gt_task_steps WHERE task_uuid=?`,
		`DELETE FROM gt_task_pipeline_snapshots WHERE task_uuid=?`,
		`DELETE FROM gt_task_expert_group_snapshots WHERE task_uuid=?`,
	} {
		if _, err = tx.ExecContext(ctx, statement, taskUUID); err != nil {
			return err
		}
	}

	now := time.Now().UnixMilli()
	if err = prepared.insert(ctx, tx, taskUUID, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	RemoveSessionArtifacts(sessionUUIDs)
	return nil
}

// ResetSwitchedExecutionStart restores a newly replaced pipeline/expert task to
// a retryable state when the synchronous first-session creation fails.
func (s *Service) ResetSwitchedExecutionStart(ctx context.Context, taskUUID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStepUUID string
	var activeSessions int
	if err = tx.QueryRowContext(ctx, `SELECT current_step_uuid,
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=gt_tasks.uuid
		 AND status IN ('created','running','waiting_input','stop_requested'))
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&currentStepUUID, &activeSessions); err == sql.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if activeSessions > 0 {
		return fmt.Errorf("新执行方式已有活动 Session，不能回退启动状态")
	}
	now := time.Now().UnixMilli()
	if currentStepUUID != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status='pending', execution_status='idle',
			started_at=0, finished_at=0, duration_ms=0, updated_at=? WHERE task_uuid=? AND uuid=?`,
			now, taskUUID, currentStepUUID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status='pending', execution_status='idle',
		current_step_uuid='', current_step_completed=0, started_at=0, finished_at=0, updated_at=? WHERE uuid=?`,
		now, taskUUID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) loadCurrentExecution(ctx context.Context, taskUUID string) (currentExecution, error) {
	return loadCurrentExecutionRow(s.db.QueryRowContext(ctx, `SELECT execution_mode, execution_tool,
		selected_pipeline_uuid, pipeline_snapshot_uuid, selected_expert_group_uuid,
		expert_group_snapshot_uuid, current_step_uuid, task_dir, execution_index
		FROM gt_tasks WHERE uuid=?`, taskUUID), s.db, taskUUID)
}

func loadCurrentExecutionTx(ctx context.Context, tx *sql.Tx, taskUUID string) (currentExecution, error) {
	return loadCurrentExecutionRow(tx.QueryRowContext(ctx, `SELECT execution_mode, execution_tool,
		selected_pipeline_uuid, pipeline_snapshot_uuid, selected_expert_group_uuid,
		expert_group_snapshot_uuid, current_step_uuid, task_dir, execution_index
		FROM gt_tasks WHERE uuid=?`, taskUUID), tx, taskUUID)
}

type rowScanner interface {
	Scan(dest ...any) error
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCurrentExecutionRow(row rowScanner, query queryRower, taskUUID string) (current currentExecution, err error) {
	err = row.Scan(&current.mode, &current.tool, &current.selectedPipelineUUID, &current.pipelineSnapshotUUID,
		&current.selectedExpertUUID, &current.expertSnapshotUUID, &current.currentStepUUID, &current.taskDir, &current.executionIndex)
	if err == sql.ErrNoRows {
		return current, ErrNotFound
	}
	if err != nil {
		return current, err
	}
	if current.mode == ExecutionModeCLI {
		_ = query.QueryRowContext(context.Background(), `SELECT model_name FROM gt_task_steps
			WHERE task_uuid=? AND step_key=? LIMIT 1`, taskUUID, CLIDirectStepKey).Scan(&current.cliModel)
	}
	return current, nil
}

func sameExecutionTarget(current currentExecution, input SwitchExecutionInput) bool {
	if current.mode != input.Mode {
		return false
	}
	switch input.Mode {
	case ExecutionModePipeline:
		return current.selectedPipelineUUID == input.SelectedPipelineUUID
	case ExecutionModeExpertGroup:
		return current.selectedExpertUUID == expertGroupUUID(input.ExpertGroup)
	case ExecutionModeVibeCoding:
		return current.tool == input.Tool
	case ExecutionModeCLI:
		return current.tool == input.Tool && current.cliModel == input.ModelName
	default:
		return false
	}
}

func sameCurrentExecution(left, right currentExecution) bool {
	return left.mode == right.mode && left.tool == right.tool &&
		left.selectedPipelineUUID == right.selectedPipelineUUID && left.pipelineSnapshotUUID == right.pipelineSnapshotUUID &&
		left.selectedExpertUUID == right.selectedExpertUUID && left.expertSnapshotUUID == right.expertSnapshotUUID &&
		left.currentStepUUID == right.currentStepUUID && left.cliModel == right.cliModel &&
		left.executionIndex == right.executionIndex
}

func expertGroupUUID(group *expertgroup.Group) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.UUID)
}

type preparedSwitchTarget struct {
	input         SwitchExecutionInput
	pipelineUUID  string
	expertUUID    string
	expertHash    string
	expertMembers []expertgroup.Member
	stepDirs      []string
	createdDirs   []string
}

func prepareSwitchTarget(taskDir string, input SwitchExecutionInput) (prepared preparedSwitchTarget, err error) {
	prepared.input = input
	defer func() {
		if err == nil {
			return
		}
		for _, dir := range prepared.createdDirs {
			_ = os.RemoveAll(dir)
		}
	}()
	switch input.Mode {
	case ExecutionModePipeline:
		prepared.pipelineUUID = uuid.NewString()
		prepared.stepDirs = make([]string, len(input.Pipeline.Steps))
		for index, step := range input.Pipeline.Steps {
			dir := filepath.Join(taskDir, fmt.Sprintf("%02d-%s-%s", index+1, safeDirName(step.Name), uuid.NewString()[:8]))
			if err = os.Mkdir(dir, 0o755); err != nil {
				return prepared, fmt.Errorf("创建 Agent 编排专属目录失败: %w", err)
			}
			prepared.stepDirs[index] = dir
			prepared.createdDirs = append(prepared.createdDirs, dir)
		}
	case ExecutionModeExpertGroup:
		prepared.expertUUID = uuid.NewString()
		prepared.expertMembers = append(prepared.expertMembers, *input.ExpertGroup.Leader)
		prepared.expertMembers = append(prepared.expertMembers, input.ExpertGroup.Members...)
		bytes, marshalErr := json.Marshal(input.ExpertGroup)
		if marshalErr != nil {
			return prepared, marshalErr
		}
		hash := sha256.Sum256(bytes)
		prepared.expertHash = hex.EncodeToString(hash[:])
		prepared.stepDirs = make([]string, len(prepared.expertMembers))
		for index, member := range prepared.expertMembers {
			dir := filepath.Join(taskDir, fmt.Sprintf("expert-%s-%s", safeDirName(member.Name), uuid.NewString()[:8]))
			if err = os.Mkdir(dir, 0o755); err != nil {
				return prepared, fmt.Errorf("创建专家团成员目录失败: %w", err)
			}
			prepared.stepDirs[index] = dir
			prepared.createdDirs = append(prepared.createdDirs, dir)
		}
	}
	return prepared, nil
}

func (prepared preparedSwitchTarget) insert(ctx context.Context, tx *sql.Tx, taskUUID string, now int64) error {
	selectedPipelineUUID := ""
	selectedPipelineConfigJSON := "[]"
	pipelineSnapshotUUID := ""
	selectedExpertUUID := ""
	expertSnapshotUUID := ""
	currentStepUUID := ""
	status := "pending"

	switch prepared.input.Mode {
	case ExecutionModePipeline:
		selectedPipelineUUID = prepared.input.SelectedPipelineUUID
		selectedPipelineConfigJSON = prepared.input.SelectedPipelineConfigJSON
		pipelineSnapshotUUID = prepared.pipelineUUID
		hash, err := snapshotHash(*prepared.input.Pipeline)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_pipeline_snapshots
			(uuid, task_uuid, source_type, source_pipeline_id, cloud_source_key, name, description, avatar, snapshot_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pipelineSnapshotUUID, taskUUID, prepared.input.Pipeline.SourceType,
			prepared.input.Pipeline.SourcePipelineID, prepared.input.Pipeline.CloudSourceKey, prepared.input.Pipeline.Name,
			prepared.input.Pipeline.Description, prepared.input.Pipeline.Avatar, hash, now); err != nil {
			return err
		}
		// 仅激活首个输入目标，不生成会话或启动 CLI，用户消息到来后才执行。
		status = "pending"
		for index, step := range prepared.input.Pipeline.Steps {
			stepUUID := uuid.NewString()
			stepStatus := "pending"
			if index == 0 {
				currentStepUUID = stepUUID
				stepStatus = "active"
			}
			stepKey := fmt.Sprintf("step-%02d", index+1)
			if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
				(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, source_step_id, local_step_uuid, cloud_step_id,
				 step_key, step_order, name, description, avatar, prompt_snapshot, cli_type, model_name, step_dir,
				 status, execution_status, execution_mode, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'idle', 'pipeline', ?, ?)`,
				stepUUID, taskUUID, pipelineSnapshotUUID, prepared.input.Pipeline.SourceType, step.SourceStepID,
				step.LocalStepID, step.CloudStepID, stepKey, index, step.Name, step.Description,
				step.Avatar, step.Prompt, step.CLIType, step.ModelName, prepared.stepDirs[index], stepStatus, now, now); err != nil {
				return err
			}
		}
	case ExecutionModeExpertGroup:
		selectedExpertUUID = prepared.input.ExpertGroup.UUID
		expertSnapshotUUID = prepared.expertUUID
		if _, err := tx.ExecContext(ctx, `INSERT INTO gt_task_expert_group_snapshots
			(uuid, task_uuid, source_expert_group_uuid, name, description, avatar, snapshot_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, expertSnapshotUUID, taskUUID, prepared.input.ExpertGroup.UUID,
			prepared.input.ExpertGroup.Name, prepared.input.ExpertGroup.Description, prepared.input.ExpertGroup.Avatar,
			prepared.expertHash, now); err != nil {
			return err
		}
		leaderStepUUID := ""
		for index, member := range prepared.expertMembers {
			stepUUID := uuid.NewString()
			stepKey := "expert-" + member.UUID
			if member.MemberRole == expertgroup.RoleLeader {
				leaderStepUUID = stepUUID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gt_task_steps
				(uuid, task_uuid, source_type, source_step_id, local_step_uuid, step_key, step_order, name, description,
				 avatar, prompt_snapshot, cli_type, model_name, step_dir, status, execution_status, execution_mode,
				 member_role, task_expert_group_snapshot_uuid, created_at, updated_at)
				VALUES (?, ?, 'local', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'idle', 'expert_group', ?, ?, ?, ?)`,
				stepUUID, taskUUID, member.UUID, member.UUID, stepKey, index, member.Name, member.Description,
				member.Avatar, member.Prompt, member.CLIType, member.ModelName, prepared.stepDirs[index], member.MemberRole,
				expertSnapshotUUID, now, now); err != nil {
				return err
			}
		}
		if leaderStepUUID == "" {
			return expertgroup.ErrNotReady
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gt_task_expert_group_snapshots SET leader_task_step_uuid=? WHERE uuid=?`, leaderStepUUID, expertSnapshotUUID); err != nil {
			return err
		}
	case ExecutionModeCLI:
		status = "pending"
		currentStepUUID = uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, task_pipeline_snapshot_uuid, source_type, step_key, step_order, name, description, avatar,
			 prompt_snapshot, cli_type, model_name, step_dir, status, execution_status, execution_mode, created_at, updated_at)
			VALUES (?, ?, '', 'local', ?, 0, ?, '', '', '', ?, ?, '', 'active', 'idle', 'cli', ?, ?)`,
			currentStepUUID, taskUUID, CLIDirectStepKey, CLIDirectStepName, prepared.input.Tool, prepared.input.ModelName, now, now); err != nil {
			return err
		}
	case ExecutionModeVibeCoding:
		status = "active"
	}

	// execution_index 在清空本地执行投影之前由调用方把当前这一次封存到云端。
	// 这里只递增序号并重置新一次执行的计数，云端按新序号另存一行，旧运行记录保持不变。
	vibeModel := ""
	if prepared.input.Mode == ExecutionModeVibeCoding {
		vibeModel = prepared.input.ModelName
	}
	result, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET
		selected_pipeline_uuid=?, selected_pipeline_config_json=?, pipeline_snapshot_uuid=?,
		selected_expert_group_uuid=?, expert_group_snapshot_uuid=?, execution_mode=?, execution_tool=?,
		vibe_coding_session_json='{}', vibe_coding_model=?,
		status=?, execution_status='idle', current_step_uuid=?, current_step_completed=0,
		cli_type='', current_step_key='', execution_summary='', cloud_workflow_snapshot_hash='', model_name='',
		input_tokens=0, output_tokens=0, total_tokens=0, conversation_rounds=0,
		execution_index=execution_index+1,
		started_at=0, finished_at=0, updated_at=? WHERE uuid=?`,
		selectedPipelineUUID, selectedPipelineConfigJSON, pipelineSnapshotUUID, selectedExpertUUID, expertSnapshotUUID,
		prepared.input.Mode, prepared.input.Tool, vibeModel, status, currentStepUUID, now, taskUUID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	if prepared.input.Mode != ExecutionModeVibeCoding {
		// 没有新会话时也保留侧栏入口，否则通知刷新会把用户切到其他任务。
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_notifications
   (uuid,task_uuid,task_step_uuid,step_order,progress_uuid,session_uuid,terminal_status,title,summary,is_read,is_archived,created_at)
   SELECT ?,uuid,?,0,'',?,'idle',title,'',1,0,? FROM gt_tasks WHERE uuid=?`,
			uuid.NewString(), currentStepUUID, uuid.NewString(), now, taskUUID)
		if err != nil {
			return err
		}
	}
	return nil
}
