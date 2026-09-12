---
title: Prod replay 完整证据与固定验收集
status: approved
approved_by: "user (2026-09-12 conversation: 同意。请按 落地顺序 推进。)"
approved_at: 2026-09-12
created: 2026-09-12
authors: [codex]
risk: high
---

授权覆盖采集、执行器与验收规则实现、隔离验证和 PR 提交。生产采集补丁部署需另行审批；
禁止 promote、Caddy reload、切流、Edge rollout、合并 PR 或恢复失效 key。
此修订承接 `deploy-stage0-workflow.md` §10；下述失效凭证验收与固定样本策略取代该节
“全部组合只能做原 key 正向生成”的旧规则。历史 red receipt 不重算、不改绿。

## 问题与落地顺序

上一轮存在真实采集缺口：普通 QA 的截断、脱敏、归一化 Gemini 路径和无正文 GET
不适合作为完整回放证据。已丢失的历史字节不能恢复，必须从新的真实请求补采。
失效凭证应验证拒绝，不能恢复凭证以完成生成。负例通过也不证明业务能力通过。

先完成实现、隔离测试与 PR；再单独审批生产采集补丁部署；随后补采、固定样本并重复
replay。完整证据不足时保持 red，达到 green 仍停在 `approval_pending=true`。

## 采集边界

Owner：`backend/internal/observability/qa/replaycapture/`、`qa/replay_capture.go`。
开关 `QA_CAPTURE_REPLAY_ENABLED` 默认 false；开启必须同时启用 QA 并提供
`QA_CAPTURE_REPLAY_PUBLIC_KEY_FILE`。网关只持有公钥，私钥留在宿主机
`/var/lib/tokenkey/replay-keys/private.pem`，不挂进网关。

只复制 handler 实际消费的完整 JSON、空正文 GET 或已观测音频转写的 multipart（同一正文上限），
音频 multipart 保留原边界与文件字节，提取 model 和有文件的多模态标记；其二进制不进入普通 QA。
其他未支持格式仍报告 gap。保留压缩解码后的原始 JSON 字节，
不重写 prompt。记录服务端请求 ID、用户/key ID、分层字段、原始 method/business path
以及 allowlist 协议头。URL 中的 key/access_token 被剔除，未知 query 不猜测。
Authorization、x-api-key、x-goog-api-key 不存入证据；执行时从隔离快照读取对应原 key。
正文中的业务字段不脱敏，因而这些加密证据不进入普通 QA blob、S3 归档或用户导出。

使用 Go 标准库 RSA-OAEP-SHA256 包装随机 AES-256-GCM 密钥；版本、公钥指纹和时间
绑定认证数据。资源上限由 `replaycapture/capsule.go` 与 `store.go` 的常量/队列拥有：
单请求、目录容量、文件数、每组合样本数、并发和队列均有界。目录文件锁在共享 DATA_DIR
的蓝绿进程之间串行化配额检查、过期清理和原子发布。保留每组合最早完整样本，
到期才补位；定时清理独立于流量，停采后在该版本运行期间仍清理。解密在保留期限届满
时立即拒绝，物理删除由下一次定时扫描完成；应用停止或回退到旧版会停止该扫描，
停采维护方案必须保留清理 owner 至密文清空。加密与 fsync 在有界后台队列，写入失败或队列满形成
可观察的采集缺口，业务请求不等待磁盘。原 QA 上限与脱敏路径继续生效。

响应观察器读取完整输出，检查流终止与末尾错误；写出失败、非成功状态、未完成 SSE、
超大帧与显式 error 都不进入正向证据。完整正文超过上限仍是 gap，不切片、不编造。

## 固定清单与验收

Owner：`ops/stage0/prod_replay.py`；入口 `scripts/stage0/replay-prod-release.py`。

