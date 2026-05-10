# TokenKey 单主控多区域 Edge 网关方案草案

> 本文用于方案 review：整理当前 AWS prod 部署现状、Anthropic OAuth 组织停用事件后的风险判断，以及“`api.tokenkey.dev` 单一主网关 + 多区域标准化 EC2 Edge”的出口资源方案。
>
> 核心结论：**不要把多区域多 IP 做成多个并列主网关，也不要做随机代理池。** 保持 `api.tokenkey.dev` 作为唯一用户入口、唯一计量计费面、唯一运营控制面；英国、新加坡、法兰克福、美国 clean 等子网关都只是标准化 Edge 资源节点，通过主网关的 `newapi` 外部上游账号接入。

## 1. 设计原则

### 1.1 乔布斯原则：一个产品，一个入口，一个体验

用户只应该感知一个 TokenKey：

```text
用户 / Claude Code / SDK
  -> api.tokenkey.dev
  -> TokenKey 主网关完成认证、分组、调度、计量、计费、错误呈现
```

所有区域 Edge 都不是第二个产品、第二套后台、第二套用户体系，而是主网关背后的区域资源节点。用户不需要知道它们的存在，也不需要切换 endpoint。

### 1.2 OPC 原则：标准化多 Edge，而不是手工多 VPS

多网关不能增加 OPC 运营压力。必须满足：

```text
统一用户入口：api.tokenkey.dev
统一用户 API key：只在主网关发放
统一计量计费：只在主网关入账
统一后台运营：主网关是唯一日常管理面
统一部署机制：所有 Edge 复用当前 EC2/CFN Stage 0 机制，并通过参数化 profile 部署
统一发布节奏：release tag -> GHCR -> deploy-stage0(edge profile) -> smoke
统一故障判断：主网关看到所有用户请求与最终费用
```

因此，**多独立 Stage 0 + 多套用户/API key/账务后台**不是可接受主方案；**多个 Lightsail/runbook 分叉**也不是长期可接受方案。

### 1.3 最小 upstream 冲突原则

第一阶段固定复用当前 `newapi` 平台的 OpenAI-compatible channel 能力，把各区域 TokenKey Edge 暴露成普通 OpenAI-compatible 外部上游，而不是重构 TokenKey 成中心控制面 + 边缘执行面。

每个 Edge 的接入形态一致：

```text
edge 上创建一个主网关专用用户级 API key
api.tokenkey.dev 上创建一个 Platform=newapi 的账号 tokenkey-edge-<edge>
该账号使用 channel_type=OpenAI-compatible、base_url=https://api-<edge>.tokenkey.dev、api_key=tk_<edge>_edge_xxx
api.tokenkey.dev 上创建 newapi 平台的 anthropic-<edge> / edge-<edge> 分组

例如 uk1：
api-uk1.tokenkey.dev 上创建 tk_uk1_edge_xxx
api.tokenkey.dev 上创建 tokenkey-edge-uk1 账号，base_url=https://api-uk1.tokenkey.dev
api.tokenkey.dev 上创建 anthropic-uk1 分组
```

代码改造只做必要的安全硬化：组织级熔断、网关身份日志、上游 TokenKey 资源标记。不要为了“多区域”重写调度核心。

### 1.4 风控边界

目标不是绕过上游风控，而是做风险隔离和流量画像稳定化。

允许：

```text
固定区域资源从固定区域出口访问上游。
主网关把每个区域资源当作明确分组。
区域出口承载与该区域身份/组织/支付/使用场景一致的账号。
```

禁止：

```text
同一个 Anthropic OAuth 账号在 US/UK/SG/JP/EU 之间漂移。
同一个 Anthropic organization 被多个国家出口轮流打。
某个 organization disabled 后，主网关自动换国家继续重试。
把多 IP 设计成风控规避代理池。
```

## 2. 当前 AWS prod 部署现状

当前生产环境是 Stage 0 单机全栈部署。

| 项 | 当前值 |
|---|---|
| Stack | `tokenkey-prod-stage0` |
| Region | `us-east-1` |
| AZ | `us-east-1a` |
| Domain | `https://api.tokenkey.dev` |
| Public IP / EIP | `34.194.234.88` |
| Instance | `i-0e4a90677f0450277` |
| Instance Type | `t4g.small` |
| Arch | `arm64` |
| Private IP | `10.0.1.97` |
| VPC | `vpc-0337cf2929fd56c45` |
| Subnet | `subnet-078d680406b580ed4` |
| CFN ImageTag | `ghcr.io/youxuanxue/sub2api:1.7.11` |
| Root EBS | 30 GiB |
| DataVolume | 30 GiB, `vol-020ce8eda4cf1e5ea` |
| Snapshot | DLM daily, `policy-061a66f72b46fe7f0` |
| 入口 | Caddy 80/443 |
| 应用 | `tokenkey` container |
| 数据 | 同机 PostgreSQL 18 + Redis 8 |
| 运维入口 | AWS SSM Session Manager |

当前拓扑：

```text
api.tokenkey.dev
  -> AWS us-east-1 EIP 34.194.234.88
  -> EC2 t4g.small
  -> Caddy
  -> tokenkey:8080
  -> local PostgreSQL + Redis
  -> upstream providers
```

这意味着当前所有用户入口、计量计费、账号调度和上游访问都集中在 `api.tokenkey.dev`。本方案保留这一点，不拆主控面，也不把主网关迁到 Lightsail。

## 3. Anthropic OAuth 事件判断

线上日志显示，`claude-am-g-4` 在请求进入 Anthropic 后约 300ms 内收到：

