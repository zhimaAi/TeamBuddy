package cloudsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

// 产出文档上报的独立通道参数。与任务投影完全解耦：同一任务内按 rel_path 串行，
// 不同任务并行；每轮每任务有上限，避免一个超大任务把队列占满。
const (
	documentUploadAttemptLimit   = 3
	documentUploadFileTimeout    = 60 * time.Second
	documentUploadRoundTaskFiles = 100
	// documentUploadRoundTasks 限制单轮处理的任务数：一个轮次串行上传可能持续数十秒，
	// 限额保证 Kick 触发能及时被消费，而不是排在一次超长轮次之后。
	documentUploadRoundTasks = 4
	// documentUploadConcurrentTasks 是同时处理的任务数上限（不同任务并行、同任务串行）。
	documentUploadConcurrentTasks   = 4
	documentZombieUploadingGrace    = 5 * time.Minute
	documentKickBuffer              = 64
	documentKickDebounce            = time.Second
	documentKickSeenLimit           = 512
	documentUploadMaxErrorRunes     = syncErrorMaxRunes
	documentUnsupportedServerErrMsg = "服务端不支持产出文档上报"
)

// documentUploadBackoff 是可重试失败后的退避序列（attempts 从 0 计）。
var documentUploadBackoff = []time.Duration{10 * time.Second, 60 * time.Second, 300 * time.Second}

// DocumentSyncService 是产出文档上报的后台 worker。
// pending/失败重试状态全部落在 SQLite，进程重启后由 ticker 继续续传。
type DocumentSyncService struct {
	db       *sql.DB
	client   func() *cloud.Client
	interval time.Duration
	kick     chan string

	// baseCtx 是服务生命周期上下文，供手动上报派发的协程复用，
	// 避免 HTTP 请求结束（request ctx 取消）时把后台上报一并中断。
	ctxMu   sync.RWMutex
	baseCtx context.Context

	kickMu   sync.Mutex
	kickSeen map[string]int64
	// dirty 记录因去抖被合并掉的触发：本轮不重复扫描，但必须在下一轮兜底扫描，
	// 否则「刚生成的新文件」不会出现在任何 pending 行里，ticker 永远发现不了。
	dirty map[string]struct{}

	// slots 是任务级并发信号量：不同任务并行上报，同一任务由哈希锁串行。
	slots chan struct{}

	// 与任务投影分开的哈希锁：文档上传可能持续数十秒，不能阻塞任务投影推送。
	locks [32]chan struct{}
}

// defaultDocumentSync 由 bootstrap 在启动时注入；未注入时所有方法都是安全的空操作。
var defaultDocumentSync atomic.Pointer[DocumentSyncService]

// SetDefaultDocumentSyncService 注册全局产出文档上报服务。
func SetDefaultDocumentSyncService(service *DocumentSyncService) {
	defaultDocumentSync.Store(service)
}

// DefaultDocumentSyncService 返回全局产出文档上报服务（可能为 nil）。
func DefaultDocumentSyncService() *DocumentSyncService {
	return defaultDocumentSync.Load()
}

// NewDocumentSyncService 创建产出文档上报服务。interval<=0 时使用配置默认值。
func NewDocumentSyncService(db *sql.DB, client func() *cloud.Client, interval time.Duration) *DocumentSyncService {
	if interval <= 0 {
		interval = DocumentUploadConfig().Interval()
	}
	service := &DocumentSyncService{
		db:       db,
		client:   client,
		interval: interval,
		kick:     make(chan string, documentKickBuffer),
		kickSeen: make(map[string]int64, 16),
		dirty:    make(map[string]struct{}, 8),
		slots:    make(chan struct{}, documentUploadConcurrentTasks),
	}
	for index := range service.locks {
		service.locks[index] = make(chan struct{}, 1)
	}
	return service
}

// Start 启动后台循环；ctx 取消后退出。
func (s *DocumentSyncService) Start(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	s.ctxMu.Lock()
	s.baseCtx = ctx
	s.ctxMu.Unlock()
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		s.runRound(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runRound(ctx)
			case taskUUID := <-s.kick:
				s.dispatchSync(ctx, taskUUID)
			}
		}
	}()
}

