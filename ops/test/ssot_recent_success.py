"""Parse recent successful usage rows for SSOT gate skip keys."""
from __future__ import annotations

from pathlib import Path


def billing_mode_to_modality(billing_mode: str, model: str) -> str:
    mode = (billing_mode or "token").strip().lower()
    if mode == "image":
        return "image"
    if mode == "video":
        return "video"
    model_l = model.lower()
    if "embedding" in model_l:
        return "embeddings"
    return "text"


def parse_recent_success_tsv(text: str, *, min_count: int = 1) -> set[tuple[str, str]]:
    """Return (model, modality) keys safe to skip live SSOT probes."""
    keys: set[tuple[str, str]] = set()
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split("\t")
        if len(parts) < 2:
            continue
        model = parts[0].strip()
        if not model:
            continue
        if len(parts) >= 3 and parts[1].strip() in {"text", "image", "video", "embeddings"}:
            modality = parts[1].strip()
            count_s = parts[2].strip()
        else:
            modality = billing_mode_to_modality(parts[1].strip(), model)
            count_s = parts[2].strip() if len(parts) >= 3 else parts[1].strip()
        try:
            count = int(float(count_s))
        except ValueError:
            continue
        if count < min_count:
            continue
        keys.add((model, modality))
    return keys


def load_skip_keys(path: str | Path, *, min_count: int = 1) -> set[tuple[str, str]]:
    text = Path(path).read_text(encoding="utf-8")
    return parse_recent_success_tsv(text, min_count=min_count)