```text
400 invalid_request_error
This organization has been disabled.
```

本地随后把账号标记为：

```text
Organization disabled (400): This organization has been disabled.
```

当前合理判断：`34.194.234.88` 这个 AWS 出口、对应 OAuth 组织、请求形态、网络/设备/地区信号、支付/账号关联中的一个或多个因素，已经被 Anthropic 风控系统关联。日志不能证明“这个 IP 单独导致封禁”，但足以说明当前 Anthropic OAuth 池不应继续在该出口上自动重试。

## 4. 外部社区与官方信号

官方强信号：

- Claude Code 官方错误文档把 `This organization has been disabled` 归类为认证/组织状态问题，常见解释是 disabled Console org 的 API key / credential 仍被使用。
- Claude Code 官方文档说明 `ANTHROPIC_API_KEY` 会覆盖 Claude.ai 订阅登录，因此环境变量污染可能导致看似订阅号异常，实际使用的是 disabled org 的 API key。
- Anthropic 隐私/帮助文档承认会使用 IP/location 等信号做地区、合规、滥用防护判断。
- 官方支持企业做 IP allowlisting，说明固定、可解释、可审计的企业出口是被认可的；随机代理池不是。

社区信号：

- Linux.do 多贴讨论 Claude / Claude Code 的 VPN、云服务器 IP、地区切换、设备指纹、支付方式、账号组织 disabled 等问题。
- 社区经验不完全一致：有人认为云厂商 IP、VPN、频繁切换地区会增加风险；也有人在正常美国环境下被误伤。
- 共同高风险组合包括：多账号、同一出口、高频自动化、订阅账号共享网关、IP/设备/支付/地区信号不一致。
- 没有官方证据证明“AWS IP 一定会导致 organization disabled”，但数据中心 IP + 多 OAuth 共享使用是社区普遍认为的高危形态。

## 5. 主方案：单主网关 + 多区域 EC2 Edge

### 5.1 总体拓扑

```text
用户 / Claude Code / SDK
  -> api.tokenkey.dev                                # 唯一用户入口、唯一计量计费
  -> 主 TokenKey: auth / group / scheduler / usage / billing
      ├─ 本地 US/OpenAI/newapi/Bedrock 资源
      ├─ newapi 分组 anthropic-uk1
      │     -> tokenkey-edge-uk1 account             # api-uk1.tokenkey.dev
      ├─ newapi 分组 anthropic-sg1
      │     -> tokenkey-edge-sg1 account             # api-sg1.tokenkey.dev
      ├─ newapi 分组 anthropic-fra1
      │     -> tokenkey-edge-fra1 account            # api-fra1.tokenkey.dev
      └─ newapi 分组 anthropic-us1
            -> tokenkey-edge-us1 account             # api-us1.tokenkey.dev
```

关键点：

```text
api.tokenkey.dev 是唯一产品入口。
所有 api-*.tokenkey.dev 都是资源节点，不是用户入口。
用户 API key 只在 api.tokenkey.dev 发放。
最终用户账单只由 api.tokenkey.dev 计算。
每个 Edge 上只创建一个或少量“主网关专用”的用户级 API key。
api.tokenkey.dev 把各 Edge API key 当作 newapi/OpenAI-compatible 外部上游账号使用。
```

### 5.2 Edge 命名规范

Edge 命名必须支持“同一 region 多个不同 IP 的子网关”。统一采用 `<geo><ordinal>`：

```text
edge_id:       <geo><n>                 # uk1, uk2, sg1, fra1, us1, jp1
Domain:        api-<geo><n>.tokenkey.dev # api-uk1.tokenkey.dev
Stack:         tokenkey-edge-<geo><n>-stage0
Account name:  tokenkey-edge-<geo><n>
Group:         anthropic-<geo><n>
SSM prefix:    /tokenkey/edge/<geo><n>
API key name:  tk_<geo><n>_edge_xxx
```

`geo` 不是 AWS region code，而是产品可读的出口地区短名：

```text
uk  = United Kingdom / London, AWS eu-west-2
sg  = Singapore, AWS ap-southeast-1
fra = Frankfurt / Germany / EU mainland, AWS eu-central-1
us  = United States clean egress, default AWS us-west-2
jp  = Japan / Tokyo, AWS ap-northeast-1
```

同一区域扩多个 IP 时只递增序号：

```text
api-uk1.tokenkey.dev -> tokenkey-edge-uk1-stage0 -> first UK EIP
api-uk2.tokenkey.dev -> tokenkey-edge-uk2-stage0 -> second UK EIP
api-uk3.tokenkey.dev -> tokenkey-edge-uk3-stage0 -> third UK EIP
```

不要使用 `api-us-clean`、`api-uk` 这类不可扩展命名；“clean” 是用途/状态标签，不进入主键。需要表达 clean 时写入描述或 tag：`purpose=clean-egress`。

### 5.3 首批 Edge 矩阵

| Edge ID | AWS Region | Domain | Stack | 角色 |
|---|---|---|---|---|
| `uk1` | `eu-west-2` | `api-uk1.tokenkey.dev` | `tokenkey-edge-uk1-stage0` | 英国 1 号出口，UK-consistent 账号/组织 |
| `sg1` | `ap-southeast-1` | `api-sg1.tokenkey.dev` | `tokenkey-edge-sg1-stage0` | 新加坡/东南亚 1 号出口 |
| `fra1` | `eu-central-1` | `api-fra1.tokenkey.dev` | `tokenkey-edge-fra1-stage0` | 法兰克福/EU mainland 1 号出口 |
| `us1` | `us-west-2` | `api-us1.tokenkey.dev` | `tokenkey-edge-us1-stage0` | 美国 clean 1 号出口，与当前可疑 EIP 隔离 |

