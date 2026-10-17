# 运营账号导入

用 Admin API 创建 TokenKey 账号。支持平台：`anthropic` · `openai` · `gemini` · `antigravity` · `newapi` · `kiro` · `grok`。

**环境**：Python 3.10+，无需安装项目依赖。

## 现网边界

2026-10-17 起活跃舰队只有 prod，edge 部署和 prod edge relay stub 已退役。`edge_oauth_relay`、`edge-prod-relay-onboard.sh` 与旧 edge 模板仅保留历史实现；当前 live matrix 为空时，校验、规划和导入均 fail closed。不要为任何 `api-*.tokenkey.dev` 创建账号、中继或 API key。

如果重新批准 edge，必须先更新 approved matrix、部署入口、健康门禁和运行时检查，再恢复 relay 文档和示例。手写 `edge_id` 或 `TOKENKEY_EDGE_BASE_URL` 不能绕过该门禁。

## 当前导入流程

现网只使用单端 `create_account` 或平台快捷格式，按实际出口需求绑定日本代理；
现役代理身份见 [舰队清单](../../deploy/hetzner/README.md#日本出口代理现役)。

```bash
cd ops/accounts
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_ADMIN_API_KEY="admin-REPLACE_ME"
./import-accounts.sh validate examples/anthropic-apikey.json
./import-accounts.sh import examples/anthropic-apikey.json --dry-run
./import-accounts.sh import examples/anthropic-apikey.json --yes
```

辅助命令：

```bash
./import-accounts.sh list-edges          # 当前应为空
./import-accounts.sh list-platforms
./import-accounts.sh list-channel-types  # newapi channel_type 对照
./import-accounts.sh selftest
```

禁止把 token、admin key 或 `tk_...` 写入 Git 或聊天。Gemini 不走 edge relay，直接使用单端导入。

---

## CodeBuddy / WorkBuddy models.json 同步

本机 CodeBuddy CLI 与 WorkBuddy 桌面版共用一份 `models.json`（SSOT + 软链）：

| 路径 | 角色 |
|------|------|
| `~/.codebuddy/models.json` | **唯一真源**（canonical） |
| `~/.workbuddy/models.json` | 软链 → `~/.codebuddy/models.json` |

模型清单来自 **TokenKey universal Gateway Key** 的实时 `GET /v1/models`（默认 `TK_FULLTEST_KEY`），自动排除纯图像/视频模型，并从 `/api/v1/public/pricing` 补 context/capabilities。`apiKey` 写 `${TK_FULLTEST_KEY}`，不写明文 sk。

```bash
cd ops/accounts

# 写入 SSOT + 更新 WorkBuddy 软链
export TK_FULLTEST_KEY='sk-...'
./sync-codebuddy-models-json.sh

# 只预览 JSON，不写文件
./sync-codebuddy-models-json.sh --dry-run | head

# 校验本机 ~/.codebuddy/models.json 是否与线上一致
./sync-codebuddy-models-json.sh --check

# 自测
python3 test_sync_codebuddy_models_json.py
```

可选环境变量：`TK_FULLTEST_BASE_URL`（默认 `https://api.tokenkey.dev`）。

---
