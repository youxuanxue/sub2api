# 客户测试：Prod Universal Key 生图（OpenAI + Gemini）

> **以 prod 实测为准**（证据：2026-09-29 修复前基线 + 1.8.265 / **1.8.266 精确画布** / **1.8.267 n>1 修复** 复测，`https://api.tokenkey.dev` + universal fulltest key；prod / edge-us3 运行镜像 `ghcr.io/youxuanxue/sub2api:1.8.267`）。
> 下列比例、像素、错误码均来自当次实跑；与代码 allowlist / Quickstart 示例不一致处，以本表「实测」列为准。
>
> **1.8.266+ 契约：** 显式 `size=WxH` 时网关 pad 到字面像素。仅发 `aspect_ratio`、不发 `size` 时仍为软控。**1.8.267：** OAuth `n>1` 由 TokenKey 多取合并；`stream+n>1` 本地 400。

> 对齐产品契约（入口与字段拼写）：
> [`image-generation-quickstart-studio.md`](../approved/image-generation-quickstart-studio.md)、
> [`gpt-image-aspect-ratio-soft-control.md`](../approved/gpt-image-aspect-ratio-soft-control.md)。

```bash
export TK_BASE='https://api.tokenkey.dev'
export TK_KEY='sk-...'   # 客户的 prod universal key
```

---

## 0. 当次 `/v1/models` 可见的生图相关 ID

```text
gpt-image-2
gpt-image-2.5
gpt-image-2.5-flare
gpt-image-2.5-sunburst
gemini-3-pro-image
gemini-3-pro-image-preview
gemini-3.1-flash-image
gemini-3.1-flash-image-preview
nano-2
nano-pro
```

（同 key 还列出 `wan2.7-image*`，本单不覆盖。）

**未出现：** `gpt-image-1`、`imagen-*`。不要按旧矩阵默认测这些。

---

## 1. 入口（实测错误码）

| 模型 | 正确入口 | 错误入口（实测） |
| --- | --- | --- |
| `gpt-image-*` | `POST /v1/images/generations` | 走 Chat → **400** `This model is not supported on the Chat Completions endpoint` |
| Gemini 原生生图 / `nano-*` | `POST /v1/chat/completions` | 走 Images → **404** `The images API is only available for OpenAI-compatible platform groups` |
| 同上（可选） | `POST /v1beta/models/{model}:generateContent` | 实测 `gemini-3.1-flash-image` + `aspectRatio=16:9` → **200**，像素 `1376x768` |

Universal 客户优先：**GPT → Images**；**Gemini → Chat**（与 Studio/Quickstart 一致）。

---

## 2. Aspect-ratio（重点 · 实测）

### 2.1 OpenAI `gpt-image-*`：顶层 `aspect_ratio`（TokenKey 软控）

**网关允许值（非法即 400）：**

`1:1` · `3:2` · `2:3` · `16:9` · `9:16`

| 请求 `aspect_ratio` | HTTP | 响应 `data[0].size`（实测） |
| --- | --- | --- |
| `1:1` | 200 | **1254x1254** |
| `3:2` | 200 | **1536x1024** |
| `2:3` | 200 | **1024x1536** |
| `16:9` | 200 | **1672x941** |
| `9:16` | 200 | **941x1672** |
| `4:3` / `21:9` | **400** | `unsupported aspect_ratio "..."; supported values: 1:1, 3:2, 2:3, 16:9, 9:16` |

**硬 `size` 对照（同 key；1.8.266 起显式 size 为精确画布）：**

| 请求 | 响应像素（1.8.266 实测） | 备注 |
| --- | --- | --- |
| `"size":"1024x1024"`（不带 aspect_ratio） | **1024×1024** | 1.8.265 及以前常为 1254×1254 |
| `"size":"1536x1024"` | **1536×1024** | |
| `"size":"1024x1536"` | **1024×1536** | |
| 同时 `"size":"1024x1024"` + `"aspect_ratio":"16:9"` | **1024×1024** | `size` 生效，比例软控不抢 |

**建议客户：** Studio/Quickstart 默认只发 `aspect_ratio`、不发 `size`。要固定像素时只发官方 `size`，不要双发。

**响应形态（当次）：** `data[]` 项含 `b64_json`、`generation_id`、`model`、`size`；未见到 `url`。

