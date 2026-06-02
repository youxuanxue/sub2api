# TokenKey 媒体生成示例（图片 / 视频）

通过 TokenKey 的 OpenAI 兼容网关生成图片（Imagen）和视频（Veo）。Gemini 媒体模型在网关后端经
Google **Vertex AI** 计费/出图，对调用方完全透明——你只需要一个绑定到 Gemini 媒体分组的 API key。

`generate_media.py` 仅依赖 Python 标准库，无需 `pip install`。

## 快速开始

```bash
export TOKENKEY_API_KEY=sk-...           # 绑定到 Gemini 媒体分组的 key
# export TOKENKEY_BASE_URL=https://api.tokenkey.dev   # 默认值

python3 generate_media.py image "a red apple on a wooden table"
python3 generate_media.py video "a golden retriever puppy running on a beach at sunset"
```

产物写到当前目录：`out_image_*.png` / `out_video_*.mp4`。

## 模型名

| 用途 | 模型 id | 备注 |
| --- | --- | --- |
| 图片 | `imagen-4.0-fast-generate-001` | 也支持 `imagen-4.0-generate-001`、`imagen-4.0-ultra-generate-001` |
| 视频 | `veo-3.1-generate-001` | **不是** `-preview`；见下方"为什么是这个名字" |

## 接口形态

### 图片（同步，OpenAI 兼容）

```
POST /v1/images/generations
{ "model": "imagen-4.0-fast-generate-001", "prompt": "...", "n": 1 }

-> 200 { "data": [ { "b64_json": "<base64 png>", "url": "", "revised_prompt": "..." } ] }
```

OpenAI 标准形态，**Cherry Studio 等客户端可直接用**（填 base_url + key + 上面的模型 id）。

### 视频（异步，原始 Vertex LRO）

```
POST /v1/video/generations
{ "model": "veo-3.1-generate-001", "prompt": "...", "duration_seconds": 8, "aspect_ratio": "16:9" }

-> 200 { "id": "vt_...", "task_id": "vt_...", "status": "queued", ... }
```

随后轮询：

```
GET /v1/video/generations/{task_id}

# 进行中（原始 Vertex operation 对象，无 done 字段）：
-> 200 { "name": "projects/.../operations/..." }

# 完成：
-> 200 { "name": "...", "done": true,
         "response": { "videos": [ { "bytesBase64Encoded": "<base64 mp4>", "mimeType": "video/mp4" } ] } }

# 完成后记录被清理（终态只下发一次，再轮询返回 404）：
-> 404 { "error": { "message": "video task not found or expired" } }
```

**注意：**

- 视频响应是**原始 Vertex 长任务（LRO）形态，不是 OpenAI 视频形态**。Cherry Studio 之类的 UI
  通常不支持这种异步轮询，**视频建议直接走 API 调用**（如本脚本）。
- 终态（`done: true`）只在那次轮询下发一次，随后记录删除。轮询间隔别设太大，否则可能错过终态
  直接拿到 404。Veo 通常 1–2 分钟出片，脚本默认 10s 轮询。

## 为什么视频名必须是 `veo-3.1-generate-001`

视频提交路径不会应用账号级 `model_mapping`，client 传入的模型名会被原样转发给 Vertex，因此必须是
Vertex 真实存在的模型名。Vertex 上没有 `veo-3.1-generate-preview`（那是 AI Studio 的命名），用它会
返回 404 `Publisher Model not found`。图片路径同名透传，`imagen-4.0-*` 在两边一致。

## 背景

Gemini 媒体为何走 Vertex 而非 AI Studio：AI Studio（generativelanguage，API key）在部分区域是
**预付费（prepay）钱包**计费，与 Cloud billing 额度是两个独立的池子，Cloud 额度喂不进 prepay
钱包，会一律返回 `prepayment credits are depleted`。Vertex（Service Account JWT）走标准 Cloud
计费，可正常使用 Cloud 额度。网关侧已配置为 Vertex 直连，调用方无需关心这些。
