---
title: Gateway capability verification independent of deployment
status: draft
risk: high
---

本会话用户已同意能力验证与部署解耦；代码合并、真实上游执行、切流仍是不同动作。

## 边界与 owners

网关正常请求链不承担部署回放的采集要求。移除仅为 replay 新增的
`RequestPath/original_path` 采集，原有 QA lifecycle、截断与脱敏策略继续由 QA owner 负责。
历史回放是可选隔离实验；历史 body、用户、key 配额和 QA 归档可用性不再定义网关能力分母。
旧回执保留原 verdict，不回填或改写。普通 staged promote 校验 prepared candidate 的审批；
仅显式提供 `APPROVED_REPLAY` 时才校验历史实验回执。任何 coverage 报告均不能授权切流。

| Owner | 职责 |
|---|---|
| `ops/stage0/gateway-account-supply.json` | 脱敏账号执行类与已评审的模型族代表；不包含账号 ID、凭据、供应商 URL、用户数据或实时状态 |
| `ops/stage0/gateway-capability-matrix.json` | universal 请求模板，不再参与模型全集生成 |
| `ops/stage0/fixtures/gateway/` | 短合成请求；模板与完整场景执行能力分开记录 |
| `ops/stage0/gateway_capability_matrix.py` | 账号类/模型族基础义务、类级协议和请求分支、稳定 ID、digest、增量与报告 |
| `ops/stage0/gateway_capability_check.py` | HTTP 响应、工具、图像、音频与转录语义校验 |
| `ops/stage0/gateway_capability_scenarios.py` | 短媒体请求与保留真实调用 ID／思考签名的工具续轮 |
| `ops/stage0/gateway_capability_host.py` | 隔离副本内创建受限测试用户与 universal key；账号类绑定、容量守卫、串行执行、usage 归因与清理 |
| `ops/stage0/post_release_replay_check.py` | plan/report/from-tag；显式授权的 prod-host run 复用同一个执行器 |
| `scripts/stage0/replay-prod-release.py` | prepare 后执行账号供给计划，取回带指纹的逐条结果；不调用历史 capture 收集 |
| `scripts/stage0/update-capability-plan.py` | 校验供应清单和生成 release artifact；preflight 通过 `--check` 校验 |
| `scripts/stage0/check-gateway-capabilities.sh` | 无网络、无付费调用的 post-release 计划与缺口报告 |

## 有限集合和成本

用户本会话确认：以实际账号供应归类，同类账号内同类模型取代表，发布主验证只用
universal，去掉 direct 重复维度。公开 catalog 不是可服务全集；删除其 exporter 和快照。

供应清单记录 platform、auth type、channel type、执行方言、原生协议集合及 endpoint
声明方式。相同执行路径的地区和账号数量不扩展 case；Kiro 镜像不得误归 Anthropic OAuth。
每个账号类内模型版本/别名按评审后的模型族压缩；Fable、显式 thinking、Codex spark/review、
Gemini 代际、vision 与独立媒体分支按当前试算保留。该等价划分是验证抽样，仍需核对
各 adapter 分支，不能推导同族所有模型已经测试通过。

每个模型族一个基础义务；每个聊天账号类另补未覆盖的 generation 协议入口和
stream/tool roundtrip/thinking/vision/count tokens 请求分支。请求分支复用清单中显式的
branch_family，不因线上调用频率变化自动改选代表。仓库内不保留原始流量计数。
默认输出完整计划；`--limit` 仅显式限制有模板且无设施阻塞的选择数量，不缩小报告分母。
不恢复默认抽样截断，不增加 direct smoke。

`--previous` 只比较声明/fixture digest，不代表执行历史。模板、代表或账号类语义改变会
使相应证据过期；清单中的集合顺序不改变 digest；case ID 包含账号类，同一模型在不同账号路径上的成功不能相互替代。
新模型映射到已有等价类时更新 represented_models；新执行分支另增代表或账号类。
供应刷新是显式脱敏盘点后评审清单，尚未实现线上 inventory 自动归类；post-release
消费版本化清单，不谎称已自动发现最新线上账号变化。暂时冷却/无供应不是删除长期义务的依据。

## 计划与执行的界限

