---
name: tokenkey-stage0-release-rollout
description: >-
  Drives TokenKey AWS Stage0 release and rollout across prod and Edge targets:
  sync main, decide VERSION/tag, run scripts/release-tag.sh, watch release.yml,
  deploy prod via deploy-stage0.yml, deploy/smoke deployable Edge targets
  (dynamic edge matrix from deploy/aws/stage0/edge-targets.json, EC2 or Lightsail
  via scripts/stage0/dispatch-edge-deploy.sh) via platform-routed workflows,
  report structured smoke results, or run a pre-release check of code facts and
  production impact risk.
  Use when the user asks to release, deploy, smoke, rollback, check release
  risk, or roll out to prod, Edge regions, or all Stage0 targets.
---

# TokenKey：Stage0 release → prod/Edge rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。

| 步骤 | 类型 | 承载 |
|---|---|---|
| VERSION/tag 三态决策（tag-only / bump-and-tag / skip-bump-skip-tag） | 机械 | `scripts/release-decide-version.sh [--emit-suggested-bump]` |
| 打 tag（含 skip-ci / VERSION 一致 / 分支 / sync 校验） | 机械 | `scripts/release-tag.sh vX.Y.Z` |
| 读取 deployable edge 矩阵 | 机械 | `python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable` |
| Edge dispatch 路由（EC2 vs Lightsail） | 机械 | `scripts/stage0/resolve-edge-deploy-route.py --edge-id <id> --json` |
| Edge upgrade/smoke/rollback dispatch | 机械 | `bash scripts/stage0/dispatch-edge-deploy.sh --edge-id … --operation …` |
| dispatch release.yml / deploy-stage0.yml + watch | 机械 | `gh workflow run` + `gh run watch --exit-status` |
| prod 完整 smoke（CI 唯一验收源） | 机械 | `deploy-stage0.yml` job log 内 `tk_post_deploy_smoke: OK`（`GATEWAY_SMOKE_SUITE=full`） |
| Edge smoke 分阶段（infra / main-via-edge / full） | 机械 | `ops/stage0/edge_post_deploy_smoke.sh` + workflow `smoke_phase`；main-via-edge 用 `GATEWAY_SMOKE_SUITE=main-via-edge`（/v1/messages + Claude UA，不走 chat） |
| 发版前 smoke 模型校验 | 机械 | `python3 scripts/stage0/check_smoke_config.py`（`TK_SMOKE_PROD_ANTHROPIC_MODEL` ∈ `/v1/models`） |
| 发版后跟进档位（skip / single / extended） | 机械 | `bash scripts/release-impact-files.sh PREV NEW` → `.followup.tier` |
| rollout 摘要（git log / diff stat / sentinel / deletion） | 机械 | `bash scripts/release-rollout-summary.sh --mode release` |
| canary 顺序、prod approval 时机、smoke 模型回退 | 判断 | prompt（爆炸半径、用户入口顺序） |
| verdict 评级（green/yellow/red） | 判断 | prompt（错误聚类 vs 基线、流量趋势） |
| Step A → 「重点观察 trace 关键词」语义命名 | 判断 | prompt（文件→hook 名映射；脚本只给文件桶） |
| `simple_release=true` / `[skip ci]` 等 hard rules | 判断 + 机械门禁 | prompt + `scripts/release-tag.sh` / preflight |

## 调用参数

本 skill 默认按用户语义解析；用户未写完整参数时，先按下面语义补全，仍有歧义再问。

```text
/tokenkey-stage0-release-rollout target=<prod|edge-<edge_id>|all> [tag=X.Y.Z] [operation=<check|release|deploy|smoke|rollback>] [previous_tag=X.Y.Z]
```

