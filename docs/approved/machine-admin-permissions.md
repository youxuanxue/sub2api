---
title: Scoped machine administrator credentials
status: approved
approved_by: "feng (conversation: 保留自动化，先拆分机器权限; implementation and PR only)"
created: 2026-09-11
---

# 管理员机器权限拆分

用户要求逐项修复安全问题并提交、推送 PR，明确选择保留自动化、先拆分机器权限；
同时要求不影响线上 TokenKey 服务。本契约仅覆盖可回退的代码、测试和 PR。
上线、签发线上机器 key、修改线上运维凭据、开启 step-up、撤销旧 key 均未执行。
现有 QA 采集、上游凭据和 JWT 存储方式保持现状；本功能只对新机器 key 保存校验摘要。

## 权限与身份

`backend/internal/service/machine_admin_policy_tk.go` 是权限、说明和路由的单一 owner。
按 HTTP method 与 Gin FullPath 精确匹配，未登记路由默认拒绝；禁止 wildcard 和路径前缀。
同一路由登记多个权限时要求全部满足。账号数据包可读写代理，因此 `/accounts/data`
导入要求 `accounts:import` + `proxies:write`，导出要求 `accounts:export` + `proxies:export`，
即使请求暂时不含代理也按完整接口能力授权。账号导入可绑定已有组/代理，不可创建管理用户。
`accounts:write` 包含凭据/上游地址/代理/调度修改，具有改变流量及凭据外送的能力，不能视作低风险。
账号列表和详情沿用现有 DTO 脱敏；备注、扩展配置仍敏感。代理读取可含密码，因此独立授权。
本轮开放账号运维及所需分组/代理接口；OAuth 交互、计费、全局设置、备份恢复、插件、
用户提权、审计清空等没有机器权限，不可因新增管理路由自动获得权限。
权限目录由列表 API 动态提供，脚本和测试从 owner 消费，不另维护一份静态路由表。

`admin_auth_machine_tk.go` 在共用管理员鉴权边界验证机器 key 并执行权限检查，
覆盖独立注册的 payment 管理组。机器身份绑定签发时的真实管理员，不使用 GetFirstAdmin。
每次请求读数据库验证摘要/有效期，并检查签发者仍是启用的管理员；数据库失败时拒绝。
停用/删除/降级签发者会阻止其机器 key；密码更改本身不轮换这些独立凭据。
敏感操作只有明确登记的账号/代理导出可以使用机器授权替代人类 MFA。
人类 JWT 和旧全局管理员 key 的现有 step-up 行为保留。
机器请求（含只读请求）与权限拒绝进入审计，以 `machine_admin_key` 和 `machine_key_id`
区别于人类会话；拒绝请求不采集正文，记录中不含密钥原文。

## 凭据生命周期与 API

新 key 由随机公开 ID 和随机 secret 组成，以 `sk-tkm-` 区分旧 key。
新 key 每条对应一个不可变 `settings.tk_machine_admin_key_<id>` 记录：名称、签发者 ID、
权限、创建时间、过期时间、SHA-256 校验摘要。公开 ID 为 128 bit、secret 为 256 bit。
TTL 必填，范围 1–2160 小时；服务端不允许无限期机器 key。不新增 schema 或启动迁移。
元数据列表不返回摘要或原文；只在创建成功时返回一次 key，响应 `Cache-Control: no-store`。
吊销删除对应记录，不修改其他 key，无进程级认证缓存；已开始执行的请求可完成。
独立行避免并发签发/吊销共享 JSON 数组导致丢失更新或复活 key。

`/api/v1/admin/settings/machine-admin-keys`：

- GET：返回 `{keys, permissions}`，权限目录来自上述 owner。
- POST：请求 `{name, scopes, ttl_hours}`，返回 `{credential, key}`；签发者从会话读取。
- DELETE `/:id`：单独吊销，不影响旧 key 或其他机器 key，可幂等重试。

所有管理接口只允许人类管理员 JWT。创建与吊销额外使用已有 step-up 中间件；
开启 step-up 时必须先完成 TOTP 二次验证。开关关闭时维持人类会话的兼容行为，
不声称所有签发动作已经由 MFA 保护。机器 key（包括旧全局 key）不能签发新机器 key。

## 运维入口与兼容迁移

运维工具：`ops/admin/machine-keys.py`。通过安全环境注入 `TOKENKEY_ADMIN_JWT`
（人类管理员会话）；命令行参数不接受 JWT。必须显式指定目标 origin，HTTPS 强制，
仅本机测试允许 HTTP；不跟随跳转。签发的密钥只写新建的 0600 文件，拒绝覆盖或 symlink，
stdout 只显示元数据。签发/文件写入失败要先 list 核对，避免重复创建；文件可能为空，
已返回的 credential ID 可用于吊销。服务端已成功但客户端未收到响应时也按此规则处理。

```sh
python3 ops/admin/machine-keys.py --base-url https://ADMIN_HOST list
python3 ops/admin/machine-keys.py --base-url https://ADMIN_HOST create \
  --name inventory --scope accounts:read --scope groups:read \
  --ttl-hours 720 --key-file /secure/path/inventory.key
python3 ops/admin/machine-keys.py --base-url https://ADMIN_HOST revoke --id CREDENTIAL_ID
```

现有脚本继续通过 `x-api-key` 携带凭据，新旧 header 完全相同。
例如现有 `.cursor/skills/sub2api-admin/scripts/sub2api-admin.js` 无需改变请求方式；
用其已有密钥环境注入入口替换为对应 scoped key 即可。不要将私密 key 文件或 JWT 入库。

待另行授权上线时：先保留现有全局 key，按任务申请最小权限的新 key；在隔离环境
验证导入/导出和失败拒绝，再逐个替换任务凭据、观察独立 key 审计，最后安排旧 key
停用及人类 MFA 的切换。新权限缺少某个接口时返回 403，不自动回退旧 key。
本轮不改线上脚本配置、不自动签发 key、不打开 step-up、不停用旧 key。
因此旧全局 key 仍然有过大权限，R-006 在完成实际迁移与人类 MFA 后才能关闭。

## 验证

US-054 记录权限矩阵、HTTP 鉴权/导出、签发/吊销、数据库故障和并发测试。
PostgreSQL 集成测试验证跨 service 实例读取及吊销；CLI 测试验证私密输出和传输边界。
签发/吊销通过 API/CLI；现有审计页新增 Machine Admin Key 筛选项。
Playwright 通过真实审计页面验证筛选请求和独立 key ID 详情，API 使用隔离 fixture；
其余鉴权/存储验证属于单元/集成测试，不冒充 UI e2e。
必须完成聚焦测试、make test、preflight 和 PR CI；不使用线上服务进行写入验证。
