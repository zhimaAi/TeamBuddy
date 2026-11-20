// Package codexhook implements the opt-in Codex App native callbacks.
package codexhook

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"goteams-client/internal/codexmessage"
	"goteams-client/internal/codexreply"
	"goteams-client/internal/executor/codex"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskskillclient"
)

const (
	markerPrefix = "TeamBuddy-Codex-App: "
	userMarker   = "TeamBuddy-Codex-Hook: "
	maxBytes     = 2 << 20
	maxPending   = 32
)

type event struct {
	Name      string `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	CWD       string `json:"cwd"`
	Prompt    string `json:"prompt"`
	IsSecret  bool   `json:"isSecret"`
	LocalOnly bool   `json:"localOnly"`
	Answer    string `json:"last_assistant_message"`
}

type launch struct {
	ExplicitPrivacy bool   `json:"explicit_privacy,omitempty"`
	Scope           string `json:"scope,omitempty"`
	ChatDir         string `json:"chat_dir,omitempty"`
	Locale          string `json:"locale,omitempty"`
	TaskUUID        string `json:"task_uuid"`
	Index           int    `json:"execution_index"`
	CWD             string `json:"cwd"`
	ExpiresAt       int64  `json:"expires_at"`
	SessionID       string `json:"session_id,omitempty"`
}

type state struct {
	LaunchID string `json:"launch_id"`
	launch
	Pending     []taskskillclient.Activity `json:"pending"`
	Bound       bool                       `json:"bound,omitempty"`
	Stale       bool                       `json:"stale,omitempty"`
	PrivateTurn string                     `json:"private_turn,omitempty"`
	Gap         bool                       `json:"gap,omitempty"`
}

// Run never starts the desktop service. Errors use exit code 1, not Codex's
// blocking exit code 2; a callback failure must not prevent ordinary work.
func Run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	client := taskskillclient.New(2 * time.Second)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	root := filepath.Join(filepath.Dir(client.RuntimePath()), "codex-app")
	if len(args) > 0 {
		switch args[0] {
		case "user":
			if len(args) != 1 {
				return errors.New("unexpected callback arguments")
			}
			return handleScope(ctx, client, root, input, output, true)
		}
		if args[0] == "inspect" {
			flags := flag.NewFlagSet("codex-hook inspect", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			executable := flags.String("runtime", "", "Codex App bundled runtime")
			cwd := flags.String("work-dir", "", "canonical task workspace")
			source := flags.String("source", "", "user hook configuration file")
			if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*executable) || !filepath.IsAbs(*cwd) {
				return errors.New("invalid Codex inspection arguments")
			}
			result, err := codex.InspectHooks(ctx, *executable, *cwd, *source)
			if err != nil {
				return errors.New("Codex Hook inspection unavailable")
			}
			return json.NewEncoder(output).Encode(result)
		}
		if args[0] == "check" {
			return checkOrPrepare(ctx, client, root, args[1:], output, false)
		}
		if args[0] != "prepare" {
			return errors.New("unknown Codex callback command")
		}
		return checkOrPrepare(ctx, client, root, args[1:], output, true)
	}
	return handle(ctx, client, root, input, output)
}

func checkOrPrepare(ctx context.Context, client *taskskillclient.Client, root string, args []string, output io.Writer, create bool) error {
	flags := flag.NewFlagSet("codex-hook prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	taskID := flags.String("task", "", "task UUID")
	index := flags.Int("execution-index", -1, "original execution generation")
	cwd := flags.String("work-dir", "", "task workspace")
	locale := flags.String("locale", "", "TeamBuddy display language")
	thread := flags.String("thread", "", "existing native thread (check only)")
	scope := flags.String("scope", "", "callback scope")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*scope != "" && *scope != "user") || *index < 0 || !filepath.IsAbs(*cwd) {
		return errors.New("expected --task UUID --execution-index N --work-dir ABSOLUTE_PATH")
	}
	id, err := taskskillclient.NormalizeTaskID(*taskID)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(*cwd)
	if err != nil {
		return errors.New("Codex workspace unavailable")
	}
	item := launch{Scope: *scope, Locale: i18n.Normalize(*locale), TaskUUID: id, Index: *index, CWD: resolved, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
	if *thread != "" {
		tid, err := uuid.Parse(*thread)
		if err != nil || tid == uuid.Nil || create {
			return errors.New("invalid existing Codex thread")
		}
		item.SessionID = tid.String()
		var current state
		if err := read(filepath.Join(root, "session-"+item.SessionID+".json"), &current); err != nil || current.SessionID != item.SessionID || current.TaskUUID != id || current.Index != *index || !sameDirectory(current.CWD, resolved) {
			return errors.New("Codex local session binding unavailable")
		}
		item.Scope = current.Scope
	}
	bound, err := verify(ctx, client, item)
	if err != nil {
		return err
	}
	if item.SessionID != "" && !bound {
		return errors.New("Codex task binding is no longer present")
	}
	if !create {
		return json.NewEncoder(output).Encode(map[string]string{"scope": item.Scope})
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	launchID := uuid.NewString()
	if err := save(filepath.Join(root, "launch-"+launchID+".json"), item); err != nil {
		return err
	}
	prefix := markerPrefix
	if item.Scope == "user" {
		prefix = userMarker
	}
	return json.NewEncoder(output).Encode(map[string]string{"launch_id": launchID, "marker": prefix + launchID})
}

func handle(ctx context.Context, client *taskskillclient.Client, root string, input io.Reader, output io.Writer) error {
	return handleScope(ctx, client, root, input, output, false)
}
func handleScope(ctx context.Context, client *taskskillclient.Client, root string, input io.Reader, output io.Writer, user bool) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxBytes+1))
	if err != nil || len(raw) > maxBytes || !utf8.Valid(raw) {
		return errors.New("invalid Codex hook input")
	}
	var e event
	if json.Unmarshal(raw, &e) != nil {
		return errors.New("invalid Codex hook JSON")
	}
	if e.Name != "SessionStart" && e.Name != "UserPromptSubmit" && e.Name != "Stop" {
		return json.NewEncoder(output).Encode(struct{}{})
	}
	id, err := uuid.Parse(e.SessionID)
	if err != nil || id == uuid.Nil || !filepath.IsAbs(e.CWD) {
		return errors.New("invalid Codex session or workspace")
	}
	e.SessionID = id.String()
	prefix := markerPrefix
	if user {
		prefix = userMarker
	}
	launchID := ""
	if e.Name == "UserPromptSubmit" {
		for _, line := range strings.Split(e.Prompt, "\n") {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			parsed, err := uuid.Parse(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			if err != nil || parsed == uuid.Nil || launchID != "" {
				return errors.New("invalid or duplicate Codex launch marker")
			}
			launchID = parsed.String()
		}
	}
	file := filepath.Join(root, "session-"+e.SessionID+".json")
	if _, err := os.Stat(file); os.IsNotExist(err) && launchID == "" {
		// Unbound chats neither contact TeamBuddy nor create storage.
		return json.NewEncoder(output).Encode(struct{}{})
	}
	if e.Name != "SessionStart" && (strings.TrimSpace(e.TurnID) == "" || len(e.TurnID) > 256) {
		return errors.New("Codex turn_id unavailable")
	}
	if launchID != "" {
		var l launch
		if err := read(filepath.Join(root, "launch-"+launchID+".json"), &l); err != nil || (l.Scope == "user") != user {
			return json.NewEncoder(output).Encode(struct{}{})
		}
	}
	// A legacy project hook and a user hook may both fire. Only the owner writes.
	if launchID == "" {
		var saved state
		if err := read(file, &saved); err != nil {
			return err
		}
		if (saved.Scope == "user") != user {
			return json.NewEncoder(output).Encode(struct{}{})
		}
	}
	release, err := acquireStateLock(ctx, file+".lock", user)
	if err != nil {
		return errors.New("Codex callback storage is busy or unavailable")
	}
	defer release()
	var s state
	if err := read(file, &s); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.LaunchID == "" {
		if launchID == "" {
			return errors.New("invalid Codex binding state")
		}
		item, err := claim(ctx, root, launchID, e.SessionID, e.CWD)
		if err != nil {
			return err
		}
		s = state{LaunchID: launchID, launch: item}
	} else if launchID != "" && launchID != s.LaunchID {
		return errors.New("Codex session already belongs to another launch")
	}
	chatDir := s.ChatDir
	if chatDir == "" {
		chatDir = s.CWD
	}
	if s.SessionID != e.SessionID || (s.Scope == "user") != user || !sameDirectory(chatDir, e.CWD) {
		return errors.New("Codex workspace or session differs from its binding")
	}
	if s.Stale {
		return json.NewEncoder(output).Encode(map[string]string{"systemMessage": i18n.WithLocale(s.Locale, "codex_hook_unavailable")})
	}
	if _, err := taskskillclient.NormalizeTaskID(s.TaskUUID); err != nil || s.Index < 0 {
		return errors.New("invalid Codex binding state")
	}
	content, kind := "", ""
	switch e.Name {
	case "UserPromptSubmit":
		content, kind = e.Prompt, "question"
		if launchID != "" {
			content = strings.ReplaceAll(content, prefix+launchID, "")
		}
	case "Stop":
		content, kind = e.Answer, "result"
	}
	content = strings.TrimSpace(content)
	if content != "" || kind == "question" && (e.IsSecret || e.LocalOnly) {
		// Native turn IDs and original text make retries stable, including a lost
		// HTTP acknowledgement. The API already enforces event_id idempotency.
		key, _ := json.Marshal([]any{s.TaskUUID, s.Index, e.SessionID, e.TurnID, kind, content})

		if kind == "question" {
			r := codexreply.Parse(content, s.ExplicitPrivacy)
			if e.LocalOnly || e.IsSecret {
				r = &codexreply.Reply{Items: []codexreply.Item{{Hidden: true, LocalOnly: e.LocalOnly}}}
			}
			if r != nil {
				content = r.Stored()
				// A final reply in this same Codex turn can repeat a marked field.
				if r.Private() && (!s.ExplicitPrivacy || !r.Unavailable) {
					s.PrivateTurn = e.TurnID
				}
			}
		} else if s.PrivateTurn == e.TurnID || e.IsSecret || e.LocalOnly {
			content = codexreply.HiddenResult
		}
		item := taskskillclient.Activity{ExecutionIndex: s.Index, EventID: uuid.NewSHA1(uuid.NameSpaceOID, key).String(), Type: kind, Content: content, Tool: "codex", ThreadID: e.SessionID, Source: "hooks"}
		duplicate := false
		for _, pending := range s.Pending {
			if pending.EventID == item.EventID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			next := append(append([]taskskillclient.Activity(nil), s.Pending...), item)
			candidate := s
			candidate.Pending = next
			data, _ := json.Marshal(candidate)
			if len(next) > maxPending || len(data) > maxBytes-1024 || utf8.RuneCountInString(content) > taskskillclient.MaxActivityContentRunes {
				s.Gap = true
			} else {
				if kind == "question" {
					codexmessage.Capture(ctx, root, item.EventID, content)
				}
				s.Pending = next
			}
		}
	}
	if user && !s.Stale && (!s.Bound || len(s.Pending) > 0) {
		if err := markPending(root, e.SessionID); err != nil {
			return err
		}
	}
	if err := save(file, s); err != nil {
		return err
	}
	var deliveryErr error
	if !user {
		deliveryErr = deliver(ctx, client, file, &s)
	}
	status := "synchronized"
	if len(s.Pending) > 0 || user && !s.Bound {
		status = "pending"
	}
	if deliveryErr != nil || s.Stale {
		status = "unavailable"
	}
	if s.Gap {
		status = "incomplete"
	}
	response := map[string]any{}
	if status != "synchronized" && !(user && status == "pending") {
		response["systemMessage"] = i18n.WithLocale(s.Locale, "codex_hook_"+status)
	}
	if e.Name != "Stop" {
		response["hookSpecificOutput"] = map[string]string{"hookEventName": e.Name, "additionalContext": fmt.Sprintf("TeamBuddy task_uuid=%s; execution_index=%d; tool=codex; thread_id=%s; callback_status=%s. Native Hooks exclusively report user messages and final replies; do not report activities through Skill/MCP. A Stop event does not complete the task. Continue the user's work if synchronization is unavailable; do not claim it succeeded.", s.TaskUUID, s.Index, e.SessionID, status)}
	}
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return err
	}
	// Valid JSON with exit 0 preserves additionalContext on transport failures.
	// Local loss is also visible via the durable gap marker and systemMessage.
	return nil
}

func verify(ctx context.Context, client *taskskillclient.Client, item launch) (bool, error) {
	task, err := client.ReadBindingContext(ctx, item.TaskUUID, "")
	if err != nil {
		if taskskillclient.HasCode(err, "conflict") {
			return false, errStale
		}
		return false, err
	}
	if task.Mode != "vibe_coding" || task.Tool != "codex" || task.Index != item.Index {
		return false, errStale
	}
	if task.BoundThread != "" && (task.BoundThread != item.SessionID || task.ReportingOwner != "hooks") {
		return false, errStale
	}
	for _, dir := range task.Dirs {
		if sameDirectory(dir, item.CWD) {
			return task.BoundThread != "", nil
		}
	}
	return false, errStale
}

func deliver(ctx context.Context, client *taskskillclient.Client, file string, s *state) error {
	bound, err := verify(ctx, client, s.launch)
	if err != nil {
		return err
	}
	if !bound {
		if err := client.BindSession(ctx, s.TaskUUID, "", taskskillclient.SessionBinding{ExecutionIndex: s.Index, Type: "codex", ThreadID: s.SessionID, ReportingOwner: "hooks"}); err != nil {
			return err
		}
	}
	for sent := 0; sent < 8 && len(s.Pending) > 0; sent++ {
		item := s.Pending[0]
		item.Source = "hooks"
		if err := client.ReportActivity(ctx, s.TaskUUID, "", item); err != nil {
			return err
		}
		s.Pending = s.Pending[1:]
		if err := save(file, *s); err != nil {
			return err
		}
	}
	return nil
}

func sameDirectory(a, b string) bool {
	left, e1 := os.Stat(a)
	right, e2 := os.Stat(b)
	return e1 == nil && e2 == nil && left.IsDir() && right.IsDir() && os.SameFile(left, right)
}
