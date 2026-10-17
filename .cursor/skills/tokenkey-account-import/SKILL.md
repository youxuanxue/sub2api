---
name: tokenkey-account-import
description: >-
  TokenKey ops account import hub for prod account imports and read-only
  validation. Edge OAuth relay onboarding is retired until a live edge is
  explicitly re-approved.
---

# TokenKey：运营账号导入（唯一 Agent 入口）

所有账号导入从 `ops/accounts/import-accounts.sh` 进入。当前活跃舰队只有
prod；`deployable=false` 的 edge 不允许导入、签发 relay key 或创建 prod mirror
stub。`edge_oauth_relay` 和 `tokenkey-edge-prod-relay-onboard` 保留历史实现，只有
重新批准 edge、更新矩阵和部署门禁后才可恢复。

现网单端导入流程：

```bash
cd ops/accounts
./import-accounts.sh validate examples/anthropic-apikey.json
./import-accounts.sh import examples/anthropic-apikey.json --dry-run
./import-accounts.sh import examples/anthropic-apikey.json --yes
```

使用 `TOKENKEY_BASE_URL` 与 `TOKENKEY_ADMIN_API_KEY` 指向 prod。禁止把 token、
admin key 或 `tk_...` 写入 Git 或聊天。

```bash
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_ADMIN_API_KEY="admin-REPLACE_ME"
```

辅助命令：

```bash
./import-accounts.sh list-edges          # 当前应为空
./import-accounts.sh list-platforms
./import-accounts.sh list-channel-types  # newapi 专用
./import-accounts.sh selftest
```

可用格式包括普通 `create_account`、Antigravity 导出、Codex session、Grok SSO
和 `batch_bundle`。Gemini 不走 edge relay，直接使用单端导入。

## 退役 edge 的恢复边界

如果业务重新批准 edge，必须先把新 edge 加入 approved matrix 并通过部署、健康检查
和运行时门禁，再恢复 relay 文档与示例。手写 `edge_id`、`TOKENKEY_EDGE_BASE_URL`
或旧的 `api-us*.tokenkey.dev` 地址都不能绕过该门禁；当前所有旧 edge ID 都应被
校验拒绝。

详细行为以 `ops/accounts/import-accounts.py` 和 `ops/accounts/README.md` 为准。
