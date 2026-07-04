#!/usr/bin/env python3
"""platform-registry-drift.py — Go ↔ TS platform registry lockstep guard.

The platform registry is written by hand on BOTH sides of the wire and the
comments merely *ask* for lockstep. This check makes the ask mechanical.
Eleven mirrored pairs, backend Go is the single source of truth:

  1. OpenAI-compat platforms
       backend/internal/engine/provider.go        OpenAICompatPlatforms()
       frontend/src/constants/gatewayPlatforms.ts OPENAI_COMPAT_PLATFORMS
     (preflight's "newapi compat-pool drift" gate only covers the backend
     half; this closes the frontend half.)

  2. Group dispatch-config platforms
       backend/internal/service/openai_messages_dispatch_tk_newapi.go
         tkGroupKeepsDispatchConfig  (= compat set ∪ explicit `g.Platform ==
         PlatformX` comparisons in the predicate body)
       frontend/src/constants/gatewayPlatforms.ts GROUP_DISPATCH_CONFIG_PLATFORMS
     Drift here is silent data loss: the backend sanitizer wipes
     messages_dispatch_model_config on save for any platform the predicate
     does not keep, while the frontend form happily shows the field.

  3. Platform constant universe
       backend/internal/domain/constants.go       Platform* string constants
       frontend/src/types/index.ts                AccountPlatform union
     BOTH directions are hard failures:
       - backend-only value → admin UI cannot type/render the platform
         (filters, forms, badge maps silently fall through);
       - frontend-only value → the union advertises a platform the backend
         rejects, so typed forms/filters can emit an invalid value.

  4. Ent schema platform enum coverage
       backend/internal/engine/provider.go        AllSchedulingPlatforms()
       backend/ent/schema/account.go              field.String("platform")
     The ent schema does NOT use a Values() enum constraint — the platform
     field is a free string. This check is therefore informational: it
     verifies the field exists (it would be a hard error if the schema
     regressed to a constrained enum that omitted a scheduling platform).

  5. Frontend style mapping coverage
       backend/internal/engine/provider.go        AllSchedulingPlatforms()
       frontend/src/constants/gatewayPlatforms.ts SOFT_BADGE + LABEL_TEXT
     A platform missing from the style maps renders as unstyled (gray
     fallback) in the admin UI — easy to miss in review.

  6. AccountType Go↔TS lockstep
       backend/internal/domain/constants.go       AccountType* string constants
       frontend/src/types/index.ts                AccountType union
     BOTH directions are hard failures (same reasoning as CHECK 3):
       - backend-only value → the admin UI cannot offer or render the type;
       - frontend-only value → typed forms emit a value the backend rejects.

  7. Role Go↔TS lockstep
       backend/internal/domain/constants.go       Role* string constants
       frontend/src/types/index.ts                User.role inline union
     Drift → the admin UI offers a role the backend rejects, or vice versa.

  8. RedeemType Go↔TS lockstep
       backend/internal/domain/constants.go       RedeemType* string constants
       frontend/src/types/index.ts                RedeemCodeType union
     Drift → redeem code creation form emits a type the backend rejects.

  9. SubscriptionType Go↔TS lockstep
       backend/internal/domain/constants.go       SubscriptionType* string constants
       frontend/src/types/index.ts                SubscriptionType union
     Drift → group subscription type selector offers invalid values.

 10. SubscriptionStatus Go↔TS lockstep
       backend/internal/domain/constants.go       SubscriptionStatus* string constants
       frontend/src/types/index.ts                UserSubscription.status inline union
     Drift → subscription status badge/filter silently drops a state.

 11. GroupPlatform Go↔TS lockstep
       backend/internal/domain/constants.go       Platform* string constants
       frontend/src/types/index.ts                GroupPlatform union
     Validates GroupPlatform stays in lockstep with the same Platform*
     constant universe that backs AccountPlatform (CHECK 3). A stale subset
     means the group-creation form cannot offer a newly added platform.

Go constant names (`domain.PlatformX` / service aliases `PlatformX`) are
resolved to their string values from constants.go, so the comparison is on
wire values, not identifiers. All parsers tolerate gofmt / prettier
re-wrapping (multiline slice literals, `|`-prefixed union members, single or
double quotes).

A parse failure (function renamed, const moved, union rewritten as an enum)
exits 2 — the checker must be updated together with the refactor, never
silently pass.

Exit codes
----------

  0 — all eleven pairs agree
  1 — drift detected (details on stderr, both sides' file:line + sets)
  2 — parse / environment failure

Usage
-----

  python3 scripts/checks/platform-registry-drift.py [--root <dir>] [--quiet]
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent

DOMAIN_CONSTANTS = "backend/internal/domain/constants.go"
ENGINE_PROVIDER = "backend/internal/engine/provider.go"
DISPATCH_PREDICATE = "backend/internal/service/openai_messages_dispatch_tk_newapi.go"
ENT_SCHEMA_ACCOUNT = "backend/ent/schema/account.go"
TS_GATEWAY_PLATFORMS = "frontend/src/constants/gatewayPlatforms.ts"
TS_TYPES_INDEX = "frontend/src/types/index.ts"


class ParseFailure(Exception):
    """Registry shape changed beyond what this checker understands."""


def line_of(text: str, offset: int) -> int:
    return text[:offset].count("\n") + 1


def read(root: Path, rel: str) -> str:
    path = root / rel
    try:
        return path.read_text(encoding="utf-8")
    except OSError as e:
        raise ParseFailure(f"{rel}: cannot read: {e}") from e


def parse_domain_constants(text: str) -> dict[str, tuple[str, int]]:
    """Platform* string constants → {name: (value, line)}."""
    consts: dict[str, tuple[str, int]] = {}
    for m in re.finditer(r'^\s*(Platform[A-Za-z0-9_]*)\s*=\s*"([^"]*)"', text, re.M):
        consts[m.group(1)] = (m.group(2), line_of(text, m.start()))
    if not consts:
        raise ParseFailure(
            f"{DOMAIN_CONSTANTS}: no `PlatformX = \"...\"` constants found — "
            "platform block moved/renamed? Update platform-registry-drift.py."
        )
    return consts


def parse_go_account_types(text: str) -> tuple[list[str], int]:
    """AccountType* string constants → ([values], first_line)."""
    consts: list[tuple[str, int]] = []
    for m in re.finditer(r'^\s*AccountType[A-Za-z0-9_]*\s*=\s*"([^"]*)"', text, re.M):
        consts.append((m.group(1), line_of(text, m.start())))
    if not consts:
        raise ParseFailure(
            f"{DOMAIN_CONSTANTS}: no `AccountTypeX = \"...\"` constants found — "
            "account-type block moved/renamed? Update platform-registry-drift.py."
        )
    values = [v for v, _ in consts]
    first_line = min(line for _, line in consts)
    return values, first_line


def _parse_go_const_group(
    text: str, prefix: str, human_label: str
) -> tuple[list[str], int]:
    """Generic: <Prefix>* string constants -> ([values], first_line).

    Matches lines like `PrefixFoo = "bar"` and collects the string values.
    """
    consts: list[tuple[str, int]] = []
    pattern = re.compile(
        rf'^\s*{re.escape(prefix)}[A-Za-z0-9_]*\s*=\s*"([^"]*)"', re.M
    )
    for m in pattern.finditer(text):
        consts.append((m.group(1), line_of(text, m.start())))
    if not consts:
        raise ParseFailure(
            f"{DOMAIN_CONSTANTS}: no `{prefix}* = \"...\"` constants found -- "
            f"{human_label} block moved/renamed? Update platform-registry-drift.py."
        )
    values = [v for v, _ in consts]
    first_line = min(line for _, line in consts)
    return values, first_line


def parse_go_role_constants(text: str) -> tuple[list[str], int]:
    """Role* string constants -> ([values], first_line)."""
    return _parse_go_const_group(text, "Role", "role")


def parse_go_redeem_type_constants(text: str) -> tuple[list[str], int]:
    """RedeemType* string constants -> ([values], first_line)."""
    return _parse_go_const_group(text, "RedeemType", "redeem-type")


def parse_go_subscription_type_constants(text: str) -> tuple[list[str], int]:
    """SubscriptionType* string constants -> ([values], first_line)."""
    return _parse_go_const_group(text, "SubscriptionType", "subscription-type")


def parse_go_subscription_status_constants(text: str) -> tuple[list[str], int]:
    """SubscriptionStatus* string constants -> ([values], first_line)."""
    return _parse_go_const_group(text, "SubscriptionStatus", "subscription-status")


def resolve_go_idents(
    tokens: list[tuple[str, str]], consts: dict[str, tuple[str, int]], where: str
) -> list[str]:
    """Resolve (ident, literal) capture pairs to string values via constants.go."""
    values: list[str] = []
    for ident, literal in tokens:
        if ident:
            if ident not in consts:
                raise ParseFailure(
                    f"{where}: references `{ident}` but {DOMAIN_CONSTANTS} defines no "
                    f"such constant (known: {', '.join(sorted(consts))})"
                )
            values.append(consts[ident][0])
        else:
            values.append(literal)
    return values


GO_MEMBER_RE = r'(?:domain\.)?(Platform[A-Za-z0-9_]+)|"([^"]*)"'


def parse_go_compat(text: str, consts: dict[str, tuple[str, int]]) -> tuple[list[str], int]:
    """OpenAICompatPlatforms() []string { return []string{...} } → values."""
    m = re.search(r"func\s+OpenAICompatPlatforms\s*\(\s*\)\s*\[\]string\s*\{", text)
    if not m:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}: `func OpenAICompatPlatforms() []string` not found — "
            "renamed/moved? Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())
    # Anchor on `return []string{...}` so earlier []string literals (local
    # variables, log arguments, etc.) in the same function body are skipped.
    slice_m = re.compile(r"return\s+\[\]string\s*\{([^{}]*)\}", re.S).search(text, m.end())
    if not slice_m:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}:{line}: OpenAICompatPlatforms body has no "
            "`return []string{{...}}` literal"
        )
    tokens = re.findall(GO_MEMBER_RE, slice_m.group(1))
    if not tokens:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}:{line}: OpenAICompatPlatforms slice literal parsed empty"
        )
    return resolve_go_idents(tokens, consts, f"{ENGINE_PROVIDER}:{line}"), line


def parse_go_dispatch(
    text: str, consts: dict[str, tuple[str, int]], compat: list[str]
) -> tuple[list[str], int]:
    """tkGroupKeepsDispatchConfig → compat set (if delegated) ∪ explicit
    `g.Platform == PlatformX` comparisons in the predicate body."""
    m = re.search(r"func\s+tkGroupKeepsDispatchConfig\s*\([^)]*\)\s*bool\s*\{", text)
    if not m:
        raise ParseFailure(
            f"{DISPATCH_PREDICATE}: `func tkGroupKeepsDispatchConfig(...) bool` not found — "
            "renamed/moved? Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())
    end = text.find("\n}", m.end())  # gofmt: top-level func closes at column 0
    if end == -1:
        raise ParseFailure(f"{DISPATCH_PREDICATE}:{line}: cannot find end of function body")
    body = text[m.end() : end]

    values: list[str] = []
    if re.search(
        r"\b(?:isOpenAICompatPlatformGroup|(?:engine\.|service\.)?IsOpenAICompatPlatform)\s*\(",
        body,
    ):
        values.extend(compat)
    cmp_tokens = re.findall(
        r"g\.Platform\s*==\s*(?:" + GO_MEMBER_RE + r")", body
    ) + re.findall(
        r"(?:(?:domain\.)?(Platform[A-Za-z0-9_]+)|\"([^\"]*)\")\s*==\s*g\.Platform", body
    )
    values.extend(resolve_go_idents(cmp_tokens, consts, f"{DISPATCH_PREDICATE}:{line}"))
    if not values:
        raise ParseFailure(
            f"{DISPATCH_PREDICATE}:{line}: tkGroupKeepsDispatchConfig neither delegates to the "
            "compat predicate nor compares g.Platform to any constant — body shape changed? "
            "Update platform-registry-drift.py."
        )
    return values, line


def parse_ts_array(text: str, name: str, rel: str) -> tuple[list[str], int]:
    """export const NAME[: type] = ['a', 'b', ...] → values (prettier-tolerant)."""
    m = re.search(rf"export\s+const\s+{name}\b[^=]*=", text)
    if not m:
        raise ParseFailure(
            f"{rel}: `export const {name}` not found — renamed/moved? "
            "Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())
    open_m = re.compile(r"\s*\[").match(text, m.end())
    if not open_m:
        raise ParseFailure(f"{rel}:{line}: {name} is not assigned an array literal")
    close = text.find("]", open_m.end())
    if close == -1:
        raise ParseFailure(f"{rel}:{line}: {name} array literal is not closed")
    values = [
        a or b
        for a, b in re.findall(r"'([^']*)'|\"([^\"]*)\"", text[open_m.end() : close])
    ]
    if not values:
        raise ParseFailure(f"{rel}:{line}: {name} array literal parsed empty")
    return values, line


def parse_ts_union(text: str, name: str, rel: str) -> tuple[list[str], int]:
    """export type NAME = 'a' | 'b' | ... → values (prettier-tolerant:
    accepts multiline unions with leading `|`)."""
    m = re.search(rf"export\s+type\s+{name}\s*=", text)
    if not m:
        raise ParseFailure(
            f"{rel}: `export type {name}` not found — renamed/moved? "
            "Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())
    member = re.compile(r"\s*\|?\s*(?:'([^']*)'|\"([^\"]*)\")")
    comment = re.compile(r"\s*//[^\n]*")
    values: list[str] = []
    pos = m.end()
    while True:
        # Skip inline comments (// ...) before attempting the next member
        cm = comment.match(text, pos)
        if cm:
            pos = cm.end()
            continue
        mm = member.match(text, pos)
        if not mm:
            break
        values.append(mm.group(1) or mm.group(2))
        pos = mm.end()
    if not values:
        raise ParseFailure(f"{rel}:{line}: {name} union parsed empty (not a string union?)")
    return values, line


def parse_ts_inline_union(
    text: str, iface_name: str, prop_name: str, rel: str
) -> tuple[list[str], int]:
    """Parse an inline string-union property inside an interface/type.

    Matches patterns like:
        interface User {
            ...
            role: 'admin' | 'user'
            ...
        }

    Also handles optional properties (prop?:), intersections (string & ...),
    and parenthesized unions.
    """
    # Find the interface/type declaration
    iface_m = re.search(
        rf"export\s+(?:interface|type)\s+{re.escape(iface_name)}\b", text
    )
    if not iface_m:
        raise ParseFailure(
            f"{rel}: `export interface/type {iface_name}` not found -- "
            "renamed/moved? Update platform-registry-drift.py."
        )

    # Find the property within the interface body (search from iface start)
    prop_pattern = re.compile(
        rf"^\s*{re.escape(prop_name)}\s*\??\s*:\s*",
        re.M,
    )
    prop_m = prop_pattern.search(text, iface_m.end())
    if not prop_m:
        raise ParseFailure(
            f"{rel}: property `{prop_name}` not found in {iface_name} -- "
            "renamed/moved? Update platform-registry-drift.py."
        )
    line = line_of(text, prop_m.start())

    # Extract the type expression (everything from after the colon to the next
    # newline that doesn't start with |, or to a semicolon/closing brace).
    type_start = prop_m.end()
    member = re.compile(r"\s*\|?\s*(?:'([^']*)'|\"([^\"]*)\")")
    values: list[str] = []
    pos = type_start
    while True:
        mm = member.match(text, pos)
        if not mm:
            break
        values.append(mm.group(1) or mm.group(2))
        pos = mm.end()
    if not values:
        raise ParseFailure(
            f"{rel}:{line}: {iface_name}.{prop_name} inline union parsed empty "
            "(not a string union?)"
        )
    return values, line


def parse_go_all_scheduling(
    text: str, consts: dict[str, tuple[str, int]]
) -> tuple[list[str], int]:
    """AllSchedulingPlatforms() []string { return []string{...} } → values."""
    m = re.search(r"func\s+AllSchedulingPlatforms\s*\(\s*\)\s*\[\]string\s*\{", text)
    if not m:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}: `func AllSchedulingPlatforms() []string` not found — "
            "renamed/moved? Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())
    slice_m = re.compile(r"return\s+\[\]string\s*\{([^{}]*)\}", re.S).search(text, m.end())
    if not slice_m:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}:{line}: AllSchedulingPlatforms body has no "
            "`return []string{{...}}` literal"
        )
    tokens = re.findall(GO_MEMBER_RE, slice_m.group(1))
    if not tokens:
        raise ParseFailure(
            f"{ENGINE_PROVIDER}:{line}: AllSchedulingPlatforms slice literal parsed empty"
        )
    return resolve_go_idents(tokens, consts, f"{ENGINE_PROVIDER}:{line}"), line


def parse_ent_platform_field(text: str) -> tuple[list[str] | None, int]:
    """Parse ent schema account.go for the platform field definition.

    Returns (enum_values, line) where enum_values is:
      - a list of string values if the field uses .Values("a","b",...) constraint
      - None if the field is a free string (no Values() enum)

    Raises ParseFailure if the platform field is not found at all.
    """
    m = re.search(r'field\.String\(\s*"platform"\s*\)', text)
    if not m:
        raise ParseFailure(
            f"{ENT_SCHEMA_ACCOUNT}: `field.String(\"platform\")` not found — "
            "platform field removed or renamed? Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())

    # Look for a .Values(...) call chained on the same field builder.
    # The field builder ends at a comma or closing paren at the same indent.
    # Scan forward from the field.String("platform") match until we hit the next
    # field.* definition or the end of the Fields() return block.
    next_field = re.search(r"field\.\w+\(", text[m.end() :])
    scope_end = m.end() + next_field.start() if next_field else len(text)
    field_chain = text[m.end() : scope_end]

    values_m = re.search(r"\.\s*Values\s*\(([^)]*)\)", field_chain)
    if not values_m:
        return None, line

    values = [
        a or b
        for a, b in re.findall(r'"([^"]*)"|`([^`]*)`', values_m.group(1))
    ]
    if not values:
        raise ParseFailure(
            f"{ENT_SCHEMA_ACCOUNT}:{line}: platform field has .Values() but "
            "the values list parsed empty"
        )
    return values, line


def parse_ts_record_keys(text: str, name: str, rel: str) -> tuple[list[str], int]:
    """const NAME: Record<...> = { key1: ..., key2: ..., } → keys.

    Parses both `Record<string, string>` and `Record<SomeType, string>` shapes.
    Tolerates multiline objects and trailing commas.
    """
    m = re.search(rf"(?:export\s+)?const\s+{name}\b[^=]*=\s*\{{", text)
    if not m:
        raise ParseFailure(
            f"{rel}: `const {name}` with object literal not found — "
            "renamed/moved? Update platform-registry-drift.py."
        )
    line = line_of(text, m.start())

    # Find the matching closing brace (simple: no nested objects expected in
    # Tailwind class string maps).
    depth = 1
    pos = m.end()
    while pos < len(text) and depth > 0:
        if text[pos] == "{":
            depth += 1
        elif text[pos] == "}":
            depth -= 1
        pos += 1
    if depth != 0:
        raise ParseFailure(f"{rel}:{line}: {name} object literal is not closed")

    body = text[m.end() : pos - 1]
    # Keys can be bare identifiers or quoted strings.
    keys = re.findall(r"(?:^|,)\s*(?:'([^']*)'|\"([^\"]*)\"|([\w]+))\s*:", body)
    values = [a or b or c for a, b, c in keys]
    if not values:
        raise ParseFailure(f"{rel}:{line}: {name} object literal has no keys")
    return values, line


def fmt_set(values: list[str]) -> str:
    return "[" + ", ".join(sorted(set(values))) + "]"


def compare_pair(
    label: str,
    go_values: list[str],
    go_loc: str,
    ts_values: list[str],
    ts_loc: str,
) -> list[str]:
    """Return failure lines (empty when the pair agrees as a set)."""
    go_set, ts_set = set(go_values), set(ts_values)
    if go_set == ts_set:
        return []
    lines = [
        f"FAIL: {label} drift (backend Go is the source of truth)",
        f"  backend  {go_loc}  {fmt_set(go_values)}",
        f"  frontend {ts_loc}  {fmt_set(ts_values)}",
    ]
    only_backend = sorted(go_set - ts_set)
    only_frontend = sorted(ts_set - go_set)
    if only_backend:
        lines.append(f"  only in backend (frontend must add): {', '.join(only_backend)}")
    if only_frontend:
        lines.append(f"  only in frontend (no backend truth — remove or add the Go side first): "
                     f"{', '.join(only_frontend)}")
    return lines


def compare_superset(
    label: str,
    required: list[str],
    required_loc: str,
    actual: list[str],
    actual_loc: str,
) -> list[str]:
    """Return failure lines when `actual` is not a superset of `required`."""
    missing = sorted(set(required) - set(actual))
    if not missing:
        return []
    return [
        f"FAIL: {label} — missing required values",
        f"  required {required_loc}  {fmt_set(required)}",
        f"  actual   {actual_loc}  {fmt_set(actual)}",
        f"  missing: {', '.join(missing)}",
    ]


def run(root: Path) -> tuple[list[list[str]], list[str]]:
    """Returns (failures, ok_lines)."""
    consts = parse_domain_constants(read(root, DOMAIN_CONSTANTS))

    engine_text = read(root, ENGINE_PROVIDER)
    go_compat, go_compat_line = parse_go_compat(engine_text, consts)
    go_sched, go_sched_line = parse_go_all_scheduling(engine_text, consts)

    dispatch_text = read(root, DISPATCH_PREDICATE)
    go_dispatch, go_dispatch_line = parse_go_dispatch(dispatch_text, consts, go_compat)

    ts_const_text = read(root, TS_GATEWAY_PLATFORMS)
    ts_compat, ts_compat_line = parse_ts_array(
        ts_const_text, "OPENAI_COMPAT_PLATFORMS", TS_GATEWAY_PLATFORMS
    )
    ts_dispatch, ts_dispatch_line = parse_ts_array(
        ts_const_text, "GROUP_DISPATCH_CONFIG_PLATFORMS", TS_GATEWAY_PLATFORMS
    )

    ts_types_text = read(root, TS_TYPES_INDEX)
    ts_union, ts_union_line = parse_ts_union(ts_types_text, "AccountPlatform", TS_TYPES_INDEX)

    domain_values = [v for v, _ in consts.values()]
    domain_line = min(line for _, line in consts.values())

    failures: list[list[str]] = []
    ok_lines: list[str] = []

    # --- CHECKs 1-3: exact bilateral lockstep ---

    for label, gv, gl, tv, tl in [
        (
            "OpenAI-compat platform list",
            go_compat,
            f"{ENGINE_PROVIDER}:{go_compat_line} OpenAICompatPlatforms()",
            ts_compat,
            f"{TS_GATEWAY_PLATFORMS}:{ts_compat_line} OPENAI_COMPAT_PLATFORMS",
        ),
        (
            "group dispatch-config platform list",
            go_dispatch,
            f"{DISPATCH_PREDICATE}:{go_dispatch_line} tkGroupKeepsDispatchConfig",
            ts_dispatch,
            f"{TS_GATEWAY_PLATFORMS}:{ts_dispatch_line} GROUP_DISPATCH_CONFIG_PLATFORMS",
        ),
        (
            "platform constant universe",
            domain_values,
            f"{DOMAIN_CONSTANTS}:{domain_line} Platform* constants",
            ts_union,
            f"{TS_TYPES_INDEX}:{ts_union_line} AccountPlatform union",
        ),
    ]:
        fail = compare_pair(label, gv, gl, tv, tl)
        if fail:
            failures.append(fail)
        else:
            ok_lines.append(f"ok: {label} in lockstep {fmt_set(gv)}")

    # --- CHECK 4: ent schema platform enum coverage ---
    # The ent schema uses field.String("platform") without a Values() enum
    # constraint (free string), so this check is informational: it verifies the
    # field still exists as a free string. If someone adds a Values() enum to the
    # schema, this check catches any scheduling platform omitted from that enum.

    ent_text = read(root, ENT_SCHEMA_ACCOUNT)
    ent_values, ent_line = parse_ent_platform_field(ent_text)

    if ent_values is not None:
        # Schema constrains platform to an enum — every scheduling platform must
        # be in that enum or accounts for it cannot be persisted.
        fail = compare_superset(
            "ent schema platform enum ⊇ AllSchedulingPlatforms()",
            go_sched,
            f"{ENGINE_PROVIDER}:{go_sched_line} AllSchedulingPlatforms()",
            ent_values,
            f"{ENT_SCHEMA_ACCOUNT}:{ent_line} field.String(\"platform\").Values()",
        )
        if fail:
            failures.append(fail)
        else:
            ok_lines.append(
                f"ok: ent schema platform enum covers all scheduling platforms "
                f"{fmt_set(go_sched)}"
            )
    else:
        ok_lines.append(
            f"ok: ent schema platform field is a free string (no Values() enum) — "
            f"no coverage gap possible [{ENT_SCHEMA_ACCOUNT}:{ent_line}]"
        )

    # --- CHECK 5: frontend style mapping coverage ---
    # Every scheduling platform should have entries in the admin UI style maps
    # (SOFT_BADGE and LABEL_TEXT). A missing key falls through to a generic gray
    # fallback — functional but visually inconsistent and easy to miss in review.

    for map_name in ("SOFT_BADGE", "LABEL_TEXT"):
        ts_keys, ts_keys_line = parse_ts_record_keys(
            ts_const_text, map_name, TS_GATEWAY_PLATFORMS
        )
        fail = compare_superset(
            f"frontend {map_name} style map ⊇ AllSchedulingPlatforms()",
            go_sched,
            f"{ENGINE_PROVIDER}:{go_sched_line} AllSchedulingPlatforms()",
            ts_keys,
            f"{TS_GATEWAY_PLATFORMS}:{ts_keys_line} {map_name}",
        )
        if fail:
            failures.append(fail)
        else:
            ok_lines.append(
                f"ok: {map_name} style map covers all scheduling platforms "
                f"{fmt_set(go_sched)}"
            )

    # --- CHECK 6: AccountType Go↔TS lockstep ---
    # Every account type on the Go side must exist in the TS AccountType union
    # and vice versa — same bilateral reasoning as CHECK 3.

    domain_text = read(root, DOMAIN_CONSTANTS)
    go_acct_types, go_acct_line = parse_go_account_types(domain_text)
    ts_acct_types, ts_acct_line = parse_ts_union(ts_types_text, "AccountType", TS_TYPES_INDEX)

    fail = compare_pair(
        "AccountType constant universe",
        go_acct_types,
        f"{DOMAIN_CONSTANTS}:{go_acct_line} AccountType* constants",
        ts_acct_types,
        f"{TS_TYPES_INDEX}:{ts_acct_line} AccountType union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(f"ok: AccountType constant universe in lockstep {fmt_set(go_acct_types)}")

    # --- CHECK 7: Role Go↔TS lockstep ---
    # The Go Role* constants must match the TS User.role inline union exactly.

    go_roles, go_role_line = parse_go_role_constants(domain_text)
    ts_roles, ts_role_line = parse_ts_inline_union(
        ts_types_text, "User", "role", TS_TYPES_INDEX
    )

    fail = compare_pair(
        "Role constant universe",
        go_roles,
        f"{DOMAIN_CONSTANTS}:{go_role_line} Role* constants",
        ts_roles,
        f"{TS_TYPES_INDEX}:{ts_role_line} User.role inline union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(f"ok: Role constant universe in lockstep {fmt_set(go_roles)}")

    # --- CHECK 8: RedeemType Go↔TS lockstep ---
    # RedeemType* constants must match the TS RedeemCodeType standalone union.

    go_redeem, go_redeem_line = parse_go_redeem_type_constants(domain_text)
    ts_redeem, ts_redeem_line = parse_ts_union(
        ts_types_text, "RedeemCodeType", TS_TYPES_INDEX
    )

    fail = compare_pair(
        "RedeemType constant universe",
        go_redeem,
        f"{DOMAIN_CONSTANTS}:{go_redeem_line} RedeemType* constants",
        ts_redeem,
        f"{TS_TYPES_INDEX}:{ts_redeem_line} RedeemCodeType union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(f"ok: RedeemType constant universe in lockstep {fmt_set(go_redeem)}")

    # --- CHECK 9: SubscriptionType Go↔TS lockstep ---
    # SubscriptionType* constants must match the TS SubscriptionType standalone union.

    go_sub_type, go_sub_type_line = parse_go_subscription_type_constants(domain_text)
    ts_sub_type, ts_sub_type_line = parse_ts_union(
        ts_types_text, "SubscriptionType", TS_TYPES_INDEX
    )

    fail = compare_pair(
        "SubscriptionType constant universe",
        go_sub_type,
        f"{DOMAIN_CONSTANTS}:{go_sub_type_line} SubscriptionType* constants",
        ts_sub_type,
        f"{TS_TYPES_INDEX}:{ts_sub_type_line} SubscriptionType union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(
            f"ok: SubscriptionType constant universe in lockstep {fmt_set(go_sub_type)}"
        )

    # --- CHECK 10: SubscriptionStatus Go↔TS lockstep ---
    # SubscriptionStatus* constants must match the TS UserSubscription.status
    # inline union.

    go_sub_status, go_sub_status_line = parse_go_subscription_status_constants(domain_text)
    ts_sub_status, ts_sub_status_line = parse_ts_inline_union(
        ts_types_text, "UserSubscription", "status", TS_TYPES_INDEX
    )

    fail = compare_pair(
        "SubscriptionStatus constant universe",
        go_sub_status,
        f"{DOMAIN_CONSTANTS}:{go_sub_status_line} SubscriptionStatus* constants",
        ts_sub_status,
        f"{TS_TYPES_INDEX}:{ts_sub_status_line} UserSubscription.status inline union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(
            f"ok: SubscriptionStatus constant universe in lockstep {fmt_set(go_sub_status)}"
        )

    # --- CHECK 11: GroupPlatform Go↔TS lockstep ---
    # GroupPlatform must stay in lockstep with the same Platform* constant
    # universe that AccountPlatform is checked against (CHECK 3). A stale
    # subset means the group-creation form cannot offer a newly added platform.

    ts_group_plat, ts_group_plat_line = parse_ts_union(
        ts_types_text, "GroupPlatform", TS_TYPES_INDEX
    )

    fail = compare_pair(
        "GroupPlatform constant universe",
        domain_values,
        f"{DOMAIN_CONSTANTS}:{domain_line} Platform* constants",
        ts_group_plat,
        f"{TS_TYPES_INDEX}:{ts_group_plat_line} GroupPlatform union",
    )
    if fail:
        failures.append(fail)
    else:
        ok_lines.append(
            f"ok: GroupPlatform constant universe in lockstep {fmt_set(domain_values)}"
        )

    return failures, ok_lines


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--root", type=Path, default=REPO_ROOT,
                    help="repo root to check (default: this script's repo)")
    ap.add_argument("--quiet", action="store_true",
                    help="suppress success output (used by preflight wrapper)")
    args = ap.parse_args()

    try:
        failures, ok_lines = run(args.root.resolve())
    except ParseFailure as e:
        print(f"[platform-registry-drift] PARSE FAILURE: {e}", file=sys.stderr)
        return 2

    if failures:
        for fail in failures:
            print("[platform-registry-drift] " + fail[0], file=sys.stderr)
            for line in fail[1:]:
                print(line, file=sys.stderr)
        print(
            "\nFix: backend Go is the single source of truth — align the frontend "
            "list/union to it (or land the backend change first).",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        for line in ok_lines:
            print(f"[platform-registry-drift] {line}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
