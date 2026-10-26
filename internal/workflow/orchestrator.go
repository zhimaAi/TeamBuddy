package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"goteams-client/internal/applog"
	"goteams-client/internal/artifactstore"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	"goteams-client/internal/storage/log"
	"goteams-client/internal/taskruntime"
)

// EventBroadcaster is the event broadcast callback type
type EventBroadcaster func(sessionUUID string, sequence int, event executor.ExecutorEvent)

// latestEventPersistIntervalMs is the throttling interval for persisting the
// latest CLI activity to gt_cli_sessions during event processing.
const latestEventPersistIntervalMs int64 = 1000

// shouldPersistSessionEvent 判定事件是否留存到执行过程回放表。
// 只留存过程类事件：思考、中间输出、工具调用、工具结果、权限请求；
// 这些事件需要供历史回放和最近动态全文查看，因此保存原始内容，不在落库层截断。
// start / usage / complete 等生命周期事件由会话状态与 token 字段承载，不重复落库。
func shouldPersistSessionEvent(eventType string) bool {
	switch eventType {
	case executor.EventThinking, executor.EventMessage, executor.EventToolCall, executor.EventToolResult, executor.EventPermission:
		return true
	default:
		return false
	}
}

func marshalSessionEvent(event executor.ExecutorEvent) ([]byte, bool, error) {
	// 增量工具结果只广播，不落库。终态结果仍保留。
	if event.ToolResultPartial || !shouldPersistSessionEvent(event.Type) {
		return nil, false, nil
	}
	payload, err := json.Marshal(event)
	return payload, true, err
}

