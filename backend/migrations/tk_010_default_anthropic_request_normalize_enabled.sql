-- TokenKey: default tk_anthropic_request_normalize_enabled to 'true'.
--
-- Enables gateway-side normalization of common client mistakes on the Anthropic
-- native /v1/messages path:
--   1) tool_choice given as an OpenAI-style string ("auto" / "required" /
--      "none") is rewritten into Anthropic's required object form
--      ({"type":"auto"} / {"type":"any"} / {"type":"none"}). Unknown strings
--      are left untouched so the upstream still surfaces the client bug.
--   2) When the client enables thinking.type=enabled together with a
--      tool_choice that forces tool use (type IN ("any","tool")), Anthropic
--      rejects the request with 400 "Thinking may not be enabled when
--      tool_choice forces tool use." TokenKey strips the thinking field to
--      preserve the client's explicit forced-tool-use intent.
--
-- Operators can disable the behavior at any time via the admin settings UI or:
--   UPDATE settings SET value='false'
--    WHERE key='tk_anthropic_request_normalize_enabled';
--
-- Idempotent: ON CONFLICT DO NOTHING — never clobbers an operator override.

INSERT INTO settings (key, value)
VALUES ('tk_anthropic_request_normalize_enabled', 'true')
ON CONFLICT (key) DO NOTHING;
