"""Shared read-only helpers for validating the TokenKey pricing registry."""

from __future__ import annotations

import math
from typing import Any


# A mode may have more than one valid billable shape (currently image models).
MODE_FIELDS: dict[str, tuple[tuple[str, ...], ...]] = {
    "audio_speech": (("input_cost_per_token", "output_cost_per_token"),),
    "audio_transcription": (("input_cost_per_token", "output_cost_per_token"),),
    "completion": (("input_cost_per_token", "output_cost_per_token"),),
    "embedding": (("input_cost_per_token",),),
    "image_generation": (("output_cost_per_image",), ("output_cost_per_image_token",)),
    "realtime": (("input_cost_per_token", "output_cost_per_token"),),
    "responses": (("input_cost_per_token", "output_cost_per_token"),),
    "tts": (("output_cost_per_character",),),
    "video_generation": (("output_cost_per_second",),),
    "chat": (("input_cost_per_token", "output_cost_per_token"),),
}


def is_positive_number(value: Any) -> bool:
    return (
        isinstance(value, (int, float))
        and not isinstance(value, bool)
        and math.isfinite(value)
        and value > 0
    )


def has_complete_price(entry: Any) -> bool:
    """Return whether an entry has one complete, positive billable shape."""
    if not isinstance(entry, dict):
        return False
    alternatives = MODE_FIELDS.get(str(entry.get("mode")), ())
    return any(all(is_positive_number(entry.get(field)) for field in fields) for fields in alternatives)


def resolve_price_owner(model_id: str, overlay: dict[str, Any]) -> str:
    """Resolve one registry alias hop, leaving unknown ids unchanged."""
    aliases = overlay.get("_aliases")
    owner = aliases.get(model_id) if isinstance(aliases, dict) else None
    return owner if isinstance(owner, str) and owner else model_id
