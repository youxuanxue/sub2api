# Spec delta: CORS SPA derive for apex→api Quickstart

## Background

`tokenkey.dev` / `callmodel.io` 工具接入页从浏览器直连 `api.*` 做连接测试。现网宿主 `config.yaml` 若留下空的 `cors.allowed_origins`，OPTIONS preflight 返回 403，浏览器显示 `Failed to fetch` / CORS error。Stage0 滚动发版只换镜像、不改宿主配置，因此仅靠示例 yaml 无法经滚动修复。

## Delta

### ADDED

- 启动加载时 `resolveCORSAllowedOriginsTK`：
  - 非空 `CORS_ALLOWED_ORIGINS` env（逗号分隔）优先
  - 否则保留非空 yaml 白名单
  - 否则从 `SERVER_FRONTEND_URL` / `SITE_DOMAIN` / `GLOBAL_SITE_DOMAIN` 推导 HTTPS SPA origin，并为 tokenkey.dev ↔ callmodel.io 补双门面 companion
- Stage0 compose / bootstrap / example 露出 CORS 与 site domain env；`gateway-tk` sentinel 锚定 derive 注入点

### MODIFIED

- `deploy/config.example.yaml` 示例白名单补 `https://callmodel.io`

### REMOVED

- 无。不开放 `*`；不改网关鉴权与业务转发路径。

## Scenarios

| 类型 | 场景 | 期望 |
|---|---|---|
| 正向 | 空 yaml + `SERVER_FRONTEND_URL=https://tokenkey.dev` | allowlist 含 tokenkey.dev 与 callmodel.io；OPTIONS `/v1/models` 对 Origin `https://tokenkey.dev` → 204 |
| 正向 | 显式 yaml 已有 origin | 不覆盖、不加 companion |
| 负向 | 空 yaml 且无 FrontendURL / site domain | 白名单仍空，跨域继续拒绝 |
| 回归 | Compose 传入空 `CORS_ALLOWED_ORIGINS=` | 不钉死 deny，仍可走 yaml/derive |

## Validation

- `go test -tags=unit ./internal/config/ -run 'CORS|OriginFrom'`
- 滚动发版后：`tokenkey.dev/quickstart` 重试连接；Network 中 OPTIONS `/v1/models` 带 `Access-Control-Allow-Origin: https://tokenkey.dev`
