package cloudsync

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/config"
)

// gt_task_document_uploads.status 取值域。与前端 TaskFileUploadStatus 一一对应，
// 额外的 deleted 仅存在于本地表（文件已被删除，等线上任务级清理）。
const (
	documentStatusPending   = "pending"
	documentStatusUploading = "uploading"
	documentStatusSynced    = "synced"
	documentStatusFailed    = "failed"
	documentStatusSkipped   = "skipped"
	documentStatusDeleted   = "deleted"
)

// 产出文档扫描需要跳过的目录：版本库/依赖目录体积大且不是任务产出，
// 附件目录是历史链路落盘位置，点号目录统一跳过（.git/.DS_Store 等）。
const (
	documentSkippedDirGit        = ".git"
	documentSkippedDirModules    = "node_modules"
	documentSkippedDirAttachment = ".goteams-attachments"
	documentSkippedDirAppSuffix  = ".app"
)

// documentScanMaxFiles 是单任务扫描时最多读取的行数保护上限，避免异常目录把内存吃满。
const documentScanMaxFiles = 5000

// DocumentUploadState 是单个文件的产出文档上报状态（文件树 upload 字段）。
type DocumentUploadState struct {
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	LastError      string `json:"last_error"`
	UploadedAt     int64  `json:"uploaded_at"`
	DocKey         string `json:"doc_key"`
	RemoteRevision int64  `json:"remote_revision"`
	// Reuploadable 表示该文件可以手动重报（命中白名单且未被规则跳过）
	Reuploadable bool `json:"reuploadable"`
}

