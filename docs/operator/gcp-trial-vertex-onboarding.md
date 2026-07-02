# Google Cloud $300 试用 · Vertex 媒体账号配置指南

> **给运营**：把 Google Cloud 免费试用（$300 / 90 天）配成 TokenKey 上可跑 **Imagen / Veo** 的 Vertex 账号，让额度经 Vertex 计费消耗。
> 网关前置：PR #488（newapi ch41 Vertex 桥接 + imagen/veo 计费）；端到端脚本见 `docs/examples/media-generation/`（PR #500）。

---

## 先读这条：为什么必须走 Vertex

Google 有两套 Gemini 入口，**只有 Vertex 吃 Cloud 试用结算**：

| 入口 | 是否消耗 $300 试用 | TokenKey 配法 |
| --- | --- | --- |
| Gemini Developer API（AI Studio，prepay） | ❌ 不吃（imagen/文本易报 prepay 耗尽） | **不要用于试用额度** |
| **Vertex AI**（Service Account + Cloud Billing） | ✅ 吃 | **扩展引擎（newapi）+ Vertex AI 渠道** |

Imagen / Veo **必须**走下面「推荐路径」；不要用 AI Studio API key（newapi ch24 等 prepay 渠道）。

---

## 一、Google Cloud 侧（一次性）

