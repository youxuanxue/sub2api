---
name: tokenkey-stage0-release-rollout
description: Drive TokenKey Stage0 release, prod deploy, edge rollout, smoke, rollback, and release-risk checks. Use for release tagging, deploy-stage0, Lightsail edge rollout, structured smoke results, or post-release OAuth checks.
---

# TokenKey：Stage0 release → prod/Edge rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| **release 全步骤（决策→bump→push→tag，worktree 隔离）** | 机械 | `bash scripts/release-bump-and-tag.sh [--dry-run]`（bump 时同步 `docs/ops/endpoint-compat-baseline.md` runtime anchor；默认 **direct-push**；仅 `release-main-push-route`=`bump-via-pr` 时 delegate `release-bump-via-pr.sh`；永不写共享 checkout） |
| **发版 bypass 一次性配置（scheme 1）** | 机械 | `bash scripts/release-configure-main-bypass.sh`（个人仓库：`enforce_admins=false`；组织仓库：`bypass_pull_request_allowances.users`） |
| **VERSION bump 经 PR（fallback）** | 机械 | `bash scripts/release-bump-via-pr.sh [--dry-run] [--pr N]`（仅当当前 gh 账号无法 direct-push 时） |
| main bump 路由探测（direct-push / bump-via-pr） | 机械 | `bash scripts/release-main-push-route.sh`（读 protection + 当前 gh 用户 bypass 能力） |
| VERSION/tag 三态决策（tag-only / bump-and-tag / skip-bump-skip-tag） | 机械 | `scripts/release-decide-version.sh [--emit-suggested-bump]`（被上行脚本消费；单独跑仅用于诊断） |
| 打 tag（含 skip-ci / VERSION 一致 / HEAD==origin/main 校验） | 机械 | `scripts/release-tag.sh vX.Y.Z`（被上行脚本调用） |
| 读取 deployable edge 矩阵 | 机械 | `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` |
| **canary Edge 选择（容量合格 → 低流量 → 高内存余量 → 矩阵顺序）** | 机械 | `python3 scripts/stage0/pick_release_canary_edge.py`（SSM 探测全部 deployable Edge；硬门禁为内存/磁盘，按近 30 分钟完成请求数和内存余量排序；native OAuth/Kiro 池仅作 audit/smoke applicability；`--json` 带完整 audit） |
| Edge dispatch 路由（edges 均为 Lightsail） | 机械 | `scripts/stage0/resolve-edge-deploy-route.py --edge-id <id> --json` |
| Edge upgrade/smoke/rollback dispatch | 机械 | `bash scripts/stage0/dispatch-edge-deploy.sh --edge-id … --operation …` |
| **其余 Edge rollout（bounded parallel fail-stop + smoke 标记验收）** | 机械 | `bash scripts/stage0/rollout-edges.sh --tag X.Y.Z --skip <canary>`（**默认 `--parallel 1` 顺序**，降低并发换容器对线上的影响；`N>1` 仅在可接受该影响时用） |
| dispatch release.yml / deploy-stage0.yml + watch | 机械 | `gh workflow run` + `gh run watch --exit-status` |
| **prod pricing registry runtime audit** | 机械 | `deploy-stage0.yml` 在镜像切换 + external health 后只读执行 `ops/pricing/manage-overlay-runtime.py check`；价格发布独立由 registry 合并到 protected main 后触发 `pricing-registry-publish.yml` |
| prod 镜像预热（deploy 前，把 ~150s pull 移出关键路径） | 机械 | `gh workflow run warm-image-stage0.yml` + `approve-github-run-env.sh` + watch（只读、非致命） |
| prod / warm Environment approval | 机械 | `bash scripts/stage0/approve-github-run-env.sh --run-id <id> --comment "…"`（批不批、何时批是判断） |
| prod 完整 smoke（CI 唯一验收源） | 机械 | `deploy-stage0.yml` job log 内 `tk_post_deploy_smoke: OK`（`GATEWAY_SMOKE_SUITE=full`） |
| Edge smoke 分阶段（infra / edge-native-oauth / main-via-edge / full） | 机械 | `ops/stage0/edge_post_deploy_smoke.sh` + workflow `smoke_phase`；**upgrade/rollback 默认 infra**；canary 显式 **full**（infra + 容器内 per-account OAuth 拟真 `probe_account_model`）；`main-via-edge` 为可选 prod 中转链路 |
| 发版前 smoke 模型校验 | 机械 | `python3 scripts/stage0/check_smoke_config.py`（`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 均 ∈ `TK_SMOKE_API_KEY` 的 `/v1/models`）。**完整校验需要 smoke key，只在 CI 可跑**；本地降级为 `bash ops/stage0/load_smoke_github_env.sh --check prod`（只验 secret/vars 已配置） |
| 发版后跟进档位（skip / single） | 机械 | `bash scripts/release-impact-files.sh PREV NEW` → `.followup.tier`（是否值得人工再跟；**实测检查不走这里**） |
| 发版后控制面探活（prod + deployable edge） | 机械 | `bash ops/observability/probe-release-control-plane.sh`（prod `/health` + `/api/v1/settings/public`，deployable Edge `/health`，JSON lines + summary） |
| **发版后两阶段实测（live tag→本次 tag 的全部 PR）** | 机械 | `deploy-stage0.yml` 同一 `deploy` job：蓝绿脚本在 Caddy reload 成功的真实切流点输出 `cutover_at`；`Check PR hooks immediately` 查该时刻起的 PR observables，workflow 只补足到 `cutover_at + 300s`，再由 `Check traffic and 5xx after 5 minutes` 查累计流量/5xx；两阶段复用 `plan.json` 与已批的 prod Environment |
| **发版后 Anthropic OAuth 配置检查（snapshot → check）** | 机械 | `python3 ops/anthropic/manage-anthropic-config.py snapshot` + `check --snapshot`（canonical：`tokenkey-anthropic-oauth-config`） |
| **发版后 Account model_mapping 配置 diff** | 机械 | `python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json`（默认 prod only；edge 空 mapping 不纳入） |
| rollout 摘要（git log / diff stat / sentinel / deletion） | 机械 | `bash scripts/release-rollout-summary.sh --mode release` |
| prod approval 时机、smoke 模型回退 | 判断 | prompt（爆炸半径、用户入口顺序） |
| post-release verdict + Summary | 机械 | `release_post_check.py evaluate --phase immediate|delayed` + `summary` + `gate`（Summary 显式显示缺失/无效证据与 baseline failure；gate 只接受 phase 匹配且 verdict=`green`，agent 禁止另评） |
| `simple_release=true` / `[skip ci]` 等 hard rules | 判断 + 机械门禁 | prompt + `scripts/release-tag.sh` / preflight |

## 调用参数

本 skill 默认按用户语义解析；用户未写完整参数时，先按下面语义补全，仍有歧义再问。

```text
/tokenkey-stage0-release-rollout target=<prod|edge-<edge_id>|all> [tag=X.Y.Z] [operation=<check|release|deploy|smoke|rollback>] [previous_tag=X.Y.Z] [anthropic_config_check=false] [account_model_mapping_check=false] [main_via_edge=false]
```

| 参数 | 语义 |
|---|---|
| `operation=check` | 只做预发布风险检查：对比上一个 release tag 到待发布 HEAD 的代码事实，判断上线 prod/Edge 的潜在影响；不 bump、不 tag、不 dispatch deploy。 |
| `target=prod` | release（必要时 bump/tag/build）→ `deploy-stage0.yml -f tag=…`（绑定 **`prod`** Environment）→ prod smoke → **默认** Anthropic OAuth snapshot/check + Account model_mapping check。 |
| `target=edge-<edge_id>` | 默认 tag 已存在：用 **`bash scripts/stage0/dispatch-edge-deploy.sh`**（edges 均为 Lightsail，路由到 `deploy-edge-lightsail-stage0.yml`）→ watch → 按 phase 验收 smoke。`operation=smoke` 只 smoke；`operation=rollback` 用 `previous_tag`。不要手选 workflow 或手填 confirm_instance。 |
| `target=all` | release 一次 → canary **upgrade (full)** → prod deploy（CI smoke）→ **默认跳过** canary `main-via-edge` → 其余 Edge **infra rollout** → followup → **默认** Anthropic OAuth snapshot/check + Account model_mapping check。`main_via_edge=true` 才跑可选段。 |
| `main_via_edge` | 默认 **false**。`target=all` 时不跑 prod→Edge 中转 smoke；缺 key 或 by-design 503 不得据此 rollback。 |
| `anthropic_config_check` | 默认 **true**（`operation=release` 且 smoke 验收通过后）。跑 `/tokenkey-anthropic-oauth-config` 的 **Stage 1–2 only**（snapshot + check，只读）。`anthropic_config_check=false` 跳过。`operation=check/smoke/rollback` 默认不跑。 |
| `account_model_mapping_check` | 默认 **true**（`operation=release` 且 smoke 验收通过后）。跑 `manage-account-model-mapping-runtime.py check-accounts --json`（默认 prod only），只读 diff prod 显式 `model_mapping` 与 Go SSOT floor/policy metadata。violation 或 SSM/OIDC 失败记 **yellow**，不 rollback 镜像。`account_model_mapping_check=false` 跳过。edge 空 mapping 不纳入 post-release 检查；需显式 `--include-edges` 才查 edge。 |

如果用户只说“发版 / deploy 最新 / ship production”，默认 `target=prod operation=release`。如果用户说“全部 / 所有网关 / prod + edge / all”，默认 `target=all operation=release`。如果用户说“检查 / 预判 / 评估上线影响 / release check”，默认 `operation=check target=all`。

## 执行顺序（细节在脚本，不在本 skill 复述）

1. `operation=check`：只读对比 prev release tag → HEAD（`git log/diff` + deploy 契约文件），不 bump/tag/deploy。
2. `operation=release` + `target=prod|all`：`bash scripts/release-bump-and-tag.sh`（worktree 隔离）→
   watch `release.yml` → warm（可选）→ `deploy-stage0.yml` → CI `tk_post_deploy_smoke: OK`。
3. `target=all`：canary `pick_release_canary_edge.py` + `dispatch-edge-deploy.sh`（full）→ prod →
   `rollout-edges.sh`（默认 `--parallel 1`，infra）→ post-release checks。
4. 单 edge：`dispatch-edge-deploy.sh --edge-id …`（不要手选 workflow）。
5. 发版后默认只读：Anthropic OAuth snapshot/check；`manage-account-model-mapping-runtime.py check-accounts --json`
   （prod only；violation = yellow，不 rollback 镜像）。
6. 两阶段实测：`ops/observability/run-post-release-check.sh` + `scripts/release_post_check.py`
   （`--phase immediate` / `--phase delayed`；禁止模型自造 verdict）。
   Workflow 步骤名与 Summary 标题必须对齐：`Check PR hooks immediately`、
   `Check traffic and 5xx after 5 minutes`、`### Traffic / 5xx (+5 min)`（含
   completed requests / top paths）。

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

