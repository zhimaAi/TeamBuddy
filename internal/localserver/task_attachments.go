package localserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"goteams-client/internal/i18n"
)

const (
	taskAttachmentDirectory             = "attachments"
	legacyTaskAttachmentDirectory       = ".goteams-attachments"
	maxTaskAttachmentSize         int64 = 10 * 1024 * 1024
	maxTaskAttachmentTotalSize          = 30 * 1024 * 1024
	maxTaskAttachmentCount              = 8
	maxTaskAttachmentRequestSize        = maxTaskAttachmentSize + 1024*1024
	maxTaskCreateRequestSize            = 45 * 1024 * 1024
)

var errTaskNotFound = errors.New("任务不存在")

var taskAttachmentMarkdownReferencePattern = regexp.MustCompile(`\]\((?:\./)?attachments/([0-9a-fA-F-]{36}\.[a-zA-Z0-9]{1,16})\)`)
var pendingTaskAttachmentMarkerPattern = regexp.MustCompile(`^!?\[[^\r\n]*\]\(attachments/pending-([0-9a-fA-F-]{36})(?:\.[a-zA-Z0-9]{1,16})?\)$`)
var pendingTaskAttachmentMarkerStripPattern = regexp.MustCompile(`!?\[[^\r\n]*\]\(attachments/pending-[0-9a-fA-F-]{36}(?:\.[a-zA-Z0-9]{1,16})?\)`)

type taskAttachmentInput struct {
	Name   string `json:"name"`
	Data   string `json:"data"`
	Marker string `json:"marker"`
}

type savedTaskAttachment struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
	AbsolutePath string `json:"absolute_path"`
	Markdown     string `json:"markdown"`
}

func (h *TasksHandler) uploadTaskAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxTaskAttachmentRequestSize)
	header, err := c.FormFile("file")
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			i18n.Error(c, http.StatusRequestEntityTooLarge, "localserver_attachment_size_exceeded", "attachment_size_exceeded")
			return
		}
		if errors.Is(err, http.ErrMissingFile) {
			i18n.Error(c, http.StatusBadRequest, "localserver_upload_file_missing", "file_required")
			return
		}
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if header.Size <= 0 {
		i18n.Error(c, http.StatusBadRequest, "localserver_file_empty", "file_empty")
		return
	}
	if header.Size > maxTaskAttachmentSize {
		i18n.Error(c, http.StatusRequestEntityTooLarge, "localserver_attachment_size_exceeded", "attachment_size_exceeded")
		return
	}

	source, err := header.Open()
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, maxTaskAttachmentSize+1))
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	if int64(len(data)) > maxTaskAttachmentSize {
		i18n.Error(c, http.StatusRequestEntityTooLarge, "localserver_attachment_size_exceeded", "attachment_size_exceeded")
		return
	}

	taskDir, err := h.taskDirectory(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errTaskNotFound) {
			status = http.StatusNotFound
		}
		i18n.LocalServerError(c, status, err)
		return
	}
	attachment, err := saveTaskAttachment(taskDir, header.Filename, data)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusCreated, attachment)
}

const taskImageMarkerPrefix = "[[TASK_IMAGE_"

var taskImageMarkerPattern = regexp.MustCompile(`\[\[TASK_IMAGE_[0-9a-fA-F-]{36}\]\]`)

