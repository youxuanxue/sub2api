# 客户测试：Prod Universal Key 生图（OpenAI + Gemini）

面向客户 / 对接方的 **prod 验收清单**。以当前线上行为为准；像素与错误码来自
`https://api.tokenkey.dev` + universal fulltest key 实跑。

| 项 | 值 |
| --- | --- |
| 基线镜像 | `ghcr.io/youxuanxue/sub2api:1.8.267`（prod / edge-us3，2026-09-30） |
| 产品契约 | [Studio/Quickstart](../approved/image-generation-quickstart-studio.md)、[GPT soft AR](../approved/gpt-image-aspect-ratio-soft-control.md)、[精确画布](../approved/gpt-image-strict-canvas-postprocess.md) |
| 不覆盖 | `wan2.7-image*`、非 universal 直连账号调参、思考强度 |

```bash
export TK_BASE='https://api.tokenkey.dev'
export TK_KEY='sk-...'   # 客户的 prod universal key
```

先 `GET $TK_BASE/v1/models` 确认本 key 可见模型；下列 ID 以当次清单为准。

---

## 1. 当前契约（给对接方的一句话）

| 场景 | 期望 |
| --- | --- |
| GPT 显式 `size=WIDTHxHEIGHT` | 返回图 **字面像素**（网关本地 pad）。Studio/Quickstart GPT 芯片默认发这个。 |
| GPT 只发 `aspect_ratio`、不发 `size` | **软控**：比例大致对，像素可漂移（非字面保证）。 |
| GPT `size` + `aspect_ratio` 同传 | **以 size 为准**。 |
| GPT 显式 `output_format=jpeg\|webp\|png` | 字节魔数与声明一致。 |
| GPT `n=2..4`（非流式） | `data` 长度 = n；usage 为多张合计。 |
| GPT `stream=true` 且 `n>1` | **400** `unsupported_parameter`（本地拒写）。 |
| Gemini / `nano-*` | 走 **Chat**；比例在 `extra_body.google.image_config.aspect_ratio`。 |
| 错入口 | GPT→Chat **400**；Gemini→Images **404**。 |

---

## 2. 模型清单（当次 `/v1/models` 生图相关）

```text
gpt-image-2
gpt-image-2.5
gpt-image-2.5-flare
gpt-image-2.5-sunburst
gemini-3-pro-image
gemini-3.1-flash-image
gemini-3.1-flash-image-preview
nano-2
nano-pro
```

同 key 还可见 `wan2.7-image` / `wan2.7-image-pro`（本单不测）。

**不要默认测：** `gpt-image-1`、`imagen-*`（当前清单未出现）。

---

## 3. 入口

| 模型 | 正确入口 | 错误入口（实测） |
| --- | --- | --- |
| `gpt-image-*` | `POST /v1/images/generations` | Chat → **400** `This model is not supported on the Chat Completions endpoint` |
| Gemini 原生生图 / `nano-*` | `POST /v1/chat/completions`（`stream:false`） | Images → **404** `The images API is only available for OpenAI-compatible platform groups` |
| 同上（可选） | `POST /v1beta/models/{model}:generateContent` | `gemini-3.1-flash-image` + `aspectRatio=16:9` → **200**，约 `1376x768` |

Universal：**GPT → Images**；**Gemini → Chat**。

---

## 4. GPT Images（`/v1/images/generations`）

### 4.1 精确画布 `size`（默认验收路径）

显式 `size` 时网关本地 pad；**不要**再期待「1024→1254」这类旧行为。

| 请求 | HTTP | 出图像素（1.8.267） |
| --- | --- | --- |
| `size=1024x1024` | 200 | **1024×1024** |
| `size=1536x1024` | 200 | **1536×1024** |
| `size=1024x1536` | 200 | **1024×1536** |
| `size=1024x1024` + `aspect_ratio=16:9` | 200 | **1024×1024**（size 优先） |

超限画布（总像素过大等）→ **400**（网关准入，不等待上游）。

### 4.2 软控 `aspect_ratio`（不发 `size`）

