package taskruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goteams-client/internal/executor"
	"goteams-client/internal/expertgroup"
)

// AssignExpertGroup freezes the current expert-group definition into the task.
func (s *Service) AssignExpertGroup(ctx context.Context, taskUUID string, group *expertgroup.Group) (err error) {
	if group == nil || !group.Ready || group.Leader == nil || len(group.Members) == 0 {
		return expertgroup.ErrNotReady
	}
	var taskDir, workDir, status, mode, pipelineUUID, pipelineSnapshotUUID, existingExpertSnapshot string
	err = s.db.QueryRowContext(ctx, `SELECT task_dir, work_dir, status, execution_mode, selected_pipeline_uuid,
		pipeline_snapshot_uuid, expert_group_snapshot_uuid FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(
		&taskDir, &workDir, &status, &mode, &pipelineUUID, &pipelineSnapshotUUID, &existingExpertSnapshot)
	if err != nil {
		return err
	}
	if mode != "" && mode != ExecutionModeExpertGroup || pipelineUUID != "" || pipelineSnapshotUUID != "" || existingExpertSnapshot != "" || status == "done" {
		return ErrExecutionModeConflict
	}
	members := make([]expertgroup.Member, 0, len(group.Members)+1)
	members = append(members, *group.Leader)
	members = append(members, group.Members...)
	for _, member := range members {
		if err := executor.ValidateThinkingLevel(ctx, member.CLIType, member.ModelName, workDir, member.ThinkingLevel); err != nil {
			return err
		}
	}
	snapshotBytes, err := json.Marshal(group)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(snapshotBytes)
	snapshotUUID := uuid.NewString()
	stepDirs := make([]string, len(members))
	createdDirs := make([]string, 0, len(members))
	defer func() {
		if err != nil {
			for _, dir := range createdDirs {
				_ = os.RemoveAll(dir)
			}
		}
	}()
	for index, member := range members {
		stepDirs[index] = filepath.Join(taskDir, fmt.Sprintf("expert-%s-%s", safeDirName(member.Name), uuid.NewString()[:8]))
		if err = os.Mkdir(stepDirs[index], 0o755); err != nil {
			return fmt.Errorf("创建专家团成员目录失败: %w", err)
		}
		createdDirs = append(createdDirs, stepDirs[index])
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	leaderStepUUID := ""
	if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_expert_group_snapshots
		(uuid, task_uuid, source_expert_group_uuid, name, description, avatar, snapshot_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, snapshotUUID, taskUUID, group.UUID, group.Name, group.Description,
		group.Avatar, hex.EncodeToString(sum[:]), now); err != nil {
		return fmt.Errorf("创建专家团快照失败: %w", err)
	}
	for index, member := range members {
		stepUUID := uuid.NewString()
		if member.MemberRole == expertgroup.RoleLeader {
			leaderStepUUID = stepUUID
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gt_task_steps
			(uuid, task_uuid, source_type, source_step_id, local_step_uuid, step_key, step_order, name, description,
			 avatar, prompt_snapshot, cli_type, model_name, thinking_level, step_dir, status, execution_status, execution_mode,
			 member_role, task_expert_group_snapshot_uuid, created_at, updated_at)
			VALUES (?, ?, 'local', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'idle', 'expert_group', ?, ?, ?, ?)`,
			stepUUID, taskUUID, member.UUID, member.UUID, "expert-"+member.UUID, index, member.Name, member.Description,
			member.Avatar, member.Prompt, member.CLIType, member.ModelName, member.ThinkingLevel, stepDirs[index], member.MemberRole, snapshotUUID, now, now); err != nil {
			return fmt.Errorf("创建专家团成员快照失败: %w", err)
		}
	}
	if leaderStepUUID == "" {
		return expertgroup.ErrNotReady
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_expert_group_snapshots SET leader_task_step_uuid=? WHERE uuid=?`, leaderStepUUID, snapshotUUID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE gt_tasks SET selected_expert_group_uuid=?, expert_group_snapshot_uuid=?,
		execution_mode='expert_group', execution_tool='', selected_pipeline_uuid='', updated_at=?
		WHERE uuid=? AND execution_mode IN ('', 'expert_group') AND pipeline_snapshot_uuid='' AND expert_group_snapshot_uuid=''`,
		group.UUID, snapshotUUID, now, taskUUID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrExecutionModeConflict
	}
	return tx.Commit()
}

// ActivateExpertMember makes one frozen member the task's only active execution participant.
func (s *Service) ActivateExpertMember(ctx context.Context, taskUUID, stepUUID string) error {
	return s.activateExpertMember(ctx, taskUUID, stepUUID, false)
}

// ActivateExpertMemberForUser rejects manual activation while an automatic handoff is pending.
func (s *Service) ActivateExpertMemberForUser(ctx context.Context, taskUUID, stepUUID string) error {
	return s.activateExpertMember(ctx, taskUUID, stepUUID, true)
}

func (s *Service) activateExpertMember(ctx context.Context, taskUUID, stepUUID string, rejectRoutingPending bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_task_steps s JOIN gt_tasks t ON t.uuid=s.task_uuid
		WHERE s.task_uuid=? AND s.uuid=? AND t.execution_mode='expert_group' AND s.execution_mode='expert_group'`, taskUUID, stepUUID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("专家团成员不存在")
	}
	if rejectRoutingPending {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_task_progress
			WHERE task_uuid=? AND dispatch_status='routing_pending'`, taskUUID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrExpertRoutingPending
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND status IN ('created','running','waiting_input','stop_requested')`, taskUUID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("任务已有活动 Session")
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status='completed', updated_at=?
		WHERE task_uuid=? AND execution_mode='expert_group' AND status='active' AND uuid<>?`, now, taskUUID, stepUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET status='active', updated_at=? WHERE task_uuid=? AND uuid=?`, now, taskUUID, stepUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status=CASE WHEN status='done' THEN status ELSE 'active' END,
		current_step_uuid=?, current_step_completed=CASE WHEN status='done' THEN current_step_completed ELSE 0 END,
		started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END, updated_at=? WHERE uuid=?`, stepUUID, now, now, taskUUID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ExpertLeaderStep(ctx context.Context, taskUUID string) (string, error) {
	var stepUUID string
	err := s.db.QueryRowContext(ctx, `SELECT s.leader_task_step_uuid FROM gt_task_expert_group_snapshots s
		JOIN gt_tasks t ON t.expert_group_snapshot_uuid=s.uuid WHERE t.uuid=?`, taskUUID).Scan(&stepUUID)
	return strings.TrimSpace(stepUUID), err
}
