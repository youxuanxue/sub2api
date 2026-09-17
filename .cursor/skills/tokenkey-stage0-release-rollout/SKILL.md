---
name: tokenkey-stage0-release-rollout
description: Drive TokenKey Stage0 release, prod deploy, edge rollout, smoke, rollback, and release-risk checks. Use for release tagging, deploy-stage0, Lightsail edge rollout, structured smoke results, or post-release OAuth checks.
---

# TokenKey：Stage0 release → prod/Edge rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 确定性基线（机械化 vs 真判断）

release、目标解析、canary 选择、rollout、smoke/post-check verdict 均调用现有脚本；模型只判断发布影响和审批时机。具体入口见执行顺序；仅查询其它工具或维护流水线时展开工具表。 见 [操作细则](references/tools.md)。

## 调用参数

“发版/deploy 最新”默认 `target=prod operation=release`；“全部/prod+edge”用 `target=all`；检查影响用只读 `operation=check`，不 bump/tag/deploy。单 edge 默认复用已有 tag。回放用 `operation=replay target=prod`，只准备 inactive color，任何结果都停在 `approval_pending=true`；禁止自动 promote/切流/edge rollout。执行 replay/rollback、修改可选检查或需要完整参数时必须先读细则。release 后默认只读 Anthropic snapshot/check 与 prod model_mapping check；配置漂移/SSM 失败记 yellow，不据此 rollback 镜像。 见 [操作细则](references/operations.md)。

## 执行顺序（细节在脚本，不在本 skill 复述）

1. `operation=check`：只读对比 prev release tag → HEAD（`git log/diff` + deploy 契约文件），不 bump/tag/deploy。
2. `operation=release` + `target=prod|all`：`bash scripts/release-bump-and-tag.sh`（worktree 隔离）→
   watch `release.yml` → warm（可选）→ `deploy-stage0.yml` → CI `tk_post_deploy_smoke: OK`。
3. `target=all`：canary `pick_release_canary_edge.py` + `dispatch-edge-deploy.sh`（full）→ prod →
   `rollout-edges.sh`（默认 `--parallel 1`，infra）→ post-release checks。
4. 单 edge：`dispatch-edge-deploy.sh --edge-id …`（不要手选 workflow）。
5. 两阶段实测：`ops/observability/run-post-release-check.sh` + `scripts/release_post_check.py`
   （`--phase immediate` / `--phase delayed`；禁止模型自造 verdict）。
   Workflow 步骤名与 Summary 标题必须对齐：`Check PR hooks immediately`、
   `Check traffic and 5xx after 5 minutes`、`### Traffic / 5xx (+5 min)`（含
   completed requests / top paths）。
6. Prod smoke 后 advisory：`check-account-group-bindings.sh` 只读检查每个健康、可调度且有显式 `model_mapping` 的账号；无 active group 或与同模型 peer 分组完全不相交时输出 `review`，无 peer 的新模型只记 inconclusive。workflow 必须保持 `continue-on-error`。

Hard rules：`simple_release` 默认 false；bump/tag 提交不得带 skip-ci 字面标记（见 `CLAUDE.md` §9）。

## Jobs / OPC 默认部署顺序

`all` 不是并行全量推送。默认采用顺序化 canary rollout：

1. **release build 一次**：只构建一个 multi-arch GHCR tag，所有目标复用同一 image，避免两套产物。
2. **Edge canary：容量合格后选择近 30 分钟流量最低、内存余量最高的 deployable Edge upgrade + full smoke（显式 `--smoke-phase full`）**：用 `python3 scripts/stage0/pick_release_canary_edge.py` 探测全 fleet 后选 canary；native OAuth/Kiro 账号数只决定该 smoke 子段是否适用，不参与 eligibility 或排序。**其余 Edge 一律 infra only**（`rollout-edges.sh`）。
3. **prod 主网关 upgrade + 完整 prod smoke**：Edge canary 过后再升级 prod。
4. **（可选）main gateway via Edge smoke**：仅当需要验证 prod→Edge 中转调度时，`smoke_phase=main-via-edge`；缺 `TK_SMOKE_API_KEY` 记 partial，不 rollback。
5. **其余 deployable Edge bounded-parallel rollout**：prod full smoke 绿后，`rollout-edges.sh` 对每个 edge dispatch upgrade（**infra only**，验 log 含 `tk_edge_post_deploy_smoke: OK phase=infra`）。

例外：

- `target=prod`：只发版/部署 prod，不自动部署 Edge。
- `target=edge-<edge_id>`：只升级/烟测对应 Edge，不发新 release，除非用户显式要求先 release。
- 用户强指定“prod 先”时照做，但在摘要中标出与默认 canary 顺序的差异。

## 故障速查

发布/烟测失败时按报错查询；fail-stop，恢复后仍由 CI/脚本裁决，不自行编造 green。 见 [操作细则](references/troubleshooting.md)。

普通 gateway/all rollout 不自动 dispatch QA；修改 bootstrap 或独立 QA 部署时，读 [工具表的镜像发布之外入口](references/tools.md#镜像发布之外的入口)。发版不自动执行 host bootstrap。
