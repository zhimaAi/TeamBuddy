package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// catalogRPC owns a bounded CLI control process for metadata and pre-turn session setup.
// It never sends generation prompts or exposes raw account/provider data.
type catalogRPC struct {
	ctx      context.Context
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	frames   chan []byte
	framed   bool
	sequence int
}

func startCatalogRPC(ctx context.Context, execPath, workDir string, framed bool, args ...string) (*catalogRPC, error) {
	return startCatalogRPCWithEnv(ctx, execPath, workDir, framed, nil, args...)
}

func startCatalogRPCWithEnv(ctx context.Context, execPath, workDir string, framed bool, env map[string]string, args ...string) (*catalogRPC, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd, err := NewCLIProbeCommand(ctx, execPath, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Dir, cmd.Stderr = workDir, io.Discard
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		cancel()
		return nil, err
	}
	rpc := &catalogRPC{ctx: ctx, cancel: cancel, cmd: cmd, stdin: stdin, stdout: stdout, frames: make(chan []byte, 8), framed: framed}
	go func() {
		defer close(rpc.frames)
		reader := bufio.NewReader(io.LimitReader(stdout, maxCLIProbeOutput+1))
		total := 0
		for {
			var data []byte
			var err error
			if framed {
				length := -1
				for headerBytes := 0; ; {
					line, readErr := reader.ReadString('\n')
					headerBytes += len(line)
					if readErr != nil || headerBytes > 8192 {
						return
					}
					if strings.TrimSpace(line) == "" {
						break
					}
					name, value, _ := strings.Cut(line, ":")
					if strings.EqualFold(name, "Content-Length") {
						length, _ = strconv.Atoi(strings.TrimSpace(value))
					}
				}
				if length < 0 || length > maxCLIProbeOutput-total {
					cancel()
					return
				}
				data = make([]byte, length)
				_, err = io.ReadFull(reader, data)
			} else {
				data, err = reader.ReadBytes('\n')
			}
			total += len(data)
			if err != nil || total > maxCLIProbeOutput {
				return
			}
			select {
			case rpc.frames <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	return rpc, nil
}

func (r *catalogRPC) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if r.framed {
		_, err = fmt.Fprintf(r.stdin, "Content-Length: %d\r\n\r\n%s", len(data), data)
	} else {
		_, err = r.stdin.Write(append(data, '\n'))
	}
	return err
}

func (r *catalogRPC) call(method string, params any, result any) error {
	r.sequence++
	id := r.sequence
	if err := r.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for messages := 0; messages < 4096; messages++ {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case raw, ok := <-r.frames:
			if !ok {
				return ErrThinkingUnavailable
			}
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(raw, &frame) != nil {
				continue
			}
			if frame.Method != "" {
				// Metadata clients never grant file/tool/login callbacks or send a prompt.
				if len(frame.ID) > 0 {
					if err := r.write(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32601, "message": "Metadata-only client"}}); err != nil {
						return err
					}
				}
				continue
			}
			if string(frame.ID) != strconv.Itoa(id) {
				continue
			}
			if len(frame.Error) > 0 && string(frame.Error) != "null" {
				return ErrThinkingUnavailable
			}
			if result == nil {
				return nil
			}
			return json.Unmarshal(frame.Result, result)
		}
	}
	return ErrThinkingUnavailable
}

func (r *catalogRPC) close() {
	r.stdin.Close()
	r.cancel()
	r.stdout.Close()
	_ = r.cmd.Wait()
}

type acpCatalogOption struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Current  string `json:"currentValue"`
	Options  []struct {
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"options"`
}

type acpCatalogSession struct {
	SessionID string             `json:"sessionId"`
	Options   []acpCatalogOption `json:"configOptions"`
}

func initializeCatalogACP(r *catalogRPC) error {
	return r.call("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "teambuddy-model-catalog", "version": "1"}, "clientCapabilities": map[string]any{"terminal": false, "fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "_meta": map[string]bool{"parameterizedModelPicker": true}}}, nil)
}

func thinkingFromACP(option acpCatalogOption, kind, source string) ModelThinking {
	var values []string
	for _, value := range option.Options {
		// Native Default resets a CLI override; the client's empty value only follows it.
		// Do not misrepresent that reset as another effort level.
		if value.Value != "default" {
			values = append(values, value.Value)
		}
	}
	thinking := ThinkingFromLevels(kind, values, option.Current)
	thinking.Source, thinking.Parameter = source, option.ID
	for i := range thinking.Levels {
		for _, item := range option.Options {
			if item.Value == thinking.Levels[i].Value {
				thinking.Levels[i].Description = item.Description
				break
			}
		}
	}
	return thinking
}
