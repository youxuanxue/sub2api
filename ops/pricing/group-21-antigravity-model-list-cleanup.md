# group 21（Google-Antigravity）公开模型清单清理 — 2026-10-23

## 为什么要有这份文档

`groups.models_list_config` 是**数据库配置**，不在 git 里。它没有提交历史、没有
code review、后台界面上也看不到「为什么是这些模型」。下一个人看到列表变短想加
回来时，没有任何线索。

本轮排查反复撞上同一个失效模式——依据只存在一处时，后来者会把它改回去：

- `gemini-3-flash-preview` 被 #2822 从 Vertex global 清单删掉（为迁合一个夹具
  选错的测试），而它官方只在 global 提供，删掉即 404 风险；
- `gemini-4-argon` 的代码注释写「2026-10-22 canary 404 后下架」，实测是 429
  限流、10-21 还有成功计费，注释误导了后续判断；
- `vertexModelUsesGlobalLocation` 那 5 个清单项全部不是 global-only，但没有
  任何地方记着依据。

所以这次把 DB 改动的依据连同代码 PR 一起留痕。

## 改了什么

group 21 的 `models_list_config.models`：**32 → 18 个 ID**，`enabled` 保持 `true`。

操作方式：事务内 `jsonb_set` 精确移除指定 ID（不重写整个数组），先在事务里验
数量再 `COMMIT`。复验：目标 ID 残留 0、6 个高流量模型全部保留、网关容器未重启。

改动前的完整原值（32 项，供回滚）：

```json
{"models": ["gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-flash-thinking", "gemini-2.5-pro", "gemini-2.5-flash-image", "gemini-2.5-flash-image-preview", "gemini-3-flash", "gemini-3-flash-agent", "gemini-3-flash-preview", "gemini-3-pro-high", "gemini-3-pro-low", "gemini-3-pro-preview", "gemini-3-pro-image", "gemini-3-pro-image-preview", "gemini-3.1-pro-high", "gemini-3.1-pro-low", "gemini-3.1-pro-preview", "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview", "gemini-3.5-flash", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low", "gemini-pro-agent", "tab_flash_lite_preview", "gemini-3.1-flash-lite", "gemini-3.1-pro", "gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.7-flash-high", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.8-flash"], "enabled": true}
```

改动后（18 项）：

```json
{"models": ["gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro", "gemini-2.5-flash-image", "gemini-3-flash", "gemini-3-flash-agent", "gemini-3-flash-preview", "gemini-3-pro-preview", "gemini-3-pro-image", "gemini-3-pro-image-preview", "gemini-3.1-pro-preview", "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview", "gemini-pro-agent", "gemini-3.1-flash-lite", "gemini-3.1-pro", "gemini-3.7-flash", "gemini-3.8-flash"], "enabled": true}
```

## 移除的 14 个 ID 及依据

依据是现网只读探测（`model_availability` 的 `last_seen_ok_at` + `usage_logs`
近 30 天流量）。

### 零流量且从未成功 / 半年未成功（9 个）

| ID | last_seen_ok_at | 30 天流量 | 备注 |
|---|---|---|---|
| `tab_flash_lite_preview` | 2026-06-15 | 0 | **在 `antigravityUnpricedModelMappingKeys` 里，命中会 $0 计费** —— 这条风险最高 |
| `gemini-3-pro-high` | 2026-06-15 | 0 | 已在 `antigravityStructuralDeadModelMappingKeys` 死名单 |
| `gemini-3-pro-low` | 2026-06-15 | 0 | 同上 |
| `gemini-3.1-pro-high` | **NULL** | 0 | 从未成功过；且 overlay 里**有定价行**（见下方风险） |
| `gemini-3.1-pro-low` | 2026-09-09 | 0 | unreachable |
| `gemini-3.5-flash` | **NULL** | 0 | 从未成功过 |
| `gemini-3.7-flash-low` | 无 availability 记录 | 0 | 连探测记录都没有 |
| `gemini-2.5-flash-image-preview` | 无记录 | 0 | — |
| `gemini-2.5-flash-thinking` | 2026-09-09 | 0 | unreachable |

### 档位 wire，本不该在客户清单里（4 个）

`gemini-3.7-flash-high`、`gemini-3.7-flash-medium`、`gemini-3.5-flash-low`、
`gemini-3.5-flash-extra-low`

按 #2207 的 Gemini-only 收敛意图，`-low/-medium/-high` 是上游 wire ID、不是公开
展示名，不该出现在 `/v1/models` 或定价菜单。

**这里有一个仍然存在的风险**：这些档位 wire 目前没有穿透到客户面，靠的是
「它们没有定价行」这个偶然护栏（`FilterClientFacing` 按 `IsModelPriced` 过滤）。
而 `gemini-3.1-pro-high` / `-low` 在 `tk_pricing_overlay.json` 里**是有定价行的**
—— 哪天有人给某个档位补了价，它会立刻出现在公开列表，且没有任何测试会红。
这条值得单独立护栏，不在本次范围。

### 已退役（1 个）

`gemini-3.6-flash` —— #2845 退役了代码侧（可服务白名单、Vertex 请求面、公开
目录、AG 档位 wire、前端清单共 9 处），但 group 21 的 DB 配置不随代码部署走，
需要单独清理。

## 保留的高流量模型（复验 6/6 在列）

`gemini-3.7-flash`（30 天 125,689）、`gemini-3.8-flash`（63,976）、
`gemini-3-flash-preview`（19,866）、`gemini-3-flash`（3,805）、
`gemini-3.1-flash-image`（104）、`gemini-2.5-flash`（10）。

## 对用户的影响

影响面是该分组下 9 个 API key 的可见模型列表。删掉后，发这些模型会从
「上游 404 / 429」变成「本地 403 model not found」——对调用方其实是更清晰、更快
的错误，但 `/v1/models` 列表会变短。

## 回滚

把上面「改动前的完整原值」整段写回 `groups.models_list_config` 即可（`id=21`）。
