# Spec delta: route gemini-3-flash-preview onto gemini-3-flash

## Background

Gemini CLI 0.70 `google_web_search` 请求
`POST /v1beta/models/gemini-3-flash-preview:generateContent`（body 含
`tools:[{googleSearch:{}}]`）。`gemini-3-flash-preview` 已从可服务目录撤下，
字面量上游 404。`tk_pricing_overlay.json` 的 `_aliases` 只把该名字结算到
`gemini-3-flash`，不参与选号。Vertex / Antigravity 账号白名单故意不持久化这把
已撤下的钥匙，准入因此在选号前返回 unsupported_model 404。

这是对外模型契约变化：原先网关直接 404 的客户端模型名，改为路由到既有可服务 id。
公共目录、推荐位和账号 mapping 落盘面不恢复该 preview id。

## Delta

### ADDED

- 路由查找别名 `canonicalGeminiServableTrafficModel`，表为既有
  `geminiTrafficAliases`（当前仅 `gemini-3-flash-preview` → `gemini-3-flash`）。
- `normalizeRequestedModelForLookup` 在 Gemini、Antigravity、NewAPI/Vertex
  的 mapping 查找里使用该别名。账号白名单已包含目标 id 时，请求被准入，上游
  wire id 为目标 id。

### MODIFIED

- 无价格、目录推荐、Vertex/Antigravity floor 键集合变化。preview 仍是
  Antigravity structural-dead key，不写回 `model_mapping`。

### REMOVED

- 无。

## Scenarios

| 类型 | 场景 | 期望 |
|---|---|---|
| 正向 | 原生 `/v1beta` 请求 model=`gemini-3-flash-preview`，账号白名单含 `gemini-3-flash`、不含 preview | Plan 解析到 `gemini-3-flash`；不是 unsupported_model |
| 正向 | 同上且 body 含 `tools:[{googleSearch:{}}]` | 准入同样成功；工具不单独触发 unsupported_model |
| 负向 | 不在 `geminiTrafficAliases` 的未知模型 | 仍 `ErrUniversalUnsupportedModel`（网关 404） |
| 回归 | 公共目录 / Vertex ch41 floor / AG 默认 mapping | 仍不包含 preview 键 |

## Validation

```bash
cd backend && go test -tags=unit ./internal/service/ -count=1 \
  -run 'TestGeminiServableTrafficAlias|TestCandidateEligibilityGeminiNativeEdgeMapping|TestGatewayService_isModelSupportedByAccount_AntigravityNoMapping|TestMapAntigravityModel_ConvergedSurfaceFloor|TestAntigravityGatewayService_GetMappedModel_ConvergedSurface'
```

## 未纳入

Antigravity 转发把仅含 `googleSearch` 的 body 标成 `requestType=web_search`
（`antigravity_gateway` wrap）。本变更不改该路径。别名后若上游拒绝 web_search，
另开跟进。
