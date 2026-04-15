# Sub2API -> TokenKey 品牌替换方案（UI）

## 1. 目标与范围

目标：将用户可见品牌从 `Sub2API` 替换为 `TokenKey`。  
范围：标题、Logo/Favicon、核心品牌文案、UI 外链（不含业务逻辑改造）。

## 2. 现状摘要（仅保留关键点）

| 类别 | 当前状态 | 主要位置 |
| --- | --- | --- |
| 站点名 | 默认值为 `Sub2API` | `frontend/src/stores/app.ts`、`frontend/src/router/title.ts`、`frontend/src/views/admin/SettingsView.vue` |
| 标题后缀 | `AI API Gateway` | `frontend/index.html`、`frontend/src/main.ts`、`backend/internal/web/frontend_spa.go` |
| 副标题 | `Subscription to API Conversion Platform`（另有 `AI API Gateway Platform`） | `backend/internal/service/setting_service.go`、`frontend/src/components/layout/AuthLayout.vue`、`frontend/src/views/HomeView.vue` |
| 默认 Logo | 仅一套 `logo.png` | `frontend/public/logo.png`（被首页/登录/侧边栏复用） |
| Favicon | 当前复用 `/logo.png` | `frontend/index.html`、`frontend/src/App.vue` |
| UI 外链（sub2api） | 5 处引用 / 3 个唯一 URL | 见第 3.3 节 |

## 3. 待准备清单

### 3.1 资源清单（最小必需）

| 资源类别 | 建议文件名 | 原对照资源 | 规格建议 | 新品牌信息（待填写） | 备注 |
| --- | --- | --- | --- | --- | --- |
| 主 Logo（唯一） | `logo.png` | `frontend/public/logo.png` | 建议 512x512 PNG |  | 直接替换默认 Logo |
| Favicon | `favicon.ico` | `frontend/index.html` 当前使用 `/logo.png` | 至少含 16x16/32x32 |  | 建议改为独立 favicon |
| 品牌文案清单 | `tokenkey-brand-copy.md` | `frontend/src/i18n/locales/en.ts`、`frontend/src/i18n/locales/zh.ts`、`README.md` | 中英对照 |  | 按第 3.2 节逐项准备 |
| 品牌链接清单 | `tokenkey-links.md` | UI 中 5 处 `sub2api` 外链 | 官网/文档/GitHub/支持渠道 |  | 按第 3.3 节逐项准备 |

### 3.2 品牌文案清单（逐条准备）

| 文案项 | 当前文案（原对照） | 主要位置 | 新品牌信息（已确认） |
| --- | --- | --- | --- | --- |
| 品牌主名 | `Sub2API` | `frontend/src/stores/app.ts`、`frontend/src/router/title.ts`、`frontend/src/views/auth/RegisterView.vue`、`frontend/src/views/auth/EmailVerifyView.vue`、`frontend/src/views/HomeView.vue`、`frontend/src/views/KeyUsageView.vue`、`frontend/src/views/admin/SettingsView.vue` | `TokenKey` |
| 页面标题后缀 | `AI API Gateway` | `frontend/index.html`、`frontend/src/main.ts`、`backend/internal/web/frontend_spa.go` | `AI API Gateway`（保留不变） |
| 站点默认副标题（登录页链路） | `Subscription to API Conversion Platform` | `backend/internal/service/setting_service.go`、`frontend/src/components/layout/AuthLayout.vue`、`frontend/src/views/admin/SettingsView.vue`（默认值） | `AI API Gateway Platform` |
| 首页默认副标题 | `AI API Gateway Platform` | `frontend/src/views/HomeView.vue` | `AI API Gateway Platform`（保留一致） |
| 站点设置占位（英文） | `siteNamePlaceholder: Sub2API`、`siteSubtitlePlaceholder: Subscription to API Conversion Platform` | `frontend/src/i18n/locales/en.ts` | `siteNamePlaceholder: TokenKey`；`siteSubtitlePlaceholder: AI API Gateway Platform` |
| 站点设置占位（中文） | `siteNamePlaceholder: Sub2API`、`siteSubtitlePlaceholder: 订阅转 API 转换平台` | `frontend/src/i18n/locales/zh.ts` | `siteNamePlaceholder: TokenKey`；`siteSubtitlePlaceholder: AI API 网关平台` |
| Onboarding 欢迎标题（英/中） | `Welcome to Sub2API` / `欢迎使用 Sub2API` | `frontend/src/i18n/locales/en.ts`、`frontend/src/i18n/locales/zh.ts` | `Welcome to TokenKey` / `欢迎使用 TokenKey` |
| Onboarding 欢迎正文品牌表述（英/中） | 含 `Sub2API is ...` / `Sub2API 是一个...` | `frontend/src/i18n/locales/en.ts`、`frontend/src/i18n/locales/zh.ts` | `TokenKey is ...` / `TokenKey 是一个...`（其余正文结构保持不变） |

### 3.3 品牌链接清单（UI 范围）

统计：**5 处引用，3 个唯一 URL**。

| 当前 URL（原对照） | 引用次数 | 位置 | 可见范围 | 新品牌信息（待填写） |
| --- | --- | --- | --- |
| `https://github.com/Wei-Shaw/sub2api` | 3 | `frontend/src/components/layout/AppHeader.vue`、`frontend/src/views/HomeView.vue`、`frontend/src/views/KeyUsageView.vue` | 管理员可见（`AppHeader`）+ 公开可见（`HomeView`/`KeyUsageView`） | `https://github.com/youxuanxue/sub2api` |
| `https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md` | 1 | `frontend/src/views/admin/SettingsView.vue` | 仅管理员可见（管理设置页） | `https://raw.githubusercontent.com/youxuanxue/sub2api/refs/heads/main/docs/ADMIN_PAYMENT_INTEGRATION_API.md` |
| `https://tls.sub2api.org` | 1 | `frontend/src/components/admin/TLSFingerprintProfilesModal.vue` | 仅管理员可见（TLS 指纹模板弹窗） | `https://tls.tokenkey.ai` |

## 4. 执行顺序（简版）

1. 准备第 3 节清单（先填“新品牌信息”列）。  
2. 一次性替换标题/文案/Logo/Favicon/UI 外链。  
3. 回归验证并清理残留字符串。

## 5. 验收标准

- 默认品牌显示为 `TokenKey`（标题、站点名、副标题、Logo/Favicon）。
- 第 3.2 节文案项全部完成替换。
- 第 3.3 节外链全部替换到新品牌地址。
- 管理员自定义 `site_name/site_logo/site_subtitle` 后仍可覆盖默认值。

## 6. 快速排查命令

```bash
rg "Sub2API|sub2api|Subscription to API Conversion Platform|AI API Gateway" frontend backend deploy README*
```