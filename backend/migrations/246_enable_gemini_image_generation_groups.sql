-- TokenKey: do not rewrite live group image policy.
--
-- Upstream sets allow_image_generation=true on every gemini/antigravity group
-- that is still false, so the new Gemini image gate does not 400 traffic that
-- already works. On TokenKey the column default is false while native Gemini
-- image models are already served, and the same flag already blocks OpenAI
-- image calls. Forcing it on would open those calls. The gate stays off for
-- this merge (tkGeminiImageGroupSwitchRetroactive). This statement records the
-- migration without touching rows.

SELECT 1;
