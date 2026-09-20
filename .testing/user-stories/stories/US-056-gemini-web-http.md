# US-056 Gemini Web Cookie HTTP gateway

- ID: US-056
- Priority: P1
- Title: Gemini Web Cookie HTTP gateway
- As a / I want / So that: 运营复用两个独立 Web 会话，经 us4 提供官方 Gemini 格式的文本和原图。
- Trace: [授权与契约](../../../docs/approved/gemini-web-channel.md#10-cookie纯-http-双账号实现)
- Risk Focus:
  - 逻辑错误：位置编码帧解析、官方响应、原图与预览区分。
  - 行为回归：现有 Gemini gateway 经兼容 upstream 调用，无后端改动。
  - 安全问题：密钥选账号、Cookie 隔离、下载域名限制、错误脱敏。
  - 运行时问题：互斥、原子持久化、重启、下载失败不重复生图。

## Acceptance Criteria

1. AC-001 正向：完成候选转换为官方 text/inlineData；不得虚构 usage/thoughts。
2. AC-002 负向：无效鉴权、越界请求、非法图片 URL 在上游副作用前被拒绝。
3. AC-003 隔离：一号 Cookie 更新持久化后重启可读，另一号 Cookie 不变；同号重入被拒绝。
4. AC-004 回归：图片下载失败不再次发起生成，也不退回预览；过期会话暂停状态跨重启保存。

## Assertions

断言官方候选内容和原图字节一致、拒绝时无上游调用、不同账号 Cookie 不串写、失败不重生图。

## Linked Tests

- AC-001: `ops/gemini-web/test_worker.py`::`WorkerTests.test_official_response_omits_unobserved_usage_and_thoughts`
- AC-001: `ops/gemini-web/test_worker.py`::`WorkerTests.test_original_rpc_and_text_url_hops_return_real_bytes`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_http_auth_and_buffered_sse`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_bad_input_has_no_upstream_side_effect`
- AC-002: `ops/gemini-web/test_worker.py`::`WorkerTests.test_untrusted_hops_rejected_before_request`
- AC-003: `ops/gemini-web/test_worker.py`::`WorkerTests.test_isolation_atomic_persistence_and_restart`
- AC-004: `ops/gemini-web/test_worker.py`::`WorkerTests.test_download_failure_does_not_regenerate_or_return_preview`
- AC-004: `ops/gemini-web/test_worker.py`::`WorkerTests.test_auth_failure_persists_pause_and_transport_error_is_redacted`

运行命令：

```sh
python3 -m pip install -r ops/gemini-web/requirements.txt
python3 -m unittest discover -s ops/gemini-web -v
python3 -m unittest discover -s ops/stage0 -p 'test_probe_account_model*.py'
```

## Evidence

本地协议/HTTP 测试通过。us4 两号均完成文本、新图、官方 inlineData、用量归属和 Worker 重启后的网关验收；长期运行仍需持续观察。
无 UI 工件；以上为协议/集成验证，不是 UI e2e。

## Status

- InTest
