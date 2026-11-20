package codexhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

func read(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(data) > maxBytes || json.Unmarshal(data, value) != nil {
		return errors.New("invalid Codex callback state")
	}
	return nil
}

func save(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxBytes {
		return errors.New("Codex callback storage limit reached")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".codex-hook-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

// A launch can be claimed by exactly one native session, including when the
// client is offline. Both files survive a crash between claim and session save.
func claim(ctx context.Context, root, id, session, cwd string) (launch, error) {
	file := filepath.Join(root, "launch-"+id+".json")
	release, err := lockFile(file + ".lock")
	if err != nil {
		return launch{}, err
	}
	defer release()
	var item launch
	if err := read(file, &item); err != nil {
		return item, err
	}

	if item.SessionID != "" && item.SessionID != session {
		return item, errors.New("Codex launch already belongs to another session")
	}
	if item.SessionID == "" && time.Now().Unix() > item.ExpiresAt {
		return item, errors.New("Codex launch expired; prepare a new launch")
	}
	if item.Scope == "user" {
		if !relatedWorkspace(ctx, item.CWD, cwd) {
			return item, errors.New("Codex launch workspace mismatch")
		}
		item.ChatDir = cwd
	} else if !sameDirectory(item.CWD, cwd) {
		return item, errors.New("Codex launch workspace mismatch")
	}
	if item.SessionID == "" {
		item.ExplicitPrivacy = true
	} // An already claimed launch keeps its original policy.
	item.SessionID = session
	return item, save(file, item)
}
