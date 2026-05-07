---
name: tokenkey-prod-release-deploy
description: >-
  Delivers a new sub2api/TokenKey production release on AWS Stage0 after syncing
  main—bumps backend/cmd/server/VERSION when needed, pushes tag via
  scripts/release-tag.sh, waits for GHCR images (allow long gh run watch),
  dispatches deploy-stage0 to prod, waits through Environment approval if any,
  then verifies with curl probes plus tk_post_deploy_smoke (prefer
  POST_DEPLOY_SMOKE_API_KEY from environment). Use when the user asks to ship
  prod, release to AWS, tag deploy, pull latest main and deploy production, or
  run post-deploy smoke against production.
---

# TokenKey：main 同步 → 打 tag → AWS prod 部署 → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 一次性跑完（原则）

- **顺序做完**：同步 → 决策 VERSION/tag →（按需 bump+push）→ `release-tag.sh` → **watch 到 release 成功** → **先检查 Bot 是否已自动触发 deploy，无则手动 dispatch** → **watch 到 deploy 成功** → **再做本地验收（B + 尽量 C）**。不要在 deploy 绿灯后就结束会话。
- **读 VERSION 前必须先** `fetch` + `pull --ff-only` **后的** `origin/main` 为准；在未更新的本地分支上读 `VERSION` 会与远端 tag 错位，误判「要不要 bump」。
- **`gh run watch` 要给够时间**：多架构 `release.yml` 常见 **十余分钟量级**；Agent 应用 `--exit-status` 跟跑到结束，不要用默认几十秒的 command 超时提前杀掉。
- **`prod` Environment**：run 卡在 `waiting` 时需有人在 GitHub Actions 里批准；批准后继续 watch，勿当作失败退出。
- **完整烟测密钥**：优先使用环境里已有的 `POST_DEPLOY_SMOKE_API_KEY`（Cursor / shell 已导出），避免停下来向用户索要 key；详见下文 C。

## 前置条件

- 工作目录：仓库根目录（含 `backend/`、`scripts/release-tag.sh`）。
- 网络、`git`、`gh` 已认证且对远端可写；`gh` 能 dispatch `release.yml` 与 `deploy-stage0.yml`。
- **生产**对应 GitHub Environment `prod`（若有 Required reviewers，需人工点批准）。
- **禁止**：VERSION bump / 发版 commit 的正文里出现字面量 `[skip ci]` 或 `[ci skip]`（任意位置都不行）。

## 决策：要不要升 patch 版本

1. `git fetch origin main --tags && git checkout main && git pull origin main --ff-only`
2. 读 **已与 `origin/main` 对齐的** `backend/cmd/server/VERSION`（记为 `V`，无 `v` 前缀）。
3. 用 `git ls-remote --tags origin "refs/tags/v${V}"`（或 `git tag -l "v${V}"` 在 fetch 标签之后）判断是否已有 **远端** `v${V}`：
   - **`v${V}` 尚不存在**：若 `main` 已含正确 `VERSION=V` 且已 push，可直接 `bash scripts/release-tag.sh v${V}`，无需 bump。
   - **`v${V}` 已存在**，且 `origin/main` 比该 tag **更新**（`git merge-base --is-ancestor v${V} origin/main` 为真 **且** `git rev-parse v${V} origin/main` 两个 SHA 不同）：须把 `VERSION` **升到下一 patch**（例 `1.7.9`→`1.7.10`），提交并 push，再对新版本执行 `release-tag.sh`。禁止对已有远端 tag 的同一 `vX.Y.Z` 重复推送。  
     （典型坑：`VERSION` 文件仍是旧 patch，但远端已有对应 tag 且仅指向更早提交——必须以本条为准 bump，不能复读旧 VERSION。）
   - **`origin/main` 与 `v${V}` 同一 commit**，仅 prod 未部署该镜像：跳过 bump 与打 tag，直接 `gh workflow run deploy-stage0.yml -f environment=prod -f tag=${V}`。

