package executor

import (
	"context"
	"strings"
	"sync"
	"time"
)

const cliDiscoveryRefreshInterval = 3 * time.Minute

// 单次实际探测的期限。等待其他扫描的时间不计入。
var cliDiscoveryBudget = 45 * time.Second

// 测试可替换探测实现，避免刷新测试启动真实 CLI。
var (
	discoverInstalledCLIs = DiscoverCLIs
	refreshUserPath       = refreshDiscoveryPath
	onDiscoveryWait       func()
)

// 发现缓存只保存可用快照；空模型结果允许下一次请求重试。
// 同类探测共享在途结果，不同 CLI 的模型探测互不阻塞。
type cliDiscoveryCache struct {
	mu           sync.RWMutex
	items        []DiscoveredCLI
	models       map[string]CLIModelCatalog
	lastSync     time.Time
	refresh      *cliDiscoveryCall
	modelFlights map[string]*modelDiscoveryCall
	generations  map[string]uint64
	persist      *discoveryPersist
}

type cliDiscoveryCall struct {
	done        chan struct{}
	err         error
	refreshPath bool
}

type modelDiscoveryCall struct {
	done       chan struct{}
	models     CLIModelCatalog
	generation uint64
}

var defaultCLIDiscovery = &cliDiscoveryCache{models: map[string]CLIModelCatalog{}}

// refreshMu 防止周期任务重叠；手动检测共享同一轮实际探测。
var refreshMu sync.Mutex

func StartCLIDiscoveryRefresh(ctx context.Context, configDir string) {
	persist := newDiscoveryPersist(configDir)
	cache := defaultCLIDiscovery
	cache.mu.Lock()
	cache.persist = persist
	cache.mu.Unlock()

	// 在后台探测开始前载入快照，避免冷启动请求与快照写回互相覆盖。
	if items, ok := persist.loadCLIs(); ok {
		cache.setItems(items)
	}
	if models, ok := persist.loadModels(); ok {
		cache.setModels(models)
	}
	go persist.runWriteQueue(ctx)
	go func() {
		refreshCLIAndModels(ctx)
		ticker := time.NewTicker(cliDiscoveryRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCLIAndModels(ctx)
			}
		}
	}()
}

func refreshCLIAndModels(ctx context.Context) {
	if !refreshMu.TryLock() {
		return
	}
	defer refreshMu.Unlock()
	if refreshCLIDiscovery(ctx, false) != nil {
		return
	}
	var wg sync.WaitGroup
	for _, item := range GetCLIDiscovery(ctx) {
		if !item.Installed {
			continue
		}
		wg.Add(1)
		go func(cliType string) {
			defer wg.Done()
			RefreshCLIModels(ctx, cliType)
		}(item.Type)
	}
	wg.Wait()
}

func GetCLIDiscovery(ctx context.Context) []DiscoveredCLI {
	cache := defaultCLIDiscovery
	cache.mu.RLock()
	ready := !cache.lastSync.IsZero()
	items := cloneCLIList(cache.items)
	cache.mu.RUnlock()
	if ready {
		return items
	}
	_ = RefreshCLIDiscovery(ctx)
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cloneCLIList(cache.items)
}

func GetCLIModels(ctx context.Context, cliType string) []string {
	return GetCLIModelCatalog(ctx, cliType, "", "", false).Models()
}

// RefreshCLIModels 保留旧调用方；目录和旧字符串列表来自同一份快照。
func RefreshCLIModels(ctx context.Context, cliType string) []string {
	return GetCLIModelCatalog(ctx, cliType, "", "", true).Models()
}

// GetCLIModelCatalogSnapshot serves the whole cached directory without waiting for
// freshness. UI callers refresh stale snapshots in the background; save/start still
// use GetCLIModelCatalog and synchronous validation.
func GetCLIModelCatalogSnapshot(ctx context.Context, cliType, workDir string, force bool) CLIModelCatalog {
	if !force {
		key := modelCatalogKey(cliType, workDir)
		defaultCLIDiscovery.mu.RLock()
		cached := catalogView(defaultCLIDiscovery.models[key])
		defaultCLIDiscovery.mu.RUnlock()
		if len(cached.Options) > 0 {
			// Legacy name-only snapshots need one background capability refresh,
			// not a fresh three-minute UI lease for unverified directory data.
			if cached.Meta.UpdatedAt.IsZero() {
				cached.Meta.State = "stale"
			}
			return cached
		}
	}
	return GetCLIModelCatalog(ctx, cliType, workDir, "", force)
}

