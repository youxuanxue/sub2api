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
| **release 全步骤（决策→bump→push→tag，worktree 隔离）** | 机械 | `bash scripts/release-bump-and-tag.sh [--dry-run]`（默认 **direct-push**；仅 `release-main-push-route`=`bump-via-pr` 时 delegate `release-bump-via-pr.sh`；永不写共享 checkout） |
| **发版 bypass 一次性配置（scheme 1）** | 机械 | `bash scripts/release-configure-main-bypass.sh`（个人仓库：`enforce_admins=false`；组织仓库：`bypass_pull_request_allowances.users`） |
| **VERSION bump 经 PR（fallback）** | 机械 | `bash scripts/release-bump-via-pr.sh [--dry-run] [--pr N]`（仅当当前 gh 账号无法 direct-push 时） |
| main bump 路由探测（direct-push / bump-via-pr） | 机械 | `bash scripts/release-main-push-route.sh`（读 protection + 当前 gh 用户 bypass 能力） |
| VERSION/tag 三态决策（tag-only / bump-and-tag / skip-bump-skip-tag） | 机械 | `scripts/release-decide-version.sh [--emit-suggested-bump]`（被上行脚本消费；单独跑仅用于诊断） |
| 打 tag（含 skip-ci / VERSION 一致 / HEAD==origin/main 校验） | 机械 | `scripts/release-tag.sh vX.Y.Z`（被上行脚本调用） |
| 读取 deployable edge 矩阵 | 机械 | `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` |
| **canary Edge 选择（第一个有 native OAuth/Kiro 池的 deployable Edge）** | 机械 | `python3 scripts/stage0/pick_oauth_canary_edge.py`（SSM 读各 edge PG 计数；与 `edge_native_anthropic_smoke.sh` 同 eligibility；`--json` 带 audit） |
| Edge dispatch 路由（edges 均为 Lightsail） | 机械 | `scripts/stage0/resolve-edge-deploy-route.py --edge-id <id> --json` |
| Edge upgrade/smoke/rollback dispatch | 机械 | `bash scripts/stage0/dispatch-edge-deploy.sh --edge-id … --operation …` |
| **其余 Edge rollout（bounded parallel fail-stop + smoke 标记验收）** | 机械 | `bash scripts/stage0/rollout-edges.sh --tag X.Y.Z --skip <canary>`（**默认 `--parallel 1` 顺序**，降低并发换容器对线上的影响；`N>1` 仅在可接受该影响时用） |
| dispatch release.yml / deploy-stage0.yml + watch | 机械 | `gh workflow run` + `gh run watch --exit-status` |
| prod 镜像预热（deploy 前，把 ~150s pull 移出关键路径） | 机械 | `gh workflow run warm-image-stage0.yml` + `approve-github-run-env.sh` + watch（只读、非致命；见 §「部署目标矩阵 → prod」） |
| prod / warm Environment approval | 机械 | `bash scripts/stage0/approve-github-run-env.sh --run-id <id> --comment "…"`（批不批、何时批是判断） |
| prod 完整 smoke（CI 唯一验收源） | 机械 | `deploy-stage0.yml` job log 内 `tk_post_deploy_smoke: OK`（`GATEWAY_SMOKE_SUITE=full`） |
| Edge smoke 分阶段（infra / edge-native-oauth / main-via-edge / full） | 机械 | `ops/stage0/edge_post_deploy_smoke.sh` + workflow `smoke_phase`；**upgrade/rollback 默认 infra**；canary 显式 **full**（infra + 容器内 per-account OAuth 拟真 `probe_account_model`）；`main-via-edge` 为可选 prod 中转链路 |
| 发版前 smoke 模型校验 | 机械 | `python3 scripts/stage0/check_smoke_config.py`（`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 均 ∈ `TK_SMOKE_API_KEY` 的 `/v1/models`）。**完整校验需要 smoke key，只在 CI 可跑**；本地降级为 `bash ops/stage0/load_smoke_github_env.sh --check prod`（只验 secret/vars 已配置） |
| 发版后跟进档位（skip / single） | 机械 | `bash scripts/release-impact-files.sh PREV NEW` → `.followup.tier` |
| 发版后控制面探活（prod + deployable edge） | 机械 | `bash ops/observability/probe-release-control-plane.sh`（prod `/health` + `/api/v1/settings/public`，deployable Edge `/health`，JSON lines + summary） |
| 发版后 tick 探针（hook 计数 + 流量/5xx/panic） | 机械 | `ops/observability/probe-post-release-tick.sh`（经 `run-probe.sh` 投递；默认 `CONTAINER=auto` 自动识别 prod blue/green active container；`HOOK_PATTERNS` 里的 hook 关键词由模型按 Step A 命名——命名是判断，计数是机械） |
| **发版后 Anthropic OAuth 配置检查（snapshot → check）** | 机械 | `python3 ops/anthropic/manage-anthropic-config.py snapshot` + `check --snapshot`（见 §「发版后 Anthropic OAuth 配置检查」；canonical：`/tokenkey-anthropic-oauth-config`） |
| rollout 摘要（git log / diff stat / sentinel / deletion） | 机械 | `bash scripts/release-rollout-summary.sh --mode release` |
| prod approval 时机、smoke 模型回退 | 判断 | prompt（爆炸半径、用户入口顺序） |
| verdict 评级（green/yellow/red） | 判断 | prompt（错误聚类 vs 基线、流量趋势） |
| Step A → 「重点观察 trace 关键词」语义命名 | 判断 | prompt（文件→hook 名映射；脚本只给文件桶） |
| `simple_release=true` / `[skip ci]` 等 hard rules | 判断 + 机械门禁 | prompt + `scripts/release-tag.sh` / preflight |

## 调用参数

本 skill 默认按用户语义解析；用户未写完整参数时，先按下面语义补全，仍有歧义再问。

```text
/tokenkey-stage0-release-rollout target=<prod|edge-<edge_id>|all> [tag=X.Y.Z] [operation=<check|release|deploy|smoke|rollback>] [previous_tag=X.Y.Z] [anthropic_config_check=false] [main_via_edge=false]
```

| 参数 | 语义 |
|---|---|
| `operation=check` | 只做预发布风险检查：对比上一个 release tag 到待发布 HEAD 的代码事实，判断上线 prod/Edge 的潜在影响；不 bump、不 tag、不 dispatch deploy。 |
| `target=prod` | release（必要时 bump/tag/build）→ `deploy-stage0.yml -f tag=…`（绑定 **`prod`** Environment）→ prod smoke → **默认** Anthropic OAuth snapshot/check。 |
| `target=edge-<edge_id>` | 默认 tag 已存在：用 **`bash scripts/stage0/dispatch-edge-deploy.sh`**（edges 均为 Lightsail，路由到 `deploy-edge-lightsail-stage0.yml`）→ watch → 按 phase 验收 smoke。`operation=smoke` 只 smoke；`operation=rollback` 用 `previous_tag`。不要手选 workflow 或手填 confirm_instance。 |
| `target=all` | release 一次 → canary **upgrade (full)** → prod deploy（CI smoke）→ **默认跳过** canary `main-via-edge` → 其余 Edge **infra rollout** → followup → **默认** Anthropic OAuth snapshot/check。`main_via_edge=true` 才跑可选段。 |
| `main_via_edge` | 默认 **false**。`target=all` 时不跑 prod→Edge 中转 smoke；缺 key 或 by-design 503 不得据此 rollback。 |
| `anthropic_config_check` | 默认 **true**（`operation=release` 且 smoke 验收通过后）。跑 `/tokenkey-anthropic-oauth-config` 的 **Stage 1–2 only**（snapshot + check，只读）。`anthropic_config_check=false` 跳过。`operation=check/smoke/rollback` 默认不跑。 |

如果用户只说“发版 / deploy 最新 / ship production”，默认 `target=prod operation=release`。如果用户说“全部 / 所有网关 / prod + edge / all”，默认 `target=all operation=release`。如果用户说“检查 / 预判 / 评估上线影响 / release check”，默认 `operation=check target=all`。

## check 模式：预发布生产影响评估

`operation=check` 是只读门禁，用来回答“如果这些变更现在上线 prod / Edge（当前 deployable 矩阵）会不会影响线上服务”。它**不修改文件、不 bump VERSION、不创建 tag、不 dispatch workflow**。

默认范围：从最新已发布 tag 到当前待发布 HEAD。用户给 `previous_tag` 时用该 tag；用户给 `tag` 时用该 tag 作为目标，否则用 `HEAD`。

执行步骤：

1. 同步事实：`git fetch origin main --tags`，确认当前分支、HEAD、`origin/main`、工作区状态；如工作区有未提交改动，必须在报告中标出，不能把它当作已发布事实。
2. 决定范围：
   - `NEW_REF=${tag:+v$tag}` 后检查为空则设为 `HEAD`；也可以直接用 `NEW_REF=${tag:+v$tag}; NEW_REF=${NEW_REF:-HEAD}`。
   - `PREV_TAG=${previous_tag:+v$previous_tag}`，未给则取 `NEW_REF` 前一个 `v*` release tag；若当前 VERSION 对应 tag 已存在且 `origin/main` 在其后，范围应是该 tag 到 `origin/main`（或当前本地 `HEAD`，取决于本次待检查对象）。
3. 盘点提交和文件：
   - `git log --oneline --decorate ${PREV_TAG}..${NEW_REF}`。
   - `git diff --stat ${PREV_TAG}..${NEW_REF}`。
   - `git diff --name-status ${PREV_TAG}..${NEW_REF}`。
4. 单独确认发布/部署契约是否变化：检查 `release.yml`、`deploy-stage0.yml`、`deploy-edge-lightsail-stage0.yml`、Dockerfile、`backend/go.mod` / `go.sum`、`frontend/package.json` / lockfile、`backend/cmd/server/VERSION`、`deploy/` 是否有 diff。
5. 按运行时影响面读代码事实，而不是只看提交标题：
   - 后端请求路径：gateway、auth、rate limit、scheduler、quota/billing、model-list/pricing、newapi bridge、middleware。
   - 前端线上路径：登录/注册、admin settings、API client、嵌入 dist freshness。
   - 数据层：Ent schema、migrations、repository、默认设置初始化。
   - 运维面：release/deploy workflow、Stage0 SSM primitive、Caddy/compose、smoke scripts、ops workflow。
   - 上游隔离：是否删除 upstream-owned 文件/route/method，是否触发 sentinel registry。
6. 跑本地门禁：执行 `bash scripts/preflight.sh`；失败必须进入风险结论，不能给出“可放心发布”的结论。其中 `migration immutability` 段会拦截对已合并 migration 文件的修改（典型症状：deploy 时 `checksum mismatch`）；应新建 `tk_NNN_*.sql` 而不是改旧文件。
7. 输出中文结论，必须包含：
   - `范围`：`${PREV_TAG}..${NEW_REF}`、commit 数、主要提交。
   - `发布契约`：release/deploy/Docker/deps/VERSION 是否变化。
   - `运行时影响`：逐项说明哪些路径会变，引用 `file:line` 代码事实。
   - `线上风险等级`：低 / 中 / 高，并说明原因。
   - `建议`：是否建议发布；若建议，列出 edge/prod smoke 重点；若不建议，列出阻塞项。
   - `未覆盖/假设`：例如缺少线上密钥、本地无法实际 UI 验证、某些行为只在配置开启时触发。

## Jobs / OPC 默认部署顺序

`all` 不是并行全量推送。默认采用顺序化 canary rollout：

1. **release build 一次**：只构建一个 multi-arch GHCR tag，所有目标复用同一 image，避免两套产物。
2. **Edge canary：第一个有 native OAuth/Kiro 池的 deployable Edge upgrade + full smoke（显式 `--smoke-phase full`）**：用 `python3 scripts/stage0/pick_oauth_canary_edge.py` 选 canary（不是矩阵下标第一个）；验证镜像启动、infra 门禁，并在该 edge 容器内对每个可调度 Anthropic OAuth 账号跑拟真 `/v1/messages`（`probe_account_model` / `smoke_anthropic_realistic.py`）。**其余 Edge 一律 infra only**（`rollout-edges.sh`）。
3. **prod 主网关 upgrade + 完整 prod smoke**：Edge canary 过后再升级 prod。
4. **（可选）main gateway via Edge smoke**：仅当需要验证 prod→Edge 中转调度时，`smoke_phase=main-via-edge`；缺 `TK_SMOKE_API_KEY` 记 partial，不 rollback。
5. **其余 deployable Edge bounded-parallel rollout**：prod full smoke 绿后，`rollout-edges.sh` 对每个 edge dispatch upgrade（**infra only**，验 log 含 `tk_edge_post_deploy_smoke: OK phase=infra`）。

例外：

- `target=prod`：只发版/部署 prod，不自动部署 Edge。
- `target=edge-<edge_id>`：只升级/烟测对应 Edge，不发新 release，除非用户显式要求先 release。
- 用户强指定“prod 先”时照做，但在摘要中标出与默认 canary 顺序的差异。

## 一次性跑完（原则）

- **顺序做完**：`release-bump-and-tag.sh` → **watch 到 release 成功** → 根据 `target` dispatch 对应 deploy workflow → **watch 到 deploy/smoke 成功** → **Anthropic OAuth snapshot/check（默认）** → **再做本地/日志验收 / followup**。不要在 workflow 绿灯后就结束会话。
- **永远不在共享 checkout 里 bump/tag**：`release-bump-and-tag.sh` 自带 worktree 隔离与 fetch，不需要也不应该先 `git checkout main && git pull`（并行 agent 可能正占用 checkout——见 §「决策 + bump + tag」）。
- **`gh run watch` 要给够时间**：多架构 `release.yml` 常见十余分钟量级；Agent 应用 `--exit-status` 跟跑到结束，不要用默认短超时提前杀掉。
- **Environment approval 不是失败**：`prod` / `edge-<edge_id>` run 卡在 `waiting` 时，按 §「部署目标矩阵 → prod」的 approval 命令在 canary 绿后自批（执行账号非 reviewer 时回落人工）；批准后继续 watch。
- **完整烟测密钥**：prod 完整 smoke 以 **CI `deploy-stage0` job log** 为唯一验收源；**仅可选** `main-via-edge` 段需要 **`TK_SMOKE_API_KEY`**（经 prod 中转）；edge-native OAuth 烟测在 edge 主机容器内直连本机网关，不依赖该 key。
- **部署前先校验 OIDC 目标实例**：`tokenkey-cicd-oidc` 的 `TargetInstanceId` 必须等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致会在 `Deploy via SSM Run-Command` 直接 `AccessDenied(ssm:SendCommand)`。
- **禁止 `simple_release_override=true`**：prod / Edge 当前都跑 AWS Graviton arm64；单架构 manifest 会导致 `exec format error`。
- **`gh` 连接抖动先做无代理重试**：若连续出现 `read ... 127.0.0.1:7890: connection reset by peer`，用一次性环境变量重试：`env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh ...`，恢复后再继续 watch/dispatch。
- **无代理模式要校验 gh 身份**：去掉 `GH_TOKEN` 或 proxy 后，`gh` 可能切到别的账号；dispatch 前必须 `gh auth status` 确认 active account 是目标仓库有权限的账号，避免 `HTTP 403 Must have admin rights to Repository`。

## 前置条件

- 工作目录：仓库根目录（含 `backend/`、`scripts/release-tag.sh`）。
- 网络、`git`、`gh` 已认证且对远端可写；`gh` 能 dispatch `release.yml`、`deploy-stage0.yml`、`deploy-edge-lightsail-stage0.yml`。
- GitHub Environment：**`prod`**、各 Edge 的 `edge-<edge_id>`（若有 Required reviewers，需人工批准）。新 edge 可参考已上线 edge 的变量/密钥结构，但 `EDGE_GHCR_PAT_SSM_NAME` 必须使用该 edge 自己的 SSM 路径。
- **禁止**：VERSION bump / 发版 commit 的正文里出现字面量 `[skip ci]` 或 `[ci skip]`（任意位置都不行）。

## 决策 + bump + tag：worktree 隔离（scheme 1 默认 direct-push）

**一次性配置**（新机器 / 新协作者 / fork 后；已配可 `--check`）：

```bash
bash scripts/release-configure-main-bypass.sh          # 写入 GitHub 分支保护
bash scripts/release-configure-main-bypass.sh --check  # 期望 release-main-push-route=direct-push
```

- **个人仓库**（`youxuanxue/sub2api`）：GitHub 不支持 bypass 用户列表 → 脚本设 `enforce_admins=false`，**仅 repo admin** 可 bypass PR 规则直推 VERSION bump；协作者仍须 PR。
- **组织仓库**：合并 `TK_RELEASE_BYPASS_USERS` 到 `bypass_pull_request_allowances.users`。

**发版前先决策**（只读）：

```bash
bash scripts/release-decide-version.sh --emit-suggested-bump
bash scripts/release-bump-and-tag.sh --dry-run   # 含 main 保护探测（bump-via-pr vs direct-push）
```

**canonical 入口**（Agent 只跑这一条；脚本内部路由）：

```bash
bash scripts/release-bump-and-tag.sh            # decide → bump（direct 或 PR 子流程）→ tag
```

| `release-decide-version` action | 脚本行为 |
|---|---|
| `skip-bump-skip-tag` | 退出 0；用现有 tag 继续 deploy |
| `tag-only` | worktree @ origin/main → `release-tag.sh` |
| `bump-and-tag` + `release-main-push-route` = **direct-push** | worktree bump → `push origin HEAD:main` → tag |
| `bump-and-tag` + route = **bump-via-pr** | 自动 `exec release-bump-via-pr.sh`（PR → merge → tag-only） |

**protected main 子流程（fallback）**（`release-main-push-route` = `bump-via-pr` 时）：

```bash
bash scripts/release-bump-via-pr.sh              # 全流程
bash scripts/release-bump-via-pr.sh --dry-run
bash scripts/release-bump-via-pr.sh --pr 1169   # CI 已绿、仅 merge+tag
```

PR 路径纪律：

- bump commit 正文必须含 `no-web-impact`（preflight web surface 机械检查）。
- CI 仅 **preflight** 段 flaky 时：`gh run rerun <run_id> --failed`，再 `--pr N` resume；**不要**为通过 CI 改 VERSION。
- merge 后 worktree 可能占着 `chore/bump-version-*` 分支 → `gh pr merge` 本地删分支失败可忽略；成功路径结束时会 `worktree remove --force`。
- merge 后**必须** `git fetch origin main --tags`，再 tag（`release-bump-via-pr.sh` 末尾已调用 `release-bump-and-tag.sh`）。

**为什么禁止在共享 checkout 里手动 bump**：主 checkout 可能被并行 agent 切到 feature 分支或塞满 WIP（已三次实录）。worktree 隔离 + `release-tag.sh` 门禁（skip-ci / VERSION / HEAD==origin/main）——不要手 `git tag`。

## 标准流程：release 新镜像

1. **bump + tag**：`bash scripts/release-bump-and-tag.sh`（见上节；输出末行含最终 tag）。
2. **等待镜像**：`gh run list --workflow=release.yml --limit 1` → 取刚触发、与本次 tag 对应的 run → `gh run watch <id> --exit-status`，直到 success。
3. 记录 `TARGET_TAG=X.Y.Z`（tag 不带 `v`），后续 prod / Edge deploy 都用这一份 image。

## 部署目标矩阵

### prod：主网关

`release.yml` **不再自动 queue prod**（原 `queue-prod-deploy` job 已移除）——release 只 build 镜像，部署由本 skill 在 build 成功后**显式 dispatch**（顺序与 rollout 意图由 skill 持有；裸 tag-push 不会自动部署 prod，发版务必走 skill）。

**先预热镜像（把 deploy 的 ~150s 镜像 pull 移出关键路径）**：`deploy-stage0` 的 in-band `docker compose pull` 是其 SSM 步骤最大耗时（prod 实测 ~150s，占 ~84%）。`warm-image-stage0.yml` 在 prod 主机上**只读** `docker pull` 新 tag（不改 `.env`、不重启容器），warm 完成后 deploy 的 pull 退化成 ~3s no-op。warm 是只读的，所以**自批它的 `prod` Environment 门禁是安全的**（门禁是为「改变在跑服务的 deploy」设的）。warm **失败/超时非致命**——deploy 仍会自己 pull，绝不阻断发版。dispatch deploy 前先 warm：

```bash
# 1) dispatch 只读预热（与 deploy 同 prod Environment）
gh workflow run warm-image-stage0.yml -f tag="$TARGET_TAG"
# 取刚触发的 warm run：选最近一个**未完成**的（卡在 waiting/in_progress 的就是它），
# 避免 --limit 1 抓到上一次已 completed 的旧 run。必要时 sleep 几秒等 run 出现。
WARM_RUN_ID=$(gh run list --workflow=warm-image-stage0.yml --event=workflow_dispatch --limit 5 \
  --json databaseId,status --jq '[.[]|select(.status!="completed")][0].databaseId')
