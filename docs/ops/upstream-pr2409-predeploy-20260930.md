# PR #2409 上线前评估与降风险

## 范围与结论

用户授权先评估、消除或降低负面影响。本轮只读生产状态、在 PR 分支修复与本地验收，
不部署、不改生产配置、不兑换 reset grant、不运行付费模型探测。

生产实测镜像为 `ghcr.io/youxuanxue/sub2api:1.8.269`，对应 git tag commit
`8b6afbf916d066b60afb60e874364c0c775bf441`，active color 为 green。
评估不能只比较 PR base：发版还会带入这个运行版本之后已合并的主线改动。
运行版本至本 PR 的 Ent schema 和数据库 migrations 没有差异；已有日志和设置的
跨版本语义仍需单独审视，不能据此声称回滚没有状态副作用。

**结论：技术风险已收敛为保守默认及可选择能力；业务已确认 Astra ultrafast
按 6 倍标准价执行。本确认仅解除定价决策门禁，不授权部署或流量切换。**

## 生产只读证据

暴露快照时间：2026-09-30 13:07:49 UTC / 21:07:49 Asia/Shanghai。
用量窗口：2026-09-29 13:07:49 至 2026-09-30 13:07:49 UTC。

| 证据字段 | 实测值 | 判断与边界 |
|---|---|---|
| `RUN_MODE` | `standard` | 不能依赖 simple mode 自动跳过计费预留 |
| 新预留相关 env/YAML override | 未发现 | 不修默认值就会在升级时自动开启 Redis 预留；只核验了 prod，未冒充全 Edge 快照 |
| `requests` / `users` | 156508 / 6 | 来自成功用量记录，不包含失败或尚未落账请求 |
| `non_subscription_requests` | 156508 | 不能假设新增余额准入只影响很小的订阅外群体；具体持久化 hold 覆盖面仍由入口代码裁决 |
| `astra_ultrafast_requests` / `ultrafast_requests` | 0 / 0 | 此窗口未观察到定价变化暴露，不等于未来没有该档位请求 |
| `max_non_deleted_keys` | 64 | 低于新默认 200；统计覆盖全部未删除 Key，而非只看活跃用户 |
| `users_at_new_key_limit` / `users_at_new_hourly_limit` | 0 / 0 | 当前未触发新创建门槛；不是以后批量创建的容量承诺 |
| 最近活跃用户余额分布 | 无非正余额、无余额不超过 5 的用户 | 不能从当前余额推断所有未来并发请求均可准入 |
| `cyber_session_block_enabled` / `risk_control_enabled` | `false` / `false` | 尊重生产显式设置，不为本轮评估打开风控 |
| 白名单设置 / 新 log-only 违规日志 | 未设置 / 0 条 | 当前无已观察到的回滚补计数暴露；白名单激活仍须跨版本门禁 |
| Redis `maxmemory` / policy | `0` / `noeviction` | 现有运行配置，不在本次修改；新增预留默认关闭，避免静默增加 Redis 热路径开销 |

另外运行既有用户计费盯盘脚本，窗口为 2026-09-30 12:31:10 至 13:01:10 UTC。
它与暴露快照不是同一窗口，不合并为一个请求量，也不把用量记录当作完整 access 成功率。

采集经 `ops/observability/run-probe.sh`；新增暴露查询使用 read-only transaction 和
12 秒 statement timeout，仅输出聚合，不输出用户身份、Key、凭据或请求正文。
SSM 暴露快照 command 为 `f148d019-4369-4e2a-9845-ae0469ce3ef0`；计费基线 command
为 `09bfd003-01fa-427b-b8e5-73cdbc06d3f8`。原始工件保存在本次会话的私有临时目录，
不会提交包含用户身份的计费原始输出。

## 已落实的降风险措施

### 新余额预留显式启用

`billing.inflight_reservation.enabled` 默认由上游的 true 改为 false，示例配置同步。
原有 TokenKey PostgreSQL 持久化 hold、候选结算源切换和余额门禁不关闭；新增 Redis
预留实现、估价、续期与异步结算引用完整保留，供后续受控启用。

默认升级不会增加这套预留的 Redis 登记/释放操作、续期协程或估算金额并发拒绝。
代价是尚未由原持久化 hold 覆盖的路径暂不获得新 Redis 预留的额外防透支收益，
不是宣称全入口已经硬保证零透支。

启用前需检查目标实际配置及生产规模下的 Redis 资源、长流/断连/异步队列、低余额
并发准入；估价输入没有完整历史 max_tokens 证据，未伪造历史拒绝率模拟。