| 参数 | 语义 |
|---|---|
| `operation=check` | 只做预发布风险检查：对比上一个 release tag 到待发布 HEAD 的代码事实，判断上线 prod/Edge 的潜在影响；不 bump、不 tag、不 dispatch deploy。 |
| `target=prod` | release（必要时 bump/tag/build）→ `deploy-stage0.yml -f tag=…`（绑定 **`prod`** Environment）→ prod smoke。 |
| `target=edge-<edge_id>` | 默认 tag 已存在：用 **`bash scripts/stage0/dispatch-edge-deploy.sh`**（自动路由 EC2/Lightsail）→ watch → 按 phase 验收 smoke。`operation=smoke` 只 smoke；`operation=rollback` 用 `previous_tag`。不要手选 workflow 或手填 confirm_stack/confirm_instance。 |
| `target=all` | release 一次 → `--list-deployable` 矩阵 → canary **upgrade (infra)** → prod deploy（CI smoke 验收）→ canary **main-via-edge 一次** → 其余 Edge **upgrade (infra only)** → followup 按 tier。 |

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
4. 单独确认发布/部署契约是否变化：检查 `release.yml`、`deploy-stage0.yml`、`deploy-edge-stage0.yml`、Dockerfile、`backend/go.mod` / `go.sum`、`frontend/package.json` / lockfile、`backend/cmd/server/VERSION`、`deploy/` 是否有 diff。
5. 按运行时影响面读代码事实，而不是只看提交标题：
   - 后端请求路径：gateway、auth、rate limit、scheduler、quota/billing、model-list/pricing、newapi bridge、middleware。
   - 前端线上路径：登录/注册、admin settings、API client、嵌入 dist freshness。
   - 数据层：Ent schema、migrations、repository、默认设置初始化。
   - 运维面：release/deploy workflow、Stage0 SSM primitive、Caddy/compose、smoke scripts、ops workflow。
   - 上游隔离：是否删除 upstream-owned 文件/route/method，是否触发 sentinel registry。
6. 跑本地门禁：执行 `bash scripts/preflight.sh`；失败必须进入风险结论，不能给出“可放心发布”的结论。
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
2. **Edge canary：只取 deployable 矩阵第一个 Edge upgrade + infra smoke**：先在低成本、非用户入口的资源节点验证镜像能在 Graviton/Stage0/共享 compose 上启动，并验证 `/health`、Caddy allowlist、Edge self-smoke。
3. **prod 主网关 upgrade + 完整 prod smoke**：主网关是唯一用户入口、计量计费面和体验中心；Edge canary 过后再升级 prod。
4. **main gateway via Edge smoke**：prod 升级后再跑主网关经这个已通过 canary 的 Edge 业务 smoke，确认 `api.tokenkey.dev` 调度到 Edge 的真实链路。
5. **其余 deployable Edge 顺序 rollout**：按矩阵逐个 **upgrade（infra smoke）**；失败即停。**main-via-edge 只在 canary 做一次**（见 `target=all` 执行顺序）。

例外：

- `target=prod`：只发版/部署 prod，不自动部署 Edge。
- `target=edge-<edge_id>`：只升级/烟测对应 Edge，不发新 release，除非用户显式要求先 release。
- 用户强指定“prod 先”时照做，但在摘要中标出与默认 canary 顺序的差异。

## 一次性跑完（原则）

- **顺序做完**：同步 → 决策 VERSION/tag →（按需 bump+push）→ `release-tag.sh` → **watch 到 release 成功** → 根据 `target` dispatch 对应 deploy workflow → **watch 到 deploy/smoke 成功** → **再做本地/日志验收**。不要在 workflow 绿灯后就结束会话。
- **读 VERSION 前必须先** `fetch` + `pull --ff-only` **后的** `origin/main` 为准；在未更新的本地分支上读 `VERSION` 会与远端 tag 错位。
- **`gh run watch` 要给够时间**：多架构 `release.yml` 常见十余分钟量级；Agent 应用 `--exit-status` 跟跑到结束，不要用默认短超时提前杀掉。
- **Environment approval 不是失败**：`prod` / `edge-<edge_id>` run 卡在 `waiting` 时，需要人在 GitHub Actions 批准；批准后继续 watch。
- **完整烟测密钥**：prod 完整 smoke 以 **CI `deploy-stage0` job log** 为唯一验收源；Edge main-via-edge 依赖 **`TK_SMOKE_EDGE_CANARY_KEY`**（canary 一次）。
- **部署前先校验 OIDC 目标实例**：`tokenkey-cicd-oidc` 的 `TargetInstanceId` 必须等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致会在 `Deploy via SSM Run-Command` 直接 `AccessDenied(ssm:SendCommand)`。
- **禁止 `simple_release_override=true`**：prod / Edge 当前都跑 AWS Graviton arm64；单架构 manifest 会导致 `exec format error`。
- **`gh` 连接抖动先做无代理重试**：若连续出现 `read ... 127.0.0.1:7890: connection reset by peer`，用一次性环境变量重试：`env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh ...`，恢复后再继续 watch/dispatch。
- **无代理模式要校验 gh 身份**：去掉 `GH_TOKEN` 或 proxy 后，`gh` 可能切到别的账号；dispatch 前必须 `gh auth status` 确认 active account 是目标仓库有权限的账号，避免 `HTTP 403 Must have admin rights to Repository`。

