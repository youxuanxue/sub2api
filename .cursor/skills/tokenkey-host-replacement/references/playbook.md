# Host replacement — 命令骨架与切流踩坑

现网：`deploy/hetzner/README.md`。回执：`docs/ops/aws-tokyo-cutover-20261017.json`。

## Prod → 新 EC2

**控制面：** 独立 AWS SSM；CommandId 先落盘再询.

**Hold：** `DATA_SYNC_HOLD` + 仅 postgres + 网络 `Internal=true`，至 final delta 与激活批准。

**冻结：** `ALTER DATABASE tokenkey SET default_transaction_read_only = on;`（禁 `ALTER SYSTEM`/事务块）。停 app/Caddy/`tokenkey.service`/pgdump·QA timers，`pg_terminate_backend`，断言 `transaction_read_only=on`。

**补数：** 新 S3 前缀；precious data-only + usage/dedup 尾差；hold 下 DELETE→`pg_restore --disable-triggers`→COPY→`setval`→对账。

**激活：** bluegreen compose=源端；`active-color` ↔ Caddy `tokenkey-(blue|green)`（禁裸 `tokenkey:`）；systemd helpers + `migration-hold.conf`；DNS 前同步 `caddy/data`；网络见硬坑 1。

**DNS：** `porkbun-upsert-a.sh`；`dig @8.8.8.8` + `curl --resolve`。

## 硬坑

1. **网络 label：** 缺 `com.docker.compose.network=tokenkey-network` / `project=tokenkey` → bluegreen 起不来。`replace_network` 须复制 Labels；开公网时 `Internal=false` 不得丢 label；compose 宜 `external: true`+固定 `name`。
2. **冻结：** 只用 `ALTER DATABASE … read_only`。
3. **Caddy：** upstream 必须匹配 blue/green `active-color`。
4. **Schema：** QA 小时分区漂移勿当失败（已排除 `qa_records%`）。
5. **DNS：** 公共 resolver；本机可能仍解析旧 IP（假失败）；SG 放行 443/80。
6. **Hostname：** `expected_host('prod')` = 活主机名（现 `tokenkey-prod-tokyo-20261017`）。

**回滚：** 新机接写后禁止 DNS 直切并解冻旧库。详见 [交接回滚](../../../../docs/ops/aws-tokyo-cutover-handoff-20261017.md#回滚dns-已切)。
