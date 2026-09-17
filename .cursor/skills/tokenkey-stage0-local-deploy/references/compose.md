## 5) 校验配置、拉依赖、启动

```bash
# 若未导出 REPO_ROOT / TOKENKEY_STAGE0_LOCAL_ROOT：见 SKILL.md「启动或日常复用」

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  config --quiet

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  pull

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  up -d
```

**本地镜像、`pull_policy: never`**：`docker compose … pull` 可能因 **tokenkey** 仅存在本机 tag 而失败（仍会去 registry 解析）。可改为只拉基础镜像：
**`docker compose … pull caddy postgres redis`**，再 **`up -d`**（`tokenkey` 使用本地 `TOKENKEY_IMAGE`）。

可选本地构建（父目录需含 `sub2api` + `new-api`）：

```bash
cd "${TOKENKEY_NEWAPI_PARENT}"
docker build -f sub2api/Dockerfile -t tokenkey-local:dev .
```

然后在 `.env` 中设置 `TOKENKEY_IMAGE=tokenkey-local:dev` 且 override 里 `pull_policy: never`。