func GetCLIModelCatalog(ctx context.Context, cliType, workDir, model string, force bool) CLIModelCatalog {
	return defaultCLIDiscovery.probeCatalog(ctx, cliType, workDir, model, force)
}

func (c *cliDiscoveryCache) probeCatalog(ctx context.Context, cliType, workDir, model string, force bool) CLIModelCatalog {
	ctx, cancel := context.WithTimeout(ctx, cliDiscoveryBudget)
	defer cancel()
	GetCLIDiscovery(ctx)
	key := modelCatalogKey(cliType, workDir)
	flightKey := key
	if cliType == CLITypePi {
		flightKey += "\x00" + model
	}
	c.mu.Lock()
	cached := catalogView(c.models[key])
	ready := len(cached.Options) > 0
	if model != "" && cliType == CLITypePi {
		ready = false
		for _, option := range cached.Options {
			if option.ID == model && option.Thinking.State == "fresh" {
				ready = true
				break
			}
		}
	}
	if !force && ready && cached.Meta.State != "stale" && !cached.Meta.UpdatedAt.IsZero() && time.Since(cached.Meta.UpdatedAt) < cliDiscoveryRefreshInterval {
		c.mu.Unlock()
		return cached
	}
	if call := c.modelFlights[flightKey]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return cached
		case <-call.done:
			return catalogView(call.models)
		}
	}
	if ctx.Err() != nil {
		c.mu.Unlock()
		return cached
	}
	if c.modelFlights == nil {
		c.modelFlights = make(map[string]*modelDiscoveryCall)
	}
	call := &modelDiscoveryCall{done: make(chan struct{}), generation: c.generations[cliType]}
	c.modelFlights[flightKey] = call
	var execPath, version string
	for _, item := range c.items {
		if item.Type == cliType && item.Installed {
			execPath, version = item.ExecPath, item.Version
			break
		}
	}
	c.mu.Unlock()

	next, err := discoverModelCatalog(ctx, cliType, execPath, workDir, model)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	c.mu.Lock()
	if call.generation == c.generations[cliType] {
		if err == nil {
			state := "fresh"
			if catalogResolvers[cliType] == nil {
				state = "unknown"
			}
			next.Meta = CatalogMeta{State: state, CLIVersion: version, UpdatedAt: time.Now()}
			// Pi 仅核验选中模型；保留同一上下文中其他模型已成功核验的能力。
			previous := c.models[key]
			if cliType == CLITypePi {
				next = appendCatalogNames(next, previous.Models())
			}
			for i := range next.Options {
				if next.Options[i].Thinking.State != "unknown" {
					continue
				}
				for _, old := range previous.Options {
					if old.ID == next.Options[i].ID && old.Thinking.Status != "unknown" {
						next.Options[i].Thinking = old.Thinking
						// A targeted Pi query did not recheck unrelated models. Other
						// failed/missing controls retain their last choices as stale.
						if cliType != CLITypePi || model == "" || old.ID == model {
							next.Options[i].Thinking.State = "stale"
						}
						break
					}
				}
			}
			if c.models == nil {
				c.models = make(map[string]CLIModelCatalog)
			}
			c.models[key] = cloneCatalog(next)
		} else if len(cached.Options) == 0 && len(next.Options) > 0 {
			c.models[key] = unknownCatalog(next.Models())
		} else {
			// 其他 Pi 模型可能在本次探测期间完成，不用起始快照覆盖其新结果。
			cached = catalogView(c.models[key])
			cached.Meta.State = "stale"
			for i := range cached.Options {
				if model == "" || cached.Options[i].ID == model {
					cached.Options[i].Thinking.State = "stale"
				}
			}
			c.models[key] = cached
		}
	}
	call.models = cloneCatalog(c.models[key])
	if c.modelFlights[flightKey] == call {
		delete(c.modelFlights, flightKey)
	}
	close(call.done)
	c.mu.Unlock()
	c.persistSnapshot()
	return catalogView(call.models)
}

// RefreshCLIDiscovery 完成实际检测后才返回；并发调用等待同一轮结果。
func RefreshCLIDiscovery(ctx context.Context) error {
	return refreshCLIDiscovery(ctx, true)
}

func refreshCLIDiscovery(ctx context.Context, refreshPath bool) error {
	return refreshCLIDiscoveryOnce(ctx, refreshPath, true)
}

