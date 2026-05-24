#!/usr/bin/env python3
"""Deterministic coverage check: every AWS action the Lightsail Edge workflow
issues must be granted by either the addon policy (this repo's CFN) or the
base OIDC role's inline policies (assumed correct because every other
workflow depends on it).

Why this script exists
======================

Phase 2 (Lightsail Edge uk1 provision) hit three separate AWS implicit
permission contracts in 24 hours:

  - PR #397: `aws ssm create-activation --iam-role` ⇒ needs iam:PassRole
  - PR #398: addon policy attached to wrong regional OIDC role
  - PR #399: `aws ssm create-activation --tags` ⇒ needs ssm:AddTagsToResource

Each was discovered via a failed workflow dispatch. Per CLAUDE.md §"升级原则":
when a soft rule slips through review more than once, harden it. This script
hardens "did we forget a perm?" — it text-checks the addon CFN (and falls
back to the base OIDC CFN for inherited perms) for every action the
Lightsail provision / upgrade / smoke / decommission paths actually issue.

Why static (not iam:SimulatePrincipalPolicy)
============================================

`aws iam simulate-principal-policy` would be more semantically accurate (it
respects NotAction, conditions, deny statements, etc.), but it requires the
caller to have `iam:SimulatePrincipalPolicy` on the target role — which our
local IAM user doesn't have. Granting that perm is itself a one-shot manual
step that defeats the OPC "no manual setup" intent.

Static text match is good enough because:
- IAM policy action names are unique strings (no overlap risk)
- All our addon statements are `Effect: Allow` (no NotAction / Deny / Condition
  hazards that need policy-semantic evaluation)
- The same gate is what `scripts/checks/test_cfn_template_version.py
  LightsailAddonContractTests` already does for individual actions; this script
  generalizes that one-action-at-a-time pattern into a single sweep.

Exit codes
==========

  0  — every expected action is granted by addon OR base policy
  1  — at least one expected action is missing from both
  2  — input / file IO error

stdlib-only.
"""
from __future__ import annotations

import argparse
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
ADDON_CFN = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml"
BASE_CFN = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc.yaml"

# Hard-coded list of every AWS action the Lightsail Edge workflow issues.
# Maintenance contract: when provision-edge.sh / render-bootstrap.sh /
# deploy_via_ssm.sh / edge_post_deploy_smoke.sh / the workflow itself adds
# a new `aws <service> <command>` invocation that hits AWS via OIDC, append
# the corresponding action here in the same PR. The notes are operator-
# facing remediation hints when the gate fires.
EXPECTED_ACTIONS: list[tuple[str, str]] = [
    # --- SSM Hybrid Activation (provision step) -------------------------
    ("ssm:CreateActivation", "aws ssm create-activation — base"),
    ("ssm:AddTagsToResource", "aws ssm create-activation --tags inline call (PR #399)"),
    ("ssm:DeleteActivation", "cleanup path"),
    ("ssm:DescribeActivations", "audit / sanity read"),
    ("iam:PassRole", "aws ssm create-activation --iam-role tokenkey-lightsail-ssm-hybrid (PR #397)"),
    # --- Lightsail instance lifecycle ---------------------------------
    ("lightsail:GetInstance", "probe before create + post-create poll"),
    ("lightsail:CreateInstances", "the act of creating the instance"),
    ("lightsail:DeleteInstance", "recreate=true path"),
    ("lightsail:StopInstance", "recreate=true graceful stop"),
    # --- Lightsail Static IP lifecycle ---------------------------------
    ("lightsail:GetStaticIp", "probe before allocate + read for downstream"),
    ("lightsail:AllocateStaticIp", "first-provision path"),
    ("lightsail:AttachStaticIp", "bind IP to instance"),
    ("lightsail:DetachStaticIp", "recreate=true path"),
    ("lightsail:ReleaseStaticIp", "recreate=true path + ip rotation"),
    # --- SSM managed-instance ops after registration -------------------
    ("ssm:DescribeInstanceInformation", "poll for mi-* registration"),
    ("ssm:SendCommand", "upgrade / smoke / log fetch via Hybrid managed instance"),
    ("ssm:GetCommandInvocation", "read SendCommand stdout/stderr"),
    # --- SSM Parameter Store (state read/write) ------------------------
    ("ssm:PutParameter", "provision writes managed_instance_id / public_ip etc."),
    ("ssm:GetParameter", "upgrade / IP rotation read public_ip and instance id"),
]


def _load(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def _missing_actions(
    expected: list[tuple[str, str]],
    addon_text: str,
    base_text: str,
) -> list[tuple[str, str, str]]:
    """Return [(action, where_checked, notes)] for every expected action not
    present in either policy text.

    Substring match suffices because IAM action names are unique. We do NOT
    try to interpret `Effect: Deny` or condition gates here — within this
    codebase neither policy file uses Deny, and conditions only narrow
    (never widen) what an Allow grants."""
    missing: list[tuple[str, str, str]] = []
    for action, notes in expected:
        if action in addon_text:
            continue
        if action in base_text:
            continue
        missing.append((action, "addon+base", notes))
    return missing


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument(
        "--quiet",
        action="store_true",
        help="On success, print only one OK line",
    )
    args = parser.parse_args()

    try:
        addon_text = _load(ADDON_CFN)
    except OSError as exc:
        print(f"FAIL: cannot read {ADDON_CFN.relative_to(REPO_ROOT)}: {exc}", file=sys.stderr)
        return 2
    try:
        base_text = _load(BASE_CFN)
    except OSError as exc:
        print(f"FAIL: cannot read {BASE_CFN.relative_to(REPO_ROOT)}: {exc}", file=sys.stderr)
        return 2

    missing = _missing_actions(EXPECTED_ACTIONS, addon_text, base_text)
    if not missing:
        if not args.quiet:
            print(
                f"ok: {len(EXPECTED_ACTIONS)} expected action(s) granted by addon and/or base OIDC policy"
            )
        else:
            print(f"ok: lightsail OIDC perm coverage ({len(EXPECTED_ACTIONS)} actions)")
        return 0

    print(
        "FAIL: actions used by the Lightsail edge workflow are not granted by "
        "either addon or base OIDC role policy. The next workflow dispatch will "
        "AccessDenied on these:",
        file=sys.stderr,
    )
    for action, where, notes in missing:
        suffix = f" — {notes}" if notes else ""
        print(f"  - {action!r} (checked: {where}){suffix}", file=sys.stderr)
    print(
        f"  Fix by adding the action(s) to {ADDON_CFN.relative_to(REPO_ROOT)} "
        "(under the SsmHybridActivation or appropriate statement) and redeploying.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
