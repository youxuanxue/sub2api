## 6) 真实测试（会话里尽量做完）

`compose up -d` 成功只代表容器调度成功；Agent **仍需**按下面顺序自检（对齐 prod 技能的 **部署后还须本地验收**，只是探针改为本机 `:8088`）。

### A — 经 Caddy 快速探活（无需网关 API Key）

```bash
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/health"
curl -sS -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:8088/api/v1/settings/public"
```

期望均为 **HTTP 200**。若要对 public 接口更严：`curl -sS "http://127.0.0.1:8088/api/v1/settings/public" | jq -e '.code == 0' >/dev/null`（需本机 **`jq`**）。

### B — 绕开 Caddy（应用本体 / 503 排查）

确认 tokenkey 容器内直连 **8080** 正常：

```bash
docker exec tokenkey wget -q -T 5 -O - http://localhost:8080/health
```

### C — 本地完整网关烟测（可选，与 prod 同款脚本）

与 **`tokenkey-stage0-release-rollout`** 中 **C** 使用同一 **`ops/stage0/post_deploy_smoke.sh`**，仅 **`TOKENKEY_BASE_URL`** 指向本机反代：

```bash
cd “${REPO_ROOT}” # 须在含 scripts/ 的仓库根；未导出 REPO_ROOT 时见「本项目路径约定」
export TOKENKEY_BASE_URL=http://127.0.0.1:8088    # 或 TK_GATEWAY_URL（脚本两个都识别）
# 对 prod 同款 key：TK_SMOKE_API_KEY（GitHub secret 值须本机 export）
# 可选 TK_SMOKE_GITHUB_ENV=prod 自动拉取 Environment variables
bash ops/stage0/post_deploy_smoke.sh
```

**前提**：须已有 **可用的用户侧网关 API Key**（新 AUTO_SETUP 栈通常没有——先在管理后台创建订阅用户与 key，或使用你专用于本地的测试 key）。**不得**打印完整 key、前缀或后缀；脚本只输出 `key=configured`。若缺 key：**不要卡住会话**，验收 **A+B**（及下方管理员登录）即可。

**烟测 key**：与 prod 一致——只导出 `TK_SMOKE_API_KEY`，它必须能看到 `TK_SMOKE_ANTHROPIC_MODELS` / `TK_SMOKE_GEMINI_MODELS` / `TK_SMOKE_OPENAI_OAUTH_MODELS` 中列出的模型；缺 key 或清单模型不可见不得视为验收通过。

**结构化验收要求**：与 prod skill § C 完全一致，以该节为准。唯一差异是 `TOKENKEY_BASE_URL=http://127.0.0.1:8088`（本地反代端口）。若有多个分组/key，按 key 的本机标识分别记录 group platform、`account_id/platform/model`；不要把一个 key 的通过误当成全部通过，也不要把 key 的任何片段写入日志。

本地 Caddy 开启压缩时，`tk_post_deploy_smoke.sh` 可能只输出启动行后等待连接关闭。若脚本卡住，不要降低验收标准：停止脚本后用同一 key 重跑等价请求，并显式加 `Accept-Encoding: identity`，仍按上面的结构化要求判定。

### 管理员会话（常与 A/B 一起做，≠ C 的网关 key）

用于验证 **AUTO_SETUP** 账密（**勿把密码粘贴到聊天**；只从本机 `.env` 引用）：

```bash
# TOKENKEY_STAGE0_LOCAL_ROOT 未导出时：见「本项目路径约定」或 §1
set -a
source "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env"
set +a
curl -sS -H 'Content-Type: application/json' \
  -d "$(printf '{"email":"%s","password":"%s"}' "${ADMIN_EMAIL}" "${ADMIN_PASSWORD}")" \
  "http://127.0.0.1:8088/api/v1/auth/login"
```
