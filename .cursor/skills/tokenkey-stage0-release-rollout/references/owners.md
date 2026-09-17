## 扩展阅读

- `.cursor/skills/tokenkey-anthropic-oauth-config/SKILL.md` — 发版后 check violation 的 plan/apply/verify canonical 路径。

- `scripts/release-bump-and-tag.sh` — release 全步骤（worktree；默认 direct-push，fallback 才 delegate PR）。
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

Gateway verification owners: `gateway_capability_host.py` (prepared candidate requests),
`gateway_capability_matrix.py` (account-supply plan/report), `gateway_capability_scenarios.py`
(synthetic scenarios), `gateway_capability_check.py` (response semantics), all under `ops/stage0/`.
Legacy historical replay remains in `prod_replay.py`; it is not the default deployment verification.
See `docs/approved/prod-replay-capability-matrix.md`.


独立 QA 发布入口：`.github/workflows/deploy-qa-bundle.yml`（`deploy` / `canary-only` / `qa-infra-check`）。
普通 gateway/all rollout 不自动 dispatch QA；target contract 由 `ops/stage0/prod_release_plan.py`
自动选择 legacy-only 回滚安全分支。发布与验收边界见 `docs/approved/design-split-deploy-qa-bundle.md`。