// appendDraftTaskAttachments 保存任务创建前粘贴的附件。带有 marker 的新请求会原位替换
// task.md 和用户可见描述中的占位符，使两者都按输入顺序保留 Markdown 相对引用。
func (h *TasksHandler) appendDraftTaskAttachments(
	ctx context.Context,
	taskUUID string,
	description string,
	inputs []taskAttachmentInput,
) error {
	if len(inputs) == 0 {
		return nil
	}
	if len(inputs) > maxTaskAttachmentCount {
		return fmt.Errorf("最多可上传 %d 个文件", maxTaskAttachmentCount)
	}

	taskDir, err := h.taskDirectory(ctx, taskUUID)
	if err != nil {
		return err
	}
	seenMarkers := make(map[string]struct{}, len(inputs))
	decoded := make([][]byte, len(inputs))
	hasPositionMarkers := false
	hasLegacyAttachments := false
	var totalSize int64
	for index, input := range inputs {
		marker := strings.TrimSpace(input.Marker)
		if marker != "" {
			hasPositionMarkers = true
			if !isTaskImageMarker(marker) {
				return fmt.Errorf("附件位置标记无效")
			}
			if _, exists := seenMarkers[marker]; exists {
				return fmt.Errorf("附件位置标记重复")
			}
			seenMarkers[marker] = struct{}{}
		} else {
			hasLegacyAttachments = true
		}
		data, decodeErr := decodeTaskAttachmentData(input.Data)
		if decodeErr != nil {
			return decodeErr
		}
		if int64(len(data)) > maxTaskAttachmentSize {
			return fmt.Errorf("文件大小不能超过 10MB")
		}
		totalSize += int64(len(data))
		if totalSize > maxTaskAttachmentTotalSize {
			return fmt.Errorf("上传文件总大小不能超过 30MB")
		}
		decoded[index] = data
	}
	if hasPositionMarkers && hasLegacyAttachments {
		return fmt.Errorf("附件必须全部包含位置标记")
	}

	taskMarkdownPath := filepath.Join(taskDir, "task.md")
	content, err := os.ReadFile(taskMarkdownPath)
	if err != nil {
		return fmt.Errorf("读取任务文档失败: %w", err)
	}
	if hasPositionMarkers {
		for marker := range seenMarkers {
			if strings.Count(string(content), marker) != 1 {
				return fmt.Errorf("附件位置标记未在任务描述中找到")
			}
		}
	}

	type savedDraftAttachment struct {
		marker   string
		markdown string
	}
	savedAttachments := make([]savedDraftAttachment, 0, len(inputs))
	fallbackMarkdown := make([]string, 0, len(inputs))
	for index, data := range decoded {
		attachment, saveErr := saveTaskAttachment(taskDir, inputs[index].Name, data)
		if saveErr != nil {
			return saveErr
		}
		marker := strings.TrimSpace(inputs[index].Marker)
		if marker == "" {
			fallbackMarkdown = append(fallbackMarkdown, attachment.Markdown)
			continue
		}
		savedAttachments = append(savedAttachments, savedDraftAttachment{
			marker:   marker,
			markdown: attachment.Markdown,
		})
	}

	updated := string(content)
	updatedDescription := description
	for _, attachment := range savedAttachments {
		// 上面已验证每个 marker 恰好出现一次；只替换这一处，避免一个损坏
		// 的请求把同一路径复制到多个不确定的位置。
		updated = strings.Replace(updated, attachment.marker, attachment.markdown, 1)
		updatedDescription = strings.Replace(updatedDescription, attachment.marker, attachment.markdown, 1)
	}
	updated = stripTaskImageMarkers(updated)
	updatedDescription = stripTaskImageMarkers(updatedDescription)
	if len(fallbackMarkdown) > 0 {
		section := "## 已上传附件\n\n" + strings.Join(prefixLines(fallbackMarkdown, "- "), "\n")
		updated = strings.TrimRight(updated, "\n") + "\n\n" + section + "\n"
		updatedDescription = strings.TrimRight(updatedDescription, "\n") + "\n\n" + section
	}
	if err := os.WriteFile(taskMarkdownPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("更新任务文档失败: %w", err)
	}
	if _, err := h.taskDB().ExecContext(ctx,
		`UPDATE gt_tasks SET content_snapshot=?, updated_at=? WHERE uuid=?`,
		updatedDescription, time.Now().UnixMilli(), taskUUID); err != nil {
		return fmt.Errorf("更新任务描述失败: %w", err)
	}
	return nil
}

func isTaskImageMarker(marker string) bool {
	if match := pendingTaskAttachmentMarkerPattern.FindStringSubmatch(marker); len(match) == 2 {
		_, err := uuid.Parse(match[1])
		return err == nil
	}
	if !strings.HasPrefix(marker, taskImageMarkerPrefix) || !strings.HasSuffix(marker, "]]") {
		return false
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(marker, taskImageMarkerPrefix), "]]")
	_, err := uuid.Parse(identifier)
	return err == nil
}

