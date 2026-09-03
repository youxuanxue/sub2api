#!/usr/bin/env python3
"""
check-gateway-tk.py — verify TokenKey gateway/service hotspot hooks.

Reads `scripts/sentinels/gateway-tk.json` and for each entry verifies:

  1. The file at `path` exists.
  2. Every literal string in `must_contain` appears at least once in the file.
  3. Every literal string in `must_not_contain` is absent from the file.

It also lints registry hygiene WITHIN each entry (anti-bloat guard):

  - duplicate `must_contain` needles in the same entry are noise;
  - a needle that is a substring of another needle in the same entry is
    vacuous (the longer literal present always implies the shorter one) —
    either drop it or strengthen it (e.g. `Name` → `Name(` / `func Name(` /
    `type Name struct`) so it pins a distinct symbol occurrence.

  Cross-entry overlap is intentionally NOT linted: two entries may share an
  anchor under different rationales, and a long needle in one entry must not
  excuse a short needle in another (entries evolve independently).

Exit codes:
  0  — all sentinels intact.
  1  — at least one sentinel missing, lost a required symbol, or failed
       registry hygiene.
  2  — registry file missing or malformed.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "sentinels" / "gateway-tk.json"


def load_registry() -> dict:
    if not REGISTRY_PATH.is_file():
        print(
            f"FATAL: registry file not found: {REGISTRY_PATH.relative_to(REPO_ROOT)}",
            file=sys.stderr,
        )
        sys.exit(2)
    try:
        with REGISTRY_PATH.open("r", encoding="utf-8") as f:
            data = json.load(f)
    except json.JSONDecodeError as exc:
        print(f"FATAL: registry file is not valid JSON: {exc}", file=sys.stderr)
        sys.exit(2)
    if "sentinels" not in data or not isinstance(data["sentinels"], list):
        print("FATAL: registry missing 'sentinels' array.", file=sys.stderr)
        sys.exit(2)
    return data


_CONTENT_CACHE: dict[str, str] = {}

_GO_FUNC_START_RE = re.compile(r"(?m)^func\b")
_GO_IDENT_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_FAILOVER_FIELD_RE = re.compile(r"(?m)^\s*(?:ShouldFailover|RetryNextAccount)\s+bool\b")
_LOCAL_NEXT_ACCOUNT_DECISION_RE = re.compile(r"\bNextAccount(?:Retry|Stop)\b")
_VERDICT_SEMANTIC_RE = re.compile(
    r"\bgatewayFailureSemantic(?:Retry|Failover|Stop|Terminal)[A-Za-z0-9_]*\b"
)


def read_file_cached(path_str: str) -> str:
    """Read a sentinel target file once even when many entries share the path
    (gateway_service.go alone is anchored by 15+ entries)."""
    if path_str not in _CONTENT_CACHE:
        _CONTENT_CACHE[path_str] = (REPO_ROOT / path_str).read_text(
            encoding="utf-8", errors="replace"
        )
    return _CONTENT_CACHE[path_str]


def lint_entry_hygiene(entry: dict) -> list[str]:
    """Within-entry anti-bloat lint; see module docstring."""
    needles = entry.get("must_contain") or []
    problems: list[str] = []
    seen: set[str] = set()
    for n in needles:
        if n in seen:
            problems.append(f"duplicate needle in same entry: `{n}`")
        seen.add(n)
    for a in sorted(seen):
        for b in sorted(seen):
            if a != b and a in b:
                problems.append(
                    f"vacuous needle `{a}` is a substring of sibling needle `{b}` "
                    "— drop it or strengthen it to pin a distinct symbol"
                )
    return problems


def check_sentinel(entry: dict) -> tuple[bool, list[str]]:
    path_str = entry.get("path")
    if not path_str:
        return False, ["entry missing 'path'"]
    file_path = REPO_ROOT / path_str
    if not file_path.is_file():
        return False, [f"file missing: {path_str}"]
    failures: list[str] = lint_entry_hygiene(entry)
    must_contain = entry.get("must_contain") or []
    must_not_contain = entry.get("must_not_contain") or []
    if not must_contain and not must_not_contain:
        return (len(failures) == 0), failures
    try:
        content = read_file_cached(path_str)
    except OSError as exc:
        return False, [f"cannot read {path_str}: {exc}"]
    for needle in must_contain:
        if needle not in content:
            failures.append(f"missing literal `{needle}` in {path_str}")
    for needle in must_not_contain:
        if needle in content:
            failures.append(f"forbidden literal `{needle}` still present in {path_str}")
    return (len(failures) == 0), failures


def _strip_go_comments_and_literals(source: str) -> str:
    """Blank comments and literals while preserving offsets and newlines."""
    chars = list(source)
    i = 0
    state = "code"
    while i < len(chars):
        ch = chars[i]
        nxt = chars[i + 1] if i + 1 < len(chars) else ""
        if state == "code":
            if ch == "/" and nxt == "/":
                chars[i] = chars[i + 1] = " "
                i += 2
                state = "line_comment"
                continue
            if ch == "/" and nxt == "*":
                chars[i] = chars[i + 1] = " "
                i += 2
                state = "block_comment"
                continue
            if ch in {'"', "'", "`"}:
                chars[i] = " "
                state = {"\"": "string", "'": "rune", "`": "raw"}[ch]
        elif state == "line_comment":
            if ch == "\n":
                state = "code"
            else:
                chars[i] = " "
        elif state == "block_comment":
            if ch == "*" and nxt == "/":
                chars[i] = chars[i + 1] = " "
                i += 2
                state = "code"
                continue
            if ch != "\n":
                chars[i] = " "
        elif state == "raw":
            if ch == "`":
                chars[i] = " "
                state = "code"
            elif ch != "\n":
                chars[i] = " "
        else:
            if ch == "\\":
                chars[i] = " "
                if i + 1 < len(chars):
                    if chars[i + 1] != "\n":
                        chars[i + 1] = " "
                    i += 2
                    continue
            if (state == "string" and ch == '"') or (state == "rune" and ch == "'"):
                chars[i] = " "
                state = "code"
            elif ch != "\n":
                chars[i] = " "
        i += 1
    return "".join(chars)


def _skip_space(source: str, pos: int) -> int:
    while pos < len(source) and source[pos].isspace():
        pos += 1
    return pos


def _after_balanced(source: str, pos: int, opening: str, closing: str) -> int | None:
    if pos >= len(source) or source[pos] != opening:
        return None
    depth = 0
    for index in range(pos, len(source)):
        if source[index] == opening:
            depth += 1
        elif source[index] == closing:
            depth -= 1
            if depth == 0:
                return index + 1
    return None


def _go_functions(source: str) -> list[tuple[str, int, str]]:
    """Return top-level Go function names, offsets, and sanitized bodies."""
    clean = _strip_go_comments_and_literals(source)
    functions: list[tuple[str, int, str]] = []
    for start in _GO_FUNC_START_RE.finditer(clean):
        pos = _skip_space(clean, start.end())
        if pos < len(clean) and clean[pos] == "(":
            pos = _after_balanced(clean, pos, "(", ")") or len(clean)
            pos = _skip_space(clean, pos)
        name_match = _GO_IDENT_RE.match(clean, pos)
        if name_match is None:
            continue
        name = name_match.group(0)
        pos = _skip_space(clean, name_match.end())
        params_end = _after_balanced(clean, pos, "(", ")")
        if params_end is None:
            continue
        body_start = clean.find("{", params_end)
        if body_start < 0:
            continue
        body_end = _after_balanced(clean, body_start, "{", "}")
        if body_end is None:
            continue
        functions.append((name, start.start(), clean[body_start:body_end]))
    return functions


def _has_direct_classifier_return(body: str, classifier: str) -> bool:
    for match in re.finditer(rf"\breturn\s+{re.escape(classifier)}\s*", body):
        pos = _skip_space(body, match.end())
        call_end = _after_balanced(body, pos, "(", ")")
        if call_end is None:
            continue
        pos = _skip_space(body, call_end)
        suffix = ".RetryNextAccount"
        if not body.startswith(suffix, pos):
            continue
        pos = _skip_space(body, pos + len(suffix))
        if pos == len(body) or body[pos] in {"\n", ";", "}"}:
            return True
    return False


def _failover_function_violation(name: str, body: str) -> str | None:
    lower_name = name.lower()
    if "shouldfailover" not in lower_name and "retrynextaccount" not in lower_name:
        return None
    classifier = (
        "classifyGatewayFailoverError"
        if name == "ShouldRetryNextAccount"
        else "classifyGatewayFailover"
    )
    if _has_direct_classifier_return(body, classifier):
        return None
    return f"{name} must directly return {classifier}(...).RetryNextAccount"


def _failover_scanner_selftest() -> list[str]:
    fixture = """
