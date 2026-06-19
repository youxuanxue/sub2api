"""Load TK_SMOKE_* GitHub Environment variables (mirrors load_smoke_github_env.sh)."""
from __future__ import annotations

import json
import os
import subprocess
import sys
from typing import Final

# Keep in sync with required_secrets_for_env() in ops/stage0/load_smoke_github_env.sh
# (all three prod smoke keys are hard prerequisites of deploy-stage0.yml).
PROD_SECRETS: Final[tuple[str, ...]] = (
    "TK_SMOKE_PROD_ANTHROPIC_KEY",
    "TK_SMOKE_PROD_GEMINI_KEY",
    "TK_SMOKE_PROD_OPENAI_OAUTH_KEY",
)
EDGE_SECRETS: Final[tuple[str, ...]] = ("TK_SMOKE_EDGE_CANARY_KEY",)


def required_secrets(env_name: str) -> tuple[str, ...]:
    if env_name == "prod":
        return PROD_SECRETS
    if env_name.startswith("edge-"):
        return EDGE_SECRETS
    raise ValueError(f"unsupported GitHub Environment: {env_name!r} (want prod or edge-<id>)")


def _gh_json(args: list[str]) -> object:
    proc = subprocess.run(
        ["gh", *args],
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or proc.stdout.strip() or "gh failed")
    return json.loads(proc.stdout)


def resolve_repo(repo_root: str) -> str:
    proc = subprocess.run(
        ["gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
        cwd=repo_root,
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError("gh cannot resolve GitHub repo")
    repo = proc.stdout.strip()
    if not repo:
        raise RuntimeError("empty repo slug from gh repo view")
    return repo


def fetch_tk_smoke_variables(repo: str, env_name: str) -> dict[str, str]:
    # Single request, no --paginate: the endpoint returns ONE object
    # ({"total_count": N, "variables": [...]}), and `gh api --paginate` on an
    # object-shaped endpoint concatenates raw objects that json.loads cannot
    # parse as a list (the old code asserted list and died with "unexpected gh
    # variables response" even on a healthy single page). per_page=100 is far
    # above any realistic TK_SMOKE_* variable count.
    payload = _gh_json(
        ["api", f"repos/{repo}/environments/{env_name}/variables?per_page=100"]
    )
    pages = payload if isinstance(payload, list) else [payload]
    out: dict[str, str] = {}
    for page in pages:
        if not isinstance(page, dict):
            raise RuntimeError("unexpected gh variables response")
        for item in page.get("variables", []):
            name = str(item.get("name", ""))
            if name.startswith("TK_SMOKE_"):
                out[name] = str(item.get("value", ""))
    return out


def secret_configured(repo: str, env_name: str, secret_name: str) -> bool:
    proc = subprocess.run(
        ["gh", "api", f"repos/{repo}/environments/{env_name}/secrets/{secret_name}"],
        check=False,
        capture_output=True,
        text=True,
    )
    return proc.returncode == 0


def load_github_env(env_name: str, *, repo_root: str | None = None, check_only: bool = False) -> dict[str, str]:
    root = repo_root or os.getcwd()
    repo = resolve_repo(root)
    variables = fetch_tk_smoke_variables(repo, env_name)

    for secret in required_secrets(env_name):
        if not check_only and os.environ.get(secret, "").strip():
            continue
        if not secret_configured(repo, env_name, secret):
            raise RuntimeError(
                f"secret {secret} not configured on GitHub Environment {env_name}"
            )

    if check_only:
        return variables

    missing = [
        secret
        for secret in required_secrets(env_name)
        if not os.environ.get(secret, "").strip()
    ]
    if missing:
        names = ", ".join(missing)
        raise RuntimeError(
            "GitHub Environment secrets are not readable via gh/API; "
            f"export locally: {names}"
        )

    return variables


def apply_github_env(env_name: str, *, repo_root: str | None = None) -> dict[str, str]:
    if os.environ.get("_TK_SMOKE_GH_LOADED") == "1":
        return {}
    variables = load_github_env(env_name, repo_root=repo_root)
    for key, value in variables.items():
        os.environ.setdefault(key, value)
    os.environ["_TK_SMOKE_GH_LOADED"] = "1"
    return variables


def main(argv: list[str] | None = None) -> int:
    args = list(argv if argv is not None else sys.argv[1:])
    check_only = False
    if args and args[0] == "--check":
        check_only = True
        args = args[1:]
    if len(args) != 1:
        print("usage: load_smoke_github_env.py [--check] <github-environment>", file=sys.stderr)
        return 1

    env_name = args[0]
    try:
        variables = load_github_env(env_name, check_only=check_only)
    except RuntimeError as exc:
        print(f"tk_load_smoke_github_env: {exc}", file=sys.stderr)
        return 1

    if check_only:
        print(f"tk_load_smoke_github_env: OK environment={env_name}", file=sys.stderr)
        return 0

    for key, value in sorted(variables.items()):
        print(f"export {key}={json.dumps(value)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
