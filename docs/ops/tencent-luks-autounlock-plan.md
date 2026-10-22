# 腾讯美西 prod · LUKS 自动解锁验收方案

状态：**方案，未执行**。契约与硬门禁仍以 [`tencent-unlock-contract.md`](tencent-unlock-contract.md) 为准：
禁止拿现网整机重启做验收、禁止把密钥写回加密盘或未加密根盘。

## 1. 现网只读事实（2026-10-22，`run-probe.sh --target prod`）

| 项 | 实况 | 对方案的影响 |
|---|---|---|
| 加密载体 | `/dev/loop0` → **根盘文件** `/var/lib/tokenkey-validation/recovery.luks`（80G），LUKS2 aes-xts-512，keyslot 仅 0，无 token | 不是独立块设备；解锁必须在 root 挂载之后由 systemd 服务完成，initramfs/crypttab 方案不适用 |
| crypttab / fstab | 均无条目 | 开机后 mapper 不会自动出现，现在完全靠人工 |
| TPM | 无 `/dev/tpm*`；BIOS 固件；Tencent Cloud CVM（Lighthouse） | **TPM2 / systemd-cryptenroll --tpm2 不可行** |
| 工具 | `systemd-cryptenroll` 255、`cryptsetup-initramfs` 已装；`clevis`/`tang` 未装 | clevis+tang 需新装包 |
| Docker 依赖 | `docker.service` `RequiresMountsFor=/var/lib/tokenkey` + `ExecStartPre=mountpoint -q /srv/tokenkey-rehearsal` | 已保证「未挂载不起业务」，自动解锁只需在 docker 前完成 unlock+mount |
| 根盘 | 120G，已用 76%（28G 可用） | 演练实例需同规格或缩小载体文件 |
| 只读健康 | `ops/observability/probe-tencent-unlock-health.sh`（操作员 helper 入仓副本）2026-10-22 13:32 UTC：`severity=ok`、`alerts=[]` | 演练与推广的验收探针 |

## 2. 候选方案

密钥不能落在本机磁盘，因此「自动」=「开机时从异机取回密钥到 tmpfs」。

| 方案 | 机制 | 优点 | 风险/成本 | 结论 |
|---|---|---|---|---|
| A. TPM2 封装 | `systemd-cryptenroll --tpm2-device` | 无网络依赖 | 无 TPM | **不可行** |
| B. clevis + tang | 新增 keyslot 绑 tang 服务器；开机 `clevis luks unlock` | 业界标准，tang 不持有密钥 | 需另起 tang 主机（可放 `usw-eval*` 私网，或 cn/欧洲异地），tang 与 prod 同时失陷即失效；需装包 | 可行，推荐候选 |
| C. 云 KMS / Secrets 拉取 | 开机用 SSM Hybrid 身份从 AWS（Secrets Manager / SSM SecureString + KMS）取第二把 keyfile 到 `/run` | 复用现有 Hybrid 身份 `tokenkey-lightsail-ssm-hybrid`，审计在 CloudTrail，无新主机 | Hybrid 角色凭据在根盘（`/var/lib/amazon/ssm`），根盘被整盘拷走即可取钥；需新增 IAM 授权 | 可行，需 IAM 审批 |
| D. 维持人工 | 现状：操作员从本机 vault 解密到 tmpfs | 零新增攻击面 | 重启即停服直到人工介入 | 兜底，始终保留 |

方案 B/C 都**新增一个 keyslot**，不替换现有 slot 0 与异机 vault；人工路径 D 始终可用。

建议：**C 优先演练**（不需新主机，改动集中在一个 systemd unit + 一条 IAM 授权），B 作为不依赖 AWS 的备选。

## 3. 验收设计（全部在临时实例，不动现网）

1. **临时实例** **[审批：预算]**：同地域 `na-siliconvalley-2` 锐驰 2C2G/40G（月价 40 CNY，按需或 1 个月预付），Ubuntu 24.04、systemd 255，与 prod 同 BIOS/无 TPM 形态。注册独立 SSM Hybrid 激活（不复用 prod 的 `mi-*`）。
2. 构造同形载体：根盘文件 `/var/lib/tokenkey-validation/recovery.luks`（缩为 2G）+ loop + LUKS2，slot 0 用演练专用口令（不复用 prod 密钥）。
3. 方案 C 实现：
   - `tokenkey-luks-unlock.service`（`Before=docker.service`、`After=network-online.target amazon-ssm-agent.service`，`Type=oneshot`，`RemainAfterExit=yes`）：losetup → 从 AWS 取 keyfile 到 `/run/tokenkey-luks/`（tmpfs，0600）→ `cryptsetup open --key-file` → 立即 shred keyfile → mount 两路径。
   - 失败语义：重试 N 次后 fail，不启动 docker（沿用 `RequiresMountsFor`），发飞书告警，等待人工 D 路径。
   - 演练 secret 与 IAM 授权只针对演练实例 `mi-*`，不授予 prod。
4. 验收用例（每项记录到 `docs/ops/reports/tencent-luks-autounlock-drill-<date>.json`）：
   - 冷启动 / `reboot` / 控制台强制重启各 3 次：mapper 出现、挂载正确、docker 启动、keyfile 在 `/run` 已清除、根盘无明文 keyfile（`grep` 指纹/`find` 新文件）。
   - 断网开机：unlock 失败、docker 不启动、告警发出；恢复网络后手工 `systemctl start tokenkey-luks-unlock` 成功。
   - 撤销 IAM / 删除 secret：开机失败且不起业务；人工 D 路径仍可解锁。
   - 计时：开机到 `/health` 可用的时长。
5. 演练结束 **[审批：删除]**：删除临时实例、Hybrid 激活、演练 secret/KMS key 授权。

## 4. 推广到现网（演练通过后另批）

1. **[审批：IAM]** 为 prod Hybrid 角色新增只读取单一 secret 的授权（KMS key policy 限定该 `mi-080ed76dc6d97d8b5`）。
2. **[审批：现网变更，无重启]** 在线 `cryptsetup luksAddKey` 新增 slot 1（需现有密钥，经 tmpfs 交付），安装 unit 但只 `enable`、不重启；跑 unit 的 `--dry-run`（只取钥并校验 `cryptsetup open --test-passphrase`）验证取钥链路。
3. 真正的现网重启验收只在用户批准的维护窗执行，人工 D 路径材料提前就位。
4. 更新 `tencent-unlock-contract.md`「自动解锁未验收」状态与回执。

## 5. 需要批准的事项

| 项 | 量级 | 可逆性 |
|---|---|---|
| 临时 Lighthouse 2C2G 演练机 | ~40 CNY（1 个月）；演练约 1 天 | 删除即止 |
| AWS Secrets Manager / KMS（演练 + 现网） | 约 1-2 USD/月 | 可删除 |
| 新 IAM 授权（演练 `mi-*`，后续 prod `mi-*`） | — | 可撤销 |
| prod 新增 LUKS keyslot | — | `luksKillSlot 1` 可撤销 |
| 现网维护窗重启验收 | 预计停服数分钟 | 有人工兜底 |
