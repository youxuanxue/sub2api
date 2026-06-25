---
name: tokenkey-modelops-planner
description: >-
  TokenKey read-only modelops reconcile planner. Use when comparing upstream discovery,
  probe TSV, pricing, tk_served_models manifest, live model_mapping snapshots, or Qwen
  60→72 mirror drift — before onboard or catalog refresh, or when priced-but-empty-pool
  429/503 needs a structured diff.
---

# TokenKey：modelops 对账（只读 planner）

**运营入口 = 本 skill。** 脚本 `ops/pricing/modelops.py` 是机械化实现；operator/agent
按本 skill 走，不要从 README 或姊妹 skill 里复制命令块。

## 一张图：model ops 三 skill，各管一件事

```text
  对账（只读）                    写入（各走各的）
  ─────────────                  ─────────────────────────────
  tokenkey-modelops-planner  →   tokenkey-onboard-model
     plan / snapshot-sql            newapi 长尾 manifest+migration+价
                             →   tokenkey-servable-model-refresh
                                   四原生平台公开目录/Menu allowlist
```

| 你在问什么 | 用哪个 skill |
| --- | --- |
| discovery / probe / 价 / manifest / prod mapping / mirror 哪里不一致？ | **本 skill** |
| 客户要上新模型，要 served + priced | `tokenkey-onboard-model`（先本 skill plan 可选） |
| 公开 `/pricing` 或 Menu 过时，要实测刷新 Go allowlist | `tokenkey-servable-model-refresh` |
| 单账号单模型能不能 200（路由隔离） | `tokenkey-account-model-probe` |

边界（硬）：本 skill **不写** `model_mapping`、不写 pricing、不跑 apply。plan 里打印的
`--dry-run` / probe 命令需人审后再执行，或交给写入 skill。

设计基线：`docs/approved/served-model-reconcile-planner.md` · 脚本细节：`ops/pricing/README.md`

## 确定性基线

- **机械化（脚本）**：upstream 形状归一、`probe-servable-models.sh` TSV 聚合、overlay/manifest
  价态、live snapshot 解析、mirror diff、probe 族 env 选择、guarded dry-run 命令生成——全在
  `modelops.py`；`--selftest` 由 preflight 门禁。
- **真判断（人/agent）**：plan 块是否值得 act、`inconclusive` vs `mapping_gap`、mirror 是否
  现在同步、缺价是否先 hotfix 再 mapping；**合并/apply-live 永远等人授权**。

## 策展账号（planner 内置 guard 元数据）

| id | 名称 | channel_type | 典型 mirror |
| --- | --- | --- | --- |
| 60 | Qwen | 17 | 源 → 72 |
| 72 | Qwen-2 | 17 | 目标，应 ≡ 60 |
| 39 | ds-官 | 43 | — |
| 67 | GLM | 26 | — |
| 7 | volcengine | 45 | — |

## 流程

### 0) 本地自检（无需 prod）

```bash
python3 ops/pricing/modelops.py --selftest
```

### 1) 拉 prod 只读 mapping 快照

```bash
python3 ops/pricing/modelops.py snapshot-sql --accounts 60,72
# 经既有 prod DB 只读通道执行 SQL，JSON 存本地，例如 /tmp/model_mapping_snapshot.json
```

### 2) 准备 upstream + probe（需 AWS / SSM 时走 run-probe）

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/pricing/probe-servable-models.sh \
  --env "DASHSCOPE_CHAT_MODELS=qwen3-8b qwen3-14b" --timeout-seconds 300 \
  | tee /tmp/qwen_probe.tsv
```

upstream 文件支持 JSON 数组、`{models:[...]}`、OpenAI `{data:[...]}`、或
`{"model_id":"priced"|"missing"}` map。

### 3) 生成 plan（核心）

```bash
python3 ops/pricing/modelops.py plan \
  --upstream 60:/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/model_mapping_snapshot.json \
  --mirror 60:72 \
  --format text
```

可加 `--candidate 60:新模型` 做 ad hoc 对账；`--strict-manifest` 把 live 多出的 key 标为
待审。

兼容旧 runbook：`python3 ops/pricing/reconcile-served-models.py plan ...`（转发同一实现）。

### 4) 读 plan → 转交写入 skill

| plan 块 | 含义 | 下一步 |
| --- | --- | --- |
| `probe_needed` / `probe_commands` | 还没真 200 | 复制 `run-probe.sh` 行 |
| `price_missing` | 无价 | `tokenkey-onboard-model` 或 `apply-pricing-hotfix.py lookup` |
| `mapping_gap_candidates` | 429 not_allowlisted | 人审 → `apply-model-mapping-live.py --dry-run` |
| `mapping_missing` | manifest 有、prod 无 | 同上，确认后进 onboard skill |
| `mirror_drift` / `mirror_sync_commands` | 72 ≠ 60 | guarded dry-run，人审后 apply |
| `ready_for_onboard` | 200 + 有价、manifest 无 | **`tokenkey-onboard-model`** |
| `surfaces` | 各面 owner | **勿**手维护第二份 menu 列表 |

公开 catalog/menu 漂移的**刷新**仍只走 `tokenkey-servable-model-refresh`；本 skill 只帮
你看清 drift，不替代 refresh。

## 四事实（planner 只对齐，不拥有）

| 事实 | Owner |
| --- | --- |
| Runtime serving | `accounts.credentials.model_mapping` |
| Price | overlay + channel_model_pricing + litellm |
| Public catalog + Menu | `pricing_catalog_supported_models_tk.go` |
| Curated newapi intent | `tk_served_models.json` |
