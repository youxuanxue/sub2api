ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS messages_compaction_enabled BOOLEAN;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS messages_compaction_input_tokens_threshold INTEGER;

COMMENT ON COLUMN groups.messages_compaction_enabled IS 'OpenAI /v1/messages 自动压缩开关；NULL 表示未配置';
COMMENT ON COLUMN groups.messages_compaction_input_tokens_threshold IS 'OpenAI /v1/messages 自动压缩输入 token 阈值；NULL 表示未配置';
