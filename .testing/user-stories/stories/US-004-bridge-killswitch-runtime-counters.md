# US-004-bridge-killswitch-runtime-counters

- ID: US-004
- Title: Bridge emergency kill switch and runtime counters
- Version: V1.0
- Priority: P0
- As a / I want / So that: 作为平台运维，我希望能在 settings 里一键关闭 NewAPI bridge 路径，并持续采集 bridge/affinity/支付失败计数，以便线上故障时可快速回退与诊断。
- Trace: [防御需求]
- Risk Focus:
- 逻辑错误：bridge 分流在 kill switch 关闭后仍继续命中。
- 行为回归：已有 channel_type 分流条件被破坏，正常路径误判。
- 安全问题：不适用：本次不涉及鉴权边界变更，仅为运行时开关与指标计数。
- 运行时问题：故障时缺少可观测计数，导致恢复窗口延长。

## Acceptance Criteria

1. AC-001 (正向): Given `newapi_bridge_enabled=true`，When 账号 `channel_type>0` 且命中 bridge 支持端点，Then `ShouldDispatchToNewAPIBridge` 返回 true。
2. AC-002 (负向): Given `newapi_bridge_enabled=false/off`，When 账号 `channel_type>0` 且命中 bridge 支持端点，Then `ShouldDispatchToNewAPIBridge` 返回 false。
3. AC-003 (回归): Given 现有 service 层 bridge 分流测试，When 执行 `go test ./internal/service -run "Bridge|Dispatch|Affinity"`，Then 全部通过并包含 kill switch 覆盖。

## Assertions

- `GatewayService` 与 `OpenAIGatewayService` 的 bridge 判定都受 `SettingKeyNewAPIBridgeEnabled` 控制。
- bridge 分流总量计数器可通过 `BridgeDispatchStats()` 读取。
- affinity 命中计数可通过 `AffinityHitStats()` 读取。
- payment 失败计数器函数可调用且可读。

## Linked Tests

- `backend/internal/service/gateway_bridge_dispatch_test.go`::`TestShouldDispatchToNewAPIBridge`
- `backend/internal/service/gateway_bridge_dispatch_test.go`::`TestShouldDispatchToNewAPIBridge_RespectsKillSwitch`
- `backend/internal/service/openai_gateway_bridge_dispatch_test.go`::`TestOpenAIShouldDispatchToNewAPIBridge`
- `backend/internal/service/openai_gateway_bridge_dispatch_test.go`::`TestOpenAIShouldDispatchToNewAPIBridge_RespectsKillSwitch`
- 运行命令: `cd backend && go test ./internal/service -run "Bridge|Dispatch|Affinity"`

## Evidence

- （无附件归档；以 Linked Tests 命令输出为准）

## Status

- Done