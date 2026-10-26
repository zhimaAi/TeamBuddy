package cloud

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// PipelineTaskDocumentPath 是产出文档上报接口（multipart/form-data）。
// 与 pipeline-tasks/sync、pipeline-tasks/sessions 并列，属独立旁路通道。
const PipelineTaskDocumentPath = "/api/client/pipeline-tasks/documents"

// PipelineTaskDocumentUpload 是一次产出文档上报的全部表单字段。
type PipelineTaskDocumentUpload struct {
	LocalTaskUUID   string
	ExecutionIndex  int
	DocKey          string
	RelPath         string
	Title           string
	FileExt         string
	FileSize        int64
	ContentSHA256   string
	ClientUpdatedAt int64
	SourceAbsPath   string
	FileName        string
}

// PipelineTaskDocumentResult 是云端接收结果。
type PipelineTaskDocumentResult struct {
	DocumentID   int64  `json:"document_id"`
	DocKey       string `json:"doc_key"`
	Revision     int64  `json:"revision"`
	StorageState string `json:"storage_state"`
	// Result 取值域固定：accepted / duplicate / replaced / deferred
	Result string `json:"result"`
}

// PushPipelineTaskDocument 把单个产出文件整传上报到云端。
// 返回的响应体不是统一包裹结构时按 2xx 视为已接收（result 留空由调用方兜底判定）。
func (c *Client) PushPipelineTaskDocument(ctx context.Context, upload PipelineTaskDocumentUpload, content []byte) (*PipelineTaskDocumentResult, error) {
	fields := map[string]string{
		"local_task_uuid":   upload.LocalTaskUUID,
		"execution_index":   strconv.Itoa(upload.ExecutionIndex),
		"doc_key":           upload.DocKey,
		"rel_path":          upload.RelPath,
		"title":             upload.Title,
		"file_ext":          upload.FileExt,
		"file_size":         strconv.FormatInt(upload.FileSize, 10),
		"content_sha256":    upload.ContentSHA256,
		"client_updated_at": strconv.FormatInt(upload.ClientUpdatedAt, 10),
	}
	if upload.SourceAbsPath != "" {
		fields["source_abs_path"] = upload.SourceAbsPath
	}
	fileName := upload.FileName
	if fileName == "" {
		fileName = upload.Title
	}

	resp, err := c.doMultipart(ctx, PipelineTaskDocumentPath, fields, "file", fileName, content)
	if err != nil {
		return nil, fmt.Errorf("上传产出文档失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseAPIError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取上传响应失败: %w", err)
	}
	result := &PipelineTaskDocumentResult{}
	if len(body) > 0 {
		if err := decodeAPIResponse(resp.StatusCode, body, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}
