#!/usr/bin/env python3
"""Run real Claude Code context recovery against the gateway's loopback fixture.

Uses synthetic history, an isolated config, no tools and no real API credentials.
Requires the specified Claude binary; it never downloads or updates the client.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import urllib.request
import uuid


def request(addr, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(addr, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as response:
        body = response.read()
    return json.loads(body) if body else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--claude", required=True, type=Path)
    args = parser.parse_args()
    cli = args.claude.resolve(strict=True)
    version = subprocess.check_output([str(cli), "--version"], text=True).strip()
    if version != "2.1.283 (Claude Code)":
        raise RuntimeError(f"Expected the incident client 2.1.283, got {version}")
    repo = Path(__file__).resolve().parents[2]
    with tempfile.TemporaryDirectory(prefix="claude-context-acceptance-") as tmp:
        work = Path(tmp)
        config = work / "config"
        config.mkdir()
        (config / ".claude.json").write_text(json.dumps({
            "hasCompletedOnboarding": True, "theme": "dark", "autoUpdates": False,
        }))
        addr_file = work / "fixture.addr"
        fixture_env = {**os.environ, "TK_CLAUDE_CONTEXT_FIXTURE_ADDR_FILE": str(addr_file)}
        with (work / "fixture.log").open("w+") as fixture_log:
            fixture = subprocess.Popen([
                "go", "test", "-tags=unit", "./internal/service",
                "-run", "^TestClaudeContextCLIFixture$", "-count=1", "-v",
            ], cwd=repo / "backend", env=fixture_env, stdout=fixture_log, stderr=subprocess.STDOUT)
            addr = None
            try:
                deadline = time.monotonic() + 120
                while not addr_file.exists():
                    if fixture.poll() is not None or time.monotonic() >= deadline:
                        fixture_log.seek(0)
                        raise RuntimeError("Fixture did not start: " + fixture_log.read()[-2000:])
                    time.sleep(0.2)
                addr = addr_file.read_text()
                # Allowlist host environment, so provider/agent credentials and
                # inherited model overrides cannot escape the synthetic fixture.
                env = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR", "LANG") if k in os.environ}
                env.update(ANTHROPIC_BASE_URL=addr, ANTHROPIC_API_KEY="synthetic-key",
                           CLAUDE_CONFIG_DIR=str(config), CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1",
                           DISABLE_AUTOUPDATER="1", TERM="xterm-256color")
                base = [str(cli), "--bare", "--tools", "", "--allowedTools", "",
                        "--setting-sources", "", "--strict-mcp-config", "--model", "claude-sonnet-4-5",
                        "--autocompact", "auto", "--system-prompt",
                        "You answer questions about a fictional project. Do not use tools.",
                        "-p", "--max-budget-usd", "2"]

                def run(prompt, session, resume):
                    streaming = isinstance(prompt, list)
                    formats = (["--input-format", "stream-json", "--output-format", "stream-json", "--verbose"]
                               if streaming else ["--output-format", "json"])
                    cmd = base + formats + (["--resume", session] if resume else ["--session-id", session])
                    if streaming:
                        prompt = "".join(json.dumps({"type": "user", "message": {"role": "user", "content": text}})
                                         + "\n" for text in prompt)
                    shell = shlex.join(cmd) + " 2>&1 | tee " + shlex.quote(str(work / "cli.log"))
                    result = subprocess.run(["bash", "-o", "pipefail", "-c", shell],
                                            input=prompt, text=True, capture_output=True,
                                            cwd=work, env=env, timeout=120)
                    results = []
                    for line in result.stdout.splitlines():
                        try:
                            item = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        if isinstance(item, dict) and item.get("type") == "result":
                            results.append(item)
                    if not results:
                        raise RuntimeError("No CLI result: " + result.stdout[-2000:])
                    value = results[-1]
                    if result.returncode or any(r.get("is_error") for r in results):
                        raise RuntimeError("CLI failed: " + str(value.get("result")))
                    return value

                for scenario, rejects, delay in (("automatic", 1, False),
                                                  ("after_heartbeat", 2, True),
                                                  ("manual_compact", 1, False)):
                    request(addr + "/fixture", {"reject": 0, "delay": False, "input_tokens": 100})
                    session = str(uuid.uuid4())
                    for turn in range(4):
                        run(f"Fictional detail {turn}. " + "Blue squares are houses and green circles are trees. " * 150,
                            session, turn > 0)
                    original = request(addr + "/fixture")[-1]
                    request(addr + "/fixture", {"reject": rejects, "delay": delay, "input_tokens": 100})
                    prompt = "Continue the fictional project."
                    # Keep compact and continuation in the same real client process:
                    # a fresh --resume can otherwise reload pre-compact history.
                    value = run(["/compact", prompt] if scenario == "manual_compact" else prompt, session, True)
                    events = request(addr + "/fixture")
                    if sum(e["reject"] for e in events) != rejects or not any(e["summary"] and not e["reject"] for e in events):
                        raise RuntimeError(f"{scenario}: no successful recovery summary: {events}")
                    if "SYNTHETIC_OK" not in value.get("result", "") or events[-1]["summary"] or events[-1]["reject"]:
                        raise RuntimeError(f"{scenario}: no usable continuation after summary: {events}")
                    if (events[-1]["bytes"] >= original["bytes"]
                            or events[-1]["messages"] >= original["messages"]):
                        raise RuntimeError(f"{scenario}: continuation did not shrink: {events}")
                    print(json.dumps({"scenario": scenario, "client": version, "result": "PASS",
                                      "requests": events}, ensure_ascii=False), flush=True)
            finally:
                if addr is not None and fixture.poll() is None:
                    request(addr + "/stop", {})
                try:
                    code = fixture.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    fixture.terminate()
                    fixture.wait(timeout=10)
                    raise RuntimeError("Fixture failed to stop") from None
                if code:
                    fixture_log.seek(0)
                    raise RuntimeError("Fixture failed: " + fixture_log.read()[-2000:])


if __name__ == "__main__":
    main()