// acquire 取一个任务并发槽位；ctx 取消时返回 false。
func (s *DocumentSyncService) acquire(ctx context.Context) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// dispatchSync 在独立协程里处理 Kick 触发的单任务（先扫描再上报）。
// 不同任务并行、同一任务由 lockTask 串行；并发由 slots 限流，取不到槽位时阻塞，
// 保证触发不会因为一次超长轮次而丢失。
func (s *DocumentSyncService) dispatchSync(ctx context.Context, taskUUID string) {
	if !s.acquire(ctx) {
		return
	}
	go func() {
		defer func() { <-s.slots }()
		s.syncTask(ctx, taskUUID)
	}()
}

// dispatchUpload 在独立协程里上报单个任务已有的 pending 行（ticker 兜底路径）。
func (s *DocumentSyncService) dispatchUpload(ctx context.Context, client *cloud.Client, taskUUID string) {
	if !s.acquire(ctx) {
		return
	}
	go func() {
		defer func() { <-s.slots }()
		s.uploadTask(ctx, client, taskUUID)
	}()
}

// Kick 触发一次立即扫描 + 上报。同一任务 1s 内重复触发会被去抖，
// 队列满时直接丢弃（pending 行仍在库里，下一轮 ticker 兜底）。
func (s *DocumentSyncService) Kick(taskUUID string) {
	if s == nil || s.db == nil || taskUUID == "" {
		return
	}
	if !DocumentUploadConfig().Enabled {
		return
	}
	now := time.Now().UnixMilli()
	s.kickMu.Lock()
	if last, ok := s.kickSeen[taskUUID]; ok && now-last < documentKickDebounce.Milliseconds() {
		// 去抖：本轮不重复扫描，但登记为 dirty，下一轮 ticker 必须补一次扫描
		s.dirty[taskUUID] = struct{}{}
		s.kickMu.Unlock()
		return
	}
	if len(s.kickSeen) >= documentKickSeenLimit {
		for key, at := range s.kickSeen {
			if now-at >= documentKickDebounce.Milliseconds() {
				delete(s.kickSeen, key)
			}
		}
	}
	s.kickSeen[taskUUID] = now
	s.kickMu.Unlock()

	select {
	case s.kick <- taskUUID:
	default:
		s.kickMu.Lock()
		s.dirty[taskUUID] = struct{}{}
		s.kickMu.Unlock()
		applog.Warn("[DocSync] 上报队列已满，等待下一轮兜底", "task_uuid", taskUUID)
	}
}

// serviceContext 返回服务生命周期上下文；Start 之前调用时退化为 Background。
func (s *DocumentSyncService) serviceContext() context.Context {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	if s.baseCtx == nil {
		return context.Background()
	}
	return s.baseCtx
}

