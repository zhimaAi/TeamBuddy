package executor

import (
	"context"
	"errors"
	"os"
	"slices"
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

// 安装信息可缓存；模型仅合并同一上下文的在途探测，不保留已完成结果。
type cliDiscoveryCache struct {
	mu           sync.RWMutex
	items        []DiscoveredCLI
	lastSync     time.Time
	refresh      *cliDiscoveryCall
	modelFlights map[string]*modelDiscoveryCall
	persist      *discoveryPersist
}

type cliDiscoveryCall struct {
	done        chan struct{}
	err         error
	refreshPath bool
}

type modelDiscoveryCall struct {
	done   chan struct{}
	models CLIModelCatalog
}

var defaultCLIDiscovery = &cliDiscoveryCache{}

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
	go persist.runWriteQueue(ctx)
	go func() {
		_ = refreshCLIDiscovery(ctx, false)
		ticker := time.NewTicker(cliDiscoveryRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = refreshCLIDiscovery(ctx, false)
			}
		}
	}()
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

// ReadCLIModelCatalog 只供显式读取使用。没有已确认的非交互协议时，不启动 CLI。
func ReadCLIModelCatalog(ctx context.Context, cliType, workDir, model string) CLIModelCatalog {
	switch cliType {
	case CLITypeCodex:
		// app-server 仅握手和 model/list，不发送登录 RPC。
		model = ""
	case CLITypePi:
		// 离线 RPC 不加载扩展；只有显式读取选中模型的强度才带模型参数。
		if model != "" && !piModelSelector.MatchString(model) {
			return unknownCatalog(nil)
		}
	case CLITypeAntigravity:
		// ACP 只读原生账号目录或选中模型，不发送认证或生成请求。
	default:
		return unknownCatalog(nil)
	}
	return defaultCLIDiscovery.probeCatalog(ctx, cliType, workDir, model)
}

func (c *cliDiscoveryCache) probeCatalog(ctx context.Context, cliType, workDir, model string) CLIModelCatalog {
	resolver := catalogResolvers[cliType]
	if resolver == nil {
		return unknownCatalog(nil)
	}
	if cliType != CLITypePi && cliType != CLITypeAntigravity {
		// 其他 resolver 一次读取整份目录，不随所选模型变化。
		model = ""
	}
	ctx, cancel := context.WithTimeout(ctx, cliDiscoveryBudget)
	defer cancel()
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}
	GetCLIDiscovery(ctx)
	key := modelCatalogKey(cliType, workDir) + "\x00" + model
	c.mu.Lock()
	if call := c.modelFlights[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return unknownCatalog(nil)
		case <-call.done:
			return cloneCatalog(call.models)
		}
	}
	var execPath, version string
	for _, item := range c.items {
		if item.Type == cliType && item.Installed {
			execPath, version = item.ExecPath, item.Version
			break
		}
	}
	if execPath == "" || ctx.Err() != nil {
		c.mu.Unlock()
		return unknownCatalog(nil)
	}
	if c.modelFlights == nil {
		c.modelFlights = make(map[string]*modelDiscoveryCall)
	}
	call := &modelDiscoveryCall{done: make(chan struct{}), models: unknownCatalog(nil)}
	c.modelFlights[key] = call
	c.mu.Unlock()

	next, err := resolver(ctx, execPath, workDir, model)
	c.mu.Lock()
	if c.modelFlights[key] == call {
		delete(c.modelFlights, key)
		if err == nil && ctx.Err() == nil {
			// 不让专供 TeamBuddy 的默认选项被第三方目录作为模型返回。
			next.Options = slices.DeleteFunc(next.Options, func(option CLIModelOption) bool {
				return option.ID == DefaultCLIModel
			})
			next.Meta = CatalogMeta{State: "fresh", CLIVersion: version, UpdatedAt: time.Now()}
			call.models = next
		} else if cliType == CLITypeAntigravity {
			switch {
			case errors.Is(err, errCatalogAuthenticationRequired):
				call.models.Meta.Reason = "login_required"
			case next.Meta.Reason != "":
				call.models.Meta.Reason = next.Meta.Reason
			default:
				call.models.Meta.Reason = "read_failed"
			}
		}
	}
	close(call.done)
	c.mu.Unlock()
	return cloneCatalog(call.models)
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
		// --version 暂时失败时保留同一安装的版本。
		next[i].Models = []string{}
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
	// 新安装不能等待旧安装的在途请求；旧请求仍按自己的 context 有界退出。
	for key := range c.modelFlights {
		if strings.HasPrefix(key, cliType+"\x00") {
			delete(c.modelFlights, key)
		}
	}
}

func (c *cliDiscoveryCache) persistSnapshot() {
	p := c.getPersist()
	if p == nil {
		return
	}
	c.mu.RLock()
	items := cloneCLIList(c.items)
	// 排队动作也在快照锁内，防止旧快照延迟入队覆盖较新的快照。
	p.saveLatest(items)
	c.mu.RUnlock()
}