# 2) 自批 prod Environment（warm 只读，自批安全）
bash scripts/stage0/approve-github-run-env.sh \
  --run-id "$WARM_RUN_ID" \
  --comment "read-only image prewarm for $TARGET_TAG"
# 3) watch 到完成；非致命——失败也继续 deploy（deploy 自己付 pull）
gh run watch "$WARM_RUN_ID" --exit-status || echo "warm non-fatal failure — proceeding to deploy (it will pay the in-band pull)"
```

先确认没有遗留的同 tag deploy run，再 dispatch（tag 不带 `v`）：

```bash
gh run list --workflow=deploy-stage0.yml --limit 3 --json databaseId,event,createdAt,status,displayTitle
gh workflow run deploy-stage0.yml \
  -f tag="$TARGET_TAG"
```

**Environment approval（机械命令，时机是判断）**：canary full smoke 绿后再批 prod deploy：

```bash
RUN_ID=<deploy run id>
bash scripts/stage0/approve-github-run-env.sh \
  --run-id "$RUN_ID" \
  --comment "canary <edge> full smoke OK (run <canary run id>)"
gh run view "$RUN_ID" --json status --jq .status   # 期望 in_progress
```

若 API 返回 403（执行账号非 reviewer），则回落人工批准，不要换号绕过。底层仍为 `gh api …/pending_deployments` POST JSON（`-f`/`-F` 在数组字段上不可靠）。

**target=all 注意**：prod 不再被 release 自动 queue，所以**先不要 dispatch prod deploy**——等第一个 deployable Edge canary full smoke 绿后，再 `gh workflow run deploy-stage0.yml -f tag=$TARGET_TAG`，然后按上面的 approval 命令自批。canary-first 顺序由"先不 dispatch deploy"自然保证，不再依赖"先不批准"的提前 waiting run。**prod 的 warm（上面的预热块）是例外**：它只读、与 canary 无依赖，应在 canary full smoke **跑的同时**就提前 dispatch+自批，让镜像 pull 与 canary 并行——这样 canary 绿后 dispatch prod deploy 时层已在盘上，净省一次 pull 的墙钟。

### edge-<edge_id>：Edge 资源节点（Lightsail，单一 dispatch 入口）

以 `resolve-edge-target.py --list-deployable` 为准（edges 均为 Lightsail，`deployable=true`）。**禁止**手选 workflow——一律走 dispatch 脚本（它路由到 `deploy-edge-lightsail-stage0.yml`）：

```bash
TARGET_TAG=X.Y.Z
EDGE_ID=<edge_id>

