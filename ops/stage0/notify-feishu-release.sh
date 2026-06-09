#!/usr/bin/env bash
set -euo pipefail

# Post a best-effort "release rolled out" card to the shared TokenKey ops Feishu
# webhook after a successful prod Stage0 deploy. Wired as the final step of
# .github/workflows/deploy-stage0.yml, AFTER external-health + post-deploy smoke,
# so the card means exactly: "this version is now LIVE on prod and smoke-green".
#
# Why this exists:
#   release.yml already pings Telegram at IMAGE-BUILD time, but nothing announced
#   the moment a version actually rolled out to prod. Operators asked for a Feishu
#   sync on every prod rollout; this script is the deterministic, code-ified
#   version of that ("自动化优先、一切代码化") instead of a remembered manual curl.
#
# Design:
#   - Best-effort. ANY send failure prints a ::warning:: and exits 0, so a flaky
#     webhook never reddens an already-successful prod deploy. The workflow step
#     itself is ALSO marked continue-on-error (belt and suspenders).
#   - Card shape + HMAC signature mirror the backend canonical sender
#     backend/internal/service/ops_feishu_notifier_tk.go (feishuCardPayload /
#     signFeishuWebhook): sign = base64(hmac_sha256(key = ts+"\n"+secret, msg="")).
#     So this rollout card looks and signs identically to prod alert cards.
#   - NEVER prints the webhook URL, the signing secret, or the computed sign.
#
# Usage:
#   TK_FEISHU_WEBHOOK_URL=... [TK_FEISHU_SIGNING_SECRET=...] \
#     bash ops/stage0/notify-feishu-release.sh <tag> <api_url> \
#       [--run-url URL] [--notes TEXT] [--dry-run]
#
#   <tag>      released image tag WITHOUT leading v (e.g. 1.7.83). A leading v is
#              tolerated and stripped.
#   <api_url>  prod gateway URL (e.g. https://api.tokenkey.dev).
#   --run-url  link to the deploy workflow run (optional).
#   --notes    release changelog (e.g. `gh release view` body) to inline under a
#              "本次更新" section; truncated for card readability. Empty/absent →
#              section omitted, card degrades to version + links (optional).
#   --dry-run  build + sign the card, print a SANITIZED payload, do NOT POST.
#
# Requires: python3, curl. GITHUB_REPOSITORY (auto-set in Actions) enriches the
# card with a GitHub Release link; absent → that link is simply omitted.

usage() {
  cat <<'EOF'
Usage:
  TK_FEISHU_WEBHOOK_URL=... [TK_FEISHU_SIGNING_SECRET=...] \
    bash ops/stage0/notify-feishu-release.sh <tag> <api_url> \
      [--run-url URL] [--notes TEXT] [--dry-run]
EOF
}

TAG=""
API_URL=""
RUN_URL=""
NOTES=""
DRY_RUN=0

# Flags that take a value: require the value to be present so a trailing
# `--run-url` (no arg) fails loudly instead of `shift 2` erroring under set -e.
need_val() { [ "$2" -ge 2 ] || { echo "[error] $1 needs a value" >&2; usage; exit 2; }; }

while [ $# -gt 0 ]; do
  case "$1" in
    --run-url) need_val "$1" "$#"; RUN_URL="$2"; shift 2 ;;
    --run-url=*) RUN_URL="${1#*=}"; shift ;;
    --notes) need_val "$1" "$#"; NOTES="$2"; shift 2 ;;
    --notes=*) NOTES="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h | --help) usage; exit 0 ;;
    --*) echo "[error] unknown flag: $1" >&2; usage; exit 2 ;;
    *)
      if [ -z "$TAG" ]; then
        TAG="$1"
      elif [ -z "$API_URL" ]; then
        API_URL="$1"
      else
        echo "[error] unexpected arg: $1" >&2; usage; exit 2
      fi
      shift ;;
  esac
done

