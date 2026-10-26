package localserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goteams-client/internal/i18n"
)

const maxOutputFileReferenceLength = 4096

var (
	errOutputFileInvalid   = errors.New("output file reference invalid")
	errOutputFileOutside   = errors.New("output file outside allowed roots")
	errOutputFileNotFound  = errors.New("output file not found")
	errOutputFileNotFile   = errors.New("output file is not a regular file")
	errOutputFileSymlink   = errors.New("output file path contains symlink")
	errOutputFileAmbiguous = errors.New("output file reference is ambiguous")
)

// locateTaskOutputFile resolves a file reference emitted by a CLI or app to one
// regular file. Relative references stay inside the current task directory or
// its project directories. Absolute references may also land in another
// configured workspace or project directory.
func (h *TasksHandler) locateTaskOutputFile(c *gin.Context) {
	taskUUID := c.Param("uuid")
	var body struct {
		Path        string `json:"path"`
		SessionUUID string `json:"session_uuid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.Error(c, http.StatusBadRequest, "localserver_output_file_invalid", "output_file_invalid")
		return
	}

	var taskDir string
	var legacyWorkDir sql.NullString
	err := h.taskDB().QueryRowContext(
		c.Request.Context(),
		`SELECT task_dir, work_dir FROM gt_tasks WHERE uuid=?`,
		taskUUID,
	).Scan(&taskDir, &legacyWorkDir)
	if errors.Is(err, sql.ErrNoRows) {
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	workDirs, err := h.loadTaskWorkDirs(taskUUID, legacyWorkDir.String)
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}

	roots := make([]string, 0, len(workDirs)+1)
	roots = append(roots, workDirs...)
	if strings.TrimSpace(taskDir) != "" {
		roots = append(roots, taskDir)
	}
	preferred, err := h.preferredOutputRoots(c.Request.Context(), taskUUID, body.SessionUUID)
	if errors.Is(err, errOutputFileInvalid) {
		i18n.Error(c, http.StatusBadRequest, "localserver_output_file_invalid", "output_file_invalid")
		return
	}
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	workspaceRoots, err := h.configuredWorkspaceRoots(c.Request.Context())
	if err != nil {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	located, err := locateOutputFile(roots, preferred, body.Path, workspaceRoots...)
	switch {
	case errors.Is(err, errOutputFileInvalid):
		i18n.Error(c, http.StatusBadRequest, "localserver_output_file_invalid", "output_file_invalid")
	case errors.Is(err, errOutputFileOutside):
		i18n.Error(c, http.StatusBadRequest, "localserver_output_file_outside", "output_file_outside")
	case errors.Is(err, errOutputFileNotFound):
		i18n.Error(c, http.StatusNotFound, "localserver_output_file_not_found", "output_file_not_found")
	case errors.Is(err, errOutputFileNotFile):
		i18n.Error(c, http.StatusBadRequest, "localserver_output_file_not_file", "output_file_not_file")
	case errors.Is(err, errOutputFileSymlink):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_symlink", "file_path_symlink")
	case errors.Is(err, errOutputFileAmbiguous):
		i18n.Error(c, http.StatusConflict, "localserver_output_file_ambiguous", "output_file_ambiguous")
	case err != nil:
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
	default:
		c.JSON(http.StatusOK, gin.H{"path": located})
	}
}

func (h *TasksHandler) preferredOutputRoots(ctx context.Context, taskUUID, sessionUUID string) ([]string, error) {
	sessionUUID = strings.TrimSpace(sessionUUID)
	if sessionUUID == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(sessionUUID); err != nil {
		return nil, errOutputFileInvalid
	}
	var workDir string
	err := h.taskDB().QueryRowContext(ctx,
		`SELECT work_dir FROM gt_cli_sessions WHERE uuid=? AND task_uuid=?`,
		sessionUUID, taskUUID).Scan(&workDir)
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(workDir) == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{workDir}, nil
}

func (h *TasksHandler) configuredWorkspaceRoots(ctx context.Context) ([]string, error) {
	rows, err := h.taskDB().QueryContext(ctx, `
		SELECT work_dir FROM gt_tasks WHERE work_dir <> ''
		UNION
		SELECT path FROM gt_task_work_dirs WHERE path <> ''
		UNION
		SELECT work_dir FROM gt_sidebar_workspaces WHERE work_dir <> ''
		UNION
		SELECT main_dir FROM gt_projects WHERE main_dir <> ''`)
	if err != nil {
		return nil, fmt.Errorf("查询已配置工作区目录失败: %w", err)
	}
	defer rows.Close()
	roots := make([]string, 0)
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, fmt.Errorf("读取已配置工作区目录失败: %w", err)
		}
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		roots = appendUniquePath(roots, dir)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历已配置工作区目录失败: %w", err)
	}
	return roots, nil
}

// locateOutputFile resolves raw against allowed roots. A preferred directory is
// used only when it is one of those roots or a directory inside one. Relative
// references stay in the task roots; a .. path is not opened. Multiple matches
// are rejected instead of guessed. extraAbsoluteRoots are other configured
// workspace or project directories and apply only to absolute references.
func locateOutputFile(roots []string, preferred []string, raw string, extraAbsoluteRoots ...string) (string, error) {
	candidate, err := parseLocalFileReference(raw)
	if err != nil {
		return "", err
	}
	prepared := prepareOutputRoots(roots)
	bases := append([]allowedOutputRoot{}, prepared...)
	preferredReals := map[string]struct{}{}
	for _, root := range prepareOutputRoots(preferred) {
		if !rootWithinAllowed(root, prepared) {
			continue
		}
		preferredReals[root.real] = struct{}{}
		if !containsOutputRoot(bases, root.real) {
			bases = append(bases, root)
		}
	}

	absolute := filepath.IsAbs(candidate)
	if !absolute {
		relative := filepath.Clean(candidate)
		if relative == "." || relative == "" || escapesDirectory(relative) {
			return "", errOutputFileInvalid
		}
	}
	if absolute {
		for _, root := range prepareOutputRoots(extraAbsoluteRoots) {
			if !containsOutputRoot(bases, root.real) {
				bases = append(bases, root)
			}
		}
	}
	var matches, preferredMatches []string
	contained := false
	for _, root := range bases {
		located, err := matchOutputFile(root, candidate, absolute)
		if err == nil {
			matches = appendUniquePath(matches, located)
			if _, ok := preferredReals[root.real]; ok {
				preferredMatches = appendUniquePath(preferredMatches, located)
			}
			continue
		}
		if errors.Is(err, errOutputFileNotFound) {
			contained = true
			continue
		}
		if errors.Is(err, errOutputFileOutside) || errors.Is(err, errOutputFileInvalid) {
			continue
		}
		return "", err
	}
	switch {
	case len(preferredMatches) == 1:
		return preferredMatches[0], nil
	case len(preferredMatches) > 1 || len(matches) > 1:
		return "", errOutputFileAmbiguous
	case len(matches) == 1:
		return matches[0], nil
	case absolute && !contained:
		return "", errOutputFileOutside
	default:
		return "", errOutputFileNotFound
	}
}

func prepareOutputRoots(roots []string) []allowedOutputRoot {
	prepared := make([]allowedOutputRoot, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		preparedRoot, err := prepareOutputRoot(root)
		if err != nil {
			continue
		}
		if _, ok := seen[preparedRoot.real]; ok {
			continue
		}
		seen[preparedRoot.real] = struct{}{}
		prepared = append(prepared, preparedRoot)
	}
	return prepared
}

func rootWithinAllowed(root allowedOutputRoot, allowed []allowedOutputRoot) bool {
	for _, candidate := range allowed {
		if root.real == candidate.real || pathWithinDirectory(candidate.real, root.real) {
			return true
		}
	}
	return false
}

func containsOutputRoot(roots []allowedOutputRoot, real string) bool {
	for _, root := range roots {
		if root.real == real {
			return true
		}
	}
	return false
}

func appendUniquePath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

var lineFragmentPattern = regexp.MustCompile(`(?i)^L?\d+$`)

func parseLocalFileReference(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxOutputFileReferenceLength || strings.ContainsRune(raw, 0) || strings.ContainsAny(raw, "\r\n") {
		return "", errOutputFileInvalid
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://"),
		strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(lower, "mailto:"),
		strings.HasPrefix(lower, "mention:"):
		return "", errOutputFileInvalid
	case strings.HasPrefix(lower, "file:"):
		return parseFileURL(raw)
	}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, `//`) {
		return "", errOutputFileInvalid
	}
	decoded, err := url.PathUnescape(stripLineFragment(raw))
	if err != nil || strings.TrimSpace(decoded) == "" || strings.ContainsRune(decoded, 0) {
		return "", errOutputFileInvalid
	}
	return decoded, nil
}

