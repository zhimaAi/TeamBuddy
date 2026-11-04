package localserver

import (
	"context"
	"database/sql"
	"goteams-client/internal/executor"
)

// resolveRemoteThinking 保留开关等无关更新中的旧选择；新目标不能继承旧模型的强度。
func resolveRemoteThinking(ctx context.Context, db *sql.DB, channelType, cliType, model, workDir string, input executor.ThinkingLevelInput) (string, error) {
	var oldCLI, oldModel, oldWorkDir, oldLevel string
	err := db.QueryRowContext(ctx, `SELECT cli_type, cli_model, workspace, thinking_level FROM gt_remote_channel_cli_configs WHERE channel_type=?`, channelType).Scan(&oldCLI, &oldModel, &oldWorkDir, &oldLevel)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	level := executor.ResolveThinkingLevel(input, cliType, model, oldCLI, oldModel, oldLevel)
	if input.Present || level != oldLevel || cliType != oldCLI || model != oldModel || workDir != oldWorkDir {
		if err := executor.ValidateThinkingLevel(ctx, cliType, model, workDir, level); err != nil {
			return "", err
		}
	}
	return level, nil
}
