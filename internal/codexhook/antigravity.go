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

	"goteams-client/internal/executor"
	"goteams-client/internal/taskskillclient"
)

const agyTool = "antigravity"
const agyLaunchEnv = "TEAMBUDDY_AGY_LAUNCH"

type agyEvent struct {
	Name           string   `json:"hookEventName"`
	SessionID      string   `json:"conversationId"`
	CWD            string   `json:"cwd"`
	WorkspacePaths []string `json:"workspacePaths"`
	Path           string   `json:"transcriptPath"`
	Prompt         string   `json:"lastUserInput"`
	Invoke         *int     `json:"invocationNum"`
	Idle           bool     `json:"fullyIdle"`
	Continues      bool     `json:"continuationActive"`
	Reason         string   `json:"terminationReason"`
	Error          string   `json:"error"`
}

func (e agyEvent) successfulStop() bool {
	return e.Name == "Stop" && e.Idle && !e.Continues && e.Error == "" && (e.Reason == "NO_TOOL_CALL" || e.Reason == "model_stop")
}

type agyLaunch struct {
	launch
	Instruction string `json:"instruction,omitempty"`
	// Resume tickets are preassigned but must still be used before expiry.
	Used bool `json:"used,omitempty"`
}

type agyProgress struct {
	PromptID  string `json:"prompt_id,omitempty"`
	AnswerID  string `json:"answer_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
}

// RunAntigravity accepts only launches prepared for TeamBuddy, not arbitrary
// CLI/App events. It shares durable delivery with Codex, not its event schema.
func RunAntigravity(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	root := callbackRootForTool(agyTool)
	if len(args) > 0 && args[0] == "app" {
		return runAntigravityApp(ctx, args[1:], input, output)
	}
	if len(args) > 0 && args[0] == "prepare" {
		return prepareAgy(ctx, root, args[1:], input, output)
	}
	if len(args) != 0 {
		return errors.New("unexpected callback arguments")
	}
	return handleAgy(ctx, root, os.Getenv(agyLaunchEnv), input, output)
}

func prepareAgy(ctx context.Context, root string, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("antigravity-hook prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	task := flags.String("task", "", "task UUID")
	index := flags.Int("execution-index", -1, "execution generation")
	cwd := flags.String("work-dir", "", "task workspace")
	thread := flags.String("thread", "", "existing Antigravity conversation")
	check := flags.Bool("check", false, "validate without preparing a ticket")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *index < 0 || !filepath.IsAbs(*cwd) || *thread != "" && !agyUUID(*thread) {
		return errors.New("invalid launch arguments")
	}
	id, err := taskskillclient.NormalizeTaskID(*task)
	if err != nil {
		return err
	}
	execPath, err := executor.ResolveVibeCodingExecutable(agyTool)
	if err != nil || !filepath.IsAbs(execPath) {
		return errors.New("Antigravity CLI unavailable")
	}
	instruction, err := io.ReadAll(io.LimitReader(input, 16*1024+1))
	if err != nil || len(instruction) == 0 || len(instruction) > 16*1024 || !utf8.Valid(instruction) {
		return errors.New("invalid launch context")
	}
	item := agyLaunch{launch: launch{Tool: agyTool, Scope: "user", TaskUUID: id, Index: *index, CWD: *cwd, ExpiresAt: time.Now().Add(30 * time.Minute).Unix(), SessionID: *thread}, Instruction: string(instruction)}
	bound, err := verify(ctx, taskskillclient.New(2*time.Second), item.launch)
	if err != nil || bound != (*thread != "") {
		return errors.New("Antigravity task binding no longer matches")
	}
	if bound {
		if claimed, _ := ClaimedSession(id, *thread, agyTool, *index); !claimed {
			return errors.New("Antigravity session was not bound on this device")
		}
	}
	if *check {
		return json.NewEncoder(output).Encode(map[string]string{"runtime": execPath})
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	ticket := uuid.NewString()
	if err := save(filepath.Join(root, "launch-"+ticket+".json"), item); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]string{"ticket": ticket, "runtime": execPath})
}

func handleAgy(ctx context.Context, root, ticket string, input io.Reader, output io.Writer) error {
	encode := json.NewEncoder(output)
	response := map[string]any{}
	if !agyUUID(ticket) {
		_, _ = io.Copy(io.Discard, io.LimitReader(input, maxBytes))
		return encode.Encode(response)
	}
	var e agyEvent
	data, err := io.ReadAll(io.LimitReader(input, maxBytes+1))
	if err != nil || len(data) > maxBytes || json.Unmarshal(data, &e) != nil || !agyUUID(e.SessionID) || !filepath.IsAbs(e.CWD) {
		return encode.Encode(response)
	}
	success := e.successfulStop()
	if e.Name != "SessionStart" && (e.Name != "PreInvocation" || e.Invoke == nil || *e.Invoke != 0) && !success {
		return encode.Encode(response)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rel := agyTranscriptRelative(home, "antigravity-cli", e.SessionID, e.Path)
	if rel == "" { // Reject App, IDE, unrelated sessions and caller-selected files.
		return encode.Encode(response)
	}
	launchPath := filepath.Join(root, "launch-"+ticket+".json")
	if info, err := os.Lstat(launchPath); err != nil || !info.Mode().IsRegular() {
		return encode.Encode(response)
	}
	unlock, err := acquireStateLock(ctx, launchPath+".lock", true)
	if err != nil {
		return err
	}
	defer unlock()
	var item agyLaunch
	if read(launchPath, &item) != nil || item.Tool != agyTool || item.Scope != "user" || !sameDirectory(item.CWD, e.CWD) ||
		(!item.Used && (e.Name == "Stop" || time.Now().Unix() > item.ExpiresAt)) || (item.SessionID != "" && item.SessionID != e.SessionID) {
		return encode.Encode(response)
	}
	file := filepath.Join(root, "session-"+e.SessionID+".json")
	release, err := acquireStateLock(ctx, file+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	var s state
	if err := read(file, &s); err != nil {
		if !os.IsNotExist(err) || e.Name != "SessionStart" || item.SessionID != "" && !item.Used {
			return encode.Encode(response)
		}
		s = state{LaunchID: ticket, launch: item.launch, Agy: &agyProgress{}}
		s.SessionID = e.SessionID
	} else if s.Agy == nil || s.Stale || s.Tool != agyTool || s.TaskUUID != item.TaskUUID || s.Index != item.Index || s.SessionID != e.SessionID ||
		item.SessionID != e.SessionID || item.Used && s.Agy.ContextID != ticket || !sameDirectory(s.CWD, item.CWD) {
		return encode.Encode(response)
	}
	if !item.Used {
		item.Used, item.SessionID = true, e.SessionID
		if err := save(launchPath, item); err != nil {
			return err
		}
	}
	if e.Name != "Stop" && s.Agy.ContextID != ticket {
		s.Agy.ContextID = ticket
		text := fmt.Sprintf("%s\n\nthread_id: %s", item.Instruction, e.SessionID)
		response["injectSteps"] = []any{map[string]any{"systemMessage": map[string]string{"systemMessage": text}}}
	}
	if e.Name != "SessionStart" {
		turn, answer, err := readAgyTurn(ctx, home, rel, strings.TrimSpace(e.Prompt), false)
		if err != nil {
			s.Gap = true
		} else if turn != "" {
			switch {
			case e.Name == "PreInvocation" && s.Agy.PromptID != turn:
				if queueAgy(&s, turn, "question", strings.TrimSpace(e.Prompt)) {
					s.Agy.PromptID = turn
				}
			case success && s.Agy.PromptID == turn && s.Agy.AnswerID != turn && answer != "":
				if queueAgy(&s, turn, "result", answer) {
					s.Agy.AnswerID = turn
				}
			}
		}
	}
	if (!s.Bound || len(s.Pending) > 0) && markPending(root, e.SessionID) != nil {
		return errors.New("unable to mark Antigravity delivery pending")
	}
	if err := save(file, s); err != nil {
		return err
	}
	return encode.Encode(response)
}

func agyUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

func queueAgy(s *state, turn, kind, text string) bool {
	key, _ := json.Marshal([]any{s.TaskUUID, s.Index, s.SessionID, turn, kind})
	item := taskskillclient.Activity{ExecutionIndex: s.Index, EventID: uuid.NewSHA1(uuid.NameSpaceOID, key).String(), Type: kind, Content: text, Tool: s.tool(), ThreadID: s.SessionID, Source: "hooks"}
	pending := append(s.Pending, item)
	data, err := json.Marshal(pending)
	if err != nil || len(pending) > maxPending || len(data) > maxBytes-4096 || utf8.RuneCountInString(text) > taskskillclient.MaxActivityContentRunes {
		s.Gap = true
		return false
	}
	s.Pending = pending
	return true
}
