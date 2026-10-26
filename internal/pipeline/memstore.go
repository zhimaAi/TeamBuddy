package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemStore is the process-wide cloud pipeline cache. Cloud pipelines are
// refreshed from the cloud on login / Agent page entry and live only in memory:
// a restart drops them until the next sync. Local pipelines keep living in the
// database and are merged at read time by Service.List/Get.
type MemStore struct {
	mu          sync.RWMutex
	activeScope string
	byUUID      map[string]*Pipeline
	bySource    map[string][]string // cloudSourceKey → ordered pipeline UUIDs
	sourceOrder []string            // first-seen order of cloudSourceKey
}

// defaultStore is shared by all handlers so syncs and reads observe the same
// in-memory cloud pipeline cache.
var defaultStore = NewMemStore()

// DefaultStore returns the process-wide cloud pipeline cache.
func DefaultStore() *MemStore {
	return defaultStore
}

// NewMemStore creates an empty in-memory cloud pipeline cache.
func NewMemStore() *MemStore {
	return &MemStore{byUUID: make(map[string]*Pipeline), bySource: make(map[string][]string)}
}

// Reset clears the cache; used by tests.
func (m *MemStore) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byUUID = make(map[string]*Pipeline)
	m.bySource = make(map[string][]string)
	m.sourceOrder = nil
}

// ReplaceAll replaces the whole cloud pipeline set of one cloud endpoint.
// Deterministic UUIDs keep identity stable across syncs and restarts; existing
// steps keep their locally configured CLIType/ModelName and the pipeline keeps
// its original CreatedAt, mirroring the former DB upsert semantics. All inputs
// are validated before any cache mutation, so a failed sync leaves the cache
// untouched (the former DB implementation relied on a transaction for that).
func (m *MemStore) ReplaceAll(sourceKey string, items []CloudPipeline, scope ...string) ([]Pipeline, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return nil, fmt.Errorf("云端来源标识不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(scope) > 0 && scope[0] != m.activeScope {
		return nil, context.Canceled
	}
	now := time.Now().UnixMilli()
	built := make([]*Pipeline, 0, len(items))
	builtByUUID := make(map[string]*Pipeline, len(items))
	for _, input := range items {
		cloudID := strings.TrimSpace(input.CloudPipelineID)
		if cloudID == "" || strings.TrimSpace(input.Name) == "" {
			return nil, fmt.Errorf("云端流水线 ID 和名称不能为空")
		}
		pipelineUUID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(sourceKey+"/pipeline/"+cloudID)).String()
		existing := m.byUUID[pipelineUUID]
		pipeline := &Pipeline{
			UUID:            pipelineUUID,
			Name:            strings.TrimSpace(input.Name),
			Description:     strings.TrimSpace(input.Description),
			Avatar:          strings.TrimSpace(input.Avatar),
			SourceType:      "cloud",
			CloudPipelineID: cloudID,
			CloudSourceKey:  sourceKey,
			CloudSyncedAt:   now,
			UpdatedAt:       now,
		}
		if existing != nil {
			pipeline.CreatedAt = existing.CreatedAt
		} else {
			pipeline.CreatedAt = now
		}
		steps := make([]Step, 0, len(input.Steps))
		sorted := append([]CloudStep(nil), input.Steps...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SortOrder < sorted[j].SortOrder })
		for index, step := range sorted {
			cloudStepID := strings.TrimSpace(step.CloudStepID)
			if cloudStepID == "" {
				cloudStepID = fmt.Sprintf("step-%d", index+1)
			}
			stepUUID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(sourceKey+"/pipeline/"+cloudID+"/step/"+cloudStepID)).String()
			item := Step{
				UUID:         stepUUID,
				PipelineUUID: pipelineUUID,
				SortOrder:    index + 1,
				Name:         strings.TrimSpace(step.Name),
				Description:  strings.TrimSpace(step.Description),
				Avatar:       strings.TrimSpace(step.Avatar),
				Prompt:       strings.TrimSpace(step.Prompt),
				CloudStepID:  cloudStepID,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if prev := m.findStepLocked(pipelineUUID, stepUUID); prev != nil {
				item.CLIType = prev.CLIType
				item.ModelName = prev.ModelName
				item.CreatedAt = prev.CreatedAt
			}
			steps = append(steps, item)
		}
		pipeline.Steps = steps
		built = append(built, pipeline)
		builtByUUID[pipelineUUID] = pipeline
	}
	// All inputs are valid: apply the new state in one pass.
	ordered := make([]string, 0, len(built))
	result := make([]Pipeline, 0, len(built))
	for _, pipeline := range built {
		m.byUUID[pipeline.UUID] = pipeline
		ordered = append(ordered, pipeline.UUID)
		result = append(result, *clonePipeline(pipeline))
	}
	// Prune pipelines of this source that are no longer in the cloud payload.
	for _, pipelineUUID := range m.bySource[sourceKey] {
		if _, keep := builtByUUID[pipelineUUID]; !keep {
			delete(m.byUUID, pipelineUUID)
		}
	}
	m.bySource[sourceKey] = ordered
	if !containsString(m.sourceOrder, sourceKey) {
		m.sourceOrder = append(m.sourceOrder, sourceKey)
	}
	return result, nil
}