> 日本（`jp1` / `ap-northeast-1` / `api-jp1.tokenkey.dev`）可以作为第二批 Edge 加入；第一批先控制在 4 个节点，避免一次性扩大运营面。

### 5.4 为什么这符合 OPC

单个 Lightsail PoC 看似更省钱，但现在已经明确会扩展多个 Edge。多个 runbook/VPS 会让 OPC 复杂度快速上升：

```text
N 套 SSH / .env / docker login / snapshot / upgrade / Caddy allowlist / smoke
```

标准 EC2/CFN Edge 的长期成本更低：

```text
一套模板
一套参数 profile
一条 deploy-stage0 edge 路径
一套 smoke
一套日志/SSM/备份习惯
```

这符合 OPC 的“流程极简、自动化优先、深度 > 广度”。

### 5.5 为什么最小 upstream 冲突

这个方案复用现有能力：

```text
TokenKey 已支持 newapi 平台和 OpenAI-compatible 上游接入。
Edge TokenKey 已暴露 /v1/chat/completions 和 /v1/messages 等网关入口。
主 TokenKey 通过 newapi OpenAI channel 把 Edge TokenKey 看成外部 API provider。
```

第一阶段固定使用 `newapi + OpenAI-compatible channel`，原因：

```text
它已有 base_url/api_key/channel_type 配置形态。
它走现有 OpenAI-compatible 调度池，不需要新增平台。
它对主网关 /v1/messages 可通过 OpenAI channel 转成 edge /v1/chat/completions，减少协议面。
```

不需要：

```text
不需要中心数据库。
不需要多主复制。
不需要把用户/API key 同步到 Edge。
不需要新增 TokenKey-to-TokenKey 专用协议。
不需要大改调度器或账户模型。
```

## 6. 标准 EC2/CFN Edge 部署形态

### 6.1 为什么选择 EC2/CFN，但默认最低成本 profile

Lightsail London 2GB 约 $12/月，单个 Edge PoC 很有吸引力；但当前目标已经变成多 Edge：英国、新加坡、法兰克福、美国 clean，后续还可能加日本/欧洲其他区域。多个 Lightsail/runbook 会制造长期手工状态，不符合 OPC。

因此主路径仍是 EC2/CFN，但**不能复制主网关规格**。主网关 `api.tokenkey.dev` 承载用户、计量计费、后台和主数据可靠性；Edge 只承载固定区域出口和少量上游资源转发，默认必须用最低 EC2 成本开展。

| 维度 | Lightsail 多 Edge | EC2/CFN `edge-minimal` | EC2/CFN `edge-standard` | 当前判断 |
|---|---|---|---|---|
| 单节点成本 | 约 $12/月 | 目标接近 `$10–16/月` | 约 `$22–28/月` | 默认 `edge-minimal` |
| 4 个 Edge 成本 | 约 $48–60/月 | 目标约 `$40–64/月` | 约 `$90–112/月` | 先 minimal，按指标升配 |
| 部署一致性 | 多 runbook，容易漂移 | 复用 CFN/UserData/deploy-stage0 | 同左 | EC2 胜 |
| 运维入口 | SSH/browser SSH | SSM Session Manager | SSM Session Manager | EC2 胜 |
| secret 管理 | 手工 `.env` / docker login | SSM SecureString + IAM role | 同左 | EC2 胜 |
| 数据持久化 | Lightsail snapshot | 小 root / 小 DataVolume，低频快照 | 主站同级 DataVolume + DLM | Edge 默认 minimal |
| 发布升级 | 手工 runbook | 参数化 workflow | 参数化 workflow | EC2 胜 |
| 多 Edge 扩展 | N 套手工状态 | N 个 profile | N 个 profile | EC2 胜 |
| OPC 长期压力 | 随 Edge 数量线性上升 | 主要沉淀在模板/参数化上 | 同左但成本更高 | minimal 最平衡 |

结论：**Lightsail 不作为默认方案；EC2/CFN 是默认方案；`edge-minimal` 是默认规格。** `edge-standard` 只在该区域 Edge 通过灰度并出现明确资源压力、稳定流量或可靠性诉求时启用。

### 6.2 Edge Profile 标准

Edge profile 分两档：

```text
edge-minimal（默认）
  Instance: t4g.micro 起步；允许 t4g.nano + swap 仅用于极低流量实验，不作为默认
  Architecture: arm64
  RootVolume: 20 GiB gp3
  DataVolume: 0 或 20 GiB gp3（默认 0；只有需要跨实例保留 Edge 配置/审计日志时启用）
  Snapshot: no DLM by default；灰度期手工/低频 snapshot；正式开放后 daily snapshot 可选
  Swap: 1–2 GiB
  Ingress: Caddy 80/443
  Runtime: Docker Compose
  Services: Caddy + tokenkey + PostgreSQL + Redis
  DB/Redis: edge 低内存参数（PG shared_buffers/Redis maxmemory 下调）
  Secrets: SSM SecureString + local .env.secret
  Operations: SSM Session Manager
  Image: 与 prod 同版本或按 edge rollout 策略固定 tag
  API allowlist: API 路径默认只允许主网关 EIP

edge-standard（升配档）
  Instance: t4g.small
  RootVolume: 30 GiB gp3
  DataVolume: 30 GiB gp3, Retain
  Snapshot: DLM daily
  其他机制与主站 Stage 0 对齐
```

升配触发条件：