在 [Google Cloud Console](https://console.cloud.google.com) 完成：

1. **开通试用**：Start free → 绑卡（试用期内不扣费）→ 获得 $300 / 90 天。
2. **新建项目**（例 `tk-vertex-trial-01`）。
3. **绑定结算**：Billing → 确认项目挂在试用结算账号上。
4. **启用 API**：API 和服务 → 库 → **Vertex AI API**（`aiplatform.googleapis.com`）→ 启用。
5. **服务账号**：IAM → 服务账号 → 创建 → 角色 **Vertex AI User**（`roles/aiplatform.user`）。
6. **下载 JSON 密钥**：该服务账号 → 密钥 → 添加密钥 → JSON → 下载。

> **组织策略**：若提示禁止创建服务账号密钥（`iam.disableServiceAccountKeyCreation`），换**个人 Google 账号**或**组织外独立项目**重建。
>
> JSON 含私钥，等同密码：只在 TokenKey Admin 粘贴/上传，不进 git、不发群。

---

## 二、TokenKey 新建账号（推荐：扩展引擎 + Vertex AI 渠道）

**Admin → 账号管理 → 新建账号**，按顺序操作：

> **易错点**：不要选 **Gemini → Vertex / Service Account**。那是 Gemini 原生 Vertex 文本账号，平台会保存为 `platform = gemini`、`channel_type = 0`，不属于本指南的 Imagen / Veo 媒体桥。试用额度媒体账号必须先选 **扩展引擎（newapi）**，再在下方 **渠道类型** 选 **Vertex AI**。

### 1. 选平台与渠道

| 步骤 | 选项 |
| --- | --- |
| 平台 | **扩展引擎（newapi）** |
| 渠道类型 | **Vertex AI**（`channel_type = 41`） |

选 Vertex AI 后：

- **不会出现** Base URL / API Key 输入框（ch41 走 Service Account，不是 apikey 中继）。
- 下方出现 **Service Account JSON** 区域（拖放 / 选择文件 / 文本框粘贴）。
- 页面顶部会提示 **Google Cloud 试用额度 / Imagen / Veo 必须走扩展引擎 + Vertex AI（ch41）**；如果看到的是 Gemini 平台的 **Vertex / Service Account** 卡，说明走错入口。

### 2. 填写账号名与可服务模型

- **账号名称**：例 `vertex-trial-01`。
- **可服务模型**：选择 **Vertex AI（channel_type 41）** 后，Admin 会**自动**从 `GET /api/v1/admin/channel-type-models` 读取 TokenKey Vertex 实测可服务清单并填入模型白名单（chat + Imagen + Veo，与 `supportedGeminiCatalogModels` 同源）。运营**无需手打**模型 ID；若要裁剪，在自动填充后取消勾选即可。
- 也可点 **「获取模型列表」** 重新拉取同一预设（ch41 不走上游 GET /v1/models，无需 Base URL / API Key）。

> 清单随 TokenKey 实测 allowlist 演进；勿填 `gemini-2.0-flash`、`gemini-3.x` 等未收录 ID。inventory 见 `docs/all-platform-model-inventory.md` §2.3。

### 3. 粘贴 Service Account JSON

在 **Service Account JSON** 区任选一种方式：

1. **拖放** JSON 文件到虚线框；
2. 点 **选择文件** 上传；
3. 在下方 **文本框** 粘贴完整 JSON。

成功后只读区会自动出现：

- **Project ID**（来自 JSON 的 `project_id`）
- **Client Email**（来自 `client_email`）

私钥不会展示在界面上；提交时整段 JSON 写入 `credentials.service_account_json`。

### 4. 选 Location

- 默认 **`us-central1`**（Imagen / Veo 通用；不确定就用它）。
- 下拉可选其它 Vertex region；选错 region 可能导致 upstream 报 location 错误。

### 5. 保存

提交后账号形态：

- `platform = newapi`
- `type = service_account`
- `channel_type = 41`
- `credentials` 含 `service_account_json`、`project_id`、`client_email`、`location`、`model_mapping` 等

---

## 三、编辑已有 Vertex 账号（write-once）

**Admin → 账号管理 → 编辑** 同一套 **VertexServiceAccountFields** UI：

| 场景 | 操作 |
| --- | --- |
| 只改 Location / 可服务模型 / 并发等 | **JSON 文本框留空** → 服务端保留已有脱敏密钥，不会清空 SA |
| 轮换 SA（换新 JSON 密钥） | 粘贴或上传**新** JSON → 提交前会校验 `project_id` / `client_email` / `private_key` |
| 后端已脱敏（响应无 `service_account_json`） | 只要 `credentials_status.has_service_account_json = true`，且界面仍有 Project ID / Client Email，可空 JSON 保存 |

Gemini / Anthropic 平台下的 **Vertex（service_account）** 类别编辑逻辑相同（同一组件）。

---

## 四、挂分组与对外 Key

1. 将账号加入 **google-vertex**（或对应 Gemini）**分组**。
2. **仅跑 chat** 时：分组不必开「允许图片生成」。
3. **跑 Imagen / Veo** 时：分组须开启 **允许图片生成**（`allow_image_generation`），否则 imagen/veo 会被拒。
4. 用**绑定该分组**的用户 API Key 对外调用。

---

## 五、验证额度在扣

```bash
export TOKENKEY_API_KEY=sk-...   # 绑定上述分组的 key

# Chat（账号须已声明 gemini-2.5-flash）
curl -sS https://api.tokenkey.dev/v1/chat/completions \
  -H "Authorization: Bearer $TOKENKEY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Say hi in one word."}],"max_tokens":16}'

# 图片 / 视频（账号须已声明对应 ID；分组须 allow_image_generation）
python3 docs/examples/media-generation/generate_media.py image "a red apple on a wooden table"
python3 docs/examples/media-generation/generate_media.py video "a puppy running on a beach"
```

- Chat 应返回 200 与 `choices[0].message.content`。
- 媒体应产出 `out_image_*.png` / `out_video_*.mp4`。
- GCP Console → Billing → Reports：Vertex AI 用量上升、试用额度下降即成功。

---

## 六、备选路径：Gemini / Anthropic 原生 Vertex

若希望 **平台维度就是 Gemini**（或 **Anthropic on Vertex**），而不是 newapi ch41：

1. 平台选 **Gemini** 或 **Anthropic**。
2. 账号类别选 **Vertex（service_account）**。
3. 同样通过 **上传 / 拖放 / 粘贴 JSON** 填凭证，选 Location，保存。

- **文本 chat**：原生 Gemini 平台即可；模型 ID 仍用上节 chat 三档（`gemini-2.5-flash` 等）。
- **Imagen / Veo 媒体**：仍优先 **第二节 newapi + ch41**（专用媒体桥 + 计费路径更清晰）。
- **Google Cloud $300 试用媒体账号**：不要用本节 Gemini 原生 Vertex；它会创建 `platform=gemini` 账号，无法进入 `google-vertex` newapi 媒体池。
- 两条路径共用 JSON 粘贴组件；**ch41 账号**另需在模型限制里声明第二节列出的 ID。

---

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 无法下载 SA JSON | 组织禁密钥 → 换个人账号/独立项目 |
| 额度耗尽但 GCP 还有试用金 | 配成了 AI Studio prepay，不是 Vertex SA |
| 保存时提示配置 model_mapping | 自动填充失败或手动清空了全部模型 → 至少保留一个，或点「获取模型列表」 |
| chat 404 / model not found | 账号未声明该 chat ID，或用了 `gemini-3.x` 等未实测 ID |
| imagen/veo 403 / 分组拒 | 分组未开「允许图片生成」或账号未进该分组 |
| project / location 报错 | JSON 不完整或 Location 不支持该模型 → 试 `us-central1` |
| 编辑时不想换密钥 | JSON 框留空，只改 Location 或模型列表 |
| 视频 404 Publisher Model not found | 模型名用了 `-preview` 等 AI Studio 命名 → 改用 `veo-3.1-generate-001` |

---

## 维护者索引

| 主题 | 代码锚点 |
| --- | --- |
| SA JSON 解析 | `frontend/src/utils/vertexServiceAccount.ts` |
| Create/Edit 表单 SSOT | `useVertexServiceAccountFields.ts` + `VertexServiceAccountFields.vue` |
| newapi ch41 创建 | `CreateAccountModal.vue`（`newapiIsVertexServiceAccount`） |
| ch41 model_mapping 门禁 | `useTkAccountNewApiPlatform.buildAuxiliaryCredentials` |
| 后端契约 | `backend/internal/service/vertex_service_account.go` |
| 分组出图门禁 | `group_service.go` → `AllowImageGeneration` |

Admin UI 或 ch41 提交语义变更时，同步更新本文第二节、第三节。