// ApplyStepExecution writes locally chosen CLI and model back onto cached steps.
// Sync rebuilds the cache from the cloud payload, which does not include the
// client's execution config, so the caller restores it from local storage.
func (m *MemStore) ApplyStepExecution(pipelineUUID string, steps []Step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pipe := m.byUUID[strings.TrimSpace(pipelineUUID)]
	if pipe == nil {
		return
	}
	byUUID := make(map[string]Step, len(steps))
	for _, step := range steps {
		byUUID[step.UUID] = step
	}
	for i := range pipe.Steps {
		next, ok := byUUID[pipe.Steps[i].UUID]
		if !ok {
			continue
		}
		if strings.TrimSpace(next.CLIType) != "" {
			pipe.Steps[i].CLIType = next.CLIType
		}
		if strings.TrimSpace(next.ModelName) != "" {
			pipe.Steps[i].ModelName = next.ModelName
		}
	}
}

// Get returns a deep copy of the pipeline with the given UUID.
func (m *MemStore) Get(pipelineUUID string) (*Pipeline, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pipe := m.byUUID[pipelineUUID]
	if pipe == nil {
		return nil, false
	}
	return clonePipeline(pipe), true
}

// List returns deep copies of all cached cloud pipelines in sync order.
func (m *MemStore) List() []Pipeline {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Pipeline, 0)
	for _, sourceKey := range m.sourceOrder {
		for _, pipelineUUID := range m.bySource[sourceKey] {
			if pipe := m.byUUID[pipelineUUID]; pipe != nil {
				result = append(result, *clonePipeline(pipe))
			}
		}
	}
	return result
}

// UpdateStep persists the locally chosen execution config of a cloud step.
func (m *MemStore) UpdateStep(pipelineUUID, stepUUID, cliType, modelName string) (*Step, error) {
	cliType = strings.TrimSpace(cliType)
	modelName = strings.TrimSpace(modelName)
	if cliType == "" || modelName == "" {
		return nil, fmt.Errorf("云端 Agent 编排必须配置 CLI 和模型")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pipe := m.byUUID[pipelineUUID]
	if pipe == nil {
		return nil, ErrNotFound
	}
	for i := range pipe.Steps {
		if pipe.Steps[i].UUID == stepUUID {
			pipe.Steps[i].CLIType = cliType
			pipe.Steps[i].ModelName = modelName
			pipe.Steps[i].UpdatedAt = time.Now().UnixMilli()
			step := pipe.Steps[i]
			return &step, nil
		}
	}
	return nil, ErrStepNotFound
}

// BatchUpdateStepExecution validates all step UUIDs before mutating the cloud
// pipeline cache, so one missing target cannot leave a partially updated set.
func (m *MemStore) BatchUpdateStepExecution(pipelineUUID string, stepUUIDs []string, cliType, modelName string) (*Pipeline, error) {
	cliType = strings.TrimSpace(cliType)
	modelName = strings.TrimSpace(modelName)
	if cliType == "" || modelName == "" {
		return nil, fmt.Errorf("云端 Agent 编排必须配置 CLI 和模型")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	pipe := m.byUUID[pipelineUUID]
	if pipe == nil {
		return nil, ErrNotFound
	}
	indices := make([]int, 0, len(stepUUIDs))
	for _, stepUUID := range stepUUIDs {
		found := -1
		for idx := range pipe.Steps {
			if pipe.Steps[idx].UUID == stepUUID {
				found = idx
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("%w: %s", ErrStepNotFound, stepUUID)
		}
		indices = append(indices, found)
	}

	now := time.Now().UnixMilli()
	for _, idx := range indices {
		pipe.Steps[idx].CLIType = cliType
		pipe.Steps[idx].ModelName = modelName
		pipe.Steps[idx].UpdatedAt = now
	}
	pipe.UpdatedAt = now
	return clonePipeline(pipe), nil
}

func (m *MemStore) findStepLocked(pipelineUUID, stepUUID string) *Step {
	pipe := m.byUUID[pipelineUUID]
	if pipe == nil {
		return nil
	}
	for i := range pipe.Steps {
		if pipe.Steps[i].UUID == stepUUID {
			return &pipe.Steps[i]
		}
	}
	return nil
}

func clonePipeline(pipe *Pipeline) *Pipeline {
	cloned := *pipe
	cloned.Steps = make([]Step, len(pipe.Steps))
	copy(cloned.Steps, pipe.Steps)
	return &cloned
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Activate drops only transient cloud resources. Local data and frozen task snapshots remain intact.
func (m *MemStore) Activate(scope string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeScope = scope
	m.byUUID = make(map[string]*Pipeline)
	m.bySource = make(map[string][]string)
	m.sourceOrder = nil
}