```text
任一 Edge 发生 OOM 或 swap thrash。
5 min memory P95 > 75% 持续 3 天。
5 min CPU P95 > 60% 持续 3 天。
PG/Redis 数据或日志需要跨实例可靠保留。
该 Edge 从内部灰度变成稳定对外资源。
低配导致 smoke / stream 稳定性失败。
```

Edge profile 示例：

```text
edge_id=uk1
geo=uk
ordinal=1
profile=edge-minimal
stack=tokenkey-edge-uk1-stage0
region=eu-west-2
domain=api-uk1.tokenkey.dev
group=anthropic-uk1
account=tokenkey-edge-uk1
ssm_prefix=/tokenkey/edge/uk1
monthly_budget_usd=16
```

```text
edge_id=sg1
geo=sg
ordinal=1
profile=edge-minimal
stack=tokenkey-edge-sg1-stage0
region=ap-southeast-1
domain=api-sg1.tokenkey.dev
group=anthropic-sg1
account=tokenkey-edge-sg1
ssm_prefix=/tokenkey/edge/sg1
monthly_budget_usd=16
```

```text
edge_id=fra1
geo=fra
ordinal=1
profile=edge-minimal
stack=tokenkey-edge-fra1-stage0
region=eu-central-1
domain=api-fra1.tokenkey.dev
group=anthropic-fra1
action_label=eu-mainland
account=tokenkey-edge-fra1
ssm_prefix=/tokenkey/edge/fra1
monthly_budget_usd=16
```

```text
edge_id=us1
geo=us
ordinal=1
profile=edge-minimal
stack=tokenkey-edge-us1-stage0
region=us-west-2
domain=api-us1.tokenkey.dev
group=anthropic-us1
purpose=clean-egress
account=tokenkey-edge-us1
ssm_prefix=/tokenkey/edge/us1
monthly_budget_usd=16
```

### 6.3 deploy-stage0 参数化要求

当前 `deploy-stage0` 主要服务 prod/test Stage 0。多 Edge 方案要求它支持 profile 化：

```text
environment=edge
edge_id=uk1|sg1|fra1|us1
profile=edge-minimal|edge-standard
tag=X.Y.Z
region=<aws-region>
stack=<cloudformation-stack>
domain=<edge-domain>
monthly_budget_usd=<budget>
smoke_mode=edge
```

部署动作：

```text
1. 校验 GHCR tag multi-arch。
2. 找到对应 edge stack/instance。
3. 通过 SSM 原地更新 TOKENKEY_IMAGE。
4. 重启 tokenkey compose。
5. 跑 edge 自身 health。
6. 从主网关跑经 edge 的 /v1/messages smoke。
7. 输出 edge_id、region、domain、instance_id、image_tag。
```

### 6.4 Edge 网关职责

每个 Edge 只做资源边缘：

```text
承载固定区域出口 IP。
承载 region-consistent 上游账号/组织。
对主网关暴露 OpenAI-compatible API。
记录边缘侧技术日志，便于排障。
不对最终用户计费。
不作为最终用户入口。
不承载主用户体系。
```

### 6.5 Edge API key

每个 Edge 上创建一个主网关专用用户/API key：

```text
User: tokenkey-main-gateway
API key: tk_<edge>_edge_xxx
Group: <edge>-edge-resource
Quota: 只用于保护边缘，不作为最终账单依据
Rate limit: 保守，匹配该区域上游账号能力
Allowed models: 只开放该区域真实可承载的模型
```

这个 key 只配置到 `api.tokenkey.dev`，不发给最终用户。

### 6.6 主网关接入 Edge 的固定契约

在 `api.tokenkey.dev` 上为每个 Edge 新增一个 `newapi` 平台账号：

```text
Platform: newapi
Name: tokenkey-edge-<edge>
Channel type: OpenAI-compatible channel（new-api constant ChannelTypeOpenAI = 1）
Credentials:
  base_url: https://api-<edge>.tokenkey.dev
  api_key: tk_<edge>_edge_xxx
  model_mapping: 按主网关模型 -> edge 模型显式配置
Group: anthropic-<edge> / edge-<edge>（group.platform=newapi）
Models: claude-opus-4-7 / claude-sonnet-4-6 / claude-haiku-4-5... 按 PoC 结果开放
Priority: 低于官方 API / 高于被隔离 OAuth 池，按业务策略定
```

协议契约：

```text
最终用户入口：api.tokenkey.dev /v1/messages 或 /v1/chat/completions
主网关上游账号：newapi tokenkey-edge-<edge>
edge 调用入口：api-<edge>.tokenkey.dev /v1/chat/completions
鉴权：Authorization: Bearer tk_<edge>_edge_xxx
usage 来源：edge OpenAI-compatible response / stream final usage
错误映射：edge 返回的 4xx/5xx 按主网关 newapi upstream error 处理
```

弃选路径：

```text
不使用自定义 extension engine 新协议。
不新增 TokenKey-to-TokenKey 专用平台。
不要求 edge 原生透传 /v1/messages 作为唯一入口。
```

## 7. 统一计量计费设计

### 7.1 计费权威只在主网关

计费权威：

```text
api.tokenkey.dev
```

所有 `api-*.tokenkey.dev` 的用量记录只作为边缘技术审计，不作为最终用户账单来源。

主网关应该记录：

```text
最终用户 user_id / api_key_id / group_id
模型
输入/输出 token
主网关售价
实际上游账号：tokenkey-edge-<edge>
上游边缘：api-<edge>.tokenkey.dev
上游成本估算或边缘成本标签
请求状态 / 错误类型
```

### 7.2 计量 PoC 是每个 Edge 的上线前置门禁

