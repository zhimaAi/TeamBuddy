package localserver

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/i18n"
)

// 图片读取只允许当前任务及其有效会话目录，不借用其他任务的工作区。
func (h *TasksHandler) getTaskOutputImage(c *gin.Context) {
	roots, preferred, err := h.loadTaskOutputRoots(c.Request.Context(), c.Param("uuid"), c.Query("session_uuid"))
	if err != nil {
		writeOutputFileError(c, err)
		return
	}
	located, err := locateOutputFile(roots, preferred, c.Query("path"), true)
	if err != nil {
		writeOutputFileError(c, err)
		return
	}
	for _, root := range prepareOutputRoots(roots) {
		if !pathWithinDirectory(root.real, located) {
			continue
		}
		relative, err := filepath.Rel(root.real, located)
		if err != nil {
			h.writeWorkspaceError(c, errWorkspacePathOutside)
			return
		}
		file, info, _, err := openVerifiedWorkspacePath(root.original, relative, false)
		if err != nil {
			h.writeWorkspaceError(c, err)
			return
		}
		defer file.Close()
		if info.Size() > workspaceImageLimit {
			h.writeWorkspaceError(c, errWorkspaceFileTooLarge)
			return
		}
		data, err := readWorkspaceFile(file, workspaceImageLimit)
		if err != nil {
			h.writeWorkspaceError(c, err)
			return
		}
		contentType := http.DetectContentType(data)
		expected := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp"}
		if expected[strings.ToLower(filepath.Ext(located))] != contentType {
			i18n.Error(c, http.StatusUnsupportedMediaType, "localserver_file_path_invalid", "file_path_invalid")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, contentType, data)
		return
	}
	writeOutputFileError(c, errOutputFileOutside)
}
