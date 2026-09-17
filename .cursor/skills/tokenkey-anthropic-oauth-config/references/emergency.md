## 8. 附录：底层工具（emergency / debug）

正常流程**只走上面的命令**。下列工具在流水线 break 或紧急 rollback 时直接用：

- `ops/anthropic/check-edge-oauth-stability.py --edge-id E --account-name A [--json] [--emit-sql FILE]` — 单 edge OAuth tier baseline / TLS drift 只读检查；`--emit-sql` 按账号 live tier 渲染 TLS profile upsert + 绑定 SQL（再 base64 经 SSM 注入）。

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核——同样不允许跳过 §0 "先查后说"协议。

### 8.1 OAuth 账号 email 机队审计（probe-account-emails.sh）

CC gateway userEmail 回填读 `accounts.extra` / `credentials` 的 email 字段。缺 email 时 normalize 会**删除** client userEmail 行而非替换。

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-account-emails.sh
```

输出：`tk_anthropic_request_normalize_enabled` 设置 + 全账号 resolved_email + schedulable OAuth 缺 email 计数。

### 8.2 OAuth 账号 email 机队补全（apply-account-contact-email.sh）

Admin `account_email` 未发版或需批量补 edge 缺 email 时，按账号名写入四处 canonical 字段（与 `ApplyAccountEmail` 一致）：

```bash
bash ops/observability/run-probe.sh \
  --target edge:us5 \
  --env ACCOUNT_NAME=kiro-us5 \
  --env ACCOUNT_EMAIL=user@example.com \
  --script ops/observability/apply-account-contact-email.sh
```

输出：单行 `row_to_json`（含 `resolved_email`）。`ACCOUNT_NAME` / `ACCOUNT_EMAIL` 必填；脚本拒绝含引号/分号的字面量。