// ReportNow 是任务级手动上报：忽略任务级「同步 Agent 产出」开关，
// 立刻扫描任务产出目录并把命中规则的文件排入上报队列。
// 返回值是本轮新入队的文件数，实际上传由后台协程继续，不阻塞调用方。
func (s *DocumentSyncService) ReportNow(ctx context.Context, taskUUID string) (int, error) {
	if s == nil || s.db == nil || taskUUID == "" || !DocumentUploadConfig().Enabled {
		return 0, fmt.Errorf("产出文档上报未启用")
	}
	// 顺带重试此前失败的文件：一次「上报文件」应覆盖所有未成功的产出，
	// 而不是让用户再逐个点「重新上报」。
	retried := 0
	result, requeueErr := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, attempts=0, next_attempt_at=0, last_error='', modified_at=0, updated_at=?
		WHERE task_uuid=? AND status=?`,
		documentStatusPending, time.Now().UnixMilli(), taskUUID, documentStatusFailed)
	if requeueErr != nil {
		applog.Warn("[DocSync] 手动上报重试失败文件出错", "task_uuid", taskUUID, "error", requeueErr.Error())
	} else if affected, rowsErr := result.RowsAffected(); rowsErr == nil {
		retried = int(affected)
	}
	queued, err := scanTaskDocuments(ctx, s.db, taskUUID, true)
	if err != nil {
		return retried, err
	}
	queued += retried
	client := s.cloudClient()
	if client == nil {
		// 未登录时保留 pending 行，登录后由轮询继续上报
		return queued, nil
	}
	s.dispatchUpload(s.serviceContext(), client, taskUUID)
	return queued, nil
}

// KickUploadOnly 只派发已有 pending 行的上报，不重新扫描。
// 任务关闭自动同步时不能走 Kick：扫描会把全部产出重新入队，超出用户手动选择的范围。
func (s *DocumentSyncService) KickUploadOnly(taskUUID string) {
	if s == nil || s.db == nil || taskUUID == "" || !DocumentUploadConfig().Enabled {
		return
	}
	client := s.cloudClient()
	if client == nil {
		return
	}
	s.dispatchUpload(s.serviceContext(), client, taskUUID)
}

// takeDirtyTasks 取出并清空待补扫任务，供 ticker 轮兜底。
func (s *DocumentSyncService) takeDirtyTasks() []string {
	s.kickMu.Lock()
	defer s.kickMu.Unlock()
	if len(s.dirty) == 0 {
		return nil
	}
	tasks := make([]string, 0, len(s.dirty))
	for taskUUID := range s.dirty {
		tasks = append(tasks, taskUUID)
	}
	s.dirty = make(map[string]struct{}, 8)
	return tasks
}

// Requeue 把单个文件重新排入上报队列（手动重报）：清零重试计数并要求重新计算变更。
func (s *DocumentSyncService) Requeue(ctx context.Context, taskUUID, relPath string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("产出文档上报未启用")
	}
	relPath = DocumentRelKey(relPath)
	if relPath == "" {
		return fmt.Errorf("文件路径不能为空")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, attempts=0, next_attempt_at=0, last_error='', modified_at=0, updated_at=?
		WHERE task_uuid=? AND rel_path=?`,
		documentStatusPending, time.Now().UnixMilli(), taskUUID, relPath)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected == 0 {
		return fmt.Errorf("文件尚未纳入上报范围: %s", relPath)
	}
	return nil
}

// runRound 是 ticker 驱动的兜底轮：处理所有到期的 pending 行。
func (s *DocumentSyncService) runRound(ctx context.Context) {
	if s == nil || s.db == nil || !DocumentUploadConfig().Enabled {
		return
	}
	client := s.cloudClient()
	if client == nil {
		return
	}
	s.resetZombieUploads(ctx)
	// 因去抖/队列满被合并掉的触发，在这里补一次扫描，保证新文件不会被漏掉
	for _, taskUUID := range s.takeDirtyTasks() {
		if ctx.Err() != nil {
			return
		}
		s.dispatchSync(ctx, taskUUID)
	}
	tasks, err := pendingDocumentTasks(ctx, s.db, client)
	if err != nil {
		applog.Warn("[DocSync] 读取待上报任务失败", "error", err.Error())
		return
	}
	for _, taskUUID := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.dispatchUpload(ctx, client, taskUUID)
	}
}

// syncTask 处理 Kick 触发的单任务：先扫描再上报。
func (s *DocumentSyncService) syncTask(ctx context.Context, taskUUID string) {
	if s == nil || s.db == nil || taskUUID == "" || !DocumentUploadConfig().Enabled {
		return
	}
	// 任务级「同步 Agent 产出」关闭时，自动链路（任务推送、编辑保存、任务收尾）
	// 一律不扫描、不上报，产出文件只能由用户在文件抽屉里手动上报。
	if !TaskAutoUploadEnabled(ctx, s.db, taskUUID) {
		return
	}
	client := s.cloudClient()
	if client == nil {
		return
	}
	if _, err := ScanTaskDocuments(ctx, s.db, taskUUID); err != nil {
		applog.Warn("[DocSync] 扫描产出文档失败", "task_uuid", taskUUID, "error", err.Error())
		return
	}
	s.uploadTask(ctx, client, taskUUID)
}

