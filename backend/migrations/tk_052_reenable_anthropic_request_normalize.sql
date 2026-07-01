-- TokenKey: re-enable Anthropic request normalize where an operator explicitly
-- set tk_anthropic_request_normalize_enabled=false. Code + tk_010 default is
-- true; leaving false on prod disables tool_choice/thinking/geo fixes.

UPDATE settings
SET value = 'true'
WHERE key = 'tk_anthropic_request_normalize_enabled'
  AND lower(trim(value)) = 'false';

INSERT INTO settings (key, value)
VALUES ('tk_anthropic_request_normalize_enabled', 'true')
ON CONFLICT (key) DO NOTHING;
