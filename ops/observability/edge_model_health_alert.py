#!/usr/bin/env python3
"""Evaluate complete Edge terminal buckets and produce one atomic alert decision."""
from __future__ import annotations

import argparse
import copy
import datetime as dt
import hashlib
import json
import pathlib
import sys


SCHEMA_VERSION = 1
FIVE_MINUTES = dt.timedelta(minutes=5)
FAMILY_NAMES = {
    "claude": "Claude",
    "gpt": "GPT",
    "gemini": "Gemini",
    "grok": "Grok",
    "deepseek": "DeepSeek",
    "qwen": "Qwen",
    "glm": "GLM",
    "minimax": "MiniMax",
}


class AlertContractError(ValueError):
    pass


def _timestamp(value: object) -> dt.datetime:
    if not isinstance(value, str):
        raise AlertContractError("bucket timestamp must be a string")
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise AlertContractError(f"invalid bucket timestamp {value!r}") from exc
    if parsed.tzinfo is None:
        raise AlertContractError("bucket timestamp must include timezone")
    return parsed.astimezone(dt.timezone.utc)


def _canonical_timestamp(value: object) -> str:
    return _timestamp(value).isoformat().replace("+00:00", "Z")


def load_family_rules(path: pathlib.Path) -> dict:
    try:
        artifact = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise AlertContractError(f"model-family artifact unavailable: {exc}") from exc
    qualifiers = artifact.get("provider_qualifiers")
    rules = artifact.get("rules")
    if artifact.get("schema_version") != SCHEMA_VERSION:
        raise AlertContractError("unsupported model-family schema version")
    if not isinstance(qualifiers, list) or not qualifiers or not all(isinstance(value, str) and value for value in qualifiers):
        raise AlertContractError("invalid provider qualifiers")
    if not isinstance(rules, list) or not rules:
        raise AlertContractError("invalid model-family rules")
    for rule in rules:
        if (
            not isinstance(rule, dict)
            or not isinstance(rule.get("family"), str)
            or not isinstance(rule.get("prefixes"), list)
            or not rule["prefixes"]
            or not all(isinstance(prefix, str) and prefix for prefix in rule["prefixes"])
        ):
            raise AlertContractError("invalid model-family rule")
    checksum_payload = json.dumps(
        {
            "schema_version": artifact["schema_version"],
            "provider_qualifiers": qualifiers,
            "rules": rules,
        },
        separators=(",", ":"),
    ).encode("utf-8")
    checksum = hashlib.sha256(checksum_payload).hexdigest()
    if artifact.get("checksum_sha256") != checksum:
        raise AlertContractError("model-family artifact checksum mismatch")
    return artifact


def _family_for(model: str, rules: dict) -> str | None:
    normalized = model.strip().lower()
    for qualifier in rules["provider_qualifiers"]:
        if normalized.startswith(qualifier):
            normalized = normalized[len(qualifier) :]
            break
    for rule in rules["rules"]:
        if any(normalized.startswith(prefix) for prefix in rule["prefixes"]):
            return rule["family"]
    return None


def _empty_state() -> dict:
    return {"schema_version": SCHEMA_VERSION, "model_units": [], "hosts": [], "telemetry": []}


def _validate_unit(unit: object) -> dict:
    if not isinstance(unit, dict) or unit.get("kind") not in {"family", "model"}:
        raise AlertContractError("unit must have family/model kind")
    if not isinstance(unit.get("id"), str) or not unit["id"]:
        raise AlertContractError("unit id must be a non-empty string")
    return unit


