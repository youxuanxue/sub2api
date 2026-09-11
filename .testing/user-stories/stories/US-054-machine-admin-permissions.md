# US-054 机器管理员最小权限与自动化兼容

- ID: US-054
- Title: 机器管理员按操作范围授权，同时保留自动化
- Priority: P1
- As a / I want / So that: As an administrator, I want independently revocable machine credentials with explicit permissions, so that automation can continue without unrestricted administrator access.
- Trace: docs/approved/machine-admin-permissions.md; user decision 保留自动化，先拆分机器权限; no production changes.
- Risk Focus:
  - 逻辑错误：复合数据包必须同时具备账号与代理权限。
  - 行为回归：旧全局 key、人类 JWT 和 step-up 原有行为不变。
  - 安全问题：越权、机器签发机器 key、路由前缀绕过、日志泄漏、账号降权。
  - 运行时：凭据过期、DB 故障 fail-closed、跨进程吊销、并发更新丢失、CLI 文件失败。

## Acceptance Criteria

1. AC-001 (positive): Given a valid machine key When calling an explicitly authorized route Then the operation succeeds and audits the independent credential ID.
2. AC-002 (negative): Given missing permissions, unknown routes, revoked/expired keys, disabled/demoted issuer or DB failure When authenticating Then no protected operation executes.
3. AC-003 (regression): Given human JWT or legacy administrator key When existing authentication/step-up runs Then old behavior remains; reviewed machine exports work with step-up enabled.
4. AC-004 (security): Given machine key management When a human creates/lists/revokes Then key plaintext is returned only on creation and issuer comes from the session; all machine identities are denied management.
5. AC-005 (runtime): Given concurrent creation/revocation or a fresh service instance When loading credentials Then no unrelated key is lost and revoked keys cannot authenticate.
6. AC-006 (negative): Given the operator CLI When issuing credentials Then output files are private and exclusive, stdout omits the secret, and remote HTTP/redirects are refused.

## Assertions

AC-001: HTTP 200, machine auth method, independent audit ID, route registry matches actual Gin templates.
AC-002: HTTP 401/403 or service error; omitted request body on denial; removal of each required scope rejects its registered routes.
AC-003: existing AdminAuth and EnforceStepUp suite plus explicit machine export positive/negative cases with the toggle on and off.
AC-004: API metadata omits digest/key; cannot select another issuer; key-management machine request returns 403.
AC-005: concurrent independent row count and absence of revoked ID; actual PostgreSQL round trip through separate service instances.
AC-006: filesystem mode 0600, preserved existing/symlink targets, secret-free output and no redirected Authorization.

## Linked Tests

- `backend/internal/service/setting_machine_admin_tk_test.go`::`TestUS054_MachineKeyLifecycle`
- `backend/internal/service/setting_machine_admin_tk_test.go`::`TestUS054_MachineKeyFailsClosed`
- `backend/internal/service/setting_machine_admin_tk_test.go`::`TestUS054_MachineKeysConcurrentIndependentRows`
- `backend/internal/service/setting_machine_admin_tk_test.go`::`TestUS054_MachinePermissionMatrix`
- `backend/internal/server/middleware/admin_auth_machine_tk_test.go`::`TestUS054_MachineAuthScopeAndAudit`
- `backend/internal/server/middleware/admin_auth_machine_tk_test.go`::`TestUS054_MachineStepUpIsExportOnly`
- `backend/internal/server/routes/admin_machine_tk_test.go`::`TestUS054_MachinePolicyUsesRegisteredRouteTemplates`
- `backend/internal/handler/admin/setting_machine_admin_tk_test.go`::`TestUS054_MachineKeyManagementAPI`
- `backend/internal/repository/machine_admin_tk_integration_test.go`::`TestUS054_MachineKeyPostgresPersistenceAndRevocation`
- `ops/admin/test_machine_keys.py`::`MachineKeyCLI.test_create_writes_private_key_without_stdout_disclosure`
- `ops/admin/test_machine_keys.py`::`MachineKeyCLI.test_existing_file_and_symlink_never_issue_or_overwrite`
- `ops/admin/test_machine_keys.py`::`MachineKeyCLI.test_rejects_remote_http`
- `ops/admin/test_machine_keys.py`::`MachineKeyCLI.test_redirect_does_not_receive_authorization`
- `frontend/e2e/us054-machine-admin-audit.e2e.ts`::`US054 administrator filters machine operations and inspects independent credential ID`

Run command: `cd backend && go test -tags=unit ./internal/service ./internal/server/middleware ./internal/server/routes ./internal/handler/admin -run 'Test(US054|EnforceStepUp|AdminAuth)' -count=1`

Run command: `cd backend && go test -tags=integration ./internal/repository -run TestUS054_MachineKeyPostgresPersistenceAndRevocation -count=1`

Run command: `python3 -m unittest discover -s ops/admin -p 'test_*.py' -v`

## Evidence

Local unit/HTTP integration and isolated PostgreSQL testcontainer evidence; no live API calls.
Playwright drives the real audit page with isolated API fixtures to verify the machine filter and credential ID detail; backend checks remain unit/integration evidence.

Run command: `E2E_BASE_URL=http://127.0.0.1:18554 E2E_USE_SYSTEM_CHROME=1 pnpm --dir frontend exec playwright test e2e/us054-machine-admin-audit.e2e.ts` (local Vite server only).

## Status

- InTest
