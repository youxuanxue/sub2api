---
name: tokenkey-account-import
description: >-
  TokenKey ops account import hub for prod account imports and read-only
  validation.
---

# TokenKey：运营账号导入（唯一 Agent 入口）

所有账号导入从 `ops/accounts/import-accounts.sh` 进入。当前只支持 prod 单端导入。

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
./import-accounts.sh list-platforms
./import-accounts.sh list-channel-types  # newapi 专用
./import-accounts.sh selftest
```

可用格式包括普通 `create_account`、Antigravity 导出、Codex session、Grok SSO
和 `batch_bundle`。

详细行为以 `ops/accounts/import-accounts.py` 和 `ops/accounts/README.md` 为准。
