# AWS prod → Tokyo 切流交接清单

更新时间：2026-10-17 03:13 UTC

## 当前结论

**东京已对外提供服务；美国源冻结 standby；源端 drain/停机未做。**

- 活 prod：`i-045f062ce33e1a5af` / `3.114.187.250` / `ap-northeast-1` / hostname `tokenkey-prod-tokyo-20261017`
- 回滚 DNS 目标（如需）：`23.21.138.205`
- 完整回执：[aws-tokyo-cutover-20261017.json](aws-tokyo-cutover-20261017.json)

## 已完成窗口（获批范围）

批准文本：`批准顺序开切流，直到东京节点提供服务。`（2026-10-17T02:52:58Z）

1. 源端 `ALTER DATABASE … default_transaction_read_only=on` 冻结；writers 停止。
2. 最终 delta 导出并 apply；冻结水位 usage/dedup max `14727151` / `15538078`；delta 各 2214 行。
3. 解 hold，蓝绿激活 `tokenkey-blue` + Caddy/Redis；本机与四域名 `/health` 200。
4. Porkbun A 记录切到 `3.114.187.250`（TTL 300）。
5. 四边 `MAIN_GATEWAY_ALLOWED_CIDR` 热更新含东京 IP；`tokenkey-pgdump.timer` 已启用。

## 仍须单独批准

- 美国源 drain / 停机 / 删除（当前保持 frozen + PG/Redis，无 80/443 监听）。

## 回滚（DNS 已切）

1. 四域名 A 改回 `23.21.138.205`
2. 源端解除 `default_transaction_read_only`，恢复 blue/Caddy writers
3. 勿用东京整库覆盖美国源；勿双写

## 连接

```bash
aws ssm start-session --region ap-northeast-1 --target i-045f062ce33e1a5af
aws ssm start-session --region us-east-1 --target i-022f2553503211775
```
