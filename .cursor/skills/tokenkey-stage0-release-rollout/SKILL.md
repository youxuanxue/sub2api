---
name: tokenkey-stage0-release-rollout
description: >-
  Drives TokenKey AWS Stage0 release and rollout across prod and Edge targets:
  sync main, decide VERSION/tag, run scripts/release-tag.sh, watch release.yml,
  deploy prod via deploy-stage0.yml, deploy/smoke deployable Edge targets
  (dynamic edge matrix from deploy/aws/stage0/edge-targets.json) via
  deploy-edge-stage0.yml, report structured smoke results, or run a pre-release
  check of code facts and production impact risk.
  Use when the user asks to release, deploy, smoke, rollback, check release
  risk, or roll out to prod, Edge regions, or all Stage0 targets.
---

# TokenKey：Stage0 release → prod/Edge rollout → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 调用参数

本 skill 默认按用户语义解析；用户未写完整参数时，先按下面语义补全，仍有歧义再问。

```text
/tokenkey-stage0-release-rollout target=<prod|edge-<edge_id>|all> [tag=X.Y.Z] [operation=<check|release|deploy|smoke|rollback>] [previous_tag=X.Y.Z]
```

| 参数 | 语义 |
|---|---|
| `operation=check` | 只做预发布风险检查：对比上一个 release tag 到待发布 HEAD 的代码事实，判断上线 prod/Edge 的潜在影响；不 bump、不 tag、不 dispatch deploy。 |
| `target=prod` | release（必要时 bump/tag/build）→ `deploy-stage0.yml -f tag=…`（绑定 **`prod`** Environment）→ prod smoke。 |
| `target=edge-<edge_id>` | 默认 tag 已存在：`deploy-edge-stage0.yml edge_id=<edge_id> operation=upgrade` → Edge smoke；`operation=smoke` 只 smoke；`operation=rollback` 用 `previous_tag`。`confirm_stack` 按矩阵取 `tokenkey-edge-<edge_id>-stage0`。 |
| `target=all` | release 一次 → 从 `deploy/aws/stage0/edge-targets.json` 读取 `deployable=true` 的 Edge 矩阵并按顺序 canary upgrade/smoke → prod deploy/smoke → main-gateway-via-edge smoke → 其余 Edge 类推。 |

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
5. **其余 deployable Edge 顺序 rollout**：按 `deploy/aws/stage0/edge-targets.json` 顺序逐个 upgrade + smoke，失败即停。

例外：

- `target=prod`：只发版/部署 prod，不自动部署 Edge。
- `target=edge-<edge_id>`：只升级/烟测对应 Edge，不发新 release，除非用户显式要求先 release。
- 用户强指定“prod 先”时照做，但在摘要中标出与默认 canary 顺序的差异。

## 一次性跑完（原则）

- **顺序做完**：同步 → 决策 VERSION/tag →（按需 bump+push）→ `release-tag.sh` → **watch 到 release 成功** → 根据 `target` dispatch 对应 deploy workflow → **watch 到 deploy/smoke 成功** → **再做本地/日志验收**。不要在 workflow 绿灯后就结束会话。
- **读 VERSION 前必须先** `fetch` + `pull --ff-only` **后的** `origin/main` 为准；在未更新的本地分支上读 `VERSION` 会与远端 tag 错位。
- **`gh run watch` 要给够时间**：多架构 `release.yml` 常见十余分钟量级；Agent 应用 `--exit-status` 跟跑到结束，不要用默认短超时提前杀掉。
- **Environment approval 不是失败**：`prod` / `edge-<edge_id>` run 卡在 `waiting` 时，需要人在 GitHub Actions 批准；批准后继续 watch。
- **完整烟测密钥**：优先使用环境里已有的 smoke key，避免停下来向用户索要 key；详见 prod smoke 与 Edge smoke 章节。
- **部署前先校验 OIDC 目标实例**：`tokenkey-cicd-oidc` 的 `TargetInstanceId` 必须等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致会在 `Deploy via SSM Run-Command` 直接 `AccessDenied(ssm:SendCommand)`。
- **禁止 `simple_release_override=true`**：prod / Edge 当前都跑 AWS Graviton arm64；单架构 manifest 会导致 `exec format error`。
- **`gh` 连接抖动先做无代理重试**：若连续出现 `read ... 127.0.0.1:7890: connection reset by peer`，用一次性环境变量重试：`env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh ...`，恢复后再继续 watch/dispatch。
- **无代理模式要校验 gh 身份**：去掉 `GH_TOKEN` 或 proxy 后，`gh` 可能切到别的账号；dispatch 前必须 `gh auth status` 确认 active account 是目标仓库有权限的账号，避免 `HTTP 403 Must have admin rights to Repository`。

