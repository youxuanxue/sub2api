## 4) 代码修复清单（HTTP-only 型）

### 4.1 仅 UA / 版本漂移（最常见的 cc patch bump）

**先跑静态门禁**（与 `client-release-watch` playbook 一致）：

```bash
bash ops/anthropic/capture-cc-fingerprint.sh check env --static
bash ops/anthropic/capture-cc-fingerprint.sh check
bash ops/anthropic/capture-cc-fingerprint.sh emit-edits
```

单一真值源 + 守卫自动生成，**人手只碰 1 个字段**：

1. 按 `emit-edits` 更新 `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` 的 `cc_version`（唯一手改源）。
2. 跑 `python3 scripts/sentinels/check-cc-version-sync.py --write` —— 自动重写全部 8 份副本：
   - 4 个 Go 编译默认值：`constants.go` 的 `CLICurrentVersion` + `DefaultHeaders["User-Agent"]`、
     `identity_service.go` 的 `defaultFingerprint.UserAgent`、
     `identity_service_tk_canonical_http.go` 的 `DefaultClaudeCodeUserAgentVersion`。
   - 2 个死快照：`ops/stage0/smoke_lib.sh`、`deploy/aws/stage0/tk_canonical_cc_oauth.json` 的 `observed.user_agent`。
   - 1 个 go:embed 镜像（load-bearing，reconciler 自愈目标）：`backend/internal/baseline/anthropic-http-mimicry-baselines.json`
     与 deploy 源 byte-identical 同步。
3. **不写**独立 spec-delta（纯版本 bump 没有行为变更意图）。记录由提交信息
   + `baselines.json` `cc_version` + `.tls_list/*-cc-capture.bundle.json` 天然承载；
   只在 `docs/ops/cc-fingerprint-changelog.md` **追加一行**（版本｜日期｜`pure UA`｜
   `A→B, TLS/beta 未变`，含 comprehensive 的 haiku A/B 计数）。一行，不是一文件。

> skill 总是跑 `--write` 并 **review 生成的 diff**（编译兜底 UA 值值得扫一眼）。
> `check-cc-version-sync.py`（check 模式）在 preflight / CI 兜底防漂移——手工漏跑 `--write` 会被拦。
> `test_capture_cc_fingerprint.py` 的版本断言已派生自 `cc_version`，无需手改。

### 4.2 beta 集合漂移（comprehensive 抓到稳定新 token，且非 A/B 灰度）

`--write` 只同步版本字符串，**不碰 beta 列表**。beta 真变了才手改，且必须有真实抓包证据：

- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` 的 `sonnet_opus` / `haiku` 数组。
- `backend/internal/pkg/claude/constants.go` 的 beta 常量 + `HaikuBetaHeader` / `FullClaudeCode*MimicryBetas()`。
- claude 包对应单测。
- 若新增 load-bearing 面：`scripts/sentinels/gateway-tk.json`。
- **写/更新一份按主题命名的决策记录** `docs/spec-delta/cc-<topic>.md`（如
  `…-haiku-beta-ab.md`、`…-canonical-ua.md`；不要用版本号命名、不要一 patch 一份），
  记录 token 集合、分布与抉择理由，并就地更新；代码按稳定名引用它。在
  `docs/ops/cc-fingerprint-changelog.md` 追加一行、type 标 `decision` 并链到该记录。
  （bimodal Haiku A/B 已在 `docs/spec-delta/cc-2.1.160.md` + #429 刻画，勿逐 patch 重述。）

### 4.4 Geo stego body 漂移（`--check-gateway` FAIL；capture 内建 `--fix` 未收敛）

`--fix` 只能机械补 **未知 Unicode 引号码点**（写入 Go regex 字符类）+ 追加 table test。**日期格式新模式 / 新 surface** 仍需人工：

- `gateway_request_tk_cc_geo_stego.go` 纯函数 + 测试（fixture 来自 `.tls_list/geo-stego-*/capture.jsonl` 的 `body_wire`）。
- 三条出站路径挂接 + `scripts/sentinels/gateway-tk.json`。
- `Web impact: none`；不写 beta/UA spec-delta。

### 4.5 system prompt 锚点漂移（`system.identity_anchor` FAIL，需抓包证据）

CC system prompt 是 load-bearing 指纹维度（上游检测身份 banner + 计费块）。只对齐**稳定锚点**，不对齐动态全文。单一声明源 = `scripts/sentinels/cc-system-prompt.json` 的 `capture_anchors`，同时被守卫与抓包 diff 共用。锚点真变了才手改，且必须有正常 `/v1/messages` 抓包证据：

- `scripts/sentinels/cc-system-prompt.json`（唯一声明源：`capture_anchors` + `sentinels[].must_contain` + `byte_identical`）。
- 同一 commit 同步 Go 副本：`claude_code_validator.go` 的 `claudeCodeSystemPrompts[]` / `claudeCodeBillingHeaderPrefix`、`gateway_service.go` 的 `claudeCodeSystemPrompt`（banner）/ `claudeCodePromptPrefixes[]`；banner 在两文件须**字节一致**。
- `ops/anthropic/test_capture_cc_fingerprint.py` 的 system 断言（如锚点串变了）。
- 决策记录就地更新 `docs/spec-delta/cc-system-prompt.md` + `docs/ops/cc-fingerprint-changelog.md` 追加 `decision` 行。

守卫 `check-cc-system-prompt.py` 是**纯守卫无 `--write`**：它只证明"代码 == 注册表 + banner 字节一致"；漂移由抓包侧发现，人工带证据改。无发版（capture + 守卫 + 文档，无运行时/编译产物变更）。
