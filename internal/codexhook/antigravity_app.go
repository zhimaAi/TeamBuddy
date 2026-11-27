package codexhook

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"goteams-client/internal/executor"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/taskskillclient"
)

const agyAppTool = taskruntime.ExecutionToolAntigravityApp
const agyAppMarker = "TeamBuddy-Antigravity-App-Hook: "

func runAntigravityApp(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	root := callbackRootForTool(agyAppTool)
	client := taskskillclient.New(2 * time.Second)
	if len(args) == 0 {
		return handleAgyApp(ctx, client, root, input, output)
	}
	if args[0] != "prepare" {
		return errors.New("unexpected App callback arguments")
	}
	flags := flag.NewFlagSet("antigravity-hook app prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	task := flags.String("task", "", "task UUID")
	index := flags.Int("execution-index", -1, "execution generation")
	cwd := flags.String("work-dir", "", "task workspace")
	thread := flags.String("thread", "", "existing App conversation")
	check := flags.Bool("check", false, "validate without preparing a ticket")
	projectCLI := flags.Bool("project-cli", false, "resolve CLI for registering an unbound App project")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *index < 0 || !filepath.IsAbs(*cwd) ||
		*thread != "" && !agyUUID(*thread) || *projectCLI && (!*check || *thread != "") {
		return errors.New("invalid App launch arguments")
	}
	id, err := taskskillclient.NormalizeTaskID(*task)
	if err != nil {
		return err
	}
	item := agyLaunch{launch: launch{Tool: agyAppTool, Scope: "user", TaskUUID: id, Index: *index, CWD: *cwd, ExpiresAt: time.Now().Add(30 * time.Minute).Unix(), SessionID: *thread}}
	bound, err := verify(ctx, client, item.launch)
	if err != nil || bound != (*thread != "") {
		return errors.New("Antigravity App task binding no longer matches")
	}
	result := map[string]string{}
	if *projectCLI {
		if runtime, err := executor.ResolveVibeCodingExecutable(agyTool); err == nil && filepath.IsAbs(runtime) {
			result["runtime"] = runtime
		}
	}
	if bound || *check {
		return json.NewEncoder(output).Encode(result)
	}
	instruction, err := io.ReadAll(io.LimitReader(input, 16*1024+1))
	if err != nil || len(instruction) == 0 || len(instruction) > 16*1024 || !utf8.Valid(instruction) {
		return errors.New("invalid App launch context")
	}
	item.Instruction = string(instruction)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	id = uuid.NewString()
	if err := save(filepath.Join(root, "launch-"+id+".json"), item); err != nil {
		return err
	}
	result["marker"] = agyAppMarker + id
	return json.NewEncoder(output).Encode(result)
}

func handleAgyApp(ctx context.Context, client *taskskillclient.Client, root string, input io.Reader, output io.Writer) error {
	response := map[string]any{}
	finish := func() error { return json.NewEncoder(output).Encode(response) }
	var e agyEvent
	data, err := io.ReadAll(io.LimitReader(input, maxBytes+1))
	if err != nil || len(data) > maxBytes || !utf8.Valid(data) || json.Unmarshal(data, &e) != nil || !agyUUID(e.SessionID) || !filepath.IsAbs(e.CWD) {
		return finish()
	}
	question := e.Name == "PreInvocation"
	result := e.successfulStop()
	if !question && !result {
		return finish()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rel := agyTranscriptRelative(home, "antigravity", e.SessionID, e.Path)
	if rel == "" { // Global configuration also fires in CLI/IDE; never open their transcripts.
		return finish()
	}
	prompt := strings.TrimSpace(e.Prompt)
	if prompt == "" {
		return finish()
	}
	body, ticket, count := appPrompt(prompt)
	file := filepath.Join(root, "session-"+e.SessionID+".json")
	var claimPath string
	var claim agyLaunch
	if info, err := os.Lstat(file); os.IsNotExist(err) {
		if !question || count != 1 || !agyUUID(ticket) || body == "" || !appWorkspace(e) {
			return finish() // Ordinary chats never read transcripts, take locks or contact TeamBuddy.
		}
		claimPath = filepath.Join(root, "launch-"+ticket+".json")
		if info, err := os.Lstat(claimPath); err != nil || !info.Mode().IsRegular() {
			return finish()
		}
		eligible := func() bool {
			return read(claimPath, &claim) == nil && claim.Tool == agyAppTool && claim.Scope == "user" &&
				time.Now().Unix() <= claim.ExpiresAt && (claim.SessionID == "" || claim.SessionID == e.SessionID) &&
				sameDirectory(claim.CWD, e.CWD)
		}
		if !eligible() {
			return finish()
		}
		checked := claim.launch
		checked.SessionID = e.SessionID
		if _, err := verify(ctx, client, checked); err != nil {
			return finish()
		}
		unlock, err := acquireStateLock(ctx, claimPath+".lock", true)
		if err != nil {
			return err
		}
		defer unlock()
		if !eligible() {
			return finish()
		}
		claim.SessionID = e.SessionID
		if claim.launch != checked {
			return finish()
		}
	} else if err != nil || !info.Mode().IsRegular() {
		return finish()
	}
	release, err := acquireStateLock(ctx, file+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	var s state
	err = read(file, &s)
	newClaim := os.IsNotExist(err) && claimPath != ""
	if newClaim {
		s = state{LaunchID: ticket, launch: claim.launch, Agy: &agyProgress{}}
	} else if err != nil || s.Agy == nil || s.Scope != "user" || s.Stale || s.Tool != agyAppTool || s.SessionID != e.SessionID || !sameDirectory(s.CWD, e.CWD) || !appWorkspace(e) {
		return finish()
	}
	if !newClaim && count != 0 && (count != 1 || ticket != s.LaunchID) {
		return finish() // Do not report another task's launch message into this one.
	}
	turn, answer, err := readAgyTurn(ctx, home, rel, prompt, newClaim)
	if newClaim {
		if err != nil || turn == "" || !queueAgy(&s, turn, "question", body) {
			return finish()
		}
		s.Agy.PromptID = turn
		if err := save(claimPath, claim); err != nil {
			return err
		}
		response["injectSteps"] = []any{map[string]any{"systemMessage": map[string]string{"systemMessage": claim.Instruction + "\n\nthread_id: " + e.SessionID}}}
	} else if err != nil {
		s.Gap = true
	} else if turn == "" {
		return finish()
	} else if question && body != "" && s.Agy.PromptID != turn {
		if queueAgy(&s, turn, "question", body) {
			s.Agy.PromptID = turn
		}
	} else if result && s.Agy.PromptID == turn && s.Agy.AnswerID != turn {
		answer = strings.TrimSpace(strings.ReplaceAll(answer, agyAppMarker+s.LaunchID, ""))
		if answer != "" && queueAgy(&s, turn, "result", answer) {
			s.Agy.AnswerID = turn
		}
	} else {
		return finish()
	}
	if (!s.Bound || len(s.Pending) > 0) && markPending(root, e.SessionID) != nil {
		return errors.New("unable to mark Antigravity App delivery pending")
	}
	if err := save(file, s); err != nil {
		return err
	}
	return finish()
}

// Local mode must run inside the task checkout, not a sibling worktree.
func appWorkspace(e agyEvent) bool {
	for _, dir := range e.WorkspacePaths {
		if filepath.IsAbs(dir) && sameDirectory(dir, e.CWD) {
			return true
		}
	}
	return false
}

func appPrompt(prompt string) (body, ticket string, count int) {
	var lines []string
	for _, line := range strings.Split(prompt, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), agyAppMarker); ok {
			ticket = value
			count++
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), ticket, count
}