## 标准流程（发布新镜像 + 部署 prod）

1. **同步 main**（同上）。
2. **如需 bump**：改 `backend/cmd/server/VERSION` → 单提交 `chore: bump VERSION to X.Y.Z`（无任何 skip-ci 字样）→ `git push origin main`。
3. **打 tag**（必须在 `main` 且与 `origin/main` 同 SHA）：  
   `bash scripts/release-tag.sh vX.Y.Z`  
   不要手打 `git tag`；脚本校验 `[skip ci]`、VERSION 一致、分支与同步。
4. **等待镜像**：`gh run list --workflow=release.yml --limit 1` → 取 **刚触发、与本次 tag 对应** 的 run（可看 `headBranch` / 标题）→ `gh run watch <id> --exit-status`，直到 **success**。多架构构建可能需 **十余分钟**。
5. **部署 prod**：`release.yml` 成功后会**自动** dispatch `deploy-stage0.yml`（由 `github-actions[bot]` 触发）。**先检查**是否已自动触发，再决定是否手动 dispatch：

   ```bash
   gh run list --workflow=deploy-stage0.yml --limit 1 --json databaseId,actor,createdAt,status
   ```

   - **Bot 已自动触发**（`actor.login=github-actions` 且 `createdAt` 在 release 完成后数秒内）→ 直接 watch 该 run：`gh run watch <id> --exit-status`
   - **未自动触发**（异常情况）→ 手动 dispatch（tag **不带 `v`**）：`gh workflow run deploy-stage0.yml -f environment=prod -f tag=X.Y.Z`，然后 watch

   **禁止**在不检查的情况下直接手动 dispatch——会产生两个并行 deploy run，需手动取消多余的一个。run 卡在 **waiting** 时在 GitHub **Environment `prod`** 批准后继续。**不要**设 `simple_release_override=true`（prod/test 为 **ARM**；单架构 manifest 会 `exec format error`）。

## 真实测试（必须做）

部署 workflow **成功**只说明流水线过了；Agent **仍需**下面顺序做完（除非用户明确只要 CI、不做本地）。

### A — CI 日志中的完整网关烟测

与 `https://api.tokenkey.dev` 真机交互，依赖仓库 secret `POST_DEPLOY_SMOKE_API_KEY` 等。在 **本次** `deploy-stage0` run log 里搜索 **`tk_post_deploy_smoke: OK`**，并确认 `GET .../v1/models`、`POST .../v1/chat/completions`、`POST .../v1/messages` 等为预期 HTTP。

**不要只看脚本 OK 或文本 marker。** 生产验收必须确认响应结构：`/v1/models` 为 `object=list` 且 `data` 非空；`/v1/chat/completions` 为 `object=chat.completion`、`choices[]` 非空、`finish_reason` 合理、`usage` 存在（若上游返回）；`/v1/messages` 为 `type=message`、`role=assistant`、`content[]` 有文本、`stop_reason` 合理、`usage` 字段结构正确。若 CI 脚本未打印足够结构信息，按 C 在本地用同一 key 补测并记录结构字段。

### B — 本地快速探活（无密钥）

```bash
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/health'
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/api/v1/settings/public'
```

期望 **200**；若需更严，对 `/api/v1/settings/public` 的 body 校验 JSON `code` 为 `0`。

### C — 本地完整烟测（prod API key）

**推荐（不间断执行）**：密钥已由环境注入时，不要在命令行粘贴 key：

```bash
cd /path/to/sub2api
export TOKENKEY_BASE_URL=https://api.tokenkey.dev    # 或 TK_GATEWAY_URL（脚本两个都识别）
# 以下三个 key 必须全部导出后再运行；任一缺失均不得视为验收通过
# POST_DEPLOY_SMOKE_API_KEY 已在 Cursor / shell 中导出即可
# POST_DEPLOY_SMOKE_GEMINI_API_KEY=sk-...     # 绑定 gemini 分组，验证 tool-schema 清理
# POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY=sk-... # 绑定 OpenAI OAuth/codex 分组，验证 reasoning_tokens 透传
bash scripts/tk_post_deploy_smoke.sh
```

