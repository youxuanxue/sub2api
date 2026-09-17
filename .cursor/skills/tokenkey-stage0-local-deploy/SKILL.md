---
name: tokenkey-stage0-local-deploy
description: >-
  Run the local Docker Stage0 stack for TokenKey. Use for Caddy/app/Postgres/Redis local deploy, compose setup, smoke verification, teardown, or matching deploy/aws behavior on a workstation.
---

# TokenKey：本地 Stage0 栈

本地 Docker Compose 验证，栈 owner 为 `deploy/aws/stage0/docker-compose.yml`，配置生成唯一入口 `deploy/aws/stage0/local-bootstrap.sh`。线上发布用 `tokenkey-stage0-release-rollout`；本流程不 bump/tag。

## 前置与状态边界

需要 Docker daemon、compose、宿主 curl；完整网关 smoke 另需 jq/python3 和全能 `TK_SMOKE_API_KEY`。私有 GHCR 先登录。默认本地 Caddy 为 HTTP `8088→80`，不申请 LE；`8443` 预留。明确版本对账用 tag，`latest` 不保证等于当前源码。

保持同一数据目录和 `.env`。已有 PG 数据时不能重随机密码；AUTO_SETUP 不重复创建管理员。停栈默认 down 保留 bind mount；只有明确要求清盘才 reset。密钥和本地数据不打印、不入 Git。

## 启动或日常复用

bootstrap 子进程不导出父 shell 变量；同一 shell 设置：

```bash
export REPO_ROOT="$(git rev-parse --show-toplevel)"
export TOKENKEY_STAGE0_LOCAL_ROOT="${TOKENKEY_STAGE0_LOCAL_ROOT:-$REPO_ROOT/.cache/tokenkey-stage0-local}"
export TOKENKEY_NEWAPI_PARENT="${TOKENKEY_NEWAPI_PARENT:-$(dirname "$REPO_ROOT")}"
bash deploy/aws/stage0/local-bootstrap.sh
```

bootstrap 保留已有 `.env`，按需重写 Caddyfile/override；`--dry-run` 可预览。非默认布局覆盖上述变量；本地 build 的上下文须含 sibling `sub2api/` 与 `new-api/`。

首次启动/镜像更新读 [compose 命令](references/compose.md)：config → pull → up → 等 healthy。日常已有配置可直接复用，镜像无变化不必 pull。下载/数据库初始化允许数分钟；`up -d` 返回不代表成功。

## 验证与交付

启动后按 [验证步骤](references/verification.md) 完成 A（经 Caddy）和 B（直达 app）；有 smoke key 再做 C，否则写明未验证。API smoke 不等于 UI e2e；触达 Web 行为用 Playwright 验证。

需要对比当前源码与上一 tag 时运行 `bash scripts/release-rollout-summary.sh --mode local`，结合实际运行镜像报告变更及未覆盖验证，不把源码 HEAD 当运行版本。

## 停栈与故障

停栈或授权清盘读 [teardown](references/teardown.md)，清盘前核对准确路径并先 down。GHCR/数据库认证/架构/Caddy 故障读 [故障处理](references/troubleshooting.md)。
