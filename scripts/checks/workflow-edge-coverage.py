#!/usr/bin/env python3
"""Gate C: per-edge workflow coverage of the deployable edge matrices.

As the edge fleet grows, the hardcoded ``choice`` option lists in per-edge
workflows silently drift: a new deployable edge in
``deploy/aws/stage0/edge-targets.json`` (EC2) or
``deploy/aws/lightsail/edge-targets-lightsail.json`` (Lightsail) becomes
un-dispatchable / un-covered with no error. GitHub Actions cannot compute choice
options dynamically, so the only defence is a drift check.

This check reads ``scripts/checks/workflow-edge-coverage.json`` (the registry of
per-edge workflows + their required deployable set + explicit opt-outs) and fails
preflight if any deployable edge is missing from a workflow's options without an
opt-out, or if an opt-out is stale (its edge is no longer deployable).

Exit codes: 0 ok · 1 coverage drift / stale opt-out · 2 unreadable input or
missing pyyaml. stdlib + pyyaml (graceful exit 2 if absent, like sibling checks).
"""
from __future__ import annotations

import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
REGISTRY = pathlib.Path(__file__).resolve().parent / "workflow-edge-coverage.json"
EC2_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
LIGHTSAIL_MATRIX = REPO_ROOT / "deploy/aws/lightsail/edge-targets-lightsail.json"


def _deployable_ids(path: pathlib.Path) -> set[str]:
    """Edge ids with deployable=true. Same matrix shape as
    scripts/checks/edge-platform-exclusivity.py::_deployable_ids."""
    if not path.is_file():
        return set()
    targets = (json.loads(path.read_text(encoding="utf-8")).get("targets") or {})
    return {eid for eid, t in targets.items() if isinstance(t, dict) and t.get("deployable") is True}


def _workflow_options(doc: dict, input_name: str) -> list[str] | None:
    # YAML 1.1 parses bare `on:` to boolean True — accept either key.
    on = doc.get("on")
    if on is None:
        on = doc.get(True)
    if not isinstance(on, dict):
        return None
    inputs = ((on.get("workflow_dispatch") or {}).get("inputs") or {})
    spec = inputs.get(input_name)
    if not isinstance(spec, dict):
        return None
    opts = spec.get("options")
    return [str(o) for o in opts] if isinstance(opts, list) else None


def main() -> int:
    try:
        import yaml  # type: ignore
    except ImportError:
        print("FAIL: pyyaml not available to parse workflow YAML", file=sys.stderr)
        return 2

    try:
        registry = json.loads(REGISTRY.read_text(encoding="utf-8"))
        ec2 = _deployable_ids(EC2_MATRIX)
        lightsail = _deployable_ids(LIGHTSAIL_MATRIX)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"FAIL: cannot read registry/matrix: {exc}", file=sys.stderr)
        return 2

    sets = {
        "ec2-deployable": ec2,
        "lightsail-deployable": lightsail,
        "all-deployable": ec2 | lightsail,
    }

    failures: list[str] = []
    for wf_rel, cfg in (registry.get("workflows") or {}).items():
        wf_path = REPO_ROOT / wf_rel
        if not wf_path.is_file():
            failures.append(f"{wf_rel}: registered workflow file not found")
            continue
        required_key = cfg.get("required_set")
        if required_key not in sets:
            failures.append(f"{wf_rel}: unknown required_set {required_key!r}")
            continue
        try:
            doc = yaml.safe_load(wf_path.read_text(encoding="utf-8")) or {}
        except yaml.YAMLError as exc:  # type: ignore[attr-defined]
            failures.append(f"{wf_rel}: YAML parse error: {exc}")
            continue

        options = _workflow_options(doc, cfg.get("input", ""))
        if options is None:
            failures.append(
                f"{wf_rel}: no workflow_dispatch input {cfg.get('input')!r} with a choice "
                f"`options:` list (registry expects one)"
            )
            continue

        required = sets[required_key]
        opt_out = {e["id"]: e.get("reason", "") for e in (cfg.get("opt_out_edges") or [])}
        prefix = cfg.get("option_prefix", "")
        opt_set = set(options)

        # Stale opt-out: opted-out an edge that is no longer in the required set.
        stale = sorted(e for e in opt_out if e not in required)
        if stale:
            failures.append(
                f"{wf_rel}: stale opt_out_edges {stale} — no longer deployable in "
                f"{required_key}; remove them from the registry"
            )
        missing_reason = sorted(e for e in opt_out if not opt_out[e].strip())
        if missing_reason:
            failures.append(f"{wf_rel}: opt_out_edges {missing_reason} need a non-empty reason")

        # Coverage: every required, non-opted-out edge must be an option.
        needed = sorted(e for e in required if e not in opt_out)
        uncovered = [e for e in needed if f"{prefix}{e}" not in opt_set]
        if uncovered:
            failures.append(
                f"{wf_rel}: deployable edge(s) {uncovered} missing from input "
                f"'{cfg.get('input')}' options (expected option '{prefix}<id>'). "
                f"Add them to the workflow, or opt out in {REGISTRY.name} with a reason."
            )

    if failures:
        print("FAIL: workflow edge coverage", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1

    covered = ", ".join(sorted((registry.get("workflows") or {})))
    print(f"ok: per-edge workflows cover the deployable matrices ({covered})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
