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

### 2. CLAUDE.md "Current Gateway Flow" 段未提 sticky routing

- **现象**：`docs/approved/sticky-routing.md` §10 计划在 CLAUDE.md 加一行 sticky routing 说明，未做。
- **决策**：下次合并 `newapi-as-fifth-platform` PR 时一并补（同一段"调度与转发"流程图）。
- **门禁**：暂无。

### 3. `scripts/export_agent_contract.py` 尚未为本仓库定制

- **现象**：`agent-contract-enforcement.mdc` 要求每个项目维护本地化的 contract 导出脚本；本仓库根目录有 placeholder（来自 main 上 WIP），但未对齐 TK 的 admin / gateway / setup / payment 路由结构。
- **决策**：随 `newapi-as-fifth-platform` PR 一并定制。届时 `scripts/preflight.sh` § 2 段同步上线（见 §7 中的 § 2 TODO 注释）。
- **门禁**：暂无（脚本完成后 § 2 段会拦截 contract drift）。

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
