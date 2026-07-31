-- Remove the retired global pricing settings layer. Pricing is embedded from
-- tk_pricing_overlay.json; urgent runtime corrections are scoped channel rows.
-- bluegreen-safe-destructive-ok: the new binary never reads this setting; an
-- older binary sharing the database safely falls back to its embedded registry.
DELETE FROM settings
WHERE key = 'tk_pricing_overlay_runtime';
