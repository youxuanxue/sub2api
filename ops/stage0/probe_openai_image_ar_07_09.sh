#!/usr/bin/env bash
# Probe aspect_ratio soft-control + matrix cases 07/09 on Responses + local PP.
# Requires docker python:3.12-slim for post-process (same as fidelity matrix).
set -euo pipefail

ACCOUNT_ID="${ACCOUNT_ID:?ACCOUNT_ID required}"
IMAGE_MODEL="${IMAGE_MODEL:-gpt-image-2}"
MAIN_MODEL="${MAIN_MODEL:-gpt-5.6-luna}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-180}"
UPSTREAM_URL="${UPSTREAM_URL:-https://chatgpt.com/backend-api/codex/responses}"
# Defaults must match DefaultOpenAICodexVersion (setting_gateway_runtime.go).
DEFAULT_CODEX_UA="codex-tui/0.162.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.162.0)"
CODEX_USER_AGENT="${CODEX_USER_AGENT:-$DEFAULT_CODEX_UA}"
CODEX_VERSION="${CODEX_VERSION:-0.162.0}"
PP_IMAGE="${PP_IMAGE:-python:3.12-slim}"

PSQL=(sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -q -A -t -v ON_ERROR_STOP=1)

fail_json() {
  python3 -c 'import json,sys; print(json.dumps({"verdict":"setup_error","error":sys.argv[1]},ensure_ascii=False))' "$1"
  exit 0
}

[[ "$ACCOUNT_ID" =~ ^[0-9]+$ ]] || fail_json "ACCOUNT_ID must be numeric"
if ! sudo docker image inspect "$PP_IMAGE" >/dev/null 2>&1; then
  sudo docker pull "$PP_IMAGE" >/dev/null || fail_json "pull $PP_IMAGE failed"
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
ACCESS_TOKEN="${fields[0]}"; ACCOUNT_NAME="${fields[1]}"; CUSTOM_UA="${fields[2]}"; CHATGPT_ACCOUNT_ID="${fields[3]}"
[[ -n "$ACCESS_TOKEN" ]] || fail_json "missing access_token"
[[ -n "$CUSTOM_UA" ]] && CODEX_USER_AGENT="$CUSTOM_UA"

WORKDIR="$(mktemp -d /tmp/fidar.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT
mkdir -p "$WORKDIR/pipcache"
cat >"$WORKDIR/pp.py" <<'PY'
import base64, io, json, sys
from PIL import Image

def sniff(raw):
    if raw[:3]==b"\xff\xd8\xff": return "jpeg"
    if raw[:8]==b"\x89PNG\r\n\x1a\n": return "png"
    if len(raw)>=12 and raw[:4]==b"RIFF" and raw[8:12]==b"WEBP": return "webp"
    return ""