## 前置条件

- 工作目录：仓库根目录（含 `backend/`、`scripts/release-tag.sh`）。
- 网络、`git`、`gh` 已认证且对远端可写；`gh` 能 dispatch `release.yml`、`deploy-stage0.yml`、`deploy-edge-stage0.yml`。
- GitHub Environment：**`prod`**、各 Edge 的 `edge-<edge_id>`（若有 Required reviewers，需人工批准）。新 edge 可参考已上线 edge 的变量/密钥结构，但 `EDGE_GHCR_PAT_SSM_NAME` 必须使用该 edge 自己的 SSM 路径。
- **禁止**：VERSION bump / 发版 commit 的正文里出现字面量 `[skip ci]` 或 `[ci skip]`（任意位置都不行）。

## 决策：要不要升 patch 版本（机械化）

`scripts/release-decide-version.sh` 输出 `action=tag-only|bump-and-tag|skip-bump-skip-tag` + `current_version=…` + `current_tag=…` + `reason=…`。模型直接消费这一段，不再现场比对 `git ls-remote` / VERSION 文件。

```bash
git fetch origin main --tags --quiet
git checkout main && git pull origin main --ff-only
bash scripts/release-decide-version.sh --emit-suggested-bump
```

按 action 路由：

- `action=tag-only` → 直接 `bash scripts/release-tag.sh "$(cut -d= -f2 <<<\"$(grep ^current_tag …)\")"`
- `action=bump-and-tag` → 把 `backend/cmd/server/VERSION` 改为脚本输出的 `suggested_next_version`，单提交 `chore: bump VERSION to X.Y.Z`（**禁止** `[skip ci]` 字面量）→ push → 再跑 `release-decide-version.sh`，转 `action=tag-only`
- `action=skip-bump-skip-tag` → 跳过 release，直接走 deploy（同一镜像）

`release-tag.sh` 自身也机械化校验 skip-ci / VERSION 一致 / 分支 / sync —— 不要手 `git tag`。

## 标准流程：release 新镜像

1. **同步 main**（同上）。
2. **如需 bump**：改 `backend/cmd/server/VERSION` → 单提交 `chore: bump VERSION to X.Y.Z`（无任何 skip-ci 字样）→ `git push origin main`。
3. **打 tag**（必须在 `main` 且与 `origin/main` 同 SHA）：

   ```bash
   bash scripts/release-tag.sh vX.Y.Z
   ```

   不要手打 `git tag`；脚本校验 skip-ci、VERSION 一致、分支与同步。

4. **等待镜像**：`gh run list --workflow=release.yml --limit 1` → 取刚触发、与本次 tag 对应的 run → `gh run watch <id> --exit-status`，直到 success。
5. 记录 `TARGET_TAG=X.Y.Z`（tag 不带 `v`），后续 prod / Edge deploy 都用这一份 image。

## 部署目标矩阵

### prod：主网关

`release.yml` 成功后会自动 dispatch `deploy-stage0.yml -f tag=<VERSION>`（由 `github-actions[bot]` 触发；job 固定绑定 **`prod`** Environment）。先检查，避免重复 dispatch：

```bash
gh run list --workflow=deploy-stage0.yml --limit 3 --json databaseId,actor,createdAt,status,displayTitle
```

- Bot 已自动触发，且 `createdAt` 在 release 完成后：直接 `gh run watch <id> --exit-status`。
- 未自动触发：手动 dispatch（tag 不带 `v`）：

```bash
gh workflow run deploy-stage0.yml \
  -f tag="$TARGET_TAG"
```

