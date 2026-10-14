---
title: TokenKey UCloud 北京二单 prod 生产部署
status: pending
approved_by: pending
risk: high
provider: ucloud
---

# 已确认的交付范围

用户在本会话确认正式生产资源、北京二、单 prod，并要求继续实施。本文记录该执行范围；
PR 仍待人工合并，不把会话授权写成已完成上线或已合并审批。

公共入口 `https://api-cn.tokenkey.dev`。单节点运行共享 Stage0 Compose 的 Caddy、TokenKey、
PostgreSQL、Redis；仅接入境内 Agent Plan / Token Plan，提供 API key、基础用量统计和 prod
运营管理面。前端由现有镜像内嵌，不增加前端服务器。无 edge、跨站 relay 或境外上游。

# 资源与费用

- UCloud 项目 `org-bpx31r`，地域 `cn-bj2`，可用区 `cn-bj2-02`。
- 一台 `cn-prod`：快杰 O 型，2 CPU / 4 GiB，Ubuntu 24.04 amd64。
- 40 GiB SSD 系统盘、20 GiB SSD 数据盘；业务数据挂载 `/var/lib/tokenkey`。
- 复用该项目北京二默认 VPC/subnet；独立防火墙，仅公网 TCP 80/443，SSH 22 仅运维出口 /32。
  数据库、Redis 和应用 8080 不开放公网。SSH 仅密钥认证。
- 一枚 BGP EIP、5 Mbps 固定带宽。官方即时月付报价：主机与磁盘 ¥209，EIP 带宽 ¥125，
  合计 ¥334/月；最终扣款以订单为准。数量固定为一台，不开自动扩容。

单节点直接提供 TLS，不新增 SLB、WAF、托管数据库、KMS 或对象存储。恢复、备份、灾备和
RPO/RTO 按用户要求后续运维处理，本期不作恢复能力承诺，不以恢复演练作为此次上线门槛。

# 供应与用户初始化

账号配置通过只读导出取得，导入时使用本机受保护快照，不修改 hcloud 的资源或配置。

| 来源账号 | 类型 | 境内端点 |
|---|---|---|
| ali-token-plan-6 | Ali Token Plan | token-plan.cn-beijing.maas.aliyuncs.com |
| ali-token-plan-7 | Ali Token Plan | token-plan.cn-beijing.maas.aliyuncs.com |
| volcengine-agent-plan-3 | VolcEngine Agent Plan | ark.cn-beijing.volces.com |
| volcengine-agent-plan-41 | VolcEngine Agent Plan | ark.cn-beijing.volces.com |

通过现有账号导入入口 validate → dry-run → apply 创建以上账号；保留凭据、模型映射、
并发、优先级、倍率和配置。只重映射目标分组 ID，不复用来源数据库主键。两个部署使用同一
供应商凭据，供应商额度仍共享；不复制来源数据库或环境文件。

用户随后要求追加报告中的十个火山账号：原名、`-2`、`-4` 至 `-10`、`-40`，同样复制
凭据及配置、绑定本站专属组。用户明确要求复用客户 key 后，原值复制 `rt@tk.com` 在源
`china_deployed` 下的两个有效业务 key；源站已停用的临时压测 key 不复制。账户余额和
用量独立记账，供应商额度共享不代表两个站点数据库同步。

复制来源 `china_deployed` 专属分组配置，组倍率 1。管理员为 `admin@cn.tokenkey.dev`。
首个且唯一业务客户为 `rt@tk.com`：初始余额 $10、并发 400、RPM 0（不限）、只允许新站
`china_deployed`，禁止公开组，保留其专属组倍率 0.3。重复执行不得重复充值或重置已有余额。
新站生成独立管理员/客户密码、API key、JWT/TOTP 和 PostgreSQL/Redis secrets；关闭公开注册。
客户后来复制的两个业务 key 是上述独立 key 原则的明确授权例外；其后用户又要求登录密码
与 hcloud 一致，因此仅同步该用户密码校验值，不复制余额或权限状态。

