---
name: tokenkey-kiro-fingerprint-alignment
description: >-
  Capture and diff complete real Kiro CLI TLS, HTTP, protocol, and auth evidence against TokenKey's single CLI runtime identity and canonical TLS profile. Use for kiro-cli release drift, Kiro TLS parity, or replay verification.
---

# TokenKey：Kiro CLI 指纹对齐

真实、已登录的 `kiro-cli` 是唯一 ground truth；release metadata、版本字符串、binary
strings 和 TokenKey 自己构造的请求都不能替代 wire evidence。证据缺失必须保留为
`NOT_OBSERVED`，禁止从 UA 或版本号推断 TLS。

## 唯一链路

```text
real kiro-cli
  → mitm collector: ClientHello + HTTP/protocol projection
  → sanitized auth cohort
  → tk_canonical_kiro_cli.json
  → TokenKey uTLS replay capture
  → production-configured DB parity
```

- HTTP owner：`backend/internal/pkg/kiro/constants.go`，由
  `backend/internal/integration/kiro/headers.go` 的所有请求路径消费。
- TLS capture owner：`deploy/aws/stage0/tk_canonical_kiro_cli.json`。
- Runtime DB owner：同名 `tls_fingerprint_profiles` row；daily parity 只证明
  `production_configured`，不冒充 live wire。
- `backend/migrations/tk_014_seed_kiro_ide_tls_template.sql` 只是不可变迁移历史；
  `tk_081_kiro_cli_tls_profile.sql` 完成现行 CLI row 的原子切换。

## 工具

- `capture-kiro-fingerprint.sh`：运行真实 `kiro-cli translate` 至少三次。
- `mitm_kiro_http_logger.py`：从源头只保留允许的 TLS/HTTP/协议字段；丢弃凭证、
  profile ARN、用户内容、完整 body 和 key-share bytes。
- `capture_kiro_fingerprint.py`：机械分类 auth cohort、验证四条 evidence lanes、
  做 semantic diff、生成 profile、验证 replay。
- `check_kiro_tls_profile_parity.py`：比较 committed profile 与只读 DB snapshot。
- `probe-runtime-gateway.sh` / `probe_runtime_gateway.py`：shell 是稳定入口，Python
  用 committed CLI HTTP identity 做协议兼容性 probe；这是 TokenKey probe provenance，
  不是真实 CLI capture。

## 标准流程

```bash
# 1. 环境与真实 CLI 版本；不会打印 token。
bash ops/kiro/capture-kiro-fingerprint.sh version

# 2. 真实采证。要求已登录 CLI、mitmdump 和本机 mitm CA。
bash ops/kiro/capture-kiro-fingerprint.sh capture --samples 3
# → .kiro_tls/<stamp>-kiro-cli.bundle.json（ignored）

# 3. 完整证据门禁与 semantic diff。
bash ops/kiro/capture-kiro-fingerprint.sh check \
  --bundle .kiro_tls/<stamp>-kiro-cli.bundle.json

# 4. 仅在完整真实证据通过后刷新 canonical profile。
bash ops/kiro/capture-kiro-fingerprint.sh emit-profile \
  --bundle .kiro_tls/<stamp>-kiro-cli.bundle.json

# 5. 捕获 TokenKey/uTLS 重放后，验证其 semantic projection。
bash ops/kiro/capture-kiro-fingerprint.sh check-replay \
  --tls-jsonl /tmp/kiro-tokenkey-replay-tls.jsonl

# 6. 本地 canonical projection；生产 snapshot 由只读 workflow 提供。
python3 ops/kiro/check_kiro_tls_profile_parity.py
```

退出码统一：`0=完整且对齐`、`1=semantic drift`、`2=无效证据/执行失败`、
`3=incomplete/NOT_OBSERVED`。

## 漂移处理

1. 先确认完整 real-client bundle，不接受 release/version-only 代证。
2. HTTP 漂移只改唯一 CLI owner 与 request-recorder tests。
3. TLS 漂移用 `emit-profile` 刷新 CLI JSON；若 runtime 字段改变，新增 forward
   migration 投影至同名 DB row，不能改已发布 migration。
4. 重跑 uTLS capture，至少三次；`shuffle_extensions=true` 时必须看到多个 extension
   orders，并用 `check-replay` 比较 semantic projection，而非固定 JA3 hash。
5. 运行 focused tests、sentinel、preflight 和全量测试后再提交。

## 禁止

- 不新增 IDE/CLI 双模式、alias、fallback、环境版本 override 或 retired profile row。
- 不提交 `.kiro_tls/`、token cache、pcap、mitm log、原始请求/响应正文。
- 不打印 access token、refresh token、client secret 或 profile ARN。
- 不把 production-configured parity、TokenKey probe 或 uTLS replay标成真实 CLI capture。
