package expertgroup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CloudGroup is one cloud expert group mirrored into the client's in-memory cache
// (需求 1896 优化点1/2). Local groups keep living in gt_expert_groups; cloud groups
// live only in this process-wide cache and are re-synced on login / page entry.
type CloudGroup struct {
	UUID         string   `json:"uuid"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Avatar       string   `json:"avatar"`
	SourceType   string   `json:"source_type"` // cloud
	CloudGroupID string   `json:"cloud_group_id"`
	CloudSource  string   `json:"cloud_source_key"`
	CloudSynced  int64    `json:"cloud_synced_at"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	Ready        bool     `json:"ready"`
	Leader       *Member  `json:"leader,omitempty"`
	Members      []Member `json:"members"`
}

// CloudAgent is one orchestration member of a cloud expert group in transit form.
type CloudAgent struct {
	CloudAgentID string `json:"cloud_agent_id"`
	Role         string `json:"role"` // leader | member
	Name         string `json:"name"`
	Description  string `json:"description"`
	Avatar       string `json:"avatar"`
	Prompt       string `json:"prompt"`
	CLIType      string `json:"cli_type"`
	ModelName    string `json:"model_name"`
	SortOrder    int    `json:"sort_order"`
}

// CloudGroupInput is the sync payload of one cloud expert group (from the cloud API).
type CloudGroupInput struct {
	CloudGroupID string       `json:"cloud_group_id"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Avatar       string       `json:"avatar"`
	Agents       []CloudAgent `json:"agents"`
}

// CloudMemStore is the process-wide cloud expert-group cache, mirroring the cloud
// pipeline MemStore. Groups live only in memory: a restart drops them until the next
// sync; the caller decides when to re-sync. Deterministic UUIDs keep identity stable
// across syncs and restarts.
type CloudMemStore struct {
	mu          sync.RWMutex
	activeScope string
	byUUID      map[string]*CloudGroup
	bySource    map[string][]string // cloudSourceKey → ordered group UUIDs
	sourceOrder []string
}

var defaultCloudStore = NewCloudMemStore()

// DefaultCloudStore returns the process-wide cloud expert-group cache.
func DefaultCloudStore() *CloudMemStore { return defaultCloudStore }

// NewCloudMemStore creates an empty in-memory cloud expert-group cache.
func NewCloudMemStore() *CloudMemStore {
	return &CloudMemStore{byUUID: make(map[string]*CloudGroup), bySource: make(map[string][]string)}
}

// Reset clears the cache; used by tests.
func (m *CloudMemStore) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byUUID = make(map[string]*CloudGroup)
	m.bySource = make(map[string][]string)
	m.sourceOrder = nil
}

// ReplaceAll replaces the whole cloud expert-group set of one cloud endpoint.
// All inputs are validated before any cache mutation, so a failed sync leaves the
// cache untouched.
func (m *CloudMemStore) ReplaceAll(sourceKey string, items []CloudGroupInput, scope ...string) ([]CloudGroup, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return nil, fmt.Errorf("云端专家团来源标识不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(scope) > 0 && scope[0] != m.activeScope {
		return nil, context.Canceled
	}
	now := time.Now().UnixMilli()
	built := make([]*CloudGroup, 0, len(items))
	builtByUUID := make(map[string]*CloudGroup, len(items))
	for _, input := range items {
		cloudID := strings.TrimSpace(input.CloudGroupID)
		if cloudID == "" || strings.TrimSpace(input.Name) == "" {
			return nil, fmt.Errorf("云端专家团 ID 和名称不能为空")
		}
		groupUUID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(sourceKey+"/expert-group/"+cloudID)).String()
		existing := m.byUUID[groupUUID]
		group := &CloudGroup{
			UUID:         groupUUID,
			Name:         strings.TrimSpace(input.Name),
			Description:  strings.TrimSpace(input.Description),
			Avatar:       strings.TrimSpace(input.Avatar),
			SourceType:   "cloud",
			CloudGroupID: cloudID,
			CloudSource:  sourceKey,
			CloudSynced:  now,
			UpdatedAt:    now,
		}
		if existing != nil {
			group.CreatedAt = existing.CreatedAt
		} else {
			group.CreatedAt = now
		}

		sorted := append([]CloudAgent(nil), input.Agents...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SortOrder < sorted[j].SortOrder })
		leaderSet := false
		members := make([]Member, 0, len(sorted))
		for _, agent := range sorted {
			agentUUID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(sourceKey+"/expert-group/"+cloudID+"/agent/"+agent.CloudAgentID)).String()
			role := normalizeRole(agent.Role)
			if role == RoleLeader {
				if leaderSet {
					// Only one leader; demote extras to member.
					role = RoleMember
				} else {
					leaderSet = true
				}
			}
			member := Member{
				UUID:            agentUUID,
				ExpertGroupUUID: groupUUID,
				MemberRole:      role,
				Name:            strings.TrimSpace(agent.Name),
				Description:     strings.TrimSpace(agent.Description),
				Avatar:          strings.TrimSpace(agent.Avatar),
				Prompt:          strings.TrimSpace(agent.Prompt),
				CLIType:         strings.TrimSpace(agent.CLIType),
				ModelName:       strings.TrimSpace(agent.ModelName),
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			// 本地执行配置（CLI/模型）只存在客户端：重新同步时保留上次本地配置，
			// 云端只下发编排（名称/头像/指令），不覆盖执行方式。
			if prev := m.findMemberLocked(existing, agentUUID); prev != nil {
				if member.CLIType == "" {
					member.CLIType = prev.CLIType
				}
				if member.ModelName == "" {
					member.ModelName = prev.ModelName
				}
				member.CreatedAt = prev.CreatedAt
			}
			if role == RoleLeader {
				copy := member
				group.Leader = &copy
			} else {
				members = append(members, member)
			}
		}
		group.Members = members
		group.Ready = group.Leader != nil && len(group.Members) > 0 && memberReady(*group.Leader)
		for _, member := range group.Members {
			group.Ready = group.Ready && memberReady(member)
		}

		built = append(built, group)
		builtByUUID[groupUUID] = group
	}
	ordered := make([]string, 0, len(built))
	result := make([]CloudGroup, 0, len(built))
	for _, group := range built {
		m.byUUID[group.UUID] = group
		ordered = append(ordered, group.UUID)
		result = append(result, *cloneCloudGroup(group))
	}
	// Prune groups of this source no longer in the cloud payload.
	for _, groupUUID := range m.bySource[sourceKey] {
		if _, keep := builtByUUID[groupUUID]; !keep {
			delete(m.byUUID, groupUUID)
		}
	}
	m.bySource[sourceKey] = ordered
	if len(ordered) > 0 && !containsString(m.sourceOrder, sourceKey) {
		m.sourceOrder = append(m.sourceOrder, sourceKey)
	}
	return result, nil
}

// List returns all cached cloud expert groups in first-seen source order.
func (m *CloudMemStore) List() []CloudGroup {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]CloudGroup, 0)
	seen := make(map[string]struct{})
	for _, sourceKey := range m.sourceOrder {
		for _, groupUUID := range m.bySource[sourceKey] {
			if _, ok := seen[groupUUID]; ok {
				continue
			}
			seen[groupUUID] = struct{}{}
			if group, ok := m.byUUID[groupUUID]; ok {
				result = append(result, *cloneCloudGroup(group))
			}
		}
	}
	return result
}

// Get returns one cached cloud expert group by its deterministic UUID.
func (m *CloudMemStore) Get(groupUUID string) (*CloudGroup, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	group, ok := m.byUUID[strings.TrimSpace(groupUUID)]
	if !ok {
		return nil, false
	}
	return cloneCloudGroup(group), true
}

// UpdateMemberExecution persists only the locally chosen execution config (CLI/model)
// of one cloud expert-group member, mirroring pipeline.MemStore.UpdateStep. Cloud-side
// orchestration fields (name/avatar/prompt/role) stay untouched: the cloud is the source
// of truth for them, the client only wires how the member actually runs locally.
func (m *CloudMemStore) UpdateMemberExecution(groupUUID, memberUUID, cliType, modelName string) (*Member, error) {
	cliType = strings.TrimSpace(cliType)
	modelName = strings.TrimSpace(modelName)
	if cliType == "" || modelName == "" {
		return nil, fmt.Errorf("云端专家团成员必须配置 CLI 和模型")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.byUUID[strings.TrimSpace(groupUUID)]
	if group == nil {
		return nil, ErrNotFound
	}
	now := time.Now().UnixMilli()
	if group.Leader != nil && group.Leader.UUID == memberUUID {
		group.Leader.CLIType = cliType
		group.Leader.ModelName = modelName
		group.Leader.UpdatedAt = now
		m.refreshReadyLocked(group, now)
		member := *group.Leader
		return &member, nil
	}
	for i := range group.Members {
		if group.Members[i].UUID == memberUUID {
			group.Members[i].CLIType = cliType
			group.Members[i].ModelName = modelName
			group.Members[i].UpdatedAt = now
			m.refreshReadyLocked(group, now)
			member := group.Members[i]
			return &member, nil
		}
	}
	return nil, ErrMemberNotFound
}

// BatchUpdateMemberExecution applies one CLI and model to every selected cloud
// member, including the leader. It validates the whole selection before writing,
// matching the local SQLite batch path.
func (m *CloudMemStore) BatchUpdateMemberExecution(groupUUID string, memberUUIDs []string, cliType, modelName string) (*Group, error) {
	cliType = strings.TrimSpace(cliType)
	modelName = strings.TrimSpace(modelName)
	if cliType == "" || modelName == "" {
		return nil, fmt.Errorf("云端专家团成员必须配置 CLI 和模型")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.byUUID[strings.TrimSpace(groupUUID)]
	if group == nil {
		return nil, ErrNotFound
	}
	targets := make([]*Member, 0, len(memberUUIDs))
	for _, memberUUID := range memberUUIDs {
		member := m.findMemberLocked(group, memberUUID)
		if member == nil {
			return nil, ErrMemberNotFound
		}
		targets = append(targets, member)
	}
	now := time.Now().UnixMilli()
	for _, member := range targets {
		member.CLIType = cliType
		member.ModelName = modelName
		member.UpdatedAt = now
	}
	m.refreshReadyLocked(group, now)
	projected := cloneCloudGroup(group).ToGroup()
	return &projected, nil
}

// refreshReadyLocked recomputes the group readiness flag after an execution-config
// change. The caller must hold m.mu (write lock).
func (m *CloudMemStore) refreshReadyLocked(group *CloudGroup, now int64) {
	group.UpdatedAt = now
	group.Ready = group.Leader != nil && len(group.Members) > 0 && memberReady(*group.Leader)
	for _, member := range group.Members {
		group.Ready = group.Ready && memberReady(member)
	}
}

func cloneCloudGroup(group *CloudGroup) *CloudGroup {
	out := *group
	if group.Leader != nil {
		leader := *group.Leader
		out.Leader = &leader
	}
	out.Members = append([]Member(nil), group.Members...)
	return &out
}

// ToGroup projects a cached cloud expert group into the Group shape the local server
// serializes to the frontend. SourceType=cloud marks it a read-only 团队资源; Ready
// highlights whether the client-side CLI/model wiring is complete (cloud ignores it).
func (g *CloudGroup) ToGroup() Group {
	return Group{
		UUID:        g.UUID,
		Name:        g.Name,
		Description: g.Description,
		Avatar:      g.Avatar,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
		Ready:       g.Ready,
		SourceType:  "cloud",
		Leader:      g.Leader,
		Members:     g.Members,
	}
}

// findMemberLocked locates a cached member (leader or member) of one group by UUID.
// The caller must hold m.mu; existing may be nil when the group first syncs.
func (m *CloudMemStore) findMemberLocked(group *CloudGroup, memberUUID string) *Member {
	if group == nil {
		return nil
	}
	if group.Leader != nil && group.Leader.UUID == memberUUID {
		return group.Leader
	}
	for i := range group.Members {
		if group.Members[i].UUID == memberUUID {
			return &group.Members[i]
		}
	}
	return nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Activate drops only transient cloud resources. Local data and frozen task snapshots remain intact.
func (m *CloudMemStore) Activate(scope string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeScope = scope
	m.byUUID = make(map[string]*CloudGroup)
	m.bySource = make(map[string][]string)
	m.sourceOrder = nil
}
