## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| **release 全步骤（决策→bump→push→tag，worktree 隔离）** | 机械 | `bash scripts/release-bump-and-tag.sh [--dry-run]`（默认 **direct-push**；仅 `release-main-push-route`=`bump-via-pr` 时 delegate `release-bump-via-pr.sh`；永不写共享 checkout） |
| **发版 bypass 一次性配置（scheme 1）** | 机械 | `bash scripts/release-configure-main-bypass.sh`（个人仓库：`enforce_admins=false`；组织仓库：`bypass_pull_request_allowances.users`） |
| **VERSION bump 经 PR（fallback）** | 机械 | `bash scripts/release-bump-via-pr.sh [--dry-run] [--pr N]`（仅当当前 gh 账号无法 direct-push 时） |
| main bump 路由探测（direct-push / bump-via-pr） | 机械 | `bash scripts/release-main-push-route.sh`（读 protection + 当前 gh 用户 bypass 能力） |
| VERSION/tag 三态决策（tag-only / bump-and-tag / skip-bump-skip-tag） | 机械 | `scripts/release-decide-version.sh [--emit-suggested-bump]`（被上行脚本消费；单独跑仅用于诊断） |
| 打 tag（含 skip-ci / VERSION 一致 / HEAD==origin/main 校验） | 机械 | `scripts/release-tag.sh vX.Y.Z`（被上行脚本调用；**git 侧必须带 `v`**） |
| **prod deploy / replay / smoke-only / warm dispatch** | 机械 | `bash scripts/stage0/dispatch-prod-deploy.sh --operation deploy\|replay\|smoke-only\|warm --tag X.Y.Z\|vX.Y.Z`（入口剥 `v`；不要手写 `gh workflow run deploy-stage0.yml` / `warm-image-stage0.yml`） |
| **cn-prod pull / sync-controls / prepare（SSM Hybrid）** | 机械 | `bash deploy/domestic/deploy_via_ssm.sh pull\|sync-controls\|prepare --tag X.Y.Z\|vX.Y.Z …`（身份读 `deploy/domestic/prod-target.json`；默认 S3 校验归档，pull 必传完整 `--revision`；`--transport ghcr` 显式备用；**不要**开 SSH 或 scp 离线 tar） |
| **cn-prod promote / 候选实测** | 机械 + 判断 | 主机 `bluegreen.py promote`（SSM Run Command / Session Manager）；烟测与 `--public-url` 必须对静态 IP；细则见 `deploy/domestic/README.md` |
| release.yml 恢复 dispatch（skip-ci 误伤等） | 机械 | `gh workflow run release.yml -f tag=vX.Y.Z -f simple_release=false`（**git tag 形态，带 `v`**）+ `gh run watch --exit-status` |
| watch workflow run | 机械 | `gh run watch --exit-status` |
| **prod pricing registry runtime audit** | 机械 | `ops-daily-diagnostics.yml` 经 `ops/observability/prod-config-audit.sh` 只读执行 `ops/pricing/manage-overlay-runtime.py check`；价格发布独立由 registry 合并到 protected main 后触发 `pricing-registry-publish.yml` |
| prod / warm Environment approval | 机械 | `bash scripts/stage0/approve-github-run-env.sh --run-id <id> --comment "…"`（批不批、何时批是判断） |
| prod 完整 smoke（CI 唯一验收源） | 机械 | `deploy-stage0.yml` job log 内 `tk_post_deploy_smoke: OK`（`GATEWAY_SMOKE_SUITE=full`） |
| 发版前 smoke 模型校验 | 机械 | `python3 scripts/stage0/check_smoke_config.py`（`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 均 ∈ `TK_SMOKE_API_KEY` 的 `/v1/models`）。**完整校验需要 smoke key，只在 CI 可跑**；本地降级为 `bash ops/stage0/load_smoke_github_env.sh --check prod`（只验 secret/vars 已配置） |
| 发版后跟进档位（skip / single） | 机械 | `bash scripts/release-impact-files.sh PREV NEW` → `.followup.tier`（是否值得人工再跟；**实测检查不走这里**） |
| 发版后控制面探活（prod） | 机械 | `bash ops/observability/probe-release-control-plane.sh`（prod `/health` + `/api/v1/settings/public`，JSON lines + summary） |
| **prod replay（只准备 inactive color，不切流）** | 机械 | `scripts/stage0/replay-prod-release.py --tag X.Y.Z\|vX.Y.Z`（入口剥 `v`）→ prod SSM 正常蓝绿 `prepare` → `ops/stage0/gateway_capability_host.py` 用现有测试 universal key 直连候选，串行执行完整短合成请求；核对响应、测试 usage 和线上路由 → 脱敏 receipt/results（不读取历史 capture） |
| **发版后两阶段实测（live tag→本次 tag 的全部 PR）** | 机械 | `deploy-stage0.yml` 同一 `deploy` job：蓝绿脚本在 Caddy reload 成功的真实切流点输出 `cutover_at`；`Check PR hooks immediately` 查该时刻起的 PR observables，workflow 只补足到 `cutover_at + 300s`，再由 `Check traffic and 5xx after 5 minutes` 查累计流量/5xx；两阶段复用 `plan.json` 与已批的 prod Environment |
| **发版后健康账号分组合理性检查（只读 advisory）** | 机械 | `bash ops/observability/check-account-group-bindings.sh --target prod`；从健康、可调度账号的显式 `model_mapping` 与 peer 分组证据派生，不硬编码模型→分组表；`review` / 探针失败均不阻塞 rollout |
| rollout 摘要（git log / diff stat / sentinel / deletion） | 机械 | `bash scripts/release-rollout-summary.sh --mode release` |
| prod approval 时机、smoke 模型回退 | 判断 | prompt（爆炸半径、用户入口顺序） |
| post-release verdict + Summary | 机械 | `release_post_check.py evaluate --phase immediate|delayed` + `summary` + `gate`（Summary 显式显示缺失/无效证据与 baseline failure；gate 只接受 phase 匹配且 verdict=`green`，agent 禁止另评） |
| `simple_release=true` / `[skip ci]` 等 hard rules | 判断 + 机械门禁 | prompt + `scripts/release-tag.sh` / preflight |

## 镜像发布之外的入口

- `.github/workflows/ops-stage0-pg-dump-refresh.yml` + `ops/stage0/pg_dump_refresh_via_ssm.sh` — in-place 同步共享 `tokenkey-pgdump.sh` 与该脚本的 timer 到 resolver 选择的 prod 主机；下次有类似 user-data 模板改动可参考此形状写一个 one-shot ops workflow。
- `.github/workflows/ops-stage0-host-mem-guard.yml` / `sync-host-mem-guard-via-ssm.sh` — 当前 `aws-fresh` prod 的宿主内存保护同步入口；由共享 prod resolver 选择实例和区域，不面向已退役 Hcloud。

Gateway verification owners: `gateway_capability_host.py` (prepared candidate requests),
`gateway_capability_matrix.py` (account-supply plan/report), `gateway_capability_scenarios.py`
(synthetic scenarios), `gateway_capability_check.py` (response semantics), all under `ops/stage0/`.
Legacy historical replay remains in `prod_replay.py`; it is not the default deployment verification.
See `docs/approved/prod-replay-capability-matrix.md`.


QA 仅保留 prod capture、raw archive/restore/lifecycle。独立 maintenance pin 的发布与
验收入口见 `ops/qa/README.md`，批准边界见 `docs/approved/qa-raw-only.md`。普通
gateway/all rollout 不更新该 pin，不切换 raw 存储坐标；已删除的用户导出和 worker
发布入口不可恢复。