### 首内容前保活显式启用

新增 `gateway.antigravity_pre_content_keepalive_enabled`，默认 false。
关闭时不运行 15 秒首内容保活或其两分钟截止计时器，保留原数据间隔超时与内容之后
的 keepalive；空流尚未提交 HTTP 响应时继续返回可失败切换错误。

显式开启后保留上游保活与截止语义：HTTP 200 已提交后的错误走 SSE，不能透明换号。
该模式只在目标客户端能识别 SSE error、监控不把 HTTP 200 当成功时再启用。
默认关闭不消除慢首包被客户端/代理超时的既有风险，只是不静默改变失败切换能力。

### 首页真实环境验证

此前缺 MP4 的本地失败是验收环境遗漏，不是线上资产被删除：#2052 已将视频迁至
S3/宿主机 `data/public` override，不应把 12MB 视频重新放入 git 或默认镜像。
线上资产返回 `video/mp4`；下载仅作本地 fixture，SHA-256 与已批准 provenance 的
`8b37bc3ee6a15bfeeb0f5df7f7348db5e4e1d529992be9681ce96d1f2e96620f` 一致。

本地使用 HTTPS 代理、忽略仅本地自签证书，并提供同哈希资产，重现部署的 override。
未修改 Playwright 断言。手机首页间距问题真实复现，修复共享 landing owner 的
手机间距，不改文案、字号或桌面间距；双品牌首页完整验收通过。
正式发布仍必须验证目标宿主机的 media override，不能用本机 fixture 证明全 fleet。

## 保留的门禁与未决项

本次保守默认不删除上游功能：API Key 创建防滥用、风险白名单、reset credits
入口、用户趋势与 Codex catalog 能力保留；主线 AG 图片空包不扣费及 strict canvas
放大保障也保留。它们的收益不能等同于已在生产生效，reset 兑换与新配置仍未实操。
新增 Redis 预留和首内容保活的收益则刻意延后，换取默认升级时较小的运行时变化。

- **定价**：业务已确认 Astra `ultrafast` 按 6 倍标准价执行；代码保持现状，没有
  擅自改回 2 倍或修改生产价格注册表。当前窗口无此档位请求，但上线上线后该档位
  将按确认口径计费；上游降档按现有结算 owner 裁决，响应不能把普通请求升级收费。
- **风控回滚**：不在本次启用白名单。若后续启用且写入 flagged log-only 日志，
  旧镜像可能重新计入自动封禁窗口；须先保证回滚镜像理解免罚模式，或由专用风控
  流程形成获批的兼容方案。禁止删除/重写证据日志来制造无风险结论。
- **API Key**：保留 200/60 限额及删除不返还计数。批量轮换超限需独立调整计划，
  不为不存在的当前触限而关闭防滥用保护；现有 Key 不因创建限额被撤销。
- **reset**：保留账号 TLS、canonical UA、禁止重定向、public-host、幂等与 fence。
  仅部署不兑换 grant；真实兑换资格与效果仍未验证。
- **Edge 与规模**：当前运行态证据只覆盖 prod。首个 canary 必须由既有容量选择器
  选择，核查其有效配置与静态资产，再按既有 full smoke / delayed verdict 流程。
  没有做付费上游或生产流量重放，也不把单测当压力测试。

## 验证入口

默认值与显式 env opt-in、默认 Chat/Responses 空流换号、显式保活/SSE 错误、
持久化 hold 不叠加及异步结算生命周期均有聚焦回归。完整 Go、frontend
lint/typecheck/unit/build、数据库集成、Playwright 与 preflight 的最终结果以 PR
当前提交的验证记录及 CI 为准，而非把本报告的证据外推到后续提交。

本轮已运行通过：Go 全量 unit、聚焦 race、真实 PostgreSQL/Redis repository
integration，前端 lint/typecheck/build 及 457 suites / 3581 tests；真实浏览器首页
9/9、public pages 10/10、设置与用户状态回归 6/6。首页测试校验真实媒体与移动端
布局；其他 UI 使用本地 API fixture，不冒充生产 backend 或上游端到端验收。
上述本地结果不替代最终提交的 GitHub CI；定价决策门禁已由业务确认解除，部署前仍须
刷新线上快照、有效配置及目标宿主机媒体资产。

正式发布前须刷新暴露快照和有效配置；这是一份有时间边界的上线前评估，不是
持续监控，也不是 main 合并、发版、切流或修改线上状态的授权。
