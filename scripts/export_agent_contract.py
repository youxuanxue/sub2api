#!/usr/bin/env python3
"""Audit the agent-facing HTTP contract for sub2api.

Required by `dev-rules/agent-contract-enforcement.mdc` and the
`docs/approved/newapi-as-fifth-platform.md` §5.3 acceptance gate.

# Why this is an *audit* tool, not a *generator* (yet)

`docs/agent_integration.md` claims to be "Generated from live Gin route
registrations". A naive literal-path extractor over `routes/*.go` works
for routes registered on the top-level `r`, but Gin's nested
`router.Group("/admin").Group("/accounts")` pattern means many endpoints
appear in source as bare paths like `/:id` — a generator that does not
resolve group prefixes would *replace* the existing curated paths with
truncated ones, regressing the doc.

Doing prefix resolution properly requires either:

  1. A Go AST walker that follows `<grp> := <parent>.Group("/x")` chains
     across helper functions (`registerXxxRoutes(admin, h)`); or
  2. A runtime route dump from `gin.Engine.Routes()` after wiring the
     real handlers — needs Wire DI + stubs for every dependency.

Both are larger tasks than this script is scoped for; if route churn makes
the soft count warning noisy, implement a Go AST walker or runtime route dump.

# What this script DOES enforce today

Four cheap, high-signal contract guards that catch the regressions we
have actually seen:

  A) **Notes-section coverage**: every TokenKey first-class platform
     must be mentioned in the
     hand-maintained `# Agent Contract Notes` tail of the doc. This is
     the test that catches "we shipped a fifth platform but forgot to
     tell agents about it".

  B) **Live CLI projection**: import the argparse parser factories for the
     modelops and account-model-mapping manager entrypoints, render their
     commands/options into the `## CLI` section, and hard-fail `--check` on
     drift.

  C) **Route-count drift sanity**: count the literal `<ident>.METHOD(`
     registrations under `backend/internal/server/routes/*.go` and
     compare against the count of bulleted lines in the existing doc. Any large delta (default ±10%) prints a warning so
     the next maintainer regenerates the doc by hand. This is a
     soft signal, not a hard fail — the prefix-resolution debt makes
     hard-fail premature.

  D) **Retired-route tombstones**: security-sensitive contract removals are
     registered once with their source literal and replacement. Generation
     removes stale inventory bullets; `--check` hard-fails if either the
     documentation or the cited source resurrects a retired route.

Usage::

    python3 scripts/export_agent_contract.py            # refresh CLI section
    python3 scripts/export_agent_contract.py --check    # CI drift gate

`--check` exits 1 on Notes coverage, generated projection drift, or retired
route resurrection; the count
warning (C) never blocks. This is intentional: route docs lag by a
few PRs in healthy projects, and we do not want the gate so strict that
it becomes the thing devs route around. We reserve hard-fail for "doc
forgot a whole platform".
"""
from __future__ import annotations

import argparse
import importlib.util
import re
import sys
from pathlib import Path
from typing import Iterable

REPO_ROOT = Path(__file__).resolve().parent.parent
ROUTES_DIR = REPO_ROOT / "backend" / "internal" / "server" / "routes"
DOC_PATH = REPO_ROOT / "docs" / "agent_integration.md"

HTTP_VERBS = ("GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS")
ROUTE_PATTERN = re.compile(
    r"\b\w+\.(?:" + "|".join(HTTP_VERBS) + r")\("
)
HANDLE_PATTERN = re.compile(
    r'\b\w+\.Handle\(\s*"(?:' + "|".join(HTTP_VERBS) + r')"\s*,'
)
DOC_BULLET = re.compile(r"^- `(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) ", re.MULTILINE)
DOC_ROUTE_BULLET = re.compile(
    r"^- `(?P<method>GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) "
    r"(?P<path>[^`]+)` from `(?P<source>[^`]+)`\n?",
    re.MULTILINE,
)
NOTES_MARKER = "# Agent Contract Notes"
CLI_START_MARKER = "## CLI"
CLI_END_MARKER = "## MCP"

CLI_ENTRYPOINTS = (
    ("ops/pricing/modelops.py", "build_parser"),
    ("ops/pricing/manage-account-model-mapping-runtime.py", "_build_parser"),
)

# TokenKey first-class platforms — the doc Notes section MUST mention each
# one so an agent reading the contract knows what gateway surface exists.
# Source of truth for the canonical names:
#   backend/internal/domain/constants.go (PlatformOpenAI, PlatformAnthropic,
#   PlatformGemini, PlatformAntigravity, PlatformNewAPI, PlatformKiro,
#   PlatformGrok).
REQUIRED_PLATFORMS = ("openai", "anthropic", "gemini", "antigravity", "newapi", "kiro", "grok")

# Public contracts intentionally removed for security or compatibility reasons.
# The source literal is the leaf path used inside its Gin route group.
RETIRED_HTTP_ROUTES = (
    {
        "method": "GET",
        "path": "/payment/channels",
        "source": "backend/internal/server/routes/payment.go",
        "source_literal": "/channels",
        "replacement": "/payment/checkout-info",
    },
)