func stripLineFragment(raw string) string {
	hash := strings.LastIndex(raw, "#")
	if hash <= 0 || !lineFragmentPattern.MatchString(raw[hash+1:]) {
		return raw
	}
	return raw[:hash]
}

func parseFileURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") {
		return "", errOutputFileInvalid
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "", errOutputFileInvalid
	}
	if parsed.RawQuery != "" || (parsed.Fragment != "" && !lineFragmentPattern.MatchString(parsed.Fragment)) {
		return "", errOutputFileInvalid
	}
	filePath := parsed.Path
	if runtime.GOOS == "windows" && len(filePath) >= 3 && filePath[0] == '/' && filePath[2] == ':' {
		filePath = filePath[1:]
	}
	filePath = filepath.FromSlash(filePath)
	if !filepath.IsAbs(filePath) {
		return "", errOutputFileInvalid
	}
	return filePath, nil
}

type allowedOutputRoot struct {
	original string
	real     string
}

func prepareOutputRoot(root string) (allowedOutputRoot, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(absolute) == "" {
		return allowedOutputRoot{}, errOutputFileInvalid
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return allowedOutputRoot{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return allowedOutputRoot{}, err
	}
	if !info.IsDir() {
		return allowedOutputRoot{}, errOutputFileNotFile
	}
	return allowedOutputRoot{original: absolute, real: real}, nil
}

func matchOutputFile(root allowedOutputRoot, candidate string, absolute bool) (string, error) {
	var relative string
	if absolute {
		var err error
		relative, err = relativeWithinOutputRoot(root, candidate)
		if err != nil {
			return "", err
		}
	} else {
		relative = filepath.Clean(candidate)
		if relative == "." || relative == "" || escapesDirectory(relative) || filepath.IsAbs(relative) {
			return "", errOutputFileInvalid
		}
	}
	target := filepath.Join(root.real, relative)
	if !pathWithinDirectory(root.real, target) {
		return "", errOutputFileOutside
	}
	if err := rejectOutputSymlink(root.real, target); err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errOutputFileNotFound
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errOutputFileNotFile
	}
	return target, nil
}

func escapesDirectory(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func relativeWithinOutputRoot(root allowedOutputRoot, candidate string) (string, error) {
	clean := filepath.Clean(candidate)
	for _, base := range []string{root.real, root.original} {
		if pathWithinDirectory(base, clean) {
			relative, err := filepath.Rel(base, clean)
			if err == nil && !escapesDirectory(relative) && relative != "." {
				return relative, nil
			}
		}
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errOutputFileNotFound
		}
		return "", err
	}
	if pathWithinDirectory(root.real, real) {
		relative, err := filepath.Rel(root.real, real)
		if err == nil && !escapesDirectory(relative) && relative != "." {
			return relative, nil
		}
	}
	return "", errOutputFileOutside
}

func rejectOutputSymlink(root, target string) error {
	if !pathWithinDirectory(root, target) {
		return errOutputFileOutside
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return errOutputFileOutside
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errOutputFileNotFound
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errOutputFileSymlink
		}
	}
	return nil
}
