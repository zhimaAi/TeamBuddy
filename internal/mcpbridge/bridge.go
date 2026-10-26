// Package mcpbridge exposes the existing local task-skill API as four MCP tools.
// The process speaks JSON-RPC on stdout and does not start the desktop client.
package mcpbridge

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"goteams-client/internal/taskskillclient"
)

const (
	maxContentRunes = taskskillclient.MaxActivityContentRunes
	maxThreadRunes  = taskskillclient.MaxNativeThreadRunes
	requestTimeout  = 15 * time.Second
)

// Bridge forwards MCP tool calls to the local task-skill HTTP API.
type Bridge struct {
	version string
	client  *taskskillclient.Client
}

// New returns a bridge that reads the stable runtime.json used by the skill scripts.
func New(version string) *Bridge {
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &Bridge{
		version: version,
		client:  taskskillclient.New(requestTimeout),
	}
}

// Run serves MCP on stdio until the client disconnects or the process is signalled.
// Diagnostics go to stderr. Stdout is reserved for JSON-RPC.
func Run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
			return
		}
		<-sig
		os.Exit(1)
	}()
	bridge := New(version)
	bridge.client.Process = ctx
	return bridge.serve(ctx, &mcp.StdioTransport{})
}

func (b *Bridge) serve(ctx context.Context, transport mcp.Transport) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "teambuddy", Version: b.version}, nil)
	// Input schemas are still inferred from the handler argument structs, but
	// toolSchema rewrites them for portability; see splitMultiType.
	mcp.AddTool(server, toolSchema[getInput](&mcp.Tool{
		Name:        "teambuddy_get_task",
		Description: "Read one local TeamBuddy Vibe Coding task and its history.",
	}), b.getTask)
	mcp.AddTool(server, toolSchema[bindInput](&mcp.Tool{
		Name:        "teambuddy_bind_session",
		Description: "Bind the current native session to one local TeamBuddy Vibe Coding task.",
	}), b.bindSession)
	mcp.AddTool(server, toolSchema[statusInput](&mcp.Tool{
		Name:        "teambuddy_update_status",
		Description: "Update the status of one local TeamBuddy Vibe Coding task.",
	}), b.updateStatus)
	mcp.AddTool(server, toolSchema[activityInput](&mcp.Tool{
		Name:        "teambuddy_report_activity",
		Description: "Report one user message or final reply to a local TeamBuddy Vibe Coding task.",
	}), b.reportActivity)
	return server.Run(ctx, transport)
}

type getInput struct {
	TaskUUID string `json:"task_uuid" jsonschema:"TeamBuddy task UUID"`
	Language string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type bindInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamBuddy task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	Type           string `json:"type" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"current native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type statusInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamBuddy task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	Status         string `json:"status" jsonschema:"pending, in_progress, blocked, or done"`
	Tool           string `json:"tool" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"bound native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type activityInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamBuddy task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	EventID        string `json:"event_id" jsonschema:"UUID for this activity; reuse it when retrying"`
	Type           string `json:"type" jsonschema:"question or result"`
	Content        string `json:"content" jsonschema:"activity text"`
	Tool           string `json:"tool" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"bound native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

func (b *Bridge) getTask(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
	id, err := taskskillclient.NormalizeTaskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil {
		return toolError("invalid_argument", err.Error())
	}
	return b.forward(ctx, http.MethodGet, id, "", in.Language, nil)
}

func (b *Bridge) bindSession(ctx context.Context, _ *mcp.CallToolRequest, in bindInput) (*mcp.CallToolResult, any, error) {
	id, err := taskskillclient.NormalizeTaskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Type == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "type and thread_id are required")
	}
	body := map[string]any{"execution_index": *in.ExecutionIndex, "type": in.Type, "thread_id": in.ThreadID}
	return b.forward(ctx, http.MethodPut, id, "/vibe-session", in.Language, body)
}

func (b *Bridge) updateStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, any, error) {
	id, err := taskskillclient.NormalizeTaskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	in.Status = strings.TrimSpace(in.Status)
	switch in.Status {
	case "pending", "in_progress", "blocked", "done":
	default:
		return toolError("invalid_argument", "status is invalid")
	}
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Tool == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "tool and thread_id are required")
	}
	body := map[string]any{
		"execution_index": *in.ExecutionIndex, "status": in.Status, "tool": in.Tool, "thread_id": in.ThreadID,
	}
	return b.forward(ctx, http.MethodPut, id, "/status", in.Language, body)
}

func (b *Bridge) reportActivity(ctx context.Context, _ *mcp.CallToolRequest, in activityInput) (*mcp.CallToolResult, any, error) {
	id, err := taskskillclient.NormalizeTaskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	if _, err = uuid.Parse(strings.TrimSpace(in.EventID)); err != nil {
		return toolError("invalid_argument", "event_id must be a UUID")
	}
	in.Type = strings.TrimSpace(in.Type)
	if in.Type != "question" && in.Type != "result" {
		return toolError("invalid_argument", "type must be question or result")
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" || utf8.RuneCountInString(in.Content) > maxContentRunes {
		return toolError("invalid_argument", "content is empty or too long")
	}
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Tool == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "tool and thread_id are required")
	}
	body := map[string]any{
		"execution_index": *in.ExecutionIndex, "event_id": strings.TrimSpace(in.EventID),
		"type": in.Type, "content": in.Content, "tool": in.Tool, "thread_id": in.ThreadID,
	}
	return b.forward(ctx, http.MethodPost, id, "/activities", in.Language, body)
}

func (b *Bridge) forward(ctx context.Context, method, taskID, suffix, language string, body any) (*mcp.CallToolResult, any, error) {
	data, err := b.client.Do(ctx, method, taskID, suffix, language, body)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}

func toolError(code, detail string) (*mcp.CallToolResult, any, error) {
	return nil, nil, &taskskillclient.RequestError{Code: code, Detail: detail}
}

func languageOK(value string) error {
	switch strings.TrimSpace(value) {
	case "", "zh-CN", "en-US":
		return nil
	default:
		return fmt.Errorf("language must be zh-CN or en-US")
	}
}