允许值：`1:1` · `3:2` · `2:3` · `16:9` · `9:16`。非法（如 `4:3` / `21:9`）→ **400**。

| `aspect_ratio` | HTTP | 常见像素（可漂移，非契约） |
| --- | --- | --- |
| `1:1` | 200 | ~1254×1254 |
| `3:2` | 200 | ~1536×1024 |
| `2:3` | 200 | ~1024×1536 |
| `16:9` | 200 | ~1672×941 |
| `9:16` | 200 | ~941×1672 |

### 4.3 其它 GPT 参数

| 参数 | 行为 |
| --- | --- |
| `model` | `gpt-image-2` / `2.5` / `2.5-flare` / `2.5-sunburst` 均可 |
| `prompt` | 必填 |
| `quality` | `low` / `high` 等可传；与显式 size 联用时画布仍跟 size |
| `output_format` | 显式 `jpeg` / `webp` / `png` → 魔数匹配并回写字段 |
| `n` | 1–4；非流式多取合并 `data[]`；usage 为多张合计 |
| `stream` | `n=1` 可流式；**`stream`+`n>1` → 400** |

响应：`data[].b64_json`（未见 `url`）；可含 `model` / `generation_id`。验像素请解码 b64（`sips` / `identify`），勿只信字段文案。

---

## 5. Gemini Chat（`/v1/chat/completions`）

比例字段：`extra_body.google.image_config.aspect_ratio`（字符串）。

上游允许集（14 个）：

```text
1:1, 1:4, 1:8, 2:3, 3:2, 3:4, 4:1, 4:3, 4:5, 5:4, 8:1, 9:16, 16:9, 21:9
```

非法例：`2:1`、`5:3`、`3:1` → **400**。

**约 1K 档实测像素（`gemini-3.1-flash-image` 同族）：**

| aspect_ratio | 像素 | aspect_ratio | 像素 |
| --- | --- | --- | --- |
| `1:1` | 1024×1024 | `16:9` | 1376×768 |
| `2:3` | 848×1264 | `9:16` | 768×1376 |
| `3:2` | 1264×848 | `21:9` | 1584×672 |
| `3:4` | 896×1200 | `4:3` | 1200×896 |
| `4:5` | 928×1152 | `5:4` | 1152×928 |
| `1:4` | 512×2064 | `4:1` | 2064×512 |
| `1:8` | 352×2928 | `8:1` | 2928×352 |

- 图在 `choices[0].message.content` 的 `data:image/...;base64,...` markdown 中。
- 无图不应再收到 **200 + `content=null`**（应为可重试错误，常见 502）。
- `image_size`（`1K`/`2K`/`4K`）经 `extra_body.google.image_config.image_size` 透传（Antigravity/一般 Gemini）；Web 供给可能拒该字段。P0 曾验：`1K`→1024、`2K`→2048（`1:1`）。
- `nano-2` / `nano-pro` / preview 别名：`1:1` 可 200，响应 `model` 回显请求名。

---

## 6. 可直接跑的用例

### 6.1 GPT：精确画布

```bash
curl -sS "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "A simple red apple on a white table, product photo",
    "size": "1024x1024",
    "n": 1
  }' | jq '{n:(.data|length), model:.data[0].model, has_b64:(.data[0].b64_json!=null)}'
# 期望：n=1、has_b64=true；解码后像素 1024x1024
```

### 6.2 GPT：`n=2` 与 `stream+n>1`

```bash
# 多图
curl -sS "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"tiny red apple","size":"1024x1024","n":2}' \
  | jq '{http_hint:"expect 200", n:(.data|length), in:.usage.input_tokens, out:.usage.output_tokens}'
# 期望：n=2；两张均为 1024x1024；usage 为合计

# 非法组合
curl -sS -o /tmp/tk-stream-n2.json -w '%{http_code}\n' "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"tiny red apple","size":"1024x1024","n":2,"stream":true}'
# 期望：400；error.code=unsupported_parameter；文案含 stream=true with n>1
```

### 6.3 GPT：软比例 / 负向

