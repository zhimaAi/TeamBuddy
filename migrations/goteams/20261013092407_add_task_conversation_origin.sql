-- +goose Up
ALTER TABLE gt_tasks ADD COLUMN created_from_conversation INTEGER NOT NULL DEFAULT 0;
