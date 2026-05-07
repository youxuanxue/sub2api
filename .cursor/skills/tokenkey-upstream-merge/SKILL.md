---
name: tokenkey-upstream-merge
description: >-
  Standard TokenKey upstream merge workflow for regularly importing Wei-Shaw/sub2api
  upstream/main while preserving TokenKey OPC goals: one product, one control plane,
  minimum Engine Spine, QA/tool evidence integrity, no silent upstream feature deletion,
  and minimum upstream conflict surface. Use when the user asks to merge upstream,
  refresh from upstream/main, prepare an upstream merge PR, review upstream drift, or
  create a recurring upstream update process.
---

# TokenKey upstream merge SOP

适用于 `merge/upstream-*` 分支。权威纪律仍以根目录 `CLAUDE.md` 与 `docs/global/tokenkey-opc-transformation-plan.md` 为准。

## 0. 不可逾越原则

每次 upstream merge 都必须同时保持：

1. **一个产品**：对外仍是 TokenKey；`newapi`、bridge、compat、projection 等内部词不外显成产品心智。
2. **一个控制面**：TokenKey 控制面留在本仓库；不在 sibling `new-api` 打私有补丁。
3. **最小 Engine Spine**：新增 endpoint / provider / capability 必须进入 Engine owner 或 companion，不在热点 service 里复制 truth。
4. **Evidence Spine**：QA/tool 调用、参数、返回、错误、stream terminal 必须无感完整记录；先脱敏再持久化；capture fail-open 但不 silent-loss。
5. **最小 upstream 冲突面**：TokenKey-only 逻辑向 companion / facade / component 收敛；不得 silent-delete upstream feature。

上游更新可能是几十到数百 commits；合并成功不是目标，冲突面下降才是目标。

## 1. 准备

1. 工作区必须干净；若已有用户改动，先确认归属，不覆盖。
2. 同步远端：`git fetch origin --tags && git fetch upstream --tags`。
3. 确认 main 与 upstream 状态：`bash scripts/check-upstream-drift.sh`。
4. Dry-run：`git merge-tree upstream/main HEAD`，先识别热点冲突。
5. 创建分支：`merge/upstream-YYYYMMDD`。

## 2. Commit 形状

每个 upstream merge PR 使用三类 commit，不混杂：

### A. Merge Harness Commit

只做：

- `git merge --no-ff upstream/main`。
- 解决冲突，保留 upstream 能力与审计链。
- 生成代码：Ent / Wire / frontend dist（按实际触达）。
- 把新增入口接入已有 canonical hooks。
- 保证基础编译与 preflight 能运行。

不得：借机清历史债务、重构无关模块、删除 upstream feature。

### B. Invariant Commit

只修不可退让项：

- TokenKey 品牌回退。
- raw secret 持久化或结构化日志泄漏。
- route canonical 破坏。
- QA/trajectory capture hook 缺失。
- redaction contract 漂移。
- newapi / engine / brand / terminal sentinel 漏洞（包括 `engine-facade-sentinels.json` 门禁：dispatch 路径须经 `engine.BuildDispatchPlan`，Gemini 思考块过滤器须保持 `shouldDropGeminiInternalText` / `normalizeGeminiFunctionArgs` 调用链）。
- release workflow ARM/tag/skip-ci 纪律回退。

### C. OPC Refactor Commit

只收敛本次 merge 新增或显著增厚的分叉面：

- 热点 Go 文件新增 TokenKey 分支 → companion / facade / owner。
- 平行 truth table → Engine / openai_compat / newapi owner。
- 大型 Vue view 新增策略块 → component / composable。
- 新 owner 必须配 focused test 或 semantic sentinel。

如果确实无法同 PR 收敛，PR body 必须写阻塞原因，并补机械门禁防止继续扩张。

## 3. 决策清单

遇到冲突或 upstream 新能力，按顺序决策：

1. **是否 upstream feature？** 默认保留；不要 silent-delete。
2. **是否影响产品心智？** 展示层用 TokenKey / Extension Engine；协议 identity 不改名。
3. **是否新增 endpoint/provider/capability？** 必须进入 Engine owner 或 companion。
4. **是否新增 QA/tool payload 或 terminal path？** 必须接入无感 capture，先脱敏再持久化。
5. **是否触碰热点文件？** 只允许薄调用点；本 PR 新增分叉必须收敛。
6. **是否新增人工操作？** 必须脚本化或 CI 化。
7. **是否改变 schema/interface？** Ent/schema-first；生成代码与所有 stubs 同步。

## 4. 标准检查清单

PR 前必须完成：

- `git diff --diff-filter=D upstream/main..HEAD -- backend/`：确认没有未说明的 upstream 文件删除。
- `git log --oneline upstream/main..HEAD | wc -l`：写入 PR body。
- `git diff --stat upstream/main..HEAD -- backend/ | head -5`：写入 PR body。
- `go -C backend generate ./ent`（如 Ent schema 或 migrations 触达）。
- `go -C backend generate ./cmd/server`（如 Wire graph 触达）。
- `go -C backend test -tags=unit ./...`。
- `go -C backend test -tags=integration ./...`（若 schema/repository/gateway path 高风险触达）。
- `pnpm --dir frontend lint:check && pnpm --dir frontend typecheck`（如 frontend 触达）。
- `pnpm --dir frontend run build`（如 frontend dist 或 embedded web 触达）。
- `python3 scripts/export_agent_contract.py --check`（如 agent contract 相关触达）。
- `./scripts/preflight.sh`（覆盖所有 sub2api sentinel 检查；`upstream-merge-pr-shape.yml` CI 独立跑其中 newapi、engine-facade、frontend-tk 三组）。

