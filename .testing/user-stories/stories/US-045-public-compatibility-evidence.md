# US-045-public-compatibility-evidence

- ID: US-045
- Title: 公开客户端 × 协议 × 传输 × 模型兼容证据
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **准备接入 TokenKey 的开发者**，我希望 **按客户端、协议、传输和模型查看实测/兼容/限制与最近验证时间**，**以便** 在投入配置时间前判断自己的组合，并理解证据强弱与替代路径。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §9。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G5。
  - 私有输入：`docs/ops/endpoint-compat-baseline.md` 与授权 probe artifacts；它们本身不直接公开。
  - 模型候选 SSOT：US-044 public catalog 最终可售 projection。
- Risk Focus:
  - 逻辑错误：把 route-open、SKIP、unknown、过期证据或 protocol contract test 升格为 live verified。
  - 行为回归：runtime/client 版本升级后旧绿色 verdict 继续展示。
  - 安全问题：public artifact 泄漏 API key、prompt/body、account/group/pool、Edge host、raw log path 或 probe 资源名。
  - 运行时：为维持绿色矩阵自动运行全量/付费生产 probe，增加封禁、成本或用户流量风险。

## Acceptance Criteria

1. **AC-001（确定性安全 artifact）**：Given已授权测试/probe 结果与当前 catalog projection，When publisher 构建 snapshot，Then输出 canonical JSON 与 detached SHA-256，只含 client/version/protocol/transport/model/verdict/limitation/time/runtime/evidence kind，build gate 重算 digest 且 secret/internal-topology gate 为零。
2. **AC-002（verdict 不升级）**：Given exact live success、contract-only support、带限制成功或无证据，When发布，Then分别只能得到 `verified`、`compatible`、`limited`、`unknown`；route-open、SKIP 和 excluded row 不能成为 verified。
3. **AC-003（freshness/runtime/cache）**：Given verified/compatible/limited row 超过其 evidence-kind freshness policy 或 runtime/client anchor 不匹配，When public API 读取，Then verdict 变为 unknown，并保留诚实的 last verified time；compatible 不能从过期 live evidence 推导，final representation `ETag` 随降级变化，`Cache-Control` 不跨过下一 freshness transition。
4. **AC-004（paid scope）**：Given non-paid probe 成功，When处理 image/video row，Then不能证明 paid media verified；paid verdict 必须来自显式批准的对应 evidence kind。
5. **AC-005（catalog membership）**：Given snapshot model 不在 US-044 有效的当前可售 projection，When生成 final public representation，Then该 row 被排除且不成为 artifact-wide 503；Given当前 catalog projection unavailable/degraded，Then API fail closed，未知/历史信息不得抬高当前可售覆盖。
6. **AC-006（公开限制码）**：Given limited row，When用户展开，Then只看到批准的用户影响码、解释与 workaround，不看到 pool、entitlement、routing、account 或 Edge 原因。
7. **AC-007（API fail closed）**：Given snapshot detached digest/schema/snapshot-level runtime contract validation 失败或 current catalog unavailable/degraded，When请求 `/api/v1/public/compatibility`，Then返回 503 `compatibility_evidence_unavailable`，不把上一份 artifact 冒充当前；成功响应的 `ETag` 来自 freshness/catalog 转换后的 final public representation。
8. **AC-008（矩阵 UX）**：Given有效 snapshot，When用户按 client/protocol/transport/model/verdict 筛选，Then每行显示 verdict 强度、client version 与 last verified time；无 row 显示“尚未验证”，不显示“不支持”。
9. **AC-009（probe 授权不扩张）**：Given artifact 过期或缺 row，When publisher 运行，Then只报告缺口，不自动触发生产或付费 probe。

## Assertions

- `docs/ops/endpoint-compat-baseline.md` 继续是私有运维记忆，不成为 public endpoint 的静态文件源。
- limitation code allowlist 只有用户影响和替代路径，不含内部故障分类。
- snapshot 可 review、可复现、可回滚；API 不从 `/tmp` 或原始日志动态读取。
- detached SHA-256 是 content identity，不是数字签名；publisher、build 和 runtime 使用同一 canonical-byte contract。
- G5 依赖 G4 的模型 candidate set，但可以独立发布/隐藏。

## Linked Tests

- `PublicCompatibilitySnapshotIsDeterministicAndSecretFree` *(planned in the P0-G5 implementation PR)*
- `PublicCompatibilitySkipUnknownAndStaleNeverBecomeVerified` *(planned in the P0-G5 implementation PR)*
- `backend/internal/handler/public_compatibility_handler_test.go`::`TestPublicCompatibilityFailsClosedOnInvalidArtifact` *(planned)*
- `backend/internal/handler/public_compatibility_handler_test.go`::`TestPublicCompatibilityETagChangesAtFreshnessBoundary` *(planned)*
- `frontend/src/views/__tests__/CompatibilityMatrixView.spec.ts`::`filters evidence and renders honest empty and stale states` *(planned)*

运行命令：

```bash
python3 -m unittest discover -s scripts/checks -p 'test_public_compatibility_snapshot.py'
cd backend && go test -tags=unit ./internal/handler -run PublicCompatibility -count=1
cd frontend && npm run test:unit -- CompatibilityMatrixView.spec.ts
```

## Evidence

- 实现 PR 附 snapshot diff、secret/internal-field scan、catalog/runtime/freshness gates 和 public matrix UI 证据；任何 live/paid probe 另走批准。

## Status

- Ready — 等待设计批准；publisher 默认只消费已有授权证据。