**主 key 解析顺序**（摘自脚本）：`POST_DEPLOY_SMOKE_API_KEY` → `ANTHROPIC_AUTH_TOKEN` → `TK_TOKEN` → `TOKENKEY_API_KEY`，任一非空即可。

**烟测 key 一览（三个均为必填）**：

| 环境变量 | 用途 |
|---|---|
| `POST_DEPLOY_SMOKE_API_KEY`（或链式备选） | 主路由：public settings / models / chat / messages |
| `POST_DEPLOY_SMOKE_GEMINI_API_KEY` | 绑定 gemini 分组，验证 Anthropic→Gemini tool-schema 清理（`const`/`propertyNames`/`exclusiveMinimum` 等 Draft 2020 关键词）|
| `POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY` | 绑定 OpenAI OAuth/codex 分组，验证账号正确性 + `reasoning_tokens` 透传 |

调节项（非必填）：`POST_DEPLOY_SMOKE_GEMINI_MODEL`、`POST_DEPLOY_SMOKE_OPENAI_OAUTH_MODEL`（默认见脚本注释）；`POST_DEPLOY_SMOKE_OPENAI_OAUTH_REQUIRE_REASONING_TOKENS=1` 可将 `reasoning_tokens=0` 升为硬失败；`POST_DEPLOY_SMOKE_SKIP_FRONTEND=1` 仅供本地调试，正式验收不得使用。

仅在无法注入环境时再临时：`POST_DEPLOY_SMOKE_API_KEY='sk-…' bash scripts/tk_post_deploy_smoke.sh`。  
不得打印完整 key；脚本只输出 `key_hint`。

**Agent 注意**：若在沙箱里跑导致读不到用户环境变量，改用可继承本机环境的执行方式（例如非 sandbox / `all`），否则 C 会因缺 key 退出。

**C 的通过标准**：

- **public settings** — HTTP 200，JSON `code=0`。
- **frontend release asset shape** — `check-frontend-release-assets.py` 通过；缺失 asset 返回 HTTP 404 + `Cache-Control: no-store`，不得 fallback 成 `index.html`。
- **`/v1/models`** — HTTP 200，`object=list`，`data` 非空。
- **`/v1/chat/completions`** — HTTP 200，`object=chat.completion`，`choices[0].message.content` 含预期 marker，`finish_reason` 合理，`usage` 存在。
- **`/v1/messages`** — HTTP 200，`type=message`，`role=assistant`，`content[]` 有文本，`stop_reason` 合理，`usage` 存在。
- **Gemini tool-schema 探针** — HTTP 400 = **硬失败**（schema 清理回归，必须回滚）；HTTP 401/403/404 = **硬失败**（key/路由配置错误）；HTTP 5xx/429/"no available accounts" = **软警告** exit 0（运行时资源问题，非 schema 回归）。**`POST_DEPLOY_SMOKE_GEMINI_API_KEY` 未设 = 阻塞，不得视为通过。**
- **OpenAI OAuth 探针** — HTTP 200 + 正确 shape + 含预期 marker + `prompt_tokens`/`completion_tokens` 非零且 `total` 自洽；`reasoning_tokens=0` 默认软警告，设 `REQUIRE=1` 升为硬失败；HTTP 4xx = **硬失败**；HTTP 5xx/429 = **软警告**。**`POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY` 未设 = 阻塞，不得视为通过。**

通过时仍需确认结构字段，不能只看 marker 文本。若本次发布触达 Responses 路径而以上探针未覆盖，可追加一次手动 `/v1/responses` 探针：HTTP 200，`object=response`，`status=completed`，`output[]`/`output_text` 含测试短句，`usage` 结构正确，无 `error`。多分组验收时，按 key 分别记录 `key_hint`、group platform、日志 `account_id/platform/model`；不得以一个 key 的通过代替全部分组。