// DocumentUploadSummary 是目录节点的上报状态聚合，供折叠状态下展示。
type DocumentUploadSummary struct {
	Total   int `json:"total"`
	Synced  int `json:"synced"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	Skipped int `json:"skipped"`
}

// documentSettingsStore 保存当前生效的产出文档上报配置。配置在启动时写入一次，
// 之后由扫描/上报/本地 API 以只读方式获取。
var documentSettingsStore struct {
	mu  sync.RWMutex
	cfg config.DocumentUploadConfig
}

func init() {
	documentSettingsStore.cfg = config.DefaultDocumentUploadConfig()
}

// SetDocumentUploadConfig 设置当前生效的产出文档上报配置（启动时调用一次）。
func SetDocumentUploadConfig(cfg config.DocumentUploadConfig) {
	documentSettingsStore.mu.Lock()
	documentSettingsStore.cfg = cfg
	documentSettingsStore.mu.Unlock()
}

// DocumentUploadConfig 返回当前生效的产出文档上报配置。
func DocumentUploadConfig() config.DocumentUploadConfig {
	documentSettingsStore.mu.RLock()
	defer documentSettingsStore.mu.RUnlock()
	return documentSettingsStore.cfg
}

// DocumentRelKey 把本地相对路径归一为上报身份：正斜杠、去掉前导 "./"。
func DocumentRelKey(relPath string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(relPath), `\`, "/")
	normalized = strings.TrimPrefix(normalized, "./")
	return strings.TrimPrefix(normalized, "/")
}

// DocumentDocKey 由相对路径生成稳定文档身份：sha1(rel_path)[:32]。
// 内容变化不改 doc_key，线上据此走「同文件更新」。
func DocumentDocKey(relPath string) string {
	sum := sha1.Sum([]byte(DocumentRelKey(relPath)))
	return hex.EncodeToString(sum[:])[:32]
}

// documentCandidate 是扫描阶段命中的候选文件（尚未判定上报与否）。
type documentCandidate struct {
	relPath    string
	absPath    string
	ext        string
	size       int64
	modifiedAt int64
}

// documentRow 是已存在的上报行（只取变更判定需要的列）。
type documentRow struct {
	status      string
	fileSize    int64
	modifiedAt  int64
	contentHash string
}

// ScanTaskDocuments 扫描 task_dir，把新增/变更文件写入 gt_task_document_uploads(pending)，
// 返回本轮需要上报的文件数。只处理 cloud 来源任务：本地任务在云端没有 pipeline_tasks 行，
// 上报必然 404，不进入扫描队列。
// 关闭「同步 Agent 产出」的任务不参与自动扫描，需要用户手动上报（见 scanTaskDocuments 的 force）。
func ScanTaskDocuments(ctx context.Context, db *sql.DB, taskUUID string) (int, error) {
	return scanTaskDocuments(ctx, db, taskUUID, false)
}

// scanTaskDocuments 是扫描的实现；force=true 表示手动上报，忽略任务级开关。
func scanTaskDocuments(ctx context.Context, db *sql.DB, taskUUID string, force bool) (int, error) {
	settings := DocumentUploadConfig()
	if db == nil || taskUUID == "" || !settings.Enabled {
		return 0, nil
	}
	var taskDir, sourceType string
	var syncAgentOutput bool
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(task_dir,''), COALESCE(source_type,''), COALESCE(sync_agent_output,1)
		 FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&taskDir, &sourceType, &syncAgentOutput)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if sourceType != "cloud" {
		return 0, nil
	}
	// 任务级开关关闭时不自动扫描，避免把产出文件排进上报队列；
	// 手动上报走 force 分支，仍然按同一套规则入队。
	if !syncAgentOutput && !force {
		return 0, nil
	}
	taskDir = strings.TrimSpace(taskDir)
	if taskDir == "" {
		return 0, nil
	}
	root, err := filepath.Abs(taskDir)
	if err != nil {
		return 0, nil
	}
	// 目录不存在或不可读时不做任何判定：既扫不到新文件，也不能把已有行误标为 deleted
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return 0, nil
	}
	candidates, err := collectDocumentCandidates(root)
	if err != nil {
		return 0, err
	}
	rows, err := loadDocumentRows(ctx, db, taskUUID)
	if err != nil {
		return 0, err
	}

	maxFiles := settings.MaxFilesPerTask
	if maxFiles <= 0 {
		maxFiles = len(candidates)
	}
	budget := settings.MaxBatchBytes()
	if budget <= 0 {
		budget = int64(^uint64(0) >> 1)
	}
	now := time.Now().UnixMilli()

	uploads := make([]documentUploadWrite, 0, len(candidates))
	changed := 0
	acceptedCount := 0
	seen := make(map[string]struct{}, len(candidates))

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		seen[candidate.relPath] = struct{}{}
		existing, hasExisting := rows[candidate.relPath]
		if hasExisting && existing.status == documentStatusUploading {
			// 正在上报的文件不参与本轮判定，避免与上传协程互相踩状态
			continue
		}

		skipReason := ""
		switch {
		case !settings.ExtensionAllowed(candidate.ext):
			skipReason = "超出上报规则（文件类型）"
		case candidate.size > settings.MaxFileBytes():
			skipReason = "超出上报规则（文件大小）"
		case acceptedCount >= maxFiles:
			// 只有命中规则的文件才计入数量上限，避免源码文件把名额挤掉
			skipReason = "超出单任务上报文件数上限"
		}
		if skipReason == "" {
			acceptedCount++
		}
		if skipReason != "" {
			if hasExisting {
				// 已有记录的文件（尤其是已上报成功的）不因为本轮规则被降级为 skipped
				continue
			}
			uploads = append(uploads, documentUploadWrite{
				relPath:    candidate.relPath,
				ext:        candidate.ext,
				size:       candidate.size,
				modifiedAt: candidate.modifiedAt,
				status:     documentStatusSkipped,
				lastError:  skipReason,
				updatedAt:  now,
			})
			continue
		}

		if hasExisting && existing.status != documentStatusSkipped && existing.status != documentStatusDeleted &&
			existing.fileSize == candidate.size && existing.modifiedAt == candidate.modifiedAt {
			// 快路径：mtime+size 未变，视为无变更（内容在 mtime 不变时被改写属异常场景，靠手动重报兜底）
			continue
		}
		hash, hashErr := hashDocumentFile(candidate.absPath)
		if hashErr != nil {
			applog.Warn("[DocSync] 读取产出文件失败，跳过本轮", "task_uuid", taskUUID, "rel_path", candidate.relPath, "error", hashErr.Error())
			continue
		}
		if hasExisting && existing.contentHash != "" && existing.contentHash == hash &&
			existing.status != documentStatusDeleted {
			// 内容未变（仅 mtime 变化，例如编辑器原子写或批量 touch）：只刷新水位。
			// deleted 行不能据此恢复：删除标记必须由重新上报确认，否则「删掉又原样放回」
			// 会永久停留在 deleted 状态（线上内容或许已被任务级清理）。
			uploads = append(uploads, documentUploadWrite{
				relPath:    candidate.relPath,
				ext:        candidate.ext,
				size:       candidate.size,
				modifiedAt: candidate.modifiedAt,
				status:     existing.status,
				touchOnly:  true,
				updatedAt:  now,
			})
			continue
		}
		if candidate.size > budget {
			// 单轮总量超限：本轮先不上报，保留原状态等下一轮（不落 skipped，避免永久放弃变更）
			applog.Warn("[DocSync] 超出单任务单轮上报总量，推迟到下一轮",
				"task_uuid", taskUUID, "rel_path", candidate.relPath, "size", candidate.size, "budget", budget)
			continue
		}
		budget -= candidate.size
		uploads = append(uploads, documentUploadWrite{
			relPath:    candidate.relPath,
			ext:        candidate.ext,
			size:       candidate.size,
			modifiedAt: candidate.modifiedAt,
			status:     documentStatusPending,
			updatedAt:  now,
		})
		changed++
	}

	// 磁盘上已消失的文件：保留行并置 deleted，等线上任务级清理（本期不做删除同步）
	deleted := make([]string, 0)
	for relPath, existing := range rows {
		if _, ok := seen[relPath]; ok {
			continue
		}
		if existing.status == documentStatusDeleted {
			continue
		}
		deleted = append(deleted, relPath)
	}

	if len(uploads) == 0 && len(deleted) == 0 {
		return 0, nil
	}
	if err := applyDocumentWrites(ctx, db, taskUUID, uploads, deleted); err != nil {
		return changed, err
	}
	return changed, nil
}

// documentUploadWrite 是扫描阶段对单行的一次写入意图。
type documentUploadWrite struct {
	relPath    string
	ext        string
	size       int64
	modifiedAt int64
	status     string
	lastError  string
	touchOnly  bool // 仅刷新 size/mtime 水位，不改状态与重试计数
	updatedAt  int64
}

// collectDocumentCandidates 遍历任务产出目录，返回按相对路径升序的候选文件。
func collectDocumentCandidates(root string) ([]documentCandidate, error) {
	candidates := make([]documentCandidate, 0, 64)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// 单个条目不可读（权限/占用）不影响整体扫描
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			// .app 是 macOS 的应用包（本质是目录），与单文件扩展名一样不参与上报
			if name == documentSkippedDirGit || name == documentSkippedDirModules ||
				name == documentSkippedDirAttachment || strings.HasPrefix(name, ".") ||
				strings.HasSuffix(strings.ToLower(name), documentSkippedDirAppSuffix) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(name, ".") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relPath := DocumentRelKey(relative)
		if relPath == "" {
			return nil
		}
		candidates = append(candidates, documentCandidate{
			relPath:    relPath,
			absPath:    path,
			ext:        strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")),
			size:       info.Size(),
			modifiedAt: info.ModTime().UnixMilli(),
		})
		if len(candidates) >= documentScanMaxFiles {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return nil, walkErr
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].relPath < candidates[j].relPath })
	return candidates, nil
}

// loadDocumentRows 读取任务下已有的上报行。
func loadDocumentRows(ctx context.Context, db *sql.DB, taskUUID string) (map[string]documentRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT rel_path, status, file_size, modified_at, content_hash
		FROM gt_task_document_uploads WHERE task_uuid=?`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]documentRow, 32)
	for rows.Next() {
		var relPath string
		var row documentRow
		if err := rows.Scan(&relPath, &row.status, &row.fileSize, &row.modifiedAt, &row.contentHash); err != nil {
			return nil, err
		}
		result[relPath] = row
	}
	return result, rows.Err()
}

