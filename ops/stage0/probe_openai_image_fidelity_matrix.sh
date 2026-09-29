#!/usr/bin/env bash
# Responses + local exact-canvas/format fidelity matrix.
# Post-process runs in docker.io/library/python:3.12-slim + Pillow on the edge host.
# STRICT: never logs tokens.
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
IMAGE_MODEL="${IMAGE_MODEL:-gpt-image-2}"
MAIN_MODEL="${MAIN_MODEL:-gpt-5.6-luna}"
CASES="${CASES:-01,02,03,04,05,06,08,10}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-150}"
UPSTREAM_URL="${UPSTREAM_URL:-https://chatgpt.com/backend-api/codex/responses}"
DEFAULT_CODEX_UA="codex-tui/0.154.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
CODEX_USER_AGENT="${CODEX_USER_AGENT:-$DEFAULT_CODEX_UA}"
CODEX_VERSION="${CODEX_VERSION:-0.154.0}"
PP_IMAGE="${PP_IMAGE:-python:3.12-slim}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

fail_json() {
  python3 -c 'import json,sys; print(json.dumps({"verdict":"setup_error","error":sys.argv[1]},ensure_ascii=False))' "$1"
  exit 0
}

[[ "$ACCOUNT_ID" =~ ^[0-9]+$ ]] || fail_json "ACCOUNT_ID must be numeric"

# Ensure postprocess image (cached after first pull).
if ! sudo docker image inspect "$PP_IMAGE" >/dev/null 2>&1; then
  sudo docker pull "$PP_IMAGE" >/dev/null || fail_json "failed to pull $PP_IMAGE"
fi

account_json="$("${PSQL[@]}" -c "
SELECT COALESCE(row_to_json(t)::text, '')
FROM (
  SELECT id, COALESCE(name,'') AS name,
         COALESCE(credentials->>'access_token','') AS access_token,
         COALESCE(credentials->>'user_agent','') AS user_agent,
         COALESCE(credentials->>'chatgpt_account_id','') AS chatgpt_account_id
  FROM accounts WHERE id = ${ACCOUNT_ID} AND deleted_at IS NULL
) t;
")" || fail_json "account lookup failed"
account_json="$(printf '%s' "$account_json" | tr -d '\n')"
[[ -n "$account_json" ]] || fail_json "account not found"

mapfile -t fields < <(python3 - "$account_json" <<'PY'
import json,sys
o=json.loads(sys.argv[1])
for k in ("access_token","name","user_agent","chatgpt_account_id"):
    print(o.get(k) or "")
PY
)
ACCESS_TOKEN="${fields[0]}"
ACCOUNT_NAME="${fields[1]}"
CUSTOM_UA="${fields[2]}"
CHATGPT_ACCOUNT_ID="${fields[3]}"
[[ -n "$ACCESS_TOKEN" ]] || fail_json "missing access_token"
[[ -n "$CUSTOM_UA" ]] && CODEX_USER_AGENT="$CUSTOM_UA"

WORKDIR="$(mktemp -d /tmp/fidmatrix.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT

# Persist postprocess helper into workdir for docker -v mount.
cat >"$WORKDIR/pp.py" <<'PY'
import base64, io, json, sys
from PIL import Image

def sniff(raw: bytes) -> str:
    if raw[:3] == b"\xff\xd8\xff":
        return "jpeg"
    if raw[:8] == b"\x89PNG\r\n\x1a\n":
        return "png"
    if len(raw) >= 12 and raw[:4] == b"RIFF" and raw[8:12] == b"WEBP":
        return "webp"
    return ""

def jpeg_q(quality, compression):
    if compression is not None:
        return max(1, min(100, int(compression)))
    return {"low": 45, "medium": 72, "high": 88, "xhigh": 95, "max": 95}.get((quality or "").lower(), 92)

def webp_method(quality, compression):
    if compression is not None:
        c = int(compression)
        return 0 if c <= 30 else 6 if c >= 80 else 4
    if (quality or "").lower() in ("high", "xhigh", "max"):
        return 6
    if (quality or "").lower() == "low":
        return 0
    return 4

