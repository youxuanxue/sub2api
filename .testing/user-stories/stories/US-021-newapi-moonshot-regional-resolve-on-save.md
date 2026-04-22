# US-021-newapi-moonshot-regional-resolve-on-save

- ID: US-021
- Title: newapi 账号保存时自动解析 Moonshot 区域 base URL（.cn vs .ai）
- Version: V1.4
- Priority: P1
- As a / I want / So that: 作为 sub2api 管理员，我希望在创建/编辑一个 Moonshot channel-type 的 newapi 账号时，后端在保存路径自动用账号 API key 探测正确的区域端点（`api.moonshot.cn` vs `api.moonshot.ai`），以便不再需要管理员手工分辨账号是国内还是海外组织——避免后续 relay 在错误区域反复 401。
- Trace: 实体生命周期（账号 create/update → 持久化）+ 防御需求（区域错配防御）。Bug B 复现：`ResolveMoonshotRegionalBaseAtSave` 已存在但 admin_service 的账号保存路径未接线，导致功能从未触发。修复策略：在 admin_service `CreateAccount` / `UpdateAccount` 的 newapi 分支调用 `MaybeResolveMoonshotBaseURLForNewAPI`。
- Risk Focus:
  - 逻辑错误：解析逻辑只能在 channel_type 命中 Moonshot 且未配置反向代理时触发；其他 channel_type 必须原样保存。
  - 行为回归：API key 为空、平台不是 newapi、用户已自定义 reverse proxy 时，必须 short-circuit 不发起探测。
  - 运行时问题：探测请求失败必须把错误向上传播并阻止保存——不能"静默回退"导致区域错配持续存在。
  - 安全问题：不适用（仅出站 GET，无新鉴权路径）。

## Acceptance Criteria

1. AC-001 (正向)：Given platform=`newapi`、channel_type=Moonshot、API key 非空、未自定义 reverse proxy，When `MaybeResolveMoonshotBaseURLForNewAPI` 被调用，Then 探测器被命中且账号 base_url 被解析为 stub 返回的区域端点（如 `https://api.moonshot.cn`）。
2. AC-002 (负向 / channel_type 不匹配)：Given channel_type 不是 Moonshot，Then 探测器**不**被调用，base_url 原样保留。
3. AC-003 (负向 / 自定义反向代理)：Given 用户已设置自定义 reverse proxy（base_url 不在标准 Moonshot 域），Then 探测器**不**被调用——尊重运维显式配置。
4. AC-004 (负向 / API key 缺失)：Given API key 为空字符串，Then 探测器**不**被调用——避免无凭证探测。
5. AC-005 (负向 / 平台不匹配)：Given platform 不是 newapi（即使 channel_type 是 Moonshot 数字也不行），Then 探测器**不**被调用——helper 是 newapi-only 的。
6. AC-006 (运行时 / 探测失败传播)：Given 探测器返回错误，Then helper 把错误向上传播且**不**修改 base_url——阻止"区域错配静默落库"。

## Assertions

- AC-001：`require.True(probeCalled)` 且 `require.Equal("https://api.moonshot.cn", account.base_url)`。
- AC-002 ~ AC-005：`require.False(probeCalled)` 且 `account.base_url` 原值未变。
- AC-006：`require.Error(err)` 且 base_url 未被修改（探测失败前的原值）。
- 失败时 testify `require` 立即报错并 exit ≠ 0。

## Linked Tests

- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_ResolvesWhenChannelTypeMatches` *(AC-001)*
- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonMoonshotChannelType` *(AC-002)*
- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForCustomReverseProxy` *(AC-003)*
- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsWhenAPIKeyEmpty` *(AC-004)*
- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonNewapiPlatform` *(AC-005)*
- `backend/internal/integration/newapi/moonshot_resolve_save_helper_test.go`::`TestMaybeResolveMoonshotBaseURLForNewAPI_PropagatesProbeFailure` *(AC-006)*
- 运行命令：`cd backend && go test -tags=unit -v -run 'TestMaybeResolveMoonshotBaseURLForNewAPI_' ./internal/integration/newapi/`

## Evidence

- `.testing/user-stories/attachments/us-021-moonshot-resolve-save-go-test-2026-04-22.txt`（待补）

## Status

- [x] InTest
