---
title: QA 正文保护与入口安全边界修复
status: approved
approved_by: "user (2026-09-11 conversation: 逐个处理。提交并推送 pr。; 保留现有采集并加强保护)"
approved_at: 2026-09-11
created: 2026-09-11
authors: [codex]
risk: high
---

用户已授权按审计发现修复并提交 PR，并明确选择保留正文采集、加强保护。
此锚点覆盖 R-001/002/003/004/005/009 的可回滚代码修复，不代表批准生产部署、
删除已有证据、修改管理员 MFA 绑定或执行凭据迁移。

现有 QA 生命周期继续遵循 `design-prod-qa-24h-s3-lifecycle.md`。
不新增 schema、第二归档/删除 owner 或新的采集开关。

批准的修复行为：

- 请求存储身份由服务端生成，响应 X-Request-ID 返回该身份，客户端不能通过复用或构造该头
  选择 QA 对象。不新增第二套「权威 request id」头名。客户端自选关联走已有副通道：
  入站 X-Client-Request-ID → context/logs/ops 的 client_request_id（及计费侧 ClientRequestID）；
  若未传该头而只传了遗留 X-Request-ID，则把该值降级为 client_request_id 标记，便于响应丢失时
  仍可按客户端持有的值检索，但它永远不是存储主键，也不能替代响应 X-Request-ID 做权威定位。
- QA blob 和 DLQ 写入由 `trajectory.WriteBlobFile` 统一执行目录边界、私有权限及禁止覆盖。
  S3 blob 写入也要求目标不存在；已有记录不迁移。读取与删除同样不能越过根目录。
- 响应采集只保留预算内的字节，流片段引用这份有界内容，不再持有无上限的待解析缓冲。
  流片段元数据数量也有上限，转发不因采集截断而截断。直接提交的流片段序列化同样有界。
- logredact 统一处理结构化敏感字段与正文内可识别的秘密；普通文字和结构保持不变。
  该功能不是完整 DLP 承诺。保留已批准的 thinking signature / encrypted reasoning 兼容行为。
- ACL、限流及会话绑定只信任已配置代理链；现有兼容开关仅作用于日志归因。
  Caddy 删除未经验证的 CF-Connecting-IP。已有依赖伪造地址的会话可能需要重新登录。
- 异步图片存储默认下载器在连接时固定到经过校验的公共 IP，重定向复用同一限制，
  不通过环境代理绕过边界；下载内容必须具有支持的图片格式签名。
- 隐私声明准确披露成功请求采集、自动脱敏的限制、QA 归档与导出生命周期。

回归覆盖：目录穿越、符号链接、重复写入、写入失败、重复客户端请求 ID、
入站 X-Client-Request-ID 被接受为 client_request_id、仅传遗留 X-Request-ID 时降级为
client_request_id、入站 X-Request-ID 不决定存储身份、长流/碎片/缺分隔符、正常转发、
正文凭据、普通 JSON 字符串、伪造 IP、公共 DNS 固定连接、混合 DNS 答案、私网重定向及
伪造图片 Content-Type。
相关 owner、调用点及负向测试登记 gateway sentinel，防止上游同步静默移除安全行为。

剩余审计事项逐项处理，不能把本 PR 当作全部安全审计结案：管理员 MFA 需要真实持有者绑定；
凭据/JWT 隔离涉及密钥管理和迁移；上游隐私资格须区分产品与合同证据；浏览器会话方案需
连同 CSRF、刷新和多端登录验证。这些不通过默认值调整伪装为已经生产修复。
