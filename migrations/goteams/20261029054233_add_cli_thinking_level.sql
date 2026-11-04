-- +goose Up
ALTER TABLE gt_pipeline_steps ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_cloud_pipeline_step_execution ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_expert_group_members ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_task_steps ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_cli_sessions ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_cli_sessions ADD COLUMN thinking_level_sent TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_cli_sessions ADD COLUMN thinking_resolution TEXT NOT NULL DEFAULT 'inherit';
ALTER TABLE gt_remote_channel_cli_configs ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_task_execution_history ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