### 2.2 Gemini 原生生图：`extra_body.google.image_config.aspect_ratio`

上游 400 原文给出的 **完整允许集（14 个）**：

```text
1:1, 1:4, 1:8, 2:3, 3:2, 3:4, 4:1, 4:3, 4:5, 5:4, 8:1, 9:16, 16:9, 21:9
```

非法例（实测 400）：`2:1`、`5:3`、`3:1`。

**Chat 路径实测像素（默认约 1K 档，`gemini-3.1-flash-image` / 同族）：**

| aspect_ratio | 实测像素 |
| --- | --- |
| `1:1` | 1024×1024 |
| `2:3` | 848×1264 |
| `3:2` | 1264×848 |
| `3:4` | 896×1200 |
| `4:3` | 1200×896 |
| `4:5` | 928×1152 |
| `5:4` | 1152×928 |
| `9:16` | 768×1376 |
| `16:9` | 1376×768 |
| `21:9` | 1584×672 |
| `1:4` | 512×2064 |
| `4:1` | 2064×512 |
| `1:8` | 352×2928 |
| `8:1` | 2928×352 |

`gemini-3-pro-image` 抽样：`1:1` / `16:9` / `9:16` / `21:9` / `4:3` / `3:4` 均 200 且像素与上表同档一致。  
别名 `nano-2`、`nano-pro`、`gemini-3.1-flash-image-preview` 在 `1:1` 均 200（响应 `model` 回显请求名）。

**Chat 返回：** 图片在 `choices[0].message.content` 的 `![image](data:image/...;base64,...)` markdown 里（不是 Images 的 `data[]`）。

**偶发空响应：** 旧行为会 **HTTP 200 + `content=null`**。修复后图片模型无图会走 **502 / 同账号可重试**，客户端不应再收到成功空包。

**`image_size`（`1K`/`2K`/`4K`）经 Chat `extra_body`：** 网关会透传到上游 `generationConfig.imageConfig.imageSize`（Antigravity/一般 Gemini）。Gemini Web 供给不接受该字段（能力投影不会给出）。验收时以实跑像素为准。

---

## 3. 其它参数（实测摘要）

### 3.1 GPT Images

| 参数 | 实测 |
| --- | --- |
| `model` | `gpt-image-2` / `gpt-image-2.5` / `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 均 200 |
| `prompt` | 必填 |
| `aspect_ratio` | 见 §2.1 |
| `size` | 见 §2.1；`1024x1024` 会被改写成 1254×1254 |
| `quality` | `low` / `high` 均 200（当次像素仍 1254×1254） |
| `output_format=jpeg` | 网关在显式请求时会把上游 PNG 重编码为 JPEG，并回写 `output_format` |
| `n=2` | Codex Direct 路径由网关按张补齐；期望 `data` 长度为 2 |

### 3.2 Gemini Chat

| 参数 | 实测 |
| --- | --- |
| `model` | 见 §0；Pro / Flash / nano 别名可用 |
| `messages` + `stream:false` | 生图应用非流式 |
| `extra_body.google.image_config.aspect_ratio` | 见 §2.2（字符串） |
| 张数 | Chat 路径单次通常 1 张图 |

---

## 4. 可直接跑的用例

### 4.1 OpenAI

```bash
curl -sS "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "A simple red apple on a white table, product photo",
    "aspect_ratio": "16:9",
    "n": 1
  }' | jq '{size: .data[0].size, model: .data[0].model, has_b64: (.data[0].b64_json != null)}'