// cloudClient 返回当前登录账号的云端客户端，未登录时返回 nil（本轮跳过，不消耗重试）。
func (s *DocumentSyncService) cloudClient() *cloud.Client {
	if s.client == nil {
		return nil
	}
	client := s.client()
	if client == nil || client.UserID() == "" {
		return nil
	}
	return client
}

// resetZombieUploads 把长期停留在 uploading 的行重置为 pending（进程崩溃/中断遗留）。
func (s *DocumentSyncService) resetZombieUploads(ctx context.Context) {
	cutoff := time.Now().Add(-documentZombieUploadingGrace).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, updated_at=? WHERE status=? AND updated_at<?`,
		documentStatusPending, time.Now().UnixMilli(), documentStatusUploading, cutoff); err != nil {
		applog.Warn("[DocSync] 重置僵尸上传状态失败", "error", err.Error())
	}
}

// pendingDocumentTasks 先过滤账号再限额，避免旧账号队列持续占满当前账号的处理名额。
func pendingDocumentTasks(ctx context.Context, db *sql.DB, client *cloud.Client) ([]string, error) {
	if client == nil || client.UserID() == "" {
		return nil, nil
	}
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	rows, err := db.QueryContext(ctx, `SELECT d.task_uuid FROM gt_task_document_uploads d
		JOIN gt_tasks t ON t.uuid=d.task_uuid
		WHERE d.status=? AND d.next_attempt_at<=? AND t.source_type='cloud'
		AND t.cloud_source_key IN (?, ?) AND (t.cloud_user_id=? OR COALESCE(t.cloud_user_id,'')='')
		GROUP BY d.task_uuid ORDER BY MIN(d.updated_at), d.task_uuid LIMIT ?`,
		documentStatusPending, time.Now().UnixMilli(), primary, alternate, client.UserID(), documentUploadRoundTasks)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]string, 0, 8)
	for rows.Next() {
		var taskUUID string
		if err := rows.Scan(&taskUUID); err != nil {
			return nil, err
		}
		tasks = append(tasks, taskUUID)
	}
	return tasks, rows.Err()
}

// lockTask 取任务级哈希锁，保证同一任务串行、不同任务并行。
func (s *DocumentSyncService) lockTask(ctx context.Context, taskUUID string) (func(), error) {
	sum := sha256.Sum256([]byte(taskUUID))
	lock := s.locks[int(sum[0])%len(s.locks)]
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return func() { <-lock }, nil
}

// uploadTask 串行上报一个任务下所有到期的 pending 文件。
func (s *DocumentSyncService) uploadTask(ctx context.Context, client *cloud.Client, taskUUID string) {
	release, err := s.lockTask(ctx, taskUUID)
	if err != nil {
		return
	}
	defer release()

	// 旁路上传与任务投影使用相同归属边界，不能把旧账号的产出发给新账号。
	owned, err := ownsTask(ctx, s.db, client, taskUUID)
	if err != nil || !owned {
		return
	}

	var taskDir string
	var executionIndex int
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(task_dir,''), COALESCE(execution_index,0)
		FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskDir, &executionIndex)
	if err != nil {
		// 任务已被删除：行随任务级联清理，这里不再上报
		return
	}
	root, err := filepath.Abs(strings.TrimSpace(taskDir))
	if err != nil || strings.TrimSpace(taskDir) == "" {
		return
	}

	rows, err := s.db.QueryContext(ctx, `SELECT rel_path, doc_key, file_ext, attempts
		FROM gt_task_document_uploads
		WHERE task_uuid=? AND status=? AND next_attempt_at<=?
		ORDER BY rel_path LIMIT ?`,
		taskUUID, documentStatusPending, time.Now().UnixMilli(), documentUploadRoundTaskFiles)
	if err != nil {
		applog.Warn("[DocSync] 读取待上报文件失败", "task_uuid", taskUUID, "error", err.Error())
		return
	}
	type pendingFile struct {
		relPath  string
		docKey   string
		ext      string
		attempts int
	}
	files := make([]pendingFile, 0, 16)
	for rows.Next() {
		var file pendingFile
		if scanErr := rows.Scan(&file.relPath, &file.docKey, &file.ext, &file.attempts); scanErr != nil {
			rows.Close()
			applog.Warn("[DocSync] 读取待上报文件失败", "task_uuid", taskUUID, "error", scanErr.Error())
			return
		}
		files = append(files, file)
	}
	rows.Close()

	for _, file := range files {
		if ctx.Err() != nil {
			return
		}
		// 允许中途关闭开关：关闭后立即停止上报，剩余行保持 pending
		if !DocumentUploadConfig().Enabled {
			return
		}
		s.uploadFile(ctx, client, taskUUID, root, executionIndex, file.relPath, file.docKey, file.ext, file.attempts)
	}
}

// uploadFile 上报单个文件，并按结果更新本地状态。
func (s *DocumentSyncService) uploadFile(ctx context.Context, client *cloud.Client, taskUUID, root string,
	executionIndex int, relPath, docKey, ext string, attempts int) {
	// 手动重报会直接置 pending 跳过扫描，这里再校验一次规则，避免把黑名单内的文件传上去
	settings := DocumentUploadConfig()
	if !settings.ExtensionAllowed(strings.ToLower(strings.TrimPrefix(ext, "."))) {
		s.markDocumentSkipped(ctx, taskUUID, relPath, "超出上报规则（文件类型）")
		return
	}
	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	if !pathWithinDocumentRoot(root, absPath) {
		s.markDocumentFailed(ctx, taskUUID, relPath, "文件路径不在任务目录范围内")
		return
	}
	before, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 文件已消失：不消耗重试次数，等下一轮扫描把它置 deleted
			applog.Info("[DocSync] 产出文件已不存在，等待扫描标记删除", "task_uuid", taskUUID, "rel_path", relPath)
			return
		}
		s.handleUploadFailure(ctx, taskUUID, relPath, documentReadErrorMessage(err), attempts, true)
		return
	}
	if before.Size() > settings.MaxFileBytes() {
		s.markDocumentSkipped(ctx, taskUUID, relPath, "超出上报规则（文件大小）")
		return
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		s.handleUploadFailure(ctx, taskUUID, relPath, documentReadErrorMessage(err), attempts, true)
		return
	}
	after, err := os.Stat(absPath)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		// 读取过程中文件被改写：本轮放弃，下次扫描按新内容重来，避免上传半新半旧内容
		applog.Info("[DocSync] 文件在上报过程中发生变化，推迟到下一轮", "task_uuid", taskUUID, "rel_path", relPath)
		return
	}

	sum := sha256.Sum256(content)
	contentHash := hex.EncodeToString(sum[:])
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, updated_at=? WHERE task_uuid=? AND rel_path=? AND status=?`,
		documentStatusUploading, time.Now().UnixMilli(), taskUUID, relPath, documentStatusPending); err != nil {
		applog.Warn("[DocSync] 标记上传中失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
		return
	}

	uploadCtx, cancel := context.WithTimeout(ctx, documentUploadFileTimeout)
	defer cancel()
	// title 对齐线上契约的展示语义（不带扩展名，见线上方案 §4.2 示例）；
	// multipart 里的文件名仍保留完整名字，便于线上排查。
	fileName := filepath.Base(filepath.FromSlash(relPath))
	title := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if strings.TrimSpace(title) == "" {
		title = fileName
	}
	result, uploadErr := client.PushPipelineTaskDocument(uploadCtx, cloud.PipelineTaskDocumentUpload{
		LocalTaskUUID:   taskUUID,
		ExecutionIndex:  executionIndex,
		DocKey:          docKey,
		RelPath:         relPath,
		Title:           title,
		FileExt:         ext,
		FileSize:        int64(len(content)),
		ContentSHA256:   contentHash,
		ClientUpdatedAt: after.ModTime().UnixMilli(),
		SourceAbsPath:   absPath,
		FileName:        fileName,
	}, content)
	if uploadErr != nil {
		retryable := documentUploadRetryable(uploadErr)
		message := uploadErr.Error()
		var apiErr *cloud.APIError
		if errors.As(uploadErr, &apiErr) && apiErr.StatusCode == 404 {
			retryable = false
			message = documentUnsupportedServerErrMsg
		}
		s.handleUploadFailure(ctx, taskUUID, relPath, message, attempts, retryable)
		return
	}

	switch result.Result {
	case "accepted", "duplicate", "replaced", "deferred", "":
	default:
		s.markDocumentFailed(ctx, taskUUID, relPath,
			fmt.Sprintf("服务端返回未知结果：%s", result.Result))
		return
	}

	now := time.Now().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, attempts=0, next_attempt_at=0, last_error='', content_hash=?, file_size=?,
		    modified_at=?, remote_doc_id=?, remote_revision=?, uploaded_at=?, updated_at=?
		WHERE task_uuid=? AND rel_path=?`,
		documentStatusSynced, contentHash, int64(len(content)), after.ModTime().UnixMilli(),
		result.DocumentID, result.Revision, now, now, taskUUID, relPath); err != nil {
		applog.Warn("[DocSync] 写入上报成功状态失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
	}
}

// handleUploadFailure 按可重试性更新重试计数与退避时间。
func (s *DocumentSyncService) handleUploadFailure(ctx context.Context, taskUUID, relPath, message string, attempts int, retryable bool) {
	if !retryable {
		s.markDocumentFailed(ctx, taskUUID, relPath, message)
		return
	}
	nextAttempts := attempts + 1
	now := time.Now()
	status := documentStatusPending
	nextAttemptAt := int64(0)
	if nextAttempts >= documentUploadAttemptLimit {
		status = documentStatusFailed
	} else {
		backoff := documentUploadBackoff[min(nextAttempts-1, len(documentUploadBackoff)-1)]
		nextAttemptAt = now.Add(backoff).UnixMilli()
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, attempts=?, next_attempt_at=?, last_error=?, updated_at=?
		WHERE task_uuid=? AND rel_path=?`,
		status, nextAttempts, nextAttemptAt, truncateRunes(message, documentUploadMaxErrorRunes),
		now.UnixMilli(), taskUUID, relPath); err != nil {
		applog.Warn("[DocSync] 写入上报失败状态失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
	}
}