# upgrade（默认 smoke_phase=infra；canary 发版时显式 --smoke-phase full）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation upgrade \
  --tag "$TARGET_TAG"

# canary full（target=all 第一步；CANARY_EDGE 来自 pick_oauth_canary_edge.py）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$CANARY_EDGE" \
  --operation upgrade \
  --tag "$TARGET_TAG" \
  --smoke-phase full

# smoke only（默认 smoke_phase=full）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation smoke \
  --smoke-phase full          # 或 infra | edge-native-oauth | main-via-edge

# rollback
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation rollback \
  --tag "$PREVIOUS_TAG"
```

路由事实源（只读核对）：

```bash
python3 scripts/stage0/resolve-edge-deploy-route.py --edge-id "$EDGE_ID" --json
# → platform, workflow_file, confirm_flag, confirm_value
```

`provision` / `rotate_egress_ip` / `decommission` 仍可直接 `gh workflow run`（非常规 rollout）；日常 release 只用上面的 dispatch 脚本。

## target=all 的执行顺序

0. **轻量预检（非完整 prod smoke）**：在 release 之前只做控制面探活 + 账号/scheduling 状态快照，避免把运维状态误判成 release 回归。
   - a. `bash ops/observability/probe-release-control-plane.sh`，期望 `summary.status=ok`（prod `/health` + `/api/v1/settings/public`，deployable Edge `/health`）。
   - b. 可选：`gh workflow run ops-daily-diagnostics.yml -f operation=diagnostics -f target_selector=prod -f diagnostics_log_since=20m`（或只读查最近错误聚类），确认 anthropic/openai/gemini 账号池非空、无新 cluster。
   - c. 向运维确认：各 deployable Edge 在 prod 端是否**预期可调度**；若刻意不可调度，canary 的 main-via-edge 503 `"no available accounts"` 记为 **by design**，不触发 rollback。
1. 完成「标准流程：release 新镜像」，得到 `TARGET_TAG`。
2. 读取 deployable 矩阵并**机械化选 canary**：

   ```bash
   CANARY_EDGE="$(python3 scripts/stage0/pick_oauth_canary_edge.py)"
   # 诊断：python3 scripts/stage0/pick_oauth_canary_edge.py --json
   python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable
   # edges 均为 Lightsail deployable=true（uk1、us2、us3、us4、…）
   ```

   `pick_oauth_canary_edge.py` 按 `--list-deployable` 顺序逐个 SSM 探测 schedulable native OAuth/Kiro 账号数，**第一个 count>0 的 edge 为 canary**。若全部为零，脚本 exit 1——先修 Edge 账号池，不要对无池 edge 跑 full smoke。

3. Canary upgrade + full smoke：`dispatch-edge-deploy.sh --edge-id=$CANARY_EDGE --operation upgrade --tag=$TARGET_TAG --smoke-phase full`，watch 到 success。
4. 推进 prod deploy：canary full smoke 绿后 `gh workflow run deploy-stage0.yml -f tag=$TARGET_TAG`（prod 不再自动 queue），按 approval 命令自批，watch 到 success。
5. **prod 验收（CI 唯一源）**：在本次 `deploy-stage0` run log 搜索 `tk_post_deploy_smoke: OK`，并核对 models/chat/messages 等 shape（见「prod 真实测试」§A）。**不要**在本地再跑完整 `post_deploy_smoke.sh`，除非 CI secret 缺失或日志不可解析。
6. **main-via-edge（默认跳过）**：除非 `main_via_edge=true`，摘要写 `main-via-edge: skipped (default)`。显式跑时用 `dispatch-edge-deploy.sh --edge-id $CANARY_EDGE --operation smoke --smoke-phase main-via-edge`。缺 **`TK_SMOKE_API_KEY`** 记 partial，不 rollback。
7. **其余 deployable Edge（单命令，bounded-parallel fail-stop）**：

   ```bash
   bash scripts/stage0/rollout-edges.sh --tag "$TARGET_TAG" --skip "$CANARY_EDGE"   # 默认 --parallel 1（顺序）
   # 每个 edge 输出 rollout-edges: edge=<id> run_id=<id> result=ok；全过输出 ALL_OK n=<k>
   ```

   脚本按 batch dispatch upgrade（`--smoke-phase infra`）→ watch → 验 `tk_edge_post_deploy_smoke: OK phase=infra`。**默认 `--parallel 1`（顺序）**；`N>1` 仅在可接受换容器窗口影响时用。批内失败则 fail-stop。
8. **发版后跟进（按 diff 档位，至多一次 tick）**：跑 `release-impact-files.sh` 读 `.followup.tier`：
   - `skip` → 不跟进，直接 rollout summary。
   - `single` → 仅 **+5min** 一次轻量诊断（含 gateway/schema/config 类变更；多轮 extended 档已下线，更长窗口仅人工显式发起）。
9. **Anthropic OAuth 配置检查（默认，在 followup 之前）**：见 §「发版后 Anthropic OAuth 配置检查」。smoke 全绿后立即跑；violations 不触发 rollback，写入 rollout 摘要为 **yellow** 并指向 `/tokenkey-anthropic-oauth-config` 修复路径。

## prod 真实测试

部署 workflow 成功只说明流水线过了；**prod 完整网关烟测以 CI 为唯一 canonical 验收**（Jobs：一条路径一个意图）。

### A — CI 日志中的完整网关烟测（默认且充分）

在本次 `deploy-stage0` run log 里搜索 `tk_post_deploy_smoke: OK`，并确认：

- `/v1/models`：`object=list` 且 `data` 非空。
- `/v1/chat/completions`：`object=chat.completion`，`choices[]` 非空，`usage` 存在。
- `/v1/messages`：`type=message`，`role=assistant`，`content[]` 有文本，`usage` 存在。
- Gemini / OpenAI OAuth 探针：workflow 已配置 `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 时应在 log 中按模型出现对应 section（软警告 vs 硬失败见 `post_deploy_smoke.sh`）。