# 期望：size == "1672x941"
```

负向：`"aspect_ratio":"4:3"` → 400。

### 4.2 Gemini Chat

```bash
curl -sS "$TK_BASE/v1/chat/completions" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image",
    "stream": false,
    "messages": [{"role":"user","content":"Draw a simple blue square icon on white background"}],
    "extra_body": {"google": {"image_config": {"aspect_ratio": "16:9"}}}
  }' | jq '{model, ct: .usage.completion_tokens, has_image: ((.choices[0].message.content // "") | contains("data:image"))}'
# 期望：has_image=true；若网关已部署空包修复，无图应返回可重试错误而非 200 空包
```

建议矩阵：§2.2 十四个比例各一发；P0 至少 `1:1`、`16:9`、`9:16`、`4:3`、`3:4`、`21:9`。

### 4.3（可选）Native

```bash
curl -sS "$TK_BASE/v1beta/models/gemini-3.1-flash-image:generateContent" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "contents":[{"role":"user","parts":[{"text":"tiny blue square"}]}],
    "generationConfig":{
      "responseModalities":["TEXT","IMAGE"],
      "imageConfig":{"aspectRatio":"16:9"}
    }
  }'
```

---

## 5. 验收记录（可直接填）

| # | 模型 | 入口 | 比例/size | HTTP | 出图像素 | 备注 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | gpt-image-2 | images | aspect_ratio=16:9 | 200 | 1672×941 | 基线 |
| 2 | gpt-image-2 | images | aspect_ratio=4:3 | 400 | — | 负向 |
| 3 | gpt-image-2.5-flare | images | aspect_ratio=1:1 | 200 | 1254×1254 | |
| 4 | gemini-3.1-flash-image | chat | 16:9 | 200 | 1376×768 | 空包则重试 |
| 5 | gemini-3-pro-image | chat | 21:9 | 200 | 1584×672 | |
| 6 | nano-2 | chat | 1:1 | 200 | 1024×1024 | 别名 |
| 7 | gemini… | images | — | 404 | — | 错入口 |
| 8 | gpt-image-2 | chat | — | 400 | — | 错入口 |

---

## 6. 实测纠偏与网关修复（2026-09-29）

| 问题 | 根因 | 修复 |
| --- | --- | --- |
| Gemini 偶发 HTTP 200 + `content=null` | 图片模型无图仍写成成功 Chat 包 | 对 `IsImageModel` 强制要求至少一张内联图；否则 **502 + 同账号可重试 failover**（不再 200 空包） |
| GPT `n=2` 只回 1 张 | Codex Direct `/images/generations` 忽略 `n` | `n>1` 时按张数串行补请求并合并 `data[]`（上限 4） |
| `output_format=jpeg` 仍 PNG | 上游常忽略 format，响应魔数仍是 PNG | 客户端显式 `output_format=jpeg/png` 时网关按字节重编码，并回写 `output_format` |
| Chat `image_size` 不生效 | 只 lift 了 `aspect_ratio` | Chat `extra_body.google.image_config.image_size` 与 native `imageConfig.imageSize` 一并透传到 cloudcode-pa（Web 供给仍拒 `image_size`，属协议收窄） |

其它对照（仍属上游行为，不是 bug）：

1. **GPT `1:1` / `size=1024x1024` 常出 1254×1254**  
2. **GPT soft `16:9`/`9:16` → 1672×941 / 941×1672**  
3. **Gemini 比例全集 14 个**（含 `1:4`/`1:8`/`4:1`/`8:1`）  
4. 模型清单以 **`GET /v1/models`** 为准

---

## 7. UI 对照

登录 prod → 选 universal Key → **接入指南 / Studio·图片**：比例芯片与请求字段应与上表一致（GPT 顶层 `aspect_ratio`；Gemini `extra_body.google.image_config.aspect_ratio`）。「验证密钥」只验鉴权，不代替生图。


## 7. 1.8.265 发版后复测（2026-09-29 UTC）

线上：`tokenkey-blue` = `ghcr.io/youxuanxue/sub2api:1.8.265`（healthy）。Stage0 Deploy workflow 在 join 步骤曾报 Failed，但主机镜像已切到 1.8.265。

| 契约项 | 请求 | 结果 | 判定 |
| --- | --- | --- | --- |
| GPT `n=2` | `/v1/images/generations` gpt-image-2 | HTTP 200，`data` 长度 **2**，两张均为 1254×1254 PNG；usage 合并 input=46 output=4116 | **PASS**（修复前为 1） |
| GPT `output_format=jpeg` | 同上 n=1 | HTTP 200，声明 `output_format=jpeg`，魔数 **JPEG** 1254×1254 | **PASS**（修复前落盘仍 PNG） |
| Gemini `image_size` | Chat `gemini-3.1-flash-image` 1:1 | `1K`→**1024×1024**；`2K`→**2048×2048** | **PASS**（修复前 Chat 透传失效，两侧都约 1024） |
| Gemini 空包 | Chat 16:9 连发 3 次 | 3/3 出图（1376×768 / 1376×768 / 2752×1536），**未**再现 200+content=null | **样本 PASS**（真空 fail-closed 未在窗口内触发，不构成反证） |
| Codex Direct `stream+n>1` | n=2 stream=true | HTTP **200 + content-length:0**（应 400 JSON） | **FAIL**（service 已返回 `OpenAIImagesUpstreamError`，但未 `writeOpenAIImagesUpstreamErrorResponse`，handler 直接 return 留下空 200） |

原始摘要：`/tmp/tk-img-265-probe/summary.json`。

## 8. 1.8.266 发版后复测（2026-09-30 UTC）

线上：prod `tokenkey-blue` = `1.8.266`；edge-us3 `tokenkey-blue` = `1.8.266`。Universal fulltest key → `https://api.tokenkey.dev`。

| 契约项 | 请求 | 结果 | 判定 |
| --- | --- | --- | --- |
| 精确 `size=1024x1024` | gpt-image-2 | HTTP 200，像素 **1024×1024** PNG | **PASS**（相对 265 的 1254 为契约升级） |
| 精确 `1536x1024` / `1024x1536` | 同上 | 字面像素 | **PASS** |
| `output_format=jpeg` + size | 同上 | 魔数 JPEG，1024×1024 | **PASS** |
| `output_format=webp` + size | 同上 | 魔数 WEBP，1024×1024 | **PASS** |
| soft `aspect_ratio=16:9`（无 size） | 同上 | 1672×941 | **PASS**（仍软） |
| soft `aspect_ratio=1:1`（无 size） | 同上 | 1254×1254 | **PASS**（仍软） |
| 非法 `aspect_ratio=4:3` | 同上 | HTTP **400** | **PASS** |
| size 优先于 AR | size=1024x1024 + AR=16:9 | 1024×1024 | **PASS** |
| `gpt-image-2.5-flare/sunburst` + size | jpeg/png | 1024×1024 | **PASS** |
| Gemini Chat `image_size=2K` | gemini-3.1-flash-image | 200 + 出图 | **PASS** |
| GPT 错入口 Chat | gpt-image-2 | HTTP **400** | **PASS** |
| GPT `n=2` | size=1024x1024 | HTTP **400** `Unknown parameter: 'tools[0].n'` | **FAIL**（Responses 误传 `tools[].n`；上游拒） |
| `stream=true` + `n=2` | 同上 | 同上 400（非 TK 本地拒写） | **FAIL**（应本地 400 JSON；被 tools.n 抢先） |
| edge-us3 fidelity 01–06/08/10 | gpt125 直打 Responses+本地 PP | **8/8 PASS**（pad/format 与网关契约一致） | **PASS**（上游旁路探针） |

原始摘要：`/tmp/tk-img-266-probe/console.log`、`edge-us3-fidelity.json`。

**跟进（已合入 #2390，1.8.267 验证）：** `n>1` TokenKey 多取；`stream+n>1` 本地拒写。

## 9. 1.8.267 发版后复测（2026-09-30 UTC）

线上：prod = `1.8.267`；edge-us3 `tokenkey-green` = `1.8.267`。Universal fulltest key → `https://api.tokenkey.dev`。

| 契约项 | 请求 | 结果 | 判定 |
| --- | --- | --- | --- |
| GPT `n=2` + `size=1024x1024` | gpt-image-2 | HTTP **200**，`data` 长度 **2**，两张均为 **1024×1024** PNG；usage 合并 input=125 output=1430 | **PASS**（相对 266 的 tools[].n 400） |
| `stream=true` + `n=2` | 同上 | HTTP **400** JSON，`unsupported_parameter`，文案含 `stream=true with n>1` | **PASS**（本地拒写，未打上游） |
| 精确 `size=1024x1024` | n=1 | 1024×1024 PNG | **PASS**（回归） |
| `output_format=jpeg` + size | n=1 | 魔数 JPEG，1024×1024 | **PASS** |
| `output_format=webp` + size | n=1 | 魔数 WEBP，1024×1024 | **PASS** |
| soft `aspect_ratio=16:9` | 无 size | 1672×941 | **PASS** |
| 非法 `aspect_ratio=4:3` | — | HTTP **400** | **PASS** |

原始摘要：`/tmp/tk-img-267-probe/console.log`。

