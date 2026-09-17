---
name: tokenkey-codex-fingerprint-alignment
description: Align TokenKey OpenAI Codex fingerprint versions with the installed Codex CLI. Use for local version drift, owner/alias consistency checks, and version bumps; non-version pins require separate evidence.
---

# TokenKey：Codex 指纹对齐

读取本机 `codex --version` 与 native binary strings，对齐 TokenKey OpenAI 平台版本。机械采集/diff/派生检查由 `ops/openai/capture-codex-fingerprint.sh`（Python owner 同目录）负责；非版本变化需要真实证据判断。

## 修改边界

- 唯一可编辑版本 owner：`backend/internal/service/setting_gateway_runtime.go` 的 `DefaultOpenAICodexVersion`。UA、gateway `version`、usage probe `Version` 都从它派生，不新建 baseline JSON。
- 版本 bump 保持 UA 的 OS/终端段原样；i18n `openaiCodexUserAgentPlaceholder`、`request.go`/测试里的格式示例不随版本修改。
- `originator=codex-tui`、`OpenAI-Beta: responses=experimental` 只读确认。binary strings 未找到不是漂移证据（可能运行时拼接）；上游 4xx 或实测明确换值才另行诊断，不自动修改。
- `/v1/messages` compat 桥接删除 originator/beta 是设计行为，不算 OAuth 出口指纹漂移。不抓网络流量、不挂每日 hook。

## 执行

```bash
codex --version
bash ops/openai/capture-codex-fingerprint.sh check env
bash ops/openai/capture-codex-fingerprint.sh diff
bash ops/openai/capture-codex-fingerprint.sh emit-edits
```

已对齐就结束；否则按 emit-edits 只更新版本 owner。指定目标版本用 `emit-edits --version <version>`；需要机器输出用 `--json`。退出码：0 对齐/一致，1 漂移/派生断裂，2 用法/环境错误。

改后验证一次：

```bash
bash ops/openai/capture-codex-fingerprint.sh check-consistency
python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
./scripts/preflight.sh
```

`check-consistency` 只守卫版本派生契约，不对移动的上游版本判 CI 失败。纯版本字面量 bump 且无符号/锚点变化时使用 `upstream-touch-trivial` + `sentinel-registry-reviewed`；否则按实际 diff 判断。PR/合并遵循根规则。
