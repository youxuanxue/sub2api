---
name: tokenkey-upstream-merge
description: >-
  TokenKey upstream merge workflow for importing Wei-Shaw/sub2api upstream/main. Use when merging or reviewing upstream drift, preparing an upstream merge PR, or maintaining recurring upstream update discipline.
---

# TokenKey upstream merge SOP

适用于 `merge/upstream-*` 分支。权威纪律仍以根目录 `CLAUDE.md` 与 `docs/global/tokenkey-opc-transformation-plan.md` 为准。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。本 skill **绝大多数是真判断**（commit 形状、决策清单、red flags 都是架构 / 风险判断），机械化部分主要在「准备」和「完成后摘要」。

| 步骤 | 类型 | 承载 |
|---|---|---|
| upstream drift / fetch / merge-tree dry-run | 机械 | `bash scripts/upstream/check-drift.sh` + `git merge-tree upstream/main HEAD` |
| 生成代码（Ent / Wire / frontend dist） | 机械 | `go generate ./ent` / `go generate ./cmd/server` / `pnpm build` |
| sentinel 一致性 + upstream-touch marker 强制 | 机械 | `bash scripts/preflight.sh`（含 sentinel registry + upstream-override-marker） |
| 完成后摘要（upstream brought-in + TK ahead + backend diff stat for PR body §5.y） | 机械 | `bash scripts/release-rollout-summary.sh --mode upstream --fetch` |
| Commit 形状（Harness / Invariant / OPC 三类） | 判断 | prompt（架构区分） |
| §3 决策清单 7 项 | 判断 | prompt（每条都需爆炸半径 + 兼容度判断） |
| §7 Red flags | 判断 + 机械门禁 | prompt + sentinel workflow `upstream-merge-pr-shape.yml` |
| 周期探测（behind → issue） | 机械 | `.github/workflows/upstream-merge-notify.yml` + `scripts/upstream/notify-merge-needed.py` |

## 0. 流程心智

周期任务只做探测：`upstream-merge-notify.yml` 发现 `origin/main` 落后于 `upstream/main` 时，开或更新一条标题以 `[upstream-merge]` 开头的跟踪 issue（label 尽力附加，不阻断建单），等人决定是否 merge。

真正的 merge 由人类按本 skill 在 `merge/upstream-YYYYMMDD` 上手动做；形状门禁仍由 `upstream-merge-pr-shape.yml` 强制。CI 不得 `git merge`、不得开 merge PR、不得派 headless agent。

## 1. 不可逾越原则

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
3. 确认 main 与 upstream 状态：`bash scripts/upstream/check-drift.sh`。
   将本轮审阅的 upstream 完整 SHA 写入 `.upstream-ref`；PR 门禁验证该固定目标已是 `HEAD` 的祖先，日常漂移报告仍比较两端 main。新目标必须经本轮审阅后更新，不能用回退目标消除失败。
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
- `./scripts/preflight.sh`（覆盖所有 sub2api sentinel 检查；`upstream-merge-pr-shape.yml` 对 merge/upstream-* PR 在 CI 中复跑与 preflight **对齐**的门禁，含 newapi、brand、frontend-tk、gateway-tk、redaction、trajectory、terminal、engine、QA 数据集、**pricing-availability** 与 **sentinel 注册表更新门闸（覆写防护）**；完整清单见该 workflow 文件头注释。）

不得跳过 hook 或用 `--no-verify`。

## 5. PR body 模板

生成 PR 时读取模板；提交形状仍由 upstream-merge 通用 skill 裁决。 见 [操作细则](references/pr-body.md)。

## 6. 完成后：本次 upstream merge 变更摘要（机械化）

完成合并后调用 `bash scripts/release-rollout-summary.sh --mode upstream --fetch`；需要展开摘要字段时读取。 见 [操作细则](references/summary.md)。

## 7. Red flags

处理冲突、兼容性或审计异常前读取此检查清单。 见 [操作细则](references/red-flags.md)。
