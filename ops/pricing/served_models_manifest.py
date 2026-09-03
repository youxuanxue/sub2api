#!/usr/bin/env python3
"""Parse and validate TokenKey's curated newapi served-model intent."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any


REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST_PATH = (
    REPO_ROOT / "backend" / "internal" / "service" / "tk_served_models.json"
)
SCHEMA_VERSION = 3
ENTRY_FIELDS = {"channel_type", "scopes", "price_owner", "display"}
SCOPE_FIELDS = {"channel_type", "base_url"}
ALLOWED_SCOPES = {
    (45, "https://ark.cn-beijing.volces.com/api/plan/v3"),
    (46, "https://qianfan.baidubce.com"),
    (54, "https://api.xrtoken.net"),
}
MODEL_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


class ManifestError(ValueError):
    def __init__(self, errors: list[str]) -> None:
        self.errors = tuple(errors)
        super().__init__("; ".join(errors))


class ManifestEntry:
    __slots__ = ("model_id", "channel_type", "scopes", "price_owner", "display")

    def __init__(
        self,
        model_id: str,
        channel_type: int | None,
        scopes: tuple[dict[str, Any], ...],
        price_owner: str,
        display: bool,
    ) -> None:
        self.model_id = model_id
        self.channel_type = channel_type
        self.scopes = scopes
        self.price_owner = price_owner
        self.display = display

    def projection(self) -> dict[str, Any]:
        return {
            "model_id": self.model_id,
            "channel_type": self.channel_type,
            "scopes": [dict(scope) for scope in self.scopes],
            "price_owner": self.price_owner,
            "display": self.display,
        }


class ServedModelsManifest:
    __slots__ = ("entries",)

    def __init__(self, entries: tuple[ManifestEntry, ...]) -> None:
        self.entries = entries

    def by_model(self) -> dict[str, ManifestEntry]:
        return {entry.model_id: entry for entry in self.entries}

    def model_ids(self) -> set[str]:
        return {entry.model_id for entry in self.entries}

    def displayed_model_ids(self) -> set[str]:
        return {entry.model_id for entry in self.entries if entry.display}


def _scope_errors(label: str, scope: Any) -> list[str]:
    if not isinstance(scope, dict):
        return [f"{label} must be an object"]
    if set(scope) != SCOPE_FIELDS:
        return [f"{label} must contain exactly: " + ", ".join(sorted(SCOPE_FIELDS))]
    channel_type = scope.get("channel_type")
    base_url = scope.get("base_url")
    if (
        not isinstance(channel_type, int)
        or isinstance(channel_type, bool)
        or not isinstance(base_url, str)
        or (channel_type, base_url) not in ALLOWED_SCOPES
    ):
        return [f"{label} is not a supported normalized newapi property scope"]
    return []


def parse_manifest_document(
    data: Any, *, source: str = "tk_served_models.json"
) -> ServedModelsManifest:
    errors: list[str] = []
    if not isinstance(data, dict):
        raise ManifestError([f"{source}: manifest must be a JSON object"])
    if data.get("schema_version") != SCHEMA_VERSION:
        errors.append(
            f"{source}: schema_version must be {SCHEMA_VERSION}, "
            f"got {data.get('schema_version')!r}"
        )
    if set(data) != {"schema_version", "entries"}:
        errors.append(
            f"{source}: top level must contain exactly schema_version and entries"
        )

    raw_entries = data.get("entries")
    if not isinstance(raw_entries, dict) or not raw_entries:
        errors.append(f"{source}: entries must be a non-empty object")
        raise ManifestError(errors)

    entries: list[ManifestEntry] = []
    for model_id, raw in raw_entries.items():
        label = f"{source}: manifest {model_id!r}"
        entry_errors: list[str] = []
        if not isinstance(model_id, str) or MODEL_ID_RE.fullmatch(model_id) is None:
            errors.append(f"{label}: invalid model id key")
            continue
        if not isinstance(raw, dict):
            errors.append(f"{label}: entry must be an object")
            continue

        unknown = sorted(set(raw) - ENTRY_FIELDS)
        if unknown:
            entry_errors.append(f"{label}: unknown fields: " + ", ".join(unknown))
        display = raw.get("display")
        if not isinstance(display, bool):
            entry_errors.append(f"{label}: display must be a bool")

        channel_type = raw.get("channel_type")
        if channel_type is not None and (
            not isinstance(channel_type, int)
            or isinstance(channel_type, bool)
            or channel_type <= 0
        ):
            entry_errors.append(f"{label}: channel_type must be a positive integer")

        raw_scopes = raw.get("scopes", [])
        scopes: list[dict[str, Any]] = []
        if not isinstance(raw_scopes, list):
            entry_errors.append(f"{label}: scopes must be a list")
        else:
            for index, scope in enumerate(raw_scopes):
                scope_errors = _scope_errors(f"{label} scopes[{index}]", scope)
                entry_errors.extend(scope_errors)
                if not scope_errors:
                    scopes.append(dict(scope))
            scope_keys = [
                (scope["channel_type"], scope["base_url"]) for scope in scopes
            ]
            if len(scope_keys) != len(set(scope_keys)):
                entry_errors.append(f"{label}: duplicate property scope")
        if channel_type is None and not raw_scopes:
            entry_errors.append(
                f"{label}: at least one channel_type or property scope is required"
            )

        price_owner = raw.get("price_owner", model_id)
        if not isinstance(price_owner, str) or not price_owner:
            entry_errors.append(f"{label}: price_owner must be a non-empty string")

        if entry_errors:
            errors.extend(entry_errors)
            continue
        entries.append(
            ManifestEntry(
                model_id,
                channel_type,
                tuple(scopes),
                price_owner,
                display,
            )
        )

    if errors:
        raise ManifestError(errors)
    return ServedModelsManifest(tuple(entries))


def parse_manifest_text(
    text: str, *, source: str = "tk_served_models.json"
) -> ServedModelsManifest:
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ManifestError([f"{source}: invalid JSON: {exc}"]) from exc
    return parse_manifest_document(data, source=source)


def load_manifest(path: Path = DEFAULT_MANIFEST_PATH) -> ServedModelsManifest:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ManifestError([f"{path}: cannot read manifest: {exc}"]) from exc
    return parse_manifest_text(text, source=str(path))
