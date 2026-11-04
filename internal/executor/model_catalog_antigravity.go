package executor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
)

func init() { RegisterModelCatalogResolver(CLITypeAntigravity, resolveAntigravityCatalog) }

func resolveAntigravityCatalog(ctx context.Context, execPath, workDir, _ string) (CLIModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	output, err := runCLIProbe(ctx, execPath, false, "models")
	if err != nil {
		return unknownCatalog(antigravityStaticModels()), err
	}
	names := parseAntigravityModelOutput(string(output))
	if len(names) == 0 {
		return unknownCatalog(antigravityStaticModels()), ErrThinkingUnavailable
	}
	catalog := unknownCatalog(names)
	if len(names) > 128 {
		return catalog, ErrThinkingUnavailable
	}
	// /effort is a documented read-only slash command, never a generation prompt.
	// Two workers and one shared deadline bound CLIs without a bulk capability API.
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				cmd, commandErr := NewCLIProbeCommand(probeCtx, execPath, "-p", "/effort", "--model", names[index], "--output-format", "json")
				if commandErr == nil {
					cmd.Dir = workDir
					capture := probeOutput{cancel: stop}
					cmd.Stdout = &capture
					if commandErr = cmd.Run(); commandErr == nil {
						if thinking, parseErr := parseAntigravityEffort(capture.buffer.Bytes()); parseErr == nil {
							catalog.Options[index].Thinking = thinking
						}
					}
				}
				stop()
			}
		}()
	}
feed:
	for index := range names {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	for _, option := range catalog.Options {
		if option.Thinking.State == "fresh" {
			return catalog, nil
		}
	}
	return catalog, ErrThinkingUnavailable
}

var antigravityAvailableEfforts = regexp.MustCompile(`(?i)available(?:\s+(?:effort\s+)?levels)?\s*[:=]\s*((?:low|medium|high|xhigh|max)(?:[ \t,|]+(?:low|medium|high|xhigh|max))*)`)
var antigravityCurrentEffort = regexp.MustCompile(`(?i)(?:current\s+)?effort\s*[:=]\s*(low|medium|high|xhigh|max)\b`)

func parseAntigravityEffort(raw []byte) (ModelThinking, error) {
	var response struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Turns    int    `json:"num_turns"`
	}
	if json.Unmarshal(raw, &response) != nil || strings.EqualFold(response.Status, "ERROR") || response.Turns != 0 {
		return ModelThinking{}, ErrThinkingUnavailable
	}
	text := strings.ToLower(stripANSI(response.Response))
	if strings.Contains(text, "not adjustable") || strings.Contains(text, "isn't adjustable") || strings.Contains(text, "isn’t adjustable") {
		thinking := ThinkingFromLevels("effort", nil, "")
		thinking.Source = "antigravity_effort_command"
		return thinking, nil
	}
	match := antigravityAvailableEfforts.FindStringSubmatch(text)
	if len(match) < 2 {
		return ModelThinking{}, ErrThinkingUnavailable
	}
	var values []string
	for _, value := range strings.FieldsFunc(match[1], func(r rune) bool { return r == ',' || r == '|' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
		switch value {
		case "low", "medium", "high", "xhigh", "max":
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return ModelThinking{}, ErrThinkingUnavailable
	}
	var current string
	if match := antigravityCurrentEffort.FindStringSubmatch(text); len(match) == 2 {
		current = match[1]
	}
	thinking := ThinkingFromLevels("effort", values, current)
	thinking.Source = "antigravity_effort_command"
	return thinking, nil
}