if [ -z "$TAG" ] || [ -z "$API_URL" ]; then
  echo "[error] <tag> and <api_url> are required" >&2
  usage
  exit 2
fi

# deploy-stage0 passes a bare tag, but tolerate a leading v just in case.
TAG="${TAG#v}"

WEBHOOK="${TK_FEISHU_WEBHOOK_URL:-}"
SECRET="${TK_FEISHU_SIGNING_SECRET:-}"

if [ "$DRY_RUN" -ne 1 ] && [ -z "$WEBHOOK" ]; then
  # best-effort: a missing webhook is surfaced but never fails the deploy.
  echo "::warning::TK_FEISHU_WEBHOOK_URL empty; skipping release Feishu notification"
  exit 0
fi

# Build (and sign) the interactive-card payload in python3 so JSON escaping and
# the HMAC are byte-correct vs the Go canonical sender. python prints the JSON
# payload to stdout (sanitized when NF_DRY_RUN=1).
PAYLOAD_JSON="$(
  NF_TAG="$TAG" \
  NF_API_URL="$API_URL" \
  NF_RUN_URL="$RUN_URL" \
  NF_NOTES="$NOTES" \
  NF_SECRET="$SECRET" \
  NF_DRY_RUN="$DRY_RUN" \
  python3 <<'PY'
import base64
import datetime
import hashlib
import hmac
import json
import os
import time

tag = os.environ["NF_TAG"]
api_url = os.environ["NF_API_URL"]
run_url = os.environ.get("NF_RUN_URL", "").strip()
notes = os.environ.get("NF_NOTES", "").strip()
secret = os.environ.get("NF_SECRET", "").strip()
repo = os.environ.get("GITHUB_REPOSITORY", "").strip()
dry = os.environ.get("NF_DRY_RUN") == "1"

ts = int(time.time())
utc = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc)
cst = utc + datetime.timedelta(hours=8)
when = f"{utc:%Y-%m-%d %H:%M} UTC · {cst:%Y-%m-%d %H:%M} CST"

lines = [
    f"**版本**  v{tag}",
    "**环境**  prod",
    f"**API**  {api_url}",
    f"**上线时间**  {when}",
    "**烟测**  ✅ 通过",
]
links = []
if repo:
    links.append(f"[GitHub Release](https://github.com/{repo}/releases/tag/v{tag})")
if run_url:
    links.append(f"[发版流水线]({run_url})")
if links:
    lines.append("  ·  ".join(links))
body = "\n".join(lines)

elements = [{"tag": "div", "text": {"tag": "lark_md", "content": body}}]

# Clean + group the goreleaser changelog into an operator-friendly "本次更新"
# section: strip commit SHAs / (#PR) / contributor handles / section headers /
# the "Full Changelog" footer, then bucket by conventional-commit type. The full
# raw list always lives behind the GitHub Release link above, so over-trimming
# here is safe. Best-effort: if cleaning yields nothing we fall back to the raw
# (truncated) body rather than silently dropping a real changelog.
def clean_changelog(raw):
    import re

    feat, fix, other = [], [], []
    for ln in raw.splitlines():
        s = ln.strip()
        if not s:
            continue
        # drop goreleaser section headers ("## Changelog", "### Features") and
        # the "**Full Changelog**: <url>" footer line.
        if s.startswith("#"):
            continue
        if re.match(r"^\**\s*full changelog\b", s, flags=re.I):
            continue
        s = re.sub(r"^[\-\*\+]\s+", "", s)            # leading list marker
        s = re.sub(r"^[0-9a-f]{7,40}[:\s]\s*", "", s)  # leading commit SHA
        s = re.sub(r"\s*\(#\d+\)", "", s)              # (#PR) anywhere
        s = re.sub(r"\s*(by\s+@[\w-]+|\(@[\w-]+\)|@[\w-]+)\s*$", "", s, flags=re.I)  # trailing contributor
        s = s.strip()
        if not s:
            continue
        m = re.match(r"^(\w+)(\([^)]*\))?!?:\s*(.*)$", s)  # type(scope): desc
        if m and m.group(3).strip():
            typ, desc = m.group(1).lower(), m.group(3).strip()
        else:
            typ, desc = "", s
        if typ in ("feat", "feature"):
            feat.append(desc)
        elif typ in ("fix", "bugfix"):
            fix.append(desc)
        else:
            other.append(desc)
    sections = []
    for title, items in (("✨ 新功能", feat), ("🐛 修复", fix), ("🔧 其他", other)):
        if items:
            sections.append(f"**{title}**\n" + "\n".join(f"• {i}" for i in items))
    return "\n\n".join(sections)


