package localserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"goteams-client/internal/codexmessage"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskskillclient"
)

// The App prompt stays immutable for event replay and cloud history. Only the local view is projected.
func projectCodexMessage(id, content, mode, tool string) *codexmessage.Message {
	if mode != "vibe_coding" || tool != "codex" {
		return nil
	}
	return codexmessage.Parse(content, id)
}

func codexImageCache() string {
	return filepath.Join(filepath.Dir(taskskillclient.DefaultRuntimePath()), "codex-app", "attachments")
}

func importCodexImage(h *TasksHandler, taskDir, eventID string, image codexmessage.Attachment) (located string, err error) {
	if taskDir == "" || !image.IsImage {
		return "", os.ErrNotExist
	}
	cacheName := filepath.Base(image.RelativePath)
	defer func() {
		if err == nil && located != "" {
			if cache, err := os.OpenRoot(codexImageCache()); err == nil {
				_ = cache.Remove(cacheName)
				_ = cache.Close()
			}
		}
	}()
	if existing, err := h.safeTaskFilePath(taskDir, image.RelativePath); err == nil {
		if _, err := codexmessage.ReadImage(existing); err == nil {
			return existing, nil
		}
	}
	cachePath := filepath.Join(codexImageCache(), cacheName)
	data, err := codexmessage.ReadImage(cachePath)
	if errors.Is(err, os.ErrNotExist) {
		data, err = codexmessage.ReadImage(image.Path)
	}
	if err != nil {
		return "", err
	}
	saved, err := saveTaskAttachmentWithID(taskDir, image.Name, codexmessage.ImageID(eventID, image.Index), data)
	if err != nil {
		return "", err
	}
	return h.safeTaskFilePath(taskDir, saved.RelativePath)
}

func (h *TasksHandler) importCodexImages(ctx context.Context, taskDir, eventID, content string) {
	message := codexmessage.Parse(content, eventID)
	if message == nil || taskDir == "" {
		return
	}
	for _, image := range message.Attachments {
		if ctx.Err() != nil {
			return
		}
		_, _ = importCodexImage(h, taskDir, eventID, image)
	}
}

// No client-supplied source path is read: the attachment must belong to this task's stored message.
func (h *TasksHandler) getCodexMessageAttachment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("progress_uuid"))
	index, indexErr := strconv.Atoi(c.Param("index"))
	if err != nil || indexErr != nil || index < 0 || index >= codexmessage.MaxImages {
		i18n.Error(c, http.StatusBadRequest, "localserver_attachment_path_invalid", "attachment_path_invalid")
		return
	}
	taskID := c.Param("uuid")
	var content, mode, tool string
	err = h.taskDB().QueryRowContext(c.Request.Context(), `
	 SELECT user_prompt,execution_mode,cli_type FROM gt_task_progress
	 WHERE task_uuid=? AND uuid=? AND record_type='user_question'
	 UNION ALL
	 SELECT json_extract(p.value,'$.user_prompt'),h.execution_mode,h.execution_tool
	 FROM gt_task_execution_history h,json_each(h.payload_json,'$.progress') p
	 WHERE h.task_uuid=? AND json_extract(p.value,'$.uuid')=? AND json_extract(p.value,'$.record_type')='user_question'
	 LIMIT 1`, taskID, id.String(), taskID, id.String()).Scan(&content, &mode, &tool)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
		return
	}
	message := projectCodexMessage(id.String(), content, mode, tool)
	if message == nil || index >= len(message.Attachments) || !message.Attachments[index].IsImage {
		i18n.Error(c, http.StatusNotFound, "localserver_attachment_not_found", "attachment_not_found")
		return
	}
	taskDir, err := h.taskDirectory(c.Request.Context(), taskID)
	if err != nil {
		i18n.Error(c, http.StatusNotFound, "localserver_attachment_not_found", "attachment_not_found")
		return
	}
	imagePath, err := importCodexImage(h, taskDir, id.String(), message.Attachments[index])
	if err != nil || strings.TrimSpace(imagePath) == "" {
		i18n.Error(c, http.StatusNotFound, "localserver_attachment_not_found", "attachment_not_found")
		return
	}
	serveTaskAttachment(c, imagePath)
}
