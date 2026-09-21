# Antigravity-Manager 路线实验记录

本实验把 Manager 的 HTTP 身份与 Chrome 风格传输作为一个账号级对照组，和现有 Antigravity CLI 路线隔离。产品目标是官方 Antigravity 真人客户端/`language_server` 的真实出口；Manager 不是 ground truth，也不应因为更像浏览器就成为默认路线。实验只用于回答“完整身份组合是否改变生图结果”，不把单独的 UA、TLS 或模型列表成功当作结论。

## 实现边界

- 默认账号现使用官方 IDE/LS 的 `antigravity/hub/<version> darwin/arm64` 和 `tk_canonical_antigravity_ide_cloudcode`；CLI 只有在 `extra.antigravity_client_profile=cli` 时启用。
- 只有 OAuth 账号 `extra.antigravity_client_profile=manager` 才选择 Manager 路线。
- Manager 路线发送 Manager 形态 UA 以及 `x-client-name`、`x-client-version`、`x-machine-id`、`x-vscode-sessionid`；machine/session 值按账号和会话稳定生成，重试不改变。
- `tk_canonical_antigravity_manager_chrome123` 当前使用 uTLS `HelloChrome_120` 兼容预设，因为 vendored uTLS 没有 Chrome123 常量。数据库和 JSON artifact 都标记 `pending-real-manager-clienthello`，不能作为“已捕获 Chrome123”的证明。

## 本地验证结果

已运行：

```text
python3 -m unittest discover -s ops/antigravity -p 'test_*.py' -t ops/antigravity  # 43 passed
go test -tags unit -run 'TestManager|TestAntigravityManager|TestAntigravityClientProfile|TestResolveTLSProfile_Antigravity' ./internal/pkg/antigravity ./internal/pkg/tlsfingerprint ./internal/service ./migrations  # passed
```

验证覆盖：默认 CLI 与显式 Manager 选择、Manager header/UA 一致性、重试 identity 稳定性、Manager profile 缺失时安全回退、Chrome preset 的 ALPN/扩展结构，以及采集器对 pending profile 的拒绝。

## 真实采集与升级流程

1. 用官方 Manager 版本触发一次真实请求；同时保存 HTTP MITM 日志和被动 `tshark` ClientHello TSV。MITM 上游握手不能代表原客户端 ClientHello。
2. 生成 profile：

   ```bash
   python3 ops/antigravity/manager_fingerprint.py profile-from-tsv \
     --tshark-tsv /path/to/manager-clienthello.tsv \
     --manager-version 4.3.0 \
     --out /tmp/tk-manager-profile.json
   python3 ops/antigravity/manager_fingerprint.py check --profile /tmp/tk-manager-profile.json
   ```

3. 只有 `capture_status=real-clienthello-captured`、非空 cipher/extension、可重算 JA3 的 artifact 才能进入 canonical profile review。将采集值导入 migration/deploy artifact 后，再运行 TLS replay 和 HTTP header/body 回归。
4. 更新 Manager 版本时先执行 `emit-headers`，再重新采集；UA、x-client-version 和 ClientHello 必须来自同一版本批次。旧 profile 不覆盖新版本，除非对照实验确认可复用。

## 本地采集结果

本机没有独立 Manager 应用，因此使用固定依赖 `rquest 5.1.0`、`rquest-util 2.2.1` 的最小本地 probe，配置 `Emulation::Chrome123`，连接本地 TLS 服务端。OpenSSL `-tlsextdebug -msg` 连续记录了 5 次 ClientHello。结果显示：

- cipher suite、supported groups、ALPN 和非 GREASE 扩展集合稳定；
- GREASE 值每次变化；
- 扩展顺序每次变化；
- ECH 扩展 payload 长度也会变化；
- 因此不能把单次 JA3 或扩展顺序写成固定 canonical profile。

另外用同一 probe 对比了 `rquest-util 2.2.1` 提供的 `Chrome136` preset：它的 ClientHello 长度和扩展/加密扩展 payload 明显不同，不能视为 Chrome123 的小版本升级。当前 Manager `main` 仍明确调用 `Emulation::Chrome123`，所以 Chrome136 只能作为独立候选实验组。本机 Chrome `153.0.8010.48` 更不能直接替换进来：rquest-util 没有 Chrome153 preset，且本机浏览器的 TLS、QUIC、HTTP/2 栈与 Manager 的 Rust rquest transport 不是同一客户端。

摘要保存于 [antigravity-manager-rquest-chrome123-local-capture-20260920.json](./antigravity-manager-rquest-chrome123-local-capture-20260920.json)。后续采集可以复用：

```bash
openssl s_server -accept 18443 -cert cert.pem -key key.pem -www -tls1_3 -tlsextdebug -msg \
  > manager-openssl.log 2>&1
# 在另一个终端运行 Manager 或同版本 rquest probe，目标为本地 HTTPS 服务
python3 ops/antigravity/manager_fingerprint.py analyze-openssl \
  --log manager-openssl.log \
  --manager-version 4.3.0 \
  --out manager-capture-summary.json
```

`analyze-openssl` 只生成采集摘要，不会自动修改 canonical profile；只有补齐原始 ClientHello payload、Google 目标 SNI 和真实生图对照后，才进入 profile 升级流程。

## 受控线上实验设计

固定同一 OAuth 账号、同一 edge 出口、同一模型、同一图片输入、同一时间窗口，只切换 `cli` / `manager` profile。每个样本记录实际图片 bytes、上游状态、request ID、模型、profile、UA family、ALPN 和（若被动采集）JA3。成功定义为返回可解码图片并归属预期模型/账号；`listModels`、额度接口或一次 HTTP 200 都不算生图成功。

若官方 IDE 在同一账号和出口也收到 `VALIDATION_REQUIRED`，应先处理 Google 账号验证/资格与出口问题。若官方 IDE 成功而 Manager 模拟失败，按完整请求身份、传输、信封逐项缩小差异；不能把本实验结果解释为绕过 Google 验证。
