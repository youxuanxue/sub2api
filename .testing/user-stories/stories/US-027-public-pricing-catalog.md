# US-027-public-pricing-catalog

- ID: US-027
- Title: Public model + pricing catalog reachable without auth so visitors can decide before registering
- Version: V1.5 (cold-start P0-A)
- Priority: P0
- As a / I want / So that:
  作为 **未登录的潜在 TokenKey 用户**，我希望 **进站点首页就能看到「这个站支持哪些模型 + 单价 + 上下文窗口 + 走哪条协议」的目录页**，**以便** 我在不消耗试用额度（甚至不留邮箱）的前提下，自己判断 TokenKey 是否覆盖我的工作流；不再像 L 站 `t/topic/1413702` 反映的那样，「点进去只见登录页就走了」。

- Trace:
  - 角色 × 能力轴线：游客 × 「了解站点支持范围」——今天该格是空的，没有任何公开入口暴露模型 / 价格 / 协议信息。
  - 防御需求轴线：公开端点必须只暴露元数据，**不得**带出 account / channel_type / api_key / access_token 等敏感字段。
  - 系统事件轴线：每次未登录访问 `/pricing` 页面 → 调用 `GET /api/v1/public/pricing` → 响应应在 60s 缓存窗口内可重复获取，避免对站点形成爬虫源头。

- Risk Focus:
  - 逻辑错误：group `status != active` 或 `visible_in_catalog=false` 必须不出现在 `groups[]`；`pricing_catalog_groups_visible="all"` vs JSON 数组两种取值都要正确解析；空交集（pricing JSON 无对应 model）模型必须不出现在 `data[]`。
  - 行为回归：与现有 `GET /v1/models`（鉴权后端点）行为正交，本端点 fallback 不得改 `/v1/models` 任何行为；现有 `model-pricing` JSON 加载逻辑不得被改坏。
  - 安全问题：响应 JSON 中**不得**出现 `account_id`、`channel_type`、`api_key`、`access_token`、`organization_id`、`base_url`、内部 cost_per_token 浮点原值（必须四舍五入到 1k token 单价）。
  - 不适用：状态机——本端点是只读元数据查询，无状态迁移。

## Acceptance Criteria

1. **AC-001 (正向 / 未登录可访)**：Given setting `pricing_catalog_public=true` 且至少一个 `status=active` group，When 未带 `Authorization` 头 GET `/api/v1/public/pricing`，Then 返回 200，body 含 `data[]` 非空、`vendors[]`、`platforms[]`、`groups[]`、`updated_at`。
2. **AC-002 (正向 / 按 group 过滤)**：Given 两个 active group `claude-pool-default`、`openai-pool-default`，When GET `/api/v1/public/pricing?group_id=<claude_id>`，Then `data[].groups` 仅包含 `"claude-pool-default"`，`groups[]` 仅一项。
3. **AC-003 (负向 / setting 关闭)**：Given setting `pricing_catalog_public=false`，When GET `/api/v1/public/pricing`，Then 返回 404（与「该路径不存在」一致，不得返回 200 + 空 body 暗示路由存在）。
4. **AC-004 (负向 / 安全字段不泄漏)**：Given 任意配置，When 解析响应 JSON，Then 序列化后的字符串中**不**包含 `account_id`、`channel_type`、`api_key`、`access_token`、`organization`、`base_url`、`cost_per_token`（精确字符串匹配，避免被错误添加的字段意外引入）。
5. **AC-005 (负向 / 非 active group 不出现)**：Given 一个 group `status=disabled`，When GET `/api/v1/public/pricing`，Then 该 group 不出现在 `groups[]`，且其独有模型不出现在 `data[]`。
6. **AC-006 (回归 / 鉴权 `/v1/models` 行为不变)**：Given 本 PR 落地，When 持有效 API Key GET `/v1/models`，Then 响应结构、字段、模型集合与 baseline 完全一致（直接复用现有 `TestGetAvailableModels_*`）。
7. **AC-007 (回归 / 单元测试全绿)**：Given 本 PR 落地，When 执行 `go test -tags=unit -count=1 ./internal/...`，Then 全部包通过。

## Assertions

- `httpGet("/api/v1/public/pricing")` → status=200，`json.data` 长度 > 0，`json.object == "list"`。
- `httpGet("/api/v1/public/pricing?group_id=999999")` → status=200，`json.data == []`（找不到 group 不报错，返回空集合）。
- 在 setting `pricing_catalog_public=false` 时，`httpGet("/api/v1/public/pricing")` → status=404，且响应 body 不含 `"object":"list"`。
- 序列化后的响应字符串 `strings.Contains(body, "account_id") == false` 并对其余 6 个敏感词做同样断言。
- 创建一个 disabled group + 仅其拥有的模型 X，请求后 `assert.NotContains(json.data[].model_id, "X")`。

## Linked Tests

- `backend/internal/handler/us027_public_pricing_catalog_test.go`::`TestUS027_UnauthReturns200`
- `backend/internal/handler/us027_public_pricing_catalog_test.go`::`TestUS027_GroupFilter`
- `backend/internal/handler/us027_public_pricing_catalog_test.go`::`TestUS027_DisabledBySetting404`
- `backend/internal/handler/us027_public_pricing_catalog_test.go`::`TestUS027_NoSensitiveFields`
- `backend/internal/handler/us027_public_pricing_catalog_test.go`::`TestUS027_DisabledGroupNotExposed`

运行命令：

```bash
go test -tags=unit -count=1 -v -run 'TestUS027_' ./backend/internal/handler/...
```

## Evidence

- 待 PR 1 实现完成后归档到 `.testing/user-stories/attachments/us027-pricing-response-snapshot.json`（含一次真实请求/响应对照，证明字段集合）。

## Status

- [ ] Draft — 设计已定，等待审批；审批通过后进入 InTest。
