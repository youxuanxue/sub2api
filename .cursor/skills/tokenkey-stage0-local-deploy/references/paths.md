## 本项目路径约定（本仓库克隆）

以下值为 **TokenKey fork 当前常用落地布局**（`sub2api` 与 `new-api` 同级）；若你的目录不同，只改这三项即可。

```bash
export REPO_ROOT="$HOME/Codes/token/tk/sub2api"
export TOKENKEY_NEWAPI_PARENT="$HOME/Codes/token/tk"   # Dockerfile 构建上下文：内含 sub2api/ 与 new-api/
export TOKENKEY_STAGE0_LOCAL_ROOT="$HOME/Codes/token/tk/sub2api/.cache/tokenkey-stage0-local"
```

下文凡出现路径均使用 `"$HOME/Codes/..."`，在脚本与非交互 shell 中也会可靠展开。

- `REPO_ROOT`：本 git 仓库根（含 `backend/`、`deploy/aws/stage0/docker-compose.yml`）。
- `TOKENKEY_NEWAPI_PARENT`：`Dockerfile` 要求的 **父目录**（与 `CLAUDE.md`「Sibling dependency: New API」一致）。
- `TOKENKEY_STAGE0_LOCAL_ROOT`：本地 override、`.env`、Caddyfile、PG/Redis 数据落盘处；位于 `REPO_ROOT/.cache/...`，仓库根 `.gitignore` 已忽略 `.cache/`。

为方便「单节复制粘贴」，**§2 / §5 / §7 / 部分自检**可能在代码块里重复写出 `export`。若你已在 **`本项目路径约定`** 或 **§1** 导出过 `REPO_ROOT` 与 `TOKENKEY_STAGE0_LOCAL_ROOT`，可跳过这些重复行。**§4 写 override 时仍须在当前 shell `export TOKENKEY_STAGE0_LOCAL_ROOT`**，否则 `${TOKENKEY_STAGE0_LOCAL_ROOT}` 在 YAML 挂载路径中会为空。

默认 **GHCR 镜像坐标** 使用 **`:latest`**，避免与 `VERSION` 文件不同步导致拉取失败；需要与某次发版逐位对照时再显式改为 `ghcr.io/youxuanxue/sub2api:<VERSION>` 或 `sha-…`：

```bash
export TOKENKEY_IMAGE_DEFAULT="ghcr.io/youxuanxue/sub2api:latest"
```

`latest` 会随 registry 更新而变，**不一定**等于当前工作区 `backend/cmd/server/VERSION`。发版/对账请用明确 tag。拉取 private 仓库前先 `docker login ghcr.io`。

本地镜像使用 `docker build -t <tag>` 的同一 tag 设置 `.env` 的 `TOKENKEY_IMAGE`，并在 override 的 `services.tokenkey` 设置 `pull_policy: never`，避免 compose 用 GHCR 覆盖本地构建。其余模板由 `local-bootstrap.sh` 生成。
