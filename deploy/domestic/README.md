# UCloud domestic prod

审批范围和费用见 [design-domestic-cluster.md](../../docs/approved/design-domestic-cluster.md)。
这是独立 UCloud 目标，不使用 Hetzner dispatcher、现网 SSM 或现网数据库。

## 当前目标

| 项目 | 值 |
|---|---|
| 公共入口 | https://api-cn.tokenkey.dev |
| 项目 / 地域 / 可用区 | `org-bpx31r` / `cn-bj2` / `cn-bj2-02` |
| 主机 | `uhost-1wgyn7gu2f6a`，2 CPU / 4 GiB |
| 系统盘 / 数据盘 | 40 GiB / 20 GiB SSD |
| 数据盘 | `bsm-1wgz3hm0c0jb`，挂载 `/var/lib/tokenkey` |
| 公网 | `eip-1wgywcyzw8bs`，`106.75.17.57`，5 Mbps BGP |
| 防火墙 | `firewall-pvx1loip`，80/443 公网，22 仅运维出口 /32 |
| 管理员 | `admin@cn.tokenkey.dev` |
| 客户 | `rt@tk.com`，初始 $10，仅 `china_deployed`，专属倍率 0.3 |

## 首次安装与配置 owner

- UCloud 身份使用官方 CLI 的 `tokenkey-domestic` profile；密钥不进入仓库。
- `bootstrap-host.sh DEVICE EXPECTED_BYTES` 只接受新空数据盘。它拒绝带分区、已挂载、
  尺寸不符或含签名的磁盘；已有文件系统必须人工核验后挂载，不会重格式化。
- `prepare-runtime.py STATE_DIR` 从共享 Stage0 Compose/Caddy 模板生成本机受保护 bundle，
  重用已存在的 secrets，不在 stdout 输出配置或密钥。`STATE_DIR/images.json` 必须来自
  已校验的 amd64 镜像，包含各服务的 `source` digest、`config` digest、归档 `sha256` 与本地 `tag`。
- `compose.override.yml` 关闭 QA capture/telemetry，限制应用连接池和 Go 内存目标；
  `compose.local.yml` 只将管理端口绑定到主机 loopback。
- 镜像经运维机离线传输；先核验 archive SHA-256，再 `docker load`。Docker 29 的 containerd
  image ID 是加载后的 manifest digest，不能直接拿 config digest 当 image ID 查找；应核验
  manifest 内的 `config.digest`，再绑定专用本地 tag。`pull_policy: never` 防止启动时漂移。
- 主机运行目录 `/opt/tokenkey`，Compose 文件依次为 `compose.yml`、`compose.override.yml`、
  `compose.local.yml`；环境文件 `.env` 权限 0600。Caddyfile 位于
  `/var/lib/tokenkey/caddy/Caddyfile`。

首次 `up -d tokenkey` 后，通过受限 SSH tunnel 初始化管理员、关闭公开注册、关闭
`backend_mode_enabled`（否则普通客户不能登录），再启动 Caddy。后续升级必须走共享 drain
与健康检查契约，不把首次安装步骤作为强制重启生产服务的升级脚本。

## 初始化与隔离

上游来源为本机受保护导出，首批四个账号后按用户要求追加十个火山计划账号；使用 `ops/accounts/import-accounts.sh`
的 validate → dry-run → apply。目标组 ID 为 2，不能沿用来源 ID 690。其他配置保留原值。
重复执行前先读目标账号/用户；不能重复创建、再次发放初始 $10 或覆盖已消费余额。

当前发布镜像的创建用户链没有持久化 `restrict_public_groups`，因此不能把该字段写入请求
视为已启用。本站通过将默认组设为 inactive/exclusive、只保留目标专属组为可用组完成实际隔离；
已验证客户可用组仅有 `china_deployed`，绑定其他组返回 403。未来开放其他组时须重新评估该边界。

QA capture 已关闭，公网 Caddy 已配置 QA、媒体和 Studio 路径限制。现网前端仍显示部分
工具菜单，客户端路由可展示其页面；复审修复统一了菜单、快捷入口与路由守卫，待镜像发布后生效。

复审发现已部署入口仍遗漏视频、Seedance 和 OpenRouter 等别名，未满足上述完整隔离目标。
仓库生成器已补齐媒体根路径、provider 别名和实时会话入口；`test_runtime.py` 使用真实 Caddy
验证拒绝路径和文本/管理路径，已纳入现有 Caddy preflight。**这些复审修复尚未部署**，
现网仍需从生成器提取新版 Caddyfile、校验并热加载后验收；不要重新覆盖生产 `.env` 或重跑初始化。
界面行为 owner 和浏览器验收入口见设计文档的 Implementation / Owners 表。

## 本机凭据与交付

