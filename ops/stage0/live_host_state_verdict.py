#!/usr/bin/env python3
"""live_host_state_verdict.py — turn the read-only live-host probe output into a
deterministic drift verdict for a Stage0 prod host.

This is the *logic* half; the *transport* half is the read-only SSM orchestrator
`assert-live-host-state.sh`. Keeping the verdict here (pure Python, no AWS) makes
it unit-testable with fixtures (`--selftest`) and registerable in preflight,
mirroring the determinism contract in dev-rules-convention.mdc §"skill / command
确定性基线" and its siblings edge_health_verdict.py / data_layer_capacity_verdict.py.

WHY THIS EXISTS:
    A Stage0 prod host's *running state* is deliberately decoupled from the
    CFN/repo baseline and maintained by imperative healers:
      - the running image tag is hot-deployed via SSM (deploy_via_ssm.sh), so the
        CFN ImageTag parameter intentionally lags (changing it would REPLACE the
        instance — ImageTag is substituted into UserData);
      - prod-only env (SERVER_FRONTEND_URL, TOKENKEY_GHCR_KEEP_TAGS, the four
        QA_CAPTURE_EXPORT_STORAGE_* vars) is sed-injected onto the host by
        deploy_via_ssm.sh, NOT carried in the shared compose (avoids the edge
        14 KiB launch-script limit).
    The decoupling is by design and good — but until now NOTHING watched the live
    host, so every drift (a deploy-sed that wrote the wrong content, a manual host
    edit, a silent tag rollback) was caught only by a human SSM-probing by hand
    (the 2026-06 "3× repeat" incident). This verdict is the gate that turns that
    into an automatic ::warning:: post-deploy and a daily audit alert.

Input (stdin): tagged, field-named JSON lines emitted by the probe:
    RUNIMAGE {"image":"ghcr.io/owner/sub2api:1.8.10"}
    ENV      {"key":"QA_CAPTURE_EXPORT_STORAGE_DRIVER","value":"s3"}
    ENV      {"key":"SERVER_FRONTEND_URL","value":"https://api.tokenkey.dev"}
    RETENTION {"value":"2"}
(ENV lines are emitted only for keys actually present in the running container;
a required key with no ENV line is therefore a "missing" drift.)

Args:
    --expected-tag TAG      assert the running image ends with ":TAG" (post-deploy
                            mode; omit for the daily audit where the intended tag
                            is not known to the caller).
    --require-env K1,K2,..  env keys that MUST be present and non-empty on the host
                            (defaults to the deploy_via_ssm.sh injection set).

Exit: 0 = no drift, 3 = drift (advisory; the orchestrator converts drift into a
non-blocking ::warning:: and always exits 0 so a working deploy is never failed).
`--selftest` exits 0/1 on fixture pass/fail.
"""

from __future__ import annotations

import argparse
import json
import sys

# The CONTAINER env keys deploy_via_ssm.sh sed-injects into the compose mapping,
# so they reach the app at runtime (verifiable via `docker exec printenv`). Keep
# in sync with that script's injection list; the detector's whole point is to
# assert these actually landed (catching a deploy-sed that wrote the wrong
# content — the 2026-06 "3× repeat"). NOTE: TOKENKEY_GHCR_KEEP_TAGS is injected
# into the HOST .env only (consumed by the host ghcr-prune script), NOT mapped
# into the container, so it is deliberately NOT a container-env assertion here.
DEFAULT_REQUIRED_ENV = [
    "SERVER_FRONTEND_URL",
    "QA_CAPTURE_EXPORT_STORAGE_DRIVER",
    "QA_CAPTURE_EXPORT_STORAGE_REGION",
    "QA_CAPTURE_EXPORT_STORAGE_BUCKET",
    "QA_CAPTURE_EXPORT_STORAGE_PREFIX",
]


def parse_facts(lines):
    """Parse tagged JSON lines into {image, env{key:value}, retention}."""
    image = None
    env = {}
    retention = None
    for raw in lines:
        raw = raw.strip()
        if not raw:
            continue
        parts = raw.split(None, 1)
        if len(parts) != 2:
            continue
        tag, payload = parts
        try:
            obj = json.loads(payload)
        except (ValueError, TypeError):
            continue
        if tag == "RUNIMAGE":
            image = (obj.get("image") or "").strip() or None
        elif tag == "ENV":
            k = (obj.get("key") or "").strip()
            if k:
                env[k] = obj.get("value")
        elif tag == "RETENTION":
            retention = obj.get("value")
    return {"image": image, "env": env, "retention": retention}


