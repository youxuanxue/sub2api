## 完成后：当前代码与上一 tag 的变更摘要（机械化）

本地栈验证通过后（A+B，或 A+B+C），调用与 release-rollout / upstream-merge 共享的摘要脚本：

```bash
bash scripts/release-rollout-summary.sh --mode local
# 输出 markdown：Range（上个 v* tag → HEAD）/ Commits / Top changed files
#               / Sentinel changes / Upstream file deletions
```

基于输出，向用户呈现（无变更则跳过对应行）：

**当前代码领先 `${LAST_TAG}` N 个提交，运行于本地栈 `:8088`**

- **feat / fix 提交**：列出关键条目及影响模块（gateway / scheduler / frontend / sentinel）
- **高风险路径**（根据 diff 判断）：
  - Gemini 路径改动 → 本地 C 节有无跑 Gemini 探针；tool-schema 清理是否已验证
  - OpenAI-compat / Responses 改动 → chat completions shape 与 reasoning_tokens 是否正常
  - pricing / model-list → `/v1/models` 返回与预期是否一致
  - frontend 改动 → 浏览器打开 `http://127.0.0.1:8088` 手动验证关键页面
  - sentinel 新增 → 列出文件名，后续 upstream merge 时 CI 会联检
- **尚未覆盖的验证**：若本次本地测试未跑 C 节（缺 API key），建议在发版前用 prod smoke 补全