func stripTaskImageMarkers(content string) string {
	content = taskImageMarkerPattern.ReplaceAllString(content, "")
	return pendingTaskAttachmentMarkerStripPattern.ReplaceAllString(content, "")
}

func (h *TasksHandler) taskDirectory(ctx context.Context, taskUUID string) (string, error) {
	var taskDir string
	err := h.taskDB().QueryRowContext(ctx, `SELECT task_dir FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskDir)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", errTaskNotFound
		}
		return "", fmt.Errorf("查询任务失败: %w", err)
	}
	if strings.TrimSpace(taskDir) == "" {
		return "", fmt.Errorf("任务目录为空")
	}
	root, err := filepath.Abs(taskDir)
	if err != nil {
		return "", fmt.Errorf("任务目录无效: %w", err)
	}
	return root, nil
}

func saveTaskAttachment(taskDir, originalName string, data []byte) (savedTaskAttachment, error) {
	return saveTaskAttachmentWithID(taskDir, originalName, uuid.NewString(), data)
}

func saveTaskAttachmentWithID(taskDir, originalName, id string, data []byte) (savedTaskAttachment, error) {
	if _, err := uuid.Parse(id); err != nil {
		return savedTaskAttachment{}, err
	}
	if len(data) == 0 {
		return savedTaskAttachment{}, fmt.Errorf("文件内容为空")
	}
	if int64(len(data)) > maxTaskAttachmentSize {
		return savedTaskAttachment{}, fmt.Errorf("文件大小不能超过 10MB")
	}
	displayName := taskAttachmentDisplayName(originalName)
	extension, isImage, err := taskAttachmentExtension(displayName, data)
	if err != nil {
		return savedTaskAttachment{}, err
	}
	attachmentDir := filepath.Join(taskDir, taskAttachmentDirectory)
	if info, err := os.Lstat(attachmentDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return savedTaskAttachment{}, fmt.Errorf("附件目录不允许使用符号链接")
	}
	if err := os.MkdirAll(attachmentDir, 0o700); err != nil {
		return savedTaskAttachment{}, fmt.Errorf("创建附件目录失败: %w", err)
	}

	filename := id + extension
	relativePath := filepath.Join(taskAttachmentDirectory, filename)
	target := filepath.Join(attachmentDir, filename)
	temporary, err := os.CreateTemp(attachmentDir, ".attachment-upload-*")
	if err != nil {
		return savedTaskAttachment{}, fmt.Errorf("创建附件临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return savedTaskAttachment{}, fmt.Errorf("设置附件权限失败: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return savedTaskAttachment{}, fmt.Errorf("保存文件失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return savedTaskAttachment{}, fmt.Errorf("保存文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return savedTaskAttachment{}, fmt.Errorf("提交文件失败: %w", err)
	}

	return savedTaskAttachment{
		Name:         displayName,
		RelativePath: filepath.ToSlash(relativePath),
		AbsolutePath: target,
		Markdown:     taskAttachmentMarkdown(displayName, filepath.ToSlash(relativePath), isImage),
	}, nil
}

func decodeTaskAttachmentData(value string) ([]byte, error) {
	parts := strings.SplitN(strings.TrimSpace(value), ",", 2)
	if len(parts) != 2 || !strings.HasPrefix(strings.ToLower(parts[0]), "data:") || !strings.Contains(strings.ToLower(parts[0]), ";base64") {
		return nil, fmt.Errorf("文件数据格式无效")
	}
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("文件数据解码失败: %w", err)
	}
	return data, nil
}

func taskAttachmentExtension(displayName string, data []byte) (string, bool, error) {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch contentType {
	case "image/png":
		return ".png", true, nil
	case "image/jpeg":
		return ".jpg", true, nil
	case "image/webp":
		return ".webp", true, nil
	case "image/gif":
		return ".gif", true, nil
	}
	extension := strings.ToLower(filepath.Ext(displayName))
	if !validTaskAttachmentExtension(extension) {
		extension = ".bin"
	}
	if isTaskAttachmentImageExtension(extension) {
		return "", false, fmt.Errorf("图片内容与文件格式不匹配")
	}
	return extension, false, nil
}

func taskAttachmentDisplayName(rawName string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(rawName, "\\", "/")))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "attachment"
	}
	return name
}

func validTaskAttachmentExtension(extension string) bool {
	if len(extension) < 2 || len(extension) > 17 || extension[0] != '.' {
		return false
	}
	for _, char := range extension[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func isTaskAttachmentImageExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func taskAttachmentMarkdown(name, relativePath string, image bool) string {
	label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(name)
	target := filepath.ToSlash(relativePath)
	if image {
		return fmt.Sprintf("![%s](%s)", label, target)
	}
	return fmt.Sprintf("[%s](%s)", label, target)
}

// resolveTaskAttachmentReferences keeps the stored/user-visible message portable while
// giving the local CLI an absolute path it can access from any project working directory.
func (h *TasksHandler) resolveTaskAttachmentReferences(ctx context.Context, taskUUID, content string) (string, error) {
	matches := taskAttachmentMarkdownReferencePattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	taskDir, err := h.taskDirectory(ctx, taskUUID)
	if err != nil {
		return "", err
	}
	resolved := content
	for _, match := range matches {
		relativePath := filepath.Join(taskAttachmentDirectory, match[1])
		absolutePath, pathErr := h.safeTaskFilePath(taskDir, relativePath)
		if pathErr != nil {
			return "", pathErr
		}
		info, statErr := os.Stat(absolutePath)
		if statErr != nil || info.IsDir() {
			return "", fmt.Errorf("引用的附件不存在: %s", filepath.ToSlash(relativePath))
		}
		absoluteMarkdownPath := strings.ReplaceAll(absolutePath, "\\", "/")
		resolved = strings.ReplaceAll(resolved, match[0], "](<"+absoluteMarkdownPath+">)")
	}
	return resolved, nil
}

func (h *TasksHandler) resolveSessionTaskAttachmentReferences(ctx context.Context, sessionUUID, content string) (string, error) {
	if !taskAttachmentMarkdownReferencePattern.MatchString(content) {
		return content, nil
	}
	var taskUUID string
	if err := h.taskDB().QueryRowContext(ctx, `SELECT task_uuid FROM gt_cli_sessions WHERE uuid=?`, sessionUUID).Scan(&taskUUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("历史会话不存在")
		}
		return "", fmt.Errorf("读取会话任务失败: %w", err)
	}
	return h.resolveTaskAttachmentReferences(ctx, taskUUID, content)
}

func (h *TasksHandler) getTaskAttachmentContent(c *gin.Context) {
	relPath := filepath.Clean(strings.TrimSpace(c.Query("path")))
	if relPath == "." || filepath.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		i18n.Error(c, http.StatusBadRequest, "localserver_attachment_path_invalid", "attachment_path_invalid")
		return
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) != 2 || parts[0] != taskAttachmentDirectory || parts[1] == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_attachment_current_task_only", "attachment_current_task_only")
		return
	}
	taskDir, err := h.taskDirectory(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errTaskNotFound) {
			status = http.StatusNotFound
		}
		i18n.LocalServerError(c, status, err)
		return
	}
	absPath, err := h.safeTaskFilePath(taskDir, relPath)
	if err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	serveTaskAttachment(c, absPath)
}

func serveTaskAttachment(c *gin.Context, absPath string) {
	file, err := os.Open(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			i18n.Error(c, http.StatusNotFound, "localserver_attachment_not_found", "attachment_not_found")
			return
		}
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		i18n.Error(c, http.StatusNotFound, "localserver_attachment_not_found", "attachment_not_found")
		return
	}
	header := make([]byte, 512)
	read, _ := file.Read(header)
	_, _ = file.Seek(0, io.SeekStart)
	contentType := http.DetectContentType(header[:read])
	inline := contentType == "image/png" || contentType == "image/jpeg" || contentType == "image/webp" || contentType == "image/gif"
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Name()}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, info.Size(), contentType, file, nil)
}

func prefixLines(values []string, prefix string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, prefix+value)
	}
	return result
}
