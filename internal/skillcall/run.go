// Package skillcall is the sidecar subcommand used by the teambuddy Skill.
// It does not start the desktop client. Both macOS and Windows invoke it
// instead of a shell script.
package skillcall

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
	"unicode/utf8"

	"goteams-client/internal/taskskillclient"
)

const requestTimeout = 15 * time.Second

var taskIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var actions = map[string]struct {
	method string
	suffix string
}{
	"get":      {method: "GET", suffix: ""},
	"bind":     {method: "PUT", suffix: "/vibe-session"},
	"status":   {method: "PUT", suffix: "/status"},
	"activity": {method: "POST", suffix: "/activities"},
}

// Run reads the stable runtime.json and performs one task-skill call.
// stdout receives the response body. Diagnostics go to stderr.
func Run(ctx context.Context, args []string) int {
	return run(ctx, args, taskskillclient.DefaultRuntimePath(), os.Stdout, os.Stderr)
}

func run(ctx context.Context, args []string, runtimePath string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skill-call", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	task := fs.String("task", "", "")
	action := fs.String("action", "", "")
	bodyPath := fs.String("body", "", "")
	lang := fs.String("lang", "zh-CN", "")
	if err := fs.Parse(args); err != nil || len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "usage: skill-call -task <uuid> -action get|bind|status|activity [-body <file>] [-lang zh-CN|en-US]")
		return 2
	}
	if !taskIDPattern.MatchString(*task) {
		fmt.Fprintln(stderr, "usage: skill-call -task <uuid> -action get|bind|status|activity [-body <file>] [-lang zh-CN|en-US]")
		return 2
	}
	spec, ok := actions[*action]
	if !ok || (*lang != "zh-CN" && *lang != "en-US") {
		fmt.Fprintln(stderr, "usage: skill-call -task <uuid> -action get|bind|status|activity [-body <file>] [-lang zh-CN|en-US]")
		return 2
	}
	var body []byte
	if *action == "get" {
		if *bodyPath != "" {
			fmt.Fprintln(stderr, "TeamBuddy get does not take a body")
			return 2
		}
	} else {
		if *bodyPath == "" {
			fmt.Fprintln(stderr, "TeamBuddy request body is required")
			return 2
		}
		raw, err := os.ReadFile(*bodyPath)
		if err != nil || !utf8.Valid(raw) || !json.Valid(raw) {
			fmt.Fprintln(stderr, "TeamBuddy request body must be valid UTF-8 JSON")
			return 2
		}
		body = raw
	}
	client := &taskskillclient.Client{
		RuntimePath:   func() string { return runtimePath },
		HTTPClient:    taskskillclient.NewHTTPClient(requestTimeout),
		ResponseLimit: -1,
	}
	status, data, err := client.Exchange(ctx, spec.method, *task, spec.suffix, *lang, body)
	if err != nil {
		if err == taskskillclient.ErrNotLoopback {
			fmt.Fprintln(stderr, "TeamBuddy runtime address must be loopback")
			return 2
		}
		if err == taskskillclient.ErrRuntimeAddress {
			fmt.Fprintln(stderr, "TeamBuddy connection failed: runtime address unavailable. Check the desktop client and this tool's sandbox/network permissions; this error alone does not prove the client is offline.")
			return 1
		}
		fmt.Fprintf(stderr, "TeamBuddy connection failed: %v. Check the desktop client and this tool's sandbox/network permissions; this error alone does not prove the client is offline.\n", err)
		return 1
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintln(stderr, "TeamBuddy connection failed: response was not written")
		return 1
	}
	if status < 200 || status >= 300 {
		fmt.Fprintf(stderr, "TeamBuddy HTTP %d\n", status)
		return 1
	}
	return 0
}
