# 已决定

## 2026-11-18 QA capture 按用户关闭；双站关闭 compute@tk.com

原话：「安排 agent 改造 QA capture，支持按用户关闭，并把 双站的 compute@tk.com 用户的 关闭。」

适用范围：`users.qa_capture_enabled`（默认 true）。站点级 `qa_capture.enabled` 可保持开启；用户为 false 时网关不持久化该用户请求/响应正文与 QA blob。Admin `PUT /api/v1/admin/users/:id` 与用户编辑 UI 可改。发版并跑 migration 后，在 prod 与 cn-prod 将 `compute@tk.com` 设为 false。

## 2026-11-18 多地域 + 多应用机 = 等效容灾/高可用（投标与对外表述）

原话：「多可用区或等效容灾，我们已经具备了，现在 prod 和 cn-prod 是多地域的。而且每个地域都是多应用机，高可用的。请改仓库文档表述。」

适用范围：对外/投标将「腾讯美西 prod + 腾讯北京 cn-prod」双站点（库与控制面独立）+ 每站入口 WRR 多应用机滚动，表述为**多地域等效容灾与站点内应用高可用**。地域内入口机仍可不做热备（故障靠重建），该细节不否定上述等效容灾表述。见 `deploy/hetzner/README.md`、`deploy/domestic/README.md`、`docs/tender/tokenkey-control-parameters.md`。

## 2026-11-18 运维旁路与密钥材料收敛到腾讯云

原话：「运维旁路与密钥材料，也应该改到 腾讯 tat 或者 cos 等，收敛到腾讯云体系内。」

适用范围：在日常发版已默认 TAT 之后，把仍依赖 AWS Hybrid SSM / OIDC / Parameter Store SecureString 的运维旁路与密钥材料，迁到腾讯 TAT（远程执行）与 COS/主机 root-only 文件（密钥与恢复材料）。不覆盖 Oregon 出口机、Kiro/Bedrock 等供应商 AWS 协议、也不要求立刻废弃 GHCR 镜像源。设计见 `docs/approved/design-tencent-ops-control-plane.md`（2026-11-18 批准；实现中）。

后续确认（2026-11-18）：「同意。请推进实现。」→ 设计批准；先合入门牙门禁，再按 `rg` 逐入口改 TAT / 迁密钥。

## 2026-11-17 cn-prod tokensea missing text part 网关兼容

原话：「请修复。」（前置：「建议网关修复。另外，网关修复之后，和 kvv 冲突吗？」已确认与 KVV 不冲突。）

适用范围：Volc Agent Plan kimi 入站。`dropVolcKimiEmptyTextParts` 在已有 `text:""` 丢弃之外，再丢弃 `type=text` 且 `text` 缺失/null 的 part（Volc `missing messages.content.text`）。不改字符串 `content:""`、不改 KVV 动态 tools / sampling。见 `docs/approved/volc-kimi-chat-compatibility.md`。

## 2026-11-17 cn-prod 镜像标准路径：prod 导出 → 北京 COS → 同城拉取

原话：「北京的镜像应该从 prod 导出上传到 北京 cos，再从北京 cos 拉下来使用。这条变为标准路径」

适用范围：cn-prod 镜像分发。默认不再从美西 GHA 直传 COS，也不以 GHCR 跨境 pull 为主路径。标准顺序：在 prod 上 `crane pull --format=legacy` 并上传北京 COS → cn-prod 各应用机同城 COS warm → rolling。GHA→COS / `transport=ghcr` 仅作应急回退。见 `docs/approved/design-cn-image-cos.md`、`cn-prod-image-distribution.md`。

后续校准（同日）：「今天只准备好 cn-prod 的镜像，不发版了。」→ 已对 `1.8.382` 完成 prod→COS→warm，未 rolling。运行时修正：现代 `docker save` OCI 布局与 legacy `verify_archive` 不兼容，标准路径改 crane-legacy；OIDC 角色 trust 增加 `environment:tencent-ops`（`publish-from-prod` 仍挂该 environment 以读腾讯密钥）。

## 2026-11-17 cn-prod rolling：drain 超时强排（有上限）

原话：「应该超时强排吧」；后续收紧：「强排应该有限制：超时（10分钟）且 in-flight 不超过 10 才强排」

适用范围：cn-prod `rolling.py`。`in_flight` 等满 `DRAIN_MAX`（10min）后：仅当 `/health=503` 且 `0 < in_flight ≤ 10` 时强排继续；未进 503，或 `in_flight > 10`，仍 abort（USR2 放回旧容器）。写入 `docs/approved/design-cn-prod-rolling-deploy.md` §4。

## 2026-11-17 cn-prod 镜像分发改腾讯 COS

原话：「Tokyo 镜像分发桶 → COS。同意。请继续。务必精简、高效，不要复杂化。」