**Gemini tools 探针 verdict（路由回归 vs 运行时资源）** — CI log 里 `POST …/v1/messages (gemini, with tools)`：

| HTTP / 日志 | 含义 | rollout |
|---|---|---|
| **400** + Codex / ChatGPT account 文案 | universal 误路由 OpenAI passthrough（如 #1168） | **red** — 停 rollout，优先 rollback |
| **429** + `gemini section soft-skipped` / runtime resource | 账号 cooldown，非 schema 回归 | **green/yellow** — 不 rollback；`tk_post_deploy_smoke: OK` 仍可过 |
| **200** + 正常 shape | 探针通过 | green |

**硬门禁仍是** log 末行 **`tk_post_deploy_smoke: OK`**；上述表只解释 warning 语义，不可替代 OK 行。

若 CI 因 smoke model 不在 key 的 `/v1/models` 列表失败：改 **`prod`** Environment 对应的 **`TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS`** 后 **重跑 deploy-stage0**。

### B — 本地快速探活（无密钥，可选）

仅在 CI 不可达或需人工 double-check 时用：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/health'
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/api/v1/settings/public'
```

期望 200。**不要**把本地完整 `post_deploy_smoke.sh` 当作默认 prod 验收（与 CI 重复）。

### C — 本地完整烟测（例外路径）

仅当 **CI secret 未配置** 或 **无法读取 deploy-stage0 run log** 时启用：

```bash
export TOKENKEY_BASE_URL=https://api.tokenkey.dev
export TK_SMOKE_GITHUB_ENV=prod              # 自动拉 GitHub Environment variables
export TK_SMOKE_API_KEY=sk-...               # secret 值 GitHub API 不可读，须本机 export 同名
bash ops/stage0/post_deploy_smoke.sh
```

校验 GitHub Environment 配齐（不跑 HTTP）：`bash ops/stage0/load_smoke_github_env.sh --check prod`

通过标准同 `ops/stage0/post_deploy_smoke.sh` 文档；缺 key 不得声称 prod 完整验收通过。

**prod release 默认下一步**：prod smoke 在 CI log 确认 `tk_post_deploy_smoke: OK` 后，立即跑 §「发版后 Anthropic OAuth 配置检查」（除非 `anthropic_config_check=false`）。

## Smoke 架构（单一 runner + suite）

| 脚本 | 角色 |
|---|---|
| `ops/stage0/post_deploy_smoke.sh` | **唯一** gateway 业务烟测 runner；`GATEWAY_SMOKE_SUITE` 控制探针子集 |
| `ops/stage0/smoke_lib.sh` | 共享 soft-degrade、model pick、suite gating |
| `ops/stage0/edge_post_deploy_smoke.sh` | Edge smoke 编排（infra / edge-native-oauth / main-via-edge / full） |
| `ops/stage0/gateway_smoke.sh` | 手动 quick 探活（有 key 时 `GATEWAY_SMOKE_SUITE=quick` 委托 post_deploy） |
| `scripts/stage0/check_smoke_config.py` | 发版前校验各平台/channel smoke 模型清单均在 `TK_SMOKE_API_KEY` 的 `/v1/models` |
| `ops/stage0/smoke_env.sh` | GitHub `TK_SMOKE_*` 默认值与导出（`TK_SMOKE_GITHUB_ENV` 触发 gh 拉 variables） |
| `ops/stage0/load_smoke_github_env.sh` | 从 GitHub Environment 拉 `TK_SMOKE_*` variables；`--check` 校验 secrets/vars 已配置 |

**GitHub Environment 配置（canonical `TK_SMOKE_*`）** — 详见 `deploy/aws/README.md` § Smoke config：

| Environment | Secrets | Vars |
|---|---|---|
| **`prod`** | `TK_SMOKE_API_KEY` | `TK_SMOKE_ANTHROPIC_MODELS`, `TK_SMOKE_GEMINI_MODELS`, `TK_SMOKE_OPENAI_OAUTH_MODELS` |
| **`edge-<id>`** | `TK_SMOKE_API_KEY` | `TK_SMOKE_EDGE_LOCAL_CHAT_MODELS`（可选；默认见下） |

> `TK_SMOKE_API_KEY` 是 `deploy-stage0.yml` 硬前置——缺 key，发版在镜像切换前 `::error::` 失败。平台覆盖只维护模型清单，不再维护多把平台专用 smoke key。

**Edge smoke 固定值（代码内）：** `TK_SMOKE_EDGE_CANARY_BASE_URL=https://api.tokenkey.dev`，`TK_SMOKE_EDGE_LOCAL_CHAT_MODELS=claude-sonnet-4-6`。