// LiveActivity 是只存在内存里的汇总行状态，刷新页面时随事件列表下发。
type LiveActivity struct {
	Kind    string `json:"kind"`
	Attempt int    `json:"attempt,omitempty"`
	Max     int    `json:"max,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Since   int64  `json:"since"`
}

func (o *Orchestrator) LiveActivity(sessionUUID string) (LiveActivity, bool) {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	item, ok := o.liveActivity[sessionUUID]
	return item, ok
}

func (o *Orchestrator) setLiveActivity(sessionUUID string, item LiveActivity) {
	o.liveMu.Lock()
	o.liveActivity[sessionUUID] = item
	o.liveMu.Unlock()
}

func (o *Orchestrator) clearLiveActivity(sessionUUID string) {
	o.liveMu.Lock()
	delete(o.liveActivity, sessionUUID)
	o.liveMu.Unlock()
}

// StateBroadcaster is the state-change broadcast callback type
type StateBroadcaster func(taskUUID, stepKey, sessionUUID, status string)

// ActivityBroadcaster pushes the latest CLI activity (throttled) to task views,
// so running-step subtitles stay fresh without polling /progress.
type ActivityBroadcaster func(taskUUID, sessionUUID string, sequence int, eventType, content string, at int64)

// RunStepOptions are the run-step options
type RunStepOptions struct {
	TaskUUID  string
	StepUUID  string
	StepKey   string // compatibility lookup; new callers should use StepUUID
	WorkDir   string
	RequestID string
	CLIType   string // optional compatibility override
	Model     string // optional compatibility override
	// UserPrompt is the complete prompt passed into the task prompt builder.
	UserPrompt string
	// DisplayPrompt is persisted for the user-message record. When omitted,
	// UserPrompt remains the backwards-compatible display value.
	DisplayPrompt      string
	RecordType         string // initial_run | user_question
	ExecutionMode      string
	ExpertChainUUID    string
	ExpertChainRound   int
	TriggerSessionUUID string
}

// ContinueConversationOptions are the options for continuing a historical CLI conversation.
type ContinueConversationOptions struct {
	ParentSessionUUID string
	// Prompt is the complete prompt passed to the CLI. It can contain local
	// attachment paths, which are an execution detail rather than user-facing
	// conversation content.
	Prompt string
	// DisplayPrompt is persisted for the user-message record. When omitted by
	// an older caller, Prompt remains the backwards-compatible fallback.
	DisplayPrompt      string
	RequestID          string
	CLIType            string
	Model              string
	InternalHandoff    bool
	ExpertChainUUID    string
	ExpertChainRound   int
	TriggerSessionUUID string
}

type activeSession struct {
	cancel   context.CancelFunc
	adapter  executor.Adapter
	stopping bool
}

// Orchestrator is the workflow orchestrator
type Orchestrator struct {
	db         *sql.DB
	logDB      *sql.DB
	eventStore *log.EventStore

	mu             sync.RWMutex
	activeSessions map[string]*activeSession
	executionGates *taskExecutionGateRegistry
	sessionStarts  *taskExecutionGateRegistry

	eventBroadcaster    EventBroadcaster
	stateBroadcaster    StateBroadcaster
	stateListeners      []StateBroadcaster
	activityBroadcaster ActivityBroadcaster
	artifacts           *artifactstore.ArtifactStore
	liveMu              sync.Mutex
	liveActivity        map[string]LiveActivity
}

// NewOrchestrator creates the orchestrator
func NewOrchestrator(db *sql.DB, logDB *sql.DB, eventStore *log.EventStore) *Orchestrator {
	return &Orchestrator{
		db:             db,
		logDB:          logDB,
		eventStore:     eventStore,
		activeSessions: make(map[string]*activeSession),
		executionGates: newTaskExecutionGateRegistry(),
		sessionStarts:  newTaskExecutionGateRegistry(),
		artifacts:      artifactstore.Default(),
		liveActivity:   map[string]LiveActivity{},
	}
}

// SetEventBroadcaster sets the event broadcast callback
func (o *Orchestrator) SetEventBroadcaster(cb EventBroadcaster) {
	o.eventBroadcaster = cb
}

// SetStateBroadcaster sets the state-change broadcast callback
func (o *Orchestrator) SetStateBroadcaster(cb StateBroadcaster) {
	o.stateBroadcaster = cb
}

// SetActivityBroadcaster sets the latest-activity broadcast callback
func (o *Orchestrator) SetActivityBroadcaster(cb ActivityBroadcaster) {
	o.activityBroadcaster = cb
}

// AddStateListener appends an extra observer of session state changes (e.g. the
// cloud task sync push). Listeners run after the main broadcaster and must not block.
func (o *Orchestrator) AddStateListener(cb StateBroadcaster) {
	if cb == nil {
		return
	}
	o.stateListeners = append(o.stateListeners, cb)
}

// RunStep executes a step
func (o *Orchestrator) RunStep(ctx context.Context, opts RunStepOptions) (string, error) {
	release := o.executionGates.acquire(opts.TaskUUID, false)
	defer release()
	return o.runStep(ctx, opts)
}

func (o *Orchestrator) runStep(ctx context.Context, opts RunStepOptions) (string, error) {
	// 与执行切换的共享门闩分开：同任务的校验、幂等查询和创建必须串行。
	release := o.sessionStarts.acquire(opts.TaskUUID, true)
	defer release()
	// 1. Resolve the immutable task-step snapshot.
	var stepUUID, stepKey, stepCLIType, modelName, workDir, snapshotUUID, expertSnapshotUUID, executionMode string
	err := o.db.QueryRowContext(ctx,
		`SELECT s.uuid, s.step_key, s.cli_type, s.model_name, t.work_dir, s.task_pipeline_snapshot_uuid,
		        s.task_expert_group_snapshot_uuid, COALESCE(NULLIF(s.execution_mode,''), t.execution_mode)
		 FROM gt_task_steps s JOIN gt_tasks t ON t.uuid = s.task_uuid
		 WHERE s.task_uuid = ? AND ((? <> '' AND s.uuid = ?) OR (? = '' AND s.step_key = ?))`,
		opts.TaskUUID, opts.StepUUID, opts.StepUUID, opts.StepUUID, opts.StepKey).Scan(
		&stepUUID, &stepKey, &stepCLIType, &modelName, &workDir, &snapshotUUID, &expertSnapshotUUID, &executionMode)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("Agent 编排不存在: %w", err)
	}
	if err != nil {
		return "", fmt.Errorf("查询步骤失败: %w", err)
	}

	// 3. Determine CLI type: prefer the user's choice, otherwise fall back to the cloud Agent snapshot.
	cliType := strings.TrimSpace(opts.CLIType)
	if cliType == "" {
		cliType = stepCLIType
	}
	if strings.TrimSpace(opts.Model) != "" {
		modelName = strings.TrimSpace(opts.Model)
	}
	if strings.TrimSpace(opts.WorkDir) == "" {
		opts.WorkDir = workDir
	}
	opts.StepUUID = stepUUID
	opts.StepKey = stepKey
	displayPrompt := strings.TrimSpace(opts.DisplayPrompt)
	if displayPrompt == "" {
		displayPrompt = strings.TrimSpace(opts.UserPrompt)
	}
	if id, err := o.existingRequest(ctx, opts.TaskUUID, stepUUID, opts.RequestID, displayPrompt, opts.CLIType, opts.Model); id != "" || err != nil {
		return id, err
	}
	if err := o.validateRun(ctx, opts); err != nil {
		return "", err
	}
	if strings.TrimSpace(cliType) == "" || strings.TrimSpace(modelName) == "" {
		return "", ErrStepRuntimeRequired
	}
	var promptSnapshot string
	promptSnapshot, err = BuildRoundPrompt(ctx, o.db, executionMode, opts.TaskUUID, stepUUID, opts.UserPrompt)
	if err != nil {
		return "", err
	}
	execPath, execErr := resolveCLIExecutable(cliType)

	// 4. Create conversation_uuid + session_uuid and generate a continuous round number per step.
	// Each re-execution of a step creates a new conversation_uuid, and is stored as
	// it is still the next round of the step, so run_no must not be reset to 1 for a new conversation.
	conversationUUID := uuid.New().String()
	sessionUUID := sessionUUIDForRequest(opts.TaskUUID, opts.RequestID)
	if executionMode == "expert_group" {
		if strings.TrimSpace(opts.ExpertChainUUID) == "" {
			opts.ExpertChainUUID = uuid.NewString()
		}
		if opts.ExpertChainRound < 1 {
			opts.ExpertChainRound = 1
		}
	}
	now := nowMillis()
	var runNo int
	if err := o.db.QueryRow(
		`SELECT COALESCE(MAX(run_no), 0) + 1 FROM gt_cli_sessions WHERE step_uuid = ?`,
		stepUUID,
	).Scan(&runNo); err != nil {
		return "", fmt.Errorf("读取步骤轮次失败: %w", err)
	}

	recordType := opts.RecordType
	if recordType == "" {
		recordType = "initial_run"
	}
	if recordType != "initial_run" && recordType != "user_question" {
		return "", fmt.Errorf("无效进度类型")
	}
	progressUUID := uuid.New().String()
	aiProgressUUID := progressUUID
	if recordType == "user_question" {
		aiProgressUUID = uuid.New().String()
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("开启执行事务失败: %w", err)
	}
	defer tx.Rollback()
	if executionMode == "pipeline" {
		if err := initializePipelineProgress(ctx, tx, opts.TaskUUID, now); err != nil {
			return "", err
		}
	}
	if recordType == "user_question" {
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, local_step_uuid, cloud_step_id,
		 session_uuid, record_type, user_prompt, cli_type, model_name, status, execution_mode, task_expert_group_snapshot_uuid, created_at, started_at)
		SELECT ?, ?, ?, s.uuid, s.local_step_uuid, s.cloud_step_id, ?, 'user_question', ?, ?, ?, 'created', ?, ?, ?, ?
		FROM gt_task_steps s WHERE s.uuid = ?`, progressUUID, opts.TaskUUID, snapshotUUID, sessionUUID,
			displayPrompt, cliType, modelName, executionMode, expertSnapshotUUID, now, now, stepUUID)
		if err != nil {
			return "", fmt.Errorf("创建用户提问进度失败: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, local_step_uuid, cloud_step_id,
		 session_uuid, record_type, user_prompt, cli_type, model_name, status, execution_mode, task_expert_group_snapshot_uuid, created_at, started_at)
		SELECT ?, ?, ?, s.uuid, s.local_step_uuid, s.cloud_step_id, ?, ?, ?, ?, ?, 'created', ?, ?, ?, ?
		FROM gt_task_steps s WHERE s.uuid = ?`, aiProgressUUID, opts.TaskUUID, snapshotUUID, sessionUUID,
		"initial_run", "", cliType, modelName, executionMode, expertSnapshotUUID, now, now, stepUUID)
	if err != nil {
		return "", fmt.Errorf("创建任务进度失败: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_cli_sessions
		(uuid, task_uuid, step_uuid, task_step_uuid, task_progress_uuid, conversation_uuid, run_no, status,
		 cli_type, model_name, work_dir, prompt_snapshot, execution_mode, expert_chain_uuid, expert_chain_round,
		 trigger_session_uuid, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionUUID, opts.TaskUUID, stepUUID, stepUUID, aiProgressUUID, conversationUUID, runNo,
		SessionStatusCreated, cliType, modelName, opts.WorkDir, promptSnapshot, executionMode, opts.ExpertChainUUID,
		opts.ExpertChainRound, opts.TriggerSessionUUID, now, now, now)
	if err != nil {
		return "", fmt.Errorf("创建 Session 失败: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET execution_status = 'running', started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END, updated_at = ? WHERE uuid = ?`, now, now, stepUUID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status = CASE WHEN status = 'done' THEN status ELSE 'active' END, execution_status = 'running', started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END, updated_at = ? WHERE uuid = ?`, now, now, opts.TaskUUID); err != nil {
		return "", err
	}
	if err = insertRunningTaskNotification(ctx, tx, opts.TaskUUID, stepUUID, aiProgressUUID, sessionUUID, now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", fmt.Errorf("提交执行事务失败: %w", err)
	}

	// Broadcast the state change
	if o.stateBroadcaster != nil {
		o.stateBroadcaster(opts.TaskUUID, opts.StepKey, sessionUUID, SessionStatusRunning)
	}
	if execErr != nil {
		go o.failSessionWithError(sessionUUID, opts.TaskUUID, opts.StepKey, "", execErr.Error())
		return sessionUUID, nil
	}

	// 7. Start the CLI process asynchronously
	go o.runCLIProcess(context.WithoutCancel(ctx), sessionUUID, opts, cliType, execPath, promptSnapshot, false, "", modelName)

	return sessionUUID, nil
}

// ContinueConversation creates a new locally recorded round. It resumes the
// same native conversation when the CLI is unchanged and a native session ID
// exists; otherwise it starts a fresh native conversation while preserving the
// product-level conversation UUID and round sequence.
func (o *Orchestrator) ContinueConversation(ctx context.Context, opts ContinueConversationOptions) (string, error) {
	var taskUUID string
	if err := o.db.QueryRowContext(ctx, `SELECT task_uuid FROM gt_cli_sessions WHERE uuid=?`, opts.ParentSessionUUID).Scan(&taskUUID); err == sql.ErrNoRows {
		return "", fmt.Errorf("历史会话不存在")
	} else if err != nil {
		return "", fmt.Errorf("读取历史会话失败: %w", err)
	}
	release := o.executionGates.acquire(taskUUID, false)
	defer release()
	return o.continueConversation(ctx, opts)
}

// insertRunningTaskNotification 让进行中的这一轮立刻出现在对话列表。
// 切换执行方式会清掉旧通知，而完成通知要等会话结束才写；中间这段时间列表里没有
// 当前任务，页面就会停在更早的会话上，用户看不到这次的执行过程。
func insertRunningTaskNotification(ctx context.Context, tx *sql.Tx, taskUUID, stepUUID, progressUUID, sessionUUID string, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO gt_task_notifications
		(uuid, task_uuid, task_step_uuid, step_order, progress_uuid, session_uuid, terminal_status, title, summary, is_read, is_archived, created_at)
		SELECT ?, t.uuid, s.uuid, s.step_order, ?, ?, 'running', t.title || ' · ' || s.name, '正在执行', 1, 0, ?
		FROM gt_task_steps s JOIN gt_tasks t ON t.uuid = s.task_uuid
		WHERE s.uuid = ? AND t.uuid = ?`,
		uuid.NewString(), progressUUID, sessionUUID, now, stepUUID, taskUUID)
	if err != nil {
		return fmt.Errorf("创建进行中通知失败: %w", err)
	}
	return nil
}

