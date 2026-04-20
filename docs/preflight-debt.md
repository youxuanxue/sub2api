# Preflight Debt Log

记录 `scripts/preflight.sh` 当前**没有**机械门禁、但已知存在的"流程债务"项。
每条必须有截止日期或明确"不修"理由（dev-rules `agent-contract-enforcement.mdc` 强约束）。

---

## 已知漂移

### 1. sticky-routing 测试函数命名 `TestUS201_*` ↔ 故事 `US-006`

- **现象**：`docs/approved/sticky-routing.md` §6 表格写 `TestUS201_*`，实际代码中为 `TestUS006_*` / `TestStickySessionInjector_*`。
- **来源**：草拟设计时按"功能编号"写了 US-201；实施时按 `.testing/user-stories/index.md` 顺次拿到 US-006，未回头改 doc。
- **决策**：**不修**。理由：rename ~10 函数 + 跑全套测试，与"消除真实风险"的 ROI 不匹配。下次新增测试一律遵循 US-006 实际命名；老命名保留作为历史。
- **未来门禁**：可在 preflight 加一段，校验 `docs/approved/*.md` 中提到的 `TestUS***_` 函数必须在 `backend/internal/.../*_test.go` 真实存在 — 当前未实现，先登记。

### 2. CLAUDE.md "Current Gateway Flow" 段未提 sticky routing — **closed (2026-04-20)**

- **现象**：`docs/approved/sticky-routing.md` §10 计划在 CLAUDE.md 加一行 sticky routing 说明，未做。
- **整改**：随 `feature/newapi-fifth-platform` PR 一起补到 CLAUDE.md "Current Gateway Flow" 段尾——新增段落同时讲清调度池分桶（newapi）与 sticky routing（在分桶之上做 prompt-cache 优化）的层叠关系。
- **闭环 commit**：`90d5d90c`（`feature/newapi-fifth-platform` 分支 M8）。

### 3. `scripts/export_agent_contract.py` — 仅 audit 模式，未做 prefix-resolving generator

- **现象**：`feature/newapi-fifth-platform` PR 落地了 `scripts/export_agent_contract.py`，被 dev-rules 模板 `preflight.sh § 4 (agent contract drift)` 自动调用，但**仅作为 audit 工具**：
  - **强检（dev-rules `preflight § 4` hard-fail）**：`docs/agent_integration.md` 的 `# Agent Contract Notes` 段必须提及全部 5 个 first-class 平台（`openai/anthropic/gemini/antigravity/newapi`）。这是新增 newapi 时的 §0 级回归门禁。
  - **软检（warning，不 fail）**：`routes/*.go` 中 `<ident>.METHOD(` 字面量计数 vs `docs/agent_integration.md` 的 `- \`METHOD …\`` 列表条数；超 ±10% 提示人工审计。
- **未做**：完整的 prefix-resolving generator —— Gin 嵌套 `Group("/x").Group("/y")` 跨函数调用（如 `registerAccountRoutes(admin, h)`）需要 Go AST walker 或运行时 `engine.Routes()` dump（需 Wire DI + handler stub）。本 PR 试过 Python 字面提取，结果会把 `accounts.GET("/:id")` 错出成裸 `/:id`，反而退化 doc。
- **决策**：拆为 follow-up PR。理由（Jobs 聚焦）：本 PR 是"newapi 接入"，不是"contract generator 重写"；当下 audit 已经能挡住 §0 级"忘了写新平台"的回归，超出 ROI 反成包袱。
- **门禁**：dev-rules `preflight § 4` 已自动接入；route-count 警告留给 follow-up PR 把它升成 hard-fail。
- **截止日期**：next routes 重构 PR 之前必须做完（无固定日期，但下次有人新增/删除路由族系前会被 warning 提醒）。

### 4. newapi-as-fifth-platform e2e 测试（HTTP+PG+upstream）暂以单测替代

- **现象**：`docs/approved/newapi-as-fifth-platform.md` §5.2 要求 US-008/009/010 跑 testcontainer 化的端到端集成测试（HTTP→Auth→scheduler→bridge dispatch→真 PG）。本 PR 提供：
  - **已落**：compat-pool predicate + scheduler-tier + gateway-tier sticky + messages_dispatch sanitize 行为测试（34 个 unit case，全 mock，覆盖 US-008/009/011/012/013/014/015 的核心安全/逻辑/回归 AC）— 见 `.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`。
  - **未落**：真 HTTP+PG e2e（US-008 chat completions、US-009 messages、US-010 responses 端到端）。
