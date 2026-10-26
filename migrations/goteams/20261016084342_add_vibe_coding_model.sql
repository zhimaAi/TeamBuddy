-- +goose Up
ALTER TABLE gt_tasks ADD COLUMN vibe_coding_model TEXT NOT NULL DEFAULT '';
