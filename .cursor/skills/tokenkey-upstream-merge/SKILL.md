---
name: tokenkey-upstream-merge
description: >-
  TokenKey upstream merge workflow for importing Wei-Shaw/sub2api upstream/main. Use when merging or reviewing upstream drift, preparing an upstream merge PR, or maintaining recurring upstream update discipline.
---

# TokenKey upstream merge

先加载通用 `upstream-merge` skill：保留上游历史、A/B/C commit、冲突审计与收尾由它单一拥有。这里仅补 TokenKey 不变量、owner 和验证入口；根 `CLAUDE.md` 与 `docs/global/tokenkey-opc-transformation-plan.md` 为项目约束。

## 0. 流程心智

周期 `.github/workflows/upstream-merge-notify.yml` 只探测并更新跟踪 issue，等人决定；不在 CI 自动 merge、创建 merge PR 或派 headless agent。获授权的合并由 Agent 按通用 SOP 执行，worktree 用 `git-worktree-submodule`。

## 1. 不可逾越原则

- 对外保持 TokenKey 产品心智；协议 identity 不改名，bridge/compat/projection 不外显。
- 控制面在本仓库，不往 sibling new-api 打私有补丁。
- 新 endpoint/provider/capability 进入 Engine owner/companion；热点只留薄调用，不复制 truth。新 owner 配 focused test 或 semantic sentinel。
- QA/tool 参数、响应、错误和 terminal path 无感记录，先脱敏再持久化；capture fail-open 但不能 silent-loss。
- 默认保留 upstream 功能；TokenKey 分叉向 companion/facade/component 收敛。无法同 PR 收敛的新增分叉须说明阻碍并补机械门禁。

## 1. 准备

1. 确认工作区归属并保留用户 WIP；按通用 skill 建 `merge/upstream-*` 隔离工作区。
2. `git fetch origin --tags`、`git fetch upstream --tags`，运行 `bash scripts/upstream/check-drift.sh`。
3. 将本轮已审阅 upstream 完整 SHA 固定到 `.upstream-ref`；目标须为最终 HEAD 祖先，不能退 pin 消除失败。
4. 用通用 skill 的 merge-tree/audit helper 识别冲突与审计范围。

## 2. Commit 形状

按通用 skill 的 A（harness）、B（invariant）、C（本次分叉收敛）职责提交；不借上游合并清无关历史债务。项目形状由 `.github/workflows/upstream-merge-pr-shape.yml` 守卫。

## 3. 决策清单

冲突按上方不变量裁决；检查 [项目 red flags](references/red-flags.md) 的具体 gateway/admin/engine 挂点。新增人工操作交脚本/CI；schema/interface 变化同步 Ent/Wire 生成物与全部 stubs。

## 4. 标准检查清单

- `go -C backend test -tags=unit ./...`；schema/repository/gateway 高风险触达加 integration。
- Ent schema/migrations 触达跑 `go -C backend generate ./ent`；Wire 触达跑 `go -C backend generate ./cmd/server`。
- 前端触达跑 pnpm lint/typecheck；dist/embed 触达加 build。UI e2e 经 Playwright。
- agent contract 触达跑 `python3 scripts/export_agent_contract.py --check`。
- `./scripts/preflight.sh` 与 PR shape CI 验证 sentinel/删除/commit 契约；失败修复后重跑，不跳 hook。

## 5. PR body 模板

沿用 product-dev 的中文摘要/风险/验证/提交，并附通用 skill 的 upstream audit。统计由下一节脚本产生，不另抄英文模板或手算。

## 6. 完成后：本次 upstream merge 变更摘要（机械化）

```bash
bash scripts/release-rollout-summary.sh --mode upstream --fetch
```

审阅实际 upstream 删除及 sentinel 变更；PR audit 保留 `upstream/main..HEAD`、TK ahead count、backend diff stat。报告上游带入、B/C 类修复、重点 smoke 与未覆盖项；数字来自脚本，合并按钮与审批遵循根规则。

## 7. Red flags

见上方决策清单的项目参考；通用流程不在项目内复制。