**Suite 矩阵**：

| `GATEWAY_SMOKE_SUITE` | 何时 | 探针 |
|---|---|---|
| `full`（默认） | prod `deploy-stage0` | public + frontend + models + chat/messages 跑 `TK_SMOKE_ANTHROPIC_MODELS`；Gemini tool-schema 跑 `TK_SMOKE_GEMINI_MODELS`；OpenAI OAuth 跑 `TK_SMOKE_OPENAI_OAUTH_MODELS` |
| `main-via-edge` | canary `smoke_phase=main-via-edge` | public + models + `/v1/messages`（Claude Code UA）；**跳过 chat**（Edge-only key 常限制 `/v1/messages`） |
| `quick` | 本地 `gateway_smoke.sh` | public + models + chat |

**模型清单**：清单中的任一模型不在 `TK_SMOKE_API_KEY` 的 `/v1/models` 时 hard fail；发版前用 `check_smoke_config.py` 提前拦截配置漂移。

## Edge smoke 验收

Edge workflow 在 `external_health.sh` 之后调用 `edge_post_deploy_smoke.sh`，并传 `SKIP_EXTERNAL_HEALTH=1`（避免重复外网 `/health`）。

**分阶段（`EDGE_SMOKE_PHASE` / workflow `smoke_phase`）**：

| phase | 何时用 | 验证内容 |
|---|---|---|
| `infra` | **upgrade/rollback 默认**；`rollout-edges.sh` 其余 Edge | 公网 runner `/v1/models`→403；SSM compose ps + localhost health |
| `edge-native-oauth` | 单独 OAuth 健康检查 | edge 容器内 per-account 拟真 `/v1/messages`（`probe_account_model`） |
| `main-via-edge` | **可选**，验证 prod→Edge 中转 | `GATEWAY_SMOKE_SUITE=main-via-edge` 经 prod 主网关 + Edge 日志确认 |
| `full` | **canary upgrade（显式）**；`operation=smoke` 且未指定 phase 时 | infra + edge-native-oauth |

验收 checklist：

- `full`：workflow log 含 `tk_edge_post_deploy_smoke: OK phase=full`（canary 或显式 full dispatch）。
- `infra`：log 含 `tk_edge_post_deploy_smoke: OK phase=infra`（其余 Edge rollout 默认）。
- `main-via-edge`（可选）：log 含 main gateway smoke 成功或明确 skip（缺 key）。
- 非 canary Edge：**只需 infra**；不得因缺少 edge-native-oauth 或 main-via-edge 判 fail。

