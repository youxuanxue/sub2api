#!/usr/bin/env python3
"""Deterministic evidence engine for the real Kiro CLI wire identity.

Input is sanitized JSONL emitted by ``mitm_kiro_http_logger.py`` while the real
``kiro-cli`` performs the same minimal operation multiple times. Repository
constants and synthetic probes are never accepted as observed evidence.

Exit codes: 0 complete/aligned, 1 committed baseline drift, 2 invalid evidence,
3 incomplete/NOT_OBSERVED.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SCHEMA_VERSION = 3
REPO_ROOT = Path(__file__).resolve().parents[2]
KIRO_TLS_PROFILE_JSON = REPO_ROOT / "deploy/aws/stage0/tk_canonical_kiro_cli.json"
KIRO_PROFILE_NAME = "tk_canonical_kiro_cli"
DEFAULT_AUTH_CACHE = Path.home() / ".aws/sso/cache/kiro-auth-token.json"
NOT_OBSERVED = "NOT_OBSERVED"
EXIT_COMPLETE, EXIT_DRIFT, EXIT_INVALID, EXIT_INCOMPLETE = 0, 1, 2, 3
GREASE_VALUES = frozenset(range(0x0A0A, 0xFAFA + 1, 0x1010))
RUNTIME_PROFILE_FIELDS = (
    "enable_grease",
    "shuffle_extensions",
    "cipher_suites",
    "curves",
    "point_formats",
    "signature_algorithms",
    "alpn_protocols",
    "supported_versions",
    "key_share_groups",
    "psk_modes",
    "extensions",
)
_SECRET_KEYS = {
    "authorization", "cookie", "profilearn", "accesstoken", "refreshtoken",
    "clientsecret", "clientid", "token", "idtoken", "sessiontoken",
    "x-amz-security-token", "body", "response_body", "body_snippet",
}
_SECRET_VALUE_PATTERNS = (
    re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+"),
    re.compile(r"arn:aws:"),
    re.compile(r"(?i)(access|refresh|session)[_-]?token"),
)


@dataclass(frozen=True)
class EvidenceLane:
    name: str
    observed: dict[str, Any]
    source: str
    valid: bool
    error: str = ""


def _strip_grease(values: list[int]) -> list[int]:
    return [value for value in values if value not in GREASE_VALUES]


def compute_ja3(version: int, ciphers: list[int], extensions: list[int], curves: list[int], points: list[int]) -> tuple[str, str]:
    join = lambda values: "-".join(str(value) for value in values)
    raw = ",".join((str(version), join(_strip_grease(ciphers)), join(_strip_grease(extensions)), join(_strip_grease(curves)), join(points)))
    return raw, hashlib.md5(raw.encode("ascii")).hexdigest()


def _read_jsonl(path: Path | None) -> tuple[list[dict[str, Any]], str]:
    if path is None or not path.is_file():
        return [], ""
    records: list[dict[str, Any]] = []
    try:
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            value = json.loads(line)
            if not isinstance(value, dict):
                return [], f"line {number} is not an object"
            records.append(value)
    except (OSError, json.JSONDecodeError) as exc:
        return [], str(exc)
    return records, ""


def _secret_errors(value: Any, path: str = "$") -> list[str]:
    errors: list[str] = []
    if isinstance(value, dict):
        for key, child in value.items():
            if str(key).lower() in _SECRET_KEYS:
                errors.append(f"{path}.{key}: forbidden field")
            errors.extend(_secret_errors(child, f"{path}.{key}"))
    elif isinstance(value, list):
        for index, child in enumerate(value):
            errors.extend(_secret_errors(child, f"{path}[{index}]"))
    elif isinstance(value, str):
        if any(pattern.search(value) for pattern in _SECRET_VALUE_PATTERNS):
            errors.append(f"{path}: secret-like value")
    return errors


def _int_list(record: dict[str, Any], field: str) -> list[int]:
    value = record.get(field)
    if not isinstance(value, list) or any(type(item) is not int for item in value):
        raise ValueError(f"{field} must be an integer array")
    return value


def _tls_semantics(record: dict[str, Any]) -> dict[str, Any]:
    return {
        "enable_grease": any(value in GREASE_VALUES for field in ("cipher_suites", "extensions", "curves") for value in _int_list(record, field)),
        "cipher_suites": _strip_grease(_int_list(record, "cipher_suites")),
        "curves": _strip_grease(_int_list(record, "curves")),
        "point_formats": _int_list(record, "point_formats"),
        "signature_algorithms": _int_list(record, "signature_algorithms"),
        "alpn_protocols": record.get("alpn_protocols", []),
        "supported_versions": _strip_grease(_int_list(record, "supported_versions")),
        "key_share_groups": _strip_grease(_int_list(record, "key_share_groups")),
        "psk_modes": _int_list(record, "psk_modes"),
        "extensions": sorted(_strip_grease(_int_list(record, "extensions"))),
    }


def build_tls_lane(
    path: Path | None,
    minimum_samples: int = 3,
    *,
    observed_source: str = "real-cli-mitm-clienthello",
) -> EvidenceLane:
    records, error = _read_jsonl(path)
    if error:
        return EvidenceLane("tls", {}, observed_source, False, error)
    if not records:
        return EvidenceLane("tls", {}, NOT_OBSERVED, True)
    try:
        semantics = [_tls_semantics(record) for record in records]
        for record in records:
            if str(record.get("server_name") or "") == "":
                raise ValueError("server_name missing")
            if _secret_errors(record):
                raise ValueError("secret-bearing TLS record")
        if len(records) < minimum_samples:
            return EvidenceLane("tls", {"sample_count": len(records)}, observed_source, True, f"need at least {minimum_samples} samples")
        if any(item != semantics[0] for item in semantics[1:]):
            raise ValueError("TLS semantic fields vary across samples")
        orders = [tuple(_strip_grease(_int_list(record, "extensions"))) for record in records]
        if len(set(orders)) < 2:
            return EvidenceLane("tls", {"sample_count": len(records)}, observed_source, True, "extension permutation not observed")
        samples = []
        for record in records:
            raw, digest = compute_ja3(int(record.get("version", 771)), _int_list(record, "cipher_suites"), _int_list(record, "extensions"), _int_list(record, "curves"), _int_list(record, "point_formats"))
            samples.append({"server_name": record["server_name"], "extension_order": _strip_grease(_int_list(record, "extensions")), "ja3_raw": raw, "ja3_hash": digest})
        observed = {**semantics[0], "shuffle_extensions": True, "sample_count": len(records), "samples": samples}
        return EvidenceLane("tls", observed, observed_source, True)
    except (TypeError, ValueError) as exc:
        return EvidenceLane("tls", {}, observed_source, False, str(exc))


def build_http_lane(path: Path | None) -> EvidenceLane:
    records, error = _read_jsonl(path)
    if error:
        return EvidenceLane("http", {}, "mitm-http", False, error)
    if not records:
        return EvidenceLane("http", {}, NOT_OBSERVED, True)
    errors = [error for record in records for error in _secret_errors(record)]
    if errors:
        return EvidenceLane("http", {}, "mitm-http", False, errors[0])
    successful = [record for record in records if record.get("success") is True and isinstance(record.get("status_code"), int)]
    if not successful:
        return EvidenceLane("http", {"record_count": len(records)}, "real-cli-mitm-http", True, "no successful response")
    return EvidenceLane("http", {"record_count": len(records), "successful_count": len(successful), "records": records}, "real-cli-mitm-http", True)


def build_protocol_lane(http_lane: EvidenceLane) -> EvidenceLane:
    if http_lane.source == NOT_OBSERVED or not http_lane.observed.get("successful_count"):
        return EvidenceLane("protocol", {}, NOT_OBSERVED, True)
    records = http_lane.observed["records"]
    observed = {
        "operations": sorted({f"{record['method']} {record['host']}{record['path']}" for record in records if record.get("success") is True}),
        "targets": sorted({record.get("headers", {}).get("x-amz-target", "") for record in records if record.get("headers", {}).get("x-amz-target")}),
        "content_types": sorted({record.get("headers", {}).get("content-type", "") for record in records if record.get("headers", {}).get("content-type")}),
        "origins": sorted({record.get("origin", "") for record in records if record.get("origin")}),
        "body_keys": sorted({key for record in records for key in record.get("body_keys", [])}),
    }
    return EvidenceLane("protocol", observed, "real-cli-mitm-http", True)


def build_auth_lane(cache_path: Path | None, whoami_path: Path | None) -> EvidenceLane:
    if cache_path is None or not cache_path.is_file() or whoami_path is None or not whoami_path.is_file():
        return EvidenceLane("auth", {}, NOT_OBSERVED, True)
    try:
        payload = json.loads(cache_path.read_text(encoding="utf-8"))
        if not isinstance(payload, dict):
            raise ValueError("auth cache must be an object")
        method = str(payload.get("authMethod") or "").strip()
        provider = str(payload.get("provider") or "").strip()
        whoami = json.loads(whoami_path.read_text(encoding="utf-8"))
        if not isinstance(whoami, dict):
            raise ValueError("sanitized whoami metadata must be an object")
        account_type = str(whoami.get("account_type") or "").strip()
        # accountType is the CLI's authoritative user-facing cohort. authMethod
        # and provider describe the local credential transport and mechanically
        # validate that this is the observed Builder ID path rather than a
        # caller-supplied label.
        classifier = {
            ("BuilderId", "IdC", "Enterprise"): "builder_id",
        }
        cohort = classifier.get((account_type, method, provider))
        if cohort is None:
            raise ValueError("unknown accountType/authMethod/provider combination")
        region = str(payload.get("region") or "").strip()
        whoami_region = str(whoami.get("region") or "").strip()
        if not region or region != whoami_region:
            raise ValueError("auth region sources disagree")
        return EvidenceLane(
            "auth",
            {"cohort": cohort, "region": region},
            "real-cli-whoami+auth-cache-metadata",
            True,
        )
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        return EvidenceLane("auth", {}, "real-cli-auth-classifier", False, str(exc))


def assemble_evidence_bundle(lanes: list[EvidenceLane], *, captured_at: str = "", kiro_cli_version: str = "") -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "captured_at": captured_at or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "kiro_cli_version": kiro_cli_version,
        "evidence_lanes": {lane.name: {"observed": lane.observed, "source": lane.source, "valid": lane.valid, "error": lane.error} for lane in lanes},
    }


def _lanes(bundle: dict[str, Any]) -> dict[str, Any]:
    value = bundle.get("evidence_lanes")
    return value if isinstance(value, dict) else {}


def evidence_complete(bundle: dict[str, Any]) -> bool:
    lanes = _lanes(bundle)
    version = str(bundle.get("kiro_cli_version") or "")
    return bool(re.fullmatch(r"\d+\.\d+\.\d+", version)) and all(
        name in lanes
        and lanes[name].get("valid") is True
        and lanes[name].get("source") != NOT_OBSERVED
        and not lanes[name].get("error")
        for name in ("tls", "http", "protocol", "auth")
    )


def build_canonical_profile(bundle: dict[str, Any]) -> dict[str, Any]:
    if not evidence_complete(bundle):
        raise ValueError("complete real CLI evidence is required")
    lanes = _lanes(bundle)
    tls = lanes["tls"]["observed"]
    http_records = lanes["http"]["observed"]["records"]
    successful = [record for record in http_records if record.get("success") is True]
    headers = successful[0].get("headers", {})
    profile = {
        "name": KIRO_PROFILE_NAME,
        "description": "TokenKey canonical Kiro CLI TLS profile captured from real kiro-cli traffic; rustls permutes the observed extension set for each ClientHello.",
        **{field: tls[field] for field in RUNTIME_PROFILE_FIELDS},
        "observed": {
            "source": "real-cli-mitm",
            "kiro_cli_version": bundle.get("kiro_cli_version", ""),
            "sample_count": tls["sample_count"],
            "samples": tls["samples"],
            "user_agent": headers.get("user-agent", ""),
            "x_amz_user_agent": headers.get("x-amz-user-agent", ""),
            "protocol": lanes["protocol"]["observed"],
            "auth_cohort": lanes["auth"]["observed"]["cohort"],
        },
    }
    if _secret_errors(profile):
        raise ValueError("candidate profile contains secret-bearing evidence")
    return profile


def runtime_profile_projection(profile: dict[str, Any]) -> dict[str, Any]:
    projection: dict[str, Any] = {}
    for field in RUNTIME_PROFILE_FIELDS:
        if field not in profile:
            raise ValueError(f"profile is missing runtime field: {field}")
        value = profile[field]
        if field in ("enable_grease", "shuffle_extensions"):
            if type(value) is not bool:
                raise ValueError(f"{field} must be a boolean")
        elif not isinstance(value, list):
            raise ValueError(f"{field} must be an array")
        projection[field] = value
    if profile.get("shuffle_extensions"):
        projection["extensions"] = sorted(projection["extensions"])
    return projection


def runtime_profile_digest(profile: dict[str, Any]) -> str:
    encoded = json.dumps(runtime_profile_projection(profile), ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def load_committed_profile(path: Path = KIRO_TLS_PROFILE_JSON) -> dict[str, Any] | None:
    if not path.is_file():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def compute_bundle_exit_code(bundle: dict[str, Any], committed: dict[str, Any] | None) -> int:
    lanes = _lanes(bundle)
    if set(lanes) != {"tls", "http", "protocol", "auth"}:
        return EXIT_INCOMPLETE
    if any(lane.get("valid") is not True for lane in lanes.values()):
        return EXIT_INVALID
    if not evidence_complete(bundle):
        return EXIT_INCOMPLETE
    try:
        candidate = build_canonical_profile(bundle)
        if committed is not None and runtime_profile_projection(candidate) != runtime_profile_projection(committed):
            return EXIT_DRIFT
    except (KeyError, TypeError, ValueError):
        return EXIT_INVALID
    return EXIT_COMPLETE


def _load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("JSON root must be an object")
    return value


def cmd_bundle(args: argparse.Namespace) -> int:
    tls = build_tls_lane(args.tls_jsonl, args.minimum_tls_samples)
    http = build_http_lane(args.http_jsonl)
    lanes = [
        tls,
        http,
        build_protocol_lane(http),
        build_auth_lane(args.auth_cache, args.whoami_file),
    ]
    bundle = assemble_evidence_bundle(lanes, captured_at=args.captured_at, kiro_cli_version=args.kiro_cli_version)
    code = compute_bundle_exit_code(bundle, load_committed_profile())
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(bundle, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"bundle={args.out}")
    print(f"exit_code={code}")
    for lane in lanes:
        print(f"{lane.name}: source={lane.source} valid={str(lane.valid).lower()} error={lane.error or '-'}")
    return code


def cmd_check(args: argparse.Namespace) -> int:
    bundle = _load(args.bundle)
    code = compute_bundle_exit_code(bundle, load_committed_profile())
    print({0: "aligned", 1: "drift", 2: "invalid", 3: "incomplete"}[code])
    return code


def cmd_check_replay(args: argparse.Namespace) -> int:
    committed = load_committed_profile()
    if committed is None:
        print("replay: committed Kiro CLI profile is missing", file=sys.stderr)
        return EXIT_INVALID
    replay = build_tls_lane(
        args.tls_jsonl,
        args.minimum_tls_samples,
        observed_source="tokenkey-utls-mitm-clienthello",
    )
    if not replay.valid:
        print(f"replay: {replay.error}", file=sys.stderr)
        return EXIT_INVALID
    if replay.error or replay.source == NOT_OBSERVED:
        print(f"replay: {replay.error or NOT_OBSERVED}", file=sys.stderr)
        return EXIT_INCOMPLETE
    try:
        expected = runtime_profile_projection(committed)
        actual = runtime_profile_projection(replay.observed)
    except (KeyError, TypeError, ValueError) as exc:
        print(f"replay: {exc}", file=sys.stderr)
        return EXIT_INVALID
    if expected != actual:
        print("replay: runtime semantic projection drift", file=sys.stderr)
        return EXIT_DRIFT
    result = {
        "source": replay.source,
        "sample_count": replay.observed["sample_count"],
        "distinct_extension_orders": len({
            tuple(sample["extension_order"])
            for sample in replay.observed["samples"]
        }),
        "semantic_projection_match": True,
        "ja3_hashes": [sample["ja3_hash"] for sample in replay.observed["samples"]],
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return EXIT_COMPLETE


def cmd_show_baseline(args: argparse.Namespace) -> int:
    committed = load_committed_profile()
    print(json.dumps({"profile_name": KIRO_PROFILE_NAME, "present": committed is not None, "runtime_projection": runtime_profile_projection(committed) if committed else None}, ensure_ascii=False, indent=2))
    return 0


def cmd_emit_profile(args: argparse.Namespace) -> int:
    try:
        bundle = _load(args.bundle)
        if compute_bundle_exit_code(bundle, None) != EXIT_COMPLETE:
            raise ValueError("bundle evidence is incomplete or invalid")
        profile = build_canonical_profile(bundle)
        out = args.out or KIRO_TLS_PROFILE_JSON
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(profile, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"wrote {out}")
        return 0
    except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
        print(f"emit-profile: {exc}", file=sys.stderr)
        return EXIT_INVALID


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    bundle = sub.add_parser("bundle")
    bundle.add_argument("--tls-jsonl", type=Path)
    bundle.add_argument("--http-jsonl", type=Path)
    bundle.add_argument("--auth-cache", type=Path, default=DEFAULT_AUTH_CACHE)
    bundle.add_argument("--whoami-file", type=Path)
    bundle.add_argument("--kiro-cli-version", required=True)
    bundle.add_argument("--captured-at", default="")
    bundle.add_argument("--minimum-tls-samples", type=int, default=3)
    bundle.add_argument("--out", type=Path, required=True)
    bundle.set_defaults(func=cmd_bundle)
    for name in ("check", "diff", "check-tls"):
        check = sub.add_parser(name)
        check.add_argument("--bundle", type=Path, required=True)
        check.set_defaults(func=cmd_check)
    replay = sub.add_parser("check-replay")
    replay.add_argument("--tls-jsonl", type=Path, required=True)
    replay.add_argument("--minimum-tls-samples", type=int, default=3)
    replay.set_defaults(func=cmd_check_replay)
    show = sub.add_parser("show-baseline")
    show.set_defaults(func=cmd_show_baseline)
    emit = sub.add_parser("emit-profile")
    emit.add_argument("--bundle", type=Path, required=True)
    emit.add_argument("--out", type=Path)
    emit.set_defaults(func=cmd_emit_profile)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INVALID


if __name__ == "__main__":
    raise SystemExit(main())