# 部署入口与密钥

使用 UCloud 官方 CLI 和本机 `tokenkey-domestic` profile。云密钥、供应商密钥、账号密码和
完整导入结果只写本机 0700 目录下的 0600 文件，不写仓库、命令输出或 PR。

复用 `deploy/aws/stage0/docker-compose.yml`，国内 override 关闭 QA capture 和 telemetry。
发布采用已经发布的 amd64 镜像，固定 digest；若云主机不能直接拉取，运维机校验镜像后通过
受限 SSH 传输 Docker archive，再校验加载结果。此路径不执行 Hetzner 发布 workflow。

数据盘初始化仅接受确认是新建、未挂载且无文件系统的数据盘；已有文件系统只能核验并挂载，
不得格式化。部署前运行 preflight。后续升级须保留共享 drain 和健康检查契约，首次 bootstrap
不作为可重复强制重启生产服务的升级命令。

QA、媒体和其他工具不进入此次服务能力。验收须确认 QA capture 关闭、未创建相关依赖，
公共入口拒绝未开放功能调用。现有镜像菜单能力如有缺口，明确记录并修复，不能仅写文档称已隔离。

## Implementation / Owners

国内站的界面限制以实际浏览器域名 `api-cn.tokenkey.dev` 判定，不增加管理开关；后端公共
入口独立执行媒体/实时功能拒绝，界面隐藏不代替鉴权。修复发布前，现网旧镜像仍有工具菜单。

| 行为 | 共享 owner | 验证 |
|---|---|---|
| 菜单、快捷入口、SPA 直达统一限制 Studio/批量图像 | `frontend/src/utils/siteRouteAvailability.ts`；Sidebar、Router、Quickstart、`useBatchImageAccess` 只消费它 | `siteRouteAvailability.spec.ts`、`public-pages.e2e.ts` 国内 user/admin 与其他站点回归 |
| 公网拒绝未开放接口及 provider/兼容别名 | `deploy/domestic/prepare-runtime.py` 渲染共享 Caddy 模板 | `deploy/domestic/test_runtime.py` 真实 Caddy 转发/拒绝测试，经 `caddyfile-syntax.sh` 进入 preflight |

# DNS、TLS 与验收

仅创建/更新 `api-cn.tokenkey.dev` 的记录。备案事项按用户要求后续考虑；域名/TLS 实际可达性
必须实测，未验证或被云平台拦截时不得宣布客户可用。

上线验收：

1. 固定镜像、独立 secrets、磁盘挂载和服务健康正确；公网不能连接 5432/6379/8080。
2. DNS 和可信 HTTPS 可用；通过真实浏览器验证管理员登录、用户登录、API key 与基础用量页面。
3. 四个账号配置与导出一致；目标专属组、客户余额与 0.3 倍率正确；公开注册与越权访问被拒绝。
4. 实际 Agent/Token 请求、流式输出、用量和计费正确；验证流量不消耗首客承诺的 $10 初始额度。
5. 重复初始化不创建重复账号/用户、不重复充值。未验证项写入交付回执。

# 当前状态

用户最终确认保持 2 CPU / 4 GiB、5 Mbps，随后要求缩小空数据盘；现已换为 20 GiB。
已创建主机 `uhost-1wgyn7gu2f6a`，运行中；EIP `eip-1wgywcyzw8bs`（`106.75.17.57`）已绑定。
后续站点验收写入回执；资源创建不是客户可用证明。PR #2624 不自动合并。

部署、账号迁移和性能实测回执见 [国内 prod 运维入口](../../deploy/domestic/README.md)。
用户追加确认沿用现有 prod 飞书群、标注 `cn-prod`，并允许一次测试告警。应用节点身份修复
与共享主机压力告警纳入此次实施；不把未验收的云告警或站外探测视为已开启。
