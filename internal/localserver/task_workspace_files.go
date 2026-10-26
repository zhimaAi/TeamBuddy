package localserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/i18n"
)

const (
	workspaceEntryLimit       = 1000
	workspaceSearchLimit      = 100
	workspaceSearchVisitLimit = 10000
	workspaceTextLimit        = 2 << 20
	workspaceImageLimit       = 10 << 20
)

var (
	errWorkspaceRootNotFound = errors.New("workspace root not found")
	errWorkspaceUnavailable  = errors.New("workspace unavailable")
	errWorkspacePathInvalid  = errors.New("workspace path invalid")
	errWorkspacePathOutside  = errors.New("workspace path outside root")
	errWorkspacePathSymlink  = errors.New("workspace path contains symlink")
	errWorkspaceFileTooLarge = errors.New("workspace file exceeds limit")
)

type taskWorkspaceRoot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Primary   bool   `json:"primary"`
	Available bool   `json:"available"`
	ErrorCode string `json:"error_code,omitempty"`
}

type taskWorkspaceEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	FileType   string `json:"file_type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt int64  `json:"modified_at,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Ignored    bool   `json:"ignored,omitempty"`
}

func (h *TasksHandler) listTaskWorkspaceRoots(c *gin.Context) {
	roots, err := h.loadTaskWorkspaceRoots(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"roots": roots})
}

func (h *TasksHandler) listTaskWorkspaceEntries(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	relativePath := c.Query("path")
	workspace, err := openTaskWorkspaceRoot(root.Path)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	defer workspace.Close()
	scanCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	result, reason, _, err := scanTaskWorkspace(scanCtx, workspace, relativePath, false, c.Query("show_ignored") == "true", "", workspaceEntryLimit)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	sortWorkspaceEntries(result)
	truncated := reason != ""
	c.JSON(http.StatusOK, gin.H{
		"root_id":   root.ID,
		"path":      filepath.ToSlash(filepath.Clean(relativePath)),
		"entries":   result,
		"truncated": truncated,
		"limit":     workspaceEntryLimit,
	})
}

func (h *TasksHandler) getTaskWorkspaceContent(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	relativePath := c.Query("path")
	if relativePath == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_path_required", "path_required")
		return
	}
	file, info, target, err := openVerifiedWorkspacePath(root.Path, relativePath, false)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	defer file.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(target), "."))
	encoding := "text"
	limit := int64(workspaceTextLimit)
	if isImageFileType(ext) {
		encoding = "base64"
		limit = workspaceImageLimit
	}
	if info.Size() > limit {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": i18n.T(c, "localserver_bad_request"),
			"code":  "workspace_file_too_large",
			"size":  info.Size(),
			"limit": limit,
		})
		return
	}
	content, err := readWorkspaceFile(file, limit)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	if encoding == "text" && !workspaceTextFile(target, content) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": i18n.Format(c, "localserver_readable_file_type", i18n.Params{"FileType": ext}),
			"code":  "workspace_binary_file",
		})
		return
	}
	rendered := string(content)
	if encoding == "base64" {
		rendered = base64.StdEncoding.EncodeToString(content)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(target))
	if mimeType == "" && encoding == "text" {
		mimeType = "text/plain; charset=utf-8"
	}
	c.JSON(http.StatusOK, gin.H{
		"root_id":     root.ID,
		"path":        filepath.ToSlash(relativePath),
		"name":        filepath.Base(target),
		"file_type":   ext,
		"mime_type":   mimeType,
		"size":        info.Size(),
		"modified_at": info.ModTime().UnixMilli(),
		"encoding":    encoding,
		"content":     rendered,
		"read_only":   true,
	})
}

func (h *TasksHandler) searchTaskWorkspace(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if len([]rune(query)) < 2 {
		i18n.LocalServerKeyError(c, http.StatusBadRequest, "localserver_bad_request", "workspace_search_query_short")
		return
	}
	limit := workspaceSearchLimit
	if requested, parseErr := strconv.Atoi(c.Query("limit")); parseErr == nil && requested > 0 {
		limit = min(requested, workspaceSearchLimit)
	}
	searchCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	workspace, err := openTaskWorkspaceRoot(root.Path)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	defer workspace.Close()
	results, truncationReason, partial, err := scanTaskWorkspace(searchCtx, workspace, ".", true, c.Query("show_ignored") == "true", query, limit)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	truncated := truncationReason != ""
	sortWorkspaceEntries(results)
	c.JSON(http.StatusOK, gin.H{
		"root_id":           root.ID,
		"query":             query,
		"entries":           results,
		"truncated":         truncated,
		"truncation_reason": truncationReason,
		"partial":           partial,
		"limit":             limit,
	})
}

