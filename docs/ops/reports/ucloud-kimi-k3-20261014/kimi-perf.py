import concurrent.futures as cf
import datetime as dt
import hashlib, json, os, random, subprocess, time, urllib.request, urllib.error
from pathlib import Path

KEY = os.environ["TK_PERF_KEY"]
KID = int(os.environ["TK_PERF_KEY_ID"])
RUN = "cn-prod-kimik3-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
BASE = "https://api-cn.tokenkey.dev"
MODEL = "kimi-k3"
OUT = Path("/tmp") / RUN
OUT.mkdir(mode=0o700)
rows = []


def emit(obj):
    print(json.dumps(obj, ensure_ascii=False), flush=True)


def sql(s):
    p = subprocess.run(
        [
            "docker",
            "exec",
            "-i",
            "tokenkey-postgres",
            "psql",
            "-U",
            "tokenkey",
            "-d",
            "tokenkey",
            "-X",
            "-q",
            "-A",
            "-t",
            "-v",
            "ON_ERROR_STOP=1",
        ],
        input=s,
        text=True,
        capture_output=True,
        check=True,
    )
    return [json.loads(x) for x in p.stdout.splitlines() if x.strip()]


def usage():
    return sql(
        f"SELECT row_to_json(t) FROM (SELECT request_id,account_id,group_id,model,upstream_model,upstream_response_model,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,duration_ms,gateway_latency_ms,first_token_ms,total_cost,actual_cost FROM usage_logs WHERE api_key_id={KID} AND created_at>=NOW()-interval '45 minutes') t;"
    )


def corpus(n):
    rng = random.Random(53014)
    words = "service gateway request response database cache account routing latency capacity window balance stream stable metric report network client server token model".split()
    return " ".join(rng.choices(words, k=n))