if notes:
    cap = 1200
    cleaned = clean_changelog(notes)
    shown = cleaned if cleaned else notes  # fall back to raw if cleaning emptied it
    if len(shown) > cap:
        shown = shown[:cap].rstrip() + "\n\n…(更多见 GitHub Release)"
    if shown.strip():
        elements.append({"tag": "hr"})
        elements.append(
            {"tag": "div", "text": {"tag": "lark_md", "content": "**本次更新**\n" + shown}}
        )

payload = {
    "msg_type": "interactive",
    "card": {
        "header": {
            "template": "green",
            "title": {"tag": "plain_text", "content": f"🚀 TokenKey 发版上线 v{tag}"},
        },
        "elements": elements,
    },
}

# Mirror backend signFeishuWebhook: key = timestamp+"\n"+secret over empty msg.
if secret:
    string_to_sign = f"{ts}\n{secret}"
    sign = base64.b64encode(
        hmac.new(string_to_sign.encode("utf-8"), b"", hashlib.sha256).digest()
    ).decode("utf-8")
    payload["timestamp"] = str(ts)
    payload["sign"] = sign

if dry:
    safe = json.loads(json.dumps(payload))
    if "sign" in safe:
        safe["sign"] = "<redacted-sign>"
    print(json.dumps(safe, ensure_ascii=False, indent=2))
else:
    print(json.dumps(payload, ensure_ascii=False))
PY
)"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] feishu release card payload (sanitized; sign + webhook withheld):"
  echo "$PAYLOAD_JSON"
  exit 0
fi

BODY_FILE="$(mktemp)"
trap 'rm -f "$BODY_FILE"' EXIT

HTTP_CODE="$(
  curl -sS -o "$BODY_FILE" -w '%{http_code}' \
    -X POST "$WEBHOOK" \
    -H 'Content-Type: application/json' \
    --max-time 10 \
    -d "$PAYLOAD_JSON" 2>/dev/null || echo "000"
)"

# Feishu returns HTTP 200 + JSON {"code":0,...} on success; a non-zero code is a
# logical failure (bad sign, bad webhook) even with HTTP 200.
CODE_FIELD="$(
  python3 -c 'import sys,json
try:
    print(json.load(open(sys.argv[1])).get("code", 0))
except Exception:
    print(0)' "$BODY_FILE" 2>/dev/null || echo 0
)"

case "$HTTP_CODE" in
  2??)
    if [ "$CODE_FIELD" = "0" ]; then
      echo "[ok] feishu release card posted (v$TAG, http=$HTTP_CODE)"
      exit 0
    fi
    ;;
esac

SAFE_HOOK="$(
  printf '%s' "$WEBHOOK" | python3 -c 'import sys,urllib.parse as u
try:
    p=u.urlparse(sys.stdin.read().strip())
    print(f"{p.scheme}://{p.netloc}/<redacted>" if p.scheme and p.netloc else "<redacted-feishu-webhook>")
except Exception:
    print("<redacted-feishu-webhook>")' 2>/dev/null || echo "<redacted-feishu-webhook>"
)"
echo "::warning::feishu release notification failed (http=$HTTP_CODE code=$CODE_FIELD endpoint=$SAFE_HOOK)"
exit 0
