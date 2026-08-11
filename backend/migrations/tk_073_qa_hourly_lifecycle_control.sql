-- TK: QA UTC-hour lifecycle control fields (design-qa-phase2-archive-closeout.md).
-- bluegreen-safe-destructive-ok: additive nullable columns with stable defaults.
ALTER TABLE qa_archive_shards
    ADD COLUMN IF NOT EXISTS source_partition_name text NULL,
    ADD COLUMN IF NOT EXISTS source_dropped_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS hot_files_cleaned_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS hot_cleanup_error text NULL;