func (o *Orchestrator) continueConversation(ctx context.Context, opts ContinueConversationOptions) (string, error) {
	prompt := strings.TrimSpace(opts.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("问题内容不能为空")
	}
	displayPrompt := strings.TrimSpace(opts.DisplayPrompt)
	if displayPrompt == "" {
		displayPrompt = prompt
	}
	var taskUUID, stepUUID, stepKey, workDir, parentStatus, conversationUUID, externalSessionID, parentCLIType, parentModelName, snapshotUUID, expertSnapshotUUID, executionMode string
	err := o.db.QueryRowContext(ctx,
		`SELECT s.task_uuid, s.step_uuid, ts.step_key, s.work_dir, s.status, s.conversation_uuid,
		        s.external_session_id, s.cli_type, s.model_name, ts.task_pipeline_snapshot_uuid,
		        ts.task_expert_group_snapshot_uuid, COALESCE(NULLIF(ts.execution_mode,''), t.execution_mode)
		 FROM gt_cli_sessions s
		 JOIN gt_task_steps ts ON ts.uuid = s.step_uuid
		 JOIN gt_tasks t ON t.uuid=s.task_uuid
		 WHERE s.uuid = ?`,
		opts.ParentSessionUUID,
	).Scan(&taskUUID, &stepUUID, &stepKey, &workDir, &parentStatus, &conversationUUID,
		&externalSessionID, &parentCLIType, &parentModelName, &snapshotUUID, &expertSnapshotUUID, &executionMode)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("历史会话不存在")
	}
	if err != nil {
		return "", fmt.Errorf("读取历史会话失败: %w", err)
	}
	release := o.sessionStarts.acquire(taskUUID, true)
	defer release()
	if id, err := o.existingRequest(ctx, taskUUID, stepUUID, opts.RequestID, displayPrompt, opts.CLIType, opts.Model); id != "" || err != nil {
		return id, err
	}
	if !IsTerminalStatus(parentStatus) {
		return "", fmt.Errorf("当前一轮仍在执行，请等待完成或先终止")
	}
	cliType, modelName, nativeSessionID, resumeNative, err := resolveContinuationRuntime(
		parentCLIType,
		parentModelName,
		externalSessionID,
		opts.CLIType,
		opts.Model,
	)
	if err != nil {
		return "", err
	}
	if cliType == "" || modelName == "" {
		return "", ErrStepRuntimeRequired
	}
	runOpts := RunStepOptions{TaskUUID: taskUUID, StepUUID: stepUUID, StepKey: stepKey,
		WorkDir: workDir, RequestID: opts.RequestID, CLIType: cliType, Model: modelName,
		UserPrompt: displayPrompt, RecordType: "user_question", ExecutionMode: executionMode,
		ExpertChainUUID: opts.ExpertChainUUID, ExpertChainRound: opts.ExpertChainRound, TriggerSessionUUID: opts.TriggerSessionUUID}
	if err = o.validateRun(ctx, runOpts); err != nil {
		return "", err
	}
	// Resume sends only the user's raw message because the native conversation
	// already carries context. Switching CLI (or lacking a native session ID)
	// starts a fresh native conversation and rebuilds the complete step prompt.
	// Both paths keep the parent's product-level conversation_uuid and advance
	// run_no, as required by the local conversation and CloudSync contract.
	storedPromptSnapshot := displayPrompt
	executionPromptSnapshot := prompt
	if !resumeNative {
		storedPromptSnapshot, err = BuildRoundPrompt(ctx, o.db, executionMode, taskUUID, stepUUID, displayPrompt)
		if err != nil {
			return "", err
		}
		executionPromptSnapshot, err = BuildRoundPrompt(ctx, o.db, executionMode, taskUUID, stepUUID, prompt)
		if err != nil {
			return "", err
		}
	}
	execPath, execErr := resolveCLIExecutable(cliType)
	now := nowMillis()
	sessionUUID := sessionUUIDForRequest(taskUUID, opts.RequestID)
	if executionMode == "expert_group" {
		if opts.ExpertChainUUID == "" {
			opts.ExpertChainUUID = uuid.NewString()
		}
		if opts.ExpertChainRound < 1 {
			opts.ExpertChainRound = 1
		}
	}
	progressUUID := uuid.NewString()   // 用户提问 progress
	aiProgressUUID := uuid.NewString() // AI 执行 progress

	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("开启继续会话事务失败: %w", err)
	}
	defer tx.Rollback()
	var runNo int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(run_no), 0) + 1 FROM gt_cli_sessions WHERE step_uuid = ?`, stepUUID).Scan(&runNo); err != nil {
		return "", fmt.Errorf("读取步骤轮次失败: %w", err)
	}
	var stepStatus, taskStatus string
	var activeCount int
	if err = tx.QueryRowContext(ctx, `SELECT ts.status, t.status,
		(SELECT COUNT(*) FROM gt_cli_sessions a WHERE a.task_uuid=t.uuid AND a.status IN ('created','running','waiting_input','stop_requested'))
		FROM gt_tasks t JOIN gt_task_steps ts ON ts.task_uuid=t.uuid
		WHERE t.uuid=? AND ts.uuid=?`, taskUUID, stepUUID).Scan(&stepStatus, &taskStatus, &activeCount); err != nil {
		return "", fmt.Errorf("重新校验任务步骤失败: %w", err)
	}
	// 越序对话不改变步骤业务状态，pending 步骤也允许继续已有会话。
	if executionMode != "pipeline" && stepStatus != "active" && stepStatus != "completed" {
		return "", fmt.Errorf("只能继续已开始或已完成的 Agent 编排")
	}
	if activeCount > 0 {
		return "", ErrTaskBusy
	}
	// 1. 用户提问 progress（record_type = user_question, user_prompt = 用户输入，显示为用户消息气泡）
	if !opts.InternalHandoff {
		_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, local_step_uuid, cloud_step_id,
		 session_uuid, record_type, user_prompt, cli_type, model_name, status, execution_mode, external_session_id, task_expert_group_snapshot_uuid, created_at, started_at)
		SELECT ?, ?, ?, s.uuid, s.local_step_uuid, s.cloud_step_id, ?, 'user_question', ?, ?, ?, 'created', ?, ?, ?, ?, ?
		FROM gt_task_steps s WHERE s.uuid = ?`, progressUUID, taskUUID, snapshotUUID, sessionUUID,
			displayPrompt, cliType, modelName, executionMode, nativeSessionID, expertSnapshotUUID, now, now, stepUUID)
		if err != nil {
			return "", fmt.Errorf("创建用户提问进度失败: %w", err)
		}
	}
	// 2. AI 执行 progress（record_type = initial_run, user_prompt = '', 显示为 AI 执行气泡）
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_task_progress
		(uuid, task_uuid, task_pipeline_snapshot_uuid, task_step_uuid, local_step_uuid, cloud_step_id,
		 session_uuid, record_type, user_prompt, cli_type, model_name, status, execution_mode, external_session_id, task_expert_group_snapshot_uuid, created_at, started_at)
		SELECT ?, ?, ?, s.uuid, s.local_step_uuid, s.cloud_step_id, ?, 'initial_run', '', ?, ?, 'created', ?, ?, ?, ?, ?
		FROM gt_task_steps s WHERE s.uuid = ?`, aiProgressUUID, taskUUID, snapshotUUID, sessionUUID,
		cliType, modelName, executionMode, nativeSessionID, expertSnapshotUUID, now, now, stepUUID)
	if err != nil {
		return "", fmt.Errorf("创建 AI 执行进度失败: %w", err)
	}
	// 3. CLI 会话记录，task_progress_uuid 指向 AI 执行 progress
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_cli_sessions
		(uuid, task_uuid, step_uuid, task_step_uuid, task_progress_uuid, conversation_uuid, parent_session_uuid,
		 run_no, status, cli_type, model_name, external_session_id, work_dir, prompt_snapshot,
		 execution_mode, expert_chain_uuid, expert_chain_round, trigger_session_uuid, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionUUID, taskUUID, stepUUID, stepUUID, aiProgressUUID, conversationUUID, opts.ParentSessionUUID,
		runNo, SessionStatusCreated, cliType, modelName, nativeSessionID, workDir, storedPromptSnapshot,
		executionMode, opts.ExpertChainUUID, opts.ExpertChainRound, opts.TriggerSessionUUID, now, now, now)
	if err != nil {
		return "", fmt.Errorf("创建继续会话 Session 失败: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gt_task_steps SET execution_status='running', updated_at=? WHERE uuid=?`, now, stepUUID); err != nil {
		return "", err
	}
	// done 任务上的补充对话不回退任务整体状态，仅刷新执行状态；其余状态恢复为 active
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET status = CASE WHEN status = 'done' THEN status ELSE 'active' END, execution_status='running', updated_at=? WHERE uuid=?`, now, taskUUID); err != nil {
		return "", err
	}
	if err = insertRunningTaskNotification(ctx, tx, taskUUID, stepUUID, aiProgressUUID, sessionUUID, now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", fmt.Errorf("提交继续会话事务失败: %w", err)
	}

	if o.stateBroadcaster != nil {
		o.stateBroadcaster(taskUUID, stepKey, sessionUUID, SessionStatusRunning)
	}
	if execErr != nil {
		go o.failSessionWithError(sessionUUID, taskUUID, stepKey, nativeSessionID, execErr.Error())
		return sessionUUID, nil
	}
	go o.runCLIProcess(context.WithoutCancel(ctx), sessionUUID, runOpts, cliType, execPath, executionPromptSnapshot, resumeNative, nativeSessionID, modelName)
	return sessionUUID, nil
}

func resolveContinuationRuntime(parentCLIType, parentModelName, externalSessionID, requestedCLIType, requestedModel string) (cliType, modelName, nativeSessionID string, resumeNative bool, err error) {
	parentCLIType = strings.TrimSpace(parentCLIType)
	parentModelName = strings.TrimSpace(parentModelName)
	requestedCLIType = strings.TrimSpace(requestedCLIType)
	requestedModel = strings.TrimSpace(requestedModel)

	cliType = requestedCLIType
	if cliType == "" {
		cliType = parentCLIType
	}
	modelName = requestedModel
	if modelName == "" {
		modelName = parentModelName
	}
	if requestedCLIType != "" && requestedCLIType != parentCLIType && requestedModel == "" {
		return "", "", "", false, fmt.Errorf("切换 CLI 时必须同时选择模型")
	}

	resumeNative = cliType == parentCLIType && strings.TrimSpace(externalSessionID) != ""
	if resumeNative {
		nativeSessionID = strings.TrimSpace(externalSessionID)
	}
	return cliType, modelName, nativeSessionID, resumeNative, nil
}

// runCLIProcess runs the CLI process asynchronously
func (o *Orchestrator) runCLIProcess(
	ctx context.Context,
	sessionUUID string,
	opts RunStepOptions,
	cliType, execPath, prompt string,
	resume bool,
	externalSessionID string,
	model string,
) {
	// Create a cancellable context
	innerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	o.mu.Lock()
	o.activeSessions[sessionUUID] = &activeSession{cancel: cancel}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.activeSessions, sessionUUID)
		o.mu.Unlock()
	}()

	var initialStopRequestedAt int64
	_ = o.db.QueryRow(`SELECT stop_requested_at FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&initialStopRequestedAt)
	if initialStopRequestedAt > 0 {
		o.finishSession(sessionUUID, opts.TaskUUID, opts.StepKey, SessionStatusStopped, externalSessionID, 0, 0, "", nil)
		return
	}

	// Create the adapter：按 CLI 类型查注册表，新增 CLI 无需改动编排层
	adapter, ok := newAdapter(cliType)
	if !ok {
		o.failSessionWithError(sessionUUID, opts.TaskUUID, opts.StepKey, "", "不支持的 CLI 类型: "+cliType)
		return
	}
	o.mu.Lock()
	shouldStopBeforeStart := false
	if active := o.activeSessions[sessionUUID]; active != nil {
		if active.stopping {
			shouldStopBeforeStart = true
		} else {
			active.adapter = adapter
		}
	}
	o.mu.Unlock()
	if shouldStopBeforeStart {
		cancel()
		o.finishSession(sessionUUID, opts.TaskUUID, opts.StepKey, SessionStatusStopped, externalSessionID, 0, 0, "", nil)
		return
	}

	activePrompt := prompt
	activeResume := resume
	activeExternalID := strings.TrimSpace(externalSessionID)
	presetID := ""
	locale := i18n.LocaleFromContext(innerCtx)
	var settledInput, settledOutput int
	var responseContent string
	eventSequence := 0
	openTools := map[string]*resumeTool{}
	stats := newSessionStats()
	watchdog := executor.DefaultWatchdogConfig(unattendedExecution(opts.ExecutionMode))

	for attemptNo := 0; ; attemptNo++ {
		var stopRequestedAt int64
		_ = o.db.QueryRow(`SELECT stop_requested_at FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&stopRequestedAt)
		if stopRequestedAt > 0 {
			o.clearLiveActivity(sessionUUID)
			o.finishSession(sessionUUID, opts.TaskUUID, opts.StepKey, SessionStatusStopped, activeExternalID, settledInput, settledOutput, responseContent, stats)
			return
		}
		if attemptNo == 0 && !activeResume && activeExternalID == "" && executor.SupportsPresetSession(cliType) {
			presetID = uuid.NewString()
			activeExternalID = presetID
			o.db.Exec(`UPDATE gt_cli_sessions SET external_session_id = ?, updated_at = ? WHERE uuid = ?`, presetID, nowMillis(), sessionUUID)
		}
		if attemptNo > 0 {
			presetID = ""
			applog.Info("[Orchestrator] cli_auto_resume_total", "session", sessionUUID, "cli", cliType, "attempt", attemptNo, "workspace", workspaceFingerprint(opts.WorkDir))
		}

		// Run
		applog.Info("[Orchestrator] CLI 开始执行",
			"session", sessionUUID,
			"cliType", cliType,
			"model", model,
			"workDir", opts.WorkDir,
			"resume", activeResume,
			"attempt", attemptNo,
			"taskUUID", opts.TaskUUID,
			"stepKey", opts.StepKey,
			"externalSessionID", activeExternalID,
			"promptBytes", len(activePrompt),
		)
		runOptions := executor.RunOptions{
			ExecPath:        execPath,
			Prompt:          activePrompt,
			WorkDir:         opts.WorkDir,
			ModelProfile:    model,
			PresetSessionID: presetID,
			Watchdog:        watchdog,
		}
		eventCh, err := startAdapterConversation(innerCtx, adapter, runOptions, activeResume, activeExternalID)
		if err != nil {
			applog.Warn("[Orchestrator] 启动 CLI 失败", "error", err)
			o.clearLiveActivity(sessionUUID)
			if attemptNo > 0 {
				o.finishSession(sessionUUID, opts.TaskUUID, opts.StepKey, SessionStatusInterrupted, activeExternalID, settledInput, settledOutput, responseContent, nil)
				return
			}
			o.failSessionWithError(sessionUUID, opts.TaskUUID, opts.StepKey, "", err.Error())
			return
		}
		// 启动广播（startSession/ContinueConversation）发生在状态翻转之前；翻转后需补发广播，
		// 否则前端在 WS 推送模式下不轮询，会一直停留在 created（等待执行）。
		// 仅当本次 UPDATE 真正把 created 翻转为 running 时广播，避免与 StopSession 竞争时误报。
		flipped, err := o.db.Exec(
			`UPDATE gt_cli_sessions SET status = ?, updated_at = ? WHERE uuid = ? AND status = ?`,
			SessionStatusRunning, nowMillis(), sessionUUID, SessionStatusCreated,
		)
		o.db.Exec(`UPDATE gt_task_progress SET status = 'running' WHERE uuid = (SELECT task_progress_uuid FROM gt_cli_sessions WHERE uuid = ?) AND status = 'created'`, sessionUUID)
		if err == nil {
			if affected, err := flipped.RowsAffected(); err == nil && affected > 0 && o.stateBroadcaster != nil {
				o.stateBroadcaster(opts.TaskUUID, opts.StepKey, sessionUUID, SessionStatusRunning)
			}
		}

		applog.Info("[Orchestrator] CLI 会话已启动，等待输出",
			"session", sessionUUID,
			"cliType", cliType,
			"model", model,
		)

		// Handle events
		sessionID := ""
		// Token accounting uses the adapter's final complete event (each adapter has already summed this execution internally).
		// Mid-way usage events are only a fallback: used only when the adapter aborts abnormally and no complete is available,
		// so they are accumulated separately in usage* variables to avoid double-counting against the complete total.
		usageInputTokens := 0
		usageOutputTokens := 0
		inputTokens := 0
		outputTokens := 0
		completeTokensSeen := false
		var terminalEvent executor.ExecutorEvent
		lastPartialAt := map[string]time.Time{}

		// Latest CLI activity (tool call / output) for the progress panel.
		// Persisted with throttling to avoid a DB write per event; the final event
		// is flushed after the loop so the panel always shows the last activity.
		latestEventType := ""
		latestEventContent := ""
		latestEventAt := int64(0)
		lastPersistedEventAt := int64(0)
		persistLatestEvent := func() {
			if latestEventAt <= lastPersistedEventAt || latestEventContent == "" {
				return
			}
			content := truncate(latestEventContent, 300)
			o.db.Exec(
				`UPDATE gt_cli_sessions SET latest_event_type = ?, latest_event_content = ?, latest_event_at = ?, updated_at = ? WHERE uuid = ?`,
				latestEventType, content, latestEventAt, nowMillis(), sessionUUID,
			)
			lastPersistedEventAt = latestEventAt
		}
		trackLatestEvent := func(eventType, content string) {
			content = strings.TrimSpace(content)
			if content == "" {
				return
			}
			latestEventType = eventType
			latestEventContent = content
			latestEventAt = nowMillis()
			if latestEventAt-lastPersistedEventAt >= latestEventPersistIntervalMs {
				persistLatestEvent()
			}
		}

		// 看板卡片展示最新一条过程事件。该广播独立于会话的 latest_event_* 快照，
		// 保证工具结果和权限请求也能按真实顺序成为最近动态。
		latestBoardEventType := ""
		latestBoardEventContent := ""
		latestBoardEventAt := int64(0)
		latestBoardEventSequence := 0
		lastBroadcastEventAt := int64(0)
		lastBroadcastSequence := 0
		broadcastLatestBoardEvent := func() {
			if latestBoardEventSequence <= lastBroadcastSequence {
				return
			}
			if o.activityBroadcaster != nil {
				o.activityBroadcaster(opts.TaskUUID, sessionUUID, latestBoardEventSequence, latestBoardEventType,
					truncate(latestBoardEventContent, 300), latestBoardEventAt)
			}
			lastBroadcastEventAt = latestBoardEventAt
			lastBroadcastSequence = latestBoardEventSequence
		}
		trackLatestBoardEvent := func(eventType, content string) {
			content = strings.TrimSpace(content)
			latestBoardEventType = eventType
			latestBoardEventContent = content
			latestBoardEventAt = nowMillis()
			latestBoardEventSequence = eventSequence
			if latestBoardEventAt-lastBroadcastEventAt >= latestEventPersistIntervalMs {
				broadcastLatestBoardEvent()
			}
		}

		// 执行过程回放表：思考/工具调用事件按序写入，供动态区展开回看与 WS 补发
		eventStore := log.NewEventStore(o.db)

		// 会话级执行统计（工具调用/文件/行数），尽力而为解析，结束时随 finishSession 落库
		lastPersistedInput := 0
		lastPersistedOutput := 0
		applyTokenEvent := func(event executor.ExecutorEvent) {
			if event.Type == executor.EventComplete {
				// complete 携带本次执行的合计，覆盖中途累加值，避免重复计算。
				inputTokens = event.InputTokens
				outputTokens = event.OutputTokens
				completeTokensSeen = true
				return
			}
			if event.InputTokens > 0 {
				usageInputTokens += event.InputTokens
			}
			if event.OutputTokens > 0 {
				usageOutputTokens += event.OutputTokens
			}
		}
		persistTokens := func() {
			in, out := usageInputTokens, usageOutputTokens
			if completeTokensSeen {
				in, out = inputTokens, outputTokens
			}
			if (in == lastPersistedInput && out == lastPersistedOutput) || (in <= 0 && out <= 0) {
				return
			}
			o.persistSessionTokenEstimate(sessionUUID, settledInput+in, settledOutput+out)
			lastPersistedInput, lastPersistedOutput = in, out
		}

		for event := range eventCh {
			// After termination, the adapter may still return the native session ID in the final complete/error event.
			// Even when stopping persistence and broadcast of other events, keep that ID first so the user can continue from a terminated round.
			if event.SessionID != "" {
				sessionID = event.SessionID
				o.db.Exec(
					`UPDATE gt_cli_sessions SET external_session_id = ?, updated_at = ? WHERE uuid = ?`,
					sessionID, nowMillis(), sessionUUID,
				)
			}
			noteResumeTool(openTools, event)
			eventError := strings.TrimSpace(event.Error)
			if eventError != "" && !executor.ExitClass(event.ExitClass).Resumable() {
				o.db.Exec(
					`UPDATE gt_cli_sessions SET error_message = ?, updated_at = ? WHERE uuid = ? AND stop_requested_at = 0`,
					truncate(eventError, 8192), nowMillis(), sessionUUID,
				)
			}
			if event.ToolResultPartial {
				last := lastPartialAt[event.ToolUseID]
				if !last.IsZero() && time.Since(last) < 500*time.Millisecond && len(event.Content) < 16*1024 {
					continue
				}
				lastPartialAt[event.ToolUseID] = time.Now()
			}

			o.mu.RLock()
			active := o.activeSessions[sessionUUID]
			stopping := active != nil && active.stopping
			o.mu.RUnlock()
			if stopping {
				// 停止后不再把缓冲事件刷到界面，但 Token、工具统计和最终可见回复仍要记下来。
				// 切换执行方式会立刻封存这一次运行，漏记就会让云端 Token 变成 0。
				applyTokenEvent(event)
				if event.Type == executor.EventMessage && strings.TrimSpace(event.Content) != "" {
					responseContent = strings.TrimSpace(event.Content)
				}
				switch event.Type {
				case executor.EventToolCall:
					stats.observeToolCall(strings.TrimSpace(event.Content))
				case executor.EventToolResult:
					stats.observeToolResult(strings.TrimSpace(event.Content))
				}
				persistTokens()
				if eventError != "" {
					o.recomputeSession(opts.TaskUUID, opts.StepKey)
				}
				continue
			}

			// 过程事件（思考 / 中间输出 / 工具调用 / 工具结果 / 权限请求）留存到
			// gt_session_events（sequence 与广播一致），供动态区“执行过程”历史回放与
			// WS 断线补发。最终输出仍以 final_result 为准：前端在回放时会排除该轮
			// 最后一条 message，避免同一条回答在过程与结论里出现两次。
			eventSequence++
			if !event.ToolResultPartial && o.artifacts != nil &&
				(event.Type == executor.EventMessage || event.Type == executor.EventThinking || event.Type == executor.EventToolResult) {
				event.Content = o.artifacts.Put(sessionUUID, int64(eventSequence), event.Content)
			}
			if payload, persist, marshalErr := marshalSessionEvent(event); marshalErr != nil {
				applog.Warn("[Orchestrator] 执行过程事件序列化失败", "session", sessionUUID, "error", marshalErr)
			} else if persist {
				if err := eventStore.Append(log.EventLogEntry{
					SessionUUID: sessionUUID,
					Sequence:    eventSequence,
					EventType:   event.Type,
					Payload:     string(payload),
				}); err != nil {
					applog.Warn("[Orchestrator] 执行过程事件写入失败", "session", sessionUUID, "error", err)
				}
			}
			// 先落库再广播，断线客户端才能从同一 sequence 补拉已看到的事件。
			if o.eventBroadcaster != nil {
				o.eventBroadcaster(sessionUUID, eventSequence, event)
			}

			// Log CLI output
			content := strings.TrimSpace(event.Content)
			switch event.Type {
			case executor.EventThinking, executor.EventMessage, executor.EventToolCall, executor.EventToolResult:
				o.clearLiveActivity(sessionUUID)
			}
			switch event.Type {
			case executor.EventActivity:
				info := event.Activity
				item := LiveActivity{Kind: event.Content, Since: nowMillis()}
				if info != nil {
					if info.Kind != "" {
						item.Kind = info.Kind
					}
					item.Attempt = info.Attempt
					item.Max = info.Max
					item.Detail = info.Detail
				}
				o.setLiveActivity(sessionUUID, item)
				continue
			case executor.EventMessage:
				if content != "" {
					applog.Debug("[Orchestrator] CLI 消息输出", "session", sessionUUID, "content", truncate(content, 2048))
					trackLatestEvent(executor.EventMessage, content)
				}
				trackLatestBoardEvent(executor.EventMessage, content)
			case executor.EventThinking:
				applog.Debug("[Orchestrator] CLI 思考过程", "session", sessionUUID, "content", truncate(content, 1024))
				trackLatestEvent(executor.EventThinking, content)
				trackLatestBoardEvent(executor.EventThinking, content)
			case executor.EventToolCall:
				applog.Debug("[Orchestrator] CLI 工具调用", "session", sessionUUID, "content", truncate(content, 1024))
				trackLatestEvent(executor.EventToolCall, content)
				trackLatestBoardEvent(executor.EventToolCall, content)
				stats.observeToolCall(content)
			case executor.EventToolResult:
				applog.Debug("[Orchestrator] CLI 工具结果", "session", sessionUUID, "content", truncate(content, 1024))
				if !event.ToolResultInterrupted {
					trackLatestEvent(executor.EventToolResult, content)
					trackLatestBoardEvent(executor.EventToolResult, content)
				}
				stats.observeToolResult(content)
				// 工具结果之后模型可能长时间没有新事件，立即刷新看板，避免继续显示“正在调用工具”。
				broadcastLatestBoardEvent()
			case executor.EventPermission:
				// 非交互模式下 CLI 会停下来等待授权；不上报活动行时，界面表现为「一直在跑」。
				applog.Info("[Orchestrator] CLI 请求工具权限", "session", sessionUUID, "content", truncate(content, 512))
				trackLatestEvent(executor.EventPermission, content)
				trackLatestBoardEvent(executor.EventPermission, content)
				// 权限请求之后 CLI 可能长期没有新事件，不能等下一次节流刷新或执行结束。
				broadcastLatestBoardEvent()
			case executor.EventUsage:
				applog.Debug("[Orchestrator] CLI Token 用量", "session", sessionUUID, "inputTokens", event.InputTokens, "outputTokens", event.OutputTokens)
			}

			// Mid-way usage events accumulate as a fallback. A complete event replaces them.
			applyTokenEvent(event)
			persistTokens()
			// Keep only the latest AI-visible message in memory; finishSession writes it
			// once as the final local result. Intermediate events are never persisted or uploaded.
			if event.Type == executor.EventMessage && strings.TrimSpace(event.Content) != "" {
				responseContent = strings.TrimSpace(event.Content)
			}

			// Completion event
			if event.Type == executor.EventComplete || event.Type == executor.EventError {
				if event.Type == executor.EventError {
					applog.Error("[Orchestrator] CLI 返回错误事件",
						"session", sessionUUID,
						"error", event.Error,
						"class", event.ExitClass,
						"responseContent", truncate(responseContent, 2048),
					)
				}
				terminalEvent = event
				if event.SessionID != "" {
					sessionID = event.SessionID
				}
				if event.Type == executor.EventComplete && strings.TrimSpace(event.Content) != "" {
					responseContent = strings.TrimSpace(event.Content)
				}
				// 完成消息可立即展示，但 adapter 关闭通道才表示受管进程已收尾。
				// 保持执行门闩，避免下一轮与旧 CLI 同时写同一原生会话。
				continue
			}
		}

		// Flush the last activity so the progress panel keeps the latest event
		persistLatestEvent()
		broadcastLatestBoardEvent()

		class := executor.ExitClass(terminalEvent.ExitClass)
		if terminalEvent.Type == executor.EventComplete && class == "" {
			class = executor.ExitCompleted
		}
		if class == "" {
			class = executor.ExitCrash
		}
		var stopAt int64
		_ = o.db.QueryRow(`SELECT stop_requested_at FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&stopAt)
		if stopAt > 0 {
			class = executor.ExitUserStopped
		}
		in, out := usageInputTokens, usageOutputTokens
		if completeTokensSeen {
			in, out = inputTokens, outputTokens
		}
		settledInput += in
		settledOutput += out
		if sessionID != "" {
			activeExternalID = sessionID
		}
		canResume := class.Resumable() && attemptNo == 0 && resumeEnabled() && innerCtx.Err() == nil &&
			strings.TrimSpace(activeExternalID) != "" && len(openTools) == 0
		if canResume {
			o.emitInterruptedTools(sessionUUID, &eventSequence, openTools, attemptNo+1)
			o.setLiveActivity(sessionUUID, LiveActivity{Kind: executor.ActivityReconnecting, Since: nowMillis(), Detail: "resume"})
			activePrompt = renderResumePrompt(locale, openTools)
			activeResume = true
			openTools = map[string]*resumeTool{}
			continue
		}

		status := SessionStatusFailed
		message := strings.TrimSpace(terminalEvent.Error)
		switch class {
		case executor.ExitCompleted:
			status = SessionStatusSuccess
			message = ""
		case executor.ExitUserStopped:
			status = SessionStatusStopped
		case executor.ExitLaunchFailed, executor.ExitFatalUpstream, executor.ExitFatalCLI:
			status = SessionStatusFailed
		default:
			status = SessionStatusInterrupted
			if message == "" || class == executor.ExitNoTerminalClean {
				if strings.TrimSpace(activeExternalID) == "" {
					message = i18n.WithLocale(locale, "cli_resume_no_session")
				} else {
					message = i18n.WithLocale(locale, "cli_resume_interrupted")
				}
			}
		}
		if stopAt > 0 {
			status = SessionStatusStopped
			// Terminating the process deliberately can truncate its normal protocol
			// terminator. Keep the explicit stop reason rather than a decoder error.
			message = ""
		}
		if message != "" && status != SessionStatusSuccess {
			o.db.Exec(
				`UPDATE gt_cli_sessions SET error_message = ?, updated_at = ? WHERE uuid = ? AND stop_requested_at = 0`,
				truncate(message, 8192), nowMillis(), sessionUUID,
			)
		}
		o.clearLiveActivity(sessionUUID)
		o.finishSession(sessionUUID, opts.TaskUUID, opts.StepKey, status, activeExternalID, settledInput, settledOutput, responseContent, stats)
		applog.Info("[Orchestrator] cli_turn_total",
			"session", sessionUUID,
			"cli", cliType,
			"attempts", attemptNo+1,
			"exit_class", string(class),
			"status", status,
			"responseContent", truncate(responseContent, 2048),
		)
		return
	}
}