## 前置条件

- 工作目录：仓库根目录（含 `backend/`、`scripts/release-tag.sh`）。
- 网络、`git`、`gh` 已认证且对远端可写；`gh` 能 dispatch `release.yml`、`deploy-stage0.yml`、`deploy-edge-stage0.yml`。
- GitHub Environment：**`prod`**、各 Edge 的 `edge-<edge_id>`（若有 Required reviewers，需人工批准）。新 edge 可参考已上线 edge 的变量/密钥结构，但 `EDGE_GHCR_PAT_SSM_NAME` 必须使用该 edge 自己的 SSM 路径。
- **禁止**：VERSION bump / 发版 commit 的正文里出现字面量 `[skip ci]` 或 `[ci skip]`（任意位置都不行）。

## 决策：要不要升 patch 版本

1. `git fetch origin main --tags && git checkout main && git pull origin main --ff-only`
2. 读已与 `origin/main` 对齐的 `backend/cmd/server/VERSION`（记为 `V`，无 `v` 前缀）。
3. 用 `git ls-remote --tags origin "refs/tags/v${V}"` 判断远端是否已有 `v${V}`：
   - **`v${V}` 尚不存在**：若 `main` 已含正确 `VERSION=V` 且已 push，可直接 `bash scripts/release-tag.sh v${V}`，无需 bump。
   - **`v${V}` 已存在**，且 `origin/main` 比该 tag 更新：须把 `VERSION` 升到下一 patch，提交并 push，再对新版本执行 `release-tag.sh`。禁止复用已有远端 tag。
   - **`origin/main` 与 `v${V}` 同一 commit**，仅某目标未部署该镜像：跳过 bump 与打 tag，直接按目标 dispatch deploy。

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

### edge-<edge_id>：Edge 资源节点

以 `deploy/aws/stage0/edge-targets.json` 为准。`deployable=true` 的 edge 才进入 rollout；`deployable=false` 仅作为 planned。

```bash
# 单个 Edge upgrade（示例：edge_id=<edge_id>）
TARGET_TAG=X.Y.Z
EDGE_ID=<edge_id>
CONFIRM_STACK="tokenkey-edge-${EDGE_ID}-stage0"

gh workflow run deploy-edge-stage0.yml \
  -f edge_id="$EDGE_ID" \
  -f operation=upgrade \
  -f tag="$TARGET_TAG" \
  -f confirm_stack="$CONFIRM_STACK"

# 单个 Edge smoke only
gh workflow run deploy-edge-stage0.yml \
  -f edge_id="$EDGE_ID" \
  -f operation=smoke \
  -f confirm_stack="$CONFIRM_STACK"

# 单个 Edge rollback
gh workflow run deploy-edge-stage0.yml \
  -f edge_id="$EDGE_ID" \
  -f operation=rollback \
  -f tag="$PREVIOUS_TAG" \
  -f confirm_stack="$CONFIRM_STACK"
```

`provision` 只用于首次创建或 CloudFormation 参数/模板更新，不是日常 release rollout 默认操作。

## target=all 的执行顺序

1. 完成“标准流程：release 新镜像”，得到 `TARGET_TAG`。
2. 读取 deployable 矩阵（按 `deploy/aws/stage0/edge-targets.json` 顺序）：
   `python3 - <<'PY'\nimport json\nfrom pathlib import Path\np=Path('deploy/aws/stage0/edge-targets.json')\nd=json.loads(p.read_text())\nfor k,v in (d.get('targets') or {}).items():\n    if v.get('deployable'): print(k)\nPY`
3. 取矩阵第一个 deployable Edge 作为 canary：dispatch `deploy-edge-stage0.yml operation=upgrade tag=$TARGET_TAG`，watch 到 success。
4. 检查 canary Edge smoke 结果：external health、public runner relay path block、SSM self-smoke；若失败，停，不推进 prod，除非用户明确 override。
5. 推进 prod deploy：优先使用 release 自动 queue 的 prod run；没有则手动 dispatch。watch 到 success。
6. 做 prod 完整 smoke（见下文）；若仅因 `POST_DEPLOY_SMOKE_CHAT_MODEL` 不在当前 key 的 `/v1/models` 可见列表而失败，先把对应 Environment 变量改到可见模型（如 `gpt-5.5`）再重跑，不要误判为发布回归。
7. 对 canary Edge 再 dispatch `operation=smoke`，做 prod 升级后的 main-gateway-via-edge 验证；若缺 `MAIN_GATEWAY_EDGE_SMOKE_API_KEY`，只可标记“infra smoke 通过，主网关经 Edge 业务 smoke 未覆盖”。
8. 其余 deployable Edge 顺序 upgrade + smoke；默认优先 `claude-sonnet-4-6`，若该 edge/key 不可见则切到该 key 的可见模型并重跑一次；失败即停。
9. **自动轻量 Diagnostics（all 默认搭配）**：all rollout 完成后自动 dispatch `.github/workflows/post-release-light-diagnostics.yml`（默认 `target_selector=all`），该 workflow 会按固定节奏触发两次 `ops-daily-diagnostics.yml operation=diagnostics`：
   - 第一次：+5min，`diagnostics_log_since=20m`（覆盖发版前后）。
   - 第二次：+1h，`diagnostics_log_since=2h`（覆盖发版前后）。
   - 两次都用 `diagnostics_include_error_clustering=false`，仅做 runtime/health 轻量巡检。