def pad(src, tw, th, opaque):
    sw, sh = src.size
    scale = min(tw/sw, th/sh)
    nw, nh = max(1,int(sw*scale)), max(1,int(sh*scale))
    resized = src.resize((nw,nh), Image.Resampling.LANCZOS)
    canvas = Image.new("RGBA",(tw,th),(0,0,0,255 if opaque else 0))
    canvas.paste(resized, ((tw-nw)//2,(th-nh)//2), resized.convert("RGBA"))
    return canvas

req=json.load(sys.stdin)
raw=base64.b64decode(req["b64"])
tw,th=map(int, req["size"].lower().split("x"))
fmt=(req.get("format") or "png").lower()
if fmt=="jpg": fmt="jpeg"
bg=(req.get("background") or "opaque").lower()
opaque = not (bg=="transparent" and fmt in ("png","webp",""))
if fmt=="jpeg": opaque=True
src=Image.open(io.BytesIO(raw)).convert("RGBA")
canvas=pad(src,tw,th,opaque)
buf=io.BytesIO()
if fmt=="jpeg":
    canvas.convert("RGB").save(buf, format="JPEG", quality=88, optimize=True)
elif fmt=="webp":
    canvas.save(buf, format="WEBP", lossless=True, method=4)
else:
    fmt="png"; canvas.save(buf, format="PNG", optimize=True)
out=buf.getvalue()
print(json.dumps({"ok":True,"container":sniff(out) or fmt,"dimensions":f"{tw}x{th}","bytes":len(out),
                  "in_sniff":sniff(raw),"in_dimensions":f"{src.size[0]}x{src.size[1]}",
                  "b64":base64.b64encode(out).decode()}))
PY
sudo docker run --rm -v "$WORKDIR:/work" -v "$WORKDIR/pipcache:/root/.cache/pip" "$PP_IMAGE" \
  bash -lc 'pip install -q "Pillow>=10" && python -c "from PIL import Image; print(\"PIL_OK\")"' >/dev/null \
  || fail_json "pillow warm failed"

export ACCESS_TOKEN ACCOUNT_ID ACCOUNT_NAME IMAGE_MODEL MAIN_MODEL UPSTREAM_URL \
  CODEX_USER_AGENT CODEX_VERSION CHATGPT_ACCOUNT_ID REQUEST_TIMEOUT_SECONDS WORKDIR PP_IMAGE

python3 - <<'PY'
import base64, json, os, struct, subprocess, tempfile, time, zlib

ACCOUNT_ID=int(os.environ["ACCOUNT_ID"]); ACCOUNT_NAME=os.environ["ACCOUNT_NAME"]
IMAGE_MODEL=os.environ["IMAGE_MODEL"]; MAIN_MODEL=os.environ["MAIN_MODEL"]
UPSTREAM_URL=os.environ["UPSTREAM_URL"]; UA=os.environ["CODEX_USER_AGENT"]; VER=os.environ["CODEX_VERSION"]
TOKEN=os.environ["ACCESS_TOKEN"]; CHAT_ACCT=os.environ.get("CHATGPT_ACCOUNT_ID") or ""
TIMEOUT=int(os.environ.get("REQUEST_TIMEOUT_SECONDS") or "180")
WORKDIR=os.environ["WORKDIR"]; PP_IMAGE=os.environ["PP_IMAGE"]

def tiny_png(w=8,h=8,rgb=(200,80,40)):
    def chunk(t,d):
        return struct.pack(">I",len(d))+t+d+struct.pack(">I",zlib.crc32(t+d)&0xffffffff)
    rows=b"".join(b"\x00"+bytes(rgb)*w for _ in range(h))
    return b"\x89PNG\r\n\x1a\n"+chunk(b"IHDR",struct.pack(">IIBBBBB",w,h,8,2,0,0,0))+chunk(b"IDAT",zlib.compress(rows))+chunk(b"IEND",b"")

def data_url_png(**kw):
    return "data:image/png;base64,"+base64.b64encode(tiny_png(**kw)).decode()

def sniff(raw):
    if not raw: return ""
    if raw[:3]==b"\xff\xd8\xff": return "jpeg"
    if raw[:8]==b"\x89PNG\r\n\x1a\n": return "png"
    if len(raw)>=12 and raw[:4]==b"RIFF" and raw[8:12]==b"WEBP": return "webp"
    return ""

def dims_of(raw, call_size=""):
    if raw and sniff(raw)=="png" and len(raw)>=24:
        w,h=struct.unpack(">II", raw[16:24]); return f"{w}x{h}"
    return call_size or ""

def aspect_of(dims):
    if not dims or "x" not in dims: return None
    w,h=map(int, dims.lower().split("x"))
    if h<=0: return None
    return round(w/h, 3)

def close_aspect(got, want, tol=0.18):
    if got is None: return False
    return abs(got-want)/want <= tol

def postprocess(raw, size, fmt, background="opaque"):
    req={"b64":base64.b64encode(raw).decode(),"size":size,"format":fmt,"background":background}
    proc=subprocess.run(
        ["sudo","docker","run","--rm","-i","-v",f"{WORKDIR}:/work","-v",f"{WORKDIR}/pipcache:/root/.cache/pip",
         PP_IMAGE,"bash","-lc",'pip install -q "Pillow>=10" >/dev/null && python /work/pp.py'],
        input=json.dumps(req).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
    line=(proc.stdout.decode("utf-8","replace").strip().splitlines() or [""])[-1]
    obj=json.loads(line or "{}")
    if not obj.get("ok"):
        raise RuntimeError(obj.get("error") or proc.stderr[:200].decode("utf-8","replace"))
    return base64.b64decode(obj["b64"]), obj["container"], obj["dimensions"], obj

def call_responses(prompt, tool_extra=None, images=None, reasoning_effort="medium", size=None, output_format=None, background=None, quality="medium", action="generate"):
    tool={"type":"image_generation","action":action,"model":IMAGE_MODEL,"quality":quality}
    if size:
        w,h=map(int,size.lower().split("x"))
        tool["size"]=f"{((w+15)//16)*16}x{((h+15)//16)*16}"
    if output_format: tool["output_format"]=output_format
    if background: tool["background"]=background
    if tool_extra: tool.update(tool_extra)
    content=[{"type":"input_text","text":prompt}]
    for url in (images or []):
        content.append({"type":"input_image","image_url":url})
    payload={
        "model":MAIN_MODEL,"instructions":"","stream":True,
        "reasoning":{"effort":reasoning_effort,"summary":"auto"},
        "parallel_tool_calls":True,"include":["reasoning.encrypted_content"],"store":False,
        "tool_choice":{"type":"image_generation"},"tools":[tool],
        "input":[{"type":"message","role":"user","content":content}],
    }
    body_path=tempfile.mktemp(prefix="ar-"); hdr_path=tempfile.mktemp(prefix="arh-")
    curl=["curl","-sS","-m",str(TIMEOUT),"-o",body_path,"-D",hdr_path,"-w","%{http_code}",
          "-H",f"Authorization: Bearer {TOKEN}","-H","Content-Type: application/json","-H","Accept: text/event-stream",
          "-H","Originator: codex-tui","-H",f"Version: {VER}","-H",f"User-Agent: {UA}",
          "-X","POST",UPSTREAM_URL,"--data-binary",json.dumps(payload,ensure_ascii=False)]
    if CHAT_ACCT: curl.extend(["-H",f"chatgpt-account-id: {CHAT_ACCT}"])
    try: http_code=subprocess.check_output(curl,text=True).strip()
    except subprocess.CalledProcessError: http_code="000"
    image_b64=None; call_meta={}; usage={}; output_tokens=None
    try:
        with open(body_path,"r",encoding="utf-8",errors="replace") as f:
            for line in f:
                line=line.strip()
                if not line.startswith("data: "): continue
                data=line[6:].strip()
                if data=="[DONE]": break
                try: obj=json.loads(data)
                except Exception: continue
                resp=obj.get("response") or {}
                if resp.get("usage"):
                    usage=resp["usage"] or usage
                    if "output_tokens" in usage: output_tokens=usage.get("output_tokens")
                item=obj.get("item") or {}
                if item.get("type")=="image_generation_call" and item.get("result"):
                    image_b64=item["result"]
                    for k in ("size","quality","output_format","background"):
                        if item.get(k): call_meta[k]=item[k]
                for out in resp.get("output") or []:
                    if out.get("type")=="image_generation_call" and out.get("result"):
                        image_b64=out["result"]
                        for k in ("size","quality","output_format","background"):
                            if out.get(k): call_meta[k]=out[k]
    finally:
        for p in (body_path,hdr_path):
            try: os.unlink(p)
            except OSError: pass
    raw=base64.b64decode(image_b64) if image_b64 else None
    return {
        "http_code":http_code,"raw":raw,"call_meta":call_meta,"usage":usage,
        "output_tokens":output_tokens,
        "upstream_container":sniff(raw) if raw else "",
        "upstream_dimensions":dims_of(raw, call_meta.get("size","")),
        "tool_size": tool.get("size"),
        "prompt": prompt,
        "image_count": len(images or []),
    }

results={"aspect_ratio":[], "case_07":None, "case_09":None}

# --- aspect_ratio soft control (no ExplicitSize => no local pad) ---
for ar, want in (("16:9", 16/9), ("9:16", 9/16), ("1:1", 1.0)):
    prompt=f"a tiny gray cube on a table, marker AR={ar}"
    r=call_responses(prompt, quality="medium", output_format="png", background="opaque")
    dims=r["upstream_dimensions"]; got=aspect_of(dims)
    ok=str(r["http_code"]).startswith("2") and r["raw"] is not None and close_aspect(got, want)
    results["aspect_ratio"].append({
        "aspect_ratio":ar,"pass":ok,
        "http_code":r["http_code"],"upstream_dimensions":dims,"upstream_aspect":got,
        "want_aspect":round(want,3),"container":r["upstream_container"],
        "reasons":[] if ok else [f"aspect {got} not close to {want:.3f} or no image"],
    })
    time.sleep(0.8)

# Combined size + aspect_ratio: local pad owns final canvas (size wins).
r=call_responses("a tiny gray cube, marker AR=16:9", size="1024x1024", output_format="jpeg", background="opaque", quality="medium")
if r.get("raw"):
    final,_,fdims,_=postprocess(r["raw"],"1024x1024","jpeg")
    ok=fdims=="1024x1024" and sniff(final)=="jpeg"
    results["aspect_ratio"].append({
        "aspect_ratio":"16:9+size=1024x1024","pass":ok,
        "note":"ExplicitSize pads to size; soft AR marker still sent but final canvas=size",
        "upstream_dimensions":r["upstream_dimensions"],"final_dimensions":fdims,"final_container":sniff(final),
        "reasons":[] if ok else ["final canvas not 1024x1024 jpeg"],
    })
else:
    results["aspect_ratio"].append({"aspect_ratio":"16:9+size","pass":False,"reasons":["no upstream image"],"http_code":r["http_code"]})

# --- case 07: 16 reference images + 3840x2160 webp + local PP ---
images=[data_url_png(rgb=(30+i*10,80,200-i*5)) for i in range(16)]
prompt="combine the 16 reference icons into one collage, keep all symbols visible"
r7=call_responses(prompt, images=images, action="edit", size="3840x2160", output_format="webp", background="opaque", quality="medium")
entry7={"title":"16 ref images -> 3840x2160 webp","http_code":r7["http_code"],
        "upstream_dimensions":r7["upstream_dimensions"],"upstream_container":r7["upstream_container"],
        "image_count_sent":16}
if str(r7["http_code"]).startswith("2") and r7.get("raw"):
    try:
        final,cont,dims,meta=postprocess(r7["raw"],"3840x2160","webp")
        ok=dims=="3840x2160" and cont=="webp"
        entry7.update({"pass":ok,"final":{"dimensions":dims,"container":cont,"bytes":len(final),"in_dimensions":meta.get("in_dimensions")},
                       "reasons":[] if ok else [f"final {dims}/{cont}"]})
    except Exception as e:
        entry7.update({"pass":False,"reasons":[str(e)]})
else:
    entry7.update({"pass":False,"reasons":[f"upstream {r7['http_code']} no image"]})
results["case_07"]=entry7

# --- case 09: quality high/max format+size (matrix row) + thought-intensity output_tokens ---
r_base=call_responses("a small ginger cat", size="1024x1024", output_format="jpeg", background="opaque", quality="medium", reasoning_effort="medium")
time.sleep(1)
r_high=call_responses("a small ginger cat, highly detailed fur", size="1024x1024", output_format="jpeg", background="opaque", quality="high", reasoning_effort="medium")
time.sleep(1)
# Thought intensity: same image request but higher reasoning effort (product currently pins medium).
r_think=call_responses("a small ginger cat, highly detailed fur", size="1024x1024", output_format="jpeg", background="opaque", quality="high", reasoning_effort="high")

def finalize(r, label):
    if not r.get("raw"):
        return {"label":label,"pass":False,"reasons":["no image"],"http_code":r.get("http_code"),"output_tokens":r.get("output_tokens")}
    final,cont,dims,_=postprocess(r["raw"],"1024x1024","jpeg")
    ok=dims=="1024x1024" and cont=="jpeg"
    return {"label":label,"pass":ok,"final":{"dimensions":dims,"container":cont,"bytes":len(final)},
            "upstream_dimensions":r["upstream_dimensions"],"output_tokens":r.get("output_tokens"),
            "usage":r.get("usage"),"reasons":[] if ok else [f"final {dims}/{cont}"]}

fb=finalize(r_base,"quality=medium")
fh=finalize(r_high,"quality=high")
ft=finalize(r_think,"quality=high reasoning=high")
ot_base=r_base.get("output_tokens") or 0
ot_think=r_think.get("output_tokens") or 0
delta=None
if ot_base>0:
    delta=(ot_think-ot_base)/ot_base
think_pass=delta is not None and delta>0.10
results["case_09"]={
    "title":"quality high/max canvas + thought-intensity output_tokens",
    "matrix_row":{"medium":fb,"high":fh,"pass":fb["pass"] and fh["pass"]},
    "thought_intensity":{
        "baseline_output_tokens":ot_base,
        "high_effort_output_tokens":ot_think,
        "delta":None if delta is None else round(delta,4),
        "pass":think_pass,
        "rule":"output_tokens must rise >10% vs baseline",
        "note":"TK image Responses currently defaults reasoning.effort=medium; this probe also tries effort=high for evidence",
        "high_effort_row":ft,
    },
    "pass": fb["pass"] and fh["pass"],  # matrix row 09 = format/size; thought is separate special judgment
}

print(json.dumps({
    "verdict":"ar_07_09_complete",
    "path":"responses_tools_plus_local_fidelity",
    "probe":{"account_id":ACCOUNT_ID,"account_name":ACCOUNT_NAME,"image_model":IMAGE_MODEL},
    "results":results,
}, ensure_ascii=False, indent=2))
PY
