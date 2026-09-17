## 6. 附录 A：底层工具（emergency / debug）

正常流程**只走 4 阶段流水线**。下列在流水线 break 或紧急 rollback 时直接用：

**手动写 priority**（自包含 SQL 模板，有 DO-block 校验；orchestrator 内部自动渲染；手动用需自起 `\set ...` 然后 base64 通过 SSM 注入）：

- `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql`

例如紧急回滚某 edge 某账号到 tier_base：

```bash
SQL=$(mktemp)
cat >"$SQL" <<EOF
\set account_name 'en-ld-ec2-16-1-b'
\set new_priority 20    -- l2 tier base
EOF
cat deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql >>"$SQL"
# One-line payload: GNU `base64 -w0` works; portable (macOS/OpenBSD): strip newlines after encode.
B64=$(base64 <"$SQL" | tr -d '\n')
aws ssm send-command --region eu-west-2 --instance-ids i-xxx \
  --document-name AWS-RunShellScript \
  --parameters "commands=[\"set -euo pipefail\necho $B64 | base64 -d | sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1\"]"
```

**底线**：手动绕开 orchestrator 时 op 必须自己做 apply 后复核 —— 同样不允许跳过 § 2 "先查后说"协议。