// applyDocumentWrites 在单个事务内落盘扫描结果，避免逐行写放大。
func applyDocumentWrites(ctx context.Context, db *sql.DB, taskUUID string, writes []documentUploadWrite, deleted []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, write := range writes {
		if write.touchOnly {
			if _, err := tx.ExecContext(ctx, `UPDATE gt_task_document_uploads
				SET file_ext=?, file_size=?, modified_at=?, updated_at=? WHERE task_uuid=? AND rel_path=?`,
				write.ext, write.size, write.modifiedAt, write.updatedAt, taskUUID, write.relPath); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gt_task_document_uploads
			(task_uuid, rel_path, doc_key, file_ext, file_size, modified_at, content_hash, status,
			 attempts, next_attempt_at, last_error, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, 0, 0, ?, ?)
			ON CONFLICT(task_uuid, rel_path) DO UPDATE SET
				file_ext=excluded.file_ext, file_size=excluded.file_size, modified_at=excluded.modified_at,
				status=excluded.status, attempts=excluded.attempts, next_attempt_at=excluded.next_attempt_at,
				last_error=excluded.last_error, updated_at=excluded.updated_at`,
			taskUUID, write.relPath, DocumentDocKey(write.relPath), write.ext, write.size, write.modifiedAt,
			write.status, write.lastError, write.updatedAt); err != nil {
			return err
		}
	}
	for _, relPath := range deleted {
		if _, err := tx.ExecContext(ctx, `UPDATE gt_task_document_uploads
			SET status=?, updated_at=? WHERE task_uuid=? AND rel_path=?`,
			documentStatusDeleted, time.Now().UnixMilli(), taskUUID, relPath); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// hashDocumentFile 计算文件内容 sha256（用于变更判定与上报校验）。
func hashDocumentFile(absPath string) (string, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// TaskAutoUploadEnabled 读取任务级「同步 Agent 产出」开关。
// 缺行/读取失败时按开启处理会误报文件，因此失败按关闭处理：本轮跳过，
// 等下一次扫描或用户手动上报，避免把用户明确选择不上报的产出推上线上。
func TaskAutoUploadEnabled(ctx context.Context, db *sql.DB, taskUUID string) bool {
	if db == nil || taskUUID == "" {
		return false
	}
	var enabled bool
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(sync_agent_output,1) FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&enabled)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			applog.Warn("[DocSync] 读取任务产出同步开关失败，本轮按不上报处理",
				"task_uuid", taskUUID, "error", err.Error())
		}
		return false
	}
	return enabled
}
