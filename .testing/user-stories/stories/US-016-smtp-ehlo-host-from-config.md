# US-016-smtp-ehlo-host-from-config

- ID: US-016
- Title: Explicit EHLO host derived from From/Username so Google Workspace SMTP relay accepts AUTH
- Version: V1.3.x (hot-fix)
- Priority: P0 (prod outage: Google Workspace SMTP not usable)
- As a / I want / So that:
  作为 **TokenKey 运维者**，我希望 **后台 SMTP 配置使用 Google Workspace SMTP relay (`smtp-relay.gmail.com:465` + App Password) 时测试发信不再报 `smtp auth: EOF`**，**以便** 我们能在 AWS SES 不批准提额的情况下，立刻切到企业邮箱（`admin@orbitlogic.dev`）发系统邮件，不必逐项重试和怀疑密码 / 网络。

- Trace:
  - 防御需求轴线：Go stdlib `net/smtp.Client` 默认 `EHLO localhost`，Google Workspace SMTP relay 把这视作 open-relay 探测并在 AUTH 阶段静默断开 TCP（实测：`openssl` 手工 EHLO 一个真实域名 → `235 Accepted`；Go 程序 EHLO localhost → `EOF`）。这是发信链路上的反滥用边界。
  - 实体生命周期轴线：SMTP 会话 = `dial → EHLO → (STARTTLS) → AUTH → MAIL → RCPT → DATA → QUIT`。本故事补齐 EHLO 这条迁移边的"不能用 localhost"约束。
  - 系统事件轴线：每次「后台 SMTP 测试连接」按钮 + 每次系统发信（验证码、密码重置、欠费提醒）。

- Risk Focus:
  - 逻辑错误：EHLO host 推导优先级（From → Username → Host）必须按业务实际填写顺序，不能在 `From` 已填的情况下回落到 `Host`；`localhost` 必须被 fallback 拒绝；malformed 邮箱（`@example.com`、`user@`、`not-an-email`）的等价类必须有定义。
  - 行为回归：`SendEmailWithConfig` 的对外签名保持不变（handler 层 0 改动）；`TestSMTPConnectionWithConfig` 的对外签名保持不变（admin 设置页 0 改动）；STARTTLS 的"先 Hello → 再 Extension/StartTLS → 再 Auth"顺序与 stdlib 兼容（错误的顺序会导致 `Extension` 触发隐式 EHLO localhost，等于没修）。
  - 安全问题：EHLO 推导出来的域名会被发到对端 SMTP 服务器，必须是我们已经在 `From` 字段公开承认的域名（不会泄漏其他内部信息）；对配置注入（CR/LF）已有 `sanitizeEmailHeader` 兜底，本故事不重做。
  - 不适用：运行时问题——SMTP 会话短连接，无并发/重启/超时新增。

## Acceptance Criteria

1. **AC-001 (正向 / EHLO 推导)**：Given `SMTPConfig{From: "noreply@orbitlogic.dev", Username: "admin@orbitlogic.dev", Host: "smtp-relay.gmail.com"}`，When 调用 `ehloHostFromConfig(cfg)`，Then 返回 `"orbitlogic.dev"`。
2. **AC-002 (正向 / Username fallback)**：Given `From=""` 但 `Username="admin@orbitlogic.dev"`，When 调用 `ehloHostFromConfig`，Then 返回 `"orbitlogic.dev"`。
3. **AC-003 (正向 / Host fallback)**：Given `From=""` 且 `Username=""` 且 `Host="smtp-relay.gmail.com"`，When 调用 `ehloHostFromConfig`，Then 返回 `"smtp-relay.gmail.com"`（last-resort 但非 localhost）。
4. **AC-004 (负向 / localhost 拒绝)**：Given `From=""`、`Username=""`、`Host="localhost"`，When 调用 `ehloHostFromConfig`，Then **不得**返回 `"localhost"`，必须返回 `"tokenkey.invalid"` 标记值，让远端 SMTP 拒收成可见错误而不是退化为原 bug。
5. **AC-005 (回归 / Picky-mock 接受)**：Given 一个本地 mock TCP 服务器，行为与 Google Workspace SMTP relay 一致——收到 `EHLO localhost` 直接 close TCP，收到 `EHLO <真实域名>` 完成 250 + AUTH PLAIN + MAIL/RCPT/DATA/QUIT 全流程；When 调用 `SendEmailWithConfig` 用 `From=noreply@orbitlogic.dev`，Then 返回 `nil` 且 mock 服务器记录的 `LastEHLOHost == "orbitlogic.dev"`。
6. **AC-006 (负向 / Pre-fix mock 行为复现)**：Given 同一个 mock 服务器，When 测试代码绕过 fix 直接发 `EHLO localhost\r\n`，Then mock 必须 close TCP（subsequent read 返回错误），证明 mock 真实复现了 Google 的反滥用行为——这条 AC 是「mock 不退化」的元保护。
7. **AC-007 (回归 / 接口签名)**：Given 此 PR 落地，When 阅读 `email_service.go` 的对外导出函数 `SendEmailWithConfig` / `TestSMTPConnectionWithConfig`，Then 签名 0 变更（仅内部 `sendMailTLS` / `sendMailPlain` 增加 `ehloHost` 参数，且仅在同包内被 `SendEmailWithConfig` 调用）。
8. **AC-008 (回归 / 全量单元测试)**：Given 此 PR 落地，When 执行 `go test -tags=unit -count=1 ./internal/service/...`，Then 全部包通过，无新增 FAIL。

