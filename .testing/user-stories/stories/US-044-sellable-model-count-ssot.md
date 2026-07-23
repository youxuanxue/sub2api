# US-044-sellable-model-count-ssot

- ID: US-044
- Title: 当前可售模型数量来自 public catalog 最终投影
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **评估 TokenKey 的用户**，我希望 **首页、模型页和价格页展示同一个实时可售模型数**，**以便** 我知道这是服务/定价/展示共同承诺的产品目录，而不是互相冲突的营销数字。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §8。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G4。
  - 现有 SSOT：`docs/approved/pricing-serving-single-source-of-truth.md`、`docs/approved/pricing-availability-source-of-truth.md`、`docs/approved/priced-or-it-doesnt-ship.md`。
- Risk Focus:
  - 逻辑错误：从 handler 最终 prune 之前的数据计数，或按 protocol row 重复计算同一 model ID。
  - 行为回归：把瞬时 cooldown/容量归零解释为产品下架，导致目录数字抖动。
  - 安全问题：为解释数量而把 account/pool/topology availability 暴露到 public response。
  - 运行时：目录失败后回退硬编码 `119`/`200+` 或陈旧缓存，制造虚假确定性。

## Acceptance Criteria

1. **AC-001（唯一 projection owner）**：Given public catalog 请求，When service 生成响应，Then servable filter、structural availability prune、去重 data 与 summary 都由同一个 `PublicCatalogProjectionService` immutable result 产生，handler 不再二次改变集合。
2. **AC-002（计数定义）**：Given served/priced/displayed rows，When计算 `sellable_model_count`，Then按最终返回的规范化 `model_id` 去重，且 `summary.catalog_updated_at` 与响应 projection 一致。
3. **AC-003（页面一致）**：Given一次成功且 `catalog_state=ready` 的 public pricing response，When首页、`/models`、`/pricing` 渲染，Then三处只显示同一 `summary.sellable_model_count` 与目录时间，不各自 `data.length` 或读取常量。
4. **AC-004（服务承诺语义）**：Given模型仍在 served/priced/displayed projection 但当前池 cooldown/繁忙，When刷新目录，Then模型仍计入“当前可售”；只有结构性不可服务/下架才从 projection 移除。
5. **AC-005（失败降级）**：Given API 错误、invalid summary、degraded source 或过期 projection，When页面渲染，Then source 明确返回 unavailable 状态，页面隐藏数字并链接实时目录；服务端可在定义 TTL 内返回完整 final projection cache，但浏览器不回退独立缓存、静态数字或“200+兼容渠道”。
6. **AC-006（静态口径 gate）**：Given first-party locale/component/content 变更，When运行 count-claim gate，Then `119`、`200+` 或其他手写模型总数导致失败；兼容证据覆盖数必须使用不同标签与 G5 数据。
7. **AC-007（custom home 入口）**：Given管理员配置 `home_content` 替换默认 landing，When访问首页，Then外层 first-party trust nav 仍提供 canonical models/pricing/docs 入口，custom content 不成为 count SSOT。

## Assertions

- “当前可售”就是 service/pricing/display SSOT 的可服务承诺，不等于瞬时空闲容量。
- response 不新增 account、group、pool、Edge 或内部 availability reason。
- `/public/pricing` 是数据与 summary 的同一 wire contract，不增加第二份模型列表。
- 首页失败态宁可不显示数字，也不显示无法证明的最后一次营销常量。

## Linked Tests

- `backend/internal/service/public_catalog_projection_test.go`::`TestPublicCatalogProjectionCountMatchesFinalDeduplicatedData` *(planned)*
- `backend/internal/handler/pricing_catalog_handler_tk_test.go`::`TestPublicCatalogHandlerDoesNotMutateProjectedCatalog` *(planned)*
- `frontend/src/views/__tests__/SellableModelCount.spec.ts`::`home models and pricing render the same API summary` *(planned)*
- `frontend/src/views/__tests__/SellableModelCount.spec.ts`::`catalog failure hides the count without a static fallback` *(planned)*
- `PublicModelCountClaimsRejectFirstPartyStaticTotals` *(planned in the P0-G4 implementation PR)*

运行命令：

```bash
cd backend && go test -tags=unit ./internal/service ./internal/handler -run PublicCatalog -count=1
cd frontend && npm run test:unit -- SellableModelCount.spec.ts
python3 -m unittest discover -s scripts/checks -p 'test_public_model_count_claims.py'
```

## Evidence

- 实现 PR 附相同 response 下三页面截图、projection data/count assertion 和静态 claim gate 输出。

## Status

- Ready — 等待设计批准；不引入硬编码模型数量。