## rollback

- prod rollback：dispatch `deploy-stage0.yml` 到 previous tag。

```bash
gh workflow run deploy-stage0.yml \
  -f tag="$PREVIOUS_TAG"
```

- Edge rollback：`bash scripts/stage0/dispatch-edge-deploy.sh --edge-id <id> --operation rollback --tag "$PREVIOUS_TAG"`。

```bash
EDGE_ID=<edge_id>
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation rollback \
  --tag "$PREVIOUS_TAG"
```

prod smoke 失败：停，优先 rollback prod；不要继续 Edge rollout。Edge canary 失败：停，不批准/推进 prod，除非用户明确 override。

**自动 rollback 也救不回 → 切灾难恢复（单次救不回即切，不必等"反复 N 次"）**：`deploy-stage0.yml` 调的 `ops/stage0/deploy_via_ssm.sh` 已内置 rollback ERR trap（失败自动恢复上一镜像）。当它**也救不回**——SSM 日志出现 `::error::…node requires MANUAL intervention`——或 dispatch rollback 到 `previous_tag` 后 external_health / smoke 仍失败，说明这已不是镜像级问题（整机 / OS / 数据卷 / 迁移 checksum 钉死）。此时切到 `deploy/aws/RUNBOOK-disaster-recovery.md`，按其 **§Agent 协同契约** 执行（Agent 自主跑只读/可逆步骤、高风险步骤先 plan 再等人类批）——具体边界与命令以该 runbook 为唯一权威，本段不复述。

## 完成后：发版后跟进（按 diff 档位）

发版后跟进**至多一次 +5min tick**（多轮 extended 档已下线——一次 tick 足以暴露启动/hook 级回归，更长窗口仅人工显式发起）。先机械化分档：

```bash
PREV_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | sed -n '2p')
NEW_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1)
bash scripts/release-impact-files.sh "${PREV_TAG}" "${NEW_TAG}"
# 读 JSON .followup.tier → skip | single
```

| tier | 动作 |
|---|---|
| `skip` | 不跟进；直接 rollout summary |
| `single` | **仅 +5min** 一次轻量诊断 |

### Step A：重点观察变量（single 时）

跟进基于本次 diff，不跑固定 metric 集。文件桶分类机械化（`release-impact-files.sh`），桶内 hook 名由模型按 release 内容命名（判断）。

JSON 出来后，对每个非空 bucket，**模型**按下表把改动文件映射到「重点观察 trace 关键词」。脚本只给桶（机械），关键词由模型按 hook 命名（判断）：

| 改动类型 | 重点观察的 trace 关键词 |
|---|---|
| 新背景 goroutine（如 `*_reaper.go`） | grep `XxxReaper` 启动日志、cycle 频率、Cleanup 退出消息 |
| 新 gateway 路径 hook（如 `*_tk_signature_preempt.go`） | grep `applyXxxIfArmed` / `armXxxOnError` 触发次数、影响的 account_id 分布 |
| 新错误处理分支（如 `*_silent_refusal.go`） | grep `silent_refusal` / 新增 `ops_error_logs.reason` 取值 |
| Stream 行为改动（keepalive ticker、超时等） | 看 SSE 连接持续时间分布、`Gateway.StreamKeepaliveInterval` 是否实际启用 |
| `wire.go` / `wire_gen.go` DI 变化 | 看启动日志 provider 顺序无 panic、新依赖构造无 nil |
| `config.go` 新字段 | 在 prod / Edge `.env` 与 `.env.example` 对齐确认；进容器后只输出字段是否设置或 redacted 状态，不打印变量值 |
| 数据库 schema / migrations | 新表/列的写入路径在 5 分钟窗口是否 surfacing 异常 |
| 前端 frontend dist hash 变化 | embedded dist freshness + 关键页面 200 |

把「重点观察变量」列在第一次跟进的开头，让 user 看到这一次跟进是按本次发版定制的，而非通用模板。

### Step B：每次 tick 的内容

1. **控制面探活（一条命令）**：不要手写 `curl`/`jq` loop；用统一脚本输出 JSON lines + summary：

   ```bash
   bash ops/observability/probe-release-control-plane.sh
   # summary.status 期望 ok；EDGE_IDS=us3,us4 可缩小范围，EDGE_IDS=none 仅查 prod
   ```

2. **hook + 流量 + 5xx + panic（一条命令）**：Step A 命名的 hook 关键词作为 `HOOK_PATTERNS`（逗号分隔的固定字符串，grep -F 语义）传给入库探针——不要每次现场手写探针脚本：

   ```bash
   bash ops/observability/run-probe.sh --target prod \
     --script ops/observability/probe-post-release-tick.sh \
     --env SINCE=6m \
     --env "HOOK_PATTERNS=<hook1>,<hook2>" \
     --timeout-seconds 120
   ```

   `probe-post-release-tick.sh` 默认 `CONTAINER=auto`：prod blue/green deploy 后会读 `/var/lib/tokenkey/active-color` 自动转成 `tokenkey-blue` / `tokenkey-green`，找不到时回退 legacy `tokenkey`。不要因为 `No such container: tokenkey` 手工补救；若仍失败，记录 stdout 的 `container_resolution` 后再排查。

3. **错误聚类**（tick 2 起，或任何 5xx/异常出现时）：`ops/observability/ops-error-triage.sh` 经 run-probe 投递，分钟窗用 `--env WINDOW_MINUTES=15`；更深排查转 `/tokenkey-online-log-troubleshooting`。
4. **hook 触发语义**：没看到触发 = 正常（流量未触达该路径）；触发频次明显高于基线 = 异常；触发后立即 5xx 跟随 = 异常。

### Step C：每次 tick 的固定输出形状

每次跟进结束输出一段 5–12 行的简洁汇报，结构固定（占位符在执行时按 Step A 提取结果填实）：

```text
[+Nmin post-release ${NEW_TAG}]
control plane: api 200 ✓ | settings/public 200 ✓ | <each deployable edge> 200 ✓
errors (last 5m): <cluster summary by reason/status_code/platform, or "none">
traffic (last 5m): N total | chat=X messages=Y models=Z
new-code hooks:
  - <hook 1 from Step A>: <grep result, or "no fires">
  - <hook 2 from Step A>: <grep result, or "no activity">
  - ...
verdict: green | yellow | red — <one-line reason>
```

不要把上面模板照搬当输出：`new-code hooks` 的具体 hook 名由 Step A 的 diff 分析动态产出。纯前端 / 纯 chore release 没有 backend hook 时，直接写 `(no new backend hooks this release)`。

`verdict` 判定原则：

- **green**：control plane 全 200 + 错误聚类无新 cluster + 重点观察变量按预期触发或合理静默 + 流量量级与基线一致
- **yellow**：control plane OK 但某条路径错误率上升 / 重点 hook 未按预期触发或频次异常 / 流量明显偏低或偏高 → 列出可疑 cluster + 建议是否需要人工触发更长窗口观察
- **red**：control plane 任一点 fail / 错误聚类含 new type 且 rate 高于基线 2× / 重点 hook 触发后立即 5xx / 流量塌方 → **立即汇报，不再续 tick**，建议人工立即决定是否 rollback 到 `previous_tag`