## prod 真实测试

部署 workflow 成功只说明流水线过了；Agent 仍需做验收（除非用户明确只要 CI）。

### A — CI 日志中的完整网关烟测

在本次 `deploy-stage0` run log 里搜索 `tk_post_deploy_smoke: OK`，并确认 `GET /v1/models`、`POST /v1/chat/completions`、`POST /v1/messages` 等为预期 HTTP。

不要只看脚本 OK 或文本 marker。生产验收必须确认：

- `/v1/models`：`object=list` 且 `data` 非空。
- `/v1/chat/completions`：`object=chat.completion`，`choices[]` 非空，`finish_reason` 合理，`usage` 存在。
- `/v1/messages`：`type=message`，`role=assistant`，`content[]` 有文本，`stop_reason` 合理，`usage` 字段结构正确。

### B — 本地快速探活（无密钥）

```bash
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/health'
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/api/v1/settings/public'
```

期望 200。

### C — 本地完整烟测（prod API key）

```bash
export TOKENKEY_BASE_URL=https://api.tokenkey.dev
# 必填：POST_DEPLOY_SMOKE_API_KEY
# 必填：POST_DEPLOY_SMOKE_GEMINI_API_KEY
# 必填：POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY
bash scripts/tk_post_deploy_smoke.sh
```

主 key 解析顺序：`POST_DEPLOY_SMOKE_API_KEY` → `ANTHROPIC_AUTH_TOKEN` → `TK_TOKEN` → `TOKENKEY_API_KEY`。不得打印完整 key；脚本只输出 `key_hint`。

正式验收中三个 smoke key 均为必填；任一缺失不得视为 prod 完整验收通过。

通过标准：

- public settings：HTTP 200，JSON `code=0`。
- frontend release asset shape：`check-frontend-release-assets.py` 通过。
- `/v1/models`：HTTP 200，`object=list`，`data` 非空。
- `/v1/chat/completions`：HTTP 200，shape 正确，usage 存在。
- `/v1/messages`：HTTP 200，shape 正确，usage 存在。
- Gemini tool-schema 探针：HTTP 400/401/403/404 为硬失败；5xx/429/no available accounts 为软警告；缺 key 为阻塞。
- OpenAI OAuth 探针：HTTP 200 + shape/marker/token totals；4xx 为硬失败；5xx/429 为软警告；缺 key 为阻塞。

## Edge smoke 验收

`deploy-edge-stage0.yml` 的 `Edge smoke` 调用 `scripts/tk_edge_post_deploy_smoke.sh`。验收时确认：

- external `GET <EDGE_API_URL>/health` 为 200。
- public runner `GET <EDGE_API_URL>/v1/models` 为 403，证明 Caddy relay path allowlist 生效。
- SSM self-smoke 成功：容器 `docker compose ps` 正常，容器内 `http://localhost:8080/health` 成功。
- 若 `EDGE_SELF_SMOKE_MODE=api` 且 Edge 本地 smoke key 配好，确认 Edge API self-smoke 成功。
- 若 `MAIN_GATEWAY_EDGE_SMOKE_API_KEY` 已配置，确认 main gateway via Edge smoke 成功，并通过对应 Edge 的 Caddy/tokenkey 日志证明请求实际命中该节点。
- 若 `MAIN_GATEWAY_EDGE_SMOKE_API_KEY` 未配置，只能声明“Edge infra smoke 通过”；不得声称主网关经 Edge 业务链路已验收。

## rollback

- prod rollback：dispatch `deploy-stage0.yml` 到 previous tag。

```bash
gh workflow run deploy-stage0.yml \
  -f tag="$PREVIOUS_TAG"
```

- Edge rollback：`deploy-edge-stage0.yml operation=rollback`（按目标替换 `edge_id`，`confirm_stack=tokenkey-edge-<edge_id>-stage0`）。

