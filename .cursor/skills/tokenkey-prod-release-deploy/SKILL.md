---
name: tokenkey-prod-release-deploy
description: >-
  Delivers a new sub2api/TokenKey production release on AWS Stage0 after syncing
  main—bumps backend/cmd/server/VERSION when needed, pushes tag via
  scripts/release-tag.sh, waits for GHCR images, dispatches deploy-stage0 to
  prod, and verifies with tk_post_deploy_smoke plus /health. Use when the user
  asks to ship prod, release to AWS, tag deploy, pull latest main and deploy
  production, or run post-deploy smoke against production.
---

# TokenKey：main 同步 → 打 tag → AWS prod 部署 → 真实测试

适用于本仓库（TokenKey fork of sub2api）。权威纪律见根目录 `CLAUDE.md`（发版、ARM、`new-api` 路径）。

## 前置条件

- 工作目录：仓库根目录（含 `backend/`、`scripts/release-tag.sh`）。
- 网络、`git`、`gh` 已认证且对远端可写；`gh` 能 dispatch `release.yml` 与 `deploy-stage0.yml`。
- **生产**对应 GitHub Environment `prod`（若有 Required reviewers，需人工点批准）。
- **禁止**：VERSION bump / 发版 commit 的正文里出现字面量 `[skip ci]` 或 `[ci skip]`（任意位置都不行）。

## 决策：要不要升 patch 版本

1. `git fetch origin main --tags && git checkout main && git pull origin main --ff-only`
2. 读 `backend/cmd/server/VERSION`（记为 `V`，无 `v` 前缀）。
3. 用 `git ls-remote --tags origin "refs/tags/v${V}"`（或 `git tag -l "v${V}"` 在 fetch 标签之后）判断是否已有 **远端** `v${V}`：
   - **`v${V}` 尚不存在**：若 `main` 已含正确 `VERSION=V` 且已 push，可直接 `bash scripts/release-tag.sh v${V}`，无需 bump。
   - **`v${V}` 已存在**，且 `origin/main` 比该 tag **更新**（`git merge-base --is-ancestor v${V} origin/main` 为真 **且** `git rev-parse v${V} origin/main` 两个 SHA 不同）：须把 `VERSION` **升到下一 patch**（例 `1.7.9`→`1.7.10`），提交并 push，再对新版本执行 `release-tag.sh`。禁止对已有远端 tag 的同一 `vX.Y.Z` 重复推送。
   - **`origin/main` 与 `v${V}` 同一 commit**，仅 prod 未部署该镜像：跳过 bump 与打 tag，直接 `gh workflow run deploy-stage0.yml -f environment=prod -f tag=${V}`。

## 标准流程（发布新镜像 + 部署 prod）

1. **同步 main**（同上）。
2. **如需 bump**：改 `backend/cmd/server/VERSION` → 单提交 `chore: bump VERSION to X.Y.Z`（无任何 skip-ci 字样）→ `git push origin main`。
3. **打 tag**（必须在 `main` 且与 `origin/main` 同 SHA）：  
   `bash scripts/release-tag.sh vX.Y.Z`  
   不要手打 `git tag`；脚本校验 `[skip ci]`、VERSION 一致、分支与同步。
4. **等待镜像**：`gh run list --workflow=release.yml --limit 1` → `gh run watch <id> --exit-status`，直到 **success**。多架构构建可能需 **十余分钟**。
5. **部署 prod**：镜像 tag **不带 `v`**：  
   `gh workflow run deploy-stage0.yml -f environment=prod -f tag=X.Y.Z`  
   **不要**设 `simple_release_override=true`（prod/test 为 **ARM**；单架构 manifest 会 `exec format error`）。
6. **等待部署**：`gh run watch` 对本次 `deploy-stage0` run；状态 `waiting` 时需 Environment 审批。

## 真实测试（必须做）

**A — CI 已做的完整网关烟测**（与 `https://api.tokenkey.dev` 真机交互，需仓库 secret `POST_DEPLOY_SMOKE_API_KEY` 等）：  
部署 workflow 成功步里会跑 `bash scripts/tk_post_deploy_smoke.sh`。从 run log 中确认出现 **`tk_post_deploy_smoke: OK`** 以及 `GET .../v1/models`、`POST .../v1/chat/completions`、`POST .../v1/messages` 为预期 HTTP。

**B — 本地快速探活**（无密钥）：  

```bash
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/health'
curl -sS -o /dev/null -w '%{http_code}\n' 'https://api.tokenkey.dev/api/v1/settings/public'
```

期望 **200**；若需更严，对同上 URL 取 body，校验 JSON 的 `code` 为 `0`。

**C — 本地完整烟测**（有 **prod** 用 API key 时）：  

```bash
cd /path/to/sub2api
TOKENKEY_BASE_URL=https://api.tokenkey.dev POST_DEPLOY_SMOKE_API_KEY='sk-...' bash scripts/tk_post_deploy_smoke.sh
```

不得打印完整 key；脚本只打 key hint。

## release 之后 main 是否还有提交

`release.yml` 可能产生 **sync-version** 写回提交。流程结束后执行 `git fetch origin main`，若本地落后则 `git pull`。**不要**手改 `docs/agent_integration.md`（有变更应跑 `python scripts/export_agent_contract.py` 并过 preflight，见仓库约定）。

## 故障速查

| 现象 | 处理 |
|------|------|
| `release-tag.sh` 报 HEAD 含 skip-ci 标记 | 修改触发打 tag 的最近一次提交说明后重试，或按 `CLAUDE.md` 用 `gh workflow dispatch` 触发 `release.yml` |
| `tag already exists on origin` | 升 `VERSION` 再打新 tag，或仅 dispatch deploy 已有 tag |
| deploy 报单架构 manifest | 重新跑 `release.yml` 且 **`simple_release=false`**；prod 不要 override |
| smoke 失败 | 看 deploy run log；查 Caddy/容器日志与网关路由（`deploy/aws/README.md`、 `docs/approved/deploy-stage0-workflow.md`） |

## 扩展阅读（按需打开）

- `scripts/release-tag.sh` — tag 门禁
- `deploy/aws/README.md` — Stage0、升级 SOP
- `.github/workflows/deploy-stage0.yml` — prod/test、tag 格式
