"""Platform profiles for edge → prod relay onboarding.

Only ``antigravity`` is implemented from live create+probe evidence.
Other platforms are explicit placeholders until the same evidence loop lands.
"""

from __future__ import annotations

from copy import deepcopy
from typing import Any

# Production naming differs from edge_relay.DEFAULT_PROD_RELAY_NAMES ("ag-{edge}").
# Live stubs are antigravity-us3/us4/us5/us6 — keep that shape here.

PROFILES: dict[str, dict[str, Any]] = {
    "antigravity": {
        "status": "implemented",
        "pool_platform": "antigravity",
        "edge_group_name": "antigravity",
        "edge_group_platform": "antigravity",
        "placeholder_name": "ag-{edge_id}-placeholder",
        "placeholder_email": "ag-{edge_id}-placeholder@example.com",
        "placeholder_notes": (
            "ops copy-template; keep unschedulable until OAuth re-authorize completes"
        ),
        "relay_key_name": "relay-antigravity-{edge_id}",
        "relay_key_user_id": 1,
        "prod_stub_name": "antigravity-{edge_id}",
        # Resolve by name on prod; ids are fallbacks from live prod (2026-10).
        "prod_group_names": ["Google-Vertex", "Google-Antigravity"],
        "prod_group_ids_fallback": [16, 21],
        "prod_parity_stub_name": "antigravity-us6",
        "prod_concurrency": 40,
        "prod_priority": 1,
        "prod_pool_mode_retry_count": 3,
        "probe": {
            "model": "gemini-3-flash",
            "endpoint": "gemini",
            "max_tokens": 32,
        },
        "edge_capability": {
            # Shared capability row used by antigravity oauth accounts on uk edges.
            "required_protocols": ["gemini_generate_content"],
            "positive_verdict_protocol": "gemini_generate_content",
            "probe_evidence": {
                "verdicts": {"gemini_generate_content": "positive"},
                "official_seed": False,
                "identity_conflict": False,
                "initial_probe_completed": True,
            },
        },
        "ops_reauth": [
            "在目标 edge 后台打开占位/复制出的 oauth 账号 → 重新授权",
            "生成授权链接（每次新会话都要重新生成；链接不绑定 account_id，但 session/state 一次性）",
            "用目标 Google 账号完成授权（尽量走该 Edge 出口）",
            "保持弹窗打开，粘贴完整回调 URL → 完成授权",
            "确认正常后开启调度；prod 中继不用动",
        ],
    },
    "gemini-web": {
        "status": "placeholder",
        "pool_platform": "gemini",
        "notes": (
            "Pending live create+probe on a new edge. Likely edge group gemini-web + "
            "prod gemini-{edge} stub; do not invent mapping/capability without evidence."
        ),
    },
    "kiro": {
        "status": "placeholder",
        "pool_platform": "kiro",
        "notes": (
            "Pending live create+probe. Prod transport is anthropic+mirror_platform=kiro "
            "(see edge_relay.relay_transport_platform); evidence required before wiring."
        ),
    },
    "openai": {
        "status": "placeholder",
        "pool_platform": "openai",
        "notes": (
            "Pending live create+probe for Codex/OpenAI OAuth edge + prod openai-{edge} stub."
        ),
    },
    "anthropic": {
        "status": "placeholder",
        "pool_platform": "anthropic",
        "notes": (
            "Pending packaging of cc-{edge} full-chain onboard; cookie path stays in "
            "tokenkey-anthropic-oauth-cookie-edge + import-accounts edge_oauth_relay."
        ),
    },
}


def list_platforms() -> list[dict[str, str]]:
    out: list[dict[str, str]] = []
    for name, profile in sorted(PROFILES.items()):
        out.append(
            {
                "platform": name,
                "status": str(profile.get("status") or "unknown"),
                "pool_platform": str(profile.get("pool_platform") or ""),
                "notes": str(profile.get("notes") or ""),
            }
        )
    return out


def get_profile(platform: str) -> dict[str, Any]:
    key = str(platform or "").strip().lower()
    if key not in PROFILES:
        known = ", ".join(sorted(PROFILES))
        raise ValueError(f"unknown platform {platform!r}; known: {known}")
    return deepcopy(PROFILES[key])


def require_implemented(platform: str) -> dict[str, Any]:
    profile = get_profile(platform)
    if profile.get("status") != "implemented":
        raise ValueError(
            f"platform {platform!r} is a placeholder "
            f"(status={profile.get('status')!r}). "
            "Implement after live create+probe evidence; see profile notes."
        )
    return profile


def format_name(template: str, *, edge_id: str) -> str:
    return str(template).format(edge_id=str(edge_id).strip().lower())