不得跳过 hook 或用 `--no-verify`。

## 5. PR body 模板

```markdown
## Summary
- Merge upstream/main into TokenKey with a merge commit while preserving TokenKey OPC invariants.
- Keep upstream features compiled in; TokenKey-specific behavior stays behind companion/facade/component boundaries.

## Risk
- Large upstream merge across: <hotspots>.
- Human-reviewed decisions: <decision list>.

## Validation
- <commands run>

## Upstream Audit
- Required audit range: upstream/main..HEAD
- TK ahead count: `<git log --oneline upstream/main..HEAD | wc -l>`
- Backend stat top files: `<git diff --stat upstream/main..HEAD -- backend/ | head -5>`
```

## 7. 完成后：本次 upstream merge 变更摘要

PR 全部检查通过、准备合并（或刚完成合并）后，运行以下命令，然后向用户输出结构化摘要。

```bash
# 先确保 upstream 已 fetch（未 fetch 时 merge-base 会出错）
git fetch upstream --quiet

# 1. upstream 本次带入了哪些提交
MERGE_BASE=$(git merge-base HEAD upstream/main)
echo "upstream 新带入提交："
git log "${MERGE_BASE}..upstream/main" --oneline --no-merges | head -30

# 2. upstream 触达的文件统计（backend 最关键）
echo "upstream backend diff stat："
git diff --stat "${MERGE_BASE}" upstream/main -- backend/ | tail -10

# 3. TK ahead 数量（PR body 审计数据）
echo "TK ahead commits: $(git log --oneline upstream/main..HEAD | wc -l | tr -d ' ')"

# 4. 本次 PR 中 TK invariant / OPC 提交
echo "本次 PR TK 提交（非 merge commit）："
git log --oneline upstream/main..HEAD --no-merges | head -20

# 5. sentinel 文件有无改动
git diff --name-only upstream/main..HEAD -- scripts/ | grep 'sentinels' || \
  echo "(no sentinel changes)"

# 6. 是否删除了 upstream backend 文件（风险点）
git diff --diff-filter=D --name-only upstream/main..HEAD -- backend/ || echo "(no upstream file deletions)"
```

基于输出，向用户呈现以下结构：

**upstream merge 范围：`<merge_base_short>` → `upstream/main`（N 个上游提交）**

**上游带入**：按影响维度分类（handler / service / frontend / schema / CI），每类列 1–3 行关键提交。

**TK invariant 修复**（B 类 commit）：列出修复的不可退让项及改动文件。

**TK OPC 收敛**（C 类 commit，如有）：列出从热点文件抽取到 companion 的内容。

**需要在 prod smoke / 本地测试中重点验证**（根据实际变更填写）：

| 触达路径 | 验证方式 |
|---|---|
| Gemini 路径 | Gemini tool-schema 探针（必须设 `POST_DEPLOY_SMOKE_GEMINI_API_KEY`）；HTTP 400=硬失败需回查 |
| OpenAI-compat / Responses | OpenAI OAuth 探针（必须设 `POST_DEPLOY_SMOKE_OPENAI_OAUTH_API_KEY`）；`reasoning_tokens` 是否透传 |
| pricing / model-list | `/v1/models` 数量与可用性标记 |
| frontend 组件 | frontend release asset 探针 + 浏览器关键页 |
| 新增 sentinel | 列出 `*-sentinels.json` 文件名，说明守卫的回归场景 |
| upstream 删除文件（如有） | 逐一确认 PR description 有 (a)/(b)/(c) 回归说明 |

**后续建议**：是否需要立即 bump VERSION 发版，或等待下一批 TK 功能合入。

## 6. Red flags

Stop and fix before PR if any is true:

- TokenKey-only code got added directly to `openai_gateway_service.go`, `openai_account_scheduler.go`, `gateway_bridge_dispatch.go`, `gateway.go`, or large admin Vue views without companion/facade/component extraction.
- New endpoint lacks QA/trajectory capture or terminal semantics.
- Sensitive payload persists without redaction version contract.
- New upstream file/route/service was deleted or disabled without explicit regression justification.
- PR shape check would fail: no upstream merge commit, missing `upstream/main..HEAD`, or first-parent commit contains skip-ci markers.
- Direct `bridge.Dispatch*` call added outside the approved service boundary files (`gateway_bridge_dispatch.go` / `openai_gateway_bridge_dispatch*.go`) — engine dispatch eligibility must route through `engine.BuildDispatchPlan`; `engine-facade-sentinels.json` will flag this mechanically.
- New Gemini response path processes `internalThought`/`executableCode` blocks without calling `shouldDropGeminiInternalText` / `normalizeGeminiFunctionArgs` — thinking-block filter or tool-arg normalizer has drifted; `engine-facade-sentinels.json` `gemini_thinking_filter_*` entries will fail.
