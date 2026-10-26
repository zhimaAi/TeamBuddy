package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildTaskPrompt builds the complete prompt for one CLI execution round.
// Product history deliberately stays in the database and is not
// injected; the CLI receives only the immutable step instruction, the current
// question, and the task's directory contract.
func BuildTaskPrompt(ctx context.Context, db *sql.DB, taskUUID, stepUUID, question string) (string, error) {
	var title, taskDir, taskMDPath, currentName, currentPrompt, currentStepDir string
	err := db.QueryRowContext(ctx, `SELECT t.title, t.task_dir, t.task_md_path, s.name, s.prompt_snapshot, s.step_dir
		FROM gt_tasks t JOIN gt_task_steps s ON s.task_uuid = t.uuid
		WHERE t.uuid = ? AND s.uuid = ?`, taskUUID, stepUUID).Scan(
		&title, &taskDir, &taskMDPath, &currentName, &currentPrompt, &currentStepDir)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("任务或 Agent 编排不存在")
	}
	if err != nil {
		return "", fmt.Errorf("读取任务执行上下文失败: %w", err)
	}

	workRows, err := db.QueryContext(ctx, `SELECT path FROM gt_task_work_dirs WHERE task_uuid = ? ORDER BY sort_order, id`, taskUUID)
	if err != nil {
		return "", fmt.Errorf("读取项目目录失败: %w", err)
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
	if err := workRows.Err(); err != nil {
		return "", err
	}

	stepRows, err := db.QueryContext(ctx, `SELECT s.uuid, s.name, s.step_dir,
		EXISTS(SELECT 1 FROM gt_cli_sessions cs WHERE cs.task_uuid=s.task_uuid AND cs.step_uuid=s.uuid)
		FROM gt_task_steps s WHERE s.task_uuid = ? ORDER BY s.step_order, s.rowid`, taskUUID)
	if err != nil {
		return "", fmt.Errorf("读取 Agent 编排目录失败: %w", err)
	}
	defer stepRows.Close()
	type stepDirItem struct {
		id, name, path       string
		executed, hasOutputs bool
	}
	stepDirs := make([]stepDirItem, 0)
	for stepRows.Next() {
		var item stepDirItem
		if err := stepRows.Scan(&item.id, &item.name, &item.path, &item.executed); err != nil {
			return "", err
		}
		if entries, err := os.ReadDir(item.path); err == nil {
			item.hasOutputs = len(entries) > 0
		}
		stepDirs = append(stepDirs, item)
	}
	if err := stepRows.Err(); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "你正在执行本地任务「%s」的 Agent 编排步骤「%s」。\n\n", title, currentName)
	b.WriteString("用户可以直接在任意步骤对话。步骤排序不代表执行事实，也不代表前序步骤已完成。\n\n")
	b.WriteString("## 执行前必读\n\n")
	fmt.Fprintf(&b, "1. 首先读取任务原始需求文件：%s。\n", taskMDPath)
	b.WriteString("2. 根据本次问题，读取用户明确引用的文档，以及其他步骤目录中已存在的相关产出。其他步骤的产出只读，排序靠后的已有产出同样可以参考。\n")
	b.WriteString("3. 尚未执行或没有产出的步骤不构成启动前置条件。材料不存在时如实说明，不编造产出，不要求先完成前序步骤；仅在具体工作确实缺少必要信息时说明所需补充。\n\n")

	b.WriteString("## 本步骤要求\n\n")
	b.WriteString(strings.TrimSpace(currentPrompt))
	b.WriteString("\n\n## 任务数据目录\n\n")
	fmt.Fprintf(&b, "- 任务专属目录：%s\n- 需求文件：%s\n- 用户附件目录：%s\n", taskDir, taskMDPath, filepath.Join(taskDir, "attachments"))
	b.WriteString("- task.md 中的 attachments/... 是相对任务专属目录的附件引用；请按它在需求中出现的顺序和上下文读取。\n")
	b.WriteString("- Agent 编排目录（按执行顺序）：\n")
	for _, item := range stepDirs {
		class := "尚未执行，暂无产出"
		if item.id == stepUUID {
			class = "本次对话步骤"
		} else if item.hasOutputs {
			class = "已有产出，可只读参考"
		} else if item.executed {
			class = "已有执行记录，暂无产出"
		}
		fmt.Fprintf(&b, "  - %s（%s）：%s\n", item.name, class, item.path)
	}
	b.WriteString("\n## 项目目录\n\n")
	for i, path := range workDirs {
		label := "子项目目录"
		if i == 0 {
			label = "主项目目录"
		}
		fmt.Fprintf(&b, "- %s：%s\n", label, path)
	}
	fmt.Fprintf(&b, "\n所有新产生的中间产物只能写入当前步骤专属目录：%s。其他步骤目录只读，仅参考已有的相关产出，不得写入或修改；不要依据步骤排序推断已执行或已完成。\n", currentStepDir)
	if strings.TrimSpace(question) != "" {
		b.WriteString("\n## 用户本次问题\n\n")
		b.WriteString(strings.TrimSpace(question))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// BuildRoundPrompt selects the prompt builder for the task's execution mode.
// Pipeline and Vibe Coding tasks carry Agent orchestration steps; expert group
// tasks carry a frozen roster; CLI direct execution carries neither.
func BuildRoundPrompt(ctx context.Context, db *sql.DB, executionMode, taskUUID, stepUUID, question string) (string, error) {
	switch executionMode {
	case "expert_group":
		return BuildExpertTaskPrompt(ctx, db, taskUUID, stepUUID, question)
	case "cli":
		return BuildCLIPrompt(ctx, db, taskUUID, question)
	default:
		return BuildTaskPrompt(ctx, db, taskUUID, stepUUID, question)
	}
}

// BuildCLIPrompt builds the prompt for one direct-CLI execution round. The mode
// skips both the pipeline and the Agent orchestration, so the CLI receives only
// the task document contract, the project directories and the user's own
// instruction — without step instructions, previous step outputs or step output
// directories.
func BuildCLIPrompt(ctx context.Context, db *sql.DB, taskUUID, question string) (string, error) {
	var title, taskDir, taskMDPath string
	err := db.QueryRowContext(ctx, `SELECT title, task_dir, task_md_path FROM gt_tasks WHERE uuid = ?`,
		taskUUID).Scan(&title, &taskDir, &taskMDPath)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("任务不存在")
	}
	if err != nil {
		return "", fmt.Errorf("读取任务执行上下文失败: %w", err)
	}

	workRows, err := db.QueryContext(ctx, `SELECT path FROM gt_task_work_dirs WHERE task_uuid = ? ORDER BY sort_order, id`, taskUUID)
	if err != nil {
		return "", fmt.Errorf("读取项目目录失败: %w", err)
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
	if err := workRows.Err(); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "你正在直接执行本地任务「%s」。\n\n", title)
	b.WriteString("## 任务数据目录\n\n")
	fmt.Fprintf(&b, "- 任务专属目录：%s\n- 需求文件：%s\n- 用户附件目录：%s\n", taskDir, taskMDPath, filepath.Join(taskDir, "attachments"))
	b.WriteString("- task.md 中的 attachments/... 是相对任务专属目录的附件引用；请按它在需求中出现的顺序和上下文读取。\n")
	b.WriteString("\n## 项目目录\n\n")
	for i, path := range workDirs {
		label := "子项目目录"
		if i == 0 {
			label = "主项目目录"
		}
		fmt.Fprintf(&b, "- %s：%s\n", label, path)
	}
	// 产出目录约定：客户端只同步任务专属目录（task_dir）内的文件，写到项目工作
	// 目录的产出既不会上报到线上，客户端文件树里也看不到。
	if strings.TrimSpace(taskDir) != "" {
		b.WriteString("\n## 产出目录（必须遵守）\n\n")
		fmt.Fprintf(&b, "- 本次任务产生的产出文档，凡需求中没有明确指定存放位置的，一律写入任务产出目录：%s，可在其中按主题自行建立子目录。\n", taskDir)
		b.WriteString("- 不要写入项目工作目录或其他位置：客户端只同步任务产出目录内的文件，写到别处的产出不会上报，客户端也看不到。\n")
	}
	if strings.TrimSpace(question) != "" {
		b.WriteString("\n## 用户本次指令\n\n")
		b.WriteString(strings.TrimSpace(question))
		b.WriteByte('\n')
	}
	return b.String(), nil
}