// startAdapterConversation explicitly chooses new or resume conversation by call semantics.
//
// Resuming can no longer rely on whether externalSessionID is empty to implicitly downgrade to a new conversation; once the caller declares resume
// it must fail outright if the native session ID is missing, to avoid Codex/Claude/CodeBuddy silently creating a new session.
func startAdapterConversation(
	ctx context.Context,
	adapter executor.Adapter,
	runOptions executor.RunOptions,
	resume bool,
	externalSessionID string,
) (<-chan executor.ExecutorEvent, error) {
	if !resume {
		return adapter.NewConversation(ctx, runOptions)
	}

	externalSessionID = strings.TrimSpace(externalSessionID)
	if externalSessionID == "" {
		return nil, fmt.Errorf("恢复 CLI 对话失败：原生会话 ID 为空")
	}
	return adapter.ResumeConversation(ctx, executor.ResumeOptions{
		RunOptions:        runOptions,
		ExternalSessionID: externalSessionID,
	})
}

// recomputeSession updates the derived step and task execution states after a session change.
func (o *Orchestrator) recomputeSession(taskUUID, stepKey string) {
	tx, err := o.db.Begin()
	if err != nil {
		applog.Warn("[Orchestrator] 开启 Session 聚合事务失败", "error", err)
		return
	}
	defer tx.Rollback()
	if err := RecomputeStepStatus(tx, taskUUID, stepKey); err != nil {
		applog.Warn("[Orchestrator] 聚合 Session 步骤状态失败", "error", err)
		return
	}
	if err := RecomputeTaskStatus(tx, taskUUID); err != nil {
		applog.Warn("[Orchestrator] 聚合 Session 任务状态失败", "error", err)
		return
	}
	if err := tx.Commit(); err != nil {
		applog.Warn("[Orchestrator] 提交 Session 聚合状态失败", "error", err)
	}
}