```bash
EDGE_ID=<edge_id>
gh workflow run deploy-edge-stage0.yml \
  -f edge_id="$EDGE_ID" \
  -f operation=rollback \
  -f tag="$PREVIOUS_TAG" \
  -f confirm_stack="tokenkey-edge-${EDGE_ID}-stage0"
```

prod smoke 失败：停，优先 rollback prod；不要继续 Edge rollout。Edge canary 失败：停，不批准/推进 prod，除非用户明确 override。

### 触发 all 后置轻量 Diagnostics（自动）

all rollout 验收完成后执行：

```bash
gh workflow run post-release-light-diagnostics.yml \
  -f target_selector=all
```

该 workflow 默认：

- +5min dispatch 一次轻量 diagnostics（`diagnostics_log_since=20m`）
- +1h dispatch 一次轻量 diagnostics（`diagnostics_log_since=2h`）

如需自定义延时/窗口，可覆写：`first_delay_minutes`、`first_window`、`second_delay_minutes`、`second_window`。

## 完成后：rollout 摘要

烟测全部完成后，运行以下命令，整理本次 release 变更：

```bash
NEW_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1)
PREV_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | sed -n '2p')
echo "range: ${PREV_TAG} → ${NEW_TAG}"

git log "${PREV_TAG}..${NEW_TAG}" --oneline --no-merges \
  | grep -v 'chore: bump VERSION' | grep -v '\[skip ci\]'

git diff --stat "${PREV_TAG}..${NEW_TAG}" -- backend/ frontend/src/ | tail -10
git diff --name-only "${PREV_TAG}..${NEW_TAG}" -- 'scripts/*-sentinels.json' 2>/dev/null || true
git diff --diff-filter=D --name-only "${PREV_TAG}..${NEW_TAG}" -- backend/ || true
```

向用户输出：

**本次发版：`${PREV_TAG}` → `${NEW_TAG}`**

| target | workflow | run id | tag | status | smoke |
|---|---|---:|---|---|---|
| edge-<edge_id>-canary（每个 deployable edge 一行） | deploy-edge-stage0 | ... | X.Y.Z | success/fail/skipped | infra / main-via-edge(按需) |
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
| `gh run watch` 被工具超时打断 | 用同一 run id 再执行 `gh run watch <id> --exit-status` 接到终态。 |
| prod `Deploy via SSM Run-Command` 报 `AccessDenied(ssm:SendCommand)` | 先核对 `tokenkey-cicd-oidc` 的 `TargetInstanceId` 是否等于 `tokenkey-prod-stage0` 当前 `InstanceId`；不一致先更新 OIDC 栈参数再重跑 deploy。 |
| prod smoke 报 `POST_DEPLOY_SMOKE_CHAT_MODEL not listed in GET /v1/models` | 不是代码回归，改 `prod` Environment 的 `POST_DEPLOY_SMOKE_CHAT_MODEL` 为该 key 可见模型（如 `gpt-5.5`）后重跑。 |
| `gh` 请求持续报 `read ... 127.0.0.1:7890: connection reset by peer` | 先用 `env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy gh <cmd>` 做无代理重试；恢复后再继续 watch。 |
| 无代理后 dispatch 报 `HTTP 403 Must have admin rights to Repository` | `gh` 可能切到另一个账号；先 `env -u GH_TOKEN ... gh auth status`，必要时 `gh auth switch -u <repo-owner>` 后重试 dispatch。 |
| `post-release-light-diagnostics` 在 `Dispatch first lightweight diagnostics` 失败且日志含 `failed to run git: ... not a git repository` | 视为 workflow 缺陷：先手动 dispatch 一次 `ops-daily-diagnostics.yml operation=diagnostics` 兜底，再记录 run id 并提修复 PR（定位该 workflow 对 `gh workflow run` 的调用上下文）。 |

## 扩展阅读（按需打开）

- `scripts/release-tag.sh` — tag 门禁。
- `.github/workflows/release.yml` — multi-arch image build 与 prod auto-dispatch。
- `.github/workflows/deploy-stage0.yml` — prod deploy。
- `.github/workflows/deploy-edge-stage0.yml` — Edge upgrade/smoke/rollback。
- `.github/workflows/post-release-light-diagnostics.yml` — all rollout 后 +5min/+1h 自动 dispatch 轻量 diagnostics。
- `scripts/tk_post_deploy_smoke.sh` — prod 完整 smoke。
- `scripts/tk_edge_post_deploy_smoke.sh` — Edge smoke wrapper。
- `deploy/aws/README.md` — Stage0、Edge、多区域升级 SOP。