- 第一次采集先写 `bluegreen-replay-observed.json` 固定观察到的组合。补采期间，旧用法
  不能因滑出时间窗口而消失。新请求可补同组合缺口；失效用法可引入其他合法用户的
  同业务能力证据，此支持组合显式加入清单，分母只能增加。
- 每组合仍按用户、模型、endpoint、stream、tool、multimodal 分层；从成功 QA 行的两端
  候选与保留的加密 request ID 选取完整原始请求。加密身份须匹配 QA 行；旧 QA 完整
  JSON 可兼容使用，但无原始方法/路径的 GET、缺失 action 的 Gemini 不猜测。
- 失效 key 不复活。使用原失效 key 做明确标注的 synthetic auth negative，检查符合
  实际状态的精确 HTTP 状态码与错误码。已硬删除、无法取回原凭证仍是 gap。
  过期或 quota_exhausted key 的合法 introspection GET 可做正向回放。
- 鉴权负例运行在无宿主机端口、无默认路由、无宿主机网关的 共享 `network=none` 命名空间：PG 持有 none 网络，Redis/app 使用
  `container:<PG ID>`，仅回环互通。通过容器内 helper 发 HTTP；校验所有容器网络配置，
  停止 app 后检查隔离库零 usage 与余额不变。异常时 fail closed，不退回可外联网络。
- 每个负例对应的模型/协议/stream/tool/multimodal 能力必须另有完整真实正向样本，
  使用该样本自己的合法原 key。鉴权成功拒绝不能填平正向能力缺口。
- 缺口未清零时不启动付费回放。齐备后写 `bluegreen-replay-corpus.json` 固定样本、
  来源、请求指纹及观察清单指纹；其中不存正文、协议头值或 key 值。
- 重试读取同一清单，不重新选请求。正文、协议头、来源、观察清单变化，证据丢失、
  到期或快照中凭证状态变化均 fail closed。过期需显式 `--reset-corpus <原清单 SHA>`
  开始新采集；该动作先撤销旧 receipt，不能把旧失败作为新测试通过。
- 验收依赖真实请求结果、覆盖完整性、正向样本多样性、候选/路由指纹、生产 usage
  审计和完整资源清理。receipt schema=2 并绑定 corpus 指纹；旧 receipt 不满足新门禁。

加密证据由候选 immutable image 的 `/app/replay-capsule` 离线解密：network=none、
只读挂载、丢弃 capabilities、禁止提权、限制内存/CPU、无容器日志；明文只经匿名管道
留在执行器内存。helper 和临时数据库/Redis/app 的清理失败都会阻止 green。

## 部署审批准备与回退

本 PR 未部署、未 provision 生产密钥、未重新消耗生产上游额度。生产执行前必须提交：
待部署镜像 digest、当前 active/prepared/Caddy 指纹、仅采集补丁的影响清单与具体维护步骤。
当前生产不具备完整采集能力，单独部署 inactive candidate 无法收集真实用户请求；因此
此阶段不能通过再次 replay 消除历史缺口，也不能把部署新主网关暗含在补采里。

审批后的配置差异只有启用 replay capture 与指定公钥文件。先用候选 helper 在宿主机
新建私钥目录，再只把公钥安装到 `/app/data/replay-public.pem`；核验目录权限、私钥
不在网关 mounts、普通 QA/export 不出现原始敏感字段，然后按单独批准的维护方案部署。
停采回退为关闭该配置并按同一维护方案恢复；不删除原 QA，不改 key/代理/路由。
超出资源上限、完整证据不足、真实失败均保留为阻塞项，禁止通过扩大权限或降低验收消除。

## 验证 owner

US-056：`.testing/user-stories/stories/US-056-prod-replay-complete-evidence.md`。
Go crypto/store/response、middleware 与 helper 的测试，Python 固定清单/HTTP/门禁测试，
真实 PostgreSQL SQL 执行，以及本地 Docker 共享 none 命名空间实验组成验证。
后台采集默认关闭，无 Web/API 返回契约修改；本次后端/API 测试不称为 UI e2e。