// latestVisibleResponseContent reads the last AI-visible reply of this round from the local event log.
// On manual termination the execution goroutine may not reach finishSession in time, so the in-memory responseContent alone cannot be relied upon.
func (o *Orchestrator) latestVisibleResponseContent(sessionUUID string) (string, error) {
	if o.logDB == nil {
		return "", nil
	}

	var response sql.NullString
	err := o.logDB.QueryRow(
		`SELECT trim(json_extract(e.payload_json, '$.content'))
		 FROM gt_session_events e
		 WHERE e.session_uuid = ?
		   AND e.event_type = ?
		   AND json_valid(e.payload_json)
		   AND trim(COALESCE(json_extract(e.payload_json, '$.content'), '')) <> ''
		 ORDER BY e.sequence DESC
		 LIMIT 1`,
		sessionUUID, executor.EventMessage,
	).Scan(&response)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 Session 最终可见回复失败: %w", err)
	}
	return response.String, nil
}

// ensureSessionSummary idempotently writes the session summary needed for the cloud snapshot.
func (o *Orchestrator) ensureSessionSummary(sessionUUID, stepKey, responseContent string, inputTokens, outputTokens int, createdAt int64) error {
	_, err := o.db.Exec(
		`INSERT INTO gt_conversation_summaries
		 (uuid, conversation_uuid, session_uuid, step_key, round_no, request_summary, response_summary,
		  token_input, token_output, token_total, duration_ms, created_at)
		 SELECT ?, s.conversation_uuid, s.uuid, ?, s.run_no, substr(s.prompt_snapshot, 1, 8192), ?,
		        ?, ?, ?, s.duration_ms, ?
		 FROM gt_cli_sessions s
		 WHERE s.uuid = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM gt_conversation_summaries cs WHERE cs.session_uuid = s.uuid
		   )`,
		uuid.New().String(), stepKey, truncate(responseContent, 16*1024),
		inputTokens, outputTokens, inputTokens+outputTokens, createdAt, sessionUUID,
	)
	return err
}

