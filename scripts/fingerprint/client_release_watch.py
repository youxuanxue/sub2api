#!/usr/bin/env python3
"""Discover upstream client releases and compare to TokenKey fingerprint pins.

Polls public release channels for Claude Code (incl. Stainless SDK), Codex (CLI + VS Code family),
Antigravity, Kiro IDE/CLI, Gemini CLI, and Grok CLI, reads the pinned versions from the repo,
and reports drift so operators can run the matching fingerprint-alignment skill before upstream
rejects spoofed traffic.

stdlib-only; safe to run locally and in GitHub Actions.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_STATE = REPO_ROOT / ".cache/fingerprint/client-release-watch.json"
DEFAULT_REPORT_JSON = REPO_ROOT / ".cache/fingerprint/client-release-watch/report.json"
DEFAULT_REPORT_MD = REPO_ROOT / ".cache/fingerprint/client-release-watch/report.md"

_VER_CORE = re.compile(r"(\d+)\.(\d+)\.(\d+)")
_GH_RELEASES = "https://api.github.com/repos/{repo}/releases/latest"
_NPM_LATEST = "https://registry.npmjs.org/{package}/latest"
_BREW_CASK = "https://formulae.brew.sh/api/cask/{name}.json"

CC_BASELINE_JSON = REPO_ROOT / "deploy/aws/stage0/anthropic-http-mimicry-baselines.json"
CLAUDE_CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/claude/constants.go"
ANTIGRAVITY_OAUTH_GO = REPO_ROOT / "backend/internal/pkg/antigravity/oauth.go"
KIRO_CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/kiro/constants.go"
GEMINI_CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/geminicli/constants.go"
XAI_OAUTH_GO = REPO_ROOT / "backend/internal/pkg/xai/oauth.go"
SETTING_GO = REPO_ROOT / "backend/internal/service/setting_gateway_runtime.go"

# Platforms that mirror another platform's drift/issue signal (listed in report, no duplicate issue).
COMPANION_OF: dict[str, str] = {
    "codex-vscode": "codex",
}


def now_utc() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def normalize_version(raw: str) -> str:
    """Return the leading semver token from release tags / brew cask strings."""
    text = (raw or "").strip()
    if not text:
        return ""
    if text.startswith("rust-v"):
        text = text[len("rust-v") :]
    text = text.lstrip("vV")
    if "," in text:
        text = text.split(",", 1)[0]
    m = _VER_CORE.search(text)
    return m.group(0) if m else text


def version_key(raw: str) -> tuple[int, int, int] | None:
    norm = normalize_version(raw)
    m = _VER_CORE.match(norm)
    if not m:
        return None
    return int(m.group(1)), int(m.group(2)), int(m.group(3))


def version_gt(a: str, b: str) -> bool:
    ka, kb = version_key(a), version_key(b)
    if ka is None or kb is None:
        return normalize_version(a) != normalize_version(b) and a > b
    return ka > kb


def version_max(*versions: str) -> str:
    best = ""
    for v in versions:
        if not v:
            continue
        if not best or version_gt(v, best):
            best = v
    return best


def http_json(url: str, *, timeout: float = 30.0, retries: int = 3) -> dict[str, Any]:
    last_err: Exception | None = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(
                url,
                headers={
                    "Accept": "application/json",
                    "User-Agent": "TokenKey-client-release-watch",
                },
            )
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, json.JSONDecodeError) as exc:
            last_err = exc
            if attempt + 1 < retries:
                time.sleep(float(attempt + 1))
    assert last_err is not None
    raise last_err


def fetch_github_latest(repo: str, *, tag_prefix: str = "") -> dict[str, str]:
    data = http_json(_GH_RELEASES.format(repo=repo))
    tag = str(data.get("tag_name") or "")
    if tag_prefix and tag.startswith(tag_prefix):
        tag = tag[len(tag_prefix) :]
    return {
        "version": normalize_version(tag),
        "published_at": str(data.get("published_at") or ""),
        "url": str(data.get("html_url") or f"https://github.com/{repo}/releases"),
        "raw_tag": str(data.get("tag_name") or ""),
    }


def fetch_npm_latest(package: str) -> dict[str, str]:
    data = http_json(_NPM_LATEST.format(package=package))
    return {
        "version": normalize_version(str(data.get("version") or "")),
        "published_at": "",
        "url": f"https://www.npmjs.com/package/{package}",
        "raw_tag": str(data.get("version") or ""),
    }


def fetch_brew_cask_latest(name: str) -> dict[str, str]:
    data = http_json(_BREW_CASK.format(name=name))
    version = normalize_version(str(data.get("version") or ""))
    urls = data.get("url") or data.get("url_specs") or ""
    url = urls if isinstance(urls, str) else str(urls)
    if not url.startswith("http"):
        url = f"https://formulae.brew.sh/cask/{name}"
    return {
        "version": version,
        "published_at": "",
        "url": url if url.startswith("http") else f"https://formulae.brew.sh/cask/{name}",
        "raw_tag": str(data.get("version") or ""),
    }


def _read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError:
        return ""


def read_pinned_claude_code() -> str:
    try:
        data = json.loads(CC_BASELINE_JSON.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return ""
    return normalize_version(str(data.get("cc_version") or ""))


def read_pinned_codex() -> str:
    text = _read_text(SETTING_GO)
    m = re.search(r'DefaultOpenAICodexVersion\s*=\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_antigravity() -> str:
    text = _read_text(ANTIGRAVITY_OAUTH_GO)
    m = re.search(r'DefaultUserAgentVersion\s*=\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_kiro() -> str:
    text = _read_text(KIRO_CONSTANTS_GO)
    m = re.search(r'DefaultKiroIDEVersion\s*=\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_cc_stainless() -> str:
    text = _read_text(CLAUDE_CONSTANTS_GO)
    m = re.search(r'"X-Stainless-Package-Version":\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_gemini_cli() -> str:
    text = _read_text(GEMINI_CONSTANTS_GO)
    m = re.search(r"GeminiCLI/(\d+\.\d+\.\d+)", text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_grok_cli() -> str:
    text = _read_text(XAI_OAUTH_GO)
    m = re.search(r'DefaultGrokCLIVersion\s*=\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_kiro_cli() -> str:
    text = _read_text(KIRO_CONSTANTS_GO)
    m = re.search(r'DefaultKiroCLIVersion\s*=\s*"([^"]+)"', text)
    return normalize_version(m.group(1)) if m else ""


def read_pinned_codex_vscode() -> str:
    return read_pinned_codex()


@dataclass
class SourceSpec:
    kind: str
    label: str
    repo: str = ""
    package: str = ""
    cask: str = ""
    tag_prefix: str = ""


@dataclass
class PlatformSpec:
    id: str
    name: str
    skill: str
    pin_path: str
    sources: list[SourceSpec] = field(default_factory=list)
    actionable: bool = True
    status_note: str = ""


PIN_READERS: dict[str, Callable[[], str]] = {
    "claude-code": read_pinned_claude_code,
    "cc-stainless": read_pinned_cc_stainless,
    "codex": read_pinned_codex,
    "codex-vscode": read_pinned_codex_vscode,
    "antigravity": read_pinned_antigravity,
    "kiro": read_pinned_kiro,
    "kiro-cli": read_pinned_kiro_cli,
    "gemini-cli": read_pinned_gemini_cli,
    "grok-cli": read_pinned_grok_cli,
}

# Remediation playbooks: version watch routes to skills; capture/diff/PR stay in skills.
PLATFORM_PLAYBOOKS: dict[str, dict[str, Any]] = {
    "claude-code": {
        "skill": "tokenkey-cc-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/anthropic/capture-cc-fingerprint.sh check env",
            "bash ops/anthropic/capture-cc-fingerprint.sh capture --http",
            "python3 ops/anthropic/capture_cc_fingerprint.py diff --bundle <bundle>",
        ],
    },
    "cc-stainless": {
        "skill": "tokenkey-cc-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/anthropic/capture-cc-fingerprint.sh check env",
            "bash ops/anthropic/capture-cc-fingerprint.sh capture --http",
            "python3 ops/anthropic/capture_cc_fingerprint.py diff --bundle <bundle>",
        ],
        "note": "Advisory npm release watch only. Keep X-Stainless-Package-Version at captured wire ground truth unless capture/diff proves a change.",
    },
    "codex": {
        "skill": "tokenkey-codex-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/openai/capture-codex-fingerprint.sh check env",
            "bash ops/openai/capture-codex-fingerprint.sh check",
            "bash ops/openai/capture-codex-fingerprint.sh emit-edits",
        ],
    },
    "codex-vscode": {
        "skill": "tokenkey-codex-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/openai/capture-codex-fingerprint.sh check env",
            "bash ops/openai/capture-codex-fingerprint.sh check",
            "bash ops/openai/capture-codex-fingerprint.sh emit-edits",
        ],
        "note": "Verify codex_vscode/* and codex_vscode_copilot/* UA prefixes share @openai/codex release train.",
    },
    "antigravity": {
        "skill": "tokenkey-antigravity-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/antigravity/capture-antigravity-fingerprint.sh check env",
            "bash ops/antigravity/capture-antigravity-fingerprint.sh capture --http",
            "bash ops/antigravity/capture-antigravity-fingerprint.sh check",
        ],
    },
    "kiro": {
        "skill": "tokenkey-kiro-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "bash ops/kiro/capture-kiro-fingerprint.sh check env",
            "bash ops/kiro/capture-kiro-fingerprint.sh capture --proxy-port 7890 --seconds 75",
            "bash ops/kiro/capture-kiro-fingerprint.sh check-tls",
        ],
    },
    "kiro-cli": {
        "skill": "tokenkey-kiro-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "brew info --cask kiro-cli",
            "/Applications/Kiro\\ CLI.app/Contents/MacOS/kiro-cli --version",
            "rg DefaultKiroCLIVersion backend/internal/pkg/kiro/constants.go",
        ],
        "note": "Homebrew kiro-cli semver is distinct from Kiro IDE; verify the local CLI binary before bumping DefaultKiroCLIVersion.",
    },
    "gemini-cli": {
        "skill": "tokenkey-gemini-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "npm view @google/gemini-cli version",
            "grep GeminiCLIUserAgent backend/internal/pkg/geminicli/constants.go",
        ],
    },
    "grok-cli": {
        "skill": "tokenkey-grok-fingerprint-alignment",
        "umbrella_skill": "tokenkey-fingerprint-alignment-all",
        "first_commands": [
            "npm view @xai-official/grok version",
            "grep DefaultGrokCLIVersion backend/internal/pkg/xai/oauth.go",
        ],
    },
}


PLATFORM_SPECS: list[PlatformSpec] = [
    PlatformSpec(
        id="claude-code",
        name="Claude Code",
        skill="tokenkey-cc-fingerprint-alignment",
        pin_path=str(CC_BASELINE_JSON.relative_to(REPO_ROOT)),
        sources=[
            SourceSpec(
                kind="github_release",
                label="GitHub anthropics/claude-code",
                repo="anthropics/claude-code",
                tag_prefix="v",
            ),
            SourceSpec(
                kind="npm",
                label="npm @anthropic-ai/claude-code",
                package="@anthropic-ai/claude-code",
            ),
        ],
    ),
    PlatformSpec(
        id="cc-stainless",
        name="Claude Code (Stainless SDK)",
        skill="tokenkey-cc-fingerprint-alignment",
        pin_path=str(CLAUDE_CONSTANTS_GO.relative_to(REPO_ROOT)) + " X-Stainless-Package-Version",
        sources=[
            SourceSpec(
                kind="npm",
                label="npm @anthropic-ai/sdk",
                package="@anthropic-ai/sdk",
            ),
        ],
        actionable=False,
        status_note=(
            "npm SDK semver is advisory; Claude Code on-wire X-Stainless-Package-Version "
            "remains capture ground truth."
        ),
    ),
    PlatformSpec(
        id="codex",
        name="Codex CLI",
        skill="tokenkey-codex-fingerprint-alignment",
        pin_path="backend/internal/service/setting_gateway_runtime.go DefaultOpenAICodexVersion",
        sources=[
            SourceSpec(
                kind="npm",
                label="npm @openai/codex",
                package="@openai/codex",
            ),
            SourceSpec(
                kind="github_release",
                label="GitHub openai/codex",
                repo="openai/codex",
                tag_prefix="rust-v",
            ),
        ],
    ),
    PlatformSpec(
        id="codex-vscode",
        name="Codex VS Code / Copilot",
        skill="tokenkey-codex-fingerprint-alignment",
        pin_path="same codex-tui pins (verify codex_vscode/* / codex_vscode_copilot/* UA)",
        sources=[
            SourceSpec(
                kind="npm",
                label="npm @openai/codex",
                package="@openai/codex",
            ),
            SourceSpec(
                kind="github_release",
                label="GitHub openai/codex",
                repo="openai/codex",
                tag_prefix="rust-v",
            ),
        ],
    ),
    PlatformSpec(
        id="gemini-cli",
        name="Gemini CLI",
        skill="tokenkey-gemini-fingerprint-alignment",
        pin_path=str(GEMINI_CONSTANTS_GO.relative_to(REPO_ROOT)),
        sources=[
            SourceSpec(
                kind="npm",
                label="npm @google/gemini-cli",
                package="@google/gemini-cli",
            ),
        ],
    ),
    PlatformSpec(
        id="grok-cli",
        name="Grok CLI",
        skill="tokenkey-grok-fingerprint-alignment",
        pin_path=str(XAI_OAUTH_GO.relative_to(REPO_ROOT)) + " DefaultGrokCLIVersion",
        sources=[
            SourceSpec(
                kind="npm",
                label="npm @xai-official/grok",
                package="@xai-official/grok",
            ),
        ],
    ),
    PlatformSpec(
        id="antigravity",
        name="Antigravity IDE",
        skill="tokenkey-antigravity-fingerprint-alignment",
        pin_path=str(ANTIGRAVITY_OAUTH_GO.relative_to(REPO_ROOT)),
        sources=[
            SourceSpec(
                kind="brew_cask",
                label="Homebrew cask antigravity",
                cask="antigravity",
            ),
        ],
    ),
    PlatformSpec(
        id="kiro",
        name="Kiro IDE",
        skill="tokenkey-kiro-fingerprint-alignment",
        pin_path=str(KIRO_CONSTANTS_GO.relative_to(REPO_ROOT)),
        sources=[
            SourceSpec(
                kind="brew_cask",
                label="Homebrew cask kiro",
                cask="kiro",
            ),
        ],
    ),
    PlatformSpec(
        id="kiro-cli",
        name="Kiro CLI",
        skill="tokenkey-kiro-fingerprint-alignment",
        pin_path=str(KIRO_CONSTANTS_GO.relative_to(REPO_ROOT)) + " DefaultKiroCLIVersion",
        sources=[
            SourceSpec(
                kind="brew_cask",
                label="Homebrew cask kiro-cli",
                cask="kiro-cli",
            ),
        ],
    ),
]


def fetch_source(spec: SourceSpec) -> dict[str, str]:
    if spec.kind == "github_release":
        return fetch_github_latest(spec.repo, tag_prefix=spec.tag_prefix)
    if spec.kind == "npm":
        return fetch_npm_latest(spec.package)
    if spec.kind == "brew_cask":
        return fetch_brew_cask_latest(spec.cask)
    raise ValueError(f"unknown source kind: {spec.kind}")


@dataclass
class PlatformResult:
    id: str
    name: str
    skill: str
    pin_path: str
    pinned: str
    upstream_latest: str
    upstream_sources: dict[str, dict[str, str]]
    status: str  # aligned | drift | unknown
    drift: bool
    fetch_errors: list[str] = field(default_factory=list)
    companion_of: str = ""
    actionable: bool = True
    status_note: str = ""

    def issue_signature(self) -> str:
        return f"client-release-{self.id}-{self.upstream_latest}"


def apply_companion_mirror(platforms: list[PlatformResult]) -> list[PlatformResult]:
    """Mirror drift/pin/upstream from parent platform for companion rows."""
    by_id = {p.id: p for p in platforms}
    out: list[PlatformResult] = []
    for platform in platforms:
        parent_id = COMPANION_OF.get(platform.id, "")
        if not parent_id or parent_id not in by_id:
            out.append(platform)
            continue
        parent = by_id[parent_id]
        out.append(
            PlatformResult(
                id=platform.id,
                name=platform.name,
                skill=platform.skill,
                pin_path=platform.pin_path,
                pinned=parent.pinned,
                upstream_latest=parent.upstream_latest,
                upstream_sources=platform.upstream_sources,
                status=parent.status,
                drift=parent.drift,
                fetch_errors=platform.fetch_errors,
                companion_of=parent_id,
                actionable=platform.actionable,
                status_note=platform.status_note,
            )
        )
    return out


def actionable_drift(platforms: list[PlatformResult]) -> list[PlatformResult]:
    by_id = {p.id: p for p in platforms}
    out: list[PlatformResult] = []
    for platform in platforms:
        if not platform.drift:
            continue
        if not platform.actionable:
            continue
        parent_id = platform.companion_of or COMPANION_OF.get(platform.id, "")
        if parent_id and parent_id in by_id and by_id[parent_id].drift:
            continue
        out.append(platform)
    return out


def platform_to_dict(platform: PlatformResult, *, by_id: dict[str, PlatformResult]) -> dict[str, Any]:
    payload = asdict(platform)
    parent_id = platform.companion_of or COMPANION_OF.get(platform.id, "")
    payload["issue_suppressed"] = bool(
        platform.drift
        and (
            not platform.actionable
            or (parent_id and parent_id in by_id and by_id[parent_id].drift)
        )
    )
    return payload


def scan_platform(spec: PlatformSpec, *, offline_upstream: dict[str, Any] | None = None) -> PlatformResult:
    reader = PIN_READERS.get(spec.id)
    if reader is None:
        raise KeyError(f"no pin reader registered for platform {spec.id}")
    pinned = reader()
    upstream_sources: dict[str, dict[str, str]] = {}
    fetch_errors: list[str] = []
    versions: list[str] = []

    offline = (offline_upstream or {}).get(spec.id) or {}

    for source in spec.sources:
        label = source.label
        if label in offline:
            info = dict(offline[label])
            upstream_sources[label] = info
            if info.get("version"):
                versions.append(str(info["version"]))
            continue
        try:
            info = fetch_source(source)
            upstream_sources[label] = info
            if info.get("version"):
                versions.append(str(info["version"]))
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, json.JSONDecodeError) as exc:
            fetch_errors.append(f"{label}: {exc}")

    upstream_latest = version_max(*versions)
    if not pinned or not upstream_latest:
        status = "unknown"
        drift = False
    elif version_gt(upstream_latest, pinned):
        status = "drift"
        drift = True
    else:
        status = "aligned"
        drift = False

    return PlatformResult(
        id=spec.id,
        name=spec.name,
        skill=spec.skill,
        pin_path=spec.pin_path,
        pinned=pinned,
        upstream_latest=upstream_latest,
        upstream_sources=upstream_sources,
        status=status,
        drift=drift,
        fetch_errors=fetch_errors,
        actionable=spec.actionable,
        status_note=spec.status_note,
    )


def build_report(
    platforms: list[PlatformResult],
    *,
    run_url: str = "",
    git_sha: str = "",
) -> dict[str, Any]:
    platforms = apply_companion_mirror(platforms)
    by_id = {p.id: p for p in platforms}
    drift = actionable_drift(platforms)
    unknown = [p for p in platforms if p.status == "unknown"]
    return {
        "schema_version": 1,
        "generated_at": now_utc(),
        "run_url": run_url,
        "git_sha": git_sha,
        "summary": {
            "platform_count": len(platforms),
            "drift_count": len(drift),
            "unknown_count": len(unknown),
            "has_actionable_drift": bool(drift),
        },
        "platforms": [platform_to_dict(p, by_id=by_id) for p in platforms],
        "drift_platform_ids": [p.id for p in drift],
    }


def render_markdown(report: dict[str, Any]) -> str:
    lines = [
        "# Client release watch report",
        "",
        f"- Generated: `{report.get('generated_at')}`",
        f"- Git SHA: `{report.get('git_sha') or 'n/a'}`",
        f"- Drift count: **{report['summary']['drift_count']}**",
        "",
        "| Platform | Pinned | Upstream latest | Status | Skill |",
        "|---|---:|---:|---|---|",
    ]
    for item in report.get("platforms") or []:
        lines.append(
            "| {name} | `{pinned}` | `{upstream}` | `{status}` | `{skill}` |".format(
                name=item.get("name"),
                pinned=item.get("pinned") or "n/a",
                upstream=item.get("upstream_latest") or "n/a",
                status=item.get("status"),
                skill=item.get("skill"),
            )
        )
    lines.append("")
    notes = [item for item in report.get("platforms") or [] if item.get("status_note")]
    if notes:
        lines.append("## Notes")
        lines.append("")
        for item in notes:
            lines.append(f"- {item.get('name')}: {item.get('status_note')}")
        lines.append("")
    drift_ids = report.get("drift_platform_ids") or []
    if drift_ids:
        lines.append("## Action required")
        lines.append("")
        for item in report.get("platforms") or []:
            if item.get("id") not in drift_ids:
                continue
            lines.append(f"### {item.get('name')}")
            lines.append("")
            lines.append(f"- Pinned: `{item.get('pinned')}`")
            lines.append(f"- Upstream latest: `{item.get('upstream_latest')}`")
            lines.append(f"- Pin path: `{item.get('pin_path')}`")
            lines.append(f"- Run skill: `{item.get('skill')}`")
            if item.get("status_note"):
                lines.append(f"- Note: {item.get('status_note')}")
            playbook = PLATFORM_PLAYBOOKS.get(item.get("id") or "", {})
            for cmd in playbook.get("first_commands") or []:
                lines.append(f"- Next command: `{cmd}`")
            for label, info in (item.get("upstream_sources") or {}).items():
                url = info.get("url") or ""
                ver = info.get("version") or "n/a"
                lines.append(f"- {label}: `{ver}` — {url}")
            lines.append("")
    else:
        lines.append("All polled platforms are aligned with upstream (or could not be fetched).")
        lines.append("")
    return "\n".join(lines) + "\n"


def render_skill_plan(report: dict[str, Any]) -> str:
    """Human/agent routing: which Cursor skill to load, then which capture commands."""
    drift_ids = report.get("drift_platform_ids") or []
    lines = [
        "# Client release watch → skill routing",
        "",
        "Version discovery only — **load the skill in Cursor**, then run its capture commands.",
        "Do not bump pins from release metadata alone; ground truth still comes from capture/diff.",
        "",
    ]
    if not drift_ids:
        lines.extend([
            "No upstream-ahead platforms. Optional sanity scan:",
            "",
            "- Skill: `tokenkey-fingerprint-alignment-all`",
            "- Command: `bash ops/fingerprint/capture-all-fingerprints.sh`",
            "",
        ])
        return "\n".join(lines) + "\n"

    if len(drift_ids) > 1:
        lines.extend([
            "## Multi-platform drift",
            "",
            "- Umbrella skill (one PR): `tokenkey-fingerprint-alignment-all`",
            "- Command: `bash ops/fingerprint/capture-all-fingerprints.sh`",
            "",
            "Or handle each platform via its dedicated skill below.",
            "",
        ])

    for item in report.get("platforms") or []:
        platform_id = item.get("id")
        if platform_id not in drift_ids:
            continue
        playbook = PLATFORM_PLAYBOOKS.get(platform_id or "", {})
        skill = playbook.get("skill") or item.get("skill")
        lines.extend([
            f"## {item.get('name')} (`{platform_id}`)",
            "",
            f"- Upstream `{item.get('upstream_latest')}` > pin `{item.get('pinned')}`",
            f"- **Load skill:** `{skill}`",
            "- **Then run:**",
        ])
        if item.get("status_note"):
            lines.append(f"- Note: {item.get('status_note')}")
        for cmd in playbook.get("first_commands") or []:
            lines.append(f"  - `{cmd}`")
        lines.append("")
    return "\n".join(lines) + "\n"


def write_state(path: Path, report: dict[str, Any]) -> None:
    state = {
        "schema_version": 1,
        "updated_at": report.get("generated_at"),
        "git_sha": report.get("git_sha"),
        "platforms": {},
    }
    for item in report.get("platforms") or []:
        state["platforms"][item["id"]] = {
            "pinned": item.get("pinned"),
            "upstream_latest": item.get("upstream_latest"),
            "status": item.get("status"),
            "actionable": item.get("actionable"),
            "status_note": item.get("status_note"),
            "upstream_sources": item.get("upstream_sources"),
        }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def run_selftest() -> None:
    assert normalize_version("v2.1.195") == "2.1.195"
    assert normalize_version("rust-v0.142.3") == "0.142.3"
    assert normalize_version("2.2.1,5287492581195776") == "2.2.1"
    assert version_gt("2.1.196", "2.1.195")
    assert not version_gt("2.1.195", "2.1.195")
    assert version_max("2.1.194", "2.1.195") == "2.1.195"

    cc = read_pinned_claude_code()
    codex = read_pinned_codex()
    ag = read_pinned_antigravity()
    kiro = read_pinned_kiro()
    stainless = read_pinned_cc_stainless()
    gemini = read_pinned_gemini_cli()
    grok = read_pinned_grok_cli()
    kiro_cli = read_pinned_kiro_cli()
    assert cc, "expected cc_version pin"
    assert codex, "expected codex pin"
    assert ag, "expected antigravity pin"
    assert kiro, "expected kiro pin"
    assert stainless, "expected cc stainless pin"
    assert gemini, "expected gemini-cli pin"
    assert grok, "expected grok-cli pin"
    assert kiro_cli, "expected kiro-cli pin"

    offline = {
        "claude-code": {
            "GitHub anthropics/claude-code": {"version": "9.9.9", "url": "https://example.com/cc"},
            "npm @anthropic-ai/claude-code": {"version": "9.9.8", "url": "https://example.com/npm"},
        }
    }
    spec = next(p for p in PLATFORM_SPECS if p.id == "claude-code")
    result = scan_platform(spec, offline_upstream=offline)
    assert result.upstream_latest == "9.9.9"
    assert result.drift is True


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--selftest", action="store_true", help="Run built-in self-test and exit")
    parser.add_argument("--offline-fixture", type=Path, help="JSON fixture for offline platform upstream data")
    parser.add_argument("--report-json", type=Path, default=DEFAULT_REPORT_JSON)
    parser.add_argument("--report-md", type=Path, default=DEFAULT_REPORT_MD)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--run-url", default="")
    parser.add_argument("--git-sha", default="")
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument(
        "--plan",
        action="store_true",
        help="After scan, print skill routing (load skill in Cursor, then run capture commands)",
    )
    parser.add_argument(
        "--plan-only",
        type=Path,
        help="Print skill routing from an existing report.json (skip network scan)",
    )
    args = parser.parse_args(argv)

    if args.selftest:
        run_selftest()
        if not args.quiet:
            print("client-release-watch selftest ok")
        return 0

    if args.plan_only:
        report = json.loads(args.plan_only.read_text(encoding="utf-8"))
        print(render_skill_plan(report))
        return 1 if report.get("summary", {}).get("has_actionable_drift") else 0

    offline: dict[str, Any] = {}
    if args.offline_fixture:
        offline = json.loads(args.offline_fixture.read_text(encoding="utf-8"))

    platforms = [scan_platform(spec, offline_upstream=offline) for spec in PLATFORM_SPECS]
    report = build_report(platforms, run_url=args.run_url, git_sha=args.git_sha)

    args.report_json.parent.mkdir(parents=True, exist_ok=True)
    args.report_json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    args.report_md.write_text(render_markdown(report), encoding="utf-8")
    write_state(args.state, report)

    if not args.quiet:
        print(render_markdown(report))
    if args.plan:
        print(render_skill_plan(report))

    return 1 if report["summary"]["has_actionable_drift"] else 0


if __name__ == "__main__":
    sys.exit(main())