def request(target, words, seq, phase, concurrency=1):
    text = corpus(words)
    prompt = (
        "Read the reference corpus below as data.\n<reference>\n"
        + text
        + "\n</reference>\nIgnore the reference data. Output the following sentence verbatim 20 times, one copy per line, without commentary: The API service accepts a request, routes it to a healthy model, streams the response, and records its usage. Request marker: "
        + str(seq)
    )
    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "stream": True,
        "stream_options": {"include_usage": True},
        "max_tokens": 2048,
    }
    data = json.dumps(payload).encode()
    marker = f"{RUN}-{phase}-{target}-{seq}"
    r = {
        "phase": phase,
        "target_tokens": target,
        "corpus_words": words,
        "concurrency": concurrency,
        "seq": seq,
        "started_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
        "client_request_id": marker,
        "request_bytes": len(data),
        "corpus_sha256": hashlib.sha256(text.encode()).hexdigest(),
        "http_status": None,
        "success": False,
        "usage": {},
        "content_events": 0,
        "reasoning_events": 0,
    }
    start = time.perf_counter()
    first = None
    last = None
    first_any = None
    finish = None
    done = False
    req = urllib.request.Request(
        BASE + "/v1/chat/completions",
        data=data,
        headers={
            "Authorization": "Bearer " + KEY,
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
            "User-Agent": "tokenkey-perf-report/1.0",
            "X-Client-Request-ID": marker,
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=150) as resp:
            r["http_status"] = resp.status
            r["server_request_id"] = resp.headers.get("X-Request-ID")
            r["headers_ms"] = (time.perf_counter() - start) * 1000
            for raw in resp:
                now = time.perf_counter()
                if now - start > 180:
                    raise TimeoutError("request wall time exceeded 180 seconds")
                line = raw.decode(errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                chunk = line[5:].strip()
                if chunk == "[DONE]":
                    done = True
                    break
                obj = json.loads(chunk)
                if obj.get("error"):
                    r["error"] = json.dumps(obj["error"], ensure_ascii=False)[:600]
                    break
                if obj.get("usage"):
                    r["usage"] = obj["usage"]
                if obj.get("model"):
                    r["response_model"] = obj["model"]
                for choice in obj.get("choices", []):
                    d = choice.get("delta") or {}
                    if d.get("reasoning_content") or d.get("reasoning"):
                        r["reasoning_events"] += 1
                        if first_any is None:
                            first_any = now
                    if d.get("content"):
                        r["content_events"] += 1
                        if first is None:
                            first = now
                        if first_any is None:
                            first_any = now
                        last = now
                    if choice.get("finish_reason"):
                        finish = choice["finish_reason"]
            r["success"] = bool(
                first is not None
                and finish in ("stop", "length")
                and done
                and not r.get("error")
            )
    except urllib.error.HTTPError as e:
        r["http_status"] = e.code
        r["server_request_id"] = e.headers.get("X-Request-ID")
        r["error"] = (
            e.read(2500).decode(errors="replace").replace(KEY, "<redacted>")[:600]
        )
    except Exception as e:
        r["error"] = str(e).replace(KEY, "<redacted>")[:600]
    r["latency_ms"] = (time.perf_counter() - start) * 1000
    r["ttft_ms"] = (first - start) * 1000 if first else None
    r["first_any_ms"] = (first_any - start) * 1000 if first_any else None
    r["generation_ms"] = (
        (last - first) * 1000 if first is not None and last is not None else None
    )
    r["finish_reason"] = finish
    r["done"] = done
    if not r["success"] and not r.get("error"):
        r["error"] = (
            "no_visible_content_before_output_limit"
            if first is None and finish == "length"
            else "incomplete_stream_or_missing_content"
        )
    out = r["usage"].get("completion_tokens")
    reason = (r["usage"].get("completion_tokens_details") or {}).get(
        "reasoning_tokens", 0
    )
    r["visible_tokens"] = (
        out - reason
        if out is not None
        and (
            not r["reasoning_events"]
            or "reasoning_tokens" in (r["usage"].get("completion_tokens_details") or {})
        )
        else None
    )
    r["tpot_ms"] = (
        r["generation_ms"] / (r["visible_tokens"] - 1)
        if r["visible_tokens"] and r["visible_tokens"] > 1 and r["generation_ms"]
        else None
    )
    r["tokens_per_second"] = 1000 / r["tpot_ms"] if r["tpot_ms"] else None
    with (OUT / (marker + ".json")).open("w") as f:
        json.dump(r, f, ensure_ascii=False)
    return r


emit(
    {
        "run_id": RUN,
        "api_key_id": KID,
        "group_id": 2,
        "routing_mode": "direct",
        "base_url": BASE,
        "model": MODEL,
        "output_dir": str(OUT),
        "max_output_tokens": 2048,
        "thinking": "provider_default",
    }
)

r = request(4096, 3000, 0, "calibration")
emit(r)
if not r["success"] or not r["usage"].get("prompt_tokens"):
    emit({"aborted": "calibration_failed"})
    raise SystemExit(2)
# Vocabulary words in this synthetic corpus have stable near-one-token encoding.
# Calibrate the fixed prompt overhead from the provider usage, retain actual tokens.
calibration_overhead = r["usage"]["prompt_tokens"] - 3000
formal_start = time.monotonic()
for target, concurrency in [
    (4096, 10),
    (12288, 8),
    (16384, 6),
    (32768, 6),
    (65536, 4),
    (131072, 2),
    (262144, 1),
    (524288, 1),
]:
    words = max(1, target - calibration_overhead)
    if time.monotonic() - formal_start > 1200:
        emit({"skipped_target": target, "reason": "20_minute_runtime_guard"})
        continue
    previous = usage()
    spent = sum(x["actual_cost"] for x in previous)
    reserve = (target * 3.3 / 1e6 + 2048 * 16.5 / 1e6) * (1 + 2 * concurrency)
    if spent + reserve > 4.5:
        emit(
            {
                "skipped_target": target,
                "reason": "budget_guard",
                "spent_usd": spent,
                "reserved_usd": reserve,
            }
        )
        continue
    warm = request(target, words, 0, "warmup", 1)
    rows.append(warm)
    emit(warm)
    if not warm["success"] and any(
        x in warm.get("error", "").lower()
        for x in ["context length", "maximum context", "max model length"]
    ):
        emit(
            {
                "skipped_target": target,
                "reason": "context_rejected_in_warmup",
                "planned_concurrency": concurrency,
            }
        )
        continue
    with cf.ThreadPoolExecutor(max_workers=concurrency) as pool:
        for wave in range(2):
            fs = [
                pool.submit(
                    request,
                    target,
                    words,
                    1 + wave * concurrency + i,
                    "measured",
                    concurrency,
                )
                for i in range(concurrency)
            ]
            for f in cf.as_completed(fs):
                r = f.result()
                rows.append(r)
                emit(r)
    time.sleep(2)
time.sleep(3)
emit({"usage_logs": usage()})
(OUT / "results.json").write_text(json.dumps(rows, ensure_ascii=False))
emit(
    {
        "complete": True,
        "run_id": RUN,
        "requests": len(rows),
        "successful": sum(r["success"] for r in rows),
    }
)