**target=all 注意**：如果 release 已自动 queue prod，而 Edge canary 尚未完成，不取消 prod run；若它卡在 `prod` Environment approval，先不要批准，等第一个 deployable Edge canary 成功后再批准。若 prod 已开始，继续完成，不强行中断，并在摘要标记“prod 已先行”。

### edge-<edge_id>：Edge 资源节点（EC2 或 Lightsail，单一 dispatch 入口）

以 `resolve-edge-target.py --list-deployable` 为准（已合并 EC2 ∪ Lightsail，Lightsail `deployable=true` 优先）。**禁止**手选 `deploy-edge-stage0.yml` vs `deploy-edge-lightsail-stage0.yml`——一律走 dispatch 脚本：

```bash
TARGET_TAG=X.Y.Z
EDGE_ID=<edge_id>

# upgrade（默认 smoke_phase=infra）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation upgrade \
  --tag "$TARGET_TAG"

# smoke only（默认 smoke_phase=full；rollout 中按需显式传 phase）
bash scripts/stage0/dispatch-edge-deploy.sh \
  --edge-id "$EDGE_ID" \
  --operation smoke \
  --smoke-phase infra          # 或 main-via-edge | full

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
   - a. `curl https://api.tokenkey.dev/health` 与 `/api/v1/settings/public` 期望 HTTP 200。
   - b. 可选：`gh workflow run ops-daily-diagnostics.yml -f operation=diagnostics -f target_selector=prod -f diagnostics_log_since=20m`（或只读查最近错误聚类），确认 anthropic/openai/gemini 账号池非空、无新 cluster。
   - c. 向运维确认：各 deployable Edge 在 prod 端是否**预期可调度**；若刻意不可调度，canary 的 main-via-edge 503 `"no available accounts"` 记为 **by design**，不触发 rollback。
1. 完成「标准流程：release 新镜像」，得到 `TARGET_TAG`。
2. 读取 deployable 矩阵：

   ```bash
   python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable
   # EC2 ∪ Lightsail 合并；Lightsail deployable=true 优先（uk1、us2、us3、us4、…）
   ```

3. 取矩阵**第一个** deployable Edge 作为 canary：`dispatch-edge-deploy.sh --operation upgrade --tag=$TARGET_TAG`（**infra smoke**，workflow 默认 `smoke_phase=infra`），watch 到 success。
4. 推进 prod deploy：优先使用 release 自动 queue 的 prod run；没有则手动 dispatch。watch 到 success。
5. **prod 验收（CI 唯一源）**：在本次 `deploy-stage0` run log 搜索 `tk_post_deploy_smoke: OK`，并核对 models/chat/messages 等 shape（见「prod 真实测试」§A）。**不要**在本地再跑完整 `post_deploy_smoke.sh`，除非 CI secret 缺失或日志不可解析。
6. **canary main-via-edge（仅此一次）**：`dispatch-edge-deploy.sh --operation smoke --smoke-phase main-via-edge`（prod 已升级后）。缺 **`TK_SMOKE_EDGE_CANARY_KEY`** 则标记 partial，不 rollback。
7. **其余 deployable Edge**：逐个 `upgrade`（infra smoke only）；**不再**对每个 edge 跑 main-via-edge。
   - **main-via-edge canary** 仍通常仅 **uk1 / us1**（需 `TK_SMOKE_EDGE_CANARY_KEY`）。
   - **us2 / us3 / us4** 等 Lightsail-only edge：rollout 以 infra smoke + 可选 `curl https://api-<id>.tokenkey.dev/health` 为准；勿因缺 canary secret 判失败。
8. **发版后跟进（按 diff 档位，非固定三轮）**：跑 `release-impact-files.sh` 读 `.followup.tier`：
   - `skip` → 不跟进，直接 rollout summary。
   - `single` → 仅 **+5min** 一次轻量诊断。
   - `extended` → **+5 / +10 / +15min** 三轮（gateway/schema/config 类变更）。

## prod 真实测试

部署 workflow 成功只说明流水线过了；**prod 完整网关烟测以 CI 为唯一 canonical 验收**（Jobs：一条路径一个意图）。

### A — CI 日志中的完整网关烟测（默认且充分）

在本次 `deploy-stage0` run log 里搜索 `tk_post_deploy_smoke: OK`，并确认：

