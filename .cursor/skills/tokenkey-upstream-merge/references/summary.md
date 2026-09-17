## 6. 完成后：本次 upstream merge 变更摘要（机械化）

PR 全部检查通过、准备合并（或刚完成合并）后，调用与 release-rollout / local-deploy 共享的摘要脚本（`--mode upstream` 启用 upstream 专属段，含 TK ahead 计数 + backend diff stat 满足 §5.y 审计需求）：

```bash
bash scripts/release-rollout-summary.sh --mode upstream --fetch
# 输出 markdown：
#   Summary / Range (merge_base..HEAD) / Commits
#   Top changed files / Sentinel changes / Upstream file deletions
#   Upstream brought in (merge_base..upstream/main)
#   TK ahead count (PR body §5.y audit cadence)
#   Backend diff stat vs upstream/main (PR body §5.y)
```

基于输出，向用户呈现以下结构：

**upstream merge 范围：`<merge_base_short>` → `upstream/main`（N 个上游提交）**

**上游带入**：按影响维度分类（handler / service / frontend / schema / CI），每类列 1–3 行关键提交。

**TK invariant 修复**（B 类 commit）：列出修复的不可退让项及改动文件。

**TK OPC 收敛**（C 类 commit，如有）：列出从热点文件抽取到 companion 的内容。

**需要在 prod smoke / 本地测试中重点验证**（根据实际变更填写）：

| 触达路径 | 验证方式 |
|---|---|
| Gemini 路径 | 统一 smoke key + `TK_SMOKE_GEMINI_MODELS` 的 Gemini tool-schema 探针；HTTP 400=硬失败需回查 |
| OpenAI-compat / Responses | 统一 smoke key + `TK_SMOKE_OPENAI_OAUTH_MODELS` 的 OpenAI OAuth 探针；`reasoning_tokens` 是否透传 |
| pricing / model-list | `/v1/models` 数量与可用性标记 |
| frontend 组件 | frontend release asset 探针 + 浏览器关键页 |
| 新增/合入 admin 视图（`frontend/src/views/admin/**`） | **TK 持久壳不可退让**：上游新 admin 视图自带 `<AppLayout>` 包裹，必须①剥掉 `<AppLayout>`（布局由 `AdminShellView.vue` 持久壳统一提供）②把路由注册进 `frontend/src/router/admin.tk.ts` 的 `AdminShellView` children（**不要**在 `router/index.ts` 内联）。`scripts/checks/admin-shell-layout.py`（preflight 内）会机械拦截漏剥的 `<AppLayout>` |
| `router/index.ts` 冲突 | admin 路由子树已隔离到 `frontend/src/router/admin.tk.ts`；冲突应只发生在非 admin 路由，admin 路由变更解析到 `admin.tk.ts` |
| 新增 sentinel | 列出 `scripts/sentinels/*.json` 文件名，说明守卫的回归场景 |
| upstream 删除文件（如有） | 逐一确认 PR description 有 (a)/(b)/(c) 回归说明 |

**后续建议**：是否需要立即 bump VERSION 发版，或等待下一批 TK 功能合入。
