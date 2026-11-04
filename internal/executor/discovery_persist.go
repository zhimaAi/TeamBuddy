package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// modelsFile is the on-disk shape of the cached model lists
// (~/.goteams/config/models.json), keyed by CLI type.
type modelsFile struct {
	UpdatedAt     time.Time                  `json:"updated_at"`
	SchemaVersion int                        `json:"schema_version"`
	Models        map[string][]string        `json:"models"`
	Catalogs      map[string]json.RawMessage `json:"catalogs,omitempty"`
}

// discoveryPersist persists the CLI/model discovery results to
// ~/.goteams/config/clis.json and models.json. All writes go through a
// coalescing queue so the background refresh and HTTP-triggered probes never
// write concurrently, and the latest full snapshot always wins.
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

func (p *discoveryPersist) modelsPath() string {
	return filepath.Join(p.configDir, "models.json")
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

// loadModels reads the cached per-CLI model lists from disk. ok is false when
// the file is missing or corrupt, in which case callers fall back to live
// detection.
func (p *discoveryPersist) loadModels() (models map[string]CLIModelCatalog, ok bool) {
	data, err := os.ReadFile(p.modelsPath())
	if err != nil {
		return nil, false
	}
	var f modelsFile
	if json.Unmarshal(data, &f) != nil {
		return nil, false
	}
	models = make(map[string]CLIModelCatalog)
	for cliType, names := range f.Models {
		models[modelCatalogKey(cliType, "")] = unknownCatalog(names)
	}
	if f.SchemaVersion == 2 {
		for key, raw := range f.Catalogs {
			var catalog CLIModelCatalog
			if json.Unmarshal(raw, &catalog) != nil || len(catalog.Options) == 0 {
				continue
			}
			// 磁盘能力不是当前环境的核验，重启后显式运行必须重新探测。
			catalog.Meta.State = "stale"
			for i := range catalog.Options {
				catalog.Options[i].Thinking.State = "stale"
			}
			models[key] = catalog
		}
	}
	return models, true
}

// saveLatest submits the given snapshots for a single coalesced disk write.
// The closure captures the snapshots at submission time, so overlapping
// submissions (background refresh vs HTTP probe) only cause the most recent
// snapshot to be written; nothing is lost because every submission is a full
// snapshot of both files.
func (p *discoveryPersist) saveLatest(items []DiscoveredCLI, models map[string]CLIModelCatalog) {
	p.queue.submit(func() {
		p.writeCLIs(items)
		p.writeModels(models)
	})
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

func (p *discoveryPersist) writeModels(models map[string]CLIModelCatalog) {
	f := modelsFile{UpdatedAt: time.Now(), SchemaVersion: 2, Models: map[string][]string{}, Catalogs: map[string]json.RawMessage{}}
	for key, catalog := range models {
		raw, err := json.Marshal(catalog)
		if err != nil {
			continue
		}
		f.Catalogs[key] = raw
		cliType, _, _ := strings.Cut(key, "\x00")
		if key == modelCatalogKey(cliType, "") {
			f.Models[cliType] = catalog.Models()
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := writeFileAtomic(p.modelsPath(), data); err != nil {
		applog.Warn("写入模型检测缓存失败", "path", p.modelsPath(), "error", err.Error())
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