- `/v1/models`：`object=list` 且 `data` 非空。
- `/v1/chat/completions`：`object=chat.completion`，`choices[]` 非空，`usage` 存在。
- `/v1/messages`：`type=message`，`role=assistant`，`content[]` 有文本，`usage` 存在。
- Gemini / OpenAI OAuth 探针：workflow 已配置三个 secret 时应在 log 中出现对应 section（软警告 vs 硬失败见 `post_deploy_smoke.sh`）。

若 CI 因 Anthropic model 不在 key 的 `/v1/models` 列表失败：改 **`prod`** Environment 的 **`TK_SMOKE_PROD_ANTHROPIC_MODEL`** 后 **重跑 deploy-stage0**。

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
export TK_SMOKE_PROD_ANTHROPIC_KEY=sk-...    # secret 值 GitHub API 不可读，须本机 export 同名
export TK_SMOKE_PROD_GEMINI_KEY=sk-...
export TK_SMOKE_PROD_OPENAI_OAUTH_KEY=sk-...
bash ops/stage0/post_deploy_smoke.sh
```

校验 GitHub Environment 配齐（不跑 HTTP）：`bash ops/stage0/load_smoke_github_env.sh --check prod`

通过标准同 `ops/stage0/post_deploy_smoke.sh` 文档；缺 key 不得声称 prod 完整验收通过。

## Smoke 架构（单一 runner + suite）

| 脚本 | 角色 |
|---|---|
| `ops/stage0/post_deploy_smoke.sh` | **唯一** gateway 业务烟测 runner；`GATEWAY_SMOKE_SUITE` 控制探针子集 |
| `ops/stage0/smoke_lib.sh` | 共享 soft-degrade、model pick、suite gating |
| `ops/stage0/edge_post_deploy_smoke.sh` | Edge **infra** SSM + **main-via-edge** 编排（调用 post_deploy） |
| `ops/stage0/gateway_smoke.sh` | 手动 quick 探活（有 key 时 `GATEWAY_SMOKE_SUITE=quick` 委托 post_deploy） |
| `scripts/stage0/check_smoke_config.py` | 发版前校验 `TK_SMOKE_PROD_ANTHROPIC_MODEL` ∈ `/v1/models` |
| `ops/stage0/smoke_env.sh` | GitHub `TK_SMOKE_*` 默认值与导出（`TK_SMOKE_GITHUB_ENV` 触发 gh 拉 variables） |
| `ops/stage0/load_smoke_github_env.sh` | 从 GitHub Environment 拉 `TK_SMOKE_*` variables；`--check` 校验 secrets/vars 已配置 |

**GitHub Environment 配置（canonical `TK_SMOKE_*`）** — 详见 `deploy/aws/README.md` § Smoke config：

| Environment | Secrets | Vars |
|---|---|---|
| **`prod`** | `TK_SMOKE_PROD_ANTHROPIC_KEY`, `TK_SMOKE_PROD_GEMINI_KEY`, `TK_SMOKE_PROD_OPENAI_OAUTH_KEY` | `TK_SMOKE_PROD_ANTHROPIC_MODEL`, `TK_SMOKE_PROD_GEMINI_MODEL`, `TK_SMOKE_PROD_OPENAI_OAUTH_MODEL` |
| **`edge-<id>`** | `TK_SMOKE_EDGE_CANARY_KEY` | —（base URL 与 local model 见下） |

**Edge smoke 固定值（代码内）：** `TK_SMOKE_EDGE_CANARY_BASE_URL=https://api.tokenkey.dev`，`TK_SMOKE_EDGE_LOCAL_CHAT_MODEL=claude-sonnet-4-6`。

**Suite 矩阵**：

| `GATEWAY_SMOKE_SUITE` | 何时 | 探针 |
|---|---|---|
| `full`（默认） | prod `deploy-stage0` | public + frontend + models + chat + messages + gemini/oauth（key 配置时） |
| `main-via-edge` | canary `smoke_phase=main-via-edge` | public + models + `/v1/messages`（Claude Code UA）；**跳过 chat**（Edge-only key 常限制 `/v1/messages`） |
| `quick` | 本地 `gateway_smoke.sh` | public + models + chat |

