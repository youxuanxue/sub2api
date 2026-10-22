# Host replacement：历史切流经验

当前工作流以 [pgBackRest 迁机](../../../../ops/stage0/pgbackrest-host-migration.md) 为准。
旧的 dump/按表尾差命令只供历史回查，不再用于 prod 新机迁移。

## Tokyo → 腾讯美西（2026-10-22）— 必记踩坑

回执：[tencent-cutover-20261022.json](../../../../docs/ops/reports/tencent-cutover-20261022.json)；
交接：[tencent-migration-handoff.md](../../../../docs/ops/tencent-migration-handoff.md)。

| 坑 | 现象 | 固化 |
|---|---|---|
| GitHub `PROD_SSM_TARGET` 钉死旧值 | workflow `vars \|\| auto` 被钉成 `aws-fresh`，CI/诊断打到冻结 Tokyo | 切流后立刻改 `auto`；旧身份失败关闭 |
| `ssm_execution` 漏腾讯分支 | `auto→tencent` 后落入 `else→hetzner` | glue 与 CLI resolver 同步；单测覆盖 live 模式 |
| host-parse stub 无腾讯 Hybrid | 切流后 preflight 挂（feishu sync 解析失败） | stub 随 live 矩阵注入 `mi-*` |
| `RuntimeDirectoryPreserve` | 身份 oneshot 结束清掉 `/run/...` tmpfs | 身份 timer 必须 Preserve=yes |
| 布局债未写进 SSOT | `.env` 在 `formal/`、容器 `tk-cutover-*`，但文档写 `/var/lib/tokenkey` | 当日走例外路径收敛；SSOT 写清 Docker Root 例外 |
| 先切流再慢慢补 | 临时布局/钉死旧 `PROD_SSM_TARGET`/漏 glue/契约滞后，多轮返工 | **从第一天按正式迁机终态 plan**；布局+控制面+备份告警+DNS+出口+SSOT 同窗收口，见 skill「正式迁机从第一天规划」 |
| 迁机后再排 Stage0 停机 | 切流用 cutover 临时布局，过几天再停机搬 Root/改名 | **plan 起就禁止**；布局目标写进 prepare，与 cutover **同窗**落地（例外路径：保留 Docker Root + 别名），见 [`stage0-layout-convergence.md`](../../../../deploy/tencent/stage0-layout-convergence.md) |
| unlock 契约未刷新 | 切流后仍写「未授权切流」 | cutover 当日改契约/清单 |
| DNS TTL 下限 | Porkbun TTL 不能回到 60 | 恢复到提供商允许的最小值（如 300） |
| 出口与主机解耦 | 主机已美西，账号仍走 JP Lightsail | 另批测延迟再迁 `proxy_id`；保留回滚清单 |
| 上游 503 ≠ 切流失误 | OpenAI overload 503 被误当成入口/DNS 问题 | verify 区分 client/upstream；对照 `ops_error` |
| Hetzner 路径名残留 | `deploy/hetzner/README` 仍是舰队 SSOT | 目录名历史遗留；targeting 必须失败关闭 |

## 更早：Tokyo 切流（2026-10-17）

历史回执：[Tokyo 切流](../../../../docs/ops/aws-tokyo-cutover-20261017.json)。

- SSM CommandId 先持久化再查询；结果超时不等于执行未发生。
- Docker 网络更换须保留 compose project/network labels；否则蓝绿部署可能无法启动。
- Caddy upstream 必须与 active-color 的 tokenkey-blue/green 对齐，不能指向裸 tokenkey。
- DNS 同时用公共 resolver 与 curl --resolve 验证；本机缓存不能作为切流结论。
- 默认事务只读不能隔离既有/超级用户写会话，写入隔离步骤见当前工作流。
- 新库接写后禁止直接 DNS 切回旧库，先保护新写入并验证反向同步方案。