适用范围：cn-prod 镜像中转从 Tokyo AWS `cfn-deploy` 改为北京 COS；publish/warm 用既有 `tencent-ops` 凭据。不改 prod GHCR 路径、不搬历史对象。设计见 `docs/approved/design-cn-image-cos.md`。

## 2026-11-17 cn-prod 压测扩容准备（800 并发前）

原话：「1、开个专用的压测机。临时用2天。2、还是维持2台应用机，但是 app-a 和 app-b 都使用 4C 的吧，先升级一下？3、同意：DATABASE_MAX_OPEN_CONNS 40→80/应用，并上调数据机 max_connections」

后续确认：「1. app-b 改为 4C8G，同意生完权重1:1；2.同意。低峰需要发我审批；3.同意。」（按量压测机）

适用范围：cn-prod。双应用机保持两台；app-b 由 2C4G 升到 `bundle_rs_mc_lg8_01` **4C8G**（非 4C16G）；入口 WRR **app-a:app-b = 1:1**。应用侧 `DATABASE_MAX_OPEN_CONNS` 40→80（idle 10→20）。数据机 `max_connections` 200→400 **须低峰窗口审批后才重启 Postgres**（审批 `docs/ops/approvals/cn-prod-pg-max-connections-200-to-400.md`；**2026-11-17 13:46 BJT 已执行**，`SHOW max_connections=400`）。专用压测 client：北京按量 CVM 约 2 天（`ins-7ltl2zv3`，tag `expire_after=2026-11-19`），不放在 `cn-ip*` 代理机或应用机上。依据：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`。

后续确认（2026-11-17）：「client 换 8C 同意。再压一下并发 1200 吧。重点关注吞吐已明显亚线性的根因。」→ 压测机由 `SA2.LARGE8`（4C8G）升到 `SA2.2XLARGE16`（8C16G）；阶梯含 1200，并采双应用/数据机 monitor。

结果落盘（同日）：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-1200-20261117.md`。operating point 仍 **800**；1200 成功率 ~20% **主因是 client soft `ulimit -n=1024`（Errno 24）**，网关 503 仅数十次；400→800 亚线性主因是 **延迟/TTFT 随并发抬升**（升 8C 后 800 RPM mean 仍≈1086，与 4C 轮同级）。数据机高压窗 loadavg 一度 >20、PG 瞬时 ~172/162，服务未挂。

## 2026-11-17 对象归档禁新写 AWS，改腾讯 COS

原话：「核心是『以后凡是对象归档都别再落 AWS』。」「对的。确认。」

适用范围：ops 冷数据 promote/export、host-config 等对象归档的**新写入**只允许腾讯 COS；禁止再落到 AWS S3。不覆盖 Oregon 出口、供应商 AWS；镜像分发另见上条。pgBackRest/QA 既有 COS/本地契约不变。设计见 `docs/approved/design-cos-object-archive.md`（2026-11-17 乔布斯复审后批准）。

## 2026-11-17 AG/Gemini 用量 TTFT：硬编码 visible，thought-only 显示 —

原话：「同意。」（针对：AG/Gemini 先硬编码 visible、不做配置开关；thought-only 终态继续 null→「—」。）

适用范围：用量「首内容」列与 AG/Gemini 原生流 `first_token_ms`。keepalive/纯 thought 不计 TTFT；无可见内容不硬凑时间。见 PR #3243。

## 2026-11-17 AG argon maxOutputTokens：只钳显式超大、不默认注入

原话：「若要对齐开源精神又留安全网：更干净的是 只钳非法/超大、不默认注入。」

适用范围：Antigravity Gemini 原生 ForwardGemini 上 `gemini-4-argon*` 的 `max_output_tokens_cap=8192`：客户端显式更大才钳到 8192；未设字段不注入。`thinkingBudget` 合法域 clamp / level 模型丢弃非法 budget 仍生效。见 PR #3242 / `docs/spec-delta-ag-argon-thinking-clamp.md`。

## 2026-11-17 cn-prod tokensea interleaved assistant 网关兼容

原话：「查下这些 400，判断一下是 rt@tk.com 在做兼容性测试还是在真实业务调用，如果是真实业务调用，我们应该要做网关兼容。」

适用范围：已判定 `rt@tk.com` / `tokensea_online` 为真实业务流量。对 `kimi-*` 在转发前丢弃夹在 `assistant.tool_calls` 与对应 `tool` 结果之间的无 `tool_calls` assistant 文本；普通非 tool round-trip 的 assistant 文本不动。契约记入 `docs/approved/volc-kimi-chat-compatibility.md`。

## 2026-11-15 美西 prod 入口机 + 多应用机

原话：「1-2 确认。4 确认接受丢失。3 同 cn-prod 一样，不需要热备入口？请安全推进，不要影响线上用户流量和请求。」