**模型 override**：`TK_SMOKE_PROD_ANTHROPIC_MODEL` 不在 key 的 `/v1/models` 时 **warn + 自动回退**（不再 hard fail）；发版前仍应用 `check_smoke_config.py` 拦截配置漂移。

## Edge smoke 验收

Edge workflow 在 `external_health.sh` 之后调用 `edge_post_deploy_smoke.sh`，并传 `SKIP_EXTERNAL_HEALTH=1`（避免重复外网 `/health`）。

**分阶段（`EDGE_SMOKE_PHASE` / workflow `smoke_phase`）**：

| phase | 何时用 | 验证内容 |
|---|---|---|
| `infra` | 每个 Edge **upgrade/rollback** 默认 | 公网 runner `/v1/models`→403；SSM 内 compose ps + localhost health；可选 Edge 本地 api self-smoke |
| `main-via-edge` | **仅 canary**，且 **prod 已升级后** | `GATEWAY_SMOKE_SUITE=main-via-edge` 经 prod 主网关打到 Edge + Edge 日志确认 |
| `full` | 单独 `operation=smoke` 且未指定 phase 时 | infra + main-via-edge |

验收 checklist：

- `infra`：workflow log 含 `tk_edge_post_deploy_smoke: OK phase=infra`。
- `main-via-edge`：log 含 main gateway smoke 成功或明确 skip（缺 key）；日志确认 optional warning 可接受。
- 非 canary Edge：**只需 infra**；不得因缺少 main-via-edge 判 fail。

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

## 完成后：发版后跟进（按 diff 档位）

**不要**每次发版固定 +5/+10/+15min 三轮。先机械化分档：

```bash
PREV_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | sed -n '2p')
NEW_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1)
bash scripts/release-impact-files.sh "${PREV_TAG}" "${NEW_TAG}"
# 读 JSON .followup.tier → skip | single | extended
```

| tier | 动作 |
|---|---|
| `skip` | 不跟进；直接 rollout summary |
| `single` | **仅 +5min** 一次轻量诊断 |
| `extended` | **+5 / +10 / +15min** 三轮 |

### Step A：重点观察变量（extended / single 时）

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

1. **控制面探活**：prod `/health` + `/api/v1/settings/public`；各 deployable Edge `/health`。
2. **错误 + 流量快照**（最近 5m）：用 `/tokenkey-online-log-troubleshooting` 查聚类摘要。
3. **重点 hook grep**（extended 时；single 可只做 1–2 条）
   - 没看到触发 = 正常（流量未触达该路径）；触发频次明显高于基线 = 异常；触发后立即 5xx 跟随 = 异常

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

### Step D：最后一次 tick 后的综合建议

- **`single`**：+5min 一次 tick 后立即给综合建议（1 tick）。
- **`extended`**：第 3 次（+15min）tick 后立即给综合建议（3 ticks）。
- **`skip`**：跳过本节。

综合建议结构（按 tier 调整 tick 计数文案）：

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

- **`single`**：最后一个 smoke 通过后 **+5min** 一次，green 即结束。
- **`extended`**：+5 / +10 / +15min 三次；任意 red → 停后续 tick。
- 更长窗口（+1h / +6h）仅人工显式发起；会话内不要自行无限延期。

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
| edge-<edge_id>-canary（每个 deployable edge 一行） | dispatch 脚本 → EC2/Lightsail workflow | ... | X.Y.Z | success/fail/skipped | infra / main-via-edge(仅 canary) |
| prod | deploy-stage0 | ... | X.Y.Z | success/fail/skipped | full/partial |

并补充：

- **有效提交**：feat/fix/chore 分类。
- **影响面与验证重点**：Gemini、OpenAI OAuth、pricing/model-list、frontend、sentinel、upstream 删除等按实际变更列出。
- **未部署或未覆盖目标**：例如某些 edge 仍 `deployable=false`、用户只要求 prod、缺少 main-gateway-via-edge smoke secret、等待人工审批等。

## release 之后 main 是否还有提交

`release.yml` 可能产生 sync-version 写回提交。流程结束后执行 `git fetch origin main`，若本地落后则 `git pull --ff-only`。不要手改 `docs/agent_integration.md`；有变更应跑 `python scripts/export_agent_contract.py` 并过 preflight。

## 故障速查