func (h *TasksHandler) loadTaskWorkspaceRoots(ctx context.Context, taskUUID string) ([]taskWorkspaceRoot, error) {
	db := h.taskDB()
	if db == nil {
		return nil, errors.New("本地任务数据库尚未就绪")
	}
	var primaryPath string
	if err := db.QueryRowContext(ctx, `SELECT work_dir FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&primaryPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	paths := make([]string, 0, 4)
	if strings.TrimSpace(primaryPath) != "" {
		paths = append(paths, primaryPath)
	}
	rows, err := db.QueryContext(ctx, `SELECT path FROM gt_task_work_dirs WHERE task_uuid = ? ORDER BY sort_order, id`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	roots := make([]taskWorkspaceRoot, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		absolute, err := filepath.Abs(rawPath)
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		key := workspacePathKey(absolute)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		available, errorCode := workspaceRootAvailability(absolute)
		roots = append(roots, taskWorkspaceRoot{
			ID:        workspaceRootID(taskUUID, absolute),
			Name:      workspaceRootName(absolute),
			Path:      absolute,
			Primary:   len(roots) == 0,
			Available: available,
			ErrorCode: errorCode,
		})
	}
	return roots, nil
}

func (h *TasksHandler) resolveTaskWorkspaceRoot(ctx context.Context, taskUUID, rootID string) (taskWorkspaceRoot, error) {
	if strings.TrimSpace(rootID) == "" {
		return taskWorkspaceRoot{}, errWorkspaceRootNotFound
	}
	roots, err := h.loadTaskWorkspaceRoots(ctx, taskUUID)
	if err != nil {
		return taskWorkspaceRoot{}, err
	}
	for _, root := range roots {
		if root.ID != rootID {
			continue
		}
		if !root.Available {
			return taskWorkspaceRoot{}, errWorkspaceUnavailable
		}
		return root, nil
	}
	return taskWorkspaceRoot{}, errWorkspaceRootNotFound
}

func (h *TasksHandler) writeWorkspaceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
	case errors.Is(err, errWorkspaceRootNotFound):
		i18n.LocalServerKeyError(c, http.StatusNotFound, "localserver_not_found", "workspace_root_not_found")
	case errors.Is(err, errWorkspaceUnavailable), errors.Is(err, os.ErrPermission):
		i18n.Error(c, http.StatusBadRequest, "localserver_workspace_unavailable", "workspace_unavailable")
	case errors.Is(err, errWorkspacePathOutside):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_outside", "file_path_outside")
	case errors.Is(err, errWorkspacePathSymlink):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_symlink", "file_path_symlink")
	case errors.Is(err, errWorkspacePathInvalid):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_invalid", "file_path_invalid")
	case errors.Is(err, errWorkspaceFileTooLarge):
		i18n.LocalServerKeyError(c, http.StatusRequestEntityTooLarge, "localserver_bad_request", "workspace_file_too_large")
	case errors.Is(err, os.ErrNotExist):
		i18n.Error(c, http.StatusNotFound, "localserver_file_not_found", "file_not_found")
	default:
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
	}
}

// 根目录在一次请求中只打开一次；后续解析均受 os.Root 的句柄边界约束。
func openTaskWorkspaceRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, errWorkspacePathSymlink
	}
	if !before.IsDir() {
		return nil, errWorkspacePathInvalid
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		_ = root.Close()
		return nil, errWorkspacePathSymlink
	}
	return root, nil
}

func openVerifiedWorkspacePath(root, relativePath string, requireDirectory bool) (*os.File, os.FileInfo, string, error) {
	workspace, err := openTaskWorkspaceRoot(root)
	if err != nil {
		return nil, nil, "", err
	}
	defer workspace.Close()
	file, info, err := openTaskWorkspacePath(workspace, relativePath, requireDirectory)
	return file, info, filepath.Join(root, relativePath), err
}

func openTaskWorkspacePath(root *os.Root, relativePath string, requireDirectory bool) (*os.File, os.FileInfo, error) {
	clean := filepath.Clean(relativePath)
	if !filepath.IsLocal(clean) {
		return nil, nil, errWorkspacePathOutside
	}
	// 保留禁止内部符号链接的策略；即使校验后路径被替换，os.Root 也禁止逃出根目录。
	current := "."
	var before os.FileInfo
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, errWorkspacePathSymlink
		}
		before = info
	}
	if (requireDirectory && !before.IsDir()) || (!requireDirectory && !before.Mode().IsRegular()) {
		return nil, nil, errWorkspacePathInvalid
	}
	file, err := openWorkspaceRootFile(root, clean)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		_ = file.Close()
		return nil, nil, errWorkspacePathSymlink
	}
	return file, info, nil
}

// 分批枚举且仅保留有界结果，不使用会一次性读取整个目录的 WalkDir。
func scanTaskWorkspace(ctx context.Context, root *os.Root, start string, recursive, showIgnored bool, query string, limit int) ([]taskWorkspaceEntry, string, bool, error) {
	results := make([]taskWorkspaceEntry, 0, limit)
	pending := []string{filepath.Clean(start)}
	visited, reason, partial := 0, "", false
	for len(pending) > 0 && reason == "" {
		if ctx.Err() != nil {
			reason = "timeout"
			break
		}
		path := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		err := func() error {
			directory, _, err := openTaskWorkspacePath(root, path, true)
			if err != nil {
				return err
			}
			defer directory.Close()
			for reason == "" {
				if ctx.Err() != nil {
					reason = "timeout"
					break
				}
				if visited >= workspaceSearchVisitLimit {
					reason = "visit_limit"
					break
				}
				entries, readErr := directory.ReadDir(min(64, workspaceSearchVisitLimit-visited))
				for _, entry := range entries {
					if ctx.Err() != nil {
						reason = "timeout"
						break
					}
					visited++
					if entry.Name() == ".git" || entry.Type()&os.ModeSymlink != 0 {
						continue
					}
					relative := filepath.Join(path, entry.Name())
					// DirEntry.Info 使用路径名称；改用受根句柄约束的 Lstat 获取元数据。
					info, err := root.Lstat(relative)
					if err != nil {
						partial = true
						continue
					}
					if !info.IsDir() && !info.Mode().IsRegular() {
						continue
					}
					ignored := workspaceIgnoredName(entry.Name(), info.IsDir())
					if ignored && !showIgnored {
						continue
					}
					if recursive && info.IsDir() {
						pending = append(pending, relative)
					}
					if query != "" && !strings.Contains(strings.ToLower(entry.Name()), query) {
						continue
					}
					results = append(results, workspaceEntryFromInfo(relative, info, ignored))
					if len(results) > limit {
						results = results[:limit]
						reason = "result_limit"
						break
					}
				}
				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						return nil
					}
					return readErr
				}
			}
			return nil
		}()
		if err != nil {
			if path == filepath.Clean(start) {
				return nil, "", false, err
			}
			partial = true
		}
	}
	return results, reason, partial, nil
}

func safeWorkspacePath(root, relativePath string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil || root == "" {
		return "", errWorkspacePathInvalid
	}
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errWorkspacePathSymlink
	}
	// 允许工作目录的祖先使用系统路径别名，但根目录和内部节点仍禁止符号链接。
	absoluteRoot, err = filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(relativePath, "/", string(filepath.Separator))
	cleanPath := filepath.Clean(normalized)
	if cleanPath == "." && normalized == "" {
		cleanPath = "."
	}
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", errWorkspacePathOutside
	}
	target := filepath.Join(absoluteRoot, cleanPath)
	if !pathWithinDirectory(absoluteRoot, target) {
		return "", errWorkspacePathOutside
	}
	containsSymlink, err := pathContainsSymlink(absoluteRoot, target)
	if err != nil {
		return "", err
	}
	if containsSymlink {
		return "", errWorkspacePathSymlink
	}
	return target, nil
}

func workspaceEntryFromDirEntry(parent string, entry os.DirEntry, ignored bool) (taskWorkspaceEntry, bool) {
	if entry.Type()&os.ModeSymlink != 0 {
		return taskWorkspaceEntry{}, false
	}
	info, err := entry.Info()
	if err != nil {
		return taskWorkspaceEntry{}, false
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return taskWorkspaceEntry{}, false
	}
	return workspaceEntryFromInfo(filepath.Join(parent, entry.Name()), info, ignored), true
}

func workspaceEntryFromInfo(relativePath string, info os.FileInfo, ignored bool) taskWorkspaceEntry {
	return taskWorkspaceEntry{
		Name:       info.Name(),
		Path:       filepath.ToSlash(relativePath),
		IsDir:      info.IsDir(),
		FileType:   strings.ToLower(strings.TrimPrefix(filepath.Ext(info.Name()), ".")),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UnixMilli(),
		Hidden:     strings.HasPrefix(info.Name(), "."),
		Ignored:    ignored,
	}
}

func sortWorkspaceEntries(entries []taskWorkspaceEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		left := strings.ToLower(entries[i].Name)
		right := strings.ToLower(entries[j].Name)
		if left == right {
			return entries[i].Name < entries[j].Name
		}
		return left < right
	})
}

func workspaceIgnoredName(name string, isDir bool) bool {
	if !isDir {
		return false
	}
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", ".cache", ".next", ".nuxt", ".turbo", "coverage", "target":
		return true
	default:
		return false
	}
}

func workspaceRootID(taskUUID, path string) string {
	sum := sha256.Sum256([]byte(taskUUID + "\x00" + workspacePathKey(path)))
	return fmt.Sprintf("root-%x", sum[:8])
}

func workspacePathKey(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func workspaceRootAvailability(path string) (bool, string) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "workspace_root_missing"
		}
		if errors.Is(err, os.ErrPermission) {
			return false, "workspace_root_permission_denied"
		}
		return false, "workspace_root_unavailable"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "workspace_root_symlink"
	}
	if !info.IsDir() {
		return false, "workspace_root_not_directory"
	}
	return true, ""
}

func workspaceRootName(path string) string {
	clean := filepath.Clean(path)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return clean
	}
	return name
}

func readWorkspaceFile(file *os.File, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errWorkspaceFileTooLarge
	}
	return content, nil
}

func workspaceTextFile(path string, content []byte) bool {
	if !utf8.Valid(content) {
		return false
	}
	for _, b := range content {
		if b == 0 {
			return false
		}
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if isTextFileType(ext) {
		return true
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "dockerfile", "makefile", "license", "readme", "notice":
		return true
	}
	return true
}