// markDocumentFailed 直接置为失败（不可重试），不消耗重试次数。
func (s *DocumentSyncService) markDocumentFailed(ctx context.Context, taskUUID, relPath, message string) {
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, last_error=?, updated_at=? WHERE task_uuid=? AND rel_path=?`,
		documentStatusFailed, truncateRunes(message, documentUploadMaxErrorRunes),
		time.Now().UnixMilli(), taskUUID, relPath); err != nil {
		applog.Warn("[DocSync] 写入上报失败状态失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
	}
}

// markDocumentSkipped 置为不参与上报（超出规则）。
func (s *DocumentSyncService) markDocumentSkipped(ctx context.Context, taskUUID, relPath, reason string) {
	if _, err := s.db.ExecContext(ctx, `UPDATE gt_task_document_uploads
		SET status=?, last_error=?, updated_at=? WHERE task_uuid=? AND rel_path=?`,
		documentStatusSkipped, reason, time.Now().UnixMilli(), taskUUID, relPath); err != nil {
		applog.Warn("[DocSync] 写入上报跳过状态失败", "task_uuid", taskUUID, "rel_path", relPath, "error", err.Error())
	}
}

// documentUploadRetryable 判断上传错误是否值得重试。
// 网络错误与 408/429/5xx 重试；其余 4xx（参数、鉴权、权限、超限、任务不存在）直接失败。
func documentUploadRetryable(err error) bool {
	var apiErr *cloud.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	return true
}

// pathWithinDocumentRoot 校验目标路径在任务目录内（防止 rel_path 被构造后越界）。
func pathWithinDocumentRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// documentReadErrorMessage 把读文件失败翻译成可读原因。
// Windows 上文件被其他进程（Word/Excel/编辑器）占用时返回的是共享冲突错误，
// Go 没有平台无关的判定方式，这里按权限错误与错误文本兜底。
func documentReadErrorMessage(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "文件被占用或无读取权限"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "being used by another process") || strings.Contains(message, "sharing violation") {
		return "文件被占用，请关闭占用该文件的程序后重试"
	}
	return "读取文件失败：" + err.Error()
}