def validate_state(value: object) -> dict:
    if value in ({}, None):
        return _empty_state()
    if not isinstance(value, dict) or value.get("schema_version") != SCHEMA_VERSION:
        raise AlertContractError("unsupported state schema")
    state = copy.deepcopy(value)
    for field in ("model_units", "hosts", "telemetry"):
        if not isinstance(state.get(field), list):
            raise AlertContractError(f"state {field} must be a list")
    seen: set[tuple[str, str, str]] = set()
    for entry in state["model_units"]:
        if not isinstance(entry, dict) or not isinstance(entry.get("edge"), str):
            raise AlertContractError("invalid model-unit state entry")
        unit = _validate_unit(entry.get("unit"))
        if entry.get("status") not in {"degraded", "unavailable"}:
            raise AlertContractError("invalid model-unit status")
        if entry.get("last_notified_severity") not in {"degraded", "unavailable"}:
            raise AlertContractError("invalid last-notified severity")
        _canonical_timestamp(entry.get("started_bucket"))
        _canonical_timestamp(entry.get("last_evaluated_bucket"))
        key = (entry["edge"], unit["kind"], unit["id"])
        if key in seen:
            raise AlertContractError("duplicate model-unit state entry")
        seen.add(key)
    host_edges: set[str] = set()
    for entry in state["hosts"]:
        if (
            not isinstance(entry, dict)
            or not isinstance(entry.get("edge"), str)
            or not entry["edge"]
            or entry.get("status") != "unreachable"
            or entry["edge"] in host_edges
        ):
            raise AlertContractError("invalid or duplicate host state entry")
        host_edges.add(entry["edge"])
    telemetry_edges: set[str] = set()
    for entry in state["telemetry"]:
        slots = entry.get("failure_slots") if isinstance(entry, dict) else None
        if (
            not isinstance(entry, dict)
            or not isinstance(entry.get("edge"), str)
            or not entry["edge"]
            or entry.get("status") not in {"pending", "unavailable"}
            or not isinstance(slots, list)
            or not slots
            or len(slots) > 2
            or not all(isinstance(slot, str) for slot in slots)
            or entry["edge"] in telemetry_edges
        ):
            raise AlertContractError("invalid or duplicate telemetry state entry")
        for slot in slots:
            _canonical_timestamp(slot)
        telemetry_edges.add(entry["edge"])
    return state


def _unit_key(edge: str, unit: dict) -> tuple[str, str, str]:
    return edge, unit["kind"], unit["id"]