| 现象 | 处理 |
|------|------|
| `release.yml` 在 **Verify endpoint-compat baseline** 失败（baseline 仍锚定旧 tag） | 2026-07-08 v1.8.91 实录：`bump` 只改 VERSION 未同步 baseline → release 红。现已由 `release-bump-and-tag.sh` / `release-bump-via-pr.sh` 在 bump commit 内机械同步；若仍失败，手动 `python3 scripts/sync_endpoint_compat_baseline_anchor.py --version X.Y.Z --previous-deploy-tag vA.B.C` 后删 tag 重建。 |
| `release-bump-and-tag.sh` 无输出且 exit 1（action=tag-only） | 已修：`field()` grep 无匹配 + `set -e` 静默退出。升级后重跑；临时绕过 = worktree @ origin/main + `release-tag.sh vX.Y.Z`。 |
| `push origin HEAD:main` / GH006 **Protected branch** | 先 `bash scripts/release-configure-main-bypass.sh`；仍失败则 fallback `release-bump-via-pr.sh`。 |
| bump PR CI 仅 **preflight** flaky fail | `gh run rerun <run_id> --failed`，再 `release-bump-via-pr.sh --pr <N>`；不要改 VERSION 对冲。 |
| 发版后残留 `sub2api-release-*` / `sub2api-bump-pr-*` worktree | `git worktree list` → `git worktree remove --force <path>`；否则后续 `worktree add` / `gh pr merge --delete-branch` 会失败。 |
| release 时主 checkout 在别的分支 / 有别人的 WIP | 正常现象（并行 agent），不要去切分支、stash 或还原别人的文件；release 脚本本来就不读写当前 checkout。 |
| `release-bump-and-tag.sh` push 被拒（origin/main moved，非 protected） | 期间有新 PR 合入；直接重跑脚本，它会基于新的 origin/main 重建 worktree。 |
| `release-tag.sh` 报 HEAD 含 skip-ci 标记 | 修改触发打 tag 的最近一次提交说明后重试，或按 `CLAUDE.md` 用 `gh workflow dispatch` 触发 `release.yml`。 |
| `tag already exists on origin` | 升 `VERSION` 再打新 tag，或仅 dispatch deploy 已有 tag。 |
| deploy 报单架构 manifest | 重新跑 `release.yml` 且 `simple_release=false`；prod / Edge 都不要 override。 |
| 误 dispatch 了一个多余 prod deploy run | release 不再自动 queue，多出来的一定是手动重复 dispatch；取消多余 run、watch 留下的那个即可。 |
| Edge `confirm_stack` mismatch | 停止；检查 Lightsail `edge-targets-lightsail.json` / `resolve-edge-deploy-route.py`，不要手改 confirm。 |
| Edge smoke 403 | public runner 访问 `/v1/models` 403 是预期；主网关来源 403 才查 `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 与 prod EIP。 |
| main-via-edge smoke HTTP 503 `"no available accounts"` | 先在 prod 上确认对应账号（如 `cc-<edge_id>-oauth`）是否被设为可调度；这是 prod 路由策略，与本次镜像无关。若设计上就不可调度，把这条 smoke 从 hard-fail 降为"infra OK / business-link by design"，**不要 rollback**。若运维想恢复该链路，请按 `/tokenkey-anthropic-oauth-config` 调可调度位再 `dispatch-edge-deploy.sh --operation smoke --smoke-phase main-via-edge` 复验。 |
| canary / 其余 Edge 的 native OAuth/Kiro 池为空 | 全部池确定为空时，selector 回落首个 deployable Edge；full smoke 的 infra 仍须通过，edge-native-oauth 输出 `SKIPPED no eligible accounts` 并记 N/A，不阻断部署。其余 Edge 仍走 `rollout-edges.sh` 的 infra-only。若账号存在但探针鉴权失败，或池计数探测失败且没有正数候选，仍应失败并停 rollout。 |
| `gh run watch` 被工具超时打断 | 用同一 run id 再执行 `gh run watch <id> --exit-status` 接到终态（`rollout-edges.sh` 已内置重连）。 |
| 发版后 tick 报 `No such container: tokenkey` | 先确认在用新版 `ops/observability/probe-post-release-tick.sh`；它默认 `CONTAINER=auto` 会解析 prod blue/green active container。不要手工猜 `tokenkey-green`；若仍失败，看 tick stdout 的 `container_resolution`。 |
| `TK_SMOKE_GITHUB_ENV=prod` 报 `unexpected gh variables response` | 旧版 `load_smoke_github_env.py` 对单页 gh api 响应断言成 list 的 bug，已修；若复现先 `gh api repos/{owner}/{repo}/environments/prod/variables` 看原始形状。 |
| prod `Deploy via SSM Run-Command` 报 `AccessDenied(ssm:SendCommand)` | 先核对 `tokenkey-cicd-oidc` 的 `TargetInstanceId` 是否等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致先更新 OIDC 栈参数再重跑 deploy。 |
| prod smoke Gemini tools **429** + soft-skip + **`tk_post_deploy_smoke: OK`** | 运行时资源/cooldown，**不是** passthrough 路由回归；verdict green/yellow，不 rollback。若要 200 证据，cooldown 后重跑 deploy-stage0 smoke。 |
| prod smoke Gemini tools **400** + Codex 账号文案 | 路由回归（#1168 类）；**red**，rollback `previous_tag` 并停 edge rollout。 |
| prod smoke 报 configured smoke model not listed in GET /v1/models | 不是代码回归，改 **`prod`** Environment 对应的 **`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS`** 为 `TK_SMOKE_API_KEY` 可见模型后重跑。 |
| `gh` 请求持续报 `read ... 127.0.0.1:7890: connection reset by peer` | 先用 `env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh <cmd>` 做无代理重试；恢复后再继续 watch/dispatch。 |
| 无代理后 dispatch 报 `HTTP 403 Must have admin rights to Repository` | `gh` 可能切到另一个账号；先 `env -u GH_TOKEN ... gh auth status`，必要时 `gh auth switch -u <repo-owner>` 后重试 dispatch。 |
| 发版后 Anthropic `check` 报 violation（tier/TLS/stub pool/balance） | **不要** rollback 镜像；按 `/tokenkey-anthropic-oauth-config` 从 `$JOBDIR/post-release-check.json` 派生 plan → apply → verify。TLS/UA 漂移优先 `remediate-guard-drift --sync-runtime`。 |
| 发版后 Anthropic `snapshot` SSM 失败 | 记 yellow；prod/Edge 镜像仍有效。补 OIDC/实例在线后重跑 snapshot+check，或 `snapshot --skip-prod` 仅 edge。 |
| 发版后 Account model_mapping `check-accounts` 报 violation | **不要** rollback 镜像；审 `$JOBDIR/post-release-account-model-mapping-check.json` 的账号/group diff。期望与 forbidden policy 均来自 Go SSOT；确认要覆盖 live 配置时走 `/tokenkey-modelops-planner` 的 runtime/account mapping 路由：必要时 `validate/check/sync-runtime` 更新 desired layer，然后 `apply-accounts --confirm yes-apply-account-model-mapping`。 |
| 发版后 Account model_mapping `check-accounts` SSM 失败 | 记 yellow；prod/Edge 镜像仍有效。补 OIDC/实例在线后重跑 `python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json`；仅排障 edge 时加 `--include-edges` 或 `--skip-prod`。 |

## 扩展阅读

- `.cursor/skills/tokenkey-anthropic-oauth-config/SKILL.md` — 发版后 check violation 的 plan/apply/verify canonical 路径。

- `scripts/release-bump-and-tag.sh` — release 全步骤（worktree；bump 时同步 endpoint-compat baseline；默认 direct-push，fallback 才 delegate PR）。
- `scripts/release-bump-via-pr.sh` — VERSION bump 经 PR + merge + tag。
- `scripts/release-configure-main-bypass.sh` — scheme 1：发版账号 bypass（个人 repo / 组织 repo 双路径）。
- `scripts/release-main-push-route.sh` — direct-push vs bump-via-pr 探测。
- `scripts/stage0/approve-github-run-env.sh` — Environment 门禁自批（warm / prod / edge）。
- `scripts/release-decide-version.sh` — VERSION/tag 三态决策。
- `scripts/release-tag.sh` — tag 门禁。
- `.github/workflows/release.yml` — multi-arch image build/publish；prod 由 skill 显式 dispatch
  `.github/workflows/deploy-stage0.yml`。
- `scripts/stage0/rollout-edges.sh` — 其余 Edge bounded-parallel rollout（fail-stop + smoke 标记验收；**默认 `--parallel 1` 顺序**，降低并发换容器对线上的影响；`N>1` 仅在可接受时用）。
- `scripts/stage0/pick_release_canary_edge.py` — 探测全 fleet 后按容量、近 30 分钟流量、内存余量和矩阵顺序选择 canary。
- `ops/stage0/edge_release_canary_probe.sh` — canary 选择的单行 JSON 资源/流量探针；OAuth/Kiro 账号数为 audit-only。
- `ops/stage0/edge_oauth_pool_probe.sh` — release probe 复用的账号池计数 owner（与 edge-native smoke 同 eligibility）。
- `scripts/stage0/dispatch-edge-deploy.sh` — 单一 Edge deploy dispatch（edges 均为 Lightsail）。
- `ops/observability/run-post-release-check.sh` — 两阶段实测入口（`--phase immediate|delayed` + 同一 `--since` / plan）。
- `scripts/release_post_check.py` — 从 live tag→new tag 派生 PR 检查、分阶段评分、等待 cutover 窗口、渲染 Summary 并 fail-closed gate；禁止模型自造 hook。
- `ops/observability/probe-release-control-plane.sh` — 发版后控制面探活（prod + deployable Edge，JSON lines + summary）。
- `ops/observability/probe-post-release-tick.sh` — tick 探针（由 wrapper 投递；hooks 来自 plan，不是 prompt）。
- `scripts/stage0/resolve-edge-deploy-route.py` — Edge → workflow + confirm 参数。
- `.github/workflows/deploy-stage0.yml` — prod deploy。
- `.github/workflows/deploy-edge-lightsail-stage0.yml` — Lightsail Edge deploy（edges 唯一路径）。
- `ops/stage0/post_deploy_smoke.sh` — prod 完整 smoke（CI canonical）。
- `ops/stage0/edge_post_deploy_smoke.sh` — Edge smoke（infra / edge-native-oauth / main-via-edge / full）。
- `deploy/aws/README.md` — Stage0、Edge、多区域升级 SOP。
- `.github/workflows/ops-stage0-pg-dump-refresh.yml` + `ops/stage0/pg_dump_refresh_via_ssm.sh` — in-place 同步 `deploy/aws/cloudformation/stage0-single-ec2.yaml` 里的 `tokenkey-pgdump.*` systemd unit 到 live 实例（不重建 EC2）；下次有类似 user-data 模板改动可参考此形状写一个 one-shot ops workflow。
- `.github/workflows/ops-stage0-host-mem-guard.yml` + `ops/stage0/sync-host-mem-guard-via-ssm.sh` — 同形状的 one-shot：把 #811 的 `/swapfile` 释放阀 + sysctl + `tokenkey-disk-metrics.sh` 内存压力告警从 `stage0-ec2-bootstrap.sh` 运行时抽取（单一源）推到 live prod（不重建 EC2，prod-only）。**发版本身不会落地这批 infra 改动**（deploy 只换镜像、不跑 bootstrap）——改了 bootstrap 的 swap/内存防御后，要么等下次换机，要么 dispatch 此 workflow 立刻生效。
