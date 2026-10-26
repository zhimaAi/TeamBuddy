// Package taskskillclient implements the loopback task-skill transport shared by
// native Hooks and the MCP adapter. It has no dependency on MCP response types.
package taskskillclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goteams-client/internal/workspace"
)

const (
	HeaderName       = "X-GoTeams-Skill-Client"
	HeaderValue      = "teambuddy-v1"
	MaxResponseBytes = 2 << 20
	connectTimeout   = 5 * time.Second
)

var (
	ErrRuntimeAddress   = errors.New("runtime address unavailable")
	ErrNotLoopback      = errors.New("not loopback")
	errRequestNotSent   = errors.New("request not sent")
	errResponseUnread   = errors.New("response unread")
	errResponseTooLarge = errors.New("response too large")
)

type RequestError struct {
	Code   string
	Detail string
}

func (e *RequestError) Error() string        { return e.Code + ": " + e.Detail }
func requestError(code, detail string) error { return &RequestError{Code: code, Detail: detail} }
func HasCode(err error, codes ...string) bool {
	var target *RequestError
	if !errors.As(err, &target) {
		return false
	}
	for _, code := range codes {
		if target.Code == code {
			return true
		}
	}
	return false
}

type Client struct {
	RuntimePath func() string
	HTTPClient  *http.Client
	Process     context.Context
	// ResponseLimit overrides the MCP response cap. Zero uses MaxResponseBytes.
	// Negative reads the body the server already returned, without that cap.
	ResponseLimit int
}

// New uses the same stable loopback address for every task-skill adapter.
func New(timeout time.Duration) *Client {
	return &Client{RuntimePath: DefaultRuntimePath, HTTPClient: NewHTTPClient(timeout)}
}

func DefaultRuntimePath() string {
	return filepath.Join(workspace.DefaultDataRoot(), "runtime", "runtime.json")
}

type TaskSnapshot struct {
	Mode  string   `json:"execution_mode"`
	Tool  string   `json:"execution_tool"`
	Index int      `json:"execution_index"`
	Dirs  []string `json:"work_dirs"`
}

type SessionBinding struct {
	ExecutionIndex int    `json:"execution_index"`
	Type           string `json:"type"`
	ThreadID       string `json:"thread_id"`
}

type Activity struct {
	ExecutionIndex int    `json:"execution_index"`
	EventID        string `json:"event_id"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Tool           string `json:"tool"`
	ThreadID       string `json:"thread_id"`
}

func (c *Client) ReadTask(ctx context.Context, id, language string) (TaskSnapshot, error) {
	data, err := c.Do(ctx, http.MethodGet, id, "", language, nil)
	if err != nil {
		return TaskSnapshot{}, err
	}
	var response struct {
		Task TaskSnapshot `json:"task"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return TaskSnapshot{}, errors.New("invalid TeamBuddy task response")
	}
	return response.Task, nil
}

func (c *Client) BindSession(ctx context.Context, id, language string, binding SessionBinding) error {
	_, err := c.Do(ctx, http.MethodPut, id, "/vibe-session", language, binding)
	return err
}

func (c *Client) ReportActivity(ctx context.Context, id, language string, activity Activity) error {
	_, err := c.Do(ctx, http.MethodPost, id, "/activities", language, activity)
	return err
}

func NewHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: connectTimeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: dialer.DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func requestContext(handler, process context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(handler)
	if process == nil {
		return ctx, cancel
	}
	if process.Err() != nil {
		cancel()
		return ctx, cancel
	}
	stop := context.AfterFunc(process, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
func (c *Client) Do(ctx context.Context, method, taskID, suffix, language string, body any) ([]byte, error) {
	var raw []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, requestError("invalid_argument", "request body is invalid")
		}
		raw = encoded
	}
	status, data, err := c.Exchange(ctx, method, taskID, suffix, language, raw)
	if err != nil {
		switch {
		case errors.Is(err, ErrRuntimeAddress), errors.Is(err, ErrNotLoopback):
			fmt.Fprintf(os.Stderr, "teambuddy local: runtime address unavailable\n")
			return nil, requestError("teambuddy_unavailable", "local TeamBuddy address is unavailable")
		case errors.Is(err, errRequestNotSent):
			return nil, requestError("teambuddy_unavailable", "local request was not sent")
		case errors.Is(err, errResponseUnread):
			return nil, requestError("teambuddy_unavailable", "local response was not read")
		case errors.Is(err, errResponseTooLarge):
			return nil, requestError("teambuddy_unavailable", "local response is too large")
		default:
			fmt.Fprintf(os.Stderr, "teambuddy local: local request failed\n")
			return nil, requestError("teambuddy_unavailable", "local TeamBuddy did not respond")
		}
	}
	if status >= 200 && status < 300 {
		return data, nil
	}
	code := statusCode(status, suffix)
	fmt.Fprintf(os.Stderr, "teambuddy local: %s status %d\n", code, status)
	return nil, requestError(code, "local TeamBuddy rejected the request")
}

// Exchange sends one task-skill request and returns the status and body.
// HTTP error statuses are returned, not turned into RequestError.
// A nil body omits the JSON content type. Callers keep the original bytes.
func (c *Client) Exchange(ctx context.Context, method, taskID, suffix, language string, body []byte) (int, []byte, error) {
	if c == nil || c.RuntimePath == nil {
		return 0, nil, ErrRuntimeAddress
	}
	address, err := readAddress(c.RuntimePath())
	if err != nil {
		return 0, nil, err
	}
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	reqCtx, stop := requestContext(ctx, c.Process)
	defer stop()
	url := "http://" + address + "/api/local/task-skill/tasks/" + taskID + suffix
	req, err := http.NewRequestWithContext(reqCtx, method, url, payload)
	if err != nil {
		return 0, nil, errRequestNotSent
	}
	req.Header.Set(HeaderName, HeaderValue)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if language != "" {
		req.Header.Set("X-GoTeams-Lang", language)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	limit := MaxResponseBytes
	if c.ResponseLimit > 0 {
		limit = c.ResponseLimit
	} else if c.ResponseLimit < 0 {
		limit = -1
	}
	var reader io.Reader = resp.Body
	if limit >= 0 {
		reader = io.LimitReader(resp.Body, int64(limit)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return 0, nil, errResponseUnread
	}
	if limit >= 0 && len(data) > limit {
		return 0, nil, errResponseTooLarge
	}
	return resp.StatusCode, data, nil
}

func statusCode(status int, suffix string) string {
	switch status {
	case http.StatusBadRequest:
		if suffix == "/vibe-session" {
			return "bind_rejected"
		}
		return "invalid_argument"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "task_not_found"
	case http.StatusConflict:
		return "conflict"
	default:
		return "teambuddy_unavailable"
	}
}

func readAddress(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ErrRuntimeAddress
	}
	var info struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", ErrRuntimeAddress
	}
	address := strings.TrimSpace(info.Address)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", ErrNotLoopback
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", ErrNotLoopback
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", ErrNotLoopback
	}
	return address, nil
}
