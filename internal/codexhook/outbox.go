package codexhook

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"goteams-client/internal/taskskillclient"
)

var errStale = errors.New("Codex task generation, workspace or binding changed")

func callbackRoot() string {
	return filepath.Join(filepath.Dir(taskskillclient.DefaultRuntimePath()), "codex-app")
}

// Locks are held only while updating files, never across HTTP in user scope.
func acquireStateLock(ctx context.Context, path string, wait bool) (func(), error) {
	for n := 0; ; n++ {
		release, err := lockFile(path)
		if err == nil || !wait || n >= 24 {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func markPending(root, id string) error {
	dir := filepath.Join(root, "outbox")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

// A marker is written before its queue, under the same lock. A crash before
// HTTP acknowledgement replays the original event_id, never a new event.
func drainSession(ctx context.Context, client *taskskillclient.Client, root, id string) error {
	if u, e := uuid.Parse(id); e != nil || u == uuid.Nil || u.String() != id {
		return errors.New("invalid outbox entry")
	}
	file := filepath.Join(root, "session-"+id+".json")
	marker := filepath.Join(root, "outbox", id)
	release, err := acquireStateLock(ctx, file+".lock", true)
	if err != nil {
		return err
	}
	var s state
	err = read(file, &s)
	if os.IsNotExist(err) {
		_ = os.Remove(marker)
	}
	release()
	if err != nil {
		return err
	}
	if s.SessionID != id || s.Scope != "user" {
		return errors.New("invalid outbox session")
	}
	bound, err := verify(ctx, client, s.launch)
	if err == nil && !bound {
		err = client.BindSession(ctx, s.TaskUUID, "", taskskillclient.SessionBinding{Type: "codex", ThreadID: id, ExecutionIndex: s.Index, ReportingOwner: "hooks"})
	}
	delivered := map[string]bool{}
	if err == nil {
		bound = true
		for n, event := range s.Pending {
			if n >= 8 {
				break
			}
			if event.ThreadID != id || event.ExecutionIndex != s.Index || event.Source != "hooks" || event.Tool != "codex" {
				err = errors.New("invalid queued event")
				break
			}
			if err = client.ReportActivity(ctx, s.TaskUUID, "", event); err != nil {
				break
			}
			delivered[event.EventID] = true
		}
	}
	stale := errors.Is(err, errStale) || taskskillclient.HasCode(err, "task_not_found", "task_not_codex", "vibe_session_stale")
	if !bound && !stale {
		return err
	}
	release, lockErr := acquireStateLock(ctx, file+".lock", true)
	if lockErr != nil {
		return lockErr
	}
	defer release()
	var latest state
	if e := read(file, &latest); e != nil {
		return e
	}
	if latest.LaunchID != s.LaunchID || latest.SessionID != id {
		return errStale
	}
	latest.Bound = bound
	latest.Stale = stale
	remaining := latest.Pending[:0]
	for _, item := range latest.Pending {
		if !delivered[item.EventID] {
			remaining = append(remaining, item)
		}
	}
	latest.Pending = remaining
	if e := save(file, latest); e != nil {
		return e
	}
	if stale || len(remaining) == 0 && bound {
		if e := os.Remove(marker); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return err
}

// Only TeamBuddy-owned pending markers are enumerated. Never read Codex's
// sessions directory. A single reader bounds concurrent delivery across chats.
func DrainPending(ctx context.Context) {
	client := taskskillclient.New(1500 * time.Millisecond)
	root := callbackRoot()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer client.HTTPClient.CloseIdleConnections()
	var dir *os.File
	defer func() {
		if dir != nil {
			dir.Close()
		}
	}()
	type retry struct {
		until time.Time
		delay time.Duration
	}
	later := map[string]retry{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if dir == nil {
			var err error
			dir, err = os.Open(filepath.Join(root, "outbox"))
			if err != nil {
				continue
			}
		}
		entries, readErr := dir.ReadDir(16)
		for _, f := range entries {
			if ctx.Err() != nil {
				return
			}
			if !f.Type().IsRegular() {
				continue
			}
			id := f.Name()
			if time.Now().Before(later[id].until) {
				continue
			}
			attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := drainSession(attempt, client, root, id)
			cancel()
			if err == nil {
				delete(later, id)
			} else {
				d := min(2*later[id].delay+time.Second, time.Minute)
				later[id] = retry{time.Now().Add(d), d}
			}
		}
		if readErr != nil || len(entries) == 0 {
			dir.Close()
			dir = nil
			if readErr != nil && readErr != io.EOF {
				continue
			}
			for id, delay := range later {
				if time.Now().After(delay.until) {
					if _, err := os.Stat(filepath.Join(root, "outbox", id)); os.IsNotExist(err) {
						delete(later, id)
					}
				}
			}
		}
	}
}