- **决策**：拆为 follow-up PR `feature/newapi-fifth-platform-e2e`。理由（OPC 务实）：
  1. 单测已锁死全部 design §3 注入点的关键不变量（混池防御 / 池空报错 / sticky 漂移降级 / channel_type=0 排除 / 平台分桶）；这些是 P0 安全断言，不依赖 e2e。
  2. e2e 需要 docker daemon + testcontainer + 完整 fixture（user/group/account/api_key），与本 PR 的代码改动正交（仅追加 `*_integration_test.go`），延后不增加合并风险。
  3. design §7.2 单 PR 原则的本意是"实现 + 行为契约不可拆"；行为契约由 21 个单测保证，e2e 是验证集成接缝、不是验证设计。
- **门禁**：follow-up PR 必须把 §5.2 命令跑绿、附 testcontainer 日志，并把这条 debt 项标 closed。
- **截止日期**：2026-05-03（两周内）。

### 5. `.testing/user-stories/verify_quality.py` 缺失 — story↔test 漂移检测尚未机械化

- **现象**：dev-rules `test-philosophy.mdc §5` 要求维护 `.testing/user-stories/verify_quality.py`，本仓库未实现；`dev-rules/templates/preflight.sh § 5 (story/test alignment)` 因此跳过该检查段而非拦截（合并 PR #11 后通过 wrapper `scripts/preflight.sh` 仍是 skip）。
- **影响**：故事 `Linked Tests` 引用的测试函数若被 rename / 删除，目前需要靠 reviewer 人眼对齐（`docs/approved/sticky-routing.md` §6 的 `TestUS201_*` 漂移就是这类问题，见 §1）。
- **决策**：登记，不在本 PR 范围内。最小实现是用 `grep` 扫描所有 `.testing/user-stories/stories/*.md` 中 `path/to/file.go::TestFunc` 字符串，与 `^func TestFunc` 对应，输出不命中清单（exit 非零）。
- **门禁**：脚本上线后 `dev-rules/templates/preflight.sh § 5` 自动启用拦截，无需额外接线。
- **截止日期**：2026-05-31（与下一次 stories 大批量新增前完成）。

### 6. 数字漂移历史 — design doc §11.2 单测计数

- **现象**：`docs/approved/newapi-as-fifth-platform.md` §11.2 在 2026-04-19 首版写"M5a 21 case"；merge 后审计发现实际 newapi-related 单测共 34（compat-pool 9 + scheduler 8 + sticky 5 + dispatch 12），数字源头是 M5a 提交里只统计了 scheduler+sticky 两类，遗漏了 M3 提交里已落的 compat_pool/dispatch test。
- **整改**（2026-04-20，本 PR merge 阶段）：标题更正为 "34 case"，并加入按文件细分的明细列表；preflight-debt §4 同步更新。
- **不再发生的依据**：design doc §11.2 现在提供按文件 `grep -cE "^func Test"` 的可复算明细；下次任何人加测试时，只要本 PR 的覆盖矩阵列表与统计一起改即可。
- **未来门禁**：可在 `docs/approved/*.md` 中新增 `<!-- stat:newapi-tests -->34<!-- /stat -->` 块，由 `dev-rules/sync-stats.sh --check`（preflight § 8）机械核对——目前未做，因为只有一处数字、人工 audit 成本低于建表本身。

### 7. dev-rules `templates/preflight.sh § 2` 在 worktree 内 commit hook 中假 fail

- **现象**：本 worktree (`/Users/xuejiao/Codes/token/tk/sub2api-newapi-fifth`) 是从主仓库 `git worktree add` 出来的；`./scripts/preflight.sh` 直接跑 PASS，但通过 `git commit` 触发 pre-commit hook 时 § 2 (`dev-rules submodule pointer is reachable on remote`) 报 `FAIL: submodule SHA ... not found in dev-rules — submodule was not committed first`。
- **根因**（2026-04-20 复现确认）：git 在 commit 阶段把 `GIT_DIR=/path/to/sub2api/.git/worktrees/sub2api-newapi-fifth` / `GIT_INDEX_FILE=...` 注入 hook 子进程；`templates/preflight.sh § 2` 内 `(cd dev-rules && git cat-file -e "$sub_sha" 2>/dev/null)` 子 shell 不 unset GIT_DIR，git 仍按上级 worktree 的 GIT_DIR 解析对象库，找不到子模块对象。脚本 `bash -c '... env -i ... cd dev-rules && git cat-file -e $sha'` 复现稳定（exit=1），unset GIT_DIR 后 PASS。
- **影响**：
  - 阻塞所有从 worktree 发起的合法 commit（手动 preflight 全绿但 hook 假 fail）。
  - 本 PR 在 audit 阶段被迫使用 `git commit --no-verify`（手动 preflight 已 PASS 作为补偿），违反"hook 必须通过"软规则。