COUNT_TOLERANCE = 0.10  # ±10% considered noise


def count_source_registrations() -> int:
    n = 0
    for go_file in sorted(ROUTES_DIR.rglob("*.go")):
        if go_file.name.endswith("_test.go"):
            continue
        text = go_file.read_text(encoding="utf-8")
        n += len(ROUTE_PATTERN.findall(text))
        n += len(HANDLE_PATTERN.findall(text))
    return n


def count_doc_bullets(doc: str) -> int:
    return len(DOC_BULLET.findall(doc))


def prune_retired_route_bullets(
    doc: str, retired_routes: Iterable[dict[str, str]] = RETIRED_HTTP_ROUTES
) -> str:
    retired = {(route["method"], route["path"]) for route in retired_routes}

    def replace(match: re.Match[str]) -> str:
        key = (match.group("method"), match.group("path"))
        return "" if key in retired else match.group(0)

    return DOC_ROUTE_BULLET.sub(replace, doc)


def find_retired_route_bullets(
    doc: str, retired_routes: Iterable[dict[str, str]] = RETIRED_HTTP_ROUTES
) -> list[dict[str, str]]:
    documented = {
        (match.group("method"), match.group("path"))
        for match in DOC_ROUTE_BULLET.finditer(doc)
    }
    return [
        route
        for route in retired_routes
        if (route["method"], route["path"]) in documented
    ]


def find_retired_source_registrations(
    repo_root: Path = REPO_ROOT,
    retired_routes: Iterable[dict[str, str]] = RETIRED_HTTP_ROUTES,
) -> list[dict[str, str]]:
    resurrected: list[dict[str, str]] = []
    for route in retired_routes:
        source_path = repo_root / route["source"]
        if not source_path.exists():
            continue
        source = source_path.read_text(encoding="utf-8")
        method = re.escape(route["method"])
        literal = re.escape(route["source_literal"])
        direct = re.compile(rf'\.{method}\(\s*"{literal}"')
        handle = re.compile(rf'\.Handle\(\s*"{method}"\s*,\s*"{literal}"')
        if direct.search(source) or handle.search(source):
            resurrected.append(route)
    return resurrected


def check_notes_coverage(doc: str, required: Iterable[str]) -> list[str]:
    """Return platforms missing from the `# Agent Contract Notes` tail.

    Pure substring match (case-insensitive) — we only require that the
    word appears somewhere in the Notes section. The point is "did the
    author at least acknowledge this platform exists in the contract?";
    deeper structure can come later.
    """
    idx = doc.find(NOTES_MARKER)
    notes = doc[idx:].lower() if idx >= 0 else ""
    missing: list[str] = []
    for name in required:
        if name.lower() not in notes:
            missing.append(name)
    return missing