### Step D：tick 后的综合建议

- **`single`**：+5min 一次 tick 后立即给综合建议（1 tick）。
- **`skip`**：跳过本节。

综合建议结构：

```text
=== Post-release follow-up summary (${NEW_TAG}, <N> tick(s)) ===
重点变更：<列出 Step A 的关键 hook / 配置项>
control plane：<N>/<N> ticks green | <or list any failure tick>
错误聚类汇总：<去重的 cluster + 频次趋势>
流量趋势：<是否与基线一致>
重点观察变量结论：<逐项 hook 是否按预期>
综合 verdict: green / yellow / red
建议：
  - <green>: 发版稳定，无 follow-up。
  - <yellow>: 列出 1-3 条需要在 24h / 1week 内再观察或修复的事项；建议是否人工触发 +1h / +6h 跟进。
  - <red>: 列出可疑回归点，建议立即 `gh workflow run deploy-stage0.yml -f tag=${PREVIOUS_TAG}` rollback；其余 Edge 暂不批准 prod approval。
```

### 调度纪律

- **`single`**：最后一个 smoke 通过后 **+5min** 一次，green / yellow / red 都在这一次 tick 给结论，不自行加轮。
- 更长窗口（+1h / +6h）仅人工显式发起；会话内不要自行无限延期。

## 发版后 Anthropic OAuth 配置检查（默认）

**触发**（同时满足）：

- `operation=release`（非 check / smoke-only / rollback）
- 本次请求的 deploy + smoke 已验收通过（prod CI `tk_post_deploy_smoke: OK`；`target=edge-*` 对应 phase OK；`target=all` 至少 prod + canary infra OK）
- 未显式 `anthropic_config_check=false`

**不做什么**：本段**只读**——不 `apply`、不 `sync-runtime`、不改 live DB/Redis。修复路径交给 canonical skill **`/tokenkey-anthropic-oauth-config`**（plan → apply → verify）。

**机械化命令**（artifact 落盘，便于 rollout 摘要引用）：

```bash
NEW_TAG="${NEW_TAG:-$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1 | sed 's/^v//')}"
JOBDIR="${JOBDIR:-/tmp/tk-post-release-${NEW_TAG}-$$}"
MGR=ops/anthropic/manage-anthropic-config.py
mkdir -p "$JOBDIR"

python3 "$MGR" snapshot --out "$JOBDIR/post-release-snap.json"
SNAP_RC=$?

CHECK_RC=0
if [[ "$SNAP_RC" -eq 0 ]]; then
  python3 "$MGR" check --snapshot "$JOBDIR/post-release-snap.json" --json \
    | tee "$JOBDIR/post-release-check.json" >/dev/null
  CHECK_RC=${PIPESTATUS[0]}
else
  echo "post-release anthropic check: SKIP (snapshot failed rc=$SNAP_RC)" >&2
fi
```

**exit 语义**（写入 rollout 摘要）：

| snapshot | check | 摘要 verdict | 动作 |
|---|---|---|---|
| 0 | 0 | **green** — `any_violation=false` | 无需 OAuth 流水线 |
| 0 | 1 | **yellow** — live 与 repo baseline 漂移 / operator balance 低 | 打开 `/tokenkey-anthropic-oauth-config`：`plan-*` → `apply --confirm …` → `verify`；TLS/UA 漂移常见路径：`plan-guard-drift-fix` 或 `remediate-guard-drift --sync-runtime` |
| ≠0 或 check 无法跑 | — | **yellow** — SSM/OIDC 只读失败 | 不 rollback 镜像；记 `JOBDIR` 路径，人工补 snapshot/check |

**与 cc 指纹发版的常见联动**（如 #438 类 UA/TLS baseline JSON 已 merge 但 live 未 sync）：

- check 报 `extra_baseline_drift` / `/tls_profile/*` → `remediate-guard-drift`（含 `apply --sync-runtime`）或最小 `sync-runtime --target all-deployable-and-prod`
- check 全绿但 compile default UA 已升 patch → 可选（非默认）跑 `sync-runtime` 让 settings + Redis fingerprint 立刻对齐；**仍不**在本 skill 默认路径自动 apply tier/stub/group 写入

**报告形状**（固定块，贴进 rollout 摘要）：

```text
=== Post-release Anthropic OAuth config check (${NEW_TAG}) ===
snapshot: OK | FAIL rc=N → $JOBDIR/post-release-snap.json
check:    OK | VIOLATION | SKIP → $JOBDIR/post-release-check.json
balance_violations: <from check JSON operator_balance.violation_count>
guard_failures: <count guards with exit_code != 0>
next: none | /tokenkey-anthropic-oauth-config <plan kind>
```

## 发版后 Antigravity 账号配置检查（默认）

**触发**（同时满足）：

- `operation=release`（非 check / smoke-only / rollback）
- 本次请求的 deploy + smoke 已验收通过（与上一段同条件）
- 未显式 `antigravity_config_check=false`

**做什么 / 不做什么**：本段**只读**——逐 deployable edge + prod 经 SSM 读 `platform=antigravity` 账号的 `credentials.model_mapping`，断言每个账号都是 **gemini-only**（不含 `claude-*` / `gpt-oss-*` 键，且 model_mapping 非空——空映射会回退到含 claude 的默认）。不写任何库。后端 `AntigravityConfigReconciler` 已在每个节点启动时 + 周期自愈这条策略（gateway.scheduling.antigravity_config_reconciler_interval_seconds，默认 300s），本检查是发版后的**收敛验证**。

**机械化命令**：

```bash
NEW_TAG="${NEW_TAG:-$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1 | sed 's/^v//')}"
JOBDIR="${JOBDIR:-/tmp/tk-post-release-${NEW_TAG}-$$}"
mkdir -p "$JOBDIR"

python3 ops/antigravity/check-antigravity-account-config.py --json \
  | tee "$JOBDIR/post-release-antigravity-check.json" >/dev/null
AGY_RC=${PIPESTATUS[0]}
```

**exit 语义**（写入 rollout 摘要）：

| rc | 摘要 verdict | 动作 |
|---|---|---|
| 0 | **green** — 所有 antigravity 账号 gemini-only | 无需动作（reconciler 已收敛） |
| 1 | **yellow** — 有账号仍可服务 claude/gpt-oss | 不 rollback 镜像；多为 reconciler 尚未跑到（或被 `antigravity_config_reconciler_interval_seconds<=0` 关闭）。等一个 reconciler 周期后重跑；持续 violation 则查该节点 reconciler 日志 / 配置 |
| 2 | **yellow** — SSM/OIDC 只读失败 | 不 rollback；记 `JOBDIR` 路径，人工补跑 |

**报告形状**（固定块，贴进 rollout 摘要）：

```text
=== Post-release Antigravity account config check (${NEW_TAG}) ===
result: OK | VIOLATION rc=1 | SKIP rc=2 → $JOBDIR/post-release-antigravity-check.json
violation_count: <from JSON .violation_count>
next: none | 等 reconciler 周期后重跑 / 查节点 reconciler 日志
```

## 完成后：rollout 摘要（机械化）

烟测全部完成后，由 `scripts/release-rollout-summary.sh` 渲染统一摘要（与 local-deploy / upstream-merge 共享同一脚本）：

```bash
bash scripts/release-rollout-summary.sh --mode release
# 输出 markdown：Summary / Range / Commits（过滤 bump VERSION + [skip ci]）
#               / Top changed files / Sentinel changes / Upstream file deletions
```

