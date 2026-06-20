# TokenKey 全平台 × 全模态网关一致性测试

`gateway_full_matrix_test.sh` 用**一把 universal key** 把 TokenKey 支持的全平台 × 核心模态打一遍，
既验证 **Universal Key 路由特性**（#878），又系统性覆盖每个平台/模态的网关一致性。

## 焦点

一句话：用一把 universal key，证明 key 主人有权的 *每个真实平台 × 每个核心模态* 都真能服务。

平台恰好 **7 个**（`backend/internal/domain/constants.go`）：
`anthropic / openai / gemini / antigravity / newapi / kiro / grok`。
没有「meta 平台」—— `/v1/models`、`/v1/usage`、`/v1/settings/public` 是**控制面端点**，
作为矩阵前的预检（`/api/v1/settings/public`、`/v1/models`、`/v1/usage`），不在平台矩阵里。

核心模态：**text / image / video**（`count_tokens`、`embeddings` 是 `--with-extras` 才跑的扩展）。

## 为什么是 universal key

Universal Key 按「入口端点形状 + 请求模型名」在请求期把一把 key 解析到对的后端组
（`backend/internal/server/middleware/universal_routing_tk.go`）。所以一个模型名就能驱动一个平台。
解析是**静默**的（成功即就地换组，下游零日志），观察点是**下游真实响应 + usage_log 归因**。

### universal-key 命名空间盲区（务必知道）

universal 只能按**模型前缀 hint** 选平台
（`backend/internal/service/universal_routing_tk_endpoint_map.go`）：

- `claude-*` 永远落 **anthropic** → 到不了 **kiro**（kiro 也服务 claude-*）。
  **kiro 需一把绑 kiro 组的 direct key**（`TK_FULLTEST_KIRO_KEY`），缺则该行 SKIP。
- `gemini-*` 落 **gemini** → 到不了 antigravity 的原生调度。
  **antigravity 经其 forced-platform 路由** `/antigravity/v1beta/models/<m>:generateContent` 强制到达。

## 用法

```bash
export TK_FULLTEST_KEY='sk-...'                 # universal key（本机 export，绝不入库）
bash ops/test/gateway_full_matrix_test.sh                 # 默认 prod、含付费模态
bash ops/test/gateway_full_matrix_test.sh --skip-paid     # 跳过 image/video（不花钱）
bash ops/test/gateway_full_matrix_test.sh --with-extras   # 追加 count_tokens / embeddings
bash ops/test/gateway_full_matrix_test.sh --list          # 只打印矩阵，不发请求
```

或把变量写进 `ops/test/.fulltest.env`（拷自 `.fulltest.env.example`，被 `.gitignore` 的
`*.env` 规则忽略），脚本启动会自动 source。

### ⚠ 计费告警

`image` / `video` 行向上游**真实下单，产生真实费用**（用你提供的 key 计费）。默认开启
（“全部测一遍”）；不想花钱用 `--skip-paid`。视频只验**提交 + 一次可查**，不轮询到渲染完成。

## 判定语义（跑完所有行再汇总，不中途退出）

| 结果 | 触发 |
|---|---|
| **PASS** | 200 且响应 shape 正确 |
| **FAIL** | 200 但 shape 不符（schema 回归）/ 401 / 非预期 403 / 非预期 4xx / 连接失败 |
| **SKIP** | 403 未授权该平台 / 429 空池或限流 / 5xx 上游瞬态 / 4xx 模型不可服务 / 缺 key / `--skip-paid` |

退出码：任一 **FAIL** → 1；只有 PASS/SKIP → 0。
平台不可达（未授权/空池/冷却/模型未在册）一律 **SKIP-with-reason，绝不 FAIL** —— 那不是网关的错。

> newapi / grok / kiro 的代表模型依赖 key 主人的实际授权与账号 `model_mapping`。若该模型未授权，
> 解析返回 403 → SKIP。用 `TK_FULLTEST_MODEL_*` 换成你账号实际在册的模型名再跑。

## 交叉验证 universal 解析落点（可选）

跑完后，运维可经 prod SSM 查 usage_log 看每条请求**解析到的后端组 + 平台**，实证 universal
路由把每个模型落到了对的平台（`api_key_id` 取该 universal key 的行 id）：

```sql
SELECT created_at, requested_model, model, group_id
FROM usage_log
WHERE api_key_id = <KEY_ID>
ORDER BY created_at DESC LIMIT 20;
```

## 边界 / 后续可扩展

- 不轮询视频到渲染完成（控成本）。
- 未测 `/v1/images/edits`（multipart + openai-only + 需图片素材）—— 后续可加。
- 不硬编码渠道/平台清单做断言；矩阵默认值可被 env 全覆盖。