def pad(src: Image.Image, tw, th, opaque: bool) -> Image.Image:
    sw, sh = src.size
    scale = min(tw / sw, th / sh)
    nw, nh = max(1, int(sw * scale)), max(1, int(sh * scale))
    resized = src.resize((nw, nh), Image.Resampling.LANCZOS)
    if opaque:
        canvas = Image.new("RGBA", (tw, th), (0, 0, 0, 255))
    else:
        canvas = Image.new("RGBA", (tw, th), (0, 0, 0, 0))
    canvas.paste(resized, ((tw - nw) // 2, (th - nh) // 2), resized.convert("RGBA"))
    return canvas

def main():
    req = json.load(sys.stdin)
    raw = base64.b64decode(req["b64"])
    tw, th = map(int, req["size"].lower().split("x"))
    fmt = (req.get("format") or "png").lower()
    if fmt == "jpg":
        fmt = "jpeg"
    bg = (req.get("background") or "opaque").lower()
    quality = req.get("quality") or "medium"
    compression = req.get("compression")
    opaque = True
    if bg == "transparent" and fmt in ("png", "webp", ""):
        opaque = False
    if fmt == "jpeg":
        opaque = True
    src = Image.open(io.BytesIO(raw)).convert("RGBA")
    canvas = pad(src, tw, th, opaque)
    buf = io.BytesIO()
    if fmt == "jpeg":
        canvas.convert("RGB").save(buf, format="JPEG", quality=jpeg_q(quality, compression), optimize=True)
    elif fmt == "webp":
        canvas.save(buf, format="WEBP", lossless=True, method=webp_method(quality, compression))
    else:
        fmt = "png"
        canvas.save(buf, format="PNG", optimize=True)
    out = buf.getvalue()
    print(json.dumps({
        "ok": True,
        "container": sniff(out) or fmt,
        "dimensions": f"{tw}x{th}",
        "bytes": len(out),
        "in_sniff": sniff(raw),
        "in_dimensions": f"{src.size[0]}x{src.size[1]}",
        "b64": base64.b64encode(out).decode("ascii"),
    }))

if __name__ == "__main__":
    main()
PY

# One-time pillow install layer via a named container volume cache dir.
PIP_CACHE="$WORKDIR/pipcache"
mkdir -p "$PIP_CACHE"
# Warm pillow into a disposable container filesystem cache file for reuse within this run.
sudo docker run --rm -v "$WORKDIR:/work" -v "$PIP_CACHE:/root/.cache/pip" "$PP_IMAGE" \
  bash -lc 'pip install -q "Pillow>=10" && python -c "from PIL import Image; print(\"PIL_OK\")"' \
  >/tmp/fid-pil-warm.out 2>/tmp/fid-pil-warm.err || fail_json "pillow warm failed: $(tr '\n' ' ' </tmp/fid-pil-warm.err | cut -c1-200)"

export ACCESS_TOKEN ACCOUNT_ID ACCOUNT_NAME IMAGE_MODEL MAIN_MODEL UPSTREAM_URL \
  CODEX_USER_AGENT CODEX_VERSION CHATGPT_ACCOUNT_ID REQUEST_TIMEOUT_SECONDS CASES \
  WORKDIR PP_IMAGE

python3 - <<'PY'
import base64, hashlib, json, os, struct, subprocess, tempfile, time

ACCOUNT_ID = int(os.environ["ACCOUNT_ID"])
ACCOUNT_NAME = os.environ["ACCOUNT_NAME"]
IMAGE_MODEL = os.environ["IMAGE_MODEL"]
MAIN_MODEL = os.environ["MAIN_MODEL"]
UPSTREAM_URL = os.environ["UPSTREAM_URL"]
UA = os.environ["CODEX_USER_AGENT"]
VER = os.environ["CODEX_VERSION"]
TOKEN = os.environ["ACCESS_TOKEN"]
CHAT_ACCT = os.environ.get("CHATGPT_ACCOUNT_ID") or ""
TIMEOUT = int(os.environ.get("REQUEST_TIMEOUT_SECONDS") or "150")
WORKDIR = os.environ["WORKDIR"]
PP_IMAGE = os.environ["PP_IMAGE"]
wanted = {c.strip() for c in os.environ.get("CASES", "").split(",") if c.strip()}

CASES = {
    "01": dict(title="webp transparent 1024", size="1024x1024", output_format="webp", background="transparent", quality="medium"),
    "02": dict(title="jpeg opaque 1024", size="1024x1024", output_format="jpeg", background="opaque", quality="medium"),
    "03": dict(title="png transparent 1024", size="1024x1024", output_format="png", background="transparent", quality="medium"),
    "04": dict(title="min size 1024x640 webp", size="1024x640", output_format="webp", background="opaque", quality="high"),
    "05": dict(title="max size 3840x2160 webp", size="3840x2160", output_format="webp", background="opaque", quality="medium"),
    "06": dict(title="oversize 3840x2176", size="3840x2176", output_format="webp", background="opaque", quality="medium", local_only=True),
    "08": dict(title="quality medium vs high volume", size="1024x1024", output_format="jpeg", background="opaque", quality_pair=("medium", "high")),
    "10": dict(title="compression 50 vs 95 volume", size="1024x1024", output_format="jpeg", background="opaque", quality="medium", compression_pair=(50, 95)),
}

MAX_PIXELS = 8294400
MAX_ASPECT = 3
QUANTUM = 16

def parse_wh(size):
    w, h = size.lower().split("x")
    return int(w), int(h)

def validate_size(size):
    try:
        w, h = parse_wh(size)
    except Exception:
        return f"bad size {size}"
    if w * h > MAX_PIXELS:
        return f"total pixels {w*h} exceeds max {MAX_PIXELS}"
    long, short = (w, h) if w >= h else (h, w)
    if long > short * MAX_ASPECT:
        return f"aspect > {MAX_ASPECT}:1"
    return None

def ceil16(v):
    return ((v + QUANTUM - 1) // QUANTUM) * QUANTUM

def sniff(raw: bytes) -> str:
    if raw[:3] == b"\xff\xd8\xff":
        return "jpeg"
    if raw[:8] == b"\x89PNG\r\n\x1a\n":
        return "png"
    if len(raw) >= 12 and raw[:4] == b"RIFF" and raw[8:12] == b"WEBP":
        return "webp"
    return ""

def postprocess(raw: bytes, size: str, output_format: str, background: str, quality="medium", compression=None):
    req = {
        "b64": base64.b64encode(raw).decode("ascii"),
        "size": size,
        "format": output_format,
        "background": background,
        "quality": quality,
    }
    if compression is not None:
        req["compression"] = compression
    proc = subprocess.run(
        ["sudo", "docker", "run", "--rm", "-i",
         "-v", f"{WORKDIR}:/work",
         "-v", f"{WORKDIR}/pipcache:/root/.cache/pip",
         PP_IMAGE,
         "bash", "-lc", 'pip install -q "Pillow>=10" >/dev/null && python /work/pp.py'],
        input=json.dumps(req).encode("utf-8"),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=180,
    )
    out = proc.stdout.decode("utf-8", "replace").strip()
    try:
        obj = json.loads(out.splitlines()[-1] if out else "{}")
    except Exception:
        raise RuntimeError(f"pp bad json rc={proc.returncode}: {out[:200]} err={proc.stderr[:200]}")
    if not obj.get("ok"):
        raise RuntimeError(obj.get("error") or f"pp failed: {proc.stderr[:200]}")
    final = base64.b64decode(obj["b64"])
    return final, obj["container"], obj["dimensions"], obj

def call_upstream(size, output_format, background, quality, prompt="a small ginger cat, simple studio photo"):
    err = validate_size(size)
    if err:
        return {"http_code": "400", "error": err, "raw": None}
    w, h = parse_wh(size)
    up_size = f"{ceil16(w)}x{ceil16(h)}"
    tool = {
        "type": "image_generation", "action": "generate", "model": IMAGE_MODEL,
        "size": up_size, "output_format": output_format, "background": background, "quality": quality,
    }
    payload = {
        "model": MAIN_MODEL, "instructions": "", "stream": True,
        "reasoning": {"effort": "medium", "summary": "auto"},
        "parallel_tool_calls": True, "include": ["reasoning.encrypted_content"], "store": False,
        "tool_choice": {"type": "image_generation"}, "tools": [tool],
        "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": prompt}]}],
    }
    body_path = tempfile.mktemp(prefix="fid-")
    hdr_path = tempfile.mktemp(prefix="fidh-")
    curl = [
        "curl", "-sS", "-m", str(TIMEOUT), "-o", body_path, "-D", hdr_path, "-w", "%{http_code}",
        "-H", f"Authorization: Bearer {TOKEN}",
        "-H", "Content-Type: application/json", "-H", "Accept: text/event-stream",
        "-H", "Originator: codex-tui", "-H", f"Version: {VER}", "-H", f"User-Agent: {UA}",
        "-X", "POST", UPSTREAM_URL, "--data-binary", json.dumps(payload, ensure_ascii=False),
    ]
    if CHAT_ACCT:
        curl.extend(["-H", f"chatgpt-account-id: {CHAT_ACCT}"])
    try:
        http_code = subprocess.check_output(curl, text=True).strip()
    except subprocess.CalledProcessError:
        http_code = "000"
    image_b64 = None
    call_meta, tool_meta = {}, {}
    try:
        with open(body_path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line.startswith("data: "):
                    continue
                data = line[6:].strip()
                if data == "[DONE]":
                    break
                try:
                    obj = json.loads(data)
                except Exception:
                    continue
                item = obj.get("item") or {}
                if item.get("type") == "image_generation_call" and item.get("result"):
                    image_b64 = item["result"]
                    for k in ("size", "quality", "output_format", "background"):
                        if item.get(k):
                            call_meta[k] = item[k]
                resp = obj.get("response") or {}
                for tool in resp.get("tools") or []:
                    if isinstance(tool, dict) and tool.get("type") == "image_generation":
                        for k in ("size", "quality", "output_format", "background", "model"):
                            if tool.get(k):
                                tool_meta[k] = tool[k]
                for out in resp.get("output") or []:
                    if out.get("type") == "image_generation_call" and out.get("result"):
                        image_b64 = out["result"]
                        for k in ("size", "quality", "output_format", "background"):
                            if out.get(k):
                                call_meta[k] = out[k]
    finally:
        for p in (body_path, hdr_path):
            try:
                os.unlink(p)
            except OSError:
                pass
    raw = base64.b64decode(image_b64) if image_b64 else None
    up_dims = call_meta.get("size") or ""
    if raw and sniff(raw) == "png" and len(raw) >= 24:
        w, h = struct.unpack(">II", raw[16:24])
        up_dims = f"{w}x{h}"
    return {
        "http_code": http_code,
        "upstream_size": up_size,
        "upstream_container": sniff(raw) if raw else "",
        "upstream_dimensions": up_dims,
        "call_meta": call_meta,
        "tool_meta": tool_meta,
        "raw": raw,
    }

results = []
for cid in sorted(wanted):
    if cid not in CASES:
        results.append({"case": cid, "pass": False, "reasons": ["unknown case"]})
        continue
    spec = CASES[cid]
    entry = {"case": cid, "title": spec["title"], "model": IMAGE_MODEL}

    if spec.get("local_only"):
        err = validate_size(spec["size"])
        ok = err is not None
        entry.update({"pass": ok, "http_code": "400" if ok else "200", "gate": "local_validate", "error": err, "reasons": [] if ok else ["expected 400"]})
        results.append(entry)
        continue

    if "quality_pair" in spec:
        a_q, b_q = spec["quality_pair"]
        ra = call_upstream(spec["size"], spec["output_format"], spec["background"], a_q)
        time.sleep(1)
        rb = call_upstream(spec["size"], spec["output_format"], spec["background"], b_q, prompt="a small ginger cat, detailed fur")
        if not ra.get("raw") or not rb.get("raw"):
            entry.update({"pass": False, "reasons": ["missing upstream image"], "a_http": ra.get("http_code"), "b_http": rb.get("http_code")})
            results.append(entry)
            continue
        fa, ca, da, _ = postprocess(ra["raw"], spec["size"], spec["output_format"], spec["background"], quality=a_q)
        fb, cb, db, _ = postprocess(rb["raw"], spec["size"], spec["output_format"], spec["background"], quality=b_q)
        delta = abs(len(fb) - len(fa)) / max(len(fa), len(fb), 1)
        reasons = []
        if da != spec["size"] or ca != spec["output_format"]:
            reasons.append(f"final_a {da}/{ca}")
        if db != spec["size"] or cb != spec["output_format"]:
            reasons.append(f"final_b {db}/{cb}")
        if delta <= 0.10:
            reasons.append(f"volume_delta {delta:.2%}<=10%")
        entry.update({
            "pass": not reasons, "reasons": reasons,
            "final": {"dimensions": db, "container": cb, "bytes_a": len(fa), "bytes_b": len(fb), "volume_delta": round(delta, 4)},
            "upstream_a": {k: ra.get(k) for k in ("http_code", "upstream_dimensions", "upstream_container", "tool_meta")},
            "upstream_b": {k: rb.get(k) for k in ("http_code", "upstream_dimensions", "upstream_container", "tool_meta")},
        })
        results.append(entry)
        continue

    if "compression_pair" in spec:
        c_lo, c_hi = spec["compression_pair"]
        r = call_upstream(spec["size"], spec["output_format"], spec["background"], spec.get("quality", "medium"))
        if not r.get("raw"):
            entry.update({"pass": False, "reasons": ["missing upstream image"], "http_code": r.get("http_code")})
            results.append(entry)
            continue
        flo, clo, dlo, _ = postprocess(r["raw"], spec["size"], spec["output_format"], spec["background"], quality=spec.get("quality", "medium"), compression=c_lo)
        fhi, chi, dhi, _ = postprocess(r["raw"], spec["size"], spec["output_format"], spec["background"], quality=spec.get("quality", "medium"), compression=c_hi)
        delta = abs(len(fhi) - len(flo)) / max(len(flo), len(fhi), 1)
        reasons = []
        if dlo != spec["size"] or clo != spec["output_format"]:
            reasons.append(f"final {dlo}/{clo}")
        if delta <= 0.10:
            reasons.append(f"volume_delta {delta:.2%}<=10%")
        entry.update({
            "pass": not reasons, "reasons": reasons,
            "final": {"dimensions": dlo, "container": clo, "bytes_lo": len(flo), "bytes_hi": len(fhi), "volume_delta": round(delta, 4)},
            "upstream": {k: r.get(k) for k in ("http_code", "upstream_dimensions", "upstream_container", "tool_meta")},
        })
        results.append(entry)
        continue

    r = call_upstream(spec["size"], spec["output_format"], spec["background"], spec.get("quality", "medium"))
    if str(r.get("http_code")) != "200" or not r.get("raw"):
        entry.update({"pass": False, "reasons": [r.get("error") or f"upstream {r.get('http_code')} no image"], "upstream": {k: r.get(k) for k in ("http_code", "upstream_dimensions", "upstream_container", "tool_meta")}})
        results.append(entry)
        continue
    try:
        final, container, dims, meta = postprocess(r["raw"], spec["size"], spec["output_format"], spec["background"], quality=spec.get("quality", "medium"))
    except Exception as e:
        entry.update({"pass": False, "reasons": [str(e)], "upstream": {k: r.get(k) for k in ("http_code", "upstream_dimensions", "upstream_container")}})
        results.append(entry)
        continue
    reasons = []
    if dims != spec["size"]:
        reasons.append(f"dims {dims}")
    if container != spec["output_format"]:
        reasons.append(f"container {container}")
    entry.update({
        "pass": not reasons, "reasons": reasons,
        "final": {"container": container, "dimensions": dims, "bytes": len(final), "sha16": hashlib.sha256(final).hexdigest()[:16], "in_dimensions": meta.get("in_dimensions"), "in_sniff": meta.get("in_sniff")},
        "upstream": {k: r.get(k) for k in ("http_code", "upstream_size", "upstream_dimensions", "upstream_container", "tool_meta", "call_meta")},
    })
    results.append(entry)
    time.sleep(0.5)

passed = sum(1 for r in results if r.get("pass"))
print(json.dumps({
    "verdict": "matrix_complete",
    "path": "responses_tools_plus_local_fidelity_docker_pillow",
    "note": "edge gateway still 1.8.265 Direct; this is Responses upstream + local pad/format (pending PR). Not gateway /v1/images.",
    "probe": {"account_id": ACCOUNT_ID, "account_name": ACCOUNT_NAME, "image_model": IMAGE_MODEL, "gateway_image": "ghcr.io/youxuanxue/sub2api:1.8.265"},
    "summary": {"passed": passed, "total": len(results), "pass_rate": round(passed / max(len(results), 1), 3)},
    "excluded": {"07": "16 ref-image edits not in this probe", "09": "thought-intensity output_tokens out of scope"},
    "cases": results,
}, ensure_ascii=False, indent=2))
PY
