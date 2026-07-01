# Google Cloud $300 试用账号配置 · 极简操作指南

> 给运营：把一个 Google Cloud $300 免费试用账号配成 TokenKey 的 **Vertex AI 媒体账号**，让试用额度经 imagen / veo 消耗。
> 前置：网关已含 PR #488（Vertex ch41 桥接 + imagen/veo 计费）；端到端示例见 PR #500（`docs/examples/media-generation/`）。

## 为什么必须走 Vertex（重要）

Google 有两个 Gemini 入口，**只有一个吃试用额度**：

| 入口 | 是否消耗 $300 试用额度 | TokenKey 里怎么配 |
| --- | --- | --- |
| Gemini Developer API（AI Studio，prepay） | ❌ **不吃**（实测 imagen/文本均报 prepay 耗尽） | 不要用 |
| **Vertex AI**（Service Account，走 Cloud Billing） | ✅ **吃** | 扩展引擎（newapi）+ Vertex AI 渠道 + Service Account |

所以要花掉试用额度，账号**必须**配成 Vertex Service Account，不能用 AI Studio 的 API key。

---

## 第一步：在 Google Cloud 侧准备

> 在 <https://console.cloud.google.com> 操作。如果你的 Google 账号属于某个**组织**且组织禁止「创建服务账号密钥」（org policy `iam.disableServiceAccountKeyCreation`），请改用**个人 Google 账号 / 不在该组织下的项目**来建，否则第 5 步下不了 key。

1. **开通试用**：首页点「免费试用 / Start free」，按引导绑卡（试用期内不扣费），获得 $300 / 90 天额度。
2. **建项目**：顶部项目选择器 → 新建项目（例 `tk-vertex-trial-01`）。
3. **确认计费已绑**：菜单 → 「结算 / Billing」→ 确认该项目关联到试用结算账号。
4. **启用 Vertex AI API**：菜单 → 「API 和服务 → 库」→ 搜 **Vertex AI API**（`aiplatform.googleapis.com`）→ 启用。
5. **建服务账号 + 给权限**：菜单 → 「IAM 和管理 → 服务账号」→ 创建服务账号 → 角色选 **Vertex AI User**（`roles/aiplatform.user`）→ 完成。
6. **下载 JSON 密钥**：进入刚建的服务账号 → 「密钥 / Keys」→ 添加密钥 → 创建新密钥 → **JSON** → 下载。

> 下载的这个 JSON 就是凭证（含私钥，等于账号密码）：只在 TokenKey 后台上传，不发群、不存公共目录、不进 git。

---

## 第二步：在 TokenKey 后台建账号

后台 → **账号管理 → 新建账号**：

### Imagen / Veo（推荐，走 google-vertex 分组）

1. 平台选 **扩展引擎（newapi）**。
2. **渠道类型**选 **Vertex AI**（channel_type 41）。
3. 在 **Service Account JSON** 区：**拖入 JSON 文件、选择文件，或粘贴完整 JSON** —— 会自动填出 **Project ID** 和 **Client Email**。
4. **model_mapping**：至少声明要服务的模型（如 `imagen-4.0-fast-generate-001`、`veo-3.1-generate-001`）。
5. **Location** 选 `us-central1`（imagen / veo 都可用；不确定就用它）。
6. 保存。

### Gemini / Anthropic 原生 Vertex（文本 / Claude on Vertex）

1. 平台选 **Gemini** 或 **Anthropic**。
2. 账号类别选 **Vertex（service_account）**。
3. 同样通过 **上传 / 拖放 / 粘贴 JSON** 填入凭证。
4. **Location** 选 `us-central1`（或业务需要的 region）。
5. 保存。

---

## 第三步：挂到 Gemini 媒体分组

1. 把账号挂到对应的 **Gemini 媒体分组**（newapi 路径通常挂 **google-vertex** 组）。
2. 该分组要打开 **允许图片生成**（`allow_image_generation`）——否则 imagen/veo 请求会被拒。
3. 用一个**绑定到该分组的 API key** 对外提供调用。

---

## 第四步：验证（确认额度真的在扣）

用 PR #500 的示例脚本，绑定到上面分组的 key 跑一次：

```bash
export TOKENKEY_API_KEY=sk-...    # 绑定到 Gemini 媒体分组的 key
python3 docs/examples/media-generation/generate_media.py image "a red apple on a wooden table"
python3 docs/examples/media-generation/generate_media.py video "a puppy running on a beach"
```

- 图片应产出 `out_image_*.png`，视频应产出 `out_video_*.mp4`。
- 回 Google Cloud →「结算 → 报告」看 Vertex AI 用量与试用额度在减少，即说明额度真的在被消耗。

---

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 建密钥时被拒 / 灰掉 | 组织禁了 SA 密钥创建——换个人账号 / 独立项目重建（见第一步提示） |
| 请求报额度耗尽但 Cloud 还有钱 | 配成了 AI Studio prepay（ch24）而非 Vertex——确认走的是 Vertex Service Account |
| imagen/veo 被拒 | 分组没开「允许图片生成」，或账号没挂进该分组，或 newapi 账号缺 model_mapping |
| Vertex 报 project / location 错误 | JSON 没传全（缺 project_id）或 Location 选了 imagen/veo 不支持的区，回退 `us-central1` |

> 计费说明：imagen 按**每张图**计费、veo 按**每秒**计费（PR #488 已落地）。$300 试用额度耗尽或满 90 天后该账号即停，换一个新试用账号重走本指南即可。

---

> 维护者备注：凭证字段契约见 `backend/internal/service/vertex_service_account.go`
> （`service_account_json` / `location`，project_id 由 JSON 自解析）；Admin UI SSOT 见
> `frontend/src/composables/useVertexServiceAccountFields.ts` +
> `frontend/src/components/account/VertexServiceAccountFields.vue`（Create / Edit 共用）；
> newapi ch41 创建见 `CreateAccountModal.vue`；分组出图门禁见 `group_service.go` 的
> `AllowImageGeneration`。字段或门禁变动时同步更新此文档。