## Assertions

- `ehloHostFromConfig(&SMTPConfig{From:"noreply@orbitlogic.dev", Host:"smtp-relay.gmail.com"})` → `"orbitlogic.dev"`（断言 From 优先级）。
- `ehloHostFromConfig(&SMTPConfig{Host:"localhost"})` → `"tokenkey.invalid"`（断言 fallback 拒绝 localhost，且**不**返回空串——否则 `client.Hello("")` 会被 stdlib 拒绝并把 bug 埋到 `smtp hello:` 错误里）。
- `sendMailPlain` / `sendMailTLS` / `TestSMTPConnectionWithConfig`（TLS 分支与 STARTTLS 分支）四处代码路径**都**在 `client.Auth` 之前调用了 `client.Hello(ehloHost)`（不是只修一处——遗漏任何一条都会让"测试连接"按钮和"实际发信"行为不一致）。
- mock 服务器收到 `EHLO orbitlogic.dev` 后回复 `250-AUTH PLAIN LOGIN`，AUTH PLAIN 后回复 `235 Accepted`；收到 `EHLO localhost` 直接 close TCP（不发 5xx）——断言 mock 与 prod 实测行为同构。
- `TestSendEmail_RejectsEHLOLocalhost.LastEHLOHost == "orbitlogic.dev"`（断言副作用：fix 后实际发出的 EHLO host 就是从 From 推导出来的那个，不是 stdlib 默认的 localhost，也不是 SMTP host `127.0.0.1`）。

## Linked Tests

- `backend/internal/service/email_service_ehlo_test.go`::`TestEHLOHostFromConfig` (8 个子用例覆盖 AC-001..AC-004 + 边界等价类)
- `backend/internal/service/email_service_ehlo_test.go`::`TestSendEmail_RejectsEHLOLocalhost` (AC-005)
- `backend/internal/service/email_service_ehlo_test.go`::`TestSendEmail_PreFixBehaviorReproduces` (AC-006)

运行命令：

```bash
# 仅本故事的回归 (秒级)
go test -tags=unit -count=1 -v -run 'TestEHLOHostFromConfig|TestSendEmail_RejectsEHLOLocalhost|TestSendEmail_PreFixBehaviorReproduces' \
  ./backend/internal/service/...

# 全 service 包回归 (~80s)
go test -tags=unit -count=1 ./backend/internal/service/...
```

## Evidence

- Prod 复现：从 EC2 (`34.194.234.88`) 到 `smtp-relay.gmail.com:465`，`openssl s_client` 手工 `EHLO orbitlogic.dev` + AUTH PLAIN（真实 App Password）→ `235 2.7.0 Accepted`。
- Go 复现：在同一台 EC2，`go run smtp_repro.go`（mirror `email_service.sendMailTLS`）→ `smtp auth: EOF`；改成 `client.Hello("orbitlogic.dev")` 后 → `AUTH OK`。
- 本地 (worktree) 自检：
  - `go build ./...` clean。
  - `go test -tags=unit -count=1 -v -run 'TestEHLOHostFromConfig|TestSendEmail_RejectsEHLOLocalhost|TestSendEmail_PreFixBehaviorReproduces' ./internal/service/...` → 3 个测试 + 8 个子用例全 PASS。
  - `go test -tags=unit -count=1 ./internal/service/...` → 包通过（`ok ... 80.666s`），无新增 FAIL。

## Status

- [ ] InTest（PR 等 review；merge 后转 Done，并随下一次 prod 部署在 admin SMTP 设置页用 `admin@orbitlogic.dev` + App Password 实测「测试连接」+ 收到测试信收尾验证）。
