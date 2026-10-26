package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// BuildExpertTaskPrompt builds one expert-group turn from the frozen task roster.
func BuildExpertTaskPrompt(ctx context.Context, db *sql.DB, taskUUID, stepUUID, handoff string) (string, error) {
	var title, taskDir, taskMDPath, name, prompt, memberDir, role, groupName, groupDescription string
	err := db.QueryRowContext(ctx, `SELECT t.title, t.task_dir, t.task_md_path, s.name, s.prompt_snapshot, s.step_dir,
		s.member_role, g.name, g.description
		FROM gt_tasks t JOIN gt_task_steps s ON s.task_uuid=t.uuid
		JOIN gt_task_expert_group_snapshots g ON g.uuid=t.expert_group_snapshot_uuid
		WHERE t.uuid=? AND s.uuid=? AND s.execution_mode='expert_group'`, taskUUID, stepUUID).Scan(
		&title, &taskDir, &taskMDPath, &name, &prompt, &memberDir, &role, &groupName, &groupDescription)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("专家团执行成员不存在")
	}
	if err != nil {
		return "", fmt.Errorf("读取专家团执行上下文失败: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT uuid, name, member_role, description, step_dir
		FROM gt_task_steps WHERE task_uuid=? AND execution_mode='expert_group'
		ORDER BY CASE member_role WHEN 'leader' THEN 0 ELSE 1 END, step_order, rowid`, taskUUID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type rosterItem struct{ id, name, role, description, dir string }
	roster := make([]rosterItem, 0)
	for rows.Next() {
		var item rosterItem
		if err := rows.Scan(&item.id, &item.name, &item.role, &item.description, &item.dir); err != nil {
			return "", err
		}
		roster = append(roster, item)
	}
	workRows, err := db.QueryContext(ctx, `SELECT path FROM gt_task_work_dirs WHERE task_uuid=? ORDER BY sort_order, id`, taskUUID)
	if err != nil {
		return "", err
	}
	defer workRows.Close()
	workDirs := make([]string, 0)
	for workRows.Next() {
		var path string
		if err := workRows.Scan(&path); err != nil {
			return "", err
		}
		workDirs = append(workDirs, path)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "你正在执行本地任务「%s」，当前身份是专家团「%s」的%s「%s」。\n\n", title, groupName, expertRoleLabel(role), name)
	fmt.Fprintf(&b, "首先完整读取任务原始需求文件：%s。\n", taskMDPath)
	b.WriteString("按需要读取任务目录和项目目录中的现有内容后再行动。\n\n")
	if role == "leader" {
		b.WriteString("# 专家团协调协议\n\n")
		b.WriteString("你是团长，只负责理解目标、协调、验收和汇总，不得亲自修改项目代码。\n")
		b.WriteString("需要成员行动时，你的最终回复必须且只能包含一个结构化成员 mention，格式必须与名册完全一致。\n")
		b.WriteString("每轮最多委派一名成员；不得同时 mention 多人，也不得 mention 自己。\n")
		b.WriteString("如果当前无需继续委派，直接给出阶段总结且不要包含 mention；任务将保持进行中等待用户决定。\n")
		b.WriteString("成员失败时判断应换人、修正要求还是向用户说明阻塞。\n\n")
		if strings.TrimSpace(prompt) != "" {
			fmt.Fprintf(&b, "# 团长角色提示词\n\n%s\n\n", strings.TrimSpace(prompt))
		}
		b.WriteString("# 当前任务可委派成员名册\n\n")
		b.WriteString("只能委派以下成员，成员名称和 mention 必须与名册完全一致。\n\n")
		for _, item := range roster {
			if item.role == "leader" {
				continue
			}
			fmt.Fprintf(&b, "- %s：%s\n  [@%s](mention://expert/%s)\n", item.name, fallbackText(item.description, "暂无简介"), item.name, item.id)
		}
		if strings.TrimSpace(groupDescription) != "" {
			fmt.Fprintf(&b, "\n# 专家团说明\n\n%s\n", strings.TrimSpace(groupDescription))
		}
	} else {
		b.WriteString("# 成员职责\n\n")
		b.WriteString(strings.TrimSpace(prompt))
		b.WriteString("\n\n完成工作后请清晰报告结果、验证与遗留风险；不要 mention 其他成员，结果会自动交回团长。\n")
	}
	b.WriteString("\n# 目录约定\n\n")
	fmt.Fprintf(&b, "- 任务专属目录：%s\n- 当前成员产出目录：%s\n- 用户附件目录：%s\n", taskDir, memberDir, filepath.Join(taskDir, "attachments"))
	for index, path := range workDirs {
		label := "子项目目录"
		if index == 0 {
			label = "主项目目录"
		}
		fmt.Fprintf(&b, "- %s：%s\n", label, path)
	}
	// 产出目录约定：客户端只同步任务专属目录（task_dir）内的文件，写到项目工作
	// 目录的产出既不会上报到线上，客户端文件树里也看不到。
	b.WriteString("- 需求中没有明确指定存放位置的产出文档，一律写入当前成员产出目录（位于任务专属目录内）；不要写入项目工作目录或其他位置。\n")
	if strings.TrimSpace(handoff) != "" {
		b.WriteString("\n# 本轮消息或交接内容\n\n")
		b.WriteString(strings.TrimSpace(handoff))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func expertRoleLabel(role string) string {
	if role == "leader" {
		return "团长"
	}
	return "成员"
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
