# Preflight Debt Log

仅保留「仍在 open 的债务」和必要的 closed 里程碑，避免日志本身再次漂移。

---

## Open Debt

### D-001 `TestUS201_*` ↔ `US-006` 命名不一致（不影响行为）

- 状态：**Accepted / Not fixing**
- 现状：代码、故事、设计文档都一致使用 `TestUS201_*`，仅与故事 ID `US-006` 前缀不一致。
- 理由：重命名 22 个测试函数收益低；行为风险可由现有测试覆盖。
- 约束：后续新增 sticky-routing 测试统一用 `TestUS006_*`。

### D-002 Agent contract 仍是 audit 模式（未做 prefix-resolving generator）

- 状态：**Open**
- 当前门禁：`scripts/export_agent_contract.py --check` 可 hard-fail 关键平台遗漏；route-count 仍是 warning。
- 缺口：Gin 嵌套 `Group()` 前缀拼接尚未自动化，无法稳定生成完整 endpoint 清单。
- 截止：下次路由族系重构前。

### D-003 US-008/009/010 e2e 缺口

- 状态：**Open**
- 现状：US-008/009/010 仍是 `Draft`，缺 testcontainer 端到端验证。
- 原因：需要真实 PG migration + newapi upstream stub，工作量约 0.5-1d。
- 退出条件：
  - `go test -tags=integration -run 'TestUS00[89]_HTTP_|TestUS010_HTTP_' ...` 全绿；
  - 附 integration 日志证据；
  - 三个故事从 `Draft` 升级（InTest/Done）。
- 截止：2026-05-03。

### D-004 未拦截 secrets 写入 commit/PR/body/docs

- 状态：**Open / P0**
- 现状：`safe-shell-commands` 只管破坏性命令，不管凭据泄露文本。
- 计划：
  1. dev-rules 增加 no-secrets 规则；
  2. `preflight` 增加 staged diff + commit message secrets regex gate；
  3.（可选）`gh pr create` wrapper。
- 截止：2026-05-10。

---

## Closed Milestones

### C-001 Story↔Test 机械对齐（原 §5）— **Closed 2026-04-20**

- 落地：`.testing/user-stories/verify_quality.py`
- 门禁：`dev-rules/templates/preflight.sh` §5（脚本存在即 hard-fail）
- 备注：状态行支持 `- Done` / `- [x] Done` / `- [ ] Draft`。

### C-002 Worktree 下 preflight §2 假失败（原 §7）— **Closed 2026-04-20**

- 根因：hook 上下文 `GIT_DIR` 污染子模块 git 调用。
- 修复：dev-rules `git_sub()` 统一清理 git context env vars。

### C-003 Skip-marker 触发 release 跳过（原 §8）— **Closed 2026-04-20**

- 修复：统一强制 `bash scripts/release-tag.sh vX.Y.Z`；README/CLAUDE 口径对齐。

### C-004 Stage-0 数据卷拆分（原 §9.b）— **Closed 2026-04-20**

- 落地：`DataVolume` + `VolumeAttachment` + `.env.secret` 持久化。
- 结果：实例替换时数据不再跟 root EBS 一起丢失。
- 备注：旧栈首次迁移仍需按 `deploy/aws/README.md` 停机 runbook 执行。

---

## Historical Notes

- 2026-04-18 sticky-routing 先实施后审批（已由 approved-doc gate 兜底）。
- 2026-04-19 preflight 与 approved-doc 校验上提到 dev-rules（本地与 CI 调用同一套模板）。