| 现象 | 处理 |
|------|------|
| `release-tag.sh` 报 HEAD 含 skip-ci 标记 | 修改触发打 tag 的最近一次提交说明后重试，或按 `CLAUDE.md` 用 `gh workflow dispatch` 触发 `release.yml`。 |
| `tag already exists on origin` | 升 `VERSION` 再打新 tag，或仅 dispatch deploy 已有 tag。 |
| deploy 报单架构 manifest | 重新跑 `release.yml` 且 `simple_release=false`；prod / Edge 都不要 override。 |
| 出现两个并行 prod deploy run | `release.yml` 已自动触发，不要再手动 dispatch；取消多余手动 run，watch Bot run。 |
| target=all 但 prod run 已自动 queue | 若在 Environment waiting，先等 Edge canary 成功再批准；若已开始，不中断，完成后摘要标记 prod 先行。 |
| Edge `confirm_stack` mismatch | 停止；检查 `deploy/aws/stage0/edge-targets.json`，不要手改成别的栈名绕过。 |
| Edge smoke 403 | public runner 访问 `/v1/models` 403 是预期；主网关来源 403 才查 `EDGE_MAIN_GATEWAY_ALLOWED_CIDR` 与 prod EIP。 |
| main-via-edge smoke HTTP 503 `"no available accounts"` | 先在 prod 上确认对应账号（如 `cc-<edge_id>-oauth`）是否被设为可调度；这是 prod 路由策略，与本次镜像无关。若设计上就不可调度，把这条 smoke 从 hard-fail 降为"infra OK / business-link by design"，**不要 rollback**。若运维想恢复该链路，请按 `/tokenkey-anthropic-oauth-config` 调可调度位再 `dispatch-edge-deploy.sh --operation smoke --smoke-phase main-via-edge` 复验。 |
| `gh run watch` 被工具超时打断 | 用同一 run id 再执行 `gh run watch <id> --exit-status` 接到终态。 |
| prod `Deploy via SSM Run-Command` 报 `AccessDenied(ssm:SendCommand)` | 先核对 `tokenkey-cicd-oidc` 的 `TargetInstanceId` 是否等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致先更新 OIDC 栈参数再重跑 deploy。 |
| prod smoke 报 Anthropic model not listed in GET /v1/models | 不是代码回归，改 **`prod`** Environment 的 **`TK_SMOKE_PROD_ANTHROPIC_MODEL`** 为该 key 可见模型后重跑。 |
| `gh` 请求持续报 `read ... 127.0.0.1:7890: connection reset by peer` | 先用 `env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh <cmd>` 做无代理重试；恢复后再继续 watch。 |
| 无代理后 dispatch 报 `HTTP 403 Must have admin rights to Repository` | `gh` 可能切到另一个账号；先 `env -u GH_TOKEN ... gh auth status`，必要时 `gh auth switch -u <repo-owner>` 后重试 dispatch。 |

## 扩展阅读（按需打开）

- `scripts/release-tag.sh` — tag 门禁。
- `.github/workflows/release.yml` — multi-arch image build 与 prod auto-dispatch。
- `scripts/stage0/dispatch-edge-deploy.sh` — 单一 Edge deploy dispatch（EC2/Lightsail 自动路由）。
- `scripts/stage0/resolve-edge-deploy-route.py` — Edge → workflow + confirm 参数。
- `.github/workflows/deploy-stage0.yml` — prod deploy。
- `.github/workflows/deploy-edge-stage0.yml` — EC2 Edge deploy。
- `.github/workflows/deploy-edge-lightsail-stage0.yml` — Lightsail Edge deploy。
- `ops/stage0/post_deploy_smoke.sh` — prod 完整 smoke（CI canonical）。
- `ops/stage0/edge_post_deploy_smoke.sh` — Edge smoke（infra / main-via-edge / full）。
- `deploy/aws/README.md` — Stage0、Edge、多区域升级 SOP。
- `.github/workflows/ops-stage0-pg-dump-refresh.yml` + `ops/stage0/pg_dump_refresh_via_ssm.sh` — in-place 同步 `deploy/aws/cloudformation/stage0-single-ec2.yaml` 里的 `tokenkey-pgdump.*` systemd unit 到 live 实例（不重建 EC2）；下次有类似 user-data 模板改动可参考此形状写一个 one-shot ops workflow。