// failSessionWithError records the failure reason and finishes the Session as failed.
//
// It is used by the synchronous failure paths in runCLIProcess (adapter creation /
// CLI start failure or unsupported CLI type), where
// the adapter cannot produce an error event on the event channel. Without this, the
// task execution window only sees the "failed" state change with no failure details.
func (o *Orchestrator) failSessionWithError(sessionUUID, taskUUID, stepKey, externalSessionID, message string) {
	message = strings.TrimSpace(message)
	if message != "" {
		applog.Error("[Orchestrator] CLI 执行失败",
			"session", sessionUUID,
			"taskUUID", taskUUID,
			"stepKey", stepKey,
			"error", message,
		)
		o.db.Exec(
			`UPDATE gt_cli_sessions SET error_message = ?, updated_at = ? WHERE uuid = ?`,
			truncate(message, 8192), nowMillis(), sessionUUID,
		)
		event := executor.ExecutorEvent{
			Type:      executor.EventError,
			Error:     message,
			Timestamp: nowMillis(),
		}
		if o.eventBroadcaster != nil {
			o.eventBroadcaster(sessionUUID, 1, event)
		}
	}
	o.finishSession(sessionUUID, taskUUID, stepKey, SessionStatusFailed, externalSessionID, 0, 0, "", nil)
}

