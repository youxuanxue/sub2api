## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| **release 全步骤（决策→bump→push→tag，worktree 隔离）** | 机械 | `bash scripts/release-bump-and-tag.sh [--dry-run]`（默认 **direct-push**；仅 `release-main-push-route`=`bump-via-pr` 时 delegate `release-bump-via-pr.sh`；永不写共享 checkout） |
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
| **prod pricing registry runtime audit** | 机械 | `ops-daily-diagnostics.yml` 经 `ops/observability/prod-config-audit.sh` 只读执行 `ops/pricing/manage-overlay-runtime.py check`；价格发布独立由 registry 合并到 protected main 后触发 `pricing-registry-publish.yml` |
| prod 镜像预热（deploy 前，把 ~150s pull 移出关键路径） | 机械 | `gh workflow run warm-image-stage0.yml` + `approve-github-run-env.sh` + watch（只读、非致命） |
| prod / warm Environment approval | 机械 | `bash scripts/stage0/approve-github-run-env.sh --run-id <id> --comment "…"`（批不批、何时批是判断） |
| prod 完整 smoke（CI 唯一验收源） | 机械 | `deploy-stage0.yml` job log 内 `tk_post_deploy_smoke: OK`（`GATEWAY_SMOKE_SUITE=full`） |
| Edge smoke 分阶段（infra / edge-native-oauth / main-via-edge / full） | 机械 | `ops/stage0/edge_post_deploy_smoke.sh` + workflow `smoke_phase`；**upgrade/rollback 默认 infra**；canary 显式 **full**（infra + 容器内 per-account OAuth 拟真 `probe_account_model`）；`main-via-edge` 为可选 prod 中转链路 |
| 发版前 smoke 模型校验 | 机械 | `python3 scripts/stage0/check_smoke_config.py`（`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 均 ∈ `TK_SMOKE_API_KEY` 的 `/v1/models`）。**完整校验需要 smoke key，只在 CI 可跑**；本地降级为 `bash ops/stage0/load_smoke_github_env.sh --check prod`（只验 secret/vars 已配置） |
| 发版后跟进档位（skip / single） | 机械 | `bash scripts/release-impact-files.sh PREV NEW` → `.followup.tier`（是否值得人工再跟；**实测检查不走这里**） |
| 发版后控制面探活（prod + deployable edge） | 机械 | `bash ops/observability/probe-release-control-plane.sh`（prod `/health` + `/api/v1/settings/public`，deployable Edge `/health`，JSON lines + summary） |
| **prod replay（只准备 inactive color，不切流）** | 机械 | `scripts/stage0/replay-prod-release.py --tag X.Y.Z` → prod SSM 正常蓝绿 `prepare` → `ops/stage0/gateway_capability_host.py` 用现有测试 universal key 直连候选，串行执行完整短合成请求；核对响应、测试 usage 和线上路由 → 脱敏 receipt/results（不读取历史 capture） |
| **发版后两阶段实测（live tag→本次 tag 的全部 PR）** | 机械 | `deploy-stage0.yml` 同一 `deploy` job：蓝绿脚本在 Caddy reload 成功的真实切流点输出 `cutover_at`；`Check PR hooks immediately` 查该时刻起的 PR observables，workflow 只补足到 `cutover_at + 300s`，再由 `Check traffic and 5xx after 5 minutes` 查累计流量/5xx；两阶段复用 `plan.json` 与已批的 prod Environment |
| **发版后 Anthropic OAuth 配置检查（snapshot → check）** | 机械 | `python3 ops/anthropic/manage-anthropic-config.py snapshot` + `check --snapshot`（canonical：`tokenkey-anthropic-oauth-config`） |
| **发版后健康账号分组合理性检查（只读 advisory）** | 机械 | `bash ops/observability/check-account-group-bindings.sh --target prod`；从健康、可调度账号的显式 `model_mapping` 与 peer 分组证据派生，不硬编码模型→分组表；`review` / 探针失败均不阻塞 rollout |
| rollout 摘要（git log / diff stat / sentinel / deletion） | 机械 | `bash scripts/release-rollout-summary.sh --mode release` |
| prod approval 时机、smoke 模型回退 | 判断 | prompt（爆炸半径、用户入口顺序） |
| post-release verdict + Summary | 机械 | `release_post_check.py evaluate --phase immediate|delayed` + `summary` + `gate`（Summary 显式显示缺失/无效证据与 baseline failure；gate 只接受 phase 匹配且 verdict=`green`，agent 禁止另评） |
| `simple_release=true` / `[skip ci]` 等 hard rules | 判断 + 机械门禁 | prompt + `scripts/release-tag.sh` / preflight |