统一计费能否成立，必须对每个 Edge 做最小 PoC。没有通过 PoC 前，不能把对应 `anthropic-<edge>` 开给用户。

PoC 必测：

```text
P1 non-stream /v1/messages：主网关有 input/output token usage，最终用户扣费正确。
   验收字段：主网关 usage 至少能记录 input_tokens / output_tokens；cache_creation / cache_read / thinking 等模型返回的扩展字段若存在不得丢失或误计。
P2 stream /v1/messages：stream final usage 不丢，主网关最终入账正确。
   验收字段：OpenAI-compatible stream final usage 能被主网关稳定入账；缺失 final usage 时必须明确降级为主网关估算并标记来源。
P3 edge 4xx：主网关不重复扣费，错误体不泄露 edge credential。
P4 edge 5xx / timeout：主网关不把失败请求记成成功用量。
P5 edge usage：tk_<edge>_edge_xxx 的边缘 usage 与主网关 tokenkey-edge-<edge> usage 可日级对账。
P6 禁用 tokenkey-edge-<edge>：默认组和其他官方资源不受影响。
```

PoC 判定：

```text
全部通过 -> 可进入内部灰度。
任一 usage 缺失 -> 该模型/协议暂不开放；只能改为主网关本地估算或补代码后重测。
任一错误重复扣费 -> 不上线，先修计费逻辑。
任一模型的 stream / thinking / cache 计量未通过 -> 该模型不得在对应 Edge 分组开放。
```

### 7.3 双重计量的处理

因为 Edge 本身也是 TokenKey，它会对 `tk_<edge>_edge_xxx` 产生边缘侧 usage。这个 usage 的用途是：

```text
边缘资源保护
边缘成本核对
异常排查
与主网关做日级 reconciliation
```

不用于：

```text
不给最终用户出账。
不作为主账务系统。
不让用户登录 Edge 后台查看自己的账。
```

推荐设置：

```text
Edge 上的 tokenkey-main-gateway 用户可以有高额度或内部额度。
Edge 的价格设置为内部成本价；不设为 0，避免边缘成本不可见。
api.tokenkey.dev 仍按主站 pricing catalog 给最终用户计费。
```

### 7.4 对账

日常不增加人工对账压力。只保留一个轻量检查：

```text
每日检查：主网关 tokenkey-edge-<edge> usage 与 Edge 的 tk_<edge>_edge_xxx usage 是否在合理误差内。
```

灰度期间人工抽查；正式开放前加入自动脚本。

## 8. 风险隔离策略

### 8.1 Anthropic organization 隔离

每个 Edge 只能放与该区域一致的账号/组织：

```text
region-consistent organization
region-consistent billing / payment
region-consistent 常用网络/设备信号
region fixed egress
```

不要把当前已 disabled 或疑似关联的 US OAuth organization 迁到其他区域继续试。

### 8.2 禁止跨国自动 failover

主网关可以有多个区域分组，但不要把它们做成无脑互备。

推荐：

```text
用户明确选择某区域分组 -> 使用该区域 Edge。
用户默认分组 -> 不自动 failover 到其他区域 OAuth。
官方 API / Bedrock / Vertex 可以作为合规 fallback。
organization_disabled 不触发跨国重试。
```

### 8.3 组织级熔断

当前代码行为是账号级禁用：

```text
account 35 -> organization disabled -> SetError(account 35)
```

建议主网关和 Edge 都具备组织级熔断意识：

```text
organization disabled
  -> 标记 account error
  -> 标记 same org / same credential family / same edge pool circuit_open
  -> 禁止同组织 failover
  -> 主网关停止选择 tokenkey-edge-<edge> 或对应区域资源池
  -> 告警人工处理
```

灰度阶段可以先运营手册执行；正式开放前建议代码硬化。

### 8.4 网关身份日志

主网关日志需要能看出请求经过了哪个 Edge：

```text
selected_account=tokenkey-edge-<edge>
downstream_edge=api-<edge>.tokenkey.dev
edge_region=<aws-region>
edge_country=<country>
```

Edge 日志需要能看出请求来自主网关：

```text
api_key_name=tokenkey-main-gateway
upstream_account=...
edge_region=<aws-region>
egress_ip=...
```

## 9. 路由与分组建议

### 9.1 主网关分组

在 `api.tokenkey.dev` 保留统一用户体系，新增区域资源分组：

```text
Group: default
  用途: 默认主资源，不自动使用区域 OAuth

Group: anthropic-uk1
  Platform: newapi
  Account: tokenkey-edge-uk1

Group: anthropic-sg1
  Platform: newapi
  Account: tokenkey-edge-sg1

Group: anthropic-fra1
  Platform: newapi
  Account: tokenkey-edge-fra1

Group: anthropic-us1
  Platform: newapi
  Account: tokenkey-edge-us1

Group: anthropic-official
  用途: 官方 API / Bedrock / Vertex
```

用户如果要使用某区域资源，通过主网关的 group/model 策略选择对应分组，而不是直接拿 Edge 的 key。

### 9.2 Edge 内部分组

每个 Edge 内部：

```text
Group: <edge>-edge-resource
  User: tokenkey-main-gateway
  Accounts: region-consistent upstream accounts only
  Rate limit: 保守
  Concurrency: 保守
  Models: 只开放实际可承载模型
```

### 9.3 模型暴露

主网关不要把 Edge 暴露成“万能 Claude”。只暴露 PoC 通过的模型：

```text
claude-opus-4-7
claude-sonnet-4-6
claude-haiku-4-5-20251001
```

每个模型应有明确：

```text
主网关售价
Edge 成本估算
是否支持 stream
是否支持 thinking
是否支持 count_tokens
失败 fallback 策略
PoC usage 是否通过
```

