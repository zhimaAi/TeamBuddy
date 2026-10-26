package bootstrap

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"goteams-client"
	"goteams-client/internal/applog"
	"goteams-client/internal/localserver"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/taskruntime"
)

func (a *App) installBuiltinSkills() (bool, string, []localserver.VibeSkillStatus) {
	sourceFS, err := fs.Sub(assets.SkillsFS, "skills")
	if err != nil {
		applog.Error("打开内置 Skill 资源失败，Vibe Coding 功能不可用", "error", err)
		return false, builtinskills.CapabilityReasonInstallationFail, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		applog.Error("读取用户目录失败，Vibe Coding Skill 未安装", "error", err)
		return false, builtinskills.CapabilityReasonInstallationFail, nil
	}
	installed := map[string]struct {
		ready  bool
		reason string
	}{}
	statuses := make([]localserver.VibeSkillStatus, 0, len(taskruntime.VibeCodingTools()))
	for _, tool := range taskruntime.VibeCodingTools() {
		root := taskruntime.VibeSkillRoot(home, tool)
		result, ok := installed[root]
		if !ok {
			result.ready, result.reason = installSkillRoot(sourceFS, root)
			installed[root] = result
		}
		statuses = append(statuses, localserver.VibeSkillStatus{
			Tool: tool, Root: root, Ready: result.ready, Reason: result.reason,
		})
	}
	codexReady, codexReason := false, builtinskills.CapabilityReasonInstallationFail
	for _, status := range statuses {
		if status.Tool == taskruntime.ExecutionToolCodex {
			codexReady, codexReason = status.Ready, status.Reason
			break
		}
	}
	return codexReady, codexReason, statuses
}

func installSkillRoot(sourceFS fs.FS, root string) (bool, string) {
	if err := builtinskills.InstallBuiltin(sourceFS, root); err != nil {
		if errors.Is(err, builtinskills.ErrManagedUserChanged) {
			applog.Warn("跳过内置 Skill 更新：用户已修改 TeamBuddy 托管内容",
				"skill", builtinskills.TaskSkillName,
				"path", filepath.Join(root, builtinskills.TaskSkillName),
			)
			return false, builtinskills.CapabilityReasonNotManaged
		}
		if errors.Is(err, builtinskills.ErrTargetNotManaged) {
			applog.Warn("跳过内置 Skill 安装：同名目录不由 TeamBuddy 管理",
				"skill", builtinskills.TaskSkillName,
				"path", filepath.Join(root, builtinskills.TaskSkillName),
			)
			return false, builtinskills.CapabilityReasonNotManaged
		}
		applog.Error("安装内置 Skill 失败",
			"skill", builtinskills.TaskSkillName,
			"path", filepath.Join(root, builtinskills.TaskSkillName),
			"error", err,
		)
		return false, builtinskills.CapabilityReasonInstallationFail
	}
	return true, ""
}

// installImBotCLISkills 按 Vibe Coding 工具维度安装 teambuddy-im-bot-cli 内置 Skill。
// 微信通道兜底流程在 prompt 中以 markdown 本地路径引用此 Skill，由 CLI 自行加载。
// teambuddy 与 teambuddy-im-bot-cli 共享 VibeSkillRoot；teambuddy 安装失败的 root 不再尝试 teambuddy-im-bot-cli。
// 返回按 tool 索引的安装状态 map。失败原因记录到 Reason，由调用方在 prompt 中透传给 CLI。
func installImBotCLISkills(statuses []localserver.VibeSkillStatus) map[string]localserver.VibeImBotSkillStatus {
	sourceFS, err := fs.Sub(assets.SkillsFS, "skills")
	if err != nil {
		applog.Error("打开内置 Skill 资源失败，teambuddy-im-bot-cli Skill 未安装", "error", err)
		failures := make(map[string]localserver.VibeImBotSkillStatus, len(statuses))
		for _, status := range statuses {
			failures[status.Tool] = localserver.VibeImBotSkillStatus{
				Tool: status.Tool, Root: status.Root, Ready: false, Reason: builtinskills.CapabilityReasonInstallationFail,
			}
		}
		return failures
	}
	results := make(map[string]localserver.VibeImBotSkillStatus, len(statuses))
	for _, status := range statuses {
		definition, known := taskruntime.VibeTool(status.Tool)
		if status.Root == "" || !known || !definition.InstallIMBot {
			continue
		}
		ready, reason := installImBotCLISkill(sourceFS, status.Root)
		results[status.Tool] = localserver.VibeImBotSkillStatus{
			Tool: status.Tool, Root: status.Root, Ready: ready, Reason: reason,
		}
	}
	return results
}

// installImBotCLISkill 安装 teambuddy-im-bot-cli 内置 Skill
// 该 Skill 实现 IM Bot 与 CLI 的双向通讯能力
func installImBotCLISkill(sourceFS fs.FS, skillRoot string) (bool, string) {
	if err := builtinskills.InstallImBotCLISkill(sourceFS, skillRoot); err != nil {
		if errors.Is(err, builtinskills.ErrManagedUserChanged) {
			applog.Warn("跳过 teambuddy-im-bot-cli Skill 更新：用户已修改 TeamBuddy 托管内容",
				"skill", builtinskills.ImBotCLISkillName,
				"path", filepath.Join(skillRoot, builtinskills.ImBotCLISkillName),
			)
			return true, ""
		}
		if errors.Is(err, builtinskills.ErrTargetNotManaged) {
			applog.Warn("跳过 teambuddy-im-bot-cli Skill 安装：同名目录不由 TeamBuddy 管理",
				"skill", builtinskills.ImBotCLISkillName,
				"path", filepath.Join(skillRoot, builtinskills.ImBotCLISkillName),
			)
			return false, builtinskills.CapabilityReasonNotManaged
		}
		applog.Error("安装 teambuddy-im-bot-cli 内置 Skill 失败",
			"skill", builtinskills.ImBotCLISkillName,
			"path", filepath.Join(skillRoot, builtinskills.ImBotCLISkillName),
			"error", err,
		)
		return false, builtinskills.CapabilityReasonInstallationFail
	}
	applog.Info("teambuddy-im-bot-cli 内置 Skill 安装成功",
		"skill", builtinskills.ImBotCLISkillName,
		"path", filepath.Join(skillRoot, builtinskills.ImBotCLISkillName),
	)
	return true, ""
}