func (s *Service) shouldFailoverGood() bool {
    return classifyGatewayFailover(gatewayFailoverObservation{}).RetryNextAccount
}
func (s *Service) shouldFailoverNoopCall() bool {
    _ = classifyGatewayFailover(gatewayFailoverObservation{}).RetryNextAccount
    return false
}
func (
    s *Service
) shouldFailoverSpoofed() bool {
    // classifyGatewayFailover(gatewayFailoverObservation{})
    return false
}
func retryNextAccountHidden() bool { return false }
func (e *Error) ShouldRetryNextAccount() bool {
    return classifyGatewayFailoverError(e).RetryNextAccount
}
"""
    functions = {name: body for name, _, body in _go_functions(fixture)}
    failures: list[str] = []
    if _failover_function_violation("shouldFailoverGood", functions.get("shouldFailoverGood", "")):
        failures.append("failover scanner self-test lost a real policy call")
    if not _failover_function_violation("shouldFailoverNoopCall", functions.get("shouldFailoverNoopCall", "")):
        failures.append("failover scanner self-test accepted a no-op policy call")
    if not _failover_function_violation("shouldFailoverSpoofed", functions.get("shouldFailoverSpoofed", "")):
        failures.append("failover scanner self-test accepted a comment-spoofed policy call")
    if not _failover_function_violation("retryNextAccountHidden", functions.get("retryNextAccountHidden", "")):
        failures.append("failover scanner self-test missed an alternate decision name")
    if _failover_function_violation("ShouldRetryNextAccount", functions.get("ShouldRetryNextAccount", "")):
        failures.append("failover scanner self-test rejected the runtime policy choke point")
    action_fixture = _strip_go_comments_and_literals(
        """type x struct {
    RetryNextAccount bool
}
var _ = E{NextAccountAction: NextAccountRetry}
var _ = gatewayFailureSemanticRetryableAccount
"""
    )
    if (
        not _FAILOVER_FIELD_RE.search(action_fixture)
        or not _LOCAL_NEXT_ACCOUNT_DECISION_RE.search(action_fixture)
        or not _VERDICT_SEMANTIC_RE.search(action_fixture)
    ):
        failures.append("failover scanner self-test missed a local decision field, action, or verdict semantic")
    return failures


def check_failover_ssot() -> tuple[bool, list[str]]:
    """Reject new adapter-local retry-next-account policy owners.

    Provider adapters may parse payloads and expose a shouldFailover facade for
    existing callers, but every such facade must directly submit an observation
    to classifyGatewayFailover. Splitting by top-level Go function declaration
    keeps this check deterministic without depending on a local Go toolchain.
    """
    service_dir = REPO_ROOT / "backend" / "internal" / "service"
    failures: list[str] = _failover_scanner_selftest()
    for file_path in sorted(service_dir.glob("*.go")):
        if file_path.name.endswith("_test.go"):
            continue
        source = file_path.read_text(encoding="utf-8", errors="replace")
        clean = _strip_go_comments_and_literals(source)
        rel = file_path.relative_to(REPO_ROOT)
        for name, offset, body in _go_functions(source):
            violation = _failover_function_violation(name, body)
            if violation:
                failures.append(
                    f"{rel}:{source.count(chr(10), 0, offset) + 1} "
                    f"{violation}"
                )
        if file_path.name != "gateway_failover_policy.go" and _FAILOVER_FIELD_RE.search(clean):
            failures.append(
                f"{rel} declares a failover decision bool; keep it in the global decision owner"
            )
        action_owner = file_path.name in {
            "gateway_failover_policy.go",
            "gateway_service.go",
        }
        if not action_owner and _LOCAL_NEXT_ACCOUNT_DECISION_RE.search(clean):
            failures.append(
                f"{rel} owns a local retry/stop action; submit a normalized semantic to applyGatewayFailoverSemantic"
            )
        if _VERDICT_SEMANTIC_RE.search(clean):
            failures.append(
                f"{rel} names a failure semantic as a retry/stop verdict; submit factual evidence to the global policy"
            )
    return (len(failures) == 0), failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="emit a machine-readable JSON report")
    args = parser.parse_args()

    registry = load_registry()
    sentinels = registry["sentinels"]

    results = []
    fail_count = 0
    for entry in sentinels:
        ok, failures = check_sentinel(entry)
        if not ok:
            fail_count += 1
        results.append(
            {
                "path": entry.get("path"),
                "ok": ok,
                "failures": failures,
                "rationale": entry.get("rationale", ""),
            }
        )

    failover_ok, failover_failures = check_failover_ssot()
    if not failover_ok:
        fail_count += 1
    results.append(
        {
            "path": "backend/internal/service (global failover SSOT invariant)",
            "ok": failover_ok,
            "failures": failover_failures,
            "rationale": (
                "Every production failover facade and handler choke point must "
                "delegate to the global policy; protocol classifiers cannot own "
                "a decision boolean or retry/stop action."
            ),
        }
    )

    if args.json:
        json.dump({"total": len(results), "failed": fail_count, "results": results}, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(
                f"gateway TK sentinels: checking {len(sentinels)} registered entries "
                f"from {REGISTRY_PATH.relative_to(REPO_ROOT)} plus global invariants"
            )
        for r in results:
            if r["ok"]:
                if not args.quiet:
                    print(f"  ok: {r['path']}")
            else:
                print(f"  FAIL: {r['path']}")
                for msg in r["failures"]:
                    print(f"        - {msg}")
                if r["rationale"]:
                    print(f"        why it matters: {r['rationale']}")
        if fail_count == 0:
            if not args.quiet:
                print(f"gateway TK sentinels: PASS ({len(results)}/{len(results)} intact)")
        else:
            print(
                f"gateway TK sentinels: FAIL ({fail_count}/{len(results)} regressed)",
                file=sys.stderr,
            )
            print("  Source of truth: scripts/sentinels/gateway-tk.json", file=sys.stderr)
            print(
                "  If a hook was intentionally moved/renamed, update the registry "
                "in the same commit. Do NOT silently delete entries.",
                file=sys.stderr,
            )

    return 0 if fail_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
