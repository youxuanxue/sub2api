#!/usr/bin/env python3
"""Export OpenRouter provider /v1/models JSON using a live TokenKey OR service key.

Usage:
  export TK_OR_PROVIDER_KEY=sk-...
  python3 ops/pricing/export-openrouter-provider-models.py \
    --base-url https://api.tokenkey.dev

Writes OpenRouter provider onboarding payload to stdout.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request


def main() -> int:
    parser = argparse.ArgumentParser(description="Fetch TokenKey OpenRouter provider model catalog")
    parser.add_argument("--base-url", default=os.environ.get("TK_BASE_URL", "https://api.tokenkey.dev"))
    parser.add_argument("--api-key", default=os.environ.get("TK_OR_PROVIDER_KEY", ""))
    args = parser.parse_args()

    api_key = (args.api_key or "").strip()
    if not api_key:
        print("TK_OR_PROVIDER_KEY (or --api-key) is required", file=sys.stderr)
        return 2

    base = args.base_url.rstrip("/")
    url = f"{base}/openrouter/v1/models"
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {api_key}"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        print(f"HTTP {exc.code}: {body}", file=sys.stderr)
        return 1
    except urllib.error.URLError as exc:
        print(f"request failed: {exc}", file=sys.stderr)
        return 1

    json.dump(payload, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