def compute_drifts(facts, expected_tag=None, required_env=None):
    """Pure verdict: return a sorted list of human-readable drift strings."""
    required_env = required_env if required_env is not None else DEFAULT_REQUIRED_ENV
    drifts = []

    image = facts.get("image")
    if expected_tag:
        if not image:
            drifts.append("running image unknown (could not read container image)")
        elif not image.endswith(":" + expected_tag):
            drifts.append(
                f"running image tag != expected: image={image} expected_tag={expected_tag}"
            )

    env = facts.get("env") or {}
    for key in required_env:
        if key not in env:
            drifts.append(f"missing required env on host: {key}")
        elif env[key] is None or str(env[key]).strip() == "":
            drifts.append(f"required env present but empty on host: {key}")

    return sorted(drifts)


def _verdict(lines, expected_tag=None, required_env=None):
    facts = parse_facts(lines)
    return compute_drifts(facts, expected_tag=expected_tag, required_env=required_env)


def _selftest():
    cases = []

    clean = [
        'RUNIMAGE {"image":"ghcr.io/o/sub2api:1.8.10"}',
        'ENV {"key":"SERVER_FRONTEND_URL","value":"https://api.tokenkey.dev"}',
        'ENV {"key":"QA_CAPTURE_EXPORT_STORAGE_DRIVER","value":"s3"}',
        'ENV {"key":"QA_CAPTURE_EXPORT_STORAGE_REGION","value":"us-east-1"}',
        'ENV {"key":"QA_CAPTURE_EXPORT_STORAGE_BUCKET","value":"tokenkey-prod-qa-exports-682751977094"}',
        'ENV {"key":"QA_CAPTURE_EXPORT_STORAGE_PREFIX","value":"traj-exports"}',
        'RETENTION {"value":"2"}',
    ]
    cases.append(("clean post-deploy → no drift", _verdict(clean, expected_tag="1.8.10"), []))

    # silent tag rollback
    rolled = list(clean)
    rolled[0] = 'RUNIMAGE {"image":"ghcr.io/o/sub2api:1.7.11"}'
    got = _verdict(rolled, expected_tag="1.8.10")
    cases.append(("tag rollback → drift", bool(got) and "running image tag" in got[0], True))

    # missing QA export env (the 3× incident class)
    missing = [l for l in clean if "QA_CAPTURE_EXPORT_STORAGE_BUCKET" not in l]
    got = _verdict(missing, expected_tag="1.8.10")
    cases.append((
        "missing QA bucket env → drift",
        any("QA_CAPTURE_EXPORT_STORAGE_BUCKET" in d for d in got),
        True,
    ))

    # empty required env
    empty = list(clean)
    empty[1] = 'ENV {"key":"SERVER_FRONTEND_URL","value":""}'
    got = _verdict(empty, expected_tag="1.8.10")
    cases.append((
        "empty SERVER_FRONTEND_URL → drift",
        any("present but empty" in d and "SERVER_FRONTEND_URL" in d for d in got),
        True,
    ))

    # daily-audit mode (no expected tag): env-only checks still apply, tag ignored
    audit = list(clean)
    audit[0] = 'RUNIMAGE {"image":"ghcr.io/o/sub2api:9.9.9"}'
    cases.append(("audit mode ignores tag → no drift", _verdict(audit), []))

    ok = True
    for name, got, want in cases:
        passed = (got == want)
        print(f"{'PASS' if passed else 'FAIL'} {name}")
        if not passed:
            print(f"    got={got!r} want={want!r}")
            ok = False
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser(description="Stage0 live-host state drift verdict")
    ap.add_argument("--expected-tag", default=None)
    ap.add_argument("--require-env", default=None,
                    help="comma-separated env keys (default: deploy_via_ssm injection set)")
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--print-required", action="store_true",
                    help="print the default required-env keys (one per line) — the "
                         "single source of truth a preflight check greps against "
                         "deploy_via_ssm.sh so the two can't silently drift")
    args = ap.parse_args()

    if args.selftest:
        return _selftest()

    if args.print_required:
        for key in DEFAULT_REQUIRED_ENV:
            print(key)
        return 0

    required_env = None
    if args.require_env is not None:
        required_env = [k.strip() for k in args.require_env.split(",") if k.strip()]

    drifts = _verdict(sys.stdin.readlines(), expected_tag=args.expected_tag, required_env=required_env)
    if not drifts:
        print("OK: live host matches intended state")
        return 0
    print("DRIFT: live host differs from intended state:")
    for d in drifts:
        print(f"  - {d}")
    return 3


if __name__ == "__main__":
    sys.exit(main())
