package taskruntime

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ExecutionModePipeline    = "pipeline"
	ExecutionModeExpertGroup = "expert_group"
	ExecutionModeVibeCoding  = "vibe_coding"
	ExecutionModeCLI         = "cli"
	ExecutionToolCodex       = "codex"
	ExecutionToolCodexCLI    = "codex_cli"
	ExecutionToolClaude      = "claude"
	ExecutionToolCodeBuddy   = "codebuddy"
	ExecutionToolQoder       = "qoder"
	ExecutionToolQoderIDE    = "qoder_ide"
	ExecutionToolPi          = "pi"
	ExecutionToolAntigravity = "antigravity"
	// 手动模式工具：客户端不启动任何进程，只生成可复制包（MCP 配置 + Skill 路径 +
	// 任务提示词），用户在外部工具中粘贴执行，并通过 Skill + MCP 回传动态与状态。
	ExecutionToolCursor    = "cursor"
	ExecutionToolWorkBuddy = "workbuddy"
	ExecutionToolDSH       = "dsh"
	ExecutionToolGeneric   = "generic"

	// CLIDirectStepKey 是 CLI 直接执行模式的隐式步骤标识。该模式没有 Agent
	// 编排，但仍复用 gt_task_steps 承载用户选定的 CLI 与模型，使会话、动态、
	// 停止和断线恢复沿用同一条链路。
	CLIDirectStepKey = "cli-direct"
	// CLIDirectStepName 是隐式步骤的展示名，前端在 CLI 模式下会用任务实际
	// 选定的 CLI 与模型覆盖它。
	CLIDirectStepName = "CLI 直接执行"
)

// VibeToolDefinition 集中描述交付、会话和 Skill 安装策略；远程工具序号保持稳定。
type VibeToolDefinition struct {
	ID               string `json:"id"`
	Launch           string `json:"launch"`
	BindingKind      string `json:"binding_kind"`
	RequiresReportID bool   `json:"requires_report_id"`
	SupportsResume   bool   `json:"supports_resume"`
	SupportsRebind   bool   `json:"supports_rebind"`
	SkillDirectory   string `json:"-"`
	InstallIMBot     bool   `json:"-"`
}

var vibeToolDefinitions = [...]VibeToolDefinition{
	{ID: ExecutionToolCodex, Launch: "app", BindingKind: "native", SupportsResume: true, SkillDirectory: ".codex/skills", InstallIMBot: true},
	{ID: ExecutionToolCodexCLI, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".codex/skills", InstallIMBot: true},
	{ID: ExecutionToolPi, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".pi/agent/skills", InstallIMBot: true},
	{ID: ExecutionToolAntigravity, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".gemini/antigravity-cli/skills", InstallIMBot: true},
	{ID: ExecutionToolQoder, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".qoder/skills", InstallIMBot: true},
	{ID: ExecutionToolCodeBuddy, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".codebuddy/skills", InstallIMBot: true},
	{ID: ExecutionToolClaude, Launch: "terminal", BindingKind: "native", SupportsResume: true, SkillDirectory: ".claude/skills", InstallIMBot: true},
	{ID: ExecutionToolQoderIDE, Launch: "app", BindingKind: "native", SupportsResume: true, SkillDirectory: ".qoder/skills", InstallIMBot: true},
	// 手动模式不启动本机进程，也不探测可执行文件；Skill 安装到外部工具的用户级目录。
	{ID: ExecutionToolCursor, Launch: "manual", BindingKind: "native", SkillDirectory: ".cursor/skills"},
	{ID: ExecutionToolGeneric, Launch: "clipboard", BindingKind: "generic", RequiresReportID: true, SupportsRebind: true, SkillDirectory: ".teambuddy/skills"},
	// 新增手动工具一律追加到末尾：VibeCodingTools 的下标即远程工具序号（1-based），
	// 插入中间会让既有 `<Vibe工具序号>` 指令指向错误的工具。
	{ID: ExecutionToolWorkBuddy, Launch: "manual", BindingKind: "native", SkillDirectory: ".workbuddy/skills"},
	{ID: ExecutionToolDSH, Launch: "manual", BindingKind: "native", SkillDirectory: ".dsh/skills"},
}

