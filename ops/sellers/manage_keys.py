#!/usr/bin/env python3
"""Plan or ensure named universal keys on the existing seller billing user."""
import argparse
import json

from export_offers import PLATFORMS, fetch_snapshot


def key_plan(keys):
    plan = []
    for name in PLATFORMS:
        matches = [k for k in keys if k["name"] == name]
        if not matches:
            plan.append({"name": name, "action": "create", "routing_mode": "universal"})
            continue
        if len(matches) != 1 or matches[0]["status"] != "active" or matches[0]["routing_mode"] != "universal" or matches[0]["group_id"] is not None:
            raise ValueError("existing key requires reconciliation: " + name)
        plan.append({"name": name, "action": "reuse", "id": matches[0]["id"], "routing_mode": "universal"})
    return plan


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="create missing keys; no changes to existing keys/user/groups/rates")
    args = parser.parse_args()
    before = fetch_snapshot()
    plan = key_plan(before["api_keys"])
    if args.apply and any(p["action"] == "create" for p in plan):
        after = fetch_snapshot(ensure_keys=True)
        for field in ("user", "groups"):
            if after[field] != before[field]:
                raise RuntimeError("seller policy changed while provisioning; reconcile before continuing")
        plan = key_plan(after["api_keys"])
        if any(p["action"] != "reuse" for p in plan):
            raise RuntimeError("provisioning incomplete")
    print(json.dumps({"applied": args.apply, "billing_user": before["user"], "keys": plan}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
