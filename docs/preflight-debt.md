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
- **闭环 commit**：M8 of `feature/newapi-fifth-platform` 分支。

### 3. `scripts/export_agent_contract.py` — 仅 audit 模式，未做 prefix-resolving generator

- **现象**：本 PR 落地了 `scripts/export_agent_contract.py`，但**仅作为 audit 工具**：
  - **强检（preflight § 3 hard-fail）**：`docs/agent_integration.md` 的 `# Agent Contract Notes` 段必须提及全部 5 个 first-class 平台（`openai/anthropic/gemini/antigravity/newapi`）。这是新增 newapi 时的 §0 级回归门禁。
  - **软检（warning，不 fail）**：`routes/*.go` 中 `<ident>.METHOD(` 字面量计数 vs `docs/agent_integration.md` 的 `- \`METHOD …\`` 列表条数；超 ±10% 提示人工审计。
- **未做**：完整的 prefix-resolving generator —— Gin 嵌套 `Group("/x").Group("/y")` 跨函数调用（如 `registerAccountRoutes(admin, h)`）需要 Go AST walker 或运行时 `engine.Routes()` dump（需 Wire DI + handler stub）。本 PR 试过 Python 字面提取，结果会把 `accounts.GET("/:id")` 错出成裸 `/:id`，反而退化 doc。
- **决策**：拆为 follow-up PR。理由（Jobs 聚焦）：本 PR 是"newapi 接入"，不是"contract generator 重写"；当下 audit 已经能挡住 §0 级"忘了写新平台"的回归，超出 ROI 反成包袱。
- **门禁**：preflight § 3 已上线；route-count 警告留给 follow-up PR 把它升成 hard-fail。
- **截止日期**：next routes 重构 PR 之前必须做完（无固定日期，但下次有人新增/删除路由族系前会被 warning 提醒）。

### 4. newapi-as-fifth-platform e2e 测试（HTTP+PG+upstream）暂以单测替代

- **现象**：`docs/approved/newapi-as-fifth-platform.md` §5.2 要求 US-008/009/010 跑 testcontainer 化的端到端集成测试（HTTP→Auth→scheduler→bridge dispatch→真 PG）。本 PR 提供：
  - **已落**：scheduler-tier + gateway-tier 行为测试（21 个 unit case，全 mock，覆盖 US-008/009/011/012/013/014/015 的核心安全/逻辑/回归 AC）— 见 `.testing/user-stories/attachments/us-newapi-unit-run-2026-04-19.txt`。
  - **未落**：真 HTTP+PG e2e（US-008 chat completions、US-009 messages、US-010 responses 端到端）。
- **决策**：拆为 follow-up PR `feature/newapi-fifth-platform-e2e`。理由（OPC 务实）：
  1. 单测已锁死全部 design §3 注入点的关键不变量（混池防御 / 池空报错 / sticky 漂移降级 / channel_type=0 排除 / 平台分桶）；这些是 P0 安全断言，不依赖 e2e。
  2. e2e 需要 docker daemon + testcontainer + 完整 fixture（user/group/account/api_key），与本 PR 的代码改动正交（仅追加 `*_integration_test.go`），延后不增加合并风险。
  3. design §7.2 单 PR 原则的本意是"实现 + 行为契约不可拆"；行为契约由 21 个单测保证，e2e 是验证集成接缝、不是验证设计。
- **门禁**：follow-up PR 必须把 §5.2 命令跑绿、附 testcontainer 日志，并把这条 debt 项标 closed。
- **截止日期**：2026-05-03（两周内）。

---

## 历史事件

### 2026-04-18: sticky-routing 实施先于审批

- **事件**：`docs/approved/sticky-routing.md`（created 2026-04-17, status=pending）未走审批门禁；
  单提交 `a68dee5b` 直接落地 main 并上线 prod，包含 schema + injector + 6 处接入点 + 单测 + UI。
- **暴露的根因**：当时缺少机械门禁，靠"自觉"遵守 `product-dev.mdc` §阶段 2→审批→阶段 3 顺序，在追产物的压力下被绕过。
- **整改**（2026-04-19）：
  1. 回填 `docs/approved/sticky-routing.md` frontmatter `status=shipped` + `related_commits: [a68dee5b]` + `related_stories: [US-006]`；新增 §11 实施情况章节。
  2. 新增 `scripts/check_approved_docs.py`：拒绝 `status=pending` 但 `related_prs/related_commits` 非空的 doc（即"shipped under pending"同款）。
  3. 新增 `scripts/preflight.sh` § 1 段调用上述脚本；本日起 commit / CI 强制运行。
- **不再发生的依据**：scripts/check_approved_docs.py R3 规则机械拦截。任何 doc 一旦在 frontmatter 写了 commit / PR，必须同时把 status 翻为 `shipped`，否则 hook fail。