// finishSession completes the Session and updates its status
func (o *Orchestrator) finishSession(sessionUUID, taskUUID, stepKey, status, externalSessionID string, inputTokens, outputTokens int, responseContent string, stats *sessionStats) {
	if stats == nil {
		// Failure/stop paths outside the event loop carry no collected stats.
		stats = newSessionStats()
	}
	now := nowMillis()
	var errorMessage string
	_ = o.db.QueryRow(`SELECT error_message FROM gt_cli_sessions WHERE uuid = ?`, sessionUUID).Scan(&errorMessage)
	finalResult := composeSessionFinalResult(status, responseContent, errorMessage)
	tx, err := o.db.Begin()
	if err != nil {
		applog.Warn("[Orchestrator] 开启完成事务失败", "error", err)
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE gt_cli_sessions
		 SET status = ?, result_status = ?, final_result = ?,
		     external_session_id = CASE WHEN ? <> '' THEN ? ELSE external_session_id END,
		     input_tokens = CASE WHEN ? > 0 THEN ? ELSE input_tokens END,
		     output_tokens = CASE WHEN ? > 0 THEN ? ELSE output_tokens END,
		     total_tokens = CASE WHEN ? > 0 THEN ? ELSE total_tokens END,
		     tool_call_count = ?, files_changed = ?, lines_added = ?, lines_deleted = ?,
		     finished_at = ?, duration_ms = CASE WHEN started_at > 0 THEN ? - started_at ELSE 0 END, updated_at = ?
		 WHERE uuid = ? AND status IN ('created', 'running', 'waiting_input', 'stop_requested')`,
		status, status, truncate(finalResult, 64*1024), externalSessionID, externalSessionID,
		inputTokens, inputTokens, outputTokens, outputTokens, inputTokens+outputTokens, inputTokens+outputTokens,
		stats.toolCalls, len(stats.files), stats.linesAdded, stats.linesDeleted,
		now, now, now, sessionUUID)
	if err != nil {
		applog.Warn("[Orchestrator] 更新 Session 状态失败", "error", err)
		return
	}
	rows, err := result.RowsAffected()
	if err != nil {
		applog.Warn("[Orchestrator] 获取影响行数失败", "error", err)
	} else if rows == 0 {
		// Already terminal; do not overwrite
		return
	}

	if _, err = tx.Exec(`UPDATE gt_task_progress SET status = ?, finished_at = ?,
		final_result = CASE WHEN record_type = 'user_question' THEN final_result ELSE ? END,
		external_session_id = CASE WHEN ? <> '' THEN ? ELSE external_session_id END
		WHERE session_uuid = ?`, status, now, truncate(finalResult, 64*1024), externalSessionID, externalSessionID, sessionUUID); err != nil {
		applog.Warn("[Orchestrator] 更新任务进度失败", "error", err)
		return
	}
	if _, err = tx.Exec(`UPDATE gt_task_progress
		SET dispatch_status='routing_pending', dispatch_message=?
		WHERE session_uuid=? AND record_type<>'user_question'
		  AND EXISTS (SELECT 1 FROM gt_cli_sessions s WHERE s.uuid=? AND s.execution_mode='expert_group')`,
		expertDispatchCodeRoutingPending, sessionUUID, sessionUUID); err != nil {
		applog.Warn("[Orchestrator] 标记专家团自动路由失败", "error", err)
		return
	}
	if err = RecomputeStepStatus(tx, taskUUID, stepKey); err != nil {
		applog.Warn("[Orchestrator] 重算 Step 状态失败", "error", err)
		return
	}
	if _, err = tx.Exec(`UPDATE gt_task_steps SET output_summary = CASE WHEN ? = 'success' THEN ? ELSE output_summary END,
		error_summary = CASE WHEN ? = 'success' THEN '' ELSE ? END, updated_at = ?
		WHERE task_uuid = ? AND step_key = ?`, status, truncate(finalResult, 64*1024), status, truncate(finalResult, 8192), now, taskUUID, stepKey); err != nil {
		return
	}
	if err = RecomputeTaskStatus(tx, taskUUID); err != nil {
		applog.Warn("[Orchestrator] 重算 Task 状态失败", "error", err)
		return
	}
	// done 任务上的补充对话失败时，不得把任务整体回退为 blocked
	if _, err = tx.Exec(`UPDATE gt_tasks SET status = CASE WHEN ? = 'success' OR status = 'done' THEN status ELSE 'blocked' END, updated_at = ? WHERE uuid = ?`, status, now, taskUUID); err != nil {
		applog.Warn("[Orchestrator] 更新任务业务状态失败", "error", err)
		return
	}
	notificationResult, err := tx.Exec(`UPDATE gt_task_notifications
		SET terminal_status=?, summary=?, is_read=0, read_at=0, created_at=?
		WHERE session_uuid=?`, status, truncate(finalResult, 512), now, sessionUUID)
	if err != nil {
		applog.Warn("[Orchestrator] 更新任务通知失败", "error", err)
		return
	}
	notificationCount, err := notificationResult.RowsAffected()
	if err != nil {
		applog.Warn("[Orchestrator] 读取任务通知更新结果失败", "error", err)
		return
	}
	if notificationCount == 0 {
		_, err = tx.Exec(`INSERT INTO gt_task_notifications
			(uuid, task_uuid, task_step_uuid, step_order, progress_uuid, session_uuid, terminal_status, title, summary, created_at)
			SELECT ?, s.task_uuid, s.uuid, s.step_order, p.uuid, ?, ?,
			       t.title || ' · ' || s.name, ?, ?
			FROM gt_cli_sessions cs
			JOIN gt_task_progress p ON p.uuid = cs.task_progress_uuid
			JOIN gt_task_steps s ON s.uuid = cs.step_uuid
			JOIN gt_tasks t ON t.uuid = cs.task_uuid
			WHERE cs.uuid = ?`, uuid.New().String(), sessionUUID, status, truncate(finalResult, 512), now, sessionUUID)
		if err != nil {
			applog.Warn("[Orchestrator] 创建任务通知失败", "error", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		applog.Warn("[Orchestrator] 提交完成事务失败", "error", err)
		return
	}

	// Persist the conversation summary consumed by the cloud task sync (idempotent per
	// session). Failed sessions fall back to the final result text so the cloud-side
	// assistant message is never empty.
	summaryContent := strings.TrimSpace(responseContent)
	if summaryContent == "" {
		summaryContent = finalResult
	}
	summaryIn, summaryOut := inputTokens, outputTokens
	if summaryIn <= 0 && summaryOut <= 0 {
		_ = o.db.QueryRow(`SELECT COALESCE(input_tokens,0), COALESCE(output_tokens,0) FROM gt_cli_sessions WHERE uuid=?`, sessionUUID).Scan(&summaryIn, &summaryOut)
	}
	if err := o.ensureSessionSummary(sessionUUID, stepKey, summaryContent, summaryIn, summaryOut, now); err != nil {
		applog.Warn("[Orchestrator] 写入会话摘要失败", "session", sessionUUID, "error", err)
	} else if _, bumpErr := o.db.Exec(
		// 摘要落库后再抬高水位。否则周期同步可能先把还没有执行过程的空会话标成已上传。
		`UPDATE gt_cli_sessions SET updated_at = MAX(updated_at + 1, ?) WHERE uuid = ?`, nowMillis(), sessionUUID); bumpErr != nil {
		applog.Warn("[Orchestrator] 刷新会话同步水位失败", "session", sessionUUID, "error", bumpErr)
	}

	// Broadcast the state change
	if o.stateBroadcaster != nil {
		o.stateBroadcaster(taskUUID, stepKey, sessionUUID, status)
	}
	for _, listener := range o.stateListeners {
		listener(taskUUID, stepKey, sessionUUID, status)
	}
	go o.routeExpertCompletion(sessionUUID, taskUUID, status, finalResult)
}

func truncate(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// StopActiveProcesses 在进程退出时回收仍在跑的 CLI，避免留下继续改工作区的孤儿进程。
func (o *Orchestrator) StopActiveProcesses() {
	o.mu.RLock()
	adapters := make([]executor.Adapter, 0, len(o.activeSessions))
	for _, active := range o.activeSessions {
		if active != nil && active.adapter != nil {
			adapters = append(adapters, active.adapter)
		}
	}
	o.mu.RUnlock()
	for _, adapter := range adapters {
		_ = adapter.Stop()
	}
}

// StopSession stops the session
func (o *Orchestrator) StopSession(ctx context.Context, sessionUUID string) error {
	var status, taskUUID, stepKey string
	err := o.db.QueryRow(
		`SELECT s.status, s.task_uuid, ts.step_key
		 FROM gt_cli_sessions s
		 JOIN gt_task_steps ts ON ts.uuid = s.step_uuid
		 WHERE s.uuid = ?`,
		sessionUUID,
	).Scan(&status, &taskUUID, &stepKey)
	if err != nil {
		return fmt.Errorf("查询 Session 失败: %w", err)
	}
	if IsTerminalStatus(status) {
		return nil // Already terminal
	}

	now := nowMillis()

	// First enter "terminating", but must not be written as stopped prematurely. Only after the process tree confirms termination,
	// is the final state committed; otherwise the UI would again show the false "terminated but still running in background" state.
	result, err := o.db.ExecContext(
		ctx,
		`UPDATE gt_cli_sessions
		 SET status = ?, stop_requested_at = ?, updated_at = ?,
		     error_message = CASE WHEN error_message <> '' THEN error_message ELSE ? END
		 WHERE uuid = ? AND status IN ('created', 'running', 'waiting_input', 'stop_requested')`,
		SessionStatusStopRequested, now, now, i18n.WithLocale(i18n.LocaleFromContext(ctx), "cli_user_stopped"), sessionUUID,
	)
	if err != nil {
		applog.Warn("[Orchestrator] 记录停止请求失败", "session", sessionUUID, "ctx_err", ctx.Err(), "error", err)
		return fmt.Errorf("记录停止请求失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取停止请求结果失败: %w", err)
	}
	if affected == 0 {
		return nil
	}

	// Set stopping before any potentially blocking process operation, to immediately stop event persistence and broadcast.
	o.mu.Lock()
	active := o.activeSessions[sessionUUID]
	var cancel context.CancelFunc
	var adapter executor.Adapter
	hasRunner := active != nil
	if active != nil {
		active.stopping = true
		cancel = active.cancel
		adapter = active.adapter
	}
	o.mu.Unlock()

	if o.stateBroadcaster != nil {
		o.stateBroadcaster(taskUUID, stepKey, sessionUUID, SessionStatusStopRequested)
	}

	// adapter.Stop must first terminate the entire process tree by root PID; only after success is the outer context cancelled.
	// If the process tree termination fails, keep the root process and the stop_requested state to let the user retry,
	// and return an explicit error rather than falsely reporting "terminated".
	if adapter != nil {
		if err := adapter.Stop(); err != nil {
			return fmt.Errorf("终止 CLI 进程树失败: %w", err)
		}
	}
	if cancel != nil {
		cancel()
	}
	if hasRunner {
		// 事件循环还在收尾时会带上已采集的 Token。这里等它自己落库，避免用 0 覆盖。
		if err := o.waitSessionTerminal(ctx, sessionUUID, 8*time.Second); err != nil {
			applog.Warn("[Orchestrator] 停止请求已记录，进程仍在收尾", "session", sessionUUID, "error", err)
			return err
		}
		return nil
	}
	o.finishSession(sessionUUID, taskUUID, stepKey, SessionStatusStopped, "", 0, 0, "", nil)
	return nil
}

// persistSessionTokenEstimate writes the best token total seen so far. A later
// zero-token stop must not erase it; finishSession only replaces a positive total.
func (o *Orchestrator) persistSessionTokenEstimate(sessionUUID string, inputTokens, outputTokens int) {
	if inputTokens <= 0 && outputTokens <= 0 {
		return
	}
	total := inputTokens + outputTokens
	if _, err := o.db.Exec(`UPDATE gt_cli_sessions SET
		input_tokens = CASE WHEN ? > input_tokens THEN ? ELSE input_tokens END,
		output_tokens = CASE WHEN ? > output_tokens THEN ? ELSE output_tokens END,
		total_tokens = CASE WHEN ? > total_tokens THEN ? ELSE total_tokens END,
		updated_at = ?
		WHERE uuid = ? AND status IN ('created', 'running', 'waiting_input', 'stop_requested')`,
		inputTokens, inputTokens, outputTokens, outputTokens, total, total, nowMillis(), sessionUUID); err != nil {
		applog.Debug("[Orchestrator] 暂存会话 Token 失败", "session", sessionUUID, "error", err)
	}
}

// waitSessionTerminal polls until the session row leaves the active states or the timeout elapses.
func (o *Orchestrator) waitSessionTerminal(ctx context.Context, sessionUUID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var status string
		err := o.db.QueryRow(`SELECT status FROM gt_cli_sessions WHERE uuid=?`, sessionUUID).Scan(&status)
		if err == nil && IsTerminalStatus(status) {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("等待会话结束超时")
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// validateRun pre-flight validation
func (o *Orchestrator) validateRun(ctx context.Context, opts RunStepOptions) error {
	// Check task exists
	var stepStatus, executionMode, taskStatus, stepMode, snapshot, currentSnapshot string
	err := o.db.QueryRowContext(ctx, `SELECT s.status, t.execution_mode, t.status, s.execution_mode,
		s.task_pipeline_snapshot_uuid,t.pipeline_snapshot_uuid
		FROM gt_task_steps s JOIN gt_tasks t ON t.uuid=s.task_uuid
		WHERE s.task_uuid=? AND s.uuid=?`, opts.TaskUUID, opts.StepUUID).Scan(
		&stepStatus, &executionMode, &taskStatus, &stepMode, &snapshot, &currentSnapshot)
	if err != nil {
		return fmt.Errorf("查询任务步骤失败: %w", err)
	}
	if stepMode != "" && stepMode != executionMode || executionMode == "vibe_coding" ||
		(executionMode == "pipeline" && (snapshot == "" || snapshot != currentSnapshot)) {
		return taskruntime.ErrExecutionModeConflict
	}
	if executionMode != "pipeline" && executionMode != "expert_group" && stepStatus != "active" && stepStatus != "completed" {
		return fmt.Errorf("只能执行任务进行中或已完成的 Agent 编排")
	}
	if executionMode == "pipeline" && taskStatus == "done" {
		var rounds int
		if err := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND step_uuid=?`, opts.TaskUUID, opts.StepUUID).Scan(&rounds); err != nil {
			return err
		}
		if rounds == 0 {
			return ErrPipelineTaskDone
		}
	}
	var activeCount int
	if err := o.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND status IN ('created','running','waiting_input','stop_requested')`,
		opts.TaskUUID).Scan(&activeCount); err != nil {
		return err
	}
	if activeCount > 0 {
		return ErrTaskBusy
	}

	// Check working directory
	if opts.WorkDir == "" {
		return fmt.Errorf("任务未配置工作目录")
	}
	info, err := os.Stat(opts.WorkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("任务工作目录不存在: %s", opts.WorkDir)
		}
		return fmt.Errorf("无法访问任务工作目录 %s: %w", opts.WorkDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("任务工作目录不是文件夹: %s", opts.WorkDir)
	}

	return nil
}

// StopTaskSessions stops all active CLI sessions for a given task.
func (o *Orchestrator) StopTaskSessions(ctx context.Context, taskUUID string) error {
	release := o.executionGates.acquire(taskUUID, false)
	defer release()
	return o.stopTaskSessions(ctx, taskUUID)
}

func (o *Orchestrator) stopTaskSessions(ctx context.Context, taskUUID string) error {
	rows, err := o.db.QueryContext(ctx,
		`SELECT uuid FROM gt_cli_sessions WHERE task_uuid = ? AND status IN ('created', 'running', 'waiting_input', 'stop_requested')`,
		taskUUID)
	if err != nil {
		return fmt.Errorf("查询任务活动会话失败: %w", err)
	}
	// 先把 UUID 全部取出并关闭游标，再逐个停止。SQLite 是单连接池
	// （SetMaxOpenConns(1)），游标未关闭前持有唯一连接，StopSession 内部
	// 的查询/更新会排队等连接，形成死锁：接口挂起直到客户端断开才报
	// context canceled。
	var sessionUUIDs []string
	for rows.Next() {
		var sessionUUID string
		if err := rows.Scan(&sessionUUID); err != nil {
			rows.Close()
			return fmt.Errorf("读取任务活动会话失败: %w", err)
		}
		sessionUUIDs = append(sessionUUIDs, sessionUUID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历任务活动会话失败: %w", err)
	}
	applog.Info("[Orchestrator] 停止任务会话", "task_uuid", taskUUID, "active_sessions", len(sessionUUIDs))

	var firstErr error
	for _, sessionUUID := range sessionUUIDs {
		start := nowMillis()
		if err := o.StopSession(ctx, sessionUUID); err != nil {
			applog.Warn("[Orchestrator] 停止会话失败", "session", sessionUUID, "error", err, "cost_ms", nowMillis()-start)
			if firstErr == nil {
				firstErr = fmt.Errorf("终止会话 %s 失败: %w", sessionUUID, err)
			}
			continue
		}
		applog.Info("[Orchestrator] 停止会话成功", "session", sessionUUID, "cost_ms", nowMillis()-start)
	}
	return firstErr
}

// GlobalRunningSessionCount returns the global (cross-task) count of running CLI sessions,
// used to show the live count of in-progress CLI tasks in the bottom-left badge.
func (o *Orchestrator) GlobalRunningSessionCount() (int, error) {
	var count int
	err := o.db.QueryRow(
		`SELECT COUNT(*) FROM gt_cli_sessions WHERE status IN ('created', 'running', 'waiting_input', 'stop_requested')`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计运行中的 CLI 会话失败: %w", err)
	}
	return count, nil
}

// resolveCLIExecutable locates the executable for the CLI type.
// It delegates to executor.ResolveCLIExecutable, which probes PATH as well as
// well-known install directories (e.g. ~/.local/bin/agent for Cursor CLI),
// so CLIs installed without being added to PATH still work.
func resolveCLIExecutable(cliType string) (string, error) {
	return executor.ResolveCLIExecutable(cliType)
}