## 10. 为什么不采用多独立主网关

弃选方案：

```text
api-uk1.tokenkey.dev、api-sg1.tokenkey.dev、api-fra1.tokenkey.dev 等都对最终用户开放。
每个网关各自发用户 API key。
每个网关各自计量计费。
用户或运维手动选择 endpoint。
```

弃选原因：

```text
违背一个产品、一个入口。
用户体验割裂。
账务分散，OPC 运营压力上升。
配置和权限要多处同步。
故障归因复杂。
后续很容易滑向随机 IP 池。
```

保留多独立网关的唯一场景：灾难恢复。即 `api.tokenkey.dev` 主站不可用时，人工临时切到某个 Edge 或新 US clean stack；这不是日常路由方案。

## 11. 必要工程/运维改造

### 11.1 第一阶段：以 uk1 为样板的标准 Edge 改造

第一阶段的**设计目标**是首批 4 个 Edge：`uk1 / us1 / sg1 / fra1`。但实施顺序必须先以 `uk1` 作为样板完成改造和验证，再复制到其他 Edge。

```text
1. 参数化 EC2/CFN Stage 0，先支持 edge_id=uk1 的 edge-minimal profile。
2. 部署 uk1：api-uk1.tokenkey.dev / tokenkey-edge-uk1-stage0。
3. 在 uk1 创建 tokenkey-main-gateway 用户/API key。
4. 在 uk1 配置 UK-consistent 上游账号池。
5. 在 api.tokenkey.dev 创建 Platform=newapi 的 tokenkey-edge-uk1 账号。
6. 在 api.tokenkey.dev 创建 group.platform=newapi 的 anthropic-uk1 分组。
7. 对 uk1 跑计量 PoC：non-stream、stream、4xx、5xx/timeout、禁用 edge。
8. uk1 通过后，把同一 profile 扩展到 us1 / sg1 / fra1。
```

这不是把首批从 4 个缩小为 1 个；而是把 `uk1` 定义为首批 Edge 的实施样板和验收门禁。

### 11.2 上线前置门禁

以下事项必须在任一 `anthropic-<edge>` 给用户开放前完成：

```text
G1 接入机制固定为 newapi + OpenAI-compatible channel，配置字段落表可复现。
G2 该 Edge 的计量 PoC 全部通过。
G3 该 Edge API 路径只允许主网关 EIP 访问。
G4 deploy-stage0 支持 edge profile：edge_id / profile / stack / region / domain / tag / monthly_budget_usd / smoke_mode。
G5 一键禁用 tokenkey-edge-<edge> 的运营动作已验证。
G6 Edge 默认 profile 必须是 edge-minimal；升到 edge-standard 必须满足升配触发条件。
G7 每个 Edge 必须配置月预算告警，默认 monthly_budget_usd=16；超过预算先停灰度/降载，而不是直接升配。
```

### 11.3 第二阶段：小代码硬化

只做和多边缘安全直接相关的最小改动：

```text
组织级熔断：organization_disabled 不只禁单账号。
网关身份日志：主网关记录 edge account / edge domain / edge_region。
边缘健康检查：主网关能快速禁用 tokenkey-edge-<edge>。
对账脚本：主网关 edge usage vs Edge gateway key usage。
```

这些改动应遵循 upstream isolation：优先 companion file / 小 helper，不在大 upstream 文件里堆逻辑。

### 11.4 不做的改造

```text
不做中心数据库。
不做多主复制。
不做用户/API key 同步。
不做跨国自动 failover。
不做 IP 池随机轮询。
不做代理规避策略。
不新增 TokenKey-to-TokenKey 专用协议。
```

## 12. 落地顺序

### Step 0：止血

```text
1. 当前 34.194.234.88 关联的 Anthropic OAuth 池下线或 quarantine。
2. organization disabled 后不再同组织 failover。
3. 主流量优先切官方 API / Bedrock / Vertex / OpenAI/newapi。
4. api.tokenkey.dev 继续作为唯一生产入口。
```

### Step 1：参数化 Edge Stage 0

先做 `uk1` 可重复部署路径，再推广到完整 edge profile 矩阵。

```text
Phase 1 / uk1 sample:
  扩展 CloudFormation 参数或 wrapper，至少支持 edge_id=uk1 / profile=edge-minimal / stack / region / domain / environment=edge。
  新增 deploy-edge-stage0 作为 Edge 编排入口，但部署核心必须复用 prod/test 的 Stage0 primitive。
  抽出 scripts/stage0_verify_ghcr_manifest.sh / stage0_deploy_via_ssm.sh / stage0_external_health.sh，prod/test 和 Edge workflow 都调用同一套脚本。
  扩展 post-deploy smoke，支持 uk1 自身 smoke + 主网关经 uk1 smoke。
  确认 SSM SecureString、GHCR PAT、DNS、ACME email 都按 uk1 独立配置。
  确认 edge-minimal 默认 root=20GiB、DataVolume=0、低频/手工 snapshot、低内存 PG/Redis 参数。

Phase 2 / first-batch replication:
  uk1 验证通过后，把同一参数化路径扩展到 edge_id=us1|sg1|fra1。
  deploy workflow 接收 edge profile 矩阵，不复制脚本；preflight 必须阻止 deploy-stage0 与 deploy-edge-stage0 出现两套 SSM 部署逻辑。
```

### Step 2：部署首批 Edge 的实施顺序

首批设计仍是 4 个 Edge，但实施先验证 uk1，再复制：