def _slot(now: dt.datetime) -> str:
    now = now.astimezone(dt.timezone.utc).replace(second=0, microsecond=0)
    return now.replace(minute=(now.minute // 5) * 5).isoformat().replace("+00:00", "Z")


def _contiguous_tail(buckets: list[dict], count: int) -> list[dict]:
    if not buckets:
        return []
    tail = [buckets[-1]]
    for candidate in reversed(buckets[:-1]):
        if _timestamp(tail[0]["bucket_start"]) - _timestamp(candidate["bucket_start"]) != FIVE_MINUTES:
            break
        tail.insert(0, candidate)
        if len(tail) == count:
            break
    return tail


def _rate(metric: dict) -> float:
    return metric["empty"] / metric["total"] if metric["total"] else 0.0


def _metric_for(bucket: dict, unit: dict, rules: dict) -> dict:
    success = empty = other = 0
    models: dict[str, int] = {}
    for fact in bucket["facts"]:
        model = fact["requested_model"]
        family = _family_for(model, rules)
        matches = family == unit["id"] if unit["kind"] == "family" else family is None and model == unit["id"]
        if not matches:
            continue
        success += fact["success"]
        empty += fact["final_empty_pool_429"]
        other += fact["other_error"]
        if fact["final_empty_pool_429"]:
            models[model] = models.get(model, 0) + fact["final_empty_pool_429"]
    return {
        "bucket_start": bucket["bucket_start"],
        "success": success,
        "empty": empty,
        "other": other,
        "total": success + empty + other,
        "models": models,
    }


def _top_models(metric: dict) -> list[dict]:
    ranked = sorted(metric["models"].items(), key=lambda item: (-item[1], item[0]))[:3]
    return [{"model": model, "empty_pool_429": count} for model, count in ranked]


def _trigger(metrics: list[dict]) -> str | None:
    latest = metrics[-1]
    if latest["empty"] >= 50:
        return "unavailable"
    if len(metrics) >= 2:
        last_two = metrics[-2:]
        if all(metric["empty"] >= 10 and _rate(metric) >= 0.80 for metric in last_two):
            return "unavailable"
        if all(metric["empty"] >= 10 and _rate(metric) >= 0.20 for metric in last_two):
            return "degraded"
    return None


def _recovery(metrics: list[dict]) -> str | None:
    if len(metrics) < 3:
        return None
    last_three = metrics[-3:]
    if all(metric["total"] == 0 for metric in last_three):
        return "traffic_stopped"
    if all(metric["total"] > 0 and metric["empty"] < 5 and _rate(metric) < 0.10 for metric in last_three):
        return "error_rate_recovered"
    return None


def _impact_changed(previous: list[dict], current: list[dict], total_empty: int) -> bool:
    if not current or total_empty <= 0:
        return False
    previous_ids = {item["model"] for item in previous}
    entering = [item for item in current if item["model"] not in previous_ids]
    if any(item["empty_pool_429"] / total_empty >= 0.25 for item in entering):
        return True
    if previous and current[0]["model"] != previous[0]["model"]:
        return current[0]["empty_pool_429"] / total_empty >= 0.25
    return False


def _safe_display(value: str) -> str:
    clean = "".join(character if character.isprintable() else "?" for character in value)
    return clean if len(clean) <= 80 else clean[:77] + "..."


def _unit_name(unit: dict) -> str:
    if unit["kind"] == "family":
        return f"{FAMILY_NAMES.get(unit['id'], unit['id'])} 模型族"
    return f"{_safe_display(unit['id'])} 模型"


def _metric_text(metrics: list[dict]) -> str:
    selected = metrics[-2:] if len(metrics) >= 2 else metrics
    return " -> ".join(
        f"{metric['empty']}/{metric['total']} ({_rate(metric) * 100:.1f}%)" for metric in selected
    )


def _transition_message(transition: dict) -> list[str]:
    kind = transition["kind"]
    edge = transition["edge"]
    if kind == "host":
        if transition["to"] == "unreachable":
            return [f"🔴 {edge} · Edge 主机不可达"]
        return [f"✅ {edge} · Edge 主机已恢复"]
    if kind == "telemetry":
        if transition["to"] == "unavailable":
            return [f"🟠 {edge} · 监控数据不可用"]
        return [f"✅ {edge} · 监控数据已恢复"]

    unit = transition["unit"]
    name = _unit_name(unit)
    reason = transition["reason"]
    if transition["to"] == "inactive":
        if reason == "traffic_stopped":
            return [f"✅ {edge} · {name}影响已停止（路由已摘除或已无流量）"]
        return [f"✅ {edge} · {name}路由异常已恢复"]
    if reason == "impact_changed":
        heading = f"🟠 {edge} · {name}影响范围变化"
    elif transition["to"] == "unavailable":
        heading = f"🔴 {edge} · {name}路由不可用"
    else:
        heading = f"🟠 {edge} · {name}路由降级"
    lines = [heading, f"  最近桶: {_metric_text(transition['metrics'])}"]
    if unit["kind"] == "family" and transition.get("top_models"):
        lines.append("  Top 受影响模型:")
        lines.extend(
            f"    {item['model']}  {item['empty_pool_429']}" for item in transition["top_models"]
        )
    lines.append("  处置: 补充可承载该模型单元的健康账号，或从该 Edge 摘除对应路由")
    return lines


def _format_message(transitions: list[dict]) -> str:
    lines = ["TokenKey Edge 模型路由健康"]
    for transition in transitions:
        lines.append("")
        lines.extend(_transition_message(transition))
    return "\n".join(lines) if transitions else ""


def _transition_order(transition: dict) -> tuple:
    severity = {
        ("host", "unreachable"): 0,
        ("model_unit", "unavailable"): 1,
        ("model_unit", "degraded"): 2,
        ("telemetry", "unavailable"): 3,
        ("model_unit", "inactive"): 4,
        ("host", "healthy"): 5,
        ("telemetry", "healthy"): 6,
    }
    unit = transition.get("unit", {})
    return severity.get((transition["kind"], transition["to"]), 9), transition["edge"], unit.get("kind", ""), unit.get("id", "")


def evaluate(scan_rows: list, previous_state: object, rules: dict, *, evaluated_at: dt.datetime | None = None) -> dict:
    state = validate_state(previous_state)
    now = (evaluated_at or dt.datetime.now(dt.timezone.utc)).astimezone(dt.timezone.utc)
    if not isinstance(scan_rows, list) or not scan_rows:
        raise AlertContractError("scan must contain at least one target")
    edges: set[str] = set()
    for row in scan_rows:
        if not isinstance(row, dict) or row.get("schema_version") != SCHEMA_VERSION:
            raise AlertContractError("invalid scan row")
        edge_id = row.get("edge")
        if not isinstance(edge_id, str) or not edge_id or edge_id in edges or not isinstance(row.get("reachable"), bool):
            raise AlertContractError("invalid or duplicate scan edge")
        edges.add(edge_id)

    units = {_unit_key(entry["edge"], entry["unit"]): entry for entry in state["model_units"]}
    hosts = {entry["edge"]: entry for entry in state["hosts"]}
    telemetry = {entry["edge"]: entry for entry in state["telemetry"]}
    transitions: list[dict] = []

    for row in scan_rows:
        edge_id = row["edge"]
        if not row["reachable"]:
            if edge_id not in hosts:
                transitions.append({"kind": "host", "edge": edge_id, "from": "healthy", "to": "unreachable", "reason": row.get("reason", "https_unreachable")})
            hosts[edge_id] = {"edge": edge_id, "status": "unreachable", "reason": row.get("reason", "https_unreachable")}
            continue
        if edge_id in hosts:
            transitions.append({"kind": "host", "edge": edge_id, "from": "unreachable", "to": "healthy", "reason": "reachable"})
            hosts.pop(edge_id)

        buckets = row.get("buckets")
        telemetry_reason = None
        if row.get("telemetry_status") != "fresh":
            telemetry_reason = row.get("reason", row.get("telemetry_status", "unavailable"))
        elif not isinstance(buckets, list) or not buckets:
            telemetry_reason = "missing_buckets"
        elif not buckets[-1].get("complete"):
            telemetry_reason = "incomplete_bucket"

        if telemetry_reason is not None:
            entry = telemetry.get(edge_id, {"edge": edge_id, "status": "pending", "failure_slots": []})
            current_slot = _slot(now)
            slots = list(entry.get("failure_slots", []))
            if current_slot not in slots:
                if slots and _timestamp(current_slot) - _timestamp(slots[-1]) != FIVE_MINUTES:
                    slots = []
                slots.append(current_slot)
            entry.update({"failure_slots": slots[-2:], "last_reason": telemetry_reason})
            if len(entry["failure_slots"]) >= 2:
                if entry.get("status") != "unavailable":
                    transitions.append({"kind": "telemetry", "edge": edge_id, "from": entry.get("status", "pending"), "to": "unavailable", "reason": telemetry_reason})
                entry["status"] = "unavailable"
            telemetry[edge_id] = entry
            continue
        if edge_id in telemetry:
            if telemetry[edge_id].get("status") == "unavailable":
                transitions.append({"kind": "telemetry", "edge": edge_id, "from": "unavailable", "to": "healthy", "reason": "fresh"})
            telemetry.pop(edge_id)

        if edge_id == "prod":
            continue
        normalized_buckets = []
        for bucket in buckets:
            if not isinstance(bucket, dict) or not isinstance(bucket.get("facts"), list):
                raise AlertContractError("invalid terminal bucket")
            copied = copy.deepcopy(bucket)
            copied["bucket_start"] = _canonical_timestamp(bucket.get("bucket_start"))
            for fact in copied["facts"]:
                counts = (fact.get("success"), fact.get("final_empty_pool_429"), fact.get("other_error")) if isinstance(fact, dict) else ()
                if (
                    not isinstance(fact, dict)
                    or not isinstance(fact.get("requested_model"), str)
                    or not fact["requested_model"]
                    or len(counts) != 3
                    or any(not isinstance(value, int) or value < 0 for value in counts)
                ):
                    raise AlertContractError("invalid terminal fact")
            normalized_buckets.append(copied)
        normalized_buckets.sort(key=lambda item: item["bucket_start"])
        complete_buckets = [bucket for bucket in normalized_buckets if bucket.get("complete")]
        tail_three = _contiguous_tail(complete_buckets, 3)
        latest_bucket = complete_buckets[-1]
        latest_start = latest_bucket["bucket_start"]

        candidates: dict[tuple[str, str, str], dict] = {}
        for bucket in _contiguous_tail(complete_buckets, 2):
            for fact in bucket["facts"]:
                family = _family_for(fact["requested_model"], rules)
                if family:
                    unit = {"kind": "family", "id": family}
                    candidates[_unit_key(edge_id, unit)] = unit
                elif fact["final_empty_pool_429"] > 0:
                    unit = {"kind": "model", "id": fact["requested_model"]}
                    candidates[_unit_key(edge_id, unit)] = unit
        for key, entry in units.items():
            if key[0] == edge_id:
                candidates[key] = entry["unit"]

        for key in sorted(candidates):
            unit = candidates[key]
            existing = units.get(key)
            if existing and existing.get("last_evaluated_bucket") == latest_start:
                continue
            metrics = [_metric_for(bucket, unit, rules) for bucket in tail_three]
            triggered = _trigger(metrics)
            top_models = _top_models(metrics[-1]) if unit["kind"] == "family" else []
            if existing is None:
                if triggered is None:
                    continue
                entry = {
                    "edge": edge_id,
                    "unit": copy.deepcopy(unit),
                    "status": triggered,
                    "started_bucket": latest_start,
                    "last_evaluated_bucket": latest_start,
                    "last_notified_severity": triggered,
                }
                if unit["kind"] == "family":
                    entry["last_notified_top_models"] = top_models
                units[key] = entry
                transitions.append({"kind": "model_unit", "edge": edge_id, "unit": copy.deepcopy(unit), "from": "inactive", "to": triggered, "reason": "threshold", "metrics": metrics, "top_models": top_models})
                continue

            recovered = _recovery(metrics)
            if recovered:
                transitions.append({"kind": "model_unit", "edge": edge_id, "unit": copy.deepcopy(unit), "from": existing["status"], "to": "inactive", "reason": recovered, "metrics": metrics})
                units.pop(key)
                continue
            if existing["status"] == "degraded" and triggered == "unavailable":
                existing.update({"status": "unavailable", "last_notified_severity": "unavailable", "last_evaluated_bucket": latest_start})
                if unit["kind"] == "family":
                    existing["last_notified_top_models"] = top_models
                transitions.append({"kind": "model_unit", "edge": edge_id, "unit": copy.deepcopy(unit), "from": "degraded", "to": "unavailable", "reason": "threshold", "metrics": metrics, "top_models": top_models})
                continue
            if unit["kind"] == "family" and _impact_changed(existing.get("last_notified_top_models", []), top_models, metrics[-1]["empty"]):
                existing["last_notified_top_models"] = top_models
                existing["last_evaluated_bucket"] = latest_start
                transitions.append({"kind": "model_unit", "edge": edge_id, "unit": copy.deepcopy(unit), "from": existing["status"], "to": existing["status"], "reason": "impact_changed", "metrics": metrics, "top_models": top_models})
                continue
            existing["last_evaluated_bucket"] = latest_start

    state["model_units"] = sorted(units.values(), key=lambda entry: _unit_key(entry["edge"], entry["unit"]))
    state["hosts"] = sorted(hosts.values(), key=lambda entry: entry["edge"])
    state["telemetry"] = sorted(telemetry.values(), key=lambda entry: entry["edge"])
    transitions.sort(key=_transition_order)
    return {
        "schema_version": SCHEMA_VERSION,
        "should_alert": bool(transitions),
        "message": _format_message(transitions),
        "transitions": transitions,
        "state": state,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scan", type=pathlib.Path, required=True)
    parser.add_argument("--state-file", type=pathlib.Path, required=True)
    parser.add_argument(
        "--rules",
        type=pathlib.Path,
        default=pathlib.Path(__file__).with_name("generated") / "model-family-rules.json",
    )
    args = parser.parse_args(argv)
    try:
        rows = [json.loads(line) for line in args.scan.read_text(encoding="utf-8").splitlines() if line.strip()]
        previous = json.loads(args.state_file.read_text(encoding="utf-8")) if args.state_file.exists() else {}
        decision = evaluate(rows, previous, load_family_rules(args.rules))
    except (OSError, json.JSONDecodeError, AlertContractError) as exc:
        print(f"edge-model-health-alert: {exc}", file=sys.stderr)
        return 1
    json.dump(decision, sys.stdout, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
