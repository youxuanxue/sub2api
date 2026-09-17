## 故障速查

| 现象 | 处理 |
| --- | --- |
| Agent / 工具 **`docker compose pull` 或 `up` 超时** | 拉长超时或与用户说明网络慢；可拆成先 `pull` 再 `up`；未完成不要当失败退出。 |
| `docker compose ps` 长期 non-healthy | 看 **`docker logs tokenkey`** / **`docker logs tokenkey-postgres`** / **`docker logs tokenkey-redis`**；等资源初始化或修 `.env` 密钥。 |
| **Postgres 起不来 / `password authentication failed`** | 多为 **§2 重写了 `POSTGRES_PASSWORD`** 但 **`postgres/` 数据目录仍是旧库**：恢复与旧库一致的 `.env`，或 **§7b** 删 `postgres/`（或整 `${TOKENKEY_STAGE0_LOCAL_ROOT}`）后重建。 |
| `exec format error` | 镜像架构与主机不一致；Apple Silicon 拉取 **arm64** 或 **`docker build --platform linux/arm64`**。 |
| Caddy **503**、应用容器已起 | **`docker logs tokenkey-caddy`**；本节 **B** 直连 `localhost:8080`；核对 Caddyfile 站点是否为 **`:80`**。 |
| GHCR pull **403** | `docker login ghcr.io`；PAT 权限与镜像 owner；Agent 若在沙箱无登录态须在用户 shell 登录。 |
| **`tk_post_deploy_smoke.sh` 报缺 KEY** | 正常：新栈尚无用户 API key。**只做 A+B** 或先做管理员登录，再在后台发证后重跑 **C**。 |
| **全量 `pull` 在 tokenkey 上失败（仅本地 tag）** | `.env` 用本地 build 且 override `pull_policy: never`：按上文改为 **`pull caddy postgres redis`** 后 **`up -d`**。 |
