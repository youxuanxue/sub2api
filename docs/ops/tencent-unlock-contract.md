# 腾讯美西 · LUKS 解锁与挂载契约

状态：**切流已完成（2026-10-22）**；现网数据在 LUKS mapper 上。  
**禁止拿现网整机重启做解锁验收。**  
关联：[切流清单](tencent-cutover-checklist.md)、[候选矩阵](../../deploy/tencent/candidate-usw-20261022.json)、[舰队 SSOT](../../deploy/hetzner/README.md)。

## 硬门禁

- 禁止把 LUKS 密钥写回目标加密盘或未加密根盘做「自动开机」。
- 禁止未经批准的整机重启试验、解冻 Tokyo、双写。
- LUKS 自动解锁仍未验收；重启须人工交付解锁材料到 **tmpfs**。

## 异机密钥托管（已落盘）

解锁材料拆成两棵树，缺一不可：

| 部分 | 位置（操作员本机，不进 git） |
|---|---|
| 密封密文 | `~/.local/state/tokenkey-luks-offhost-vault/recovery-luks.key.enc` |
| 包装密钥 | `~/.local/share/tokenkey-custody/luks-wrap-20261022.key` |
| 回执 | `~/.local/state/tokenkey-tencent-usw-validation/p1-20261022/luks-offhost-vault-receipt.json` |

取钥（仅注入目标 **tmpfs**，故障恢复时）：

```bash
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
  -pass file:$HOME/.local/share/tokenkey-custody/luks-wrap-20261022.key \
  -in $HOME/.local/state/tokenkey-luks-offhost-vault/recovery-luks.key.enc \
  -out /path/to/tmpfs/luks.key
# 核验 sha256 与回执 plaintext_key_sha256 一致后再 cryptsetup open
```

明文指纹（可公开核对，不是密钥）：见回执 `plaintext_key_sha256`。

## 启动顺序（强制）

`unlock` → `mount` → `Docker` → `app`  
未解锁 / 未挂载时禁止启动业务容器。

## 现网挂载与布局（2026-10-22 布局窗后）

同一 LUKS mapper 同时挂在两条路径上。布局收敛走 **Docker Root 例外路径**（详见 `[deploy/tencent/stage0-layout-convergence.md](../../deploy/tencent/stage0-layout-convergence.md)`）。

| 项 | 实况 |
|---|---|
| mapper | `/dev/mapper/tokenkey_recovery` |
| 挂载 | `/var/lib/tokenkey` **与** `/srv/tokenkey-rehearsal`（同一 source） |
| Docker Root Dir | `/srv/tokenkey-rehearsal/docker`（**例外保留**；未改 daemon.json） |
| 业务 `.env` | `/var/lib/tokenkey/.env`（`formal/.env` 仍保留作历史/回滚参考） |
| 容器名 | `tokenkey`、`tokenkey-caddy`、`tk-cutover-prod-redis`、`tk-archive-standby` |
| 网络别名 | PG:`postgres`；Redis:`redis` / `tokenkey-redis` |

标准 `deploy-stage0` 的 `.env` 路径与 legacy `tokenkey` 容器前置已满足；首次蓝绿仍须按脚本迁移，并依赖上述别名。回执：`/var/lib/tokenkey/layout-staging/receipts/window-cutover-20261022T061053Z.json`。

布局工具（经 `run-probe.sh --target prod`）：`probe-tencent-layout-prep.sh`、`probe-tencent-layout-find-compose.sh`、`probe-tencent-layout-capture-recipe.sh`、`probe-tencent-layout-window-preflight.sh`、`apply-tencent-layout-window.sh`（仅 `APPLY=yes-layout-window-cutover`）。

## 解锁失败信号（只读探针）

目标机跑 `helpers/unlock-health-readonly.sh`（TAT）。关键字段：

- `severity`: `ok` | `warn` | `critical`
- `alerts`: `mapper_missing` / `luks_mount_missing_or_wrong_source` / `docker_root_not_on_luks` / `swap_present` / …

日常只跑只读探针；**不**用重启来验收。

## 只读核验（不重启）

```text
hostname == VM-0-16-ubuntu
findmnt SOURCE /var/lib/tokenkey == /dev/mapper/tokenkey_recovery
findmnt SOURCE /srv/tokenkey-rehearsal == /dev/mapper/tokenkey_recovery
docker info DockerRootDir 前缀 == /srv/tokenkey-rehearsal
test -f /var/lib/tokenkey/.env
docker inspect tokenkey >/dev/null
swapon 为空
```
