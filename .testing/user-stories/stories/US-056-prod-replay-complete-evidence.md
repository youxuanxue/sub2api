# US-056 完整真实证据驱动 prod replay

- ID: US-056
- Title: 完整真实证据驱动 prod replay
- Priority: P0
- As a / I want / So that: 作为发布审核者，我希望每个已观察用法都有可验证的结果，缺失正文不能靠窗口滚动或降低成功标准变绿。
- Trace: `docs/approved/prod-replay-complete-evidence.md`

- Risk Focus:
  - 安全问题：原始正文的安全保留、密钥隔离与资源上限。
  - 行为回归：分母漂移、凭证误判、外联与错误回放 receipt 放行。

## Acceptance Criteria

1. 原始完整请求只进入短期加密证据；普通 QA 保持上限，认证头不被持久化。错误/不完整响应不准入。
2. 采集窗口移动不能移除旧缺口；样本齐备前不启动付费请求，齐备后固定请求指纹，篡改/到期/丢失必须失败。
3. 失效 key 测试明确拒绝；负例需要实际错误码且必须有相应业务能力的正向证据，不能恢复 key。
4. 负例无外部网络与宿主机端口，零 usage/余额变化；正常回放使用隔离数据层。
5. 任意覆盖/执行/清理失败或候选/路由漂移保持 red；green 也无切流授权。

## Assertions

断言原始字节与密文不泄漏、解密认证失败、权限/目录边界、首批样本保留、真实 HTTP
请求方法/错误码、固定清单指纹、没有调用 sandbox.start 和没有公网端口。

## Linked Tests

- `backend/internal/observability/qa/replaycapture/capsule_test.go`::`TestCapsuleAuthenticatesOriginalBytesAndExpiry`
- `backend/internal/observability/qa/replaycapture/capsule_test.go`::`TestStoreRetainsFirstSamplesAndConfinesEncryptedFiles`
- `backend/internal/observability/qa/replaycapture/capsule_test.go`::`TestStoreBudgetsAndSlots`
- `backend/internal/observability/qa/replaycapture/capsule_test.go`::`TestResponseObserverRejectsLateErrorsAndIncompleteFrames`
- `backend/internal/observability/qa/replay_capture_test.go`::`TestReplayCaptureKeepsCompleteBodyOutsideOrdinaryQA`
- `backend/internal/observability/qa/replay_capture_test.go`::`TestReplayIngressPreservesGETGeminiAndDropsCredentials`
- `backend/internal/observability/qa/replay_capture_test.go`::`TestReplayAudioMultipartStaysEncryptedAndKeepsModel`
- `backend/cmd/replay-capsule/main_test.go`::`TestKeygenAndOfflineDecrypt`
- `backend/cmd/replay-capsule/auth_test.go`::`TestAuthCheckKeepsCredentialsOffOutputAndRefusesRedirect`
- `ops/stage0/test_prod_replay_corpus.py`::`test_observed_gaps_do_not_disappear_when_window_moves`
- `ops/stage0/test_prod_replay_corpus.py`::`test_frozen_corpus_ignores_new_traffic_and_fails_tamper_missing_expiry`
- `ops/stage0/test_prod_replay_corpus.py`::`test_revoked_auth_requires_real_positive_business_coverage`
- `ops/stage0/test_prod_replay_corpus.py`::`test_gaps_do_not_freeze_or_start_paid_execution`
- `ops/stage0/test_prod_replay_corpus.py`::`test_negative_auth_requires_exact_code_and_original_state`
- `ops/stage0/test_prod_replay.py`::`test_gaps_failures_drift_and_cleanup_never_green`

- `ops/stage0/test_prod_replay_isolation.py`::`test_shared_none_namespace_has_no_host_port_or_external_route`

- Run command: `python3 -m unittest discover -s ops/stage0 -p 'test_prod_replay*.py'`；`cd backend && go test -tags=unit ./internal/observability/qa/... ./cmd/replay-capsule`

## Status

- InTest
