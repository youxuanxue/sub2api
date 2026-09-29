# 客户测试：Prod Universal Key 生图（OpenAI + Gemini）

> **以 prod 实测为准**（证据时间：2026-09-29，`https://api.tokenkey.dev` + 测试用 universal key）。
> 下列比例、像素、错误码均来自当次实跑；与代码 allowlist / Quickstart 示例不一致处，以本表「实测」列为准。
>
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

**硬 `size` 对照（同 key）：**

| 请求 | 响应 `size` |
| --- | --- |
| `"size":"1024x1024"`（不带 aspect_ratio） | **1254x1254**（上游会改写，不是字面 1024） |
| `"size":"1536x1024"` | **1536x1024** |
| 同时 `"size":"1024x1024"` + `"aspect_ratio":"16:9"` | **1254x1254**（`size` 生效，比例软控不抢） |

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