适用范围：美西 prod 推进入口机 + 双应用机 + 数据机。`lhins-2kf31667` 即「新 2C8G」，不另买应用机；发版走滚动、不留节点内蓝绿；不做旧机热备入口（切流稳定后关公网 80/443，入口故障靠重建）；脱离 LUKS 时 `qa_blobs` 超过 24h 接受丢失。设计见 `docs/approved/design-prod-entry-multi-app.md`（2026-11-15 会话已批准设计；购买入口机与切 DNS 仍须逐步确认）。

## 2026-11-14 Antigravity Claude 进 claude 组

原话：「只试点 ultra，裸名映射到 -medium。」「268 / 291 / 294，把他们做和 ultra 一样的改动。」

适用范围：prod 可调度 Ultra（233/244/292/529）与已 advertise Claude 5.5 的 Pro（268/291/294）。裸名 → `-medium`，档位透传；`mixed_scheduling=true`；挂 `claude`+`Google-Antigravity`。其余账号与 SSOT floor 不动。

## 2026-11-14 Kiro 缓存拆分比例

原话：「改为 10% 按缓存读取，另外 90% 仍按输入价。」「新写进去的后缀全部按缓存计入，按未知 TTL 的 1 小时价结算。」

适用范围：Kiro 本地前缀指纹。满 1024 才拆：读侧 10% `cache_read` / 90% 输入价；新后缀满 1024 全记 `cache_creation`（未知 TTL→1h 写入价）。只影响新结算。

## 2026-11-14 Kimi 两套 effort 与递归 schema 文案

原话：「1. 两套 effort 同时出现，同意建议。2. 8 项递归 schema 建议改下文案。」

适用范围：`kimi-k3`：有 `thinking.effort` 且思考未关时，以它覆盖 `reasoning_effort`。`kimi-*` 带工具 `parameters` 的递归 schema 400 文案补 `parameters schema`。不停词、不改 `prompt_tokens`。

## 2026-11-13 Gemini 截断冷却（最终）

原话：「空前缀换号不计数；已写出正文/工具/媒体的截断，1 分钟内满 3 次才冷却；档位不变。」

适用范围：原生流截断与 `unexpected EOF`。冷却仍走 `anthropicCooldownTierLadder`。`INSUFFICIENT_G1_CREDITS` 立刻停调度 30 分钟。取代此前 15 分钟窗口方案。

## 2026-11-13 Gemini 4 Hydrogen 裸名映射 medium

原话：「能访问的账号加上 hydrogen low/medium/high/xhigh，裸名映射到 -medium。」

适用范围：已实测可访问 Hydrogen 的 Antigravity 账号。档位 wire 不上公开目录；不能访问的不加。

## 2026-11-12 无供给模型 404，supported=0 则 400

原话：「没有任何可调度账号能提供的模型返回 404，不要可重试 429。」「supported=0 → 400；ready=0 才是容量问题。」

适用范围：万能钥匙选号。`supported=0` → 400 `unsupported_request`；`supported>0` 且 `ready=0` 仍 429。不改协议 Plan。

## 2026-11-12 Gemini 截断熔断与原生请求体校验

原话：「第 1 个 和 第 3 个，同意建议，请修复。」

适用范围：短窗口反复原生截断/`unexpected EOF` 临时移出调度；`INSUFFICIENT_G1_CREDITS` 单独停用。缺 `contents` 或顶层误带 `messages`/`max_tokens`/`input`/`store` → 本地 400。Antigravity 只接受 `inlineData`，拒绝 `gs://` 与 http(s) `fileUri`；不改写成 Chat。

## 2026-11-12 手动停调度不进 Plan

原话：「schedulable=false 不应该进 plan。」「同意。请修复。」

适用范围：`Schedulable=false` 在 Plan 前排除。临时不可调度/限流/过载/配额仍进 Plan，仅这些路径时为容量 429。

## 2026-11-12 Gemini 协议转换按 Chat 直接路径放宽选号

原话：「同意。按照建议方案从 1-4 逐个实现。」

适用范围：Chat→Gemini 直转；Messages→Gemini 保留图/thought/tool_choice；Responses 先桥 Chat 再转；原生带图仍 identity。Antigravity OAuth 不声明原生 Chat/Responses/Messages。远程图 URL、strict、关并行、内置+function 混放仍拒。

## 2026-11-12 Gemini 4 Argon / Carbon 默认 medium

原话：「carbon/argon 默认映射到 -medium。」

适用范围：Antigravity 上 argon/carbon 裸名默认 medium；Flash 仍锁 high。显式档位按请求。

## 2026-11-12 去掉 typed tokensea relay 地板

原话：「没有 tokensea 账号，是供应源受管的。」「同意。继续。」

适用范围：删 openai/anthropic 上 tokensea relay 识别/地板/目录/调度特例。`agent.tokensea.ai` 请求体剥离按 host 保留。不改现网映射。