用户在本会话继续授权实现并验证，附加约束为禁止切流、控制并发、不影响线上用户。
`plan_validation=required` 仍不因原生协议声明而消失：文本请求实际经过候选网关的
canonical routing；只在响应正确且真实 usage 属于绑定账号时记录通过。媒体走现有
media handler；不另造 Python 协议路由策略。count-token endpoint 本身不计费时，
结果明确标记 unmetered_endpoint，不能假称取得账号计费证据。

运行时以 platform/type/channel/dialect/native protocols/exclusive endpoints 和代表的
显式 model_mapping 匹配当前健康供给。缺供给、映射漂移不删用例、不修改线上账号。
独立 PostgreSQL/Redis 复用 prepared image ID；启动应用前在副本建立每账号专属组和
受限用户，测试 key 必须为 universal。用户仅获准对应组，组内仅绑定目标账号，
每次调用前复核绑定；正确响应但命中其他账号或缺 usage 不得通过。

并发固定为一，所有请求（包括工具续轮和视频轮询）共享最小间隔；主机余量和线上账号
剩余槽位由执行器检查。副本 dump/restore/startup 期间每两秒复核主机余量，超限中止并清理。生产 SQL/Redis 只读，副本数据库写测试用户、key、分组和 usage。
遇到容量不足不重置线上 cooldown；限流、服务不可用、超时或未结束视频任务停止后续付费调用。
所有剩余用例保留停止原因。生产 usage 必须没有本次响应 request ID，active/candidate/Caddy
指纹必须一致，临时资源清理失败使通过证据失效。

视觉场景使用本地生成的纯色图片并校验颜色答案；思考场景要求 reasoning/thinking 内容或 token 证据。
工具场景执行真实 tool call 和 tool result 第二轮；Gemini 图片请求要求 IMAGE 输出；
语音使用模型对应 voice；转录使用本地合成的 Hello WAV multipart；视频串行轮询到终态。
OpenAI Chat 未定义 count-token 操作，该义务明确为 unsupported，不猜路径，也不算通过。
coverage 不可由 manifest 手写 passed；harness 成功不算网关实测成功。真实错误和安全阻断
均如实报告，本次授权不是要求把所有组合改成绿色。

账号供给回执独立存于 bluegreen-capability-replay.json，旧历史回执不覆盖。
新回执是审核材料、deployment_gate=false，不授权 promote，也不能冒充历史回执去切流。

## 本次旧缺口的处理

| 旧聚合原因 | 补充方式 |
|---|---|
| body_missing_or_redacted（40） | 使用短合成模板，不扩大 QA capture 或修改脱敏 |
| capture_endpoint_ambiguous（4） | 显式 count_tokens/input_tokens/Gemini action 模板，不猜历史路径 |
| capability_not_declared（2） | 从账号映射及操作类型生成义务；媒体缺执行器保持可见 |
| unsupported_path（3） | 用受支持模板路径；历史路径未逐项还原，不声称已定位为某特定协议 |
| historical_response_error（1） | 错误/SSE 异常进入离线验证器负例，不使用失败请求作成功基线 |
| key_quota_exhausted（4） | 专用测试 key ID 绑定；校验 snapshot 中真实 routing_mode，缺失/耗尽报设施缺口 |

这里是旧聚合的处理映射，不是虚构的逐条 54 行审计。媒体类别不能从旧计数推断。

## 使用与验收

```sh
python3 scripts/stage0/update-capability-plan.py --check
bash scripts/stage0/check-gateway-capabilities.sh /tmp/gateway-check
python3 ops/stage0/post_release_replay_check.py plan \
  --inventory ops/stage0/gateway-account-supply.json --out /tmp/plan.json
```

新供应清单可用 `--inventory` 或 shell 入口的 `CAPABILITY_INVENTORY` 指定；校验拒绝
额外账号字段、未知操作/协议、重复模型族和不存在的代表。数目从输入计算，不把本轮规模
当上限。CI 对本轮已接受的集合做回归断言，避免重新展开 catalog 或添加 direct。

post-release 使用目标 tag 的供应清单与 fixture，从上一 tag 还原同格式 baseline；
老 tag 尚无账号供应清单，或 tag 中的生成器与当前不同，则报告 baseline_available=false，
使用完整计划，避免以新规则重写旧义务而漏掉新增分支。
自动上传 plan/coverage artifact，不自动执行上游请求，不参与切流审批。
