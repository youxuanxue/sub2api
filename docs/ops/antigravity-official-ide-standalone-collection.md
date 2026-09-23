# 官方 Antigravity IDE/LS 单机采集（WIP）

当前仅支持 macOS App 布局；Linux 适配器源码尚未随 profile-488 采集包提供。此版本是实验稿，不能作为已验证的生图采集流程。

待修复：非转发 sink 会打断启动／资格检查，可能导致尚未尝试连接 cloudcode；独立 LS 不保证继承 GUI 登录态；endpoint override 不保证阻止所有外联；缺少采集器哈希、实际启动参数和任务触发时间的证据。跨 TLS record 的握手重组也尚未正确实现／验证。现有单元测试不代表这些流程已验证。

采集脚本：[`collect-official-ide-standalone.sh`](../../ops/antigravity/collect-official-ide-standalone.sh)。它只使用 Python 3 标准库，不需要 pip、mitmproxy、tshark、Go 或 Node。Go 若存在只用于补充 `go version -m`，缺失也不影响采集。

脚本会保存：

- App 版本、bundle ID、Electron 版本、LS SHA-256、LS `--stamp`、`app.asar` 启动身份摘录；
- 官方 App 通过本地非转发 CONNECT sink 发出的首条 TLS record，按 SNI 可解析为 ClientHello 字段、JA3、ALPN、key-share group 和扩展顺序；
- 可选的官方 LS 本地 HTTP sink 事件：UA、客户端头、IDE metadata、顶层 JSON key 名、Authorization 是否存在。

它不会保存 token、refresh token、cookie、Authorization 值或完整请求体。TLS 原始 record 会保存在本地目录，便于复解析；原始 record 包含每条连接的 TLS random 和 key-share 公钥，派生 JSON 报告会去掉这些字段。TLS sink 不向 Google 转发，所以这轮只验证客户端证据，不验证模型服务成功。

## 同事机器上的操作

把下面两个文件一起复制到同事机器（或者复制整个 `ops/antigravity` 目录）：

```text
ops/antigravity/collect-official-ide-standalone.sh
ops/antigravity/collect_official_ide_standalone.py
```

先退出正在运行的 Antigravity，然后执行：

```bash
capture_dir="$HOME/Desktop/antigravity-ide-capture-$(date +%Y%m%d-%H%M%S)"
bash collect-official-ide-standalone.sh collect \
  --out "$capture_dir" \
  --target both \
  --launch \
  --seconds 90
```

`--launch` 会用当前用户的 App 数据目录启动官方 App，给它设置本地非转发代理，等待 90 秒后只终止本次启动的进程组。运行期间 App 的请求会失败，这是预期行为；不要在这轮执行真实业务操作或反复点击登录。

如果不希望脚本启动 App，可以省略 `--launch`，在另一个终端手动启动已登录 App，并把它的 HTTPS 代理设为脚本提示的本地端口：

```bash
bash collect-official-ide-standalone.sh collect \
  --out "$HOME/Desktop/antigravity-ide-capture" \
  --target app \
  --seconds 90
```

HTTP 阶段会自动启动 `language_server`，将 API 和 cloudcode endpoint 改为本地 HTTP sink。它使用当前用户 LS 状态读取登录信息；sink 只记录 `authorization_present=true/false`，不会保存凭据值。若 LS 没有可用登录状态，HTTP 事件为 0 是有效结果，不能把它解释成已登录生图证据。

## 交回文件

只需把整个目录压缩后交回：

```bash
ditto -c -k --sequesterRsrc --keepParent \
  "$HOME/Desktop/antigravity-ide-capture" \
  "$HOME/Desktop/antigravity-ide-capture.zip"
```

交回前确认目录中没有手工添加的日志、截图或 token 文件。若不希望交回 TLS random/key-share 原始字节，可以只交回 `report.json` 和 `http.jsonl`；但为了后续复解析和争议核对，建议保留原始文件。我们需要的主要文件是：

```text
report.json
snapshot.json（如果单独运行过 snapshot）
tls/connections.jsonl
tls/clienthello-*.bin
http.jsonl
```

## 证据解释

- `report.json` 中 `official-app-startup` 的 TLS 按 SNI 分组，是实际启动的官方 App/LS 连接证据；下载域名和 cloudcode 必须分开看。
- `official-ls-local-http` 只证明 LS 在本地 endpoint override 下生成了哪些 HTTP 身份字段，不等同于生产 HTTPS 连接的完整请求。
- `cloudcode` ClientHello 仍需已登录官方 App 触发真实模型请求才能作为生图 canonical profile；控制面握手、版本号或 HTTP 204 都不够。
- 如果出现 `403 VALIDATION_REQUIRED`，保留错误 reason 和发生阶段即可；不要把验证链接、token 或完整 URL 放进压缩包。
