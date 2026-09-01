"""Pure probe parsing and command selection for ``modelops.py``.

No network, database, settings, or repository writes live here. Probe-family selection
is derived only from a caller-supplied live account snapshot; account ids and names are
not capability metadata.
"""
from __future__ import annotations

import collections
import re
import shlex
from pathlib import Path
from typing import Any


class ProbeAggregate:
    __slots__ = ("platform", "model_id", "verdicts", "codes", "variants")

    def __init__(self, platform: str, model_id: str) -> None:
        self.platform = platform
        self.model_id = model_id
        self.verdicts: collections.Counter[str] = collections.Counter()
        self.codes: collections.Counter[str] = collections.Counter()
        self.variants: list[str] = []

    def add(self, code: str, verdict: str, variant: str | None = None) -> None:
        self.verdicts[verdict] += 1
        self.codes[code] += 1
        if variant:
            self.variants.append(variant)

    @property
    def status(self) -> str:
        if self.verdicts["servable"]:
            return "servable"
        if self.verdicts["not_allowlisted"]:
            return "mapping_gap"
        if self.verdicts["auth_error"] or self.verdicts["config_error"]:
            return "probe_error"
        if self.verdicts["inconclusive"]:
            return "inconclusive"
        if self.verdicts["unsupported"]:
            return "unsupported"
        return "unknown"


class AccountSnapshot:
    __slots__ = ("account_id", "name", "platform", "channel_type", "base_url", "model_mapping")

    def __init__(
        self,
        account_id: str,
        name: str | None = None,
        platform: str | None = None,
        channel_type: int | None = None,
        model_mapping: dict[str, str] | None = None,
        base_url: str | None = None,
    ) -> None:
        self.account_id = account_id
        self.name = name
        self.platform = platform
        self.channel_type = channel_type
        self.base_url = (base_url or "").strip().lower().rstrip("/")
        self.model_mapping = model_mapping or {}


_VARIANT_RE = re.compile(r"^(?P<model>.+?)\s+\((?P<variant>thinking|nonthinking)\)$")


def normalize_probe_model(raw: str) -> tuple[str, str | None]:
    raw = raw.strip()
    match = _VARIANT_RE.match(raw)
    if match:
        return match.group("model").strip(), match.group("variant")
    return raw, None


def load_probe_results(paths: list[Path]) -> dict[str, ProbeAggregate]:
    out: dict[str, ProbeAggregate] = {}
    for path in paths:
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != 4:
                raise SystemExit(f"{path}:{lineno}: expected 4 TSV columns, got {len(parts)}")
            platform, raw_model, code, verdict = (part.strip() for part in parts)
            model, variant = normalize_probe_model(raw_model)
            if not model:
                continue
            aggregate = out.setdefault(model, ProbeAggregate(platform=platform, model_id=model))
            aggregate.add(code, verdict, variant)
    return out


def infer_mode(model_id: str, overlay: dict[str, dict[str, Any]]) -> str:
    mode = overlay.get(model_id, {}).get("mode")
    if mode == "image_generation":
        return "image"
    if mode == "video_generation":
        return "video"
    lower = model_id.lower()
    if "seedream" in lower or "image" in lower or "imagen" in lower:
        return "image"
    if "seedance" in lower or "video" in lower or "veo" in lower:
        return "video"
    return "chat"


def probe_env_name(
    account_id: str,
    model_id: str,
    overlay: dict[str, dict[str, Any]],
    snapshot: AccountSnapshot | None = None,
) -> str | None:
    del account_id  # IDs are identifiers only, never probe-family metadata.
    if snapshot is None or snapshot.channel_type is None:
        return None
    channel_type = snapshot.channel_type
    base_url = (snapshot.base_url or "").strip().lower().rstrip("/")
    if channel_type == 17:
        return "DASHSCOPE_CHAT_MODELS"
    if channel_type == 26:
        return None
    if channel_type == 45:
        if base_url == "https://ark.cn-beijing.volces.com/api/plan/v3":
            return "VOLCENGINE_AGENT_PLAN_MODELS"
        mode = infer_mode(model_id, overlay)
        if mode == "image":
            return "ARK_IMAGE_MODELS"
        if mode == "video":
            return "ARK_VIDEO_MODELS"
        return "ARK_CHAT_MODELS"
    return None


def run_probe_command(env_name: str, models: list[str]) -> str:
    if env_name == "VOLCENGINE_AGENT_PLAN_MODELS":
        model_value = " ".join(models)
        return (
            "bash ops/observability/run-probe.sh --target prod "
            "--script ops/pricing/probe-volcengine-agent-plan-models.sh "
            f"--env {shlex.quote('AGENT_PLAN_CHAT_MODELS=' + model_value)} "
            f"--env {shlex.quote('AGENT_PLAN_RESPONSES_MODELS=' + model_value)} "
            "--timeout-seconds 600"
        )
    env_value = f"{env_name}={' '.join(models)}"
    return (
        "bash ops/observability/run-probe.sh --target prod "
        "--script ops/pricing/probe-servable-models.sh "
        "--with ops/pricing/probe_reserved_resources.sh "
        f"--env {shlex.quote(env_value)} --timeout-seconds 300"
    )