func VibeTool(tool string) (VibeToolDefinition, bool) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	for _, definition := range vibeToolDefinitions {
		if definition.ID == tool {
			return definition, true
		}
	}
	return VibeToolDefinition{}, false
}

func VibeCodingTools() []string {
	tools := make([]string, 0, len(vibeToolDefinitions))
	for _, definition := range vibeToolDefinitions {
		tools = append(tools, definition.ID)
	}
	return tools
}

func IsVibeCodingTool(tool string) bool {
	_, ok := VibeTool(tool)
	return ok
}

func RequiresVibeReportID(tool string) bool {
	definition, ok := VibeTool(tool)
	return ok && definition.RequiresReportID
}

// Codex 应用和 Codex CLI 共用 ~/.codex/skills。
func VibeSkillRoot(home, tool string) string {
	definition, ok := VibeTool(tool)
	if !ok {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(definition.SkillDirectory))
}

var (
	ErrVibeModelInvalid         = errors.New("Vibe Coding 模型无效")
	ErrExecutionModeConflict    = errors.New("任务已经指派其他执行方式")
	ErrExecutionModeUnsupported = errors.New("该执行方式暂未支持")
	ErrExpertRoutingPending     = errors.New("专家团自动路由处理中")
)

var vibeModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\-]{0,255}$`)

// Vibe 模型为空时跟随 CLI 配置；只保存模型标识，不接收任意启动参数。
func ValidateVibeModel(tool, model string) error {
	if model == "" {
		return nil
	}
	switch tool {
	case ExecutionToolCodexCLI, ExecutionToolPi, ExecutionToolAntigravity, ExecutionToolQoder, ExecutionToolCodeBuddy:
		if vibeModelPattern.MatchString(model) && !(tool == ExecutionToolPi && strings.HasSuffix(model, "/")) {
			return nil
		}
	}
	return ErrVibeModelInvalid
}

func NormalizeExecutionMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func ValidateExecutionAssignment(mode, tool, pipelineUUID string) error {
	return ValidateExecutionTarget(mode, tool, pipelineUUID, "")
}

func ValidateExecutionTarget(mode, tool, pipelineUUID, expertGroupUUID string) error {
	mode = NormalizeExecutionMode(mode)
	tool = strings.ToLower(strings.TrimSpace(tool))
	pipelineUUID = strings.TrimSpace(pipelineUUID)
	expertGroupUUID = strings.TrimSpace(expertGroupUUID)
	switch mode {
	case "":
		if tool != "" || pipelineUUID != "" || expertGroupUUID != "" {
			return fmt.Errorf("执行方式未选择")
		}
		return nil
	case ExecutionModePipeline:
		if tool != "" || expertGroupUUID != "" {
			return fmt.Errorf("流水线执行不能指定编程工具")
		}
		return nil
	case ExecutionModeVibeCoding:
		if pipelineUUID != "" || expertGroupUUID != "" {
			return fmt.Errorf("Vibe Coding 执行不能指定流水线")
		}
		if !IsVibeCodingTool(tool) {
			return fmt.Errorf("Vibe Coding 工具无效")
		}
		return nil
	case ExecutionModeExpertGroup:
		if tool != "" || pipelineUUID != "" {
			return fmt.Errorf("专家团执行不能指定流水线或编程工具")
		}
		if expertGroupUUID == "" {
			return fmt.Errorf("专家团不能为空")
		}
		return nil
	case ExecutionModeCLI:
		if pipelineUUID != "" || expertGroupUUID != "" {
			return fmt.Errorf("CLI 执行不能指定流水线或专家团")
		}
		if tool == "" {
			return fmt.Errorf("CLI 不能为空")
		}
		return nil
	default:
		return fmt.Errorf("无效任务执行方式")
	}
}

// ValidateCLITarget 校验 CLI 直接执行方式的目标。该模式没有 Agent 编排，
// CLI 与模型都是必填项，缺一不可执行。
func ValidateCLITarget(cliType, modelName string) error {
	if strings.ToLower(strings.TrimSpace(cliType)) == "" {
		return fmt.Errorf("CLI 不能为空")
	}
	if strings.TrimSpace(modelName) == "" {
		return fmt.Errorf("模型不能为空")
	}
	return nil
}
