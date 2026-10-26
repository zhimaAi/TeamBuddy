package taskskillclient

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// 接口文本上限独立于各入口自己的输入、队列及响应字节预算。
const (
	MaxActivityContentRunes = 64 * 1024
	MaxNativeThreadRunes    = 256
)

// NormalizeTaskID preserves the task-skill protocol's canonical UUID form.
func NormalizeTaskID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("task_uuid must be a UUID")
	}
	return id.String(), nil
}
