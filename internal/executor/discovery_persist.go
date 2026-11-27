package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"goteams-client/internal/applog"
)

// clisFile is the on-disk shape of the cached installed-CLI list
// (~/.goteams/config/clis.json). Its "items" field mirrors the
// /tasks/cli-discovery response so the handler can serve the file directly.
type clisFile struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Items     []DiscoveredCLI `json:"items"`
}

type discoveryPersist struct {
	configDir string
	queue     *writeQueue
}

func newDiscoveryPersist(configDir string) *discoveryPersist {
	return &discoveryPersist{
		configDir: configDir,
		queue:     newWriteQueue(),
	}
}

func (p *discoveryPersist) clisPath() string {
	return filepath.Join(p.configDir, "clis.json")
}

// runWriteQueue starts the serialized file-write worker; it stops when ctx is
// cancelled. Submissions made after that are dropped (next startup re-detects).
func (p *discoveryPersist) runWriteQueue(ctx context.Context) {
	p.queue.run(ctx)
}

// loadCLIs reads the cached CLI list from disk. ok is false when the file is
// missing or corrupt, in which case callers fall back to live detection.
func (p *discoveryPersist) loadCLIs() (items []DiscoveredCLI, ok bool) {
	data, err := os.ReadFile(p.clisPath())
	if err != nil {
		return nil, false
	}
	var f clisFile
	if json.Unmarshal(data, &f) != nil {
		return nil, false
	}
	return f.Items, true
}

// saveLatest 合并连续的安装检测写入；模型目录不落盘。
func (p *discoveryPersist) saveLatest(items []DiscoveredCLI) {
	p.queue.submit(func() { p.writeCLIs(items) })
}

func (p *discoveryPersist) writeCLIs(items []DiscoveredCLI) {
	data, err := json.MarshalIndent(clisFile{UpdatedAt: time.Now(), Items: items}, "", "  ")
	if err != nil {
		return
	}
	if err := writeFileAtomic(p.clisPath(), data); err != nil {
		applog.Warn("写入 CLI 检测缓存失败", "path", p.clisPath(), "error", err.Error())
	}
}

// writeFileAtomic writes data via a temp file + rename so concurrent readers
// never observe a half-written JSON document.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".clis-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeQueue serializes file writes and coalesces overlapping submissions: the
// worker only ever executes the most recent submission, which is safe because
// every submission carries the full latest snapshot.
type writeQueue struct {
	mu   sync.Mutex
	ch   chan struct{}
	last func()
}

func newWriteQueue() *writeQueue {
	return &writeQueue{ch: make(chan struct{}, 1)}
}

func (q *writeQueue) submit(fn func()) {
	q.mu.Lock()
	q.last = fn
	q.mu.Unlock()
	select {
	case q.ch <- struct{}{}:
	default:
	}
}

func (q *writeQueue) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.ch:
			q.mu.Lock()
			fn := q.last
			q.last = nil
			q.mu.Unlock()
			if fn != nil {
				fn()
			}
		}
	}
}