def _load_argparse_parser(rel_path: str, factory_name: str) -> argparse.ArgumentParser:
    path = REPO_ROOT / rel_path
    module_name = "agent_contract_" + re.sub(r"[^a-zA-Z0-9_]", "_", rel_path)
    spec = importlib.util.spec_from_file_location(module_name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load CLI entrypoint {rel_path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    factory = getattr(module, factory_name, None)
    if not callable(factory):
        raise RuntimeError(f"{rel_path}: parser factory {factory_name} is not callable")
    parser = factory()
    if not isinstance(parser, argparse.ArgumentParser):
        raise RuntimeError(f"{rel_path}: {factory_name} did not return ArgumentParser")
    return parser


def _display_default(value: object) -> str | None:
    if value in (None, False, [], argparse.SUPPRESS):
        return None
    if callable(value):
        return None
    if isinstance(value, Path):
        try:
            return str(value.resolve().relative_to(REPO_ROOT))
        except ValueError:
            return str(value)
    return str(value)


def _format_action(action: argparse.Action) -> str:
    names = " / ".join(f"`{name}`" for name in action.option_strings)
    if not names:
        names = f"`{action.dest}`"
    attributes: list[str] = []
    if action.required:
        attributes.append("required")
    if isinstance(action, argparse._AppendAction):
        attributes.append("repeatable")
    if action.choices:
        attributes.append("choices: " + ", ".join(f"`{choice}`" for choice in action.choices))
    default = _display_default(action.default)
    if default is not None:
        attributes.append(f"default: `{default}`")
    suffix = f" ({'; '.join(attributes)})" if attributes else ""
    help_text = (action.help or "").strip()
    return f"- {names}{suffix}: {help_text}".rstrip()


def _parser_options(parser: argparse.ArgumentParser) -> list[argparse.Action]:
    return [
        action
        for action in parser._actions
        if (
            not isinstance(action, argparse._SubParsersAction)
            and action.dest != "help"
            and action.help != argparse.SUPPRESS
        )
    ]


def _exclusive_constraints(parser: argparse.ArgumentParser) -> list[str]:
    constraints: list[str] = []
    for group in parser._mutually_exclusive_groups:
        names = [action.option_strings[0] for action in group._group_actions if action.option_strings]
        if len(names) < 2:
            continue
        requirement = "exactly one" if group.required else "at most one"
        constraints.append(f"- Constraint: {requirement} of " + ", ".join(f"`{name}`" for name in names) + ".")
    return constraints


def render_cli_contract() -> str:
    lines = [CLI_START_MARKER, "", "Generated from the live argparse parser factories; do not edit this section."]
    for rel_path, factory_name in CLI_ENTRYPOINTS:
        parser = _load_argparse_parser(rel_path, factory_name)
        command_name = Path(rel_path).name
        lines.extend(["", f"### `python3 {rel_path}`"])
        root_options = _parser_options(parser)
        if root_options:
            lines.extend(["", "Root options:"])
            lines.extend(_format_action(action) for action in root_options)
        subparsers = next(
            (action for action in parser._actions if isinstance(action, argparse._SubParsersAction)),
            None,
        )
        if subparsers is None:
            continue
        help_by_name = {
            choice.dest: choice.help
            for choice in subparsers._choices_actions
        }
        for name, child in subparsers.choices.items():
            help_text = help_by_name.get(name) or ""
            lines.extend(["", f"#### `{command_name} {name}`", "", help_text])
            options = _parser_options(child)
            if options:
                lines.append("")
                lines.extend(_format_action(action) for action in options)
            constraints = _exclusive_constraints(child)
            if constraints:
                lines.append("")
                lines.extend(constraints)
    return "\n".join(lines).rstrip() + "\n"


def replace_cli_contract(doc: str, cli_contract: str) -> str:
    start = doc.find(CLI_START_MARKER)
    end = doc.find(CLI_END_MARKER, start + len(CLI_START_MARKER))
    if start < 0 or end < 0:
        raise RuntimeError(f"{DOC_PATH.relative_to(REPO_ROOT)} must contain {CLI_START_MARKER!r} before {CLI_END_MARKER!r}")
    return doc[:start] + cli_contract + "\n" + doc[end:]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 on Notes coverage or generated CLI contract drift",
    )
    args = parser.parse_args()

    if not DOC_PATH.exists():
        sys.stderr.write(f"FAIL: {DOC_PATH.relative_to(REPO_ROOT)} not found\n")
        return 1

    doc = DOC_PATH.read_text(encoding="utf-8")
    resurrected_routes = find_retired_source_registrations()
    if resurrected_routes:
        for route in resurrected_routes:
            sys.stderr.write(
                f"FAIL: retired route was re-registered in {route['source']}: "
                f"{route['method']} {route['path']}; replacement is "
                f"{route['replacement']}.\n"
            )
        return 1
    try:
        expected_doc = prune_retired_route_bullets(
            replace_cli_contract(doc, render_cli_contract())
        )
    except RuntimeError as e:
        sys.stderr.write(f"FAIL: {e}\n")
        return 1
    generated_drift = expected_doc != doc
    stale_retired_routes = find_retired_route_bullets(doc)
    if generated_drift and args.check:
        sys.stderr.write(
            "FAIL: generated agent contract drifted. "
            "Run: python3 scripts/export_agent_contract.py\n"
        )
    elif generated_drift:
        DOC_PATH.write_text(expected_doc, encoding="utf-8")
        doc = expected_doc
        print("updated docs/agent_integration.md generated contract")
    src_count = count_source_registrations()
    doc_count = count_doc_bullets(doc)
    missing = check_notes_coverage(doc, REQUIRED_PLATFORMS)

    print(f"agent_integration.md  : {doc_count} HTTP route bullets")
    print(f"routes/*.go (source)  : {src_count} <ident>.METHOD(...) registrations")
    if doc_count == 0:
        delta_pct = 100.0
    else:
        delta_pct = abs(doc_count - src_count) / max(doc_count, 1) * 100
    if delta_pct > COUNT_TOLERANCE * 100:
        sys.stderr.write(
            f"WARN: doc/source route-count drift = {delta_pct:.1f}% "
            f"(>{COUNT_TOLERANCE*100:.0f}% tolerance). The Go-AST or runtime "
            f"route-dump generator follow-up "
            f"would resolve this — for now, audit by hand if you added or "
            f"removed routes.\n"
        )

    if missing:
        sys.stderr.write(
            "FAIL: Notes section is missing required TokenKey platforms: "
            f"{', '.join(missing)}.\n"
            f"Edit {DOC_PATH.relative_to(REPO_ROOT)} (the `{NOTES_MARKER}` "
            "tail) so every first-class platform is acknowledged.\n"
        )
        return 1

    if stale_retired_routes and args.check:
        for route in stale_retired_routes:
            sys.stderr.write(
                f"FAIL: retired route remains in agent contract: "
                f"{route['method']} {route['path']}; replacement is "
                f"{route['replacement']}.\n"
            )
        return 1

    if generated_drift and args.check:
        return 1

    if args.check:
        print(f"OK: every required platform ({', '.join(REQUIRED_PLATFORMS)}) is in Notes.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