```text
Sample first:
  uk1 -> eu-west-2 -> api-uk1.tokenkey.dev -> tokenkey-edge-uk1-stage0 -> edge-minimal

Replicate after uk1 PoC + 7 天灰度通过:
  us1  -> us-west-2      -> api-us1.tokenkey.dev  -> tokenkey-edge-us1-stage0  -> edge-minimal
  sg1  -> ap-southeast-1 -> api-sg1.tokenkey.dev  -> tokenkey-edge-sg1-stage0  -> edge-minimal
  fra1 -> eu-central-1   -> api-fra1.tokenkey.dev -> tokenkey-edge-fra1-stage0 -> edge-minimal
```

每个 Edge 部署后只做内部验证，不对用户开放。

### Step 3：配置 Edge 内部资源

```text
创建 tokenkey-main-gateway 用户。
创建 tk_<edge>_edge_xxx API key。
创建 <edge>-edge-resource 分组。
添加 region-consistent 上游账号。
设置保守 RPM / concurrency / quota。
跑 Edge 本地 smoke。
```

### Step 4：主网关接入 Edge

```text
在 api.tokenkey.dev 添加 Platform=newapi 账号 tokenkey-edge-<edge>。
channel_type = 1（OpenAI-compatible / ChannelTypeOpenAI）。
base_url = https://api-<edge>.tokenkey.dev。
api_key = tk_<edge>_edge_xxx。
创建/绑定 group.platform=newapi 的 anthropic-<edge> 分组。
只开放少量模型。
```

### Step 5：计量 PoC

```text
用主网关 API key 跑 non-stream /v1/messages。
用主网关 API key 跑 stream /v1/messages。
构造 edge 4xx、edge 5xx/timeout。
禁用 tokenkey-edge-<edge> 验证默认业务不受影响。
核对主网关 usage/billing 与 Edge usage。
```

PoC 不通过则该 Edge 停止，不进入灰度。

### Step 6：小流量灰度

```text
只给内部 API key 或少量测试用户开放对应 anthropic-<edge>。
观察错误率、延迟、token 计量、Edge usage。
发现 organization disabled / 401 / 429 异常立即关闭 tokenkey-edge-<edge>。
```

### Step 7：固化运营

```text
写入 runbook。
加入 post-deploy smoke。
加入边缘健康检查。
加入用量对账脚本。
再考虑日本 api-jp1.tokenkey.dev 或同区域第二 IP（如 api-uk2.tokenkey.dev）等第二批 Edge。
```

## 13. 风险与注意事项

### 13.1 成本控制：标准化不等于复制主站规格

EC2/CFN 标准 Edge 比 Lightsail 贵，但 Edge 不是主网关，不能默认复制 `api.tokenkey.dev` 的 `t4g.small + 60GiB EBS + DLM daily` 规格。

成本策略：

```text
主网关：保持当前 t4g.small + DataVolume + DLM 的生产规格。
Edge：默认 edge-minimal，以最低 EC2 成本开展。
Edge 升配：必须由资源指标、稳定流量或可靠性需求触发。
```

成本档位：

```text
edge-minimal: 目标接近 $10–16 / edge / month（区域价格会浮动）。
edge-standard: 约 $22–28 / edge / month，仅在触发条件满足后使用。
4 个 edge-minimal: 目标约 $40–64 / month。
4 个 edge-standard: 约 $90–112 / month，不作为默认预算。
```

这是用少量额外成本换取：

```text
统一 IaC
统一 SSM
统一 secret
统一 deploy
统一 smoke
统一排障习惯
```

但不为 Edge 购买主站级冗余和持久化，避免 OPC 标准化变成过度配置。

### 13.2 双 TokenKey 链路带来的计量偏差

请求会经过两套 TokenKey：主网关和 Edge。必须确认：

```text
主网关能拿到最终 token usage。
主网关按最终用户计费。
Edge 不对最终用户计费。
streaming 请求 usage 不丢。
错误请求不重复扣费。
```

### 13.3 延迟增加

路径变长：

```text
用户 -> US 主网关 -> Edge region -> upstream -> Edge -> US 主网关 -> 用户
```

影响：

- 首 token 延迟增加。
- 流式链路多一跳，断流概率上升。
- 大请求体跨区域传输成本增加。

缓解：

```text
Edge 只承接明确需要该区域出口的资源。
默认流量不走区域 Edge。
大上下文/高并发任务谨慎灰度。
```

### 13.4 Edge 不是风控豁免

如果 Edge 上放的是与区域不一致的账号/组织，风险不会消失，只是换了一个触发点。

要求：

```text
账号、组织、支付、常用环境、出口尽量区域一致。
不迁移已 disabled 的 US OAuth organization。
不跨国共享 refresh token / OAuth credential。
```

### 13.5 主网关需要能一键下线 Edge

主网关必须能快速停用：

```text
tokenkey-edge-<edge> account
anthropic-<edge> group
对应模型映射
```

停用后不影响默认组和其他官方资源。

### 13.6 Edge 安全边界

`api-<edge>.tokenkey.dev` 不直接面向最终用户，默认不应对全网开放 API 路径。

默认要求：

```text
Security Group 只做端口层边界：80/443 按 Caddy/ACME 需要开放，SSH 默认不开放或限制 AdminCidr。
Caddy 做路径层边界：/v1/*、/api/* 等 API 路径默认只允许主网关 EIP。
调试访问使用短时 allowlist，不常态开放 0.0.0.0/0 到 API 路径。
只给主网关专用 key。
强随机 API key。
最小模型权限。
保守 rate limit。
后台管理员强密码/TOTP。
日志不要泄露上游 credential。
SSM 运维，不开放 SSH 或限制 AdminCidr。
```

