-- +goose Up
-- 产出文档上报的本地状态表（独立通道，不复用 gt_cloud_sync_outbox）。
-- 以 (task_uuid, rel_path) 为主键：rel_path 相对 gt_tasks.task_dir、正斜杠归一，
-- 是文档在本地与云端的稳定身份；doc_key = sha1(rel_path)[:32] 供云端幂等。
-- content_hash 只在「上报成功」后写入，与 modified_at 一起构成上次上报水位。
CREATE TABLE IF NOT EXISTS gt_task_document_uploads (
    task_uuid        TEXT    NOT NULL,
    rel_path         TEXT    NOT NULL,
    doc_key          TEXT    NOT NULL,
    file_ext         TEXT    NOT NULL DEFAULT '',
    file_size        INTEGER NOT NULL DEFAULT 0,
    modified_at      INTEGER NOT NULL DEFAULT 0,
    content_hash     TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'pending',
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT    NOT NULL DEFAULT '',
    remote_doc_id    INTEGER NOT NULL DEFAULT 0,
    remote_revision  INTEGER NOT NULL DEFAULT 0,
    uploaded_at      INTEGER NOT NULL DEFAULT 0,
    updated_at       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (task_uuid, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_task_doc_uploads_status
    ON gt_task_document_uploads(status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_task_doc_uploads_task
    ON gt_task_document_uploads(task_uuid, status);
