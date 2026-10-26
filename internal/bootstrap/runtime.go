package bootstrap

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/executor"
	"goteams-client/internal/executor/antigravity"
	"goteams-client/internal/executor/claude"
	"goteams-client/internal/executor/codebuddy"
	"goteams-client/internal/executor/codex"
	"goteams-client/internal/executor/copilot"
	"goteams-client/internal/executor/cursor"
	"goteams-client/internal/executor/grok"
	"goteams-client/internal/executor/hermes"
	"goteams-client/internal/executor/kimi"
	"goteams-client/internal/executor/openclaw"
	"goteams-client/internal/executor/opencode"
	"goteams-client/internal/executor/pi"
	"goteams-client/internal/executor/qoder"
	"goteams-client/internal/executor/qwen"
	"goteams-client/internal/localserver"
	"goteams-client/internal/pipeline"
	"goteams-client/internal/storage"
	storagelog "goteams-client/internal/storage/log"
	"goteams-client/internal/workflow"
)

// LocalRuntime owns all local task components for the lifetime of the process.
// It is intentionally independent from the optional cloud account session.
type LocalRuntime struct {
	manager      *storage.Manager
	DB           *sql.DB
	EventStore   *storagelog.EventStore
	Orchestrator *workflow.Orchestrator
	WSHub        *localserver.WSHub
}

func NewLocalRuntime(ctx context.Context, dataDir string, cloudClient *cloud.Client) (*LocalRuntime, error) {
	manager, err := storage.NewManagerForRole(dataDir, storage.DatabaseGoTeams)
	if err != nil {
		return nil, err
	}
	if err := manager.Migrate(ctx); err != nil {
		manager.Close()
		return nil, err
	}

	db := manager.DB()
	// 旧版本曾把云端流水线缓存进 gt_pipelines；现在云端流水线仅存内存，启动时清掉历史残留。
	// 清理失败不阻塞启动：List/Get 只读本地来源，残留行不会对外暴露。
	if err := pipeline.CleanupLegacyCloudPipelines(ctx, db); err != nil {
		applog.Warn("清理历史云端流水线本地缓存失败", "error", err)
	}
	eventStore := storagelog.NewEventStore(db)
	executor.ReapStaleGroups(filepath.Join(filepath.Dir(dataDir), "runtime"))
	registerExecutorFactories()
	orchestrator := workflow.NewOrchestrator(db, db, eventStore)
	wsHub := localserver.NewWSHub(db)
	orchestrator.SetEventBroadcaster(func(sessionUUID string, sequence int, event executor.ExecutorEvent) {
		wsHub.BroadcastEvent(sessionUUID, sequence, event)
	})
	orchestrator.SetStateBroadcaster(func(taskUUID, stepKey, sessionUUID, status string) {
		wsHub.BroadcastStateChanged(taskUUID, stepKey, sessionUUID, status)
	})
	orchestrator.SetActivityBroadcaster(func(taskUUID, sessionUUID string, sequence int, eventType, content string, at int64) {
		wsHub.BroadcastActivity(taskUUID, sessionUUID, sequence, eventType, content, at)
	})
	wsHub.RegisterPushFunc(func() (string, interface{}) {
		count, err := orchestrator.GlobalRunningSessionCount()
		if err != nil {
			return "", nil
		}
		return "app.cli_count", map[string]interface{}{"count": count}
	})
	wsHub.StartPushLoop(5 * time.Second)

	runtime := &LocalRuntime{
		manager: manager, DB: db, EventStore: eventStore,
		Orchestrator: orchestrator, WSHub: wsHub,
	}
	workflow.StartSessionCleanup(ctx, db, nil)
	go func() {
		if err := orchestrator.HandleCrashRecovery(); err != nil {
			applog.Error("崩溃恢复失败", "error", err)
		}
	}()
	return runtime, nil
}

func (r *LocalRuntime) Close() {
	if r == nil {
		return
	}
	if r.Orchestrator != nil {
		r.Orchestrator.StopActiveProcesses()
	}
	if r.WSHub != nil {
		r.WSHub.Close()
	}
	if r.manager != nil {
		r.manager.Close()
	}
}

// registerExecutorFactories 把各 CLI 适配器注册到 workflow 的适配器注册表。
// 新增 CLI 只需要在 internal/executor/<cli> 实现适配器，并在此处补一行注册。
func registerExecutorFactories() {
	workflow.RegisterAdapterFactory(executor.CLITypeClaude, claude.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeCodeBuddy, codebuddy.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeCodex, codex.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeOpenCode, opencode.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeCursor, cursor.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeCopilot, copilot.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeGrok, grok.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeHermes, hermes.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeKimi, kimi.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeQoder, qoder.NewAdapter)
	// Qoder 国内版与海外版协议一致，复用同一适配器
	workflow.RegisterAdapterFactory(executor.CLITypeQoderCN, qoder.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeQwen, qwen.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeOpenClaw, openclaw.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypePi, pi.NewAdapter)
	workflow.RegisterAdapterFactory(executor.CLITypeAntigravity, antigravity.NewAdapter)
}