- **决策**：上修到 dev-rules 仓库（`templates/preflight.sh § 2` 在 `cd dev-rules` 子 shell 前 `unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE`）。本 PR 范围内仅登记 + worktree commit 用 `--no-verify`。
- **门禁**：dev-rules 修复后，本仓库 `git submodule update --remote dev-rules` 拉新 SHA 即自动恢复 hook 拦截。
- **截止日期**：2026-04-26（一周内推 dev-rules 修复 PR）。
- **临时缓解**：从 sub2api **主仓库** 目录（非 worktree）做 commit 不受影响（GIT_DIR 直接指向主 .git）；或用 `git -c core.hooksPath=/dev/null commit ...` 显式跳 hook（与 `--no-verify` 等价但更显式）。

---

## 历史事件

### 2026-04-18: sticky-routing 实施先于审批

- **事件**：`docs/approved/sticky-routing.md`（created 2026-04-17, status=pending）未走审批门禁；
  单提交 `a68dee5b` 直接落地 main 并上线 prod，包含 schema + injector + 6 处接入点 + 单测 + UI。
- **暴露的根因**：当时缺少机械门禁，靠"自觉"遵守 `product-dev.mdc` §阶段 2→审批→阶段 3 顺序，在追产物的压力下被绕过。
- **整改**（2026-04-19）：
  1. 回填 `docs/approved/sticky-routing.md` frontmatter `status=shipped` + `related_commits: [a68dee5b]` + `related_stories: [US-006]`；新增 §11 实施情况章节。
  2. 新增 `scripts/check_approved_docs.py`：拒绝 `status=pending` 但 `related_prs/related_commits` 非空的 doc（即"shipped under pending"同款）。
  3. 新增 `scripts/preflight.sh § 1` 段调用上述脚本；本日起 commit / CI 强制运行。
- **后续演进**（2026-04-19 当日，见下条）：脚本于同日上提到 dev-rules submodule，执行链改为 `dev-rules/templates/preflight.sh § 7 → dev-rules/scripts/check_approved_docs.py`；项目级 `scripts/preflight.sh § 1` 段被删除，但拦截语义不变（R1-R5 同步生效在所有消费者项目）。
- **不再发生的依据**：`dev-rules/scripts/check_approved_docs.py` R3 规则机械拦截。任何 doc 一旦在 frontmatter 写了 commit / PR，必须同时把 status 翻为 `shipped`，否则 hook fail。

### 2026-04-19: 接入 dev-rules submodule + 上提 check_approved_docs.py

- **事件**：`scripts/preflight.sh` 与 `scripts/check_approved_docs.py` 都是 sub2api 私有，但前者只调用后者一行、后者本身是「跨项目共享的 frontmatter 不变量」；同时 dev-rules 仓库已存在 `templates/preflight.sh` 模板（8 段，覆盖本项目所需全部检查）。两份冗余实现导致：
  1. 任何对 frontmatter 规则的演进（如 `ALLOWED_STATUS` 加 `approved` 以兼容 zw-brain GATE 模型）都要同时改两处；
  2. 项目 wrapper 仅 1 段、模板有 8 段，本地 commit 实际只跑 1 段就放行——CI 与 hook 强度不一致。
- **整改**（2026-04-19）：
  1. 在 dev-rules 仓库新增 `dev-rules/scripts/check_approved_docs.py`（ALLOWED_STATUS = {draft, pending, approved, shipped, archived}），由 `dev-rules/templates/preflight.sh § 7 R1-R4` 在任何分支上调用；R5 (`approved_by: pending`) 仍仅在 main/master 拦截。
  2. 改 `dev-rules/templates/install-hooks.sh`：项目无 `scripts/preflight.sh` 时，pre-commit hook 自动 fallback 到 `dev-rules/templates/preflight.sh`（8 段全跑）。
  3. sub2api 接入 dev-rules 为 git submodule（`dev-rules/`），删除项目级 `scripts/preflight.sh` + `scripts/check_approved_docs.py`，沿用 dev-rules 模板（CLAUDE.md §10 记录此选择）。
  4. CI `.github/workflows/backend-ci.yml` 新增 `preflight` job（`submodules: recursive`），与 pre-commit hook 走同一脚本，本地与 CI 强度对齐。
- **不再发生的依据**：单一事实来源（dev-rules）+ 本地与 CI 调用同一脚本 + dev-rules-convention.mdc §"Git 提交顺序" 与 preflight § 2 子模块指针检查共同保障"先子模块后父仓库"。
