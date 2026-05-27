-- Migration: tk_011_seed_claude_code_template_2_1_152
-- Follow-up to PR #423 / migration-129: seed a cc 2.1.152-aligned channel monitor template.
-- Does NOT update the legacy 「Claude Code 伪装」 row (still 2.1.114) so operator edits are preserved.
-- Operators on the old template should switch to this seed or bump headers manually.

INSERT INTO channel_monitor_request_templates (
    name, provider, description, extra_headers, body_override_mode, body_override
)
VALUES (
    'Claude Code 伪装 2.1.152',
    'anthropic',
    '完整模拟 Claude Code 2.1.152 客户端：UA + anthropic-beta + system + metadata.user_id 对齐 cc 2.1.152 抓包（无 oauth beta，适用于 API-key 渠道监控）。',
    '{
        "User-Agent": "claude-cli/2.1.152 (external, sdk-cli)",
        "X-App": "cli",
        "anthropic-version": "2023-06-01",
        "anthropic-beta": "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07",
        "anthropic-dangerous-direct-browser-access": "true"
    }'::jsonb,
    'merge',
    '{
        "system": [
            {
                "type": "text",
                "text": "You are Claude Code, Anthropic''s official CLI for Claude."
            }
        ],
        "metadata": {
            "user_id": "user_0000000000000000000000000000000000000000000000000000000000000000_account_00000000-0000-0000-0000-000000000000_session_00000000-0000-0000-0000-000000000000"
        }
    }'::jsonb
)
ON CONFLICT (provider, name) DO NOTHING;
