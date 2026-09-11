# US-053 正文采集与入口安全边界

- ID: US-053
- Title: 正文采集与入口安全边界
- Priority: P0
- As a / I want / So that: 作为 TokenKey 用户，我希望 QA 历史继续可用，同时客户端输入不能覆盖其他请求的证据、绕过采集上限或伪造安全身份。
- Trace: `docs/approved/security-capture-and-ingress.md`

- Risk Focus:
  - 安全问题：不可信输入影响存储、凭据和安全身份。
  - 运行时：长流采集失控或影响正常转发。

## Acceptance Criteria

1. 重复客户端请求 ID 不能决定服务端身份；本地 blob/DLQ 禁止目录越界、符号链接逃逸和覆盖。入站 X-Client-Request-ID 仅作为 client_request_id 标记；若只传遗留 X-Request-ID，降级为同一标记以便响应丢失时仍可检索，但永不成为存储主键。
2. QA 长响应、未结束帧和密集小帧的采集有界，客户端收到的响应不被采集截断。
3. 结构化内容中的常见凭据会遮蔽，普通文本和字符串 JSON 不改变、不无限递归。
4. 兼容日志开关不覆盖 ACL、限流和会话绑定的可信代理链。
5. 默认图片下载拒绝内部地址及重定向，固定经过检查的 DNS 地址，并拒绝伪造图片类型的文本。
6. 对外披露保留现有采集并加强保护后的真实数据处理；保持原 QA 生命周期。

## Assertions

校验具体错误、原文件内容、文件权限、实际转发字节、保留字节与帧数、服务端身份、
可信代理的解析结果、DNS 连接地址和未发生的私网 HTTP 请求。

## Linked Tests

- `backend/internal/service/gateway_usage_billing_request_id_test.go`::`TestUsageBillingIgnoresReusedClientMarker`
- `backend/internal/server/middleware/request_access_logger_test.go`::`TestRequestLoggerKeepsServerIDWhenUpstreamOverwritesHeader`
- `backend/internal/observability/qa/internal_thinking_capture_test.go`::`TestBuildBlobPreservesInternalThinkingSignatureAndRedactsSecrets`
- `backend/internal/observability/qa/middleware_synth_test.go`::`TestQAMetadataSurvivesBodyCaptureLimit`
- `backend/internal/observability/trajectory/blob_files_test.go`::`TestWriteBlobFileConfinesPathsAndRefusesReplacement`
- `backend/internal/observability/trajectory/blob_files_test.go`::`TestWriteBlobFileCleansOnlyItsIncompleteWrite`
- `backend/internal/observability/qa/security_capture_test.go`::`TestQACaptureBoundsAllStreamBytesWithoutTruncatingForwarding`
- `backend/internal/observability/qa/security_capture_test.go`::`TestQACaptureBoundsIncompleteAndTinyFrames`
- `backend/internal/observability/qa/security_capture_test.go`::`TestQABlobStoreConfinesReadsAndDeletes`
- `backend/internal/util/logredact/content_test.go`::`TestRedactSecretsInContentAndToolResults`
- `backend/internal/util/logredact/content_test.go`::`TestRedactJSONStringPreservesOrdinaryContentAndTerminates`
- `backend/internal/server/middleware/request_access_logger_test.go`::`TestRequestLogger_DoesNotTrustIncomingRequestID`
- `backend/internal/server/middleware/client_request_id_test.go`::`TestClientRequestIDAcceptsInboundHeader`
- `backend/internal/server/middleware/client_request_id_test.go`::`TestClientRequestIDFallsBackToInboundRequestIDAsClientMarker`
- `backend/internal/server/middleware/client_request_id_test.go`::`TestClientRequestIDPrefersExplicitClientHeaderOverLegacyRequestID`
- `backend/internal/pkg/ip/ip_test.go`::`TestGetSecurityClientIPSwitchCannotOverrideTrustedPeer`
- `backend/internal/pkg/httpclient/public_test.go`::`TestPublicClientRejectsLocalTargetsAndRedirects`
- `backend/internal/pkg/httpclient/public_test.go`::`TestPublicDialPinsResolvedAddressAndRejectsMixedAnswers`
- `backend/internal/service/image_storage_security_test.go`::`TestImageUploaderRejectsPrivateURLAndNonImageContent`

后端 API/函数回归不称为 e2e；本次网页改动为安全设置说明与静态隐私披露。

- Run command: `cd backend && go test -tags=unit ./internal/observability/trajectory ./internal/observability/qa ./internal/util/logredact ./internal/pkg/ip ./internal/pkg/httpclient ./internal/server/middleware ./internal/service`

## Status

- InTest
