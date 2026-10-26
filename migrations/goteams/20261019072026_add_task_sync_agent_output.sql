-- +goose Up
-- 任务级「同步 Agent 产出」开关：0 表示该任务默认不把产出文件自动上报到线上，
-- 需要用户在文件抽屉里手动触发上报。历史任务统一按 1（自动上报）处理。
ALTER TABLE gt_tasks ADD COLUMN sync_agent_output INTEGER NOT NULL DEFAULT 1;