注意：限制只允许主网关 EIP 访问 Edge，是为了减少 Edge 的公网滥用面；它不会改变上游看到的区域出口 IP，因为上游请求仍从 Edge 出去。

### 13.7 发布漂移风险

多 Edge 必须避免手工漂移，尤其不能让主网关和 Edge 形成两套不同部署逻辑。

要求：

```text
deploy-edge-stage0 只是 Edge 编排入口，不复制 prod/test 的 SSM 部署命令。
deploy-stage0 与 deploy-edge-stage0 必须共享 scripts/stage0_deploy_via_ssm.sh。
主网关和 Edge 都部署同一个 GHCR image tag 产物、同一份 docker-compose 基线。
Edge 只允许通过 target matrix / CFN profile / Caddy allowlist 表达差异。
preflight 必须检查两个 workflow 都调用共享部署 primitive，禁止重新内联 SSM deploy SOP。
release 后 Edge 是否跟随升级必须有明确策略；默认按批次升级，不自动无差别跟随 prod。
每次升级后跑 edge smoke + 主网关经 edge smoke。
```

## 14. 成功标准

第一阶段成功标准分两层：

```text
uk1 sample success:
  用户只使用 api.tokenkey.dev。
  uk1 edge-minimal 可稳定运行。
  api-uk1.tokenkey.dev 可从 UK 出口完成 /v1/chat/completions。
  主网关可通过 /v1/messages 选择 tokenkey-edge-uk1 并正确计费。
  主网关 usage/billing 正常入账。
  api-uk1 API 路径默认只允许主网关 EIP。
  禁用 tokenkey-edge-uk1 后默认业务不受影响。
  uk1 连续 7 天无 organization_disabled / 异常 401 / 异常 429 风险信号。

first-batch replication success:
  us1 / sg1 / fra1 复用 uk1 profile，无脚本分叉。
  每个 Edge 都通过同一 PoC 清单。
  每个 Edge 都有 monthly_budget_usd=16 预算告警。
  禁用任一 tokenkey-edge-<edge> 后默认业务不受影响。
```

## 15. Review 问题

需要重点评审：

1. 首批设计矩阵是否就是 `uk1 / us1 / sg1 / fra1`，并以 `uk1` 作为唯一先验样板？日本 `jp1` 和同区域第二 IP（如 `uk2`）是否放第二批？
2. `anthropic-<edge>` 是作为显式用户可选分组，还是只给内部测试 key 开放？
3. 组织级熔断是否作为正式开放前置条件，还是内部灰度阶段先用手工 runbook 控制？
4. Edge 成本价如何映射到主网关售价，是否需要单独 pricing catalog 标签？
5. `deploy-stage0` 是扩展现有 workflow，还是先新增 `deploy-edge-stage0` workflow？
6. `us1` 选择 `us-west-2` 还是 `us-east-1` 新 EIP？为了和当前可疑 EIP 隔离，默认倾向 `us-west-2`；如未来需要第二个美国 IP，命名为 `us2`。
7. Edge 默认最低成本 profile 选 `t4g.micro` 还是允许 `t4g.nano + swap` 实验？
8. Edge 是否默认不启用 DataVolume，只用 20GiB root；哪些 Edge 需要 20GiB DataVolume？
9. 每个 Edge 的月预算上限是否统一为 `$16`，超过预算时是停灰度、降载，还是升配？

## 16. 参考资料

- [Claude Code error reference](https://code.claude.com/docs/en/errors)
- [Claude Code troubleshoot installation/login](https://code.claude.com/docs/en/troubleshoot-install)
- [Managing API Key Environment Variables in Claude Code](https://support.anthropic.com/en/articles/12304248-managing-api-key-environment-variables-in-claude-code)
- [Using Claude Code with your Team or Enterprise plan](https://support.anthropic.com/en/articles/11845131-using-claude-code-with-your-team-or-enterprise-plan/)
- [Anthropic IP addresses](https://docs.anthropic.com/en/api/ip-addresses)
- [Restrict access to Claude with IP allowlisting](https://support.claude.com/en/articles/13200993-restrict-access-to-claude-with-ip-allowlisting)
- [Does Claude use my location?](https://privacy.claude.com/en/articles/11186740-does-claude-use-my-location)
- [Claude Code on Amazon Bedrock](https://docs.anthropic.com/en/docs/claude-code/amazon-bedrock)
- [Linux.do: This organization has been disabled 好像不是封号但是用不了 Claude 了](https://linux.do/t/topic/1062012)
- [Linux.do: claude code 对 IP 有要求吗](https://linux.do/t/topic/1831124)
- [Linux.do: Claude 网页使用后 organization disable](https://linux.do/t/topic/1903579)
- [Linux.do: Claude 神奇风控策略](https://linux.do/t/topic/1796009)
- [Linux.do: Claude 注册+支付稳定路径尝试](https://linux.do/t/topic/1797342)
- [Linux.do: Claude 封号潮讨论](https://linux.do/t/topic/1790909)
- [Linux.do: Claude Code 官方 Max 订阅封号条件讨论](https://linux.do/t/topic/1738356)
- [Reddit: Unable to use Claude, does Anthropic ban IPs?](https://www.reddit.com/r/ClaudeAI/comments/1rax0vz/unable_to_use_claude_does_anthropic_ban_ips/)
- [Reddit: Claude account disabled twice](https://www.reddit.com/r/claude/comments/1sp4grc/claude_account_disabled_twice_main_partially/)
- [GitHub issue: anthropics/claude-code #8327](https://github.com/anthropics/claude-code/issues/8327)
