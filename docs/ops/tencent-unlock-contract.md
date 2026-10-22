# 腾讯美西 · LUKS 解锁与正式挂载契约

状态：准备中（**未授权切流**；未做未经批准的整机重启）。  
关联：[切流清单](tencent-cutover-checklist.md)、[候选矩阵](../../deploy/tencent/candidate-usw-20261022.json)。

## 硬门禁

- 禁止把 LUKS 密钥写回目标加密盘或未加密根盘做「自动开机」。
- 禁止未经批准的整机重启试验、promote、DNS、Tokyo 停写。
- `cutover_authorized` 保持 false，直至人工批准窗口。

## 异机密钥托管（已落盘）

解锁材料拆成两棵树，缺一不可：

| 部分 | 位置（操作员本机，不进 git） |
|---|---|
| 密封密文 | `~/.local/state/tokenkey-luks-offhost-vault/recovery-luks.key.enc` |
| 包装密钥 | `~/.local/share/tokenkey-custody/luks-wrap-20261022.key` |
| 回执 | `~/.local/state/tokenkey-tencent-usw-validation/p1-20261022/luks-offhost-vault-receipt.json` |

取钥（仅注入目标 **tmpfs**，窗口/故障恢复时）：

```bash
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 \
  -pass file:$HOME/.local/share/tokenkey-custody/luks-wrap-20261022.key \
  -in $HOME/.local/state/tokenkey-luks-offhost-vault/recovery-luks.key.enc \
  -out /path/to/tmpfs/luks.key
# 核验 sha256 与回执 plaintext_key_sha256 一致后再 cryptsetup open
```

明文指纹（可公开核对，不是密钥）：见回执 `plaintext_key_sha256`。

## 启动顺序（强制）

`unlock` → `mount` → `Docker` → `standby/app`  
未解锁 / 未挂载时禁止启动业务容器。

## 解锁失败信号（只读探针）

目标机跑 `helpers/unlock-health-readonly.sh`（TAT）。关键字段：

- `severity`: `ok` | `warn` | `critical`
- `alerts`: `mapper_missing` / `luks_mount_missing_or_wrong_source` / `docker_root_not_on_luks` / `swap_present` / …

日常准备只跑只读探针；**不**用重启来验收。

## 正式挂载（窗口内）

意图：`/dev/mapper/tokenkey_recovery` → `/var/lib/tokenkey`。  
窗口外只跑 `helpers/formal-remount-plan.sh`（默认 dry-run）。  
真正 remount 需要批准窗口 + `CUTOVER_REMOUNT=1` + `/run/tokenkey-cutover.authorized`；准备阶段执行器故意拒动。

## 只读核验（不重启）

```text
hostname == VM-0-16-ubuntu
findmnt SOURCE /srv/tokenkey-rehearsal == /dev/mapper/tokenkey_recovery
docker info DockerRootDir 前缀 == /srv/tokenkey-rehearsal
swapon 为空
unlock-health-readonly → severity=ok
```
