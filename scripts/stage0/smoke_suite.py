"""Gateway smoke suite gating — mirrors ops/stage0/smoke_lib.sh."""
from __future__ import annotations

from typing import Final

_SUITE_ALIASES: Final[dict[str, str]] = {
    "full": "full",
    "prod": "full",
    "main-via-edge": "main-via-edge",
    "edge-via-prod": "main-via-edge",
    "quick": "quick",
    "minimal": "quick",
}

_SECTIONS: Final[dict[str, frozenset[str]]] = {
    "full": frozenset(
        {"public", "frontend", "models", "chat", "messages", "gemini", "openai_oauth"}
    ),
    "main-via-edge": frozenset({"public", "models", "messages"}),
    "quick": frozenset({"public", "models", "chat"}),
}


def normalize_suite(raw: str) -> str:
    key = (raw or "full").strip()
    try:
        return _SUITE_ALIASES[key]
    except KeyError as exc:
        raise ValueError(
            f"unknown GATEWAY_SMOKE_SUITE={raw!r} (want full|main-via-edge|quick)"
        ) from exc


def suite_runs(section: str, suite: str = "full") -> bool:
    normalized = normalize_suite(suite)
    return section in _SECTIONS[normalized]


def edge_phase_gateway_suite(phase: str) -> str | None:
    """Gateway suite used by legacy run_main_via_edge_smoke (optional prod relay)."""
    if phase == "main-via-edge":
        return "main-via-edge"
    return None


def edge_phase_runs_native_oauth(phase: str) -> bool:
    return phase in {"edge-native-oauth", "full"}


def needs_chat_model(phase: str, self_mode: str) -> bool:
    """Edge deploy no longer exports chat models for formulaic post_deploy smoke."""
    return False


def pick_model(models: list[dict], override: str | None = None) -> tuple[str, str | None]:
    """Return (model_id, warning). Prefer claude id, else first; honor override when listed."""
    ids = [str(m.get("id") or "") for m in models if m.get("id")]
    if not ids:
        raise ValueError("no model id in /v1/models")

    auto = next((i for i in ids if "claude" in i.lower()), ids[0])
    if override:
        if override in ids:
            return override, None
        return auto, (
            f"configured chat model {override!r} not listed for this key; "
            f"using auto-selected model={auto}"
        )
    return auto, None
