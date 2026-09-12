"""Read-only Edge release gate: signed handoff requires an isolated serving prod."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import secrets
import subprocess
import sys
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
PROD_ORIGIN = "https://tokenkey.dev"
HEADER = "X-TokenKey-Handoff-Isolation"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def requires_isolation(tag: str) -> bool:
    subprocess.run(["bash", str(ROOT / "ops/stage0/validate-deploy-tag.sh"), tag],
                   check=True, stdout=subprocess.DEVNULL)
    ref = f"refs/tags/v{tag}"
    # Resolve the image's release source, not the workflow checkout or a version floor.
    subprocess.run(["git", "fetch", "--no-tags", "origin", f"{ref}:{ref}"],
                   cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
    source = subprocess.check_output(
        ["git", "show", f"{ref}:backend/internal/server/routes/edge_tk_routes.go"],
        cwd=ROOT, text=True)
    return '"/edge/admin-handoff/mint"' in source


def probe_serving_prod(origin: str = PROD_ORIGIN) -> dict:
    request = urllib.request.Request(
        origin + "/api/v1/edge/admin-handoff/configuration?release_probe=" + secrets.token_hex(8),
        headers={"Cache-Control": "no-cache", "Accept": "application/json"})
    opener = urllib.request.build_opener(NoRedirect())
    try:
        response = opener.open(request, timeout=10)
    except urllib.error.HTTPError as exc:
        response = exc
    with response:
        status = response.code
        isolated = (status in (200, 503)
                    and response.headers.get(HEADER) == "1"
                    and response.headers.get("Cache-Control") == "no-store")
    return {"origin": origin, "status": status, "isolated": isolated}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True, help="Edge image tag without v")
    args = parser.parse_args()
    try:
        if not requires_isolation(args.tag):
            print(json.dumps({"verdict": "not_required", "tag": args.tag}))
            return 0
        result = probe_serving_prod()
        result.update(tag=args.tag, verdict="pass" if result["isolated"] else "blocked")
        print(json.dumps(result))
        if result["isolated"]:
            return 0
        print("Edge rollout blocked: deploy and promote isolated prod first; "
              "an inactive container or /health success is insufficient.", file=sys.stderr)
    except (OSError, subprocess.CalledProcessError, urllib.error.URLError, ValueError):
        # No response bodies, credentials or subprocess output in the gate result.
        print(json.dumps({"verdict": "blocked", "reason": "could_not_verify_serving_prod_or_release"}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