- `~/codes/keys/tokenkey-cn-admin.json`：管理员登录地址、邮箱、密码。
- `~/codes/keys/tokenkey-cn-rt.json`：客户登录地址、密码来源说明与 API key。
- 两文件权限均为 0600；完整初始化状态位于
  `~/.local/share/tokenkey/domestic-onboard`（0700）。禁止粘贴完整导出或 dry-run 日志。
- 没有自动发送邮件或客户消息。

客户的 `tokensea_online`、`国产LLM测试` 两个有效 key 已按用户要求从 hcloud 原值复制，
归属本站 `rt@tk.com` 并绑定本站 `china_deployed`。原 key 可直接改用
`https://api-cn.tokenkey.dev/v1`；新旧站点余额/用量独立记账。
已停用的源站临时压测 key 未复制。原始 key 和账号完整导出不进入仓库。
用户随后要求保持原登录密码：已通过加密传输将源账号密码校验值定点同步至本站同名用户，
核对校验值一致且余额不变，原新机密码和旧登录 token 均返回 401。原密码明文不可恢复，
未用明文模拟登录；本机客户凭据文件已移除失效密码，并注明沿用 hcloud 密码。

## 验收回执

2026-10-14：北京本机直连 DNS、可信 HTTPS 和 `/health` 通过。四个账号配置与受保护导出
逐项一致。Chat Completions 普通/流式请求及 Responses 请求成功，usage 记录有账户归属和
0.3 倍率；实际覆盖 VolcEngine 两个账号和 Ali 的 `ali-token-plan-7`，另一个 Ali 账号完成配置
核验但未单独做请求归属验证。首次验收使用管理员临时 key，结束后已删除，首客初始余额 $10。

追加的十个火山账号（原名、`-2`、`-4` 至 `-10`、`-40`）凭据与所有可导出配置逐项一致，
绑定专属组且 active/schedulable；本站共十四个供应账号。两个复制的客户 key 均通过公网
模型列表和 kimi-k3 请求验证（HTTP 200、有正文），这两次客户验证实际记账 $0.00062686。

[Kimi K3 性能报告](../../docs/ops/reports/ucloud-kimi-k3-20261014/report.md)：4K–512K 八档，
正式请求 76/76 成功；含校准/预热 85/85 成功，管理员实际记账 $1.157457。
独立压测 key 已停用并验证 401。测试为 prod 主机经公网域名发起的小样本混合缓存测量，
不代表终端用户网络、冷缓存性能或持续容量承诺。

Playwright 真实 UI 完成管理员登录、账号/用户页、客户登录、API key/用量页，未捕获页面异常。
客户访问管理 API、绑定默认组和公开注册均返回 403。共享 preflight 通过；磁盘初始化实测拒绝
系统分区盘并正确挂载新数据盘。

## 监控与通知

应用监控和实时指标开启，复用现有 prod 飞书群。`api-cn.tokenkey.dev` 在共享节点身份
owner 中显式识别为 `cn-prod`，避免被 `api-*` 的 edge 推断抑制 prod 用户失败告警。
通知策略保持现有 prod 的分级规则，并非所有启用规则都会发送飞书：P0 与指定 P1 用户失败
走飞书，普通 CPU/P1 内存等规则仍保留在运维面。邮件 SMTP 未配置，不宣称邮件可达。

`install-host-alerts.sh SHARED_SCRIPT` 安装共享
`deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh`，每五分钟独立于 Docker/Postgres 检查
系统盘、数据盘和内存。磁盘达到 85% 告警、低于 80% 恢复；内存 90% 连续两次或 96%
单次告警，低于 80% 恢复。凭据放在系统盘 `/opt/tokenkey/host-alerts.env`（0600），
通过 `TOKENKEY_HOST_ALERT_ENV_FILE` 指定，数据盘失挂时仍能通知；节点标识为 `cn-prod`。

整机掉线、网络断开时本机告警不能发出；UCloud 云告警和站外探测尚未验收，不能将本机
健康检查描述为完整的外部可用性监控。备份/恢复仍按用户要求延后。

节点身份修复以 `1.8.292-cn-prod` 本地 amd64 补丁镜像部署，仅替换 UCloud 应用容器。
镜像基于原已校验发布镜像，附源码差异 SHA-256 与二进制 SHA-256；构建记录保存在受保护
初始化状态中。部署前 preflight 和节点身份/通知回归测试通过，使用共享 Stage0
`drain_container` 确认 `draining=true, in_flight=0` 后替换，健康检查通过；原镜像保留回退。

2026-10-14 飞书验收：通过临时、明确标注“非真实故障”的 P0 CPU 规则触发应用实际通知，
事件记录 `feishu_firing_sent=true`、`feishu_firing_status=sent`。测试规则已删除，测试事件已
标记恢复；未修改正式规则阈值。主机定时器 enabled/active、首次执行成功。公网健康检查和
两个复制客户 key 的鉴权在更新后仍通过。邮件与整机失联外部探测不在此次送达证明之内。

备案、备份和恢复按用户决定后续处理；此回执仅记录当前实测可达性。
