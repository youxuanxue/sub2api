"""Stage Batch COS on one application node before a rolling plan.

Run as root. Supply the two scoped MANAGED_BATCH_STORAGE_* credentials on
stdin as a JSON object. No secret is passed via argv or written to stdout.
Use --override to identify the release's site-specific compose.override.yml.
"""
import argparse
import json
import os
from pathlib import Path
import re
import tempfile


def write_private(path, value):
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as tmp:
        tmp.write(value)
        tmp.flush()
        os.fsync(tmp.fileno())
    os.replace(tmp.name, path)


def stage(root, source, credentials):
    keys = {"MANAGED_BATCH_STORAGE_ACCESS_KEY_ID", "MANAGED_BATCH_STORAGE_SECRET_ACCESS_KEY"}
    if set(credentials) != keys or not all(isinstance(v, str) and re.fullmatch(r"[a-zA-Z0-9]+", v) for v in credentials.values()):
        raise ValueError("expected exactly two nonempty CAM credential fields")
    pattern = r"(?m)^      MANAGED_BATCH_STORAGE_[A-Z_]+:.*\n"
    block = "".join(re.findall(pattern, source))
    if not all(key + ":" in block for key in keys) or "MANAGED_BATCH_STORAGE_BUCKET:" not in block:
        raise ValueError("release override has incomplete Batch storage settings")
    override = root / "compose.override.yml"
    env = root / ".env"
    existing = re.sub(pattern, "", override.read_text())
    start = re.search(r"(?m)^  tokenkey:\s*\n", existing)
    if not start:
        raise ValueError("host override lacks tokenkey service")
    rest = existing[start.end():]
    service = re.split(r"(?m)^  [\w-]+:", rest, maxsplit=1)[0]
    anchor = re.search(r"(?m)^    environment:\s*\n", service)
    if not anchor:
        raise ValueError("host tokenkey service lacks environment mapping")
    pos = start.end() + anchor.end()
    staged = existing[:pos] + block + existing[pos:]
    lines = [line for line in env.read_text().splitlines() if line.split("=", 1)[0] not in keys]
    lines.extend(f"{key}={credentials[key]}" for key in sorted(keys))
    write_private(env, "\n".join(lines) + "\n")
    write_private(override, staged)


if __name__ == "__main__":
    import sys
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", choices=("prod", "cn-prod"), required=True)
    parser.add_argument("--override", type=Path, required=True)
    args = parser.parse_args()
    root, bucket = {
        "prod": (Path("/var/lib/tokenkey"), "tokenkey-prod-batch-usw-1320330536"),
        "cn-prod": (Path("/opt/tokenkey"), "tokenkey-cnprod-batch-bj-1320330536"),
    }[args.site]
    source = args.override.read_text()
    if f'MANAGED_BATCH_STORAGE_BUCKET: "{bucket}"' not in source:
        parser.error("override does not match requested site")
    stage(root, source, json.load(sys.stdin))
    print("Batch COS settings staged. The next rolling recreate applies them.")