向用户输出：

**本次发版：`${PREV_TAG}` → `${NEW_TAG}`**

| target | workflow | run id | tag | status | smoke |
|---|---|---:|---|---|---|
| edge-<edge_id>-canary | dispatch 脚本 → deploy-edge-lightsail-stage0 | ... | X.Y.Z | success/fail/skipped | full（显式）/ main-via-edge(可选) |
| edge-<edge_id>（其余） | dispatch 脚本 → deploy-edge-lightsail-stage0 | ... | X.Y.Z | success/fail/skipped | infra |
| prod | deploy-stage0 | ... | X.Y.Z | success/fail/skipped | full/partial |
| anthropic-oauth-config | manage-anthropic-config.py | — | — | check OK / violation / skip | snapshot+check（只读） |
| antigravity-account-config | check-antigravity-account-config.py | — | — | gemini-only OK / violation / skip | 逐 edge+prod model_mapping（只读） |

并补充：

- **有效提交**：feat/fix/chore 分类。
- **影响面与验证重点**：Gemini、OpenAI OAuth、pricing/model-list、frontend、sentinel、upstream 删除等按实际变更列出。
- **Anthropic OAuth 配置检查**：§「发版后 Anthropic OAuth 配置检查」固定块；violation 时列 `post-release-check.json` 里的 edge / guard / balance 摘要。
- **未部署或未覆盖目标**：例如某些 edge 仍 `deployable=false`、用户只要求 prod、**main-via-edge: skipped (default)**、缺少 main-gateway-via-edge smoke secret、等待人工审批等。

## release 之后 main 是否还有提交

`release.yml` 可能产生 sync-version 写回提交。流程结束后执行 `git fetch origin main` 即可让后续命令（如 `release-rollout-summary.sh`、下次 `release-bump-and-tag.sh`）看到最新远端；**不要**在共享 checkout 里 `git pull` / 切分支（checkout 可能在并行 agent 的分支上，pull 会把 origin/main 合进去——同 §「决策 + bump + tag」的纪律）。不要手改 `docs/agent_integration.md`；有变更应跑 `python scripts/export_agent_contract.py` 并过 preflight。

## 故障速查

| 现象 | 处理 |
|------|------|
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
| Edge `confirm_stack` mismatch | 停止；检查 `deploy/aws/stage0/edge-targets.json`，不要手改成别的栈名绕过。 |
| Edge smoke 403 | public runner 访问 `/v1/models` 403 是预期；主网关来源 403 才查 `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 与 prod EIP。 |
| main-via-edge smoke HTTP 503 `"no available accounts"` | 先在 prod 上确认对应账号（如 `cc-<edge_id>-oauth`）是否被设为可调度；这是 prod 路由策略，与本次镜像无关。若设计上就不可调度，把这条 smoke 从 hard-fail 降为"infra OK / business-link by design"，**不要 rollback**。若运维想恢复该链路，请按 `/tokenkey-anthropic-oauth-config` 调可调度位再 `dispatch-edge-deploy.sh --operation smoke --smoke-phase main-via-edge` 复验。 |
| 其余 Edge rollout 因 edge-native-oauth 失败（无 schedulable OAuth/Kiro 账号） | 说明该 edge **不应跑 full**——只对 `pick_oauth_canary_edge.py` 选出的 canary 跑 full；其余走 `rollout-edges.sh`（固定 `--smoke-phase infra`）。canary 选 edge 时 count=0 应被脚本跳过；若 canary full 仍失败，查该 edge 账号/scheduling，不要对多个 edge 批量重试 full。 |
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

## 扩展阅读（按需打开）

- `.cursor/skills/tokenkey-anthropic-oauth-config/SKILL.md` — 发版后 check violation 的 plan/apply/verify canonical 路径。

- `scripts/release-bump-and-tag.sh` — release 全步骤（worktree；默认 direct-push，fallback 才 delegate PR）。
- `scripts/release-bump-via-pr.sh` — VERSION bump 经 PR + merge + tag。
- `scripts/release-configure-main-bypass.sh` — scheme 1：发版账号 bypass（个人 repo / 组织 repo 双路径）。
- `scripts/release-main-push-route.sh` — direct-push vs bump-via-pr 探测。
- `scripts/stage0/approve-github-run-env.sh` — Environment 门禁自批（warm / prod / edge）。
- `scripts/release-decide-version.sh` — VERSION/tag 三态决策。
- `scripts/release-tag.sh` — tag 门禁。
- `.github/workflows/release.yml` — multi-arch image build 与 prod auto-dispatch。
- `scripts/stage0/rollout-edges.sh` — 其余 Edge bounded-parallel rollout（fail-stop + smoke 标记验收；**默认 `--parallel 1` 顺序**，降低并发换容器对线上的影响；`N>1` 仅在可接受时用）。
- `scripts/stage0/pick_oauth_canary_edge.py` — 按 deployable 顺序选第一个有 native OAuth/Kiro 池的 Edge 作 canary full smoke。
- `ops/stage0/edge_oauth_pool_probe.sh` — canary 选择用的 SSM 账号池计数探针（与 edge-native smoke 同 eligibility）。
- `scripts/stage0/dispatch-edge-deploy.sh` — 单一 Edge deploy dispatch（edges 均为 Lightsail）。
- `ops/observability/probe-release-control-plane.sh` — 发版后控制面探活（prod + deployable Edge，JSON lines + summary）。
- `ops/observability/probe-post-release-tick.sh` — 发版后 tick 探针（blue/green active container auto + HOOK_PATTERNS 计数 + 流量/5xx/panic）。
- `scripts/stage0/resolve-edge-deploy-route.py` — Edge → workflow + confirm 参数。
- `.github/workflows/deploy-stage0.yml` — prod deploy。
- `.github/workflows/deploy-edge-lightsail-stage0.yml` — Lightsail Edge deploy（edges 唯一路径）。
- `ops/stage0/post_deploy_smoke.sh` — prod 完整 smoke（CI canonical）。
- `ops/stage0/edge_post_deploy_smoke.sh` — Edge smoke（infra / edge-native-oauth / main-via-edge / full）。
- `deploy/aws/README.md` — Stage0、Edge、多区域升级 SOP。
- `.github/workflows/ops-stage0-pg-dump-refresh.yml` + `ops/stage0/pg_dump_refresh_via_ssm.sh` — in-place 同步 `deploy/aws/cloudformation/stage0-single-ec2.yaml` 里的 `tokenkey-pgdump.*` systemd unit 到 live 实例（不重建 EC2）；下次有类似 user-data 模板改动可参考此形状写一个 one-shot ops workflow。
- `.github/workflows/ops-stage0-host-mem-guard.yml` + `ops/stage0/sync-host-mem-guard-via-ssm.sh` — 同形状的 one-shot：把 #811 的 `/swapfile` 释放阀 + sysctl + `tokenkey-disk-metrics.sh` 内存压力告警从 `stage0-ec2-bootstrap.sh` 运行时抽取（单一源）推到 live prod（不重建 EC2，prod-only）。**发版本身不会落地这批 infra 改动**（deploy 只换镜像、不跑 bootstrap）——改了 bootstrap 的 swap/内存防御后，要么等下次换机，要么 dispatch 此 workflow 立刻生效。
