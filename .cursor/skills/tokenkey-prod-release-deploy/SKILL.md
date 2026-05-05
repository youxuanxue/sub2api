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

- **顺序做完**：同步 → 决策 VERSION/tag →（按需 bump+push）→ `release-tag.sh` → **watch 到 release 成功** → dispatch deploy → **watch 到 deploy 成功** → **再做本地验收（B + 尽量 C）**。不要在 deploy 绿灯后就结束会话。
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
5. **部署 prod**：镜像 tag **不带 `v`**：`gh workflow run deploy-stage0.yml -f environment=prod -f tag=X.Y.Z`。记下输出的 run URL，对该 run `gh run watch <id> --exit-status` 直至 success（停在 **waiting** 时在 GitHub **Environment `prod`** 批准后继续）。**不要**设 `simple_release_override=true`（prod/test 为 **ARM**；单架构 manifest 会 `exec format error`）。

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
export TOKENKEY_BASE_URL=https://api.tokenkey.dev
# POST_DEPLOY_SMOKE_API_KEY 已在 Cursor / shell 中导出即可
bash scripts/tk_post_deploy_smoke.sh
```

脚本内解析顺序（摘自 `scripts/tk_post_deploy_smoke.sh`）：`POST_DEPLOY_SMOKE_API_KEY` → `ANTHROPIC_AUTH_TOKEN` → `TK_TOKEN` → `TOKENKEY_API_KEY`。任一为非空即可。

仅在无法注入环境时再临时：`POST_DEPLOY_SMOKE_API_KEY='sk-…' bash scripts/tk_post_deploy_smoke.sh`。  
不得打印完整 key；脚本只输出 `key_hint`。

**Agent 注意**：若在沙箱里跑导致读不到用户环境变量，改用可继承本机环境的执行方式（例如非 sandbox / `all`），否则 C 会因缺 key 退出。

**C 的通过标准**：`tk_post_deploy_smoke.sh` 覆盖 public settings、frontend assets、`/v1/models`、`/v1/chat/completions`、`/v1/messages`；通过时仍需确认结构字段，而不是只看 marker 文本。若本次发布触达 Responses/OpenAI-compat/Engine/Evidence 相关路径，或脚本当前未覆盖 `/v1/responses`，必须追加一次 `/v1/responses` 结构化请求：HTTP 200，`object=response`，`status=completed`（或有明确可解释的非失败终态），`output[]` / `output_text` 含测试短句，`usage` 字段结构正确，且没有 `error`。多个 key/group 验收时，分别记录 `key_hint`、group platform、以及日志里的 `account_id/platform/model`，不得用一个 key 的通过替代全部分组。

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

## 扩展阅读（按需打开）

- `scripts/release-tag.sh` — tag 门禁
- `deploy/aws/README.md` — Stage0、升级 SOP
- `.github/workflows/deploy-stage0.yml` — prod/test、tag 格式
