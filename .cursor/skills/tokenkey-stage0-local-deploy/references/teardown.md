## 7) 停栈 / 重置

### 7a) 停栈，**保留** Postgres / Redis / app 数据（默认）

释放端口与容器，**下次 `up -d` 数据仍在**：

```bash
# 若未导出：见上文

docker compose \
  -f "${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml" \
  -f "${TOKENKEY_STAGE0_LOCAL_ROOT}/docker-compose.override.yml" \
  --env-file "${TOKENKEY_STAGE0_LOCAL_ROOT}/.env" \
  down
```

**不要**在本机日常测试末尾自动加 **`rm -rf`**。本栈使用 **bind mount**，**`down` 默认不会删** `${TOKENKEY_STAGE0_LOCAL_ROOT}/postgres` 等目录（与「具名卷 + `down -v`」不同）。

### 7b) **有意清空**（删库 / 回到「全新栈」）

确认 **`TOKENKEY_STAGE0_LOCAL_ROOT`** 指向正确后：

1. 先按 **7a** `down`（避免删目录时容器仍占用文件）。
2. 用户明确要求清盘、路径核对无误后，调用 `bash deploy/aws/stage0/local-bootstrap.sh --reset` 清空并重建配置。新凭据与空数据目录一同生成，不手写第二套删目录/生成密钥流程。

若曾 `docker build` 本地标签且不再使用：`docker rmi tokenkey-local:dev`（替换成实际 tag）。
