---
title: TokenKey UCloud 北京二单 prod 生产部署
status: approved
revised: 2026-10-23
approved_by: "feng (2026-10-15 conversation: 可以。继续。; review/merge before cn-prod release)"
approved_at: 2026-10-15
risk: high
provider: ucloud
---

> **现状（2026-10-27）**：cn-prod 在**腾讯北京**（身份见 [`deploy/domestic/prod-target.json`](../../deploy/domestic/prod-target.json)）。国内长期 HTTPS 入口是 `https://api.galaxywaveai.com`（`154.8.214.179`）。`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役；退役前不改这条解析，也不把它写成 `public_origin` 或 `SERVER_FRONTEND_URL`。本文 UCloud 资源、供应与首客章节为首次上线历史；蓝绿发布授权与边界仍有效。UCloud `106.75.29.139` 已于 2026-10-27 销毁。

# 已确认的交付范围

用户在本会话确认正式生产资源、北京二、单 prod，并要求继续实施。本文记录该执行范围；
2026-10-15 用户同意先将 #2624 对齐 main、复审并合并，再发布 cn-prod。此授权不是
部署完成证明；本文件的资源、供应与首客章节记录首次上线范围，不用于覆盖后续运营配置。

首次上线时的公共入口是 `https://api-cn.tokenkey.dev`。该名字现仍暂时指向美西，日后退役；当前国内长期入口见文首现状。

单节点运行共享 Stage0 Compose 的 Caddy、TokenKey、
PostgreSQL、Redis；仅接入境内 Agent Plan / Token Plan，提供 API key、基础用量统计和 prod
运营管理面。前端由现有镜像内嵌，不增加前端服务器。无 edge、跨站 relay 或境外上游。

# 资源与费用

- UCloud 项目 `org-bpx31r`，地域 `cn-bj2`，可用区 `cn-bj2-02`。
- 一台 `cn-prod`：快杰 O 型，2 CPU / 4 GiB，Ubuntu 24.04 amd64。
- 40 GiB SSD 系统盘、20 GiB SSD 数据盘；业务数据挂载 `/var/lib/tokenkey`。
- 复用该项目北京二默认 VPC/subnet；独立防火墙，仅公网 TCP 80/443；日常运维走 AWS SSM Hybrid，
  不长期开放公网 SSH 22。数据库、Redis 和应用 8080 不开放公网。SSH 仅密钥认证，且只用于
  一次性 agent bootstrap 或 GHCR/SSM 不可用时的恢复（临时放行运维出口 `/32`，完成即移除）。
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
发布复用与 AWS prod 相同的已发布 amd64 GHCR 包（固定 digest / version label）。默认经
AWS SSM Hybrid 在主机上 `docker pull`（入口 `deploy/domestic/deploy_via_ssm.sh`，身份读
`deploy/domestic/prod-target.json`）；仅当 GHCR 拉取或 SSM 不可用时，才走运维机校验 archive
→ 受限 SSH 传输 → `docker load` 的恢复路径。运维叙述唯一 owner 为
`deploy/domestic/README.md`。此路径不执行 Hetzner / `deploy-stage0` workflow。

数据盘初始化仅接受确认是新建、未挂载且无文件系统的数据盘；已有文件系统只能核验并挂载，
不得格式化。部署前运行 preflight。后续升级须保留共享 drain 和健康检查契约，首次 bootstrap
不作为可重复强制重启生产服务的升级命令。

QA 不进入此次一期服务承诺。验收须确认 QA capture 关闭、未创建相关依赖。前端不按国内
域名隐藏工具菜单或路由；媒体、视频、音频、实时会话及 provider 兼容接口不在 Caddy 层按
路径永久封禁，应用根据鉴权、模型目录和供应账号能力决定是否可用，避免未来新增模型必须
先改网关配置。

## Implementation / Owners

国内站不增加按域名隐藏工具的界面开关；各站点共用同一套菜单和路由。cn-prod 直接复用与
prod 相同的共享 Caddy 模板，不增加国内专属路径拦截。媒体、实时、provider 以及已退役的
QA 旧路径由应用、鉴权和退役状态裁决。

| 行为 | 共享 owner | 验证 |
|---|---|---|
| cn-prod 与 prod 复用共享 Caddy，产品和旧路径保持应用所有权 | `deploy/domestic/prepare-runtime.py` 渲染共享 Caddy 模板 | `deploy/domestic/test_runtime.py` 真实 Caddy 产品、媒体/provider/实时/旧 QA 路径转发测试，经 `caddyfile-syntax.sh` 进入 preflight |
| cn-prod 发版默认 SSM Hybrid + 主机 GHCR pull（离线 SSH 仅恢复） | `deploy/domestic/deploy_via_ssm.sh` + `deploy/domestic/prod-target.json`；叙述 `deploy/domestic/README.md` | `deploy/domestic/test_deploy_via_ssm.py`，经 `caddyfile-syntax.sh` 进入 preflight |

# DNS、TLS 与验收

当前国内长期名字是 `api.galaxywaveai.com`，A 记录指向腾讯北京 `154.8.214.179`。`api-cn.tokenkey.dev` 仍暂时指向美西，日后退役；退役前保持现有记录，不把它改到北京。域名与 TLS 必须实测可达后才能交给调用方。

上线验收：

1. 固定镜像、独立 secrets、磁盘挂载和服务健康正确；公网不能连接 5432/6379/8080。
2. DNS 和可信 HTTPS 可用；通过真实浏览器验证管理员登录、用户登录、API key 与基础用量页面。
3. 四个账号配置与导出一致；目标专属组、客户余额与 0.3 倍率正确；公开注册与越权访问被拒绝。
4. 实际 Agent/Token 请求、流式输出、用量和计费正确；验证流量不消耗首客承诺的 $10 初始额度。
5. 重复初始化不创建重复账号/用户、不重复充值。未验证项写入交付回执。

# 首次上线记录（2026-10-14）

用户最终确认保持 2 CPU / 4 GiB、5 Mbps，随后要求缩小空数据盘；现已换为 20 GiB。
已创建主机 `uhost-1wgyn7gu2f6a`，运行中；EIP `eip-1wgywcyzw8bs`（`106.75.17.57`）已绑定。
后续站点验收写入回执；资源创建不是客户可用证明。用户后续已授权带宽扩至 10 Mbps；
上述 5 Mbps 和费用为首次部署记录，当前资源与账号状态须通过只读检查重新核实。

部署、账号迁移和性能实测回执见 [国内 prod 运维入口](../../deploy/domestic/README.md)。
用户追加确认沿用现有 prod 飞书群、标注 `cn-prod`，并允许一次测试告警。应用节点身份修复
与共享主机压力告警纳入此次实施；不把未验收的云告警或站外探测视为已开启。

# 国内蓝绿发布授权

2026-10-15 用户明确要求“#2650 已合并。蓝绿发布部署 cn-prod。只部署 cn-prod 哈”。
范围仅为 UCloud 国内节点：先启动同库候选并实测，使用共享 Caddy 切流和排空契约，
保留旧容器/镜像供回退；不调用海外 prod/edge dispatcher，不改变用户、额度或供应配置。
默认发版：`deploy_via_ssm.sh` `pull` → `sync-controls` → `prepare`，再主机侧候选实测与
`bluegreen.py promote`（经 SSM Run Command / Session Manager）。日常不得为发版开放公网
SSH 22；仅恢复路径临时放行运维出口 `/32` 并在结束后移除。主机适配入口见
`deploy/domestic/bluegreen.py`；运维说明唯一 owner 为 `deploy/domestic/README.md`。
