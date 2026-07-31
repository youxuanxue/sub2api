# Model Pricing Registry

Runtime pricing is owned by [`backend/internal/service/tk_pricing_overlay.json`](../../internal/service/tk_pricing_overlay.json).
This directory is intentionally empty of runtime price snapshots. Provider pages,
offline imports, and channel configuration are inputs or scoped overrides; they
must not become an implicit second owner.

The former `model_prices_and_context_window.json` mirror has been removed. Do not
recreate it or add another local/provider snapshot here: use the registry owner
and, when an immediate per-channel correction is required, a scoped
`channel_model_pricing` override.
