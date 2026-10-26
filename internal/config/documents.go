package config

import (
	"strconv"
	"strings"
	"time"
)

// DocumentUploadConfig 控制产出文档上报（客户端 → 云端）的行为。
// 配置来自 configs/goteams-client/*.ini 的 [document-upload] 段；段缺失或某项为空时
// 使用 DefaultDocumentUploadConfig 的默认值，保证老配置文件也能正常启动。
type DocumentUploadConfig struct {
	Enabled         bool // 总开关；关闭后不扫描、不上报，文件树也不返回上报状态
	IntervalSeconds int  // 后台上报轮询间隔（秒）
	MaxFileMB       int  // 单文件大小上限（MB）
	MaxFilesPerTask int  // 单任务参与上报的文件数上限（超出按 rel_path 排序标 skipped）
	MaxBatchMB      int  // 单任务单轮上报总量上限（MB）
	// ExcludedExtensions 不参与上报的扩展名（小写、不含点）。
	// 采用「黑名单」而非白名单：脚本、源码、配置等文件都可以同步，
	// 只排除可执行文件与压缩包这类线上无法预览、且体积不可控的产物。
	ExcludedExtensions []string
}

// DefaultDocumentUploadConfig 返回产出文档上报的默认配置（默认开启）。
func DefaultDocumentUploadConfig() DocumentUploadConfig {
	return DocumentUploadConfig{
		Enabled:            true,
		IntervalSeconds:    5,
		MaxFileMB:          20,
		MaxFilesPerTask:    300,
		MaxBatchMB:         100,
		ExcludedExtensions: []string{"exe", "zip", "app"},
	}
}

// MaxFileBytes 返回单文件字节上限。
func (c DocumentUploadConfig) MaxFileBytes() int64 { return int64(c.MaxFileMB) * 1024 * 1024 }

// MaxBatchBytes 返回单任务单轮字节总量上限。
func (c DocumentUploadConfig) MaxBatchBytes() int64 { return int64(c.MaxBatchMB) * 1024 * 1024 }

// Interval 返回轮询间隔，非法值回退默认 5 秒。
func (c DocumentUploadConfig) Interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// ExtensionAllowed 判断扩展名是否允许上报：只有黑名单内的类型不上报，
// 其余（含无扩展名的脚本/说明文件）一律允许。
func (c DocumentUploadConfig) ExtensionAllowed(ext string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ext, ".")))
	if normalized == "" {
		return true
	}
	for _, excluded := range c.ExcludedExtensions {
		if strings.ToLower(strings.TrimSpace(strings.TrimPrefix(excluded, "."))) == normalized {
			return false
		}
	}
	return true
}

// WithOverrides 用 ini 中的原始字符串覆盖默认配置，空串表示沿用默认值。
func (c DocumentUploadConfig) WithOverrides(enabled, interval, maxFileMB, maxFiles, maxBatchMB, excludedExtensions string) DocumentUploadConfig {
	if parsed, ok := parseBool(enabled); ok {
		c.Enabled = parsed
	}
	if parsed, ok := parsePositiveInt(interval); ok {
		c.IntervalSeconds = parsed
	}
	if parsed, ok := parsePositiveInt(maxFileMB); ok {
		c.MaxFileMB = parsed
	}
	if parsed, ok := parsePositiveInt(maxFiles); ok {
		c.MaxFilesPerTask = parsed
	}
	if parsed, ok := parsePositiveInt(maxBatchMB); ok {
		c.MaxBatchMB = parsed
	}
	if list := splitExtensions(excludedExtensions); len(list) > 0 {
		c.ExcludedExtensions = list
	}
	return c
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parsePositiveInt(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

func splitExtensions(value string) []string {
	parts := strings.Split(value, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		ext := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), ".")))
		if ext != "" {
			list = append(list, ext)
		}
	}
	return list
}
