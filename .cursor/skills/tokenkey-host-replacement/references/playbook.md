# Host replacement：历史切流经验

当前工作流以 [pgBackRest 迁机](../../../../ops/stage0/pgbackrest-host-migration.md) 为准。
旧的 dump/按表尾差命令只供历史回查，不再用于 prod 新机迁移。
历史回执：[Tokyo 切流](../../../../docs/ops/aws-tokyo-cutover-20261017.json)。

- SSM CommandId 先持久化再查询；结果超时不等于执行未发生。
- Docker 网络更换须保留 compose project/network labels；否则蓝绿部署可能无法启动。
- Caddy upstream 必须与 active-color 的 tokenkey-blue/green 对齐，不能指向裸 tokenkey。
- DNS 同时用公共 resolver 与 curl --resolve 验证；本机缓存不能作为切流结论。
- 默认事务只读不能隔离既有/超级用户写会话，写入隔离步骤见当前工作流。
- 新库接写后禁止直接 DNS 切回旧库，先保护新写入并验证反向同步方案。
