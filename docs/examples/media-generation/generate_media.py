#!/usr/bin/env python3
"""TokenKey media-generation example: image (Imagen) + video (Veo).

Calls the TokenKey OpenAI-compatible gateway, which routes Gemini-media models
to Google Vertex AI. Standard-library only (urllib/json/base64) — no pip install.

Endpoints used:
  POST {base}/v1/images/generations            -> sync, OpenAI-shaped image response
  POST {base}/v1/video/generations             -> async submit, returns a `vt_` task id
  GET  {base}/v1/video/generations/{task_id}   -> poll until terminal, returns raw Vertex LRO

Model ids (must be the exact Vertex names — the video submit path does NOT apply
account-level model remapping, so the client model name is forwarded verbatim):
  image: imagen-4.0-fast-generate-001   (also: imagen-4.0-generate-001, imagen-4.0-ultra-generate-001)
  video: veo-3.1-generate-001

Configuration (env):
  TOKENKEY_BASE_URL   default https://api.tokenkey.dev
  TOKENKEY_API_KEY    required — a key bound to the Gemini-media group

Usage:
  export TOKENKEY_API_KEY=sk-...
  python3 generate_media.py image "a red apple on a wooden table"
  python3 generate_media.py video "a golden retriever puppy running on a beach at sunset"

Output files are written to the current directory (out_image_*.png / out_video_*.mp4).
"""

import base64
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE_URL = os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev").rstrip("/")
API_KEY = os.environ.get("TOKENKEY_API_KEY", "")

IMAGE_MODEL = "imagen-4.0-fast-generate-001"
VIDEO_MODEL = "veo-3.1-generate-001"

# Veo takes ~1-2 min. Poll a little faster than that so we catch the terminal
# response before the gateway deletes the finished task record (terminal status
# is delivered once, then the record is cleaned up and further polls return 404).
POLL_INTERVAL_SECONDS = 10
POLL_MAX_ATTEMPTS = 60  # ~10 minutes ceiling


def _request(method, path, payload=None):
    """Return (status_code, parsed_json_or_None, raw_bytes)."""
    url = f"{BASE_URL}{path}"
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", f"Bearer {API_KEY}")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=300) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    parsed = None
    if raw:
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = None
    return status, parsed, raw


def _die(msg):
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def generate_image(prompt):
    print(f"[image] model={IMAGE_MODEL} prompt={prompt!r}")
    status, body, _ = _request(
        "POST",
        "/v1/images/generations",
        {"model": IMAGE_MODEL, "prompt": prompt, "n": 1},
    )
    if status != 200 or not body or "data" not in body:
        _die(f"image generation failed (http {status}): {body}")
    item = body["data"][0]
    b64 = item.get("b64_json")
    if not b64:
        # Some deployments return a URL instead of inline base64.
        url = item.get("url")
        _die(f"no inline image bytes; url={url}")
    out = f"out_image_{int(time.time())}.png"
    with open(out, "wb") as fh:
        fh.write(base64.b64decode(b64))
    print(f"[image] saved {out} ({os.path.getsize(out)} bytes)")


def generate_video(prompt):
    print(f"[video] model={VIDEO_MODEL} prompt={prompt!r}")
    status, body, _ = _request(
        "POST",
        "/v1/video/generations",
        {
            "model": VIDEO_MODEL,
            "prompt": prompt,
            "duration_seconds": 8,
            "aspect_ratio": "16:9",
        },
    )
    if status != 200 or not body:
        _die(f"video submit failed (http {status}): {body}")
    task_id = body.get("task_id") or body.get("id")
    if not task_id:
        _die(f"video submit returned no task id: {body}")
    print(f"[video] submitted task_id={task_id} status={body.get('status')}")

    for attempt in range(1, POLL_MAX_ATTEMPTS + 1):
        time.sleep(POLL_INTERVAL_SECONDS)
        status, body, _ = _request("GET", f"/v1/video/generations/{task_id}")
        if status == 404:
            _die(
                "task not found / expired — it likely finished and the record was "
                "cleaned up before this poll. Lower POLL_INTERVAL_SECONDS and retry."
            )
        if status != 200 or body is None:
            print(f"[video] poll {attempt}: http {status}, retrying")
            continue
        # Poll body is the raw Vertex long-running-operation object.
        if body.get("done") is True:
            if "error" in body:
                _die(f"video generation failed upstream: {body['error']}")
            videos = (body.get("response") or {}).get("videos") or []
            if not videos or "bytesBase64Encoded" not in videos[0]:
                _die(f"done but no video bytes in response: {body}")
            out = f"out_video_{int(time.time())}.mp4"
            with open(out, "wb") as fh:
                fh.write(base64.b64decode(videos[0]["bytesBase64Encoded"]))
            print(f"[video] done -> saved {out} ({os.path.getsize(out)} bytes)")
            return
        print(f"[video] poll {attempt}: in progress")
    _die("timed out waiting for video to finish")


def main():
    if not API_KEY:
        _die("set TOKENKEY_API_KEY")
    if len(sys.argv) < 3 or sys.argv[1] not in ("image", "video"):
        _die("usage: generate_media.py {image|video} <prompt>")
    mode, prompt = sys.argv[1], " ".join(sys.argv[2:])
    (generate_image if mode == "image" else generate_video)(prompt)


if __name__ == "__main__":
    main()
