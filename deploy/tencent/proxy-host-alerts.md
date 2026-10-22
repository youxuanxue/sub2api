# 代理主机飞书告警（SOCKS egress）

**Owner：** 本目录 `install-proxy-host-alerts.sh` + `tokenkey-proxy-host-watch.sh`；  
盘/内存复用 [`../hetzner/tokenkey-disk-metrics-prod.sh`](../hetzner/tokenkey-disk-metrics-prod.sh)。

## 不变式

- **每一台**生产 SOCKS 出口机（现网：美西 `usw-eval1..4`、北京 `cn-ip1..5`）必须安装并启用：
  - `tokenkey-disk-metrics.timer`（盘/内存）
  - `tokenkey-proxy-watch.timer`（`tokenkey-proxy` + `:1080`）
- **新机 / 换机 / 重装**后，在装完 GOST/`tokenkey-proxy` 之后立刻跑本安装器；未装齐不算交付。
- 交付前必须跑 `install-proxy-host-alerts.sh --verify`（无写操作；缺 timer / 脚本 / 0600 凭据即失败）。
- 活绑定以 prod / cn-prod `proxies` 为准；本目录不复制 IP 表。
- 凭据只在主机 `/opt/tokenkey/host-alerts.env`（0600）；**不进 git**。
- 应用侧 `proxy_unreachable_count` P0 是另一层（网关拨测）；不能替代本机 timer。

## 安装

在目标机 root：

```bash
TOKENKEY_PROXY_NODE_NAME=cn-ip1 \
  bash install-proxy-host-alerts.sh /secure/path/host-alerts.env
```

可选覆盖脚本路径：第 2/3 参数分别为 disk-metrics、proxy-watch。默认指向本仓上述 owner。

## 验收（换机必跑）

```bash
bash install-proxy-host-alerts.sh --verify
```

断言：两 timer `active`、`/usr/local/bin/tokenkey-{disk-metrics,proxy-host-watch}.sh --selftest` 通过、`/opt/tokenkey/host-alerts.env` 存在且 mode 600。

## 舰队入口

节点/IP 清单仍见历史路径下的现网 SSOT：[`../hetzner/README.md`](../hetzner/README.md)（目录名历史遗留，内容为腾讯美西舰队）。
