package codexhook

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxAgyTranscript = 64 << 20

func agyTranscriptRelative(home, surface, session, supplied string) string {
	if !filepath.IsAbs(supplied) {
		return ""
	}
	supplied = filepath.Clean(supplied)
	matches := func(want string) bool {
		return supplied == want || runtime.GOOS == "windows" && strings.EqualFold(supplied, want)
	}
	for _, name := range []string{"transcript_full.jsonl", "transcript.jsonl"} {
		rel := filepath.Join(".gemini", surface, "brain", session, ".system_generated", "logs", name)
		want := filepath.Join(home, rel)
		if matches(want) {
			return rel
		}
		// macOS can report either /var or its canonical /private/var path.
		if realHome, err := filepath.EvalSymlinks(home); err == nil && matches(filepath.Join(realHome, rel)) {
			return rel
		}
	}
	return ""
}

// Only the surface transcript at the verified conversation UUID is eligible.
// Keep source/type and status, never thinking, tool arguments or tool results.
func readAgyTurn(ctx context.Context, home, rel, prompt string, firstOnly bool) (turn, answer string, err error) {
	if prompt == "" {
		return "", "", nil
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	// Anchor each component separately so a concurrent symlink replacement
	// cannot redirect the read to a different conversation within the home.
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() {
			return "", "", errors.New("not an Antigravity transcript directory")
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return "", "", err
		}
		defer next.Close()
		actual, err := next.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			return "", "", errors.New("Antigravity transcript directory changed")
		}
		root = next
	}
	name := parts[len(parts)-1]
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxAgyTranscript {
		return "", "", errors.New("not a bounded Antigravity transcript")
	}
	f, err := root.Open(name)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	if actual, err := f.Stat(); err != nil || !os.SameFile(info, actual) {
		return "", "", errors.New("Antigravity transcript changed")
	}
	limited := &io.LimitedReader{R: f, N: maxAgyTranscript + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxBytes)
	seenUser := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		var row struct {
			Index   *int              `json:"step_index"`
			Created string            `json:"created_at"`
			Type    string            `json:"type"`
			Source  string            `json:"source"`
			Status  string            `json:"status"`
			Content string            `json:"content"`
			Tools   []json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.Index == nil || *row.Index < 0 {
			return "", "", errors.New("invalid Antigravity transcript step")
		}
		switch {
		case row.Type == "USER_INPUT" && row.Source == "USER_EXPLICIT":
			if firstOnly && seenUser {
				return "", "", nil
			}
			seenUser = true
			turn, answer = "", ""
			// The raw user text comes from the hook; the transcript adds metadata.
			_, dateErr := time.Parse(time.RFC3339Nano, row.Created)
			if row.Status == "DONE" && dateErr == nil && (row.Content == prompt || strings.HasPrefix(row.Content, "<USER_REQUEST>\n"+prompt+"\n</USER_REQUEST>")) {
				turn = strconv.Itoa(*row.Index) + ":" + row.Created
			}
		case row.Type == "PLANNER_RESPONSE" && row.Source == "MODEL" && turn != "":
			answer = "" // A later model/tool step invalidates earlier commentary.
			if row.Status == "DONE" && len(row.Tools) == 0 {
				answer = strings.TrimSpace(row.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil || limited.N == 0 {
		return "", "", errors.New("Antigravity transcript exceeds read limit or is incomplete")
	}
	return turn, answer, nil
}