## 完成后：本次发版变更摘要

烟测全部通过后，运行以下命令，然后向用户输出结构化摘要。

```bash
# 找到本次与上一个 tag
NEW_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | head -1)
PREV_TAG=$(git tag --sort=-version:refname | grep '^v[0-9]' | sed -n '2p')
echo "range: ${PREV_TAG} → ${NEW_TAG}"

# 1. 有效提交（排除 merge commit 和 VERSION bump）
git log "${PREV_TAG}..${NEW_TAG}" --oneline --no-merges \
  | grep -v 'chore: bump VERSION' | grep -v '\[skip ci\]'

# 2. 变更文件统计（backend + frontend）
git diff --stat "${PREV_TAG}..${NEW_TAG}" -- backend/ frontend/src/ \
  | tail -10

# 3. sentinel 文件新增 / 改动
git diff --name-only "${PREV_TAG}..${NEW_TAG}" -- 'scripts/*-sentinels.json' 2>/dev/null || \
  git diff --name-only "${PREV_TAG}..${NEW_TAG}" -- scripts/ | grep 'sentinels'

# 4. upstream 文件是否有删除（风险点）
git diff --diff-filter=D --name-only "${PREV_TAG}..${NEW_TAG}" -- backend/ || true
```

基于输出，向用户呈现以下结构（不要省略任何非空部分）：

**本次发版：`${PREV_TAG}` → `${NEW_TAG}`**

| 类别 | 提交 / 文件 | 说明 |
|---|---|---|
| feat | … | 新功能及影响的网关 / 调度 / frontend 模块 |
| fix | … | 修复内容及触达路径 |
| chore/ci | … | 基础设施、CI、sentinel 变更 |

**影响面与验证重点**（根据实际变更填写，无则省略）：

- **Gemini tool-schema 探针结果** — HTTP 200 为通过，400 为硬失败（schema 清理回归）
- **OpenAI OAuth 探针结果** — shape/marker/token totals；`reasoning_tokens` 是否透传
- **pricing / model-list** → `/v1/models` 返回数量与可用性标记是否符合预期
- **frontend 变更** → frontend release asset shape 探针结果
- **新增 / 修改的 sentinel** → 说明守卫的回归场景；upstream merge 时需重点确认
- **upstream 文件删除（如有）** → 列出文件，说明是否有 PR description 中的 (a)/(b)/(c) 回归说明

## release 之后 main 是否还有提交

`release.yml` 可能产生 **sync-version** 写回提交。流程结束后执行 `git fetch origin main`，若本地落后则 `git pull`。**不要**手改 `docs/agent_integration.md`（有变更应跑 `python scripts/export_agent_contract.py` 并过 preflight，见仓库约定）。

## 故障速查

| 现象 | 处理 |
|------|------|
| `release-tag.sh` 报 HEAD 含 skip-ci 标记 | 修改触发打 tag 的最近一次提交说明后重试，或按 `CLAUDE.md` 用 `gh workflow dispatch` 触发 `release.yml` |
| `tag already exists on origin` | 升 `VERSION` 再打新 tag，或仅 dispatch deploy 已有 tag |
| deploy 报单架构 manifest | 重新跑 `release.yml` 且 **`simple_release=false`**；prod 不要 override |
| smoke 失败 | 看 deploy run log；查 Caddy/容器日志与网关路由（`deploy/aws/README.md`、 `docs/approved/deploy-stage0-workflow.md`） |
| `gh run watch` 被工具超时打断 | 用同一 run id 再执行 `gh run watch <id> --exit-status` 接到终态 |
| 出现两个并行 deploy run（Bot + 手动） | `release.yml` 已自动触发，不要再手动 dispatch；`gh run cancel <手动触发的 run id>` 取消多余的，watch Bot run |

## 扩展阅读（按需打开）

- `scripts/release-tag.sh` — tag 门禁
- `deploy/aws/README.md` — Stage0、升级 SOP
- `.github/workflows/deploy-stage0.yml` — prod/test、tag 格式
