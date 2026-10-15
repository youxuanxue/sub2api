"""Complete the budget-skipped 512K tier using the verified billing multiplier."""

import ast
import json
import os
from pathlib import Path

os.umask(0o077)
base = Path("/root/tokenkey-benchmark")
key = json.loads((base / "key.json").read_text())
assert key["user_id"] == 1 and key["group_id"] == 2 and key["id"] == 3
os.environ["TK_PERF_KEY"] = key["key"]
os.environ["TK_PERF_KEY_ID"] = str(key["id"])
entries = [
    json.loads(line)
    for line in (base / "run-output.jsonl").read_text().splitlines()
    if line.startswith("{")
]
assert entries[-1].get("complete") is True
assert not any(e.get("target_tokens") == 524288 for e in entries)
assert any(
    e.get("skipped_target") == 524288 and e.get("reason") == "budget_guard"
    for e in entries
)
meta = entries[0]
tree = ast.parse((base / "kimi-perf.py").read_text())
body = []
for node in tree.body:
    if (
        isinstance(node, ast.Expr)
        and isinstance(node.value, ast.Call)
        and isinstance(node.value.func, ast.Name)
        and node.value.func.id == "emit"
    ):
        break
    body.append(node)
ns = {}
exec(compile(ast.Module(body=body, type_ignores=[]), "kimi-perf.py", "exec"), ns)
ns["RUN"] = meta["run_id"]
ns["OUT"] = Path(meta["output_dir"])
previous = ns["usage"]()
assert previous and all(
    abs(u["actual_cost"] - u["total_cost"] * 0.3) < 1e-7 for u in previous
)
spent = sum(u["actual_cost"] for u in previous)
reserve = (524288 * 3.3 / 1e6 + 2048 * 16.5 / 1e6) * 3 * 0.3
assert spent + reserve < key["quota"] == 4.5
ns["emit"](
    {
        "supplemental_target": 524288,
        "reason": "verified_actual_billing_multiplier_0.3",
        "spent_usd": spent,
        "reserved_usd": reserve,
        "supersedes_skip": "budget_guard",
    }
)
calibration = next(e for e in entries if e.get("phase") == "calibration")
words = 524288 - (calibration["usage"]["prompt_tokens"] - 3000)
for seq, phase in [(0, "warmup"), (1, "measured"), (2, "measured")]:
    ns["emit"](ns["request"](524288, words, seq, phase, 1))
ns["time"].sleep(3)
ns["emit"]({"supplemental_complete": True, "target": 524288})