// allowRetry 只补一轮：等待到的扫描没有刷新 PATH，或对方已取消而本次请求仍有效。
// 再失败就返回，避免后台扫描持续超时时来回重试。
func refreshCLIDiscoveryOnce(ctx context.Context, refreshPath bool, allowRetry bool) error {
	cache := defaultCLIDiscovery
	cache.mu.Lock()
	if call := cache.refresh; call != nil {
		cache.mu.Unlock()
		if onDiscoveryWait != nil {
			onDiscoveryWait()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			if allowRetry && shouldRediscover(ctx, refreshPath, call) {
				return refreshCLIDiscoveryOnce(ctx, refreshPath, false)
			}
			return call.err
		}
	}
	if err := ctx.Err(); err != nil {
		cache.mu.Unlock()
		return err
	}
	// 期限从真正开始探测时计算。前面的等待不能吃掉手动刷新的 45 秒。
	runCtx, cancel := context.WithTimeout(ctx, cliDiscoveryBudget)
	defer cancel()
	call := &cliDiscoveryCall{done: make(chan struct{}), refreshPath: refreshPath}
	cache.refresh = call
	cache.mu.Unlock()

	if refreshPath {
		refreshUserPath(runCtx)
	}
	items := discoverInstalledCLIs(runCtx)
	err := runCtx.Err()
	if err == nil {
		cache.setItems(items)
		cache.persistSnapshot()
	}
	cache.mu.Lock()
	call.err = err
	cache.refresh = nil
	close(call.done)
	cache.mu.Unlock()
	return err
}

func shouldRediscover(ctx context.Context, refreshPath bool, call *cliDiscoveryCall) bool {
	if ctx.Err() != nil {
		return false
	}
	if refreshPath && !call.refreshPath {
		return true
	}
	// 对方的取消或超时不属于本次请求；本次 context 仍有效时再探测一轮。
	return call.err != nil
}

func cloneCLIList(items []DiscoveredCLI) []DiscoveredCLI {
	out := make([]DiscoveredCLI, len(items))
	copy(out, items)
	for i := range out {
		out[i].Models = cloneModels(out[i].Models)
	}
	return out
}

func cloneModels(models []string) []string {
	return append([]string{}, models...)
}

func (c *cliDiscoveryCache) getPersist() *discoveryPersist {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.persist
}

func (c *cliDiscoveryCache) setItems(items []DiscoveredCLI) {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := make(map[string]DiscoveredCLI, len(c.items))
	for _, item := range c.items {
		previous[item.Type] = item
	}
	next := cloneCLIList(items)
	for i, item := range next {
		old, exists := previous[item.Type]
		delete(previous, item.Type)
		// --version 暂时失败不能让同一安装的可用模型丢失。
		if exists && item.Installed && old.Installed && item.ExecPath == old.ExecPath && item.Version == "" {
			next[i].Version = old.Version
		}
		if !item.Installed || (exists && (old.Installed != item.Installed || old.ExecPath != item.ExecPath || old.Version != next[i].Version)) {
			c.invalidateModelLocked(item.Type)
		}
	}
	for cliType := range previous {
		c.invalidateModelLocked(cliType)
	}
	c.items = next
	c.lastSync = time.Now()
}

func (c *cliDiscoveryCache) invalidateModelLocked(cliType string) {
	for key := range c.models {
		if strings.HasPrefix(key, cliType+"\x00") {
			delete(c.models, key)
		}
	}
	// 新安装不能等待旧安装的在途请求；旧请求仍按自己的 context 有界退出。
	for key := range c.modelFlights {
		if strings.HasPrefix(key, cliType+"\x00") {
			delete(c.modelFlights, key)
		}
	}
	if c.generations == nil {
		c.generations = make(map[string]uint64)
	}
	c.generations[cliType]++
}

func (c *cliDiscoveryCache) setModels(models map[string]CLIModelCatalog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = make(map[string]CLIModelCatalog, len(models))
	for key, catalog := range models {
		c.models[key] = cloneCatalog(catalog)
	}
}

func (c *cliDiscoveryCache) getModel(cliType string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	catalog, ok := c.models[modelCatalogKey(cliType, "")]
	return catalog.Models(), ok
}

func (c *cliDiscoveryCache) persistSnapshot() {
	p := c.getPersist()
	if p == nil {
		return
	}
	c.mu.RLock()
	items := cloneCLIList(c.items)
	models := make(map[string]CLIModelCatalog, len(c.models))
	for k, v := range c.models {
		models[k] = cloneCatalog(v)
	}
	// 排队动作也在快照锁内，防止旧快照延迟入队覆盖较新的快照。
	p.saveLatest(items, models)
	c.mu.RUnlock()
}
