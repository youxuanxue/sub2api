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
- newapi / engine / brand / terminal sentinel 漏洞。
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
- `./scripts/preflight.sh`。

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

## 6. Red flags

Stop and fix before PR if any is true:

- TokenKey-only code got added directly to `openai_gateway_service.go`, `openai_account_scheduler.go`, `gateway_bridge_dispatch.go`, `gateway.go`, or large admin Vue views without companion/facade/component extraction.
- New endpoint lacks QA/trajectory capture or terminal semantics.
- Sensitive payload persists without redaction version contract.
- New upstream file/route/service was deleted or disabled without explicit regression justification.
- PR shape check would fail: no upstream merge commit, missing `upstream/main..HEAD`, or first-parent commit contains skip-ci markers.