```bash
curl -sS "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"tiny red apple","aspect_ratio":"16:9","n":1}' \
  | jq '{has_b64:(.data[0].b64_json!=null)}'
# 期望：200；像素常见约 1672x941（非字面保证）

# 负向
# "aspect_ratio":"4:3" → 400
```

### 6.4 GPT：format

```bash
curl -sS "$TK_BASE/v1/images/generations" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"tiny red apple","size":"1024x1024","output_format":"jpeg","n":1}' \
  | jq -r '.data[0].b64_json' | base64 -d | xxd | head -1
# 期望：JPEG 魔数 ff d8 ff；像素 1024x1024
```

### 6.5 Gemini Chat

```bash
curl -sS "$TK_BASE/v1/chat/completions" \
  -H "Authorization: Bearer $TK_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image",
    "stream": false,
    "messages": [{"role":"user","content":"Draw a simple blue square icon on white background"}],
    "extra_body": {"google": {"image_config": {"aspect_ratio": "16:9"}}}
  }' | jq '{model, has_image: ((.choices[0].message.content // "") | contains("data:image"))}'
# 期望：has_image=true；无图应是可重试错误，不是 200 空包
```

P0 比例抽样：`1:1`、`16:9`、`9:16`、`4:3`、`3:4`、`21:9`。完整 14 个见 §5。

### 6.6（可选）Gemini native

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

## 7. 验收记录（可勾选）

基线：prod `1.8.267`。括号内为当次实跑参考。

| # | 用例 | 期望 | 结果 |
| --- | --- | --- | --- |
| 1 | GPT `size=1024x1024` | 200，像素 1024×1024 | ☐（267：PASS） |
| 2 | GPT `output_format=jpeg` + size | 200，JPEG 魔数，1024×1024 | ☐（267：PASS） |
| 3 | GPT `output_format=webp` + size | 200，WEBP 魔数，1024×1024 | ☐（267：PASS） |
| 4 | GPT `n=2` + size | 200，`data` 长度 2，两张 1024×1024 | ☐（267：PASS，usage 曾见 in=125 out=1430） |
| 5 | GPT `stream`+`n=2` | 400 `unsupported_parameter` | ☐（267：PASS） |
| 6 | GPT soft `aspect_ratio=16:9`（无 size） | 200，约 1672×941 | ☐（267：PASS） |
| 7 | GPT 非法 AR `4:3` | 400 | ☐（267：PASS） |
| 8 | GPT→Chat | 400 | ☐ |
| 9 | `gpt-image-2.5-flare` + size | 200，1024×1024 | ☐ |
| 10 | Gemini Chat `16:9` | 200 + 出图，约 1376×768 | ☐ |
| 11 | Gemini Chat `21:9`（pro） | 200，约 1584×672 | ☐ |
| 12 | `nano-2` Chat `1:1` | 200，约 1024×1024 | ☐ |
| 13 | Gemini→Images | 404 | ☐ |
| 14 | Gemini `image_size=2K`（可选） | `1:1` 约 2048×2048 | ☐ |

---

## 8. UI 对照

登录 prod → 选 universal Key → **接入指南 / Studio·图片**：

- GPT：芯片发顶层 **`size=WxH`**（与精确画布一致）。
- Gemini：`extra_body.google.image_config.aspect_ratio`。
- 「验证密钥」只验鉴权，**不代替**生图。

---

## 9. 变更摘要（为何与旧客户文档不同）

| 版本 | 客户可见变化 |
| --- | --- |
| ≤1.8.265 | 显式 `size=1024x1024` 常落到 ~1254×1254；`n>1` 曾不可靠 / 空 200 等问题已陆续修 |
| **1.8.266** | 显式 `size` → **字面像素**；format coerce 稳定 |
| **1.8.267** | OAuth `n>1` 多取合并；`stream+n>1` **本地 400**（不再出现 `tools[0].n` 上游拒） |

历史逐版探针明细若需审计，见 git 历史中本文件 §「发版后复测」旧段；**日常对接只以本文 §1–§8 为准**。
